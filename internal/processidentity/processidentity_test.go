package processidentity

import (
	"os"
	"testing"
)

func TestCurrentProcessIdentityMatchesKernelState(t *testing.T) {
	identity, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID != os.Getpid() || identity.Started == "" {
		t.Fatalf("identity = %+v", identity)
	}
	matches, err := Matches(identity.PID, identity.Started)
	if err != nil || !matches {
		t.Fatalf("Matches() = %v, %v", matches, err)
	}
	matches, err = Matches(identity.PID, identity.Started+"-forged")
	if err != nil || matches {
		t.Fatalf("forged Matches() = %v, %v", matches, err)
	}
}
