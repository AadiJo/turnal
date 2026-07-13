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

func TestDeniedMatchesDirectoryAncestor(t *testing.T) {
	patterns := []string{"secret-dir", "nested/private"}
	for _, candidate := range []string{"secret-dir", "secret-dir/key.txt", "secret-dir/deeper/key.txt", "nested/private/token"} {
		if !Denied(candidate, patterns) {
			t.Fatalf("Denied(%q) = false", candidate)
		}
	}
	for _, candidate := range []string{"public/secret-dir-name/key.txt", "nested/private-name/token"} {
		if Denied(candidate, patterns) {
			t.Fatalf("Denied(%q) = true", candidate)
		}
	}
}
