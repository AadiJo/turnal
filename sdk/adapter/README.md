# Turnal adapter SDK

This package is the public Go SDK for `turnal-adapter-*` plugins. A plugin is a standalone executable that translates one provider's hook payloads into Turnal's provider-neutral NDJSON protocol. It cannot write Turnal events or checkpoints directly.

Implement a manifest and normalizer, then serve them with `adapter.Serve`:

```go
package main

import (
	"encoding/json"
	"os"

	"github.com/AadiJo/turnal/sdk/adapter"
)

func main() {
	manifest := adapter.Manifest{
		Name: "example", DisplayName: "Example", AdapterVersion: "1.0.0", Provider: "Example",
		ProtocolVersions: []int{adapter.ProtocolVersion},
		EventTypes: []string{string(adapter.EventPromptUser)},
	}
	_ = adapter.Serve(os.Stdin, os.Stdout, manifest, func(_ string, raw json.RawMessage) ([]adapter.Event, error) {
		return []adapter.Event{{Type: adapter.EventPromptUser, SessionID: "session-id", CWD: "/workspace"}}, nil
	})
}
```

Name the installed executable `turnal-adapter-example`, then run `turnal adapter doctor example`. The complete contract, lifecycle fields, security boundary, and conformance fixtures are documented in [`docs/adapters.md`](../../docs/adapters.md). An installed Turnal also exposes the contract through `turnal adapter contract --json`.
