# Compatibility and support

Turnal supports the Go version declared in `go.mod` and the prebuilt npm platforms listed by the release build. CI executes tests on current Ubuntu, macOS, and Windows runners and builds the CLI on each.

Metadata readers remain backward compatible with v1 global raw references (`adapter:line`). New writes use v2 per-session references (`v2:session:adapter:sequence`). Checkpoints created before permission manifests were introduced restore Git's basic `0644`/`0755` modes; newer checkpoints restore the captured POSIX permission bits exactly on platforms that support them.

Agent hook formats are external integration points and may change with Claude Code or Codex releases. `turnal status` is the supported way to identify an unhealthy or incompatible hook installation.

Repository verifiers are launched as an executable plus an exact argument vector on every supported platform; shell command strings are not supported. Exit codes, launch failures, and timeouts are normalized in the versioned report. Timeouts use Windows Job Objects or Unix process groups; deliberately detached Unix descendants require stronger OS-level containment than Turnal currently provides, so output-pipe waiting is separately bounded.

Company-wide use should be piloted on representative repositories before broad rollout. Pin a Turnal version, test rollback and recovery, define retention expectations, and monitor hook-failure diagnostics during the pilot.
