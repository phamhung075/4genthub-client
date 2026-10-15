## The comments name the path that owns the decision

### What was wrong
Deleting the checker's `send` verb left prose naming `cmd/seatcheck` as a caller of things it no longer
reaches: `ResolveIdentity` (its own doc, the whoami verb's comment, and feedback's), `--intent` ("TWO surfaces
take" it), `DeliverToSession` ("the native half of the seat guard's delivery"), and the seat's pinned policy
(`policy.json is the file seatcheck reads at runtime`, `the only place seatcheck looks`, `seatcheck reads a
seat's policy from the pin`). Each was measured against `cmd/seatcheck/main.go`, whose ONLY project import is
`internal/commpolicy` and whose only call is `commpolicy.BypassSuspected` (main.go:107): the checker no longer
resolves an identity, takes `--intent`, delivers, or reads a policy.

### Fixed
- `internal/clientruntime/transport.go` (147, 189-195): `ResolveIdentity`'s "SECOND caller (the seat guard,
  cmd/seatcheck)" is the send path (`internal/clientseat`) and `4genteam feedback`; `DeliverToSession` is named
  for what it is — the restore path's delivery on the same transport the `4genteam send` verb delivers over —
  and the seat guard's own delivery-failure mapping is gone with the delivery.
- `internal/clientruntime/command.go:43`, `internal/clientfeedback/feedback.go:216`: the co-caller that
  reasons about that identity chain is the send path, not the checker.
- `internal/commpolicy/intent.go:3`: ONE surface takes `--intent` (`internal/clientseat`, reseat.go:207/:302).
- `internal/clientsync/bundleverb.go:8`, `rigverb.go:221`, `offlineinstallverb.go:22,41`: the pin is the seat's
  `pinned.json` and the `policy.json` `offline-install` installs into the seat store — the store the send path
  writes a seat's `audit.jsonl` into (`commpolicy.AuditPath`) — and nothing is decided from that file:
  `4genteam send` builds the policy at send time from the room's declared links.
- LEFT, with the reason: `internal/clientseat/sendpolicy.go:16` is past tense ("was written only by
  `seatcheck send`") inside the dated architect-ruling block, and the sentence after it names the live owner
  ("One path now owns both halves - this one"); it records the hole rather than claiming a live caller.

### Verified
- `gofmt -l cmd internal` empty · `go build ./...` rc 0 · `go test ./...` rc 0 (30 packages ok).
- Measured while checking coherence, NOT fixed (needs a ruling, not a comment edit):
  `internal/clientsync/installcheckerverb.go:285` still passes `-ldflags -X main.installedPinsDir=<store>` for
  a variable the deletion removed (`grep -rn installedPins cmd/seatcheck/` -> no hits), and `README.md:84`
  still says each installed checker carries the pins directory of the store it was built for. Nothing is
  broken — `go build -ldflags "-X main.installedPinsDir=<dir>" -o <out> ./cmd/seatcheck` -> rc 0 — so the flag
  is dead weight and that README sentence is stale prose of the same class as the sites fixed above.
