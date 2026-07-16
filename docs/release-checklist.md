# Release checklist

- Confirm `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting, module tidy, vulnerability scanning, npm tests, and the OS matrix pass.
- Run the authenticated Codex test explicitly with `TURNAL_LIVE_CODEX_TEST=1` in a trusted disposable repository.
- Exercise checkpoint and workspace-Git rollback, then inject a `restoring` journal and verify both recovery choices.
- Verify `.env`, nested credentials, symlinks, and custom deny globs do not enter checkpoint or git-sync captures.
- Verify session deletion removes v2 raw data, redacts legacy payloads, invalidates search, and reports residual hidden-Git data.
- Review the generated SBOM, package contents, changelog, support statement, and security disclosures. CircleCI trusted publishing does not currently emit npm provenance; the retained GitHub Actions fallback does when it is the active publisher.
- Test upgrade from the previous stable metadata version and confirm pre-manifest checkpoints still restore.

## Automated release gates

- Stable releases start only from a semver-shaped `v*.*.*` tag. CircleCI runs quality checks and native Linux, macOS, and Windows jobs at the tagged commit before the stable publish job can start.
- Nightly releases start from a manually triggered CircleCI pipeline on `main` with the `publish_nightly` boolean parameter set to `true`. The nightly workflow repeats every required gate before publishing.
- Failed, cancelled, filtered, or missing prerequisite jobs prevent the corresponding publish job from starting. Do not add an independent publish workflow or attach release credentials to a validation job.
- npm publication uses CircleCI OIDC trusted publishing from the protected `turnal-release` context. The publish job builds every supported npm binary, performs a package dry run, generates an SPDX SBOM, publishes npm, and attaches the SBOM to the GitHub release.
- The `turnal-release` context must be restricted to this project and disallow SSH reruns. Its only stored credential is a least-privilege `GH_TOKEN`; npm authentication is a short-lived OIDC token.
- GitHub Actions is retained as a manual-only fallback. It calls the same repository scripts and keeps GitHub OIDC provenance enabled, but it must not be restored to automatic publishing until CircleCI publishing is disabled for the same refs.
- Protect `main` with the exact CircleCI contexts `ci/circleci: Quality and race`, `ci/circleci: Test (linux)`, `ci/circleci: Test (macos)`, and `ci/circleci: Test (windows)` after all four have reported successfully at least once.

See [CI provider switching](ci-provider-switching.md) for the account setup and the controlled path back to GitHub Actions.
