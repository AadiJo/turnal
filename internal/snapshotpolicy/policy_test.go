package snapshotpolicy

import "testing"

func TestDeniedMatchesRepositoryGlobs(t *testing.T) {
	patterns := []string{".env", ".env.*", "**/.env", "**/credentials.*"}
	for _, candidate := range []string{".env", ".env.local", "nested/.env", "config/credentials.json"} {
		if !Denied(candidate, patterns) {
			t.Fatalf("Denied(%q) = false", candidate)
		}
	}
	if Denied("src/app.go", patterns) {
		t.Fatal("Denied(src/app.go) = true")
	}
}
