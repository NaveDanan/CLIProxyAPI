# GitHub Copilot compatibility audit

Audited on 2026-10-08 against backend commit `8124a6b8` and desktop version 1.2.5, including the local fixes described below.

The audit reproduced several defects that can make an interrupted or incomplete Copilot response look like a finished Claude Code turn. The strongest match to the reported symptom is the Chat Completions fallback: a clean socket close could generate a synthetic completion marker, which the Claude translator turned into a successful end of turn. Missing translation registration, incorrect completion reasons, model naming, and token rejection handling created additional failure paths. These have been corrected and covered by deterministic tests.

This establishes plausible causes in the application. It does not establish which defect caused a particular historical conversation: no failing user trace or authenticated Copilot conversation was available for this audit.

## How the providers differ

| Provider | Credentials and inference | Request path | Conversation implications |
| --- | --- | --- | --- |
| Native Claude | Anthropic OAuth or API key | Claude executor, Messages API, Anthropic SSE | Closest match to Claude Code's messages, tools, and thinking protocol. Native headers and compatibility behavior remain subject to the credential and upstream origin. |
| Native Codex | OpenAI OAuth | Dedicated Codex Responses executor, with a separate WebSocket executor | Handles Codex-specific request preparation and Responses behavior. The shared translator change also preserves truncation reasons here. |
| GitHub Copilot | GitHub OAuth exchanges for a cached, short-lived inference token | Catalog chooses Messages, Responses, or Chat Completions; existing protocol executors perform inference | Shares the proxy's account selection, exclusions, aliases, quota cooldown, transport, and translation infrastructure. Endpoint support and model access come from the Copilot account. It does not acquire every native Claude or Codex feature merely by serving the same model family. |

The main Copilot path is [CopilotExecutor](../internal/runtime/executor/copilot_executor.go), [catalog discovery](../internal/auth/copilot/models.go), and [account model registration](../sdk/cliproxy/service_models.go). Model metadata determines the upstream protocol per selected account; choosing a Claude or Codex client does not itself determine the upstream endpoint.

Previously the catalog chose Chat Completions first whenever it was advertised. It now selects:

1. Messages for an Anthropic model that advertises it.
2. Responses when advertised.
3. Chat Completions as a fallback.
4. Messages for remaining Messages-only models.

This reduces translation for Claude Code and retains Responses semantics where available. Microsoft's current Copilot client also contains endpoint selection for these protocols; its feature flags and request preparation are separate from this proxy's implementation. [Copilot chat endpoint source](https://raw.githubusercontent.com/microsoft/vscode/main/extensions/copilot/src/platform/endpoint/node/chatEndpoint.ts).

The CLI generally supplies conversation history and owns the local thread. The proxy maintains additional token, translation, signature, and session state. There is no general mechanism here that reconstructs missing assistant output after a dropped stream.

## Confirmed defects and corrections

| Priority | Trigger and previous behavior | Correction |
| --- | --- | --- |
| High | Copilot Chat stream closes after partial content. The Claude-target fallback could synthesize `[DONE]` and a successful stop. A bare `[DONE]` could also finish a turn without an upstream finish reason. Trailing usage could make the translator emit success before either check. | Copilot Chat requires both an upstream finish reason and `[DONE]`. Claude completion events are held until those checks pass. Premature EOF returns a stream error. Other generic Chat providers retain their existing fallback behavior. |
| High | Responses sends `[DONE]` without a terminal response event. The executor could accept it as completion. | Responses requires `response.completed` or `response.incomplete`; failure events remain errors. |
| High | Messages closes before `message_stop`. The shared Claude executor could silently close its output channel. | Both native forwarding and translated output explicitly fail on missing `message_stop`. This correction also applies to native Claude. |
| High | Messages emits an error after HTTP 200. Translation can discard the error event, obscuring the reason the thread stopped. | Classify and preserve Anthropic error payloads before translation, including rate limits, overload, authentication, and billing failures. |
| High | Claude request targets a Copilot Responses model. The Claude-to-HTTP-Responses route was unregistered. | Register the complete request/response route, reuse the existing item and tool conversion, and map `max_tokens` to `max_output_tokens`. |
| High | Non-streaming Responses returns a plain response object. The reused Codex converter expected a wrapped terminal event and could return empty output. | Accept both plain HTTP response objects and wrapped Codex terminal events. |
| High | Responses ends incomplete while a tool call exists. Tool presence overrode the truncation reason with `tool_use`. | Preserve token-limit, context-window, and refusal reasons before considering tool presence. Tests cover streaming and non-streaming output. |
| Medium | A cached inference token is rejected with HTTP 401 before output begins. It can be treated as an account failure despite the exchange credential remaining valid. | Force one inference-token refresh and retry. A repeated 401 stops; 403 is not retried by this mechanism. Streams are retried only before they are returned to the caller. |
| Medium | Claude model IDs arrive in Copilot's dotted spelling, while both CLI menus can contain the whole provider catalog. Family aliases and a fixed subagent override can select unintended models. | Add canonical Claude aliases, preserve the upstream ID, separate model families in desktop setup, migrate saved selections, assign family aliases to selected family models, and let subagents inherit the session choice. |
| Medium | Same-format `ExecuteStream` receives a body without `stream: true`, or a payload rule changes it. The upstream may return ordinary JSON to the SSE reader. | The Claude streaming method now explicitly requests SSE after payload configuration. |

Anthropic defines `message_stop` as the stream's final event and allows errors within an already-started stream. [Messages streaming protocol](https://platform.claude.com/docs/en/build-with-claude/streaming), [Anthropic API errors](https://platform.claude.com/docs/en/api/errors). OpenAI Responses uses typed lifecycle events rather than relying on a Chat Completions sentinel. [Responses streaming guide](https://developers.openai.com/api/docs/guides/streaming-responses).

The tests exercise all three Copilot upstream protocols with both Claude and Codex clients. They verify tool-call identifiers, arguments, the next request's tool result, and the resulting completion reason. These fixtures check the adapter boundary; they are not live model evaluations.

## Model names and CLI configuration

The following names illustrate the mapping; they are not a claim that these model versions are available to every account.

| Copilot upstream ID | Claude Code menu | Codex menu |
| --- | --- | --- |
| `claude-sonnet-5.1` | `claude-sonnet-5-1` | Excluded |
| `claude-opus-4.6` | `claude-opus-4-6` | Excluded |
| `gpt-5.1` | Excluded | `gpt-5.1` |
| `gpt-5.3-codex` | Excluded | `gpt-5.3-codex` |

The proxy keeps the original Copilot ID callable and adds the canonical Claude alias. Private registry metadata maps that alias back to the upstream ID. Account prefixes and configured alias copies retain the metadata. Exclusion rules are applied before and after generated aliases so an excluded raw model cannot return through an automatic alias.

Desktop discovery hides a dotted Claude duplicate when its canonical alias is advertised. Saved dotted selections and defaults migrate to the canonical alias. Claude setup accepts Anthropic models; Codex setup accepts OpenAI models, using vendor metadata and recognizable IDs. Unsupported family cells are disabled, and Apply is disabled when an enabled CLI has no eligible selection. The API remains capable of cross-family requests; this restriction concerns the requested CLI menus.

Claude settings use `model`, `availableModels`, and the custom picker. The generated Opus, Sonnet, Haiku, and Fable aliases select an available chosen model from their own family, falling back to the initial model when that family is absent. The setup removes its fixed `CLAUDE_CODE_SUBAGENT_MODEL` override so a subagent can inherit the session model. [Claude Code model configuration](https://code.claude.com/docs/en/model-config).

Codex setup uses the HTTP Responses wire protocol and a local `model_catalog_json` file. OpenAI IDs retain their original spelling. Both CLI integrations need a restart after their configuration changes; Codex reads its custom catalog at startup. [Codex configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference).

Desktop configuration also now reads the API key from v8's `access.api-keys`, with the legacy root key as a fallback. File merging, backups, and rollback remain covered by the existing desktop tests.

## Remaining limits

- **No live end-to-end confirmation.** No authenticated inference calls, real CLI sessions, or historical failing-thread traces were used. A live tool-use conversation in each CLI remains necessary to measure account-specific compatibility and actual disconnect frequency.
- **Chat fallback translates native features.** Messages tools/thinking and Responses items are not identical to Chat Completions. The fixtures cover the tested tool and terminal paths; they do not establish support for every Claude beta, native thinking signature, document feature, or Codex extension.
- **Unsupported endpoint classes remain explicit.** Copilot inference rejects Responses compaction and image-generation endpoints. Models that advertise only unsupported endpoints are not registered. This provider uses HTTP executors rather than the dedicated native Codex WebSocket executor.
- **Token counting is an estimate.** Copilot's `CountTokens` uses the generic local compatibility path, not an authoritative Anthropic token-counting endpoint.
- **Internal GitHub interfaces can change.** The device login is documented, but Copilot token exchange, model discovery, and quota contracts depend on interfaces used by GitHub's clients. Capability metadata and organization policy can change independently of the model ID.
- **Mid-stream recovery requires care.** Once output has reached the CLI, replaying the whole request can duplicate content or tools. The new token retry does not replay partial turns. A transport drop produces a visible failure instead of a fabricated successful stop.
- **A quiet connection can still be dropped externally.** Downstream SSE keepalives and bootstrap retries are disabled by default. The repository prohibits adding general inference timeouts. An upstream that remains connected without sending data is therefore a separate liveness concern.

For a deployment whose intervening HTTP connection drops quiet streams, the existing optional v8 settings are:

```yaml
requests:
  streaming:
    keepalive-seconds: 15
    bootstrap-retries: 1
```

These send downstream keepalives and permit retries before the first downstream byte. They cannot repair a completed tool execution or recover missing stream content. They were not applied to user configuration during this audit.

## Validation and delivery

Passed after the final fixes:

- `go test ./internal/auth/copilot ./internal/registry ./internal/translator/codex/claude ./sdk/cliproxy -count=1`.
- `go test ./internal/runtime/executor -run '^(TestCopilot|TestClaude|TestOpenAICompat)' -count=1`.
- Server build: `go build -o temp/copilot-audit/cli-proxy-api.exe ./cmd/server`.
- Desktop `npm test`: all 36 tests passed; its pretest step compiles the TypeScript sources.
- The isolated Electron smoke test in [harness-renderer.smoke.cjs](../desktop/test/harness-renderer.smoke.cjs): actual setup page and preload, saved-name migration, family filtering, Apply output, and empty-selection prevention. It uses test-only IPC and does not write the user's CLI settings.
- `git diff --check` in both repositories.

The complete `go test ./... -timeout 5m` run did not pass. HTTP-proxy tests, Codex token-refresh proxy tests, and credential-proxy tests failed with loopback transport errors; the broad run also encountered Windows test-executable file locks. The selected proxy failures were reproduced in an untouched detached checkout of `8124a6b8`, including CONNECT, management APICall, Antigravity transport, three Codex refresh cases, and Vertex credential exchange. Windows reported that established connections were aborted by host software. This confirms that those test failures predate these changes, but does not identify the responsible software or establish whether the user's live connection is affected.

The new missing-terminal check exposed a Claude stream test that omitted the request's stream flag. The streaming-method correction resolved it; the final focused Claude suite passes. Broad-suite success must still be established in an environment where the existing proxy tests can run.

Source changes are in both the backend and desktop repositories. Desktop version 1.2.6 includes these fixes and pins its backend source with the `desktop-v1.2.6` tag. The backend's existing usage-dashboard changes are included as a separate prerequisite commit so the tagged source reproduces the bundled features. After upgrading, restart the proxy, refresh the CLI model list, click **Apply to CLIs**, and restart Claude Code and Codex. Reapplying is necessary to replace CLI settings generated by earlier desktop versions.
