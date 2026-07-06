package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateAgentConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "missing-agent-vcs-config.toml")
	t.Setenv("AGENT_VCS_CONFIG", path)
	return path
}

func writeGlobalAgentConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-vcs-config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write global agent-vcs config: %v", err)
	}
	t.Setenv("AGENT_VCS_CONFIG", path)
	return path
}
