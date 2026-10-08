package cliproxy

import (
	"context"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestCopilotCatalogFailurePreservesRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		aliases      []config.OAuthModelAlias
	}{
		{name: "prefix", prefix: "team"},
		{name: "alias", aliases: []config.OAuthModelAlias{{Name: "model", Alias: "public-model"}, {Name: "public-model", Alias: "second-alias"}}},
		{name: "alias and prefix", prefix: "team", aliases: []config.OAuthModelAlias{{Name: "model", Alias: "public-model"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{cfg: &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"github-copilot": tc.aliases}}}
			s.cfg.ForceModelPrefix = true
			auth := &coreauth.Auth{ID: t.Name(), Provider: "github-copilot", Prefix: tc.prefix, Metadata: map[string]any{"auth_kind": "oauth"}}
			models := []*ModelInfo{{ID: "model", UpstreamEndpoint: "/responses"}}
			models = applyOAuthModelAliasForAuth(s.cfg, auth.Provider, auth.AuthKind(), nil, models)
			models = applyModelPrefixes(models, auth.Prefix, true)
			r := registry.GetGlobalRegistry()
			r.RegisterClient(auth.ID, auth.Provider, models)
			t.Cleanup(func() { r.UnregisterClient(auth.ID) })
			before, epoch := r.GetModelsAndEpochForClient(auth.ID)
			r.SuspendClientModel(auth.ID, before[0].ID, "test suspension")
			for range 2 {
				// Missing credentials fail catalog acquisition without network access.
				s.registerModelsForAuth(context.Background(), auth)
				after, newEpoch := r.GetModelsAndEpochForClient(auth.ID)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("catalog failure changed registration: before=%+v after=%+v", before[0], after)
				}
				if newEpoch != epoch || !r.IsModelSuspendedForClient(auth.ID, before[0].ID) {
					t.Fatal("catalog failure replaced the existing registration state")
				}
			}
		})
	}
}

func TestCopilotModelNamesPreserveUpstreamAndExplicitIDs(t *testing.T) {
	models := []*ModelInfo{{ID: "claude-sonnet-5.1", OwnedBy: "Anthropic", UpstreamEndpoint: "/v1/messages"}, {ID: "gpt-5.1", OwnedBy: "OpenAI"}}
	got := applyCopilotModelNames(models)
	if len(got) != 3 || got[0].ID != "claude-sonnet-5.1" || got[1].ID != "gpt-5.1" || got[2].ID != "claude-sonnet-5-1" || got[2].UpstreamModelName != "claude-sonnet-5.1" || got[2].UpstreamEndpoint != "/v1/messages" {
		t.Fatalf("incorrect Claude alias: %+v", got)
	}
	if models[0].ID != "claude-sonnet-5.1" {
		t.Fatal("mutated source catalog")
	}
	if repeated := applyCopilotModelNames(got); len(repeated) != len(got) {
		t.Fatal("duplicated canonical alias")
	}
}
