package copilotusage

import (
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Rates are USD per million tokens, as published by GitHub.
type rate struct {
	Input       float64 `json:"input"`
	Cached      float64 `json:"cached"`
	Write       float64 `json:"write"`
	Output      float64 `json:"output"`
	Threshold   int64   `json:"threshold,omitempty"`
	LongContext *rate   `json:"long_context,omitempty"`
}

func modelKey(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.ReplaceAll(model, "(preview)", "")
	model = strings.ReplaceAll(model, "(fast mode)", " fast")
	return strings.Join(strings.FieldsFunc(model, func(r rune) bool { return r == ' ' || r == '-' || r == '_' || r == '.' }), "-")
}

func estimatedCost(rates map[string]rate, model string, detail usage.Detail) *int64 {
	pricing, found := rates[modelKey(model)]
	if !found || detail.TotalTokens <= 0 {
		return nil
	}
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.Quality != usage.TokenAccountingQualityComplete {
		return nil
	}
	input := breakdown.Input
	if pricing.Threshold > 0 && input.TotalTokens > pricing.Threshold {
		if pricing.LongContext == nil {
			return nil
		}
		pricing = *pricing.LongContext
	}
	usd := (float64(input.UncachedTokens)*pricing.Input + float64(input.CacheReadTokens)*pricing.Cached +
		float64(input.CacheWriteTokens)*pricing.Write + float64(breakdown.Output.TotalTokens)*pricing.Output) / 1_000_000
	nanos := int64(math.Round(usd * 1_000_000_000))
	return &nanos
}
