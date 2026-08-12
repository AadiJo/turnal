# Shared history

Turnal shared history publishes review context without publishing the exact local evidence used for rollback and replay. It is disabled until a repository has an explicit remote and prompt policy, and it will not push until the current policy hash has been previewed and approved.

## Configure and publish

The remote must be a trusted Git URL or filesystem path reachable by Git from the current machine. It must accept custom refs under `refs/turnal/`. Projection limits bound data Turnal accepts and publishes; they do not sandbox Git's packfile transport, so use a dedicated remote with server-side storage quotas when publishers are not equally trusted.

```sh
turnal share enable \
  --remote <git-url-or-path> \
  --prompt-mode redacted_text

turnal share preview <session>:<turn> --json
turnal share preview <session>:<turn> --approve
turnal sync push --dry-run
turnal sync push
```

`redacted_text` publishes prompt text after workspace-path normalization, secret scanning, and deterministic size limits. `omit` publishes a typed prompt omission instead of prompt text but retains redacted assistant and compact intent text, so it withholds one field rather than materially reducing scanner exposure. `metadata_only` omits prompt, assistant, intent, and source-branch text while retaining lifecycle, tool classification, and checkpoint metadata. Changing the remote, prompt mode, schema, scanner, allowlist, or limits changes the policy hash and requires approval again.

Workspace paths normalize to `$WORKSPACE` only when their boundary is unambiguous: the root ends the field, continues through a path separator without a `..` component, or is enclosed by matching quotes. Because Unix permits whitespace and punctuation inside filenames, an unquoted workspace-root mention followed by either is ambiguous and causes the entire field to become `[PATH_REDACTED]`. Any other absolute path also redacts the entire field. This fail-closed rule trades some shared prose for a stable privacy boundary; quote a standalone path when precise normalization matters.

Zero-width and other invisible characters are removed before scanning, so text is matched as a reader sees it. Otherwise a zero-width space in front of a separator would hide a path from the boundary rules and a later display pass would reassemble it.

### Redaction policy diagnostics

The version 3 scanner is an ordered detector pipeline rather than one regex list. It combines high-entropy scoring, Betterleaks rules, deterministic provider-token formats, credentialed URLs, database connection strings, and bounded credential assignments. Overlapping findings become one replacement, while the manifest records aggregate `secret` counts and bounded `secret:<detector>` diagnostics. Private-key headers fail closed by redacting the complete field. Placeholder credentials such as `${DB_PASSWORD}`, `changeme`, and mask runs are left visible so examples remain reviewable.

Run the local policy diagnostic before approving a scanner migration:

```sh
turnal share redaction diagnose
turnal share redaction diagnose --json
```

The command names every detector, runs embedded leak and safe-text golden corpora, reports false positives and false negatives separately, and compares the compiled scanner with the configured policy. It does not contact the shared-history remote. A scanner change requires `turnal share enable` and a fresh preview approval after any older outbox has drained.

Teams can review project-specific examples with one or more strict JSONL corpora:

```json
{"id":"internal-token","text":"ACME_API_KEY=synthetic-review-value","expect":"redact"}
{"id":"product-copy","text":"Rotate the token bucket hourly","expect":"allow"}
```

```sh
turnal share redaction review redaction-leaks.jsonl redaction-safe.jsonl
turnal share redaction review redaction-leaks.jsonl --json
```

Each case needs a terminal-safe unique `id`, a `text` value no larger than 64 KiB, and an `expect` value of `redact` or `allow`. The command exits nonzero for either a false positive or a false negative. Its human and JSON reports include only case ids, outcomes, and detector ids, never the reviewed source text. This flow diagnoses scanner behavior; it does not create an exception that can weaken publication policy.

`turnal share status` prints the shared repository id. A teammate with an independently initialized clone joins that history by supplying the publisher's id explicitly:

```sh
turnal share enable \
  --remote <same-git-url-or-path> \
  --repo-id <publisher-repo-id> \
  --prompt-mode omit
turnal sync pull
```

The explicit id prevents a remote that contains history for another project from being silently adopted. It identifies the shared project without replacing the clone's private Turnal store identity.

`preview --json` is the complete bundle projection: signed manifest, projected events, omissions, truncations, evidence class, source links, and stable locator. Preview remains important because secret scanning is best-effort and allowed text can contain source fragments. Novel low-entropy secrets can still evade automated detection.

Approval applies to the policy hash, not only to the previewed turn. `sync push --dry-run` lists every pending turn, its locator and projected size, the next bounded batch, and any blocked projection before anything contacts the remote. Preview and dry-run also distinguish path and secret redactions from typed omissions.

If imported history contains the same session and turn number in more than one event stream, preview reports the ambiguity. Select the intended source with `turnal share preview <session>:<turn> --stream <stream-id>`.

The locator can also connect a source commit to its context without putting published data in the source repository:

```sh
git commit --trailer "Turnal-History: v1:<device-id>:<bundle-id>"
```

The manifest records the source Git heads observed at checkpoints independently of that trailer. A locator may exist before its bundle is pushed; that is pending publication, not corruption.

## Pull and inspect

```sh
turnal sync pull
turnal sync status
turnal share list
turnal share show v1:<device-id>:<bundle-id> --json
```

Pull writes verified bundles beneath `.turnal/shared-history/pulled/<repo-id>/`. It does not change the workspace, the project's `.git/`, or Turnal's private checkpoint repository. The RepoID namespace keeps reconfigured project scopes separate. The pulled JSON files are a derived local materialization; signed Git history remains the transport source.

`share list` discovers local and pulled locators, with optional `--session`, `--device`, and `--commit` filters. Each row names the bundle's source commit and branch, and `--commit` accepts a SHA or prefix so a reviewer can move from a commit to its context. When sharing is configured, `turnal status` reports a one-line publication summary covering pending, unapproved, blocked, and quarantined state. `share show` renders the projected context for people by default; use `--json` for the complete signed representation.

Each publishing device owns an advancing ref:

```text
refs/turnal/v1/history/<device-id>
```

The device id is derived from its Ed25519 public key. Publication batches and bundle manifests are signed. Turnal remembers every observed device head and rejects a rewind, replacement, disappearance, merge commit, key substitution, changed bundle, or invalid content hash. This makes history tamper-evident after observation; it does not claim that the Git server is globally append-only.

A signature proves who published a bundle, not that the bundle is honest, so receivers also enforce the policy a manifest declares. A bundle labeled `metadata_only` is rejected when it carries intent text, assistant text, or a source branch, even when the publisher signed it consistently. Bounded reads abort the underlying Git command instead of draining an oversize object, and remote-supplied text is escaped before it reaches a terminal.

A malformed ref outside the exact protocol namespace is ignored and reported as a warning. A publisher whose previously observed ref disappears or fails verification is quarantined without advancing its observation cursor; healthy publishers continue to pull and their progress is saved. The command reports that partial result and exits nonzero while any quarantine remains. `turnal share status` reports quarantined device ids and reasons so the failure is never silent. If a teammate intentionally retired and deleted a device ref, acknowledge that specific disappearance with `turnal share forget-device <device-id> --yes`; Turnal keeps its last verified head pinned so a later reappearance must still extend the trusted history.

## Publication boundary

The version 1 schema can contain the fields below. Shared history v1 is introduced by this PR and had no released receiver before the `redactions` metadata, `metadata_only` policy, source branch, and projection versions were included; its signing bytes are frozen from this complete initial contract. Later additive fields are optional in the signing payload, so a bundle that omits them signs and verifies exactly as it did before they existed.

- turn lifecycle events;
- prompt text or a typed prompt omission, according to policy;
- compact agent intent, scope, and evidence strings;
- assistant text;
- tool name, category, status, and mutation classification;
- checkpoint, source-commit, and source-branch references;
- the allowlist, scanner, and Turnal versions that produced the projection;
- a typed capture-error marker.

Source branch names are published under `redacted_text` and `omit` because review questions are usually branch-shaped. `metadata_only` publishes no source naming and records a typed `branch_policy` omission instead. A detached HEAD has no branch to name. Branch names are author-controlled, so a name containing anything outside letters, digits, `.`, `_`, `/`, and `-` is dropped as `invalid_branch` rather than normalized, and receivers reject a bundle whose branch falls outside that set.

`allowlist_version` and `scanner_version` name the projection contract, and `producer_version` names the Turnal build that applied it. The policy hash is opaque to a receiver, so these make it possible to tell which bundles predate a scanner upgrade without asking the publisher to re-derive them. Bundles published before these fields existed report an unknown projection rather than defaulting to the current one.

It cannot contain snapshots, patches, file bodies, raw provider payloads, tool commands, stdin, stdout, or stderr. Unknown and malformed source events are omitted and counted. Fields are limited to 64 KiB and an uncompressed bundle is limited to 2 MiB. A bundle that cannot pass projection is marked blocked while other eligible turns continue.

Every manifest labels its evidence as `publisher_attested_projection`. Source event hashes are correlation references so the publishing device can re-derive the projection from private history. They are not third-party proof of source bytes that were never published.

## Failure and recovery

The isolated repository beneath `.turnal/shared-history/repository/` is a crash-safe local outbox. A network failure leaves its commit queued for a later `turnal sync push`. If Turnal stops after committing a batch but before updating local state, the next push reconstructs the outbox from the signed local tip.

Turnal will not change the remote or privacy policy while that outbox is pending, because the queued projection was approved under the previous policy. Publish it first. After the outbox is clear, changing the remote requires both `share enable --include-existing-history` and a new preview approval. The next push copies the device's existing approved Git history to the new remote even when no new turns are pending. That existing history is copied unchanged; a new prompt mode applies only to turns that have not already been published.

One publication batch contains at most 16 turns and 16 MiB of encoded bundle data. `turnal sync push --dry-run` distinguishes an already-queued outbox from the next new batch and follows the same first-overflow boundary as a real push. If more publishable turns remain, rerun `turnal sync push`; permanently blocked turns are reported separately rather than keeping `remaining` above zero. Pull advances each publishing device by one verified batch per run, so rerun `turnal sync pull` until it reports `pulled: 0`. Remote URL credentials and query parameters are retained only in the private policy used for Git transport and are redacted from status, consent hashes, and Git diagnostics.

After a scanner or allowlist upgrade, an outbox already approved under the prior projection can still be drained with `turnal sync push`; Turnal will not project new turns under that older policy. Then rerun `turnal share enable` and approve the updated policy.

`turnal share status` is local-only and fast by default. Use `turnal share status --check-remote` for a bounded remote comparison. Shared-history sync commands are interruptible and use bounded command contexts.

To stop future synchronization without deleting local inspection data, run `turnal share disable --yes`. This takes effect immediately and preserves any queued outbox locally; re-running `share enable` resumes the same policy and makes that queue eligible for a later push. Disabling cannot recall history that another device already fetched.

Shared-history Git operations use the configured remote directly, scrub inherited `GIT_*` variables, and never update source Git refs. `turnal sync push` reports transport failures; normal Turnal capture and project Git commands do not invoke it.

Credential rotation comes before history cleanup after a secret incident. Deleting remote history cannot recall copies another consumer already fetched.

`turnal session drop` removes private source history but deliberately does not rewrite append-only shared history. When sharing is configured, it reports shared history as a residual deletion boundary. Published copies and teammate materializations remain outside session deletion.
