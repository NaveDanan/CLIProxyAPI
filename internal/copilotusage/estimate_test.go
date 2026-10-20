package copilotusage

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestLegacyMessagesRecordsSplitCacheReadsAndWrites(t *testing.T) {
	estimate := newEstimator(testRates(t))
	start := time.Date(2026, time.October, 12, 9, 0, 0, 0, time.UTC)
	legacy := func(at time.Time, cached, output int64) Event {
		return Event{At: at, Model: "claude-opus-5-5", InputTokens: 2, OutputTokens: output, TotalTokens: 2 + cached + output}
	}
	first := estimate.price(legacy(start, 45291, 1974))
	if first.tokens.CacheRead != 0 || first.tokens.CacheWrite != 45291 || !first.priced || !first.estimated {
		t.Fatalf("first request should write its whole prompt: %+v", first)
	}
	second := estimate.price(legacy(start.Add(time.Minute), 49431, 319))
	if second.tokens.CacheRead != 45291 || second.tokens.CacheWrite != 4140 {
		t.Fatalf("follow-up request should read the earlier prompt: %+v", second.tokens)
	}
	// Matches the cost of the same request saved with full cache details.
	if second.cost.total() != 36_146_200 {
		t.Fatalf("follow-up cost = %d", second.cost.total())
	}
	expired := estimate.price(legacy(start.Add(10*time.Minute), 55623, 652))
	if expired.tokens.CacheRead != 0 || expired.tokens.CacheWrite != 55623 {
		t.Fatalf("an expired cache should be written again: %+v", expired.tokens)
	}
}

func TestLegacyResponsesRecordRecoversCachedInputFromSavedCost(t *testing.T) {
	saved := int64(2_400_000)
	priced := newEstimator(testRates(t)).price(Event{
		At: time.Now(), Model: "gpt-6-luna", InputTokens: 100000, OutputTokens: 1000, TotalTokens: 101000, CostNanos: &saved,
	})
	if priced.tokens.CacheRead != 90000 || priced.tokens.Input != 10000 || priced.cost.total() != saved || !priced.estimated {
		t.Fatalf("cached input was not recovered: %+v", priced)
	}
}

func TestRequestsWithoutTokensCostNothing(t *testing.T) {
	priced := newEstimator(testRates(t)).price(Event{At: time.Now(), Model: "unlisted-model", Failed: true})
	if !priced.priced || priced.cost.total() != 0 {
		t.Fatalf("a request without tokens should be priced at zero: %+v", priced)
	}
}

func TestDatedModelIDsUseBaseModelRate(t *testing.T) {
	if modelKey("claude-haiku-4-5-20251001") != modelKey("Claude Haiku 4.5") {
		t.Fatalf("dated ID key = %q", modelKey("claude-haiku-4-5-20251001"))
	}
}

func TestDetectHarness(t *testing.T) {
	for _, test := range []struct{ userAgent, sessionID, want string }{
		// User agents and session IDs below are as captured from the CLIs.
		{"claude-cli/2.1.304 (external, sdk-ts, agent-sdk/0.3.276)", "claude:91805fef-2049-4905-a149-2d35a9321d8d", harnessClaudeCode},
		{"codex_exec/0.163.0 (Windows 10.0.26200; x86_64) xterm-256color (codex_exec; 0.163.0)", "codex:01a15823-00b7-76c3-a558-24ee8e14715f", harnessCodex},
		{"T3 Code/0.47.0 (Windows 10.0.26200; x86_64)", "codex:thread-1", harnessCodex},
		{"opencode/1.4.0 ai-sdk/provider-utils", "codex:generic-session-header", harnessOpenCode},
		{"node", "affinity:abc", harnessOpenCode},
		{"curl/8.0", "", harnessOther},
	} {
		if got := detectHarness(test.userAgent, test.sessionID); got != test.want {
			t.Errorf("detectHarness(%q, %q) = %q, want %q", test.userAgent, test.sessionID, got, test.want)
		}
	}
}

func TestDetectHarnessFromCodexSessionHeaders(t *testing.T) {
	// Codex launched by T3 Code reports T3 Code as its user agent but keeps the
	// Codex session headers captured here.
	headers := http.Header{}
	headers.Set("User-Agent", "T3 Code/0.47.0 (Windows 10.0.26200; x86_64)")
	headers.Set("Session-Id", "01a15823-00b7-76c3-a558-24ee8e14715f")
	headers.Set("Thread-Id", "01a15823-00b7-76c3-a558-24ee8e14715f")
	headers.Set("X-Codex-Turn-Metadata", `{"session_id":"01a15823-00b7-76c3-a558-24ee8e14715f","thread_id":"01a15823-00b7-76c3-a558-24ee8e14715f"}`)
	info, found := session.ExtractSessionInfo(headers, nil, nil)
	if !found {
		t.Fatal("Codex session headers were not recognized")
	}
	if got := detectHarness(headers.Get("User-Agent"), info.SessionID); got != harnessCodex {
		t.Fatalf("detectHarness = %q for session %q", got, info.SessionID)
	}
}

func TestDashboardSplitsProvidersHarnessesAndBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copilot-usage.jsonl")
	store := NewStore(path)
	store.pricing.data = catalogData{UpdatedAt: time.Now().UTC(), Rates: testRates(t)}
	if err := store.pricing.save(store.pricing.data); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)
	claude := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{UserAgent: "claude-cli/2.1.3 (external, cli)"})
	detail := usage.Detail{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100, TokenBreakdown: usage.NewSubsetTokenBreakdown(1000, 0, 0, 100, 0, 1100)}
	store.HandleUsage(claude, usage.Record{Provider: "github-copilot", Model: "claude-sonnet-4.6", RequestedAt: day.Add(time.Hour), Detail: detail})
	store.HandleUsage(claude, usage.Record{Provider: "github-copilot", Model: "claude-sonnet-4.6", RequestedAt: day.Add(25 * time.Hour), Detail: detail})
	store.HandleUsage(context.Background(), usage.Record{Provider: "codex", Model: "gpt-6-luna", SessionID: "codex:thread-1", RequestedAt: day.Add(26 * time.Hour), Detail: detail})
	store.HandleUsage(claude, usage.Record{Provider: "github-copilot", Model: "claude-sonnet-4.6", RequestedAt: day.Add(49 * time.Hour), Detail: detail})

	dashboard, err := NewStore(path).Dashboard([]time.Time{day, day.Add(24 * time.Hour), day.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Providers) != 2 {
		t.Fatalf("providers = %+v", dashboard.Providers)
	}
	copilotUsage, codexUsage := dashboard.Providers[0], dashboard.Providers[1]
	if copilotUsage.Provider != "github-copilot" || copilotUsage.Requests != 2 || copilotUsage.CostUSD != 0.009 ||
		len(copilotUsage.Series) != 1 || copilotUsage.Series[0].Harness != harnessClaudeCode {
		t.Fatalf("unexpected Copilot usage: %+v", copilotUsage)
	}
	if len(copilotUsage.Buckets) != 2 || len(copilotUsage.Buckets[0]) != 1 || len(copilotUsage.Buckets[1]) != 1 ||
		copilotUsage.Buckets[1][0].CostUSD != 0.0045 {
		t.Fatalf("unexpected Copilot buckets: %+v", copilotUsage.Buckets)
	}
	if codexUsage.Provider != "codex" || codexUsage.Series[0].Harness != harnessCodex ||
		len(codexUsage.Buckets[0]) != 0 || codexUsage.Buckets[1][0].Requests != 1 {
		t.Fatalf("unexpected Codex usage: %+v", codexUsage)
	}

	summary, err := NewStore(path).Summary(day, day.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ByModel) != 1 || summary.ByModel[0].Requests != 2 {
		t.Fatalf("Copilot usage page should list only Copilot requests: %+v", summary.ByModel)
	}
}
