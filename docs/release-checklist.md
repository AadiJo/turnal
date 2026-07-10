# Release checklist

- Confirm `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting, module tidy, vulnerability scanning, npm tests, and the OS matrix pass.
- Run the authenticated Codex test explicitly with `TURNAL_LIVE_CODEX_TEST=1` in a trusted disposable repository.
- Exercise checkpoint and workspace-Git rollback, then inject a `restoring` journal and verify both recovery choices.
- Verify `.env`, nested credentials, symlinks, and custom deny globs do not enter checkpoint or git-sync captures.
- Verify session deletion removes v2 raw data, redacts legacy payloads, invalidates search, and reports residual hidden-Git data.
- Review the generated SBOM and npm provenance, package contents, changelog, support statement, and security disclosures.
- Test upgrade from the previous stable metadata version and confirm pre-manifest checkpoints still restore.
