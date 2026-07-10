# Retention and deletion

`turnal session drop <session>` removes the session event log, active/checkpoint state, per-session v2 raw logs, and private refs, redacts matching payloads in legacy v1 raw logs without shifting references, and invalidates the disposable search index.

Hidden Git objects may remain reachable through reflogs or as unreachable objects until garbage collection. For immediate local cleanup after confirming the drop, run:

```sh
turnal retention prune
turnal maintenance gc
turnal reindex
```

Filesystem, volume, backup, endpoint-protection, or cloud-sync copies are outside Turnal's deletion boundary. Treat deletion as logical and repository-local unless those systems are also purged according to company policy.

Raw hook payloads are limited to 8 MiB. Oversize inputs fail open for agent continuity, emit a stderr diagnostic, and create a failure record visible in `turnal status` when the workspace can be identified.
