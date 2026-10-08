package copilotusage

import (
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type rate struct {
	input, cached, write, output float64
	threshold                    int64
	longContext                  *rate
}

var modelRates = map[string]rate{
	"gpt-5-mini":         {input: 0.25, cached: 0.025, output: 2},
	"gpt-5.3-codex":      {input: 1.75, cached: 0.175, output: 14},
	"gpt-5.4":            {input: 2.5, cached: 0.25, output: 15, threshold: 272000, longContext: &rate{input: 5, cached: 0.5, output: 22.5}},
	"gpt-5.4-mini":       {input: 0.75, cached: 0.075, output: 4.5},
	"gpt-5.4-nano":       {input: 0.2, cached: 0.02, output: 1.25},
	"gpt-5.5":            {input: 5, cached: 0.5, output: 30, threshold: 272000, longContext: &rate{input: 10, cached: 1, output: 45}},
	"gpt-5.6-luna":       {input: 0.2, cached: 0.02, write: 0.25, output: 1.2, threshold: 200000, longContext: &rate{input: 0.4, cached: 0.04, write: 0.5, output: 1.8}},
	"gpt-5.6-sol":        {input: 4, cached: 0.4, write: 5, output: 20, threshold: 272000, longContext: &rate{input: 8, cached: 0.8, write: 10, output: 30}},
	"gpt-5.6-terra":      {input: 2, cached: 0.2, write: 2.5, output: 12, threshold: 272000, longContext: &rate{input: 4, cached: 0.4, write: 5, output: 18}},
	"gpt-6-astra":        {input: 10, cached: 1, write: 12.5, output: 50, threshold: 272000, longContext: &rate{input: 20, cached: 2, write: 25, output: 75}},
	"gpt-6-luna":         {input: 0.1, cached: 0.01, write: 0.125, output: 0.5, threshold: 272000, longContext: &rate{input: 0.2, cached: 0.02, write: 0.25, output: 0.75}},
	"gpt-6-sol":          {input: 2, cached: 0.2, write: 2.5, output: 10, threshold: 272000, longContext: &rate{input: 4, cached: 0.4, write: 5, output: 15}},
	"gpt-6.1-sol":        {input: 2, cached: 0.1, write: 2.5, output: 10, threshold: 272000, longContext: &rate{input: 4, cached: 0.2, write: 5, output: 15}},
	"claude-haiku-4.5":   {input: 1, cached: 0.1, write: 1.25, output: 5},
	"claude-sonnet-4":    {input: 3, cached: 0.3, write: 3.75, output: 15},
	"claude-sonnet-4.6":  {input: 3, cached: 0.3, write: 3.75, output: 15},
	"claude-opus-4.8":    {input: 5, cached: 0.5, write: 6.25, output: 25},
	"claude-opus-5":      {input: 5, cached: 0.5, write: 6.25, output: 25},
	"claude-opus-5.5":    {input: 4, cached: 0.2, write: 5, output: 20},
	"claude-sonnet-5":    {input: 2, cached: 0.2, write: 2.5, output: 10},
	"claude-sonnet-5.5":  {input: 2, cached: 0.2, write: 2.5, output: 10},
	"claude-fable-5":     {input: 10, cached: 1, write: 12.5, output: 50},
	"claude-fable-5.1":   {input: 10, cached: 0.25, write: 12.5, output: 50},
	"gemini-3.7-flash":   {input: 0.75, cached: 0.075, output: 3.75},
	"gemini-3.8-flash":   {input: 0.75, cached: 0.075, output: 3.75},
	"mai-code-1.1-flash": {input: 0.2, cached: 0.02, output: 1.2},
	"grok-4.5":           {input: 2, cached: 0.5, output: 6, threshold: 200000, longContext: &rate{input: 4, cached: 1, output: 12}},
	"grok-4.6":           {input: 2, cached: 0.5, output: 6, threshold: 200000, longContext: &rate{input: 4, cached: 1, output: 12}},
	"grok-4.7":           {input: 2, cached: 0.5, output: 6, threshold: 200000, longContext: &rate{input: 4, cached: 1, output: 12}},
	"kimi-k3":            {input: 3, cached: 0.3, output: 15},
}

func estimatedCost(model string, detail usage.Detail) *int64 {
	pricing, found := modelRates[strings.ToLower(strings.TrimSpace(model))]
	if !found || detail.TotalTokens <= 0 {
		return nil
	}
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.Quality != usage.TokenAccountingQualityComplete {
		return nil
	}
	input := breakdown.Input
	if pricing.threshold > 0 && input.TotalTokens > pricing.threshold {
		pricing = *pricing.longContext
	}
	usd := (float64(input.UncachedTokens)*pricing.input + float64(input.CacheReadTokens)*pricing.cached +
		float64(input.CacheWriteTokens)*pricing.write + float64(breakdown.Output.TotalTokens)*pricing.output) / 1_000_000
	nanos := int64(math.Round(usd * 1_000_000_000))
	return &nanos
}
