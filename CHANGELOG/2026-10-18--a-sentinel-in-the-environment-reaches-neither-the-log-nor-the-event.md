# A sentinel in the token environment reaches neither the log nor the event

Batch 9 of the supervisor-port table, `c22558a` on `01a2837`. Ported, not cherry-picked: the commit's parent does
not carry the port's split of `secretscan` and `clientsync`, so the three source halves and the two fixtures were
re-applied against main's shapes.

### Changed

- `internal/secretscan/redact.go` — `RedactKnownValues` is the known-values half of `Redact`, exported so a caller
  with its own pattern set and its own marker can use it alone. `Redact` now calls it and its behaviour is
  unchanged, so one implementation of the ordering (longest value first, longest form first) remains.
- `internal/clientsync/redaction.go` — `Redact` removes the known environment values BEFORE the pattern list,
  which only sees shapes: a child that echoes its environment prints the value bare, and inside JSON the variable
  name is followed by `":"`, which the name-shaped rule does not match. The seam `redactionKnownValues` is read per
  line, so a rotated credential takes effect without restarting the upload loop.
- `internal/clientrestore/log.go` — `teeLog.printf` runs the same known-values redaction on the line before it
  reaches the file and stdout (`rig-continue.log`, `rig-watchdog.log`), so a step that fails with a child's output
  or its own environment in the message does not write the value down.

Using the whole `secretscan.Redact` in `clientsync` was the first attempt and was rejected: its own pattern set and
its `[REDACTED:<label>]` marker rewrote the JWT/AWS/PEM/URL-userinfo cases the gate already covers.

### Tests

New: `TestASentinelInTheTokenEnvironmentNeverReachesTheEvent`,
`TestAClientLineRedactedByNameIsWrittenBackAroundIt` (clientsync);
`TestALineLoggedWithASecretInTheEnvironmentIsWrittenWithoutIt`,
`TestTheLogWriterLeavesAnOrdinaryLineAlone` (clientrestore, new file).

Red first, with only the two call sites removed and the helpers kept:

```
redaction_test.go:264: the sentinel reached the event for "the fake child printed sentinel-token-value-abc with no name around it": {"payload":{"text":"the fake child printed sentinel-token-value-abc with no name around it"},"type":"message"} (reason "")
redaction_test.go:280: Redact left the sentinel in place: "before sentinel-token-value-abc after"
log_test.go:41: the sentinel reached the log file: "2026-10-18T12:06:46+02:00 restore step said: the child printed sentinel-token-value-abc and then exited"
```

Green: `gofmt -l internal/ cmd/` empty; `go vet ./...` rc 0; `go test -count=1 ./...` 34 packages `ok`, no FAIL.

Not run: a live restore/upload cycle — the sentinel is asserted at the two writers, not against a running cloud
session.
