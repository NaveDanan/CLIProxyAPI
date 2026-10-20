package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/copilotusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestGetCopilotUsageCustomRange(t *testing.T) {
	store := copilotusage.NewStore(filepath.Join(t.TempDir(), "usage.jsonl"))
	for _, day := range []int{1, 2, 3} {
		store.HandleUsage(context.Background(), usage.Record{
			Provider: "github-copilot", Model: "gpt-5.4", RequestedAt: time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC),
			Detail: usage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		})
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/usage/copilot?period=custom&start=2026-10-02&end=2026-10-03", nil)
	(&Handler{copilotUsage: store}).GetCopilotUsage(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var summary copilotusage.Summary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.ByModel) != 1 || summary.ByModel[0].Requests != 2 || len(summary.Days) != 2 {
		t.Fatalf("custom date inclusion: %+v", summary)
	}
}

func TestGetCopilotUsageRejectsInvalidRange(t *testing.T) {
	for _, query := range []string{"period=custom&start=2026-10-03&end=2026-10-02", "period=custom&start=bad&end=2026-10-03", "period=unknown"} {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/usage/copilot?"+query, nil)
		(&Handler{copilotUsage: copilotusage.NewStore(filepath.Join(t.TempDir(), "usage.jsonl"))}).GetCopilotUsage(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q returned %d", query, rec.Code)
		}
	}
}

func TestGetUsageDashboardBucketsEveryProvider(t *testing.T) {
	store := copilotusage.NewStore(filepath.Join(t.TempDir(), "usage.jsonl"))
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for _, provider := range []string{"github-copilot", "codex"} {
		store.HandleUsage(context.Background(), usage.Record{
			Provider: provider, Model: "gpt-5.4", RequestedAt: day.Add(time.Hour),
			Detail: usage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		})
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/usage/dashboard?boundaries="+
		strconv.FormatInt(day.UnixMilli(), 10)+","+strconv.FormatInt(day.Add(24*time.Hour).UnixMilli(), 10), nil)
	(&Handler{copilotUsage: store}).GetUsageDashboard(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var dashboard copilotusage.Dashboard
	if err := json.Unmarshal(rec.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Providers) != 2 || len(dashboard.Providers[0].Buckets) != 1 {
		t.Fatalf("unexpected dashboard: %+v", dashboard)
	}
}

func TestGetUsageDashboardRejectsInvalidBoundaries(t *testing.T) {
	for _, query := range []string{"", "boundaries=5", "boundaries=5,4", "boundaries=5,x"} {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/usage/dashboard?"+query, nil)
		(&Handler{copilotUsage: copilotusage.NewStore(filepath.Join(t.TempDir(), "usage.jsonl"))}).GetUsageDashboard(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q returned %d", query, rec.Code)
		}
	}
}

func TestGetUsageQueuePopsRequestedRecords(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload []json.RawMessage
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if len(payload) != 2 {
			t.Fatalf("response records = %d, want 2", len(payload))
		}
		requireRecordID(t, payload[0], 1)
		requireRecordID(t, payload[1], 2)

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":3}` {
			t.Fatalf("remaining queue = %q, want third item only", remaining)
		}
	})
}

func TestGetUsageQueueInvalidCountDoesNotPop(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=0", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":1}` {
			t.Fatalf("remaining queue = %q, want original item", remaining)
		}
	})
}

func withManagementUsageQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	defer func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	}()

	fn()
}

func requireRecordID(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()

	var payload struct {
		ID int `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal record: %v", errUnmarshal)
	}
	if payload.ID != want {
		t.Fatalf("record id = %d, want %d", payload.ID, want)
	}
}
