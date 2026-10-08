package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

func (h *Handler) GetCopilotUsage(c *gin.Context) {
	if h == nil || h.copilotUsage == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Copilot usage history is unavailable"})
		return
	}
	now := time.Now().UTC()
	var start, end time.Time
	switch c.DefaultQuery("period", "today") {
	case "today":
		start = now.Truncate(24 * time.Hour)
		end = start.AddDate(0, 0, 1)
	case "this_week":
		start = now.Truncate(24*time.Hour).AddDate(0, 0, -int((now.Weekday()+6)%7))
		end = start.AddDate(0, 0, 7)
	case "this_month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)
	case "custom":
		var errStart, errEnd error
		start, errStart = time.Parse("2006-01-02", c.Query("start"))
		end, errEnd = time.Parse("2006-01-02", c.Query("end"))
		end = end.AddDate(0, 0, 1)
		if errStart != nil || errEnd != nil || !start.Before(end) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "start and end must be valid UTC dates with start <= end"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "period must be today, this_week, this_month, or custom"})
		return
	}
	summary, err := h.copilotUsage.Summary(start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read Copilot usage history"})
		return
	}
	c.JSON(http.StatusOK, summary)
}
