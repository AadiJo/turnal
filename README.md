# Turnal

Turnal is a local-first flight recorder for AI coding agents. It records what happened, why it happened, what files changed, and how to roll back safely.

Turnal should be piloted before company-wide adoption. Pin a version and validate hook compatibility, retention, and rollback behavior on representative repositories; see the [compatibility policy](docs/compatibility.md), [retention semantics](docs/retention.md), and [recovery runbook](docs/recovery.md).

## Installation

```sh
npm install -g @aadijo/turnal
```

The npm package ships prebuilt binaries for macOS, Linux, and Windows on x64 and arm64. Go is only required when installing from source or when using an unsupported npm platform.

## Upgrading

```sh
turnal upgrade
```

`turnal upgrade` preserves the current release channel. Use `turnal upgrade --stable` or `turnal upgrade --nightly` to switch channels explicitly.

For npm installs, Turnal may occasionally print a channel-preserving update notice after interactive commands. Set `TURNAL_NO_UPDATE_CHECK=1` to disable these notices.

## Usage

```sh
turnal --help
turnal init
turnal status
turnal recovery status
```

## Development

```sh
go test ./...
go build -o bin/turnal ./cmd/turnal
```

Authenticated provider testing is intentionally excluded from the default suite. Set `TURNAL_LIVE_CODEX_TEST=1` to run the live Codex integration test in a trusted disposable repository.

## Security

Workspace metadata can contain prompts, tool input/output, file history, and raw provider payloads. Review [SECURITY.md](SECURITY.md) before using Turnal with sensitive repositories.

## License

Apache-2.0
