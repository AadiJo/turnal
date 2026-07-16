# Release checklist

- Confirm `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting, module tidy, vulnerability scanning, npm tests, and the OS matrix pass.
- Run the authenticated Codex test explicitly with `TURNAL_LIVE_CODEX_TEST=1` in a trusted disposable repository.
- Exercise checkpoint and workspace-Git rollback, then inject a `restoring` journal and verify both recovery choices.
- Verify `.env`, nested credentials, symlinks, and custom deny globs do not enter checkpoint or git-sync captures.
- Verify session deletion removes v2 raw data, redacts legacy payloads, invalidates search, and reports residual hidden-Git data.
- Review the generated SBOM and npm provenance, package contents, changelog, support statement, and security disclosures.
- Test upgrade from the previous stable metadata version and confirm pre-manifest checkpoints still restore.

## Automated release gates

- Stable releases start only from a `v*.*.*` tag. The CI workflow runs quality checks and the Linux, macOS, and Windows matrix at the tagged commit before it can call the reusable publish workflow.
- Nightly releases start from a manual CI dispatch on `main` with `publish_nightly` enabled. Dispatches from any other ref run checks but cannot publish.
- Failed, cancelled, or missing prerequisite jobs leave the publish job skipped. Do not bypass that state by adding a direct dispatch trigger to `release.yml`.
- npm publication uses GitHub OIDC trusted publishing from `release.yml`; the workflow also emits provenance, builds all supported npm binaries, and attaches the generated SBOM to the GitHub release.
- CircleCI is an independent Linux quality signal only. It has no npm or GitHub release credentials and no publishing workflow.
