# External adapter plugin contract

Turnal discovers adapter executables named `turnal-adapter-*` on `PATH`. Adapters are deliberately narrow translators: they read provider hook input and emit normalized events. They cannot append Turnal events or create checkpoints. The `turnal adapter capture` bridge validates their output and sends it through the same locks, secrets policy, durable event log, and checkpoint manager used by built-in hooks.

Protocol v1 is newline-delimited JSON on stdin and stdout. Every line includes `"protocol":"turnal-adapter"`, `"version":1`, and a request `id`. Turnal sends a `describe` request during discovery and a `normalize` request for provider input:

```json
{"protocol":"turnal-adapter","version":1,"id":"capture","method":"normalize","hook":"AfterTool","payload":{"session_id":"demo","cwd":"/workspace","tool_name":"write_file"}}
```

An adapter emits zero or more `event` responses. A single after-tool hook normally becomes a `tool.call` followed by a `tool.result`:

```json
{"protocol":"turnal-adapter","version":1,"id":"capture","type":"event","event":{"type":"tool.call","session_id":"demo","cwd":"/workspace","tool_name":"write_file","input":{"path":"README.md"}}}
```

The supported event types are `session.start`, `prompt.user`, `tool.call`, `tool.result`, `assistant.message`, and `turn.finish`. `session_id` and an absolute `cwd` are required on every event. Tool events also require `tool_name`. The `source_id`, `provider_turn_id`, `tool_use_id`, model metadata, transcript paths, JSON input and output, and `is_error` fields are optional. A child `session.start` can set `parent_session_id` and the spawning `parent_tool_use_id`. Turnal stores that relation on the session instead of flattening the child into the parent's tool activity.

The Go SDK lives at `github.com/AadiJo/turnal/sdk/adapter`. Its `Serve` function implements framing, version negotiation, request validation, response encoding, and event validation. Protocol conformance transcripts are in [`sdk/adapter/testdata/conformance/v1`](../sdk/adapter/testdata/conformance/v1). Adapters must write protocol data only to stdout; diagnostics belong on stderr. Run `turnal adapter contract` for the installed contract summary or `turnal adapter contract --json` for a machine-readable description.

An adapter is a plugin when its executable is named `turnal-adapter-<name>`, is available on `PATH` or beside `turnal`, and returns a matching manifest from `describe`. This makes third-party adapters independently installable without registering them in Turnal core. Use `turnal adapter doctor <name>` as the compatibility gate.

## Included adapters

Release packages include `turnal-adapter-cursor`, `turnal-adapter-pi`, `turnal-adapter-opencode`, `turnal-adapter-gemini-cli`, and `turnal-adapter-copilot-cli`. Source installs can build all commands together:

```sh
go install github.com/AadiJo/turnal/cmd/...@latest
turnal adapter list
turnal adapter doctor
```

`list` shows discovered executables and their advertised versions. `doctor` performs a protocol-v1 handshake and checks that the executable name matches its manifest. A specific installation can be checked with `turnal adapter doctor gemini-cli`.

Provider hooks pipe their JSON payload to the hidden capture bridge. The bridge always returns successful, valid hook output after reporting capture failures to stderr so recording cannot block the agent.

### Cursor

Run `turnal init --agent cursor` to merge these commands into `.cursor/hooks.json`. Cursor's generic tool hooks capture tool boundaries. The `afterAgentResponse` hook captures assistant text where the surface emits it, and `stop` always closes the turn. The `subagentStart` hook records a child session linked to its parent conversation and spawning Task call.

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [{"command":"turnal adapter capture cursor sessionStart"}],
    "beforeSubmitPrompt": [{"command":"turnal adapter capture cursor beforeSubmitPrompt"}],
    "preToolUse": [{"command":"turnal adapter capture cursor preToolUse"}],
    "postToolUse": [{"command":"turnal adapter capture cursor postToolUse"}],
    "postToolUseFailure": [{"command":"turnal adapter capture cursor postToolUseFailure"}],
    "afterAgentResponse": [{"command":"turnal adapter capture cursor afterAgentResponse"}],
    "stop": [{"command":"turnal adapter capture cursor stop"}],
    "subagentStart": [{"command":"turnal adapter capture cursor subagentStart"}]
  }
}
```

Cursor loads project hooks from the trusted workspace and user hooks from `~/.cursor/hooks.json`. Hook availability varies by Cursor surface. The CLI uses `stop` as the final checkpoint boundary even when it does not emit `afterAgentResponse`. The `beforeSubmitPrompt` response adds the active Turnal intent command to the submitted prompt.

### Pi

Pi uses an extension to forward typed lifecycle events. Run `turnal init --agent pi` to install the managed project extension at `.pi/extensions/turnal.ts`. To install it for all projects, copy [`integrations/pi/turnal.ts`](../integrations/pi/turnal.ts) to `~/.pi/agent/extensions/turnal.ts`. Approve the project extension when Pi prompts. Capture errors produce diagnostics and never block Pi.

The extension records session start, prompts, tool start and result pairs, structured tool failures, and the settled assistant response. It adds the active Turnal intent command to the system prompt for each turn. When Pi forks or clones a session, the extension reads the parent session header and emits `parent_session_id`. Both `turnal sessions` and `turnal sessions --json` expose the fork topology.

### Gemini CLI

Run `turnal init --agent gemini` to merge Turnal-owned command hooks into `.gemini/settings.json` while preserving unrelated groups. The complete lifecycle uses `SessionStart`, `BeforeAgent`, `BeforeTool`, `AfterTool`, `AfterAgent`, and `SessionEnd`:

```json
{
  "hooks": {
    "SessionStart": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli SessionStart"}]}],
    "BeforeAgent": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli BeforeAgent"}]}],
    "BeforeTool": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli BeforeTool"}]}],
    "AfterTool": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli AfterTool"}]}],
    "AfterAgent": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli AfterAgent"}]}],
    "SessionEnd": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli SessionEnd"}]}]
  }
}
```

### GitHub Copilot CLI

This integration is specifically for the GitHub Copilot CLI. It does not claim support for unrelated Copilot products or surfaces. Run `turnal init --agent copilot` to create or merge the repository hook file `.github/hooks/turnal.json`. Turnal writes the GitHub Copilot CLI's platform-specific `bash` and `powershell` commands:

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [{"type":"command","bash":"turnal adapter capture copilot-cli sessionStart","powershell":"& turnal adapter capture copilot-cli sessionStart"}],
    "userPromptSubmitted": [{"type":"command","bash":"turnal adapter capture copilot-cli userPromptSubmitted","powershell":"& turnal adapter capture copilot-cli userPromptSubmitted"}],
    "preToolUse": [{"type":"command","bash":"turnal adapter capture copilot-cli preToolUse","powershell":"& turnal adapter capture copilot-cli preToolUse"}],
    "postToolUse": [{"type":"command","bash":"turnal adapter capture copilot-cli postToolUse","powershell":"& turnal adapter capture copilot-cli postToolUse"}],
    "errorOccurred": [{"type":"command","bash":"turnal adapter capture copilot-cli errorOccurred","powershell":"& turnal adapter capture copilot-cli errorOccurred"}],
    "agentStop": [{"type":"command","bash":"turnal adapter capture copilot-cli agentStop","powershell":"& turnal adapter capture copilot-cli agentStop"}]
  }
}
```

GitHub Copilot CLI's stop hook points at its session transcript instead of carrying settled assistant text. During single-turn prompt-mode `turnal run`, Turnal waits until the child exits, validates that transcript against the session and workspace, records the final assistant response, and only then completes the provider turn. Interactive wrappers complete each turn at its stop hook instead of deferring every turn until process exit. The wrapper defaults `GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS` to `true` so repository hooks load in programmatic prompt mode, but preserves an explicitly configured value. Review every file under `.github/hooks/` before enabling repository hooks. The default `--agent auto` only refreshes an existing `.github/hooks/turnal.json`; creating repository hooks requires explicit `--agent copilot` or `--agent all`.

### OpenCode

Run `turnal init --agent opencode` to install the managed plugin at `.opencode/plugins/turnal.js`. Turnal refuses to overwrite a same-named plugin it does not manage. The installed plugin forwards message deltas, settled replies, session state, and both tool boundaries through the capture bridge. Its core shape is:

```js
export const TurnalPlugin = async ({ directory }) => {
  const capture = async (hook, payload) => {
    const child = Bun.spawn(
      ["turnal", "adapter", "capture", "opencode", hook],
      { stdin: new Blob([JSON.stringify({ directory, ...payload })]), stdout: "ignore", stderr: "inherit" },
    )
    await child.exited
  }

  return {
    event: async ({ event }) => capture("event", { event }),
    "tool.execute.before": async (input, output) =>
      capture("tool.execute.before", { ...input, args: output.args }),
    "tool.execute.after": async (input, output) =>
      capture("tool.execute.after", { ...input, output }),
  }
}
```

OpenCode message and session events are normalized through the `event` callback; tools use paired `tool.execute.before` and `tool.execute.after` callbacks.

## End-to-end parity test

The default suite uses normalized fixtures for all seven providers and compares the durable semantic log after removing provider-specific identifiers and timestamps. An opt-in live contract launches authenticated CLIs in disposable repositories and requires one prompt, one settled assistant response, paired tool activity, exactly one real file mutation, and matching pre/post checkpoints:

```sh
TURNAL_LIVE_PARITY_TEST=1 go test ./internal/cli -run TestLiveProviderEditParity -count=1
TURNAL_LIVE_PARITY_TEST=1 TURNAL_LIVE_PARITY_PROVIDER=copilot \
  go test ./internal/cli -run TestLiveProviderEditParity -count=1
```

The provider selector accepts `claude`, `codex`, `copilot`, `cursor`, `gemini`, `opencode`, or `pi`. Each selected CLI must already be installed and authenticated. `copilot` always means GitHub Copilot CLI.

## Compatibility and safety

Protocol versions are explicit and adapters advertise every supported version in their manifest. Turnal rejects unknown versions, malformed NDJSON, mismatched request IDs, invalid event types, non-absolute workspaces, cross-session batches, excessive lines, and processes that exceed the handshake timeout. Provider input is retained under the existing raw-adapter policy; prompt and tool aliases used by the included adapters are recursively redacted when the corresponding secrets settings are disabled.
