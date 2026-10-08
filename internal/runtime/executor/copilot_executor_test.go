package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type copilotRoundTripper func(*http.Request) (*http.Response, error)

func (f copilotRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestCopilot() (*CopilotExecutor, *coreauth.Auth) {
	e := NewCopilotExecutor(&config.Config{})
	e.tokenSource = func(context.Context, *coreauth.Auth, bool) (copilot.Token, error) {
		return copilot.Token{Token: "short-copilot-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	}
	auth := &coreauth.Auth{ID: "copilot-test", Provider: copilot.Provider, Metadata: map[string]any{"access_token": "persistent-github-token", "email": "octocat", "auth_kind": "oauth"}}
	return e, auth
}

func TestCopilotProtocolRoutingAndCredentialIsolation(t *testing.T) {
	for _, tc := range []struct {
		endpoint          string
		format            translator.Format
		payload, response string
	}{
		{"/chat/completions", translator.FormatOpenAI, `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`, `{"id":"chat-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`},
		{"/responses", translator.FormatOpenAIResponse, `{"model":"test-model","input":"hello"}`, `{"id":"resp-1","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`},
		{"/v1/messages", translator.FormatClaude, `{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`, `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			e, auth := newTestCopilot()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: tc.endpoint}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			otherEndpoint := "/chat/completions"
			if tc.endpoint == otherEndpoint {
				otherEndpoint = "/responses"
			}
			otherID := auth.ID + "-other"
			registry.GetGlobalRegistry().RegisterClient(otherID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: otherEndpoint}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(otherID) })
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.githubcopilot.com" || r.URL.Path != tc.endpoint {
					t.Errorf("wrong endpoint: %s", r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer short-copilot-token" {
					t.Errorf("wrong inference credential: %q", r.Header.Get("Authorization"))
				}
				if r.Header.Get("Copilot-Integration-Id") != "vscode-chat" {
					t.Error("missing Copilot headers")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(body), "persistent-github-token") {
					t.Error("leaked GitHub credential")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			}))
			req := exec.Request{Model: "test-model", Payload: []byte(tc.payload)}
			opts := exec.Options{SourceFormat: tc.format, OriginalRequest: req.Payload, Metadata: map[string]any{exec.SelectedAuthMetadataKey: auth.ID}}
			if got := e.RequestToFormat(req, opts); got != tc.format {
				t.Errorf("selected account format = %q, want %q", got, tc.format)
			}
			if got := e.delegate(auth, req, opts).Identifier(); got != copilot.Provider {
				t.Errorf("usage attributed to %s", got)
			}
			resp, err := e.Execute(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !gjson.ValidBytes(resp.Payload) {
				t.Fatalf("invalid response: %s", resp.Payload)
			}
			if auth.Attributes["api_key"] != "" || auth.Metadata["access_token"] != "persistent-github-token" {
				t.Fatal("request mutated persisted credentials")
			}
		})
	}
}

func TestCopilotResponsesStreamTerminalAndUsage(t *testing.T) {
	for _, terminal := range []string{"completed", "incomplete", ""} {
		t.Run(terminal, func(t *testing.T) {
			complete := terminal != ""
			e, auth := newTestCopilot()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-response", UpstreamEndpoint: "/responses"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			otherID := auth.ID + "-other"
			registry.GetGlobalRegistry().RegisterClient(otherID, copilot.Provider, []*registry.ModelInfo{{ID: "test-response", UpstreamEndpoint: "/chat/completions"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(otherID) })
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/responses" || gjson.GetBytes(body, "stream_options").Exists() {
					t.Errorf("bad Responses request: %s %s", r.URL, body)
				}
				sse := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
				if complete {
					sse += "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"
					sse = strings.ReplaceAll(sse, "completed", terminal)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}, nil
			}))
			req := exec.Request{Model: "test-response", Payload: []byte(`{"model":"test-response","input":"hi","stream":true}`)}
			stream, err := e.ExecuteStream(ctx, auth, req, exec.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: req.Payload, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			var output strings.Builder
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					failed = true
				}
				output.Write(chunk.Payload)
			}
			if failed == complete {
				t.Fatalf("complete=%v failed=%v", complete, failed)
			}
			if complete && !strings.Contains(output.String(), "response."+terminal) {
				t.Fatal("missing terminal event")
			}
		})
	}
}

func TestCopilotQuotaFailureUsesAccountFailover(t *testing.T) {
	e, auth := newTestCopilot()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 402, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"quota exhausted"}}`))}, nil
	}))
	_, err := e.Execute(ctx, auth, exec.Request{Model: "test", Payload: []byte(`{"model":"test","messages":[]}`)}, exec.Options{SourceFormat: translator.FormatOpenAI})
	status, ok := err.(interface {
		StatusCode() int
		IsCredentialScoped() bool
	})
	if !ok || status.StatusCode() != 429 || !status.IsCredentialScoped() {
		t.Fatalf("quota error=%v", err)
	}
}

func TestCopilotFormatUsesSelectedAccountAlias(t *testing.T) {
	e, auth := newTestCopilot()
	r := registry.GetGlobalRegistry()
	r.RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "team/public-model", UpstreamEndpoint: "/responses"}})
	otherID := auth.ID + "-other"
	r.RegisterClient(otherID, copilot.Provider, []*registry.ModelInfo{{ID: "team/public-model", UpstreamEndpoint: "/chat/completions"}})
	t.Cleanup(func() { r.UnregisterClient(auth.ID); r.UnregisterClient(otherID) })
	req := exec.Request{Model: "upstream-model(high)"}
	opts := exec.Options{Metadata: map[string]any{
		exec.SelectedAuthMetadataKey:   auth.ID,
		exec.RequestedModelMetadataKey: "team/public-model(high)",
	}}
	if got := e.RequestToFormat(req, opts); got != translator.FormatOpenAIResponse {
		t.Fatalf("aliased account format = %q, want Responses", got)
	}
	opts.Metadata[exec.SelectedAuthMetadataKey] = otherID
	if got := e.RequestToFormat(req, opts); got != translator.FormatOpenAI {
		t.Fatalf("other account format = %q, want Chat Completions", got)
	}
}

func TestCopilotStreamRejectsPrematureCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, sse string
		format              translator.Format
	}{
		{"chat EOF to Claude", "/chat/completions", "data: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Working\"},\"finish_reason\":null}]}\n\n", translator.FormatClaude},
		{"chat bare DONE to Claude", "/chat/completions", "data: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Working\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n", translator.FormatClaude},
		{"chat trailing usage without finish reason to Claude", "/chat/completions", `data: {"id":"chat-1","choices":[{"index":0,"delta":{"content":"Working"},"finish_reason":null}]}

data: {"id":"chat-1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1}}

data: [DONE]

`, translator.FormatClaude},
		{"chat finish with usage but missing DONE to Claude", "/chat/completions", `data: {"id":"chat-1","choices":[{"index":0,"delta":{"content":"Working"},"finish_reason":null}]}

data: {"id":"chat-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}

`, translator.FormatClaude},
		{"Responses bare DONE", "/responses", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: [DONE]\n\n", translator.FormatOpenAIResponse},
		{"Messages EOF to Claude", "/v1/messages", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n", translator.FormatClaude},
		{"Messages EOF to Codex", "/v1/messages", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n", translator.FormatOpenAIResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, auth := newTestCopilot()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: tc.endpoint}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.sse))}, nil
			}))
			payload := []byte(`{"model":"test-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
			if tc.format == translator.FormatOpenAIResponse {
				payload = []byte(`{"model":"test-model","input":"hi","stream":true}`)
			}
			result, err := e.ExecuteStream(ctx, auth, exec.Request{Model: "test-model", Payload: payload}, exec.Options{SourceFormat: tc.format, OriginalRequest: payload, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var failed bool
			var output strings.Builder
			for chunk := range result.Chunks {
				failed = failed || chunk.Err != nil
				output.Write(chunk.Payload)
			}
			if !failed {
				t.Fatalf("unfinished turn was accepted as success: %s", output.String())
			}
			if strings.Contains(output.String(), `"stop_reason":"end_turn"`) || strings.Contains(output.String(), `"type":"message_stop"`) || strings.Contains(output.String(), `"type":"response.completed"`) {
				t.Fatalf("unfinished turn emitted successful completion: %s", output.String())
			}
		})
	}
}

func TestCopilotCanonicalClaudeModelUsesOriginalUpstreamID(t *testing.T) {
	for _, endpoint := range []string{"/v1/messages", "/chat/completions"} {
		t.Run(endpoint, func(t *testing.T) {
			e, auth := newTestCopilot()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "team/claude-sonnet-5-1", UpstreamModelName: "claude-sonnet-5.1", UpstreamEndpoint: endpoint}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if got := gjson.GetBytes(body, "model").String(); got != "claude-sonnet-5.1" {
					t.Errorf("upstream model = %s", got)
				}
				response := `{"id":"msg-1","type":"message","role":"assistant","model":"claude-sonnet-5.1","content":[],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`
				if endpoint == "/chat/completions" {
					response = `{"id":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
			}))
			payload := []byte(`{"model":"team/claude-sonnet-5-1","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
			_, err := e.Execute(ctx, auth, exec.Request{Model: "claude-sonnet-5-1", Payload: payload}, exec.Options{SourceFormat: translator.FormatClaude, OriginalRequest: payload, Metadata: map[string]any{exec.RequestedModelMetadataKey: "team/claude-sonnet-5-1"}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCopilotRetriesRejectedInferenceTokenBeforeStreaming(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			e, auth := newTestCopilot()
			var exchanges []bool
			e.tokenSource = func(_ context.Context, _ *coreauth.Auth, force bool) (copilot.Token, error) {
				exchanges = append(exchanges, force)
				token := "stale-token"
				if force {
					token = "fresh-token"
				}
				return copilot.Token{Token: token, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: "/responses"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
				code, body := 200, `{"id":"resp-1","status":"completed","output":[]}`
				if streaming {
					body = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[]}}\n\n"
				}
				if r.Header.Get("Authorization") == "Bearer stale-token" {
					code, body = 401, `{"error":{"message":"invalid token"}}`
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			payload := []byte(`{"model":"test-model","input":"hi","stream":true}`)
			req, opts := exec.Request{Model: "test-model", Payload: payload}, exec.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: payload, Stream: streaming}
			if streaming {
				result, err := e.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else {
				if _, err := e.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(exchanges, []bool{false, true}) {
				t.Fatalf("token refresh attempts: %v", exchanges)
			}
		})
	}
}

func TestCopilotInferenceTokenRetryIsBounded(t *testing.T) {
	for _, code := range []int{401, 403} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e, auth := newTestCopilot()
			calls := 0
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"denied"}}`))}, nil
			}))
			_, err := e.ExecuteStream(ctx, auth, exec.Request{Model: "test", Payload: []byte(`{"model":"test","messages":[],"stream":true}`)}, exec.Options{SourceFormat: translator.FormatOpenAI, Stream: true})
			want := 1
			if code == 401 {
				want = 2
			}
			if err == nil || calls != want {
				t.Fatalf("code=%d calls=%d err=%v", code, calls, err)
			}
		})
	}
}

func TestCopilotResponsesToClaudePreservesIncompleteToolTurn(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			e, auth := newTestCopilot()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: "/responses"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			response := `{"id":"resp-1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"fc_1","type":"function_call","call_id":"toolu_copilot_1","name":"Bash","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":1}}`
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if got := gjson.GetBytes(body, "max_output_tokens").Int(); got != 64 {
					t.Errorf("lost max_tokens budget: %s", body)
				}
				if gjson.GetBytes(body, "messages").Exists() || !gjson.GetBytes(body, "input").Exists() {
					t.Errorf("wrong Responses request: %s", body)
				}
				payload := response
				if streaming {
					payload = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"model\":\"test-model\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"toolu_copilot_1\",\"name\":\"Bash\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.incomplete\",\"response\":" + response + "}\n\n"
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
			}))
			payload := []byte(fmt.Sprintf(`{"model":"test-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"stream":%t}`, streaming))
			req, opts := exec.Request{Model: "test-model", Payload: payload}, exec.Options{SourceFormat: translator.FormatClaude, OriginalRequest: payload, Stream: streaming}
			var output strings.Builder
			if streaming {
				result, err := e.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					output.Write(chunk.Payload)
				}
			} else {
				result, err := e.Execute(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				output.Write(result.Payload)
			}
			if !strings.Contains(output.String(), `"stop_reason":"max_tokens"`) {
				t.Fatalf("incomplete tool turn presented as completed tool call: %s", output.String())
			}
		})
	}
}

func TestCopilotMessagesStreamErrorsRemainErrorsForCodex(t *testing.T) {
	e, auth := newTestCopilot()
	registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: "/v1/messages"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(*http.Request) (*http.Response, error) {
		body := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n"
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	payload := []byte(`{"model":"test-model","input":"hi","stream":true}`)
	result, err := e.ExecuteStream(ctx, auth, exec.Request{Model: "test-model", Payload: payload}, exec.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: payload, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var failure error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			failure = chunk.Err
		}
	}
	status, ok := failure.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != 429 || !strings.Contains(failure.Error(), "slow down") {
		t.Fatalf("lost upstream error: %v", failure)
	}
}

func TestCopilotStreamingToolRoundTripAcrossCodingClients(t *testing.T) {
	const callID = "toolu_copilot_1"
	streams := map[string]string{
		"/chat/completions": `data: {"id":"chat-1","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_copilot_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"pwd\"}"}}]},"finish_reason":null}]}

data: {"id":"chat-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1}}

data: [DONE]

`,
		"/v1/messages": `event: message_start
data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"test-model","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_copilot_1","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"pwd\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`,
		"/responses": `data: {"type":"response.created","response":{"id":"resp-1","model":"test-model","status":"in_progress","output":[]}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"toolu_copilot_1","name":"Bash","arguments":""}}

data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"command\":\"pwd\"}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"toolu_copilot_1","name":"Bash","arguments":"{\"command\":\"pwd\"}"}}

data: {"type":"response.completed","response":{"id":"resp-1","model":"test-model","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"toolu_copilot_1","name":"Bash","arguments":"{\"command\":\"pwd\"}"}],"usage":{"input_tokens":2,"output_tokens":1}}}

`,
	}
	for _, format := range []translator.Format{translator.FormatClaude, translator.FormatOpenAIResponse} {
		for endpoint, sse := range streams {
			t.Run(format.String()+endpoint, func(t *testing.T) {
				e, auth := newTestCopilot()
				registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: endpoint}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				requests := 0
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
					requests++
					body, _ := io.ReadAll(r.Body)
					if requests == 2 && (!strings.Contains(string(body), callID) || !strings.Contains(string(body), "directory-output")) {
						t.Errorf("lost tool result continuity: %s", body)
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(sse))}, nil
				}))
				first := []byte(`{"model":"test-model","max_tokens":64,"messages":[{"role":"user","content":"run pwd"}],"tools":[{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],"stream":true}`)
				second := []byte(`{"model":"test-model","max_tokens":64,"messages":[{"role":"user","content":"run pwd"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_copilot_1","name":"Bash","input":{"command":"pwd"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_copilot_1","content":"directory-output"}]}],"stream":true}`)
				if format == translator.FormatOpenAIResponse {
					first = []byte(`{"model":"test-model","input":"run pwd","tools":[{"type":"function","name":"Bash","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}],"stream":true}`)
					second = []byte(`{"model":"test-model","input":[{"role":"user","content":"run pwd"},{"type":"function_call","call_id":"toolu_copilot_1","name":"Bash","arguments":"{\"command\":\"pwd\"}"},{"type":"function_call_output","call_id":"toolu_copilot_1","output":"directory-output"}],"stream":true}`)
				}
				for _, payload := range [][]byte{first, second} {
					result, err := e.ExecuteStream(ctx, auth, exec.Request{Model: "test-model", Payload: payload}, exec.Options{SourceFormat: format, OriginalRequest: payload, Stream: true})
					if err != nil {
						t.Fatal(err)
					}
					var output strings.Builder
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output.Write(chunk.Payload)
					}
					if !strings.Contains(output.String(), callID) || !strings.Contains(output.String(), "Bash") {
						t.Fatalf("lost tool call: %s", output.String())
					}
					if format == translator.FormatClaude {
						if !strings.Contains(output.String(), `"stop_reason":"tool_use"`) || !strings.Contains(output.String(), "message_stop") {
							t.Fatalf("lost tool continuation: %s", output.String())
						}
					} else if !strings.Contains(output.String(), "response.completed") {
						t.Fatalf("missing terminal response: %s", output.String())
					}
				}
			})
		}
	}
}
