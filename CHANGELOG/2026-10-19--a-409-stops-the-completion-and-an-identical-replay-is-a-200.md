A 409 stops the completion and an identical replay is the hub's 200

The hub now answers an identical replay of a task's latest evidence with **200 and the stored entry**,
and reserves 409 for a head_sha whose stored account differs (`base_sha`, `head_sha`, `numstat`,
`test.command/exit_code/failed`). The client's 409 continuation is deleted with it.

Why: the continuation was completing on evidence the local gate never judged. Two different accounts
of one head meant the FIRST was kept by the writer and the SECOND was what `complete` believed it was
completing on, so a criterion admitted against the local numstat could be judged against the stored
one.

Changes:
- `internal/clientcomplete/complete.go` - the `409 -> continue` branch and its warning are gone. Any
  evidence exit other than 0/5 prints `the task was not completed: the evidence was not stored; task
  <id> is unchanged` and returns without a completion call (exit 1 for a 409).
- `internal/clientevidence/evidence.go` - the 409 line names the new meaning: `head_sha <sha> already
  carries a different submission on this task; nothing was stored`. A 200 relays the stored event,
  which the verb already treats as stored.
- `TestStoredEvidenceContinuesPast409` is replaced by `TestEvidenceConflictStopsTheCompletion` (exit
  1, the consequence line, zero `/complete` calls). It failed on the pre-change source (exit 0, one
  completion call).

The retry path needs no client branch: the hub's 200 is the existing stored path.
