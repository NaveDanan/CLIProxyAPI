package copilotusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/html"
)

const pricingURL = "https://docs.github.com/en/copilot/reference/copilot-billing/models-and-pricing"

type PricingStatus struct {
	UpdatedAt time.Time `json:"updated_at"`
	Models    int       `json:"models"`
	Error     string    `json:"error,omitempty"`
}

type catalogData struct {
	UpdatedAt time.Time       `json:"updated_at"`
	Rates     map[string]rate `json:"rates"`
}

type pricingCatalog struct {
	mu        sync.RWMutex
	refreshMu sync.Mutex
	path      string
	data      catalogData
	lastError string
}

func newPricingCatalog(path string) *pricingCatalog {
	c := &pricingCatalog{path: path}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &c.data); err == nil {
			err = validateRates(c.data.Rates)
		}
	}
	if err != nil && !os.IsNotExist(err) {
		c.data = catalogData{}
		log.WithError(err).Warn("github-copilot: could not load cached pricing")
	}
	return c
}

func (c *pricingCatalog) snapshot() (map[string]rate, PricingStatus) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.Rates, PricingStatus{UpdatedAt: c.data.UpdatedAt, Models: len(c.data.Rates), Error: c.lastError}
}

// RefreshPricing replaces the catalog only after a complete fetch, parse and save.
// Failed refreshes leave the last successful catalog available for calculations.
func (s *Store) RefreshPricing(ctx context.Context) error {
	return s.pricing.refresh(ctx, http.DefaultClient, pricingURL)
}

func (c *pricingCatalog) refresh(ctx context.Context, client *http.Client, source string) (err error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.lastError = ""
		if err != nil {
			c.lastError = "Could not update prices from GitHub."
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return fmt.Errorf("create pricing request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch Copilot pricing: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("github-copilot: close pricing response")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch Copilot pricing: HTTP %d", resp.StatusCode)
	}
	rates, err := parsePricing(resp.Body)
	if err != nil {
		return err
	}
	data := catalogData{UpdatedAt: time.Now().UTC(), Rates: rates}
	if err = c.save(data); err != nil {
		return err
	}
	c.mu.Lock()
	c.data = data
	c.mu.Unlock()
	return nil
}

func (c *pricingCatalog) save(data catalogData) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return fmt.Errorf("create pricing directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(c.path), ".copilot-pricing-*")
	if err != nil {
		return fmt.Errorf("create pricing cache: %w", err)
	}
	name := file.Name()
	defer func() {
		if errRemove := os.Remove(name); errRemove != nil && !os.IsNotExist(errRemove) {
			log.WithError(errRemove).Warn("github-copilot: remove temporary pricing cache")
		}
	}()
	errWrite := json.NewEncoder(file).Encode(data)
	errClose := file.Close()
	if errWrite != nil {
		return fmt.Errorf("write pricing cache: %w", errWrite)
	}
	if errClose != nil {
		return fmt.Errorf("close pricing cache: %w", errClose)
	}
	if err := os.Rename(name, c.path); err != nil {
		return fmt.Errorf("replace pricing cache: %w", err)
	}
	return nil
}

var thresholdPattern = regexp.MustCompile(`^(\x{2264}|<=|>)\s*([0-9]+(?:\.[0-9]+)?)\s*([KM]?)$`)

func parsePricing(reader io.Reader) (map[string]rate, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parse Copilot pricing page: %w", err)
	}
	rates := make(map[string]rate)
	var parseErr error
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if parseErr != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "table" {
			parseErr = parsePricingTable(n, rates)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := validateRates(rates); err != nil {
		return nil, err
	}
	return rates, nil
}

func nodeText(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "sup" {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(nodeText(child))
	}
	return strings.TrimSpace(b.String())
}

func parsePricingTable(table *html.Node, rates map[string]rate) error {
	headers := make(map[string]int)
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			rows = append(rows, n)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(table)
	for _, row := range rows {
		var cells []string
		isHeader := false
		for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type == html.ElementNode && (cell.Data == "th" || cell.Data == "td") {
				isHeader = isHeader || cell.Data == "th"
				cells = append(cells, nodeText(cell))
			}
		}
		if isHeader {
			for i, name := range cells {
				headers[strings.ToLower(name)] = i
			}
			continue
		}
		_, modelColumn := headers["model"]
		_, inputColumn := headers["input"]
		_, outputColumn := headers["output"]
		if !modelColumn || !inputColumn || !outputColumn {
			continue
		}
		get := func(name string) string {
			if i, ok := headers[name]; ok && i < len(cells) {
				return cells[i]
			}
			return ""
		}
		model := modelKey(get("model"))
		if model == "" {
			return fmt.Errorf("Copilot pricing row is missing its model")
		}
		r := rate{}
		for name, target := range map[string]*float64{"input": &r.Input, "cached input": &r.Cached, "cache write": &r.Write, "output": &r.Output} {
			value := get(name)
			if name == "cache write" && (value == "" || strings.EqualFold(value, "Not applicable")) {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimPrefix(value, "$"), 64)
			if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return fmt.Errorf("invalid Copilot %s rate for %s", name, model)
			}
			*target = parsed
		}
		threshold := get("threshold (input tokens)")
		long := false
		if threshold != "" && !strings.EqualFold(threshold, "Not applicable") {
			match := thresholdPattern.FindStringSubmatch(threshold)
			if match == nil {
				return fmt.Errorf("invalid Copilot threshold for %s", model)
			}
			value, _ := strconv.ParseFloat(match[2], 64)
			if match[3] == "K" {
				value *= 1000
			} else if match[3] == "M" {
				value *= 1000000
			}
			r.Threshold = int64(value)
			long = match[1] == ">"
		}
		existing, found := rates[model]
		if long {
			if existing.LongContext != nil || (found && existing.Threshold != r.Threshold) {
				return fmt.Errorf("conflicting Copilot tiers for %s", model)
			}
			existing.LongContext = &r
			if !found {
				existing.Threshold = r.Threshold
			}
			rates[model] = existing
		} else {
			if found && (existing.Input != 0 || existing.Output != 0) {
				return fmt.Errorf("duplicate Copilot rates for %s", model)
			}
			if existing.LongContext != nil && existing.Threshold != r.Threshold {
				return fmt.Errorf("conflicting Copilot tiers for %s", model)
			}
			r.LongContext = existing.LongContext
			rates[model] = r
		}
	}
	return nil
}

func validateRates(rates map[string]rate) error {
	if len(rates) == 0 {
		return fmt.Errorf("Copilot pricing page contains no model token rates")
	}
	for model, r := range rates {
		if model == "" || r.Threshold < 0 || (r.Threshold > 0 && r.LongContext == nil) {
			return fmt.Errorf("incomplete Copilot pricing for %s", model)
		}
		for _, tier := range []*rate{&r, r.LongContext} {
			if tier == nil {
				continue
			}
			for _, value := range []float64{tier.Input, tier.Cached, tier.Write, tier.Output} {
				if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
					return fmt.Errorf("invalid Copilot pricing for %s", model)
				}
			}
			if tier.Input <= 0 || tier.Output <= 0 {
				return fmt.Errorf("missing Copilot input/output rates for %s", model)
			}
		}
	}
	return nil
}
