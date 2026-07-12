# Compatibility and support

Turnal supports the Go version declared in `go.mod` and the prebuilt npm platforms listed by the release build. CI executes tests on current Ubuntu, macOS, and Windows runners and builds the CLI on each.

Metadata readers remain backward compatible with v1 global raw references (`adapter:line`). New writes use v2 per-session references (`v2:session:adapter:sequence`). Checkpoints created before permission manifests were introduced restore Git's basic `0644`/`0755` modes; newer checkpoints restore the captured POSIX permission bits exactly on platforms that support them.

Agent hook formats are external integration points and may change with Claude Code or Codex releases. `turnal status` checks project hook files offline, while `turnal status --probe-agent-capture` also asks Codex app-server which Turnal hooks it discovered and whether it will execute them. The probe performs no agent turn, sends no prompt, changes no workspace files, and never changes provider project or hook trust.

Installed hooks do not prove that every host will load or run them. Claude Code loads project hooks from `.claude/settings.json`. A Claude Agent SDK host must omit `settingSources` or include `"project"`; `settingSources: []` excludes project hooks, and Turnal cannot infer an arbitrary host's choice from workspace state. Turnal does not currently consume the SDK message stream directly.

Codex CLI loads project hooks from `.codex/config.toml`. Codex app-server can discover enabled hooks but skip them while their exact definitions remain untrusted, so review the project and definitions in the Codex hooks UI before granting trust. Turnal reports that state but does not write provider trust databases. `turnal run -- codex` still provides wrapper checkpoints when rich hooks are unavailable; an app-server host does not automatically use that wrapper.

Company-wide use should be piloted on representative repositories before broad rollout. Pin a Turnal version, test rollback and recovery, define retention expectations, and monitor hook-failure diagnostics during the pilot.
