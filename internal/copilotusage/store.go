package copilotusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

type Event struct {
	At           time.Time `json:"at"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
	Failed       bool      `json:"failed"`
	CostNanos    *int64    `json:"cost_nanos,omitempty"`
}

type ModelSummary struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AICredits    float64 `json:"ai_credits"`
	Unpriced     int64   `json:"unpriced_requests"`
	costNanos    int64
}

type Day struct {
	Date    string         `json:"date"`
	ByModel []ModelSummary `json:"by_model"`
}

type Summary struct {
	Start   time.Time      `json:"start"`
	End     time.Time      `json:"end"`
	ByModel []ModelSummary `json:"by_model"`
	Days    []Day          `json:"days"`
}

type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) HandleUsage(_ context.Context, record usage.Record) {
	if s == nil || record.Provider != copilot.Provider || !usage.GenerateEnabled(record.Generate) {
		return
	}
	model := strings.TrimSpace(record.ResponseModel)
	if model == "" {
		model = strings.TrimSpace(record.Model)
	}
	if model == "" {
		return
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = time.Now()
	}
	event := Event{
		At: at.UTC(), Model: model, InputTokens: record.Detail.InputTokens,
		OutputTokens: record.Detail.OutputTokens, TotalTokens: record.Detail.TotalTokens,
		Failed: record.Failed,
	}
	event.CostNanos = estimatedCost(model, record.Detail)
	if err := s.append(event); err != nil {
		log.WithError(err).Error("github-copilot: failed to save usage")
	}
}

func (s *Store) append(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create usage directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open usage history: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Error("github-copilot: failed to close usage history")
		}
	}()
	if err := json.NewEncoder(file).Encode(event); err != nil {
		return fmt.Errorf("write usage history: %w", err)
	}
	return nil
}

func (s *Store) Summary(start, end time.Time) (Summary, error) {
	result := Summary{Start: start.UTC(), End: end.UTC(), ByModel: []ModelSummary{}, Days: []Day{}}
	if s == nil || !start.Before(end) {
		return result, fmt.Errorf("usage range must have start before end")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("open usage history: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Error("github-copilot: failed to close usage history")
		}
	}()
	models := make(map[string]ModelSummary)
	days := make(map[string]map[string]ModelSummary)
	decoder := json.NewDecoder(file)
	for {
		var event Event
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return result, fmt.Errorf("read usage history: %w", err)
		}
		if event.At.Before(start) || !event.At.Before(end) {
			continue
		}
		model := models[event.Model]
		models[event.Model] = add(model, event)
		date := event.At.UTC().Format("2006-01-02")
		if days[date] == nil {
			days[date] = make(map[string]ModelSummary)
		}
		days[date][event.Model] = add(days[date][event.Model], event)
	}
	result.ByModel = sortedModels(models)
	for date, dayModels := range days {
		result.Days = append(result.Days, Day{Date: date, ByModel: sortedModels(dayModels)})
	}
	sort.Slice(result.Days, func(i, j int) bool { return result.Days[i].Date < result.Days[j].Date })
	return result, nil
}

func add(model ModelSummary, event Event) ModelSummary {
	model.Model = event.Model
	model.Requests++
	model.InputTokens += event.InputTokens
	model.OutputTokens += event.OutputTokens
	model.TotalTokens += event.TotalTokens
	if event.CostNanos == nil {
		model.Unpriced++
	} else {
		model.costNanos += *event.CostNanos
		model.CostUSD = float64(model.costNanos) / 1_000_000_000
		model.AICredits = float64(model.costNanos) / 10_000_000
	}
	return model
}

func sortedModels(models map[string]ModelSummary) []ModelSummary {
	result := make([]ModelSummary, 0, len(models))
	for _, model := range models {
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Model < result[j].Model })
	return result
}
