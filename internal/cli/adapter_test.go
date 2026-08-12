package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAdapterContractIsDiscoverable(t *testing.T) {
	output := runRootStdout(t, "adapter", "contract")
	for _, want := range []string{"turnal-adapter-<name>", "github.com/AadiJo/turnal/sdk/adapter", "parent_session_id"} {
		if !strings.Contains(output, want) {
			t.Fatalf("contract output missing %q:\n%s", want, output)
		}
	}

	var contract adapterContract
	if err := json.Unmarshal([]byte(runRootStdout(t, "adapter", "contract", "--json")), &contract); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if contract.SchemaVersion != adapterContractSchemaVersion || contract.ProtocolVersion != 1 {
		t.Fatalf("contract = %+v", contract)
	}
	if contract.SessionTopology.ParentSessionField != "parent_session_id" || len(contract.EventTypes) != 6 {
		t.Fatalf("contract = %+v", contract)
	}
}
