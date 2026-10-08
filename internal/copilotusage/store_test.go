package copilotusage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestStorePersistsCopilotUsageByModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copilot-usage.jsonl")
	store := NewStore(path)
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	store.HandleUsage(context.Background(), usage.Record{
		Provider: "github-copilot", Model: "gpt-5", RequestedAt: now,
		Detail: usage.Detail{InputTokens: 120, OutputTokens: 30, TotalTokens: 150},
	})
	store.HandleUsage(context.Background(), usage.Record{
		Provider: "openai", Model: "gpt-5", RequestedAt: now,
		Detail: usage.Detail{TotalTokens: 999},
	})

	summary, err := NewStore(path).Summary(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ByModel) != 1 || summary.ByModel[0].Model != "gpt-5" ||
		summary.ByModel[0].Requests != 1 || summary.ByModel[0].InputTokens != 120 ||
		summary.ByModel[0].OutputTokens != 30 || summary.ByModel[0].TotalTokens != 150 {
		t.Fatalf("unexpected Copilot summary: %+v", summary)
	}
}

func TestStoreAggregatesPricedUsageAcrossDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copilot-usage.jsonl")
	store := NewStore(path)
	first := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{first, first.AddDate(0, 0, 1)} {
		store.HandleUsage(context.Background(), usage.Record{
			Provider: "github-copilot", Model: "gpt-5.4", RequestedAt: at,
			Detail: usage.Detail{
				InputTokens: 200, OutputTokens: 100, TotalTokens: 300,
				TokenBreakdown: usage.NewSubsetTokenBreakdown(200, 0, 0, 100, 0, 300),
			},
		})
	}
	summary, err := NewStore(path).Summary(first.Add(-time.Hour), first.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Days) != 2 || len(summary.ByModel) != 1 ||
		summary.ByModel[0].Requests != 2 || summary.ByModel[0].CostUSD != 0.004 ||
		summary.ByModel[0].AICredits != 0.4 || summary.ByModel[0].Unpriced != 0 ||
		summary.Days[0].ByModel[0].CostUSD != 0.002 {
		t.Fatalf("unexpected daily priced history: %+v", summary)
	}
}
