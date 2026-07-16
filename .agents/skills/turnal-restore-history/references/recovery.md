# Interrupted rollback recovery

Use this workflow when `turnal status` reports a pending rollback journal or a rollback error says a safety checkpoint was created.

## Preserve evidence

Do not delete `.turnal/tmp/rollback-journal.json`, edit Turnal refs, rerun rollback, or manually copy checkpoint files. Preserve the reported safety ref and commit until recovery is complete.

## Inspect first

```sh
turnal recovery status
```

Record the journal phase, intended target, rollback mode, safety ref, and safety commit. Inspect the live workspace enough to decide whether to finish the intended target restore or return to the pre-rollback safety snapshot.

Neither recovery action is implied by a request to diagnose the interruption. Obtain explicit user direction because both choices replace live workspace state.

## Finish the intended rollback

Use this when the user wants to reapply the original target and finalize its journal:

```sh
turnal recovery resume --yes
```

This may reapply a partially completed restore. Do not run it before reviewing `recovery status`.

## Abandon the target and return to safety

Use this when the user wants to abandon the target restore and return to the workspace snapshot captured immediately before rollback:

```sh
turnal recovery restore-safety --yes
```

This replaces the current workspace with the safety snapshot. Do not confuse it with rolling back to the historical target.

## Verify completion

Run `turnal recovery status` again and confirm it reports `rollback recovery: none`. Then run `turnal status`, summarize which recovery path was taken, and retain the safety identifiers in the report if any follow-up investigation remains.
