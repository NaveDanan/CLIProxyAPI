package copilotusage

import (
	"fmt"
	"sort"
	"time"
)

// Totals summarizes requests, tokens and their estimated USD cost at the
// model rates GitHub publishes.
type Totals struct {
	Requests          int64   `json:"requests"`
	TotalTokens       int64   `json:"total_tokens"`
	InputTokens       int64   `json:"input_tokens"`
	CacheReadTokens   int64   `json:"cache_read_tokens"`
	CacheWriteTokens  int64   `json:"cache_write_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	InputCostUSD      float64 `json:"input_cost_usd"`
	CacheReadCostUSD  float64 `json:"cache_read_cost_usd"`
	CacheWriteCostUSD float64 `json:"cache_write_cost_usd"`
	OutputCostUSD     float64 `json:"output_cost_usd"`
	OtherCostUSD      float64 `json:"other_cost_usd"`
	CacheSavingsUSD   float64 `json:"cache_savings_usd"`
	UnpricedRequests  int64   `json:"unpriced_requests"`
	EstimatedRequests int64   `json:"estimated_requests"`
	cost              costSplit
}

func usd(nanos int64) float64 {
	return float64(nanos) / 1_000_000_000
}

func (t *Totals) add(event Event, priced pricedEvent) {
	t.Requests++
	t.TotalTokens += event.TotalTokens
	t.InputTokens += priced.tokens.Input
	t.CacheReadTokens += priced.tokens.CacheRead
	t.CacheWriteTokens += priced.tokens.CacheWrite
	t.OutputTokens += priced.tokens.Output
	if !priced.priced {
		t.UnpricedRequests++
		return
	}
	if priced.estimated {
		t.EstimatedRequests++
	}
	t.cost.add(priced.cost)
	t.CostUSD = usd(t.cost.total())
	t.InputCostUSD = usd(t.cost.Input)
	t.CacheReadCostUSD = usd(t.cost.CacheRead)
	t.CacheWriteCostUSD = usd(t.cost.CacheWrite)
	t.OutputCostUSD = usd(t.cost.Output)
	t.OtherCostUSD = usd(t.cost.Other)
	t.CacheSavingsUSD = usd(t.cost.CacheSavings)
}

// SeriesUsage is one model as used from one harness.
type SeriesUsage struct {
	Model   string `json:"model"`
	Harness string `json:"harness"`
	Totals
}

// BucketPoint is one series' usage within one time bucket.
type BucketPoint struct {
	Series      int     `json:"series"`
	Requests    int64   `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
	costNanos   int64
}

type ProviderUsage struct {
	Provider string `json:"provider"`
	Totals
	Series []SeriesUsage `json:"series"`
	// Buckets lists, for each requested time bucket, the series used in it.
	Buckets [][]BucketPoint `json:"buckets"`
}

type Dashboard struct {
	Start     time.Time       `json:"start"`
	End       time.Time       `json:"end"`
	Pricing   PricingStatus   `json:"pricing"`
	Providers []ProviderUsage `json:"providers"`
}

type providerAccumulator struct {
	usage  ProviderUsage
	series map[[2]string]int
	points []map[int]*BucketPoint
}

// Dashboard summarizes every provider across consecutive time buckets. The
// caller supplies the bucket boundaries so days follow its local calendar.
func (s *Store) Dashboard(boundaries []time.Time) (Dashboard, error) {
	if s == nil || len(boundaries) < 2 {
		return Dashboard{}, fmt.Errorf("usage dashboard needs at least two bucket boundaries")
	}
	for i := 1; i < len(boundaries); i++ {
		if !boundaries[i-1].Before(boundaries[i]) {
			return Dashboard{}, fmt.Errorf("usage bucket boundaries must increase")
		}
	}
	start, end, buckets := boundaries[0], boundaries[len(boundaries)-1], len(boundaries)-1
	result := Dashboard{Start: start.UTC(), End: end.UTC(), Providers: []ProviderUsage{}}
	providers := make(map[string]*providerAccumulator)
	status, err := s.scan(func(event Event, priced pricedEvent) {
		if event.At.Before(start) || !event.At.Before(end) {
			return
		}
		name := eventProvider(event)
		provider := providers[name]
		if provider == nil {
			provider = &providerAccumulator{usage: ProviderUsage{Provider: name}, series: make(map[[2]string]int), points: make([]map[int]*BucketPoint, buckets)}
			providers[name] = provider
		}
		key := [2]string{event.Model, eventHarness(event)}
		index, found := provider.series[key]
		if !found {
			index = len(provider.usage.Series)
			provider.series[key] = index
			provider.usage.Series = append(provider.usage.Series, SeriesUsage{Model: key[0], Harness: key[1]})
		}
		provider.usage.add(event, priced)
		provider.usage.Series[index].add(event, priced)
		bucket := sort.Search(buckets, func(i int) bool { return event.At.Before(boundaries[i+1]) })
		if provider.points[bucket] == nil {
			provider.points[bucket] = make(map[int]*BucketPoint)
		}
		point := provider.points[bucket][index]
		if point == nil {
			point = &BucketPoint{Series: index}
			provider.points[bucket][index] = point
		}
		point.Requests++
		point.TotalTokens += event.TotalTokens
		if priced.priced {
			point.costNanos += priced.cost.total()
			point.CostUSD = usd(point.costNanos)
		}
	})
	result.Pricing = status
	if err != nil {
		return result, err
	}
	for _, provider := range providers {
		provider.usage.Buckets = make([][]BucketPoint, buckets)
		for bucket, points := range provider.points {
			provider.usage.Buckets[bucket] = []BucketPoint{}
			for _, point := range points {
				provider.usage.Buckets[bucket] = append(provider.usage.Buckets[bucket], *point)
			}
			sort.Slice(provider.usage.Buckets[bucket], func(i, j int) bool {
				return provider.usage.Buckets[bucket][i].Series < provider.usage.Buckets[bucket][j].Series
			})
		}
		result.Providers = append(result.Providers, provider.usage)
	}
	sort.Slice(result.Providers, func(i, j int) bool {
		left, right := result.Providers[i], result.Providers[j]
		if left.CostUSD != right.CostUSD {
			return left.CostUSD > right.CostUSD
		}
		return left.Provider < right.Provider
	})
	return result, nil
}
