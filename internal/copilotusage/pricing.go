package copilotusage

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Rates are USD per million tokens, as published by GitHub.
type rate struct {
	Input  float64 `json:"input"`
	Cached float64 `json:"cached"`
	Write  float64 `json:"write"`
	Output float64 `json:"output"`
	// Category is the GitHub cost group, such as Lightweight or Powerful.
	Category    string `json:"category,omitempty"`
	Threshold   int64  `json:"threshold,omitempty"`
	LongContext *rate  `json:"long_context,omitempty"`
}

// Upstream snapshot IDs such as claude-haiku-4-5-20251001 share the base model price.
var datedModelSuffix = regexp.MustCompile(`-\d{8}$`)

func modelKey(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.ReplaceAll(model, "(preview)", "")
	model = strings.ReplaceAll(model, "(fast mode)", " fast")
	key := strings.Join(strings.FieldsFunc(model, func(r rune) bool { return r == ' ' || r == '-' || r == '_' || r == '.' }), "-")
	return datedModelSuffix.ReplaceAllString(key, "")
}

// tokenSplit holds the mutually exclusive token buckets that GitHub prices separately.
type tokenSplit struct {
	Input      int64
	CacheRead  int64
	CacheWrite int64
	Output     int64
}

// costSplit holds estimated nano-USD per token bucket. Other is a saved cost
// whose buckets are unknown; CacheSavings is not part of the total.
type costSplit struct {
	Input        int64
	CacheRead    int64
	CacheWrite   int64
	Output       int64
	Other        int64
	CacheSavings int64
}

func (c costSplit) total() int64 {
	return c.Input + c.CacheRead + c.CacheWrite + c.Output + c.Other
}

func (c *costSplit) add(other costSplit) {
	c.Input += other.Input
	c.CacheRead += other.CacheRead
	c.CacheWrite += other.CacheWrite
	c.Output += other.Output
	c.Other += other.Other
	c.CacheSavings += other.CacheSavings
}

func lookupRate(rates map[string]rate, model string, inputTokens int64) (rate, bool) {
	pricing, found := rates[modelKey(model)]
	if !found {
		return rate{}, false
	}
	if pricing.Threshold > 0 && inputTokens > pricing.Threshold {
		if pricing.LongContext == nil {
			return rate{}, false
		}
		pricing = *pricing.LongContext
	}
	return pricing, true
}

// nanos converts tokens at a USD-per-million rate to nano-USD.
func nanos(tokens int64, usdPerMillion float64) int64 {
	return int64(math.Round(float64(tokens) * usdPerMillion * 1000))
}

func priceTokens(rates map[string]rate, model string, tokens tokenSplit) (costSplit, bool) {
	pricing, found := lookupRate(rates, model, tokens.Input+tokens.CacheRead+tokens.CacheWrite)
	if !found {
		return costSplit{}, false
	}
	return costSplit{
		Input:        nanos(tokens.Input, pricing.Input),
		CacheRead:    nanos(tokens.CacheRead, pricing.Cached),
		CacheWrite:   nanos(tokens.CacheWrite, pricing.Write),
		Output:       nanos(tokens.Output, pricing.Output),
		CacheSavings: nanos(tokens.CacheRead, pricing.Input-pricing.Cached),
	}, true
}

func completeTokens(breakdown usage.TokenBreakdown) (tokenSplit, bool) {
	if !breakdown.Valid() || breakdown.Quality != usage.TokenAccountingQualityComplete {
		return tokenSplit{}, false
	}
	return tokenSplit{
		Input:      breakdown.Input.UncachedTokens,
		CacheRead:  breakdown.Input.CacheReadTokens,
		CacheWrite: breakdown.Input.CacheWriteTokens,
		Output:     breakdown.Output.TotalTokens,
	}, true
}

func estimatedCost(rates map[string]rate, model string, detail usage.Detail) *int64 {
	tokens, complete := completeTokens(detail.TokenBreakdown)
	if detail.TotalTokens <= 0 || !complete {
		return nil
	}
	cost, found := priceTokens(rates, model, tokens)
	if !found {
		return nil
	}
	total := cost.total()
	return &total
}

// legacyCacheWindow matches the default prompt cache lifetime, so a prompt
// cached longer ago than this counts as written again.
const legacyCacheWindow = 5 * time.Minute

type cachedPrompt struct {
	at     time.Time
	tokens int64
}

// pricedEvent is an event's token split and cost at the current rates.
type pricedEvent struct {
	tokens    tokenSplit
	cost      costSplit
	priced    bool
	estimated bool
}

// estimator prices events in file order. Older records saved only their
// input, output and total tokens. Agent harnesses resend a growing prompt, so
// the largest recently cached prompt that fits inside a request approximates
// its cache read, and the remaining cached tokens its cache write.
type estimator struct {
	rates   map[string]rate
	prompts map[string][]cachedPrompt
}

func newEstimator(rates map[string]rate) *estimator {
	return &estimator{rates: rates, prompts: make(map[string][]cachedPrompt)}
}

func (e *estimator) price(event Event) pricedEvent {
	// A request without reported tokens, usually a failed one, costs nothing.
	if event.TotalTokens <= 0 {
		return pricedEvent{priced: true}
	}
	key := eventProvider(event) + "\x00" + modelKey(event.Model)
	if tokens, complete := completeTokens(event.TokenBreakdown); complete {
		e.remember(key, event.At, tokens.CacheRead+tokens.CacheWrite)
		return e.priceSplit(event, tokens, false)
	}
	tokens := tokenSplit{Input: event.InputTokens, Output: event.OutputTokens}
	// Messages-style usage reports cache tokens beside input; Responses-style
	// usage counts them inside input, where only the saved cost can recover them.
	if cached := event.TotalTokens - event.InputTokens - event.OutputTokens; cached > 0 {
		tokens.CacheRead = e.recentPrompt(key, event.At, cached)
		tokens.CacheWrite = cached - tokens.CacheRead
		e.remember(key, event.At, cached)
	} else if event.CostNanos != nil {
		tokens.CacheRead = e.cachedInput(event, *event.CostNanos)
		tokens.Input -= tokens.CacheRead
	}
	priced := e.priceSplit(event, tokens, true)
	if !priced.priced && event.CostNanos != nil {
		priced.cost = costSplit{Other: *event.CostNanos}
		priced.priced = true
	}
	return priced
}

func (e *estimator) priceSplit(event Event, tokens tokenSplit, estimated bool) pricedEvent {
	cost, found := priceTokens(e.rates, event.Model, tokens)
	return pricedEvent{tokens: tokens, cost: cost, priced: found, estimated: estimated && found}
}

// cachedInput solves the saved cost for the cached share of an input total.
func (e *estimator) cachedInput(event Event, costNanos int64) int64 {
	pricing, found := lookupRate(e.rates, event.Model, event.InputTokens)
	if !found || pricing.Input <= pricing.Cached {
		return 0
	}
	uncachedCost := float64(event.InputTokens)*pricing.Input + float64(event.OutputTokens)*pricing.Output
	cached := int64(math.Round((uncachedCost - float64(costNanos)/1000) / (pricing.Input - pricing.Cached)))
	return min(max(cached, 0), event.InputTokens)
}

func (e *estimator) recentPrompt(key string, at time.Time, cached int64) int64 {
	var best int64
	for _, prompt := range e.prompts[key] {
		if age := at.Sub(prompt.at); age >= 0 && age <= legacyCacheWindow && prompt.tokens <= cached && prompt.tokens > best {
			best = prompt.tokens
		}
	}
	return best
}

func (e *estimator) remember(key string, at time.Time, cached int64) {
	if cached <= 0 {
		return
	}
	prompts := e.prompts[key][:0]
	for _, prompt := range e.prompts[key] {
		if age := at.Sub(prompt.at); age >= 0 && age <= legacyCacheWindow {
			prompts = append(prompts, prompt)
		}
	}
	prompts = append(prompts, cachedPrompt{at: at, tokens: cached})
	if len(prompts) > 64 {
		prompts = prompts[len(prompts)-64:]
	}
	e.prompts[key] = prompts
}
