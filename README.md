# Turnal

Turnal is a local-first flight recorder for AI coding agents. It records what happened, why it happened, what files changed, and how to roll back safely.

## Installation

```sh
npm install -g @aadijo/turnal
```

The npm package ships prebuilt binaries for macOS, Linux, and Windows on x64 and arm64. Go is only required when installing from source or when using an unsupported npm platform.

## Usage

```sh
turnal --help
turnal init
turnal status
```

## Development

```sh
go test ./...
go build -o bin/turnal ./cmd/turnal
```

## License

Apache-2.0
