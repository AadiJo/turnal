# CI provider switching

The repository keeps test and release behavior in `scripts/ci/`; CircleCI and GitHub Actions supply only triggers, executors, dependency wiring, and credentials. A provider change should not alter release metadata, package contents, SBOM generation, or GitHub release arguments.

## CircleCI is active

CircleCI automatically runs `Quality and race`, `Test (linux)`, `Test (macos)`, and `Test (windows)` on branch and tag pipelines. Stable publication is filtered to semver-shaped tags and requires all four jobs. A manual nightly pipeline must target `main`, set `publish_nightly` to `true`, and pass a second complete set of gates.

Before the first release:

1. Create a CircleCI context named `turnal-release`, restrict it to this project, and add the expression restriction `not job.ssh.enabled`.
2. Add a least-privilege `GH_TOKEN` capable of creating releases in this repository to that context.
3. Register CircleCI as the trusted publisher for `@aadijo/turnal`. Bind the npm configuration to the CircleCI organization ID, project ID, pipeline definition ID, `github.com/AadiJo/turnal`, and the `turnal-release` context ID.
4. Require the four exact CircleCI checks listed in the release checklist on `main`, with current-branch checks and administrative enforcement enabled.

CircleCI trusted publishing uses `NPM_ID_TOKEN` and deliberately omits `--provenance` because npm provenance is not currently supported for this provider. The GitHub Actions fallback retains `NPM_PUBLISH_PROVENANCE=true` so provenance returns with that publisher.

## Return to GitHub Actions

Make the cutover in one pull request so only one provider can publish a given ref:

1. Restore the `push` and `pull_request` triggers in `.github/workflows/ci.yml`; keep the existing `workflow_dispatch` trigger for rehearsals and nightlies.
2. Disable the `Publish stable release` and `Publish nightly release` invocations in `.circleci/config.yml` before merging. CircleCI validation jobs may remain as redundant signals.
3. Change npm trusted publishing from the CircleCI pipeline to the GitHub Actions `release.yml` workflow.
4. Replace the four required CircleCI contexts with `Quality and race` plus the three GitHub platform contexts only after those GitHub jobs have completed successfully on the cutover commit.
5. Manually dispatch GitHub Actions with both publication inputs disabled, then verify the validation matrix before creating the next release tag. Stable fallback publication requires selecting the tag and explicitly setting `publish_stable` to `true`.

Never leave both stable publish paths enabled. Both are fail-closed behind their own gates, but concurrent publishers would race on the same immutable npm version and GitHub tag.
