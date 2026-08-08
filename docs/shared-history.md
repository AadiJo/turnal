# Shared history

Turnal shared history publishes review context without publishing the exact local evidence used for rollback and replay. It is disabled until a repository has an explicit remote and prompt policy, and it will not push until the current policy hash has been previewed and approved.

## Configure and publish

The remote must be a Git URL or filesystem path reachable by Git from the current machine. It must accept custom refs under `refs/turnal/`.

```sh
turnal share enable \
  --remote <git-url-or-path> \
  --prompt-mode redacted_text

turnal share preview <session>:<turn> --json
turnal share preview <session>:<turn> --approve
turnal sync push
```

`redacted_text` publishes prompt text after workspace-path normalization, secret scanning, and deterministic size limits. `omit` publishes a typed prompt omission instead of prompt text. Changing the remote, prompt mode, schema, scanner, allowlist, or limits changes the policy hash and requires approval again.

`preview --json` is the complete bundle projection: signed manifest, projected events, omissions, truncations, evidence class, source links, and stable locator. Preview remains important because secret scanning is best-effort and allowed text can contain source fragments.

The locator can also connect a source commit to its context without putting published data in the source repository:

```sh
git commit --trailer "Turnal-History: v1:<device-id>:<bundle-id>"
```

The manifest records the source Git heads observed at checkpoints independently of that trailer. A locator may exist before its bundle is pushed; that is pending publication, not corruption.

## Pull and inspect

```sh
turnal sync pull
turnal sync status
turnal share show v1:<device-id>:<bundle-id> --json
```

Pull writes verified bundles beneath `.turnal/shared-history/pulled/`. It does not change the workspace, the project's `.git/`, or Turnal's private checkpoint repository. The pulled JSON files are a derived local materialization; signed Git history remains the transport source.

Each publishing device owns an advancing ref:

```text
refs/turnal/v1/history/<device-id>
```

The device id is derived from its Ed25519 public key. Publication batches and bundle manifests are signed. Turnal remembers every observed device head and rejects a rewind, replacement, disappearance, merge commit, key substitution, changed bundle, or invalid content hash. This makes history tamper-evident after observation; it does not claim that the Git server is globally append-only.

## Publication boundary

The version 1 schema can contain:

- turn lifecycle events;
- prompt text or a typed prompt omission, according to policy;
- compact agent intent, scope, and evidence strings;
- assistant text;
- tool name, category, status, and mutation classification;
- checkpoint and source-commit references;
- a typed capture-error marker.

It cannot contain snapshots, patches, file bodies, raw provider payloads, tool commands, stdin, stdout, or stderr. Unknown and malformed source events are omitted and counted. Fields are limited to 64 KiB and an uncompressed bundle is limited to 2 MiB. A bundle that cannot pass projection is marked blocked while other eligible turns continue.

Every manifest labels its evidence as `publisher_attested_projection`. Source event hashes are correlation references so the publishing device can re-derive the projection from private history. They are not third-party proof of source bytes that were never published.

## Failure and recovery

The isolated repository beneath `.turnal/shared-history/repository/` is a crash-safe local outbox. A network failure leaves its commit queued for a later `turnal sync push`. If Turnal stops after committing a batch but before updating local state, the next push reconstructs the outbox from the signed local tip.

Shared-history Git operations use the configured remote directly, scrub inherited `GIT_*` variables, and never update source Git refs. `turnal sync push` reports transport failures; normal Turnal capture and project Git commands do not invoke it.

Credential rotation comes before history cleanup after a secret incident. Deleting remote history cannot recall copies another consumer already fetched.
