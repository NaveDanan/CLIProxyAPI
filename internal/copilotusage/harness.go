package copilotusage

import "strings"

// Harness names stored with each request. Unknown means the request predates
// harness tracking; other means the client did not identify itself.
const (
	harnessClaudeCode  = "claude-code"
	harnessCodex       = "codex"
	harnessOpenCode    = "opencode"
	harnessGeminiCLI   = "gemini-cli"
	harnessAntigravity = "antigravity"
	harnessOther       = "other"
	harnessUnknown     = "unknown"
)

// detectHarness identifies the client from its user agent, then from its
// session identity, which the proxy derives from harness-specific headers.
// Distinctive user agents win because other clients may send generic session
// headers, while Codex launched by another app keeps its session headers but
// may use that app's name as its user agent.
func detectHarness(userAgent, sessionID string) string {
	agent := strings.ToLower(userAgent)
	prefix, _, _ := strings.Cut(sessionID, ":")
	switch {
	case strings.Contains(agent, "opencode"):
		return harnessOpenCode
	case strings.Contains(agent, "claude-cli"), strings.Contains(agent, "claude-code"):
		return harnessClaudeCode
	case strings.Contains(agent, "codex"):
		return harnessCodex
	case strings.Contains(agent, "geminicli"), strings.Contains(agent, "gemini-cli"):
		return harnessGeminiCLI
	case strings.Contains(agent, "antigravity"):
		return harnessAntigravity
	}
	switch prefix {
	case "claude":
		return harnessClaudeCode
	case "codex":
		return harnessCodex
	case "affinity":
		return harnessOpenCode
	case "agy":
		return harnessAntigravity
	}
	return harnessOther
}

func eventHarness(event Event) string {
	if event.Harness == "" {
		return harnessUnknown
	}
	return event.Harness
}
