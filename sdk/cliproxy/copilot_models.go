package cliproxy

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (s *Service) fetchCopilotModels(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
	models, err := executor.NewCopilotExecutor(s.cfg).Models(ctx, auth)
	if err != nil {
		log.WithError(err).WithField("provider", "github-copilot").Warn("could not refresh account models")
	}
	return models, err
}

// Keep Copilot IDs callable while exposing Anthropic's version spelling to native clients.
func applyCopilotModelNames(models []*ModelInfo) []*ModelInfo {
	result := append([]*ModelInfo(nil), models...)
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		if model != nil {
			seen[model.ID] = true
		}
	}
	for _, model := range models {
		if model == nil || !strings.HasPrefix(model.ID, "claude-") {
			continue
		}
		canonical := strings.ReplaceAll(model.ID, ".", "-")
		if canonical == model.ID || seen[canonical] {
			continue
		}
		alias := cloneModelInfoForCatalogRoute(model)
		alias.ID = canonical
		alias.UpstreamModelName = model.ID
		result = append(result, &alias)
		seen[canonical] = true
	}
	return result
}
