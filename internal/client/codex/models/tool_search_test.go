package models

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestCopilotCodexCatalogDefersToolsOnlyOnSupportedRoutes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		modelID   string
		providers []string
		endpoints []string
		want      bool
	}{
		{"responses", "gpt-6.1-sol", []string{"github-copilot"}, []string{"/responses"}, true},
		{"prefixed responses", "team/gpt-6.1-sol", []string{"github-copilot"}, []string{"/responses"}, true},
		{"chat completions", "gpt-6.1-sol", []string{"github-copilot"}, []string{"/chat/completions"}, false},
		{"messages", "gpt-6.1-sol", []string{"github-copilot"}, []string{"/v1/messages"}, false},
		{"unknown endpoint", "gpt-6.1-sol", []string{"github-copilot"}, []string{""}, false},
		{"mixed endpoints", "gpt-6.1-sol", []string{"github-copilot", "github-copilot"}, []string{"/chat/completions", "/responses"}, false},
		{"reversed mixed endpoints", "gpt-6.1-sol", []string{"github-copilot", "github-copilot"}, []string{"/responses", "/chat/completions"}, false},
		{"mixed native and copilot", "gpt-6.1-sol", []string{"codex", "github-copilot"}, []string{"", "/responses"}, true},
		{"unsupported provider", "gpt-6.1-sol", []string{"github-copilot", "openai-compatibility"}, []string{"/responses", "/responses"}, false},
		{"unrecognized model", "unknown-tool-search-model", []string{"github-copilot"}, []string{"/responses"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := registry.GetGlobalRegistry()
			for i, provider := range tc.providers {
				clientID := fmt.Sprintf("tool-search-%s-%d", tc.name, i)
				reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
					ID: tc.modelID, Type: provider, UpstreamEndpoint: tc.endpoints[i],
					ContextLength: 400000, InputTokenLimit: 272000, MaxCompletionTokens: 128000,
				}})
				t.Cleanup(func() { reg.UnregisterClient(clientID) })
			}
			response := BuildResponse(reg.GetAvailableModels("openai"), reg.GetModelProviders, false)
			var entry map[string]any
			for _, model := range response["models"].([]map[string]any) {
				if model["slug"] == tc.modelID {
					entry = model
					break
				}
			}
			if entry == nil {
				t.Fatalf("missing catalog entry %q", tc.modelID)
			}
			if got, _ := entry["supports_search_tool"].(bool); got != tc.want {
				t.Errorf("supports_search_tool = %v, want %v; disabling discovery eagerly loads the entire tool catalog", got, tc.want)
			}
			if entry["prefer_websockets"] != false {
				t.Error("Copilot must continue to use HTTP")
			}
		})
	}
}
