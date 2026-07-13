//go:build !linux && !windows && !darwin

package processidentity

import (
	"errors"
	"os"
	"testing"
)

func TestUnsupportedPlatformFailsClosed(t *testing.T) {
	if _, err := startedAt(os.Getpid()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("startedAt() error = %v, want ErrUnsupported", err)
	}
}
