# Retention and deletion

`turnal session drop <session>` removes the session event log, active/checkpoint state, per-session v2 raw logs, and private refs, redacts matching payloads in legacy v1 raw logs without shifting references, and invalidates the disposable search index.

Workspace-level checkpoints created by `turnal save` do not belong to an agent session, so dropping a session does not remove them.

Shared history is append-only review material outside the private session-deletion boundary. Whenever sharing is configured, `turnal session drop` conservatively reports that boundary because local, pulled, or teammate copies may contain the session; it does not rewrite the shared-history Git repository or delete teammate copies. `turnal share disable --yes` stops future synchronization while preserving existing bundles; it cannot recall a bundle that was already pushed or pulled.

Hidden Git objects may remain reachable through reflogs or as unreachable objects until garbage collection. For immediate local cleanup after confirming the drop, run:

```sh
turnal retention prune
turnal maintenance gc
turnal reindex
```

Filesystem, volume, backup, endpoint-protection, or cloud-sync copies are outside Turnal's deletion boundary. Treat deletion as logical and repository-local unless those systems are also purged according to company policy.

The isolated shared-history outbox and pulled materializations are not pruned by `turnal retention prune` or hidden-Git garbage collection. They currently grow with published and pulled history.

Raw hook payloads are limited to 8 MiB. Oversize inputs fail open for agent continuity, emit a stderr diagnostic, and create a failure record visible in `turnal status` when the workspace can be identified.
