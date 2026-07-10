# Release checklist

- Confirm `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting, module tidy, vulnerability scanning, npm tests, and the OS matrix pass.
- Run the authenticated Codex test explicitly with `TURNAL_LIVE_CODEX_TEST=1` in a trusted disposable repository.
- Exercise checkpoint and workspace-Git rollback, then inject a `restoring` journal and verify both recovery choices.
- Verify `.env`, nested credentials, symlinks, and custom deny globs do not enter checkpoint or git-sync captures.
- Verify session deletion removes v2 raw data, redacts legacy payloads, invalidates search, and reports residual hidden-Git data.
- Review the generated SBOM and npm provenance, package contents, changelog, support statement, and security disclosures.
- Test upgrade from the previous stable metadata version and confirm pre-manifest checkpoints still restore.

## Telemetry release gate

- Keep the endpoint empty and rollout at zero unless the named evidence record in [`telemetry-rollout.md`](telemetry-rollout.md) is complete.
- Verify explicit opt-in, first-notice no-write behavior, `DO_NOT_TRACK`, CI/dev overrides, JSON/hidden-command silence, and immediate off/reset queue deletion.
- Run client privacy golden tests, collector mutation/crash/replay tests, the 30-installation dashboard fixture, synthetic canary reconciliation, and the complete deletion rehearsal.
- Confirm the exact release linker values, collector 410 kill switch, persistent outbox capacity, PostHog IP discard, 90-day raw retention, least-privilege access, and export retention.
- For a 10% or 100% release, attach the prior cohort's full observation window and owner sign-offs; never promote solely because the build is ready.
