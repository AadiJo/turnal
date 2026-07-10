# External adapter SDK

Turnal discovers adapter executables named `turnal-adapter-*` on `PATH`. Adapters are deliberately narrow translators: they read provider hook input and emit normalized events. They cannot append Turnal events or create checkpoints. The `turnal adapter capture` bridge validates their output and sends it through the same locks, secrets policy, durable event log, and checkpoint manager used by built-in hooks.

Protocol v1 is newline-delimited JSON on stdin and stdout. Every line includes `"protocol":"turnal-adapter"`, `"version":1`, and a request `id`. Turnal sends a `describe` request during discovery and a `normalize` request for provider input:

```json
{"protocol":"turnal-adapter","version":1,"id":"capture","method":"normalize","hook":"AfterTool","payload":{"session_id":"demo","cwd":"/workspace","tool_name":"write_file"}}
```

An adapter emits zero or more `event` responses. A single after-tool hook normally becomes a `tool.call` followed by a `tool.result`:

```json
{"protocol":"turnal-adapter","version":1,"id":"capture","type":"event","event":{"type":"tool.call","session_id":"demo","cwd":"/workspace","tool_name":"write_file","input":{"path":"README.md"}}}
```

The supported event types are `session.start`, `prompt.user`, `tool.call`, `tool.result`, `assistant.message`, and `turn.finish`. `session_id` and an absolute `cwd` are required on every event. Tool events also require `tool_name`; `source_id`, `provider_turn_id`, `tool_use_id`, model metadata, transcript paths, and JSON input/output are optional.

The Go SDK lives at `github.com/AadiJo/turnal/sdk/adapter`. Its `Serve` function implements framing, version negotiation, request validation, response encoding, and event validation. Protocol conformance transcripts are in [`sdk/adapter/testdata/conformance/v1`](../sdk/adapter/testdata/conformance/v1). Adapters must write protocol data only to stdout; diagnostics belong on stderr.

## Included adapters

Release packages include `turnal-adapter-opencode`, `turnal-adapter-gemini-cli`, and `turnal-adapter-copilot-cli`. Source installs can build all commands together:

```sh
go install github.com/AadiJo/turnal/cmd/...@latest
turnal adapter list
turnal adapter doctor
```

`list` shows discovered executables and their advertised versions. `doctor` performs a protocol-v1 handshake and checks that the executable name matches its manifest. A specific installation can be checked with `turnal adapter doctor gemini-cli`.

Provider hooks pipe their JSON payload to the hidden capture bridge. The bridge always returns successful, valid hook output after reporting capture failures to stderr so recording cannot block the agent.

### Gemini CLI

Add the following commands under `hooks` in `.gemini/settings.json` (or merge them with existing hook groups):

```json
{
  "hooks": {
    "SessionStart": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli SessionStart"}]}],
    "BeforeAgent": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli BeforeAgent"}]}],
    "AfterTool": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli AfterTool"}]}],
    "AfterAgent": [{"matcher":"*","hooks":[{"name":"turnal","type":"command","command":"turnal adapter capture gemini-cli AfterAgent"}] }]
  }
}
```

### Copilot CLI

Create or merge `.github/hooks/turnal.json`. Both camelCase and VS Code-compatible PascalCase payloads are accepted; this example uses the cross-platform `command` field:

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [{"type":"command","command":"turnal adapter capture copilot-cli sessionStart"}],
    "userPromptSubmitted": [{"type":"command","command":"turnal adapter capture copilot-cli userPromptSubmitted"}],
    "postToolUse": [{"type":"command","command":"turnal adapter capture copilot-cli postToolUse"}],
    "agentStop": [{"type":"command","command":"turnal adapter capture copilot-cli agentStop"}]
  }
}
```

Copilot's stop hook does not expose assistant text, so the adapter emits `turn.finish`; Turnal still creates the post-turn checkpoint.

### OpenCode

OpenCode plugins can forward their event and tool callbacks. Save this as `.opencode/plugins/turnal.js`:

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
    "tool.execute.after": async (input, output) =>
      capture("tool.execute.after", { ...input, output }),
  }
}
```

OpenCode message and session events are normalized through the `event` callback; completed tool calls use `tool.execute.after`.

## Compatibility and safety

Protocol versions are explicit and adapters advertise every supported version in their manifest. Turnal rejects unknown versions, malformed NDJSON, mismatched request IDs, invalid event types, non-absolute workspaces, cross-session batches, excessive lines, and processes that exceed the handshake timeout. Provider input is retained under the existing raw-adapter policy; prompt and tool aliases used by the included adapters are recursively redacted when the corresponding secrets settings are disabled.

