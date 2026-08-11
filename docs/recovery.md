# Recovery runbook

Turnal writes crash-safe checkpoint and rollback journals before destructive transitions. If `turnal status` reports an interrupted rollback, do not delete `.turnal/tmp/rollback-journal.json` manually.

Inspect the recorded target and safety snapshot:

```sh
turnal recovery status
```

To deliberately reapply the target and finalize the rollback:

```sh
turnal recovery resume --yes
```

To abandon the target and restore the snapshot captured immediately before rollback:

```sh
turnal recovery restore-safety --yes
```

The `restoring` phase is ambiguous after a crash, so Turnal never chooses between these actions automatically. Back up the current worktree before recovery if it contains changes made after the interruption.

Hook capture is fail-open. A capture failure does not block the agent, but it prints a warning and is retained in the health ledger. After investigation, acknowledge it with `turnal maintenance clear-hook-failures --yes`.

Shared-history publication uses a separate crash-safe Git outbox. A failed or interrupted push is recovered by rerunning `turnal sync push`; do not edit `.turnal/shared-history/repository/` or `state.json` manually. A quarantined publisher remains pinned to the last verified head while other publishers continue to synchronize; inspect the reason with `turnal share status` before repairing remote refs. For an intentionally retired device ref, `turnal share forget-device <device-id> --yes` acknowledges its absence without discarding that pinned head.
