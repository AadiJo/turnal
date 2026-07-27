# Release checklist

- Confirm `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting, module tidy, vulnerability scanning, npm tests, and the OS matrix pass.
- Run the authenticated Codex test explicitly with `TURNAL_LIVE_CODEX_TEST=1` in a trusted disposable repository.
- Exercise checkpoint and workspace-Git rollback, then inject a `restoring` journal and verify both recovery choices.
- Verify `.env`, nested credentials, symlinks, and custom deny globs do not enter checkpoint or git-sync captures.
- Verify session deletion removes v2 raw data, redacts legacy payloads, invalidates search, and reports residual hidden-Git data.
- Review the generated SBOM, npm provenance, package contents, changelog, support statement, and security disclosures.
- Verify all four macOS/Linux standalone archives and `checksums.txt` are attached to the GitHub release, and confirm installer fixtures pass on Linux and macOS.
- Test upgrade from the previous stable metadata version and confirm pre-manifest checkpoints still restore.

## Automated release gates

- Stable releases start only from a version-shaped `v*.*.*` tag. GitHub Actions runs quality checks and native Linux, macOS, and Windows jobs at the tagged commit before the stable publish job can start.
- Nightly releases start from a manually dispatched GitHub Actions workflow on `main` with `publish_nightly` set to `true`. The nightly workflow repeats every required gate before publishing.
- Before enabling the first publisher, manually dispatch GitHub Actions on `main` with `rehearse_release` set to `true`. It repeats every gate, builds the package and SBOM, and exits before npm or GitHub publication.
- Failed, cancelled, filtered, or missing prerequisite jobs prevent the corresponding publish job from starting. Do not add an independent publish workflow or attach release credentials to a validation job.
- npm publication uses GitHub OIDC trusted publishing with provenance. The publish job builds every supported npm binary and standalone macOS/Linux archive, performs a package dry run, generates an SPDX SBOM and SHA-256 checksums, moves the npm dist-tag, and creates the GitHub release with the complete asset set.
- Configure npm trusted publishing for `@aadijo/turnal` with repository `AadiJo/turnal` and workflow filename `ci.yml`; npm validates the caller rather than the reusable `release.yml` workflow, and no long-lived npm token is stored in GitHub.
- Protect `main` with the exact GitHub Actions contexts `Quality and race`, `Test (linux)`, `Test (macos)`, and `Test (windows)` after all four have reported successfully at least once.

See [CI provider switching](ci-provider-switching.md) for the active setup and provider-level differences.
