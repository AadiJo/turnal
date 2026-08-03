package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateAgentConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "missing-turnal-config.toml")
	t.Setenv("TURNAL_CONFIG", path)
	// Also redirect machine-wide state so a test that initializes a store does
	// not append its temp directory to the developer's real project registry.
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	return path
}

func writeGlobalAgentConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "turnal-config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write global turnal config: %v", err)
	}
	t.Setenv("TURNAL_CONFIG", path)
	return path
}
