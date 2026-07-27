# CI provider switching

The repository keeps test and release behavior in `scripts/ci/`; GitHub Actions supplies only triggers, executors, dependency wiring, and credentials. A provider change must not alter release metadata, package contents, SBOM generation, or GitHub release arguments.

## GitHub Actions is active

GitHub Actions runs `Quality and race`, `Test (linux)`, `Test (macos)`, and `Test (windows)` for pushes, pull requests, and tags. Stable publication is filtered to version tags, excludes nightly tags, and requires all four jobs. A manual nightly run must target `main`, set `publish_nightly` to `true`, and pass a second complete set of gates. A manual release rehearsal must target `main`, set `rehearse_release` to `true`, pass the same gates, and execute the release build with publication disabled.

Before the first release:

1. Configure npm trusted publishing for `@aadijo/turnal` with repository `AadiJo/turnal` and workflow filename `ci.yml`. npm validates the calling workflow when `workflow_call` delegates publication to `release.yml`.
2. Require the four exact GitHub Actions checks listed in the release checklist on `main`, with current-branch checks and administrative enforcement enabled.
3. Manually dispatch GitHub Actions on `main` with `rehearse_release` enabled, then verify the validation matrix and release rehearsal before creating the next release tag.

## Provider differences

The validation commands, platform coverage, release gates, package contents, SBOM generation, and GitHub release arguments remain the same. The provider boundary cannot be byte-for-byte identical:

- GitHub-hosted Ubuntu, macOS, and Windows images replace CircleCI images and resource classes, so the operating-system revisions, CPU architecture or capacity, and preinstalled tools can differ.
- GitHub Actions caches Go dependencies with `actions/setup-go`; cache keys and storage paths differ from CircleCI even though a cache miss still falls back to `go mod download`.
- Pull requests have their own GitHub event and can also receive a branch push run, whereas CircleCI represented validation through branch and tag pipelines.
- npm publication uses GitHub OIDC trusted publishing with provenance instead of a CircleCI `NPM_TOKEN` without provenance.
- Nightly versions use the GitHub Actions run number, so their numeric suffix does not continue the CircleCI build-number sequence.

Keep only one active publication provider. Concurrent publishers would race on the same immutable npm version and GitHub tag.
