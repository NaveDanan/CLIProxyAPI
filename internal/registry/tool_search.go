package registry

import "strings"

// SupportsClientToolSearch checks the transport for every registered route.
// Model support is checked separately against the Codex catalog template.
func (r *ModelRegistry) SupportsClientToolSearch(modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	if r == nil || modelID == "" {
		return false
	}
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	found := false
	for clientID, models := range r.clientModelInfos {
		info, ok := models[modelID]
		if !ok {
			continue
		}
		found = true
		switch strings.ToLower(strings.TrimSpace(r.clientProviders[clientID])) {
		case "codex":
		case "github-copilot":
			if info == nil || info.UpstreamEndpoint != "/responses" {
				return false
			}
		default:
			return false
		}
	}
	return found
}
