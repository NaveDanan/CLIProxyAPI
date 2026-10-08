package helps

import (
	"bytes"
	"net/http"

	"github.com/tidwall/gjson"
)

// ClaudeStreamError preserves errors sent after successful HTTP response headers.
func ClaudeStreamError(line []byte) (int, []byte, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return 0, nil, false
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if !gjson.ValidBytes(payload) || gjson.GetBytes(payload, "type").String() != "error" {
		return 0, nil, false
	}
	status := http.StatusBadGateway
	switch gjson.GetBytes(payload, "error.type").String() {
	case "invalid_request_error":
		status = http.StatusBadRequest
	case "authentication_error":
		status = http.StatusUnauthorized
	case "billing_error":
		status = http.StatusPaymentRequired
	case "permission_error":
		status = http.StatusForbidden
	case "not_found_error":
		status = http.StatusNotFound
	case "request_too_large":
		status = http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		status = http.StatusTooManyRequests
	case "api_error":
		status = http.StatusInternalServerError
	case "overloaded_error":
		status = 529
	}
	return status, bytes.Clone(payload), true
}
