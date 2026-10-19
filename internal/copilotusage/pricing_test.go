package copilotusage

import (
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestEstimatedCostUsesCacheAndLongContextRates(t *testing.T) {
	detail := usage.Detail{
		TotalTokens:    300030,
		TokenBreakdown: usage.NewSubsetTokenBreakdown(300000, 100000, 0, 30, 0, 300030),
	}
	cost := estimatedCost(testRates(t), "gpt-5.4", detail)
	if cost == nil || *cost != 1_050_675_000 {
		t.Fatalf("long-context estimate: %v", cost)
	}
	if estimatedCost(testRates(t), "unknown-model", detail) != nil {
		t.Fatal("unknown models must not have an invented price")
	}
}

func TestEstimatedCostIncludesCacheWrites(t *testing.T) {
	detail := usage.Detail{
		TotalTokens:    1600,
		TokenBreakdown: usage.NewSubsetTokenBreakdown(1500, 300, 200, 100, 0, 1600),
	}
	cost := estimatedCost(testRates(t), "claude-sonnet-4.6", detail)
	if cost == nil || *cost != 5_340_000 {
		t.Fatalf("cache write estimate: %v", cost)
	}
}

func TestEstimatedCostPricesDocumentedHaiku55(t *testing.T) {
	detail := usage.Detail{TotalTokens: 1100, TokenBreakdown: usage.NewSubsetTokenBreakdown(1000, 0, 0, 100, 0, 1100)}
	cost := estimatedCost(testRates(t), "claude-haiku-5.5", detail)
	if cost == nil {
		t.Fatal("documented Copilot model is unpriced")
	}
	if *cost != 150000 {
		t.Fatalf("cost = %d, want 150000", *cost)
	}
}

func testRates(t *testing.T) map[string]rate {
	t.Helper()
	page, err := os.Open("testdata/pricing.html")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errClose := page.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	rates, err := parsePricing(page)
	if err != nil {
		t.Fatal(err)
	}
	return rates
}

func TestEstimatedCostMatchesCopilotModelIDs(t *testing.T) {
	detail := usage.Detail{TotalTokens: 1100, TokenBreakdown: usage.NewSubsetTokenBreakdown(1000, 0, 0, 100, 0, 1100)}
	for _, model := range []string{"Claude Sonnet 4.6", "claude-sonnet-4.6", "claude-sonnet-4-6", "claude_sonnet_4_6"} {
		cost := estimatedCost(testRates(t), model, detail)
		if cost == nil || *cost != 4_500_000 {
			t.Errorf("model %q is not priced at the documented Sonnet rate: %v", model, cost)
		}
	}
}
