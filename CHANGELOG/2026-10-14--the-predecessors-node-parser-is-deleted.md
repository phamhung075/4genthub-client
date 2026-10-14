# The predecessor's node listing has no reader in production

`ParseRigNodes` (`internal/clientbridge/payload.go`) parsed the output of `rig ps --json --nodes` — the tool
this cutover removes. Nothing in production called it: the bridge fills its nodes from the runtime's own room
records (`bridge.go`'s `runtimeNodes`) and `BuildSeats` consumes the node *shape*, not the old listing's text.
Its only callers were three test files, two of them parity fixtures.

## Why it goes rather than stays

An exported entry point shaped for the predecessor's stdout is a silent reintroduction path. The node read is
the runtime's now, so a caller that does not exist today can be written tomorrow, compile, and put the
predecessor's listing back on a path — and no test would notice, because the class has no automatic pin (the
reviewer's gate, finding 1). Deleting it is the cheap half of that finding: what cannot be called cannot be
reintroduced by accident.

## The change

- `ParseRigNodes` and its doc comment are deleted from `payload.go`.
- The three test callers call the shared unwrapper directly — `parseItems(…, "rig ps")` — so every fixture
  still parses through the same code and the label in the error text is unchanged:
  `parity_test.go` (the captured reference listing, `testdata/rig_ps.json`), `runloop_test.go` (the run-loop
  fixture), `pinnedhash_test.go` (a two-seat literal).
- `parseItems` stays where it is: the queue reader uses it, and it is the one place that decides the two
  listing shapes so the readers cannot disagree.

No behavior changes: the parser was a one-line wrapper, and the text of the error its callers could see
(`unexpected rig ps JSON shape`) is preserved by passing the same label.

## Evidence

- `go build ./...` 0, `go vet ./...` 0, `go test ./...` **25 packages ok, 0 FAIL**.
- The repointed tests run by name: `TestPayloadParityWithThePythonBridge` PASS, `TestPinnedHashUsedWhenValid`
  PASS.
- **The fixtures are still load-bearing, falsified rather than assumed:** with `testdata/rig_ps.json` replaced
  by `[]`, `TestPayloadParityWithThePythonBridge` FAILS at its duplicate-warnings assertion
  (`duplicates = [], want [seat 'check' in rig room-c …]`) — the test reads the parsed nodes, so the repoint
  did not turn it into a no-op. The fixture was then restored **byte-identically**
  (md5 `b0244a05085c691a58ef5473763c582c`, 19013 bytes, before and after).
- No reference to the deleted symbol remains anywhere in the tree (`grep -rn ParseRigNodes` → nothing), and
  the package is `internal/`, so no other module could have imported it.

Provenance: the reviewer's gate on the C3 five units, finding 2
(`GATE-c3-printed-rig-five-units-2026-10-14.md`).
