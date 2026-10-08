package copilotusage

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestEstimatedCostUsesCacheAndLongContextRates(t *testing.T) {
	detail := usage.Detail{
		TotalTokens:    300030,
		TokenBreakdown: usage.NewSubsetTokenBreakdown(300000, 100000, 0, 30, 0, 300030),
	}
	cost := estimatedCost("gpt-5.4", detail)
	if cost == nil || *cost != 1_050_675_000 {
		t.Fatalf("long-context estimate: %v", cost)
	}
	if estimatedCost("unknown-model", detail) != nil {
		t.Fatal("unknown models must not have an invented price")
	}
}

func TestEstimatedCostIncludesCacheWrites(t *testing.T) {
	detail := usage.Detail{
		TotalTokens:    1600,
		TokenBreakdown: usage.NewSubsetTokenBreakdown(1500, 300, 200, 100, 0, 1600),
	}
	cost := estimatedCost("claude-sonnet-4.6", detail)
	if cost == nil || *cost != 5_340_000 {
		t.Fatalf("cache write estimate: %v", cost)
	}
}
