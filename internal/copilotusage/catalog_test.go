package copilotusage

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestPricingRefreshPersistsReplacesAndRetainsCache(t *testing.T) {
	page, err := os.ReadFile("testdata/pricing.html")
	if err != nil {
		t.Fatal(err)
	}
	body, status := string(page), http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if _, errWrite := w.Write([]byte(body)); errWrite != nil {
			t.Error(errWrite)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "copilot-usage.jsonl")
	store := NewStore(path)
	at := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	store.HandleUsage(context.Background(), usage.Record{
		Provider: "github-copilot", Model: "claude-sonnet-4-6", RequestedAt: at,
		Detail: usage.Detail{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100,
			TokenBreakdown: usage.NewSubsetTokenBreakdown(1000, 0, 0, 100, 0, 1100)},
	})
	legacyCost := int64(1000000)
	for _, cost := range []*int64{&legacyCost, nil} {
		if err := store.append(Event{At: at, Model: "legacy", TotalTokens: 1100, CostNanos: cost}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(s *Store, cost float64, unpriced int64) {
		t.Helper()
		summary, err := s.Summary(at.Add(-time.Hour), at.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(summary.ByModel) != 2 {
			t.Fatalf("models: %+v", summary)
		}
		modern, legacy := summary.ByModel[0], summary.ByModel[1]
		if math.Abs(modern.CostUSD-cost) > 1e-12 || math.Abs(modern.AICredits-cost*100) > 1e-12 || modern.Unpriced != unpriced {
			t.Fatalf("modern pricing: %+v", modern)
		}
		if legacy.CostUSD != .001 || legacy.Unpriced != 1 {
			t.Fatalf("legacy pricing: %+v", legacy)
		}
	}
	check(store, 0, 1)
	refresh := func() error { return store.pricing.refresh(context.Background(), server.Client(), server.URL) }
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	check(store, .0045, 0)
	check(NewStore(path), .0045, 0)
	body = strings.ReplaceAll(body, "$3.00", "$6.00")
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	check(store, .0075, 0)
	check(NewStore(path), .0075, 0)
	for _, failure := range []struct {
		status int
		body   string
	}{{503, "unavailable"}, {200, "<html>no pricing tables</html>"}} {
		status, body = failure.status, failure.body
		if err := refresh(); err == nil {
			t.Fatal("bad response accepted")
		}
		check(store, .0075, 0)
		_, pricing := store.pricing.snapshot()
		if pricing.Error == "" || pricing.UpdatedAt.IsZero() || pricing.Models == 0 {
			t.Fatalf("fallback status: %+v", pricing)
		}
	}
	status, body = http.StatusOK, string(page)
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	_, pricing := store.pricing.snapshot()
	if pricing.Error != "" {
		t.Fatalf("error survived successful refresh: %+v", pricing)
	}
	check(NewStore(path), .0045, 0)
}

func TestPricingHaiku45CreditConversion(t *testing.T) {
	rates := testRates(t)
	r, ok := rates[modelKey("Claude Haiku 4.5")]
	if !ok || r.Input != 1 || r.Cached != .1 || r.Write != 1.25 || r.Output != 5 {
		t.Fatalf("Haiku 4.5 rates: %+v", r)
	}
	for _, bucket := range []struct {
		detail  usage.TokenBreakdown
		credits float64
	}{
		{usage.NewSubsetTokenBreakdown(1000000, 0, 0, 0, 0, 1000000), 100},
		{usage.NewSubsetTokenBreakdown(1000000, 1000000, 0, 0, 0, 1000000), 10},
		{usage.NewSubsetTokenBreakdown(1000000, 0, 1000000, 0, 0, 1000000), 125},
		{usage.NewSubsetTokenBreakdown(0, 0, 0, 1000000, 0, 1000000), 500},
	} {
		cost := estimatedCost(rates, "claude-haiku-4-5", usage.Detail{TotalTokens: 1000000, TokenBreakdown: bucket.detail})
		if cost == nil {
			t.Fatal("Haiku 4.5 is unpriced")
		}
		result := add(ModelSummary{}, Event{CostNanos: cost})
		if result.AICredits != bucket.credits {
			t.Fatalf("credits = %v, want %v", result.AICredits, bucket.credits)
		}
	}
}
