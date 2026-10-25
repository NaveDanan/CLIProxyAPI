package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCopilotClientToolSearchRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			e, auth := newTestCopilot()
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "gpt-6.1-sol", UpstreamEndpoint: "/responses"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

			searchTool := `{"type":"tool_search","execution":"client","description":"Discover tools","parameters":{"type":"object","properties":{"goal":{"type":"string"}},"required":["goal"],"additionalProperties":false}}`
			searchCall := `{"id":"ts_1","type":"tool_search_call","execution":"client","call_id":"call_search_1","status":"completed","arguments":{"goal":"Find the shipping ETA tool"}}`
			loadedTool := `{"type":"function","name":"get_shipping_eta","defer_loading":true,"parameters":{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"],"additionalProperties":false}}`
			searchOutput := `{"type":"tool_search_output","execution":"client","call_id":"call_search_1","status":"completed","tools":[` + loadedTool + `]}`
			functionCall := `{"id":"fc_1","type":"function_call","call_id":"call_shipping_1","name":"get_shipping_eta","arguments":"{\"order_id\":\"order_42\"}","status":"completed"}`
			for phase, input := range []string{`[{"role":"user","content":"Find the shipping ETA tool"}]`, `[` + searchCall + `,` + searchOutput + `]`} {
				item := searchCall
				if phase == 1 {
					item = functionCall
				}
				response := `{"id":"resp-search","object":"response","status":"completed","output":[` + item + `],"usage":{"input_tokens":53,"output_tokens":30,"total_tokens":83}}`
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path != "/responses" {
						t.Errorf("request path = %q, want /responses", r.URL.Path)
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					if !gjson.GetBytes(body, "tools.0").IsObject() || gjson.GetBytes(body, "tools.0.type").String() != "tool_search" {
						t.Fatal("client tool search declaration was lost")
					}
					if phase == 1 {
						if gjson.GetBytes(body, "input.1.call_id").String() != "call_search_1" || !gjson.GetBytes(body, "input.1.tools.0.defer_loading").Bool() {
							t.Fatal("tool discovery result or deferred declaration was lost")
						}
					}
					contentType := "application/json"
					if stream {
						contentType = "text/event-stream"
						response = "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + item + "}\n\n" +
							"data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}, nil
				}))
				req := exec.Request{Model: "gpt-6.1-sol", Payload: []byte(`{"model":"gpt-6.1-sol","input":` + input + `,"tools":[` + searchTool + `]}`)}
				opts := exec.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: req.Payload}
				var body string
				if stream {
					result, err := e.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						body += string(chunk.Payload)
					}
				} else {
					result, err := e.Execute(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					body = string(result.Payload)
				}
				for _, want := range []string{gjson.Get(item, "type").String(), gjson.Get(item, "call_id").String()} {
					if !strings.Contains(body, want) {
						t.Errorf("phase %d response lost %q: %s", phase, want, body)
					}
				}
			}
		})
	}
}
