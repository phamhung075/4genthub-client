## The decision attaches to the capability

### What was wrong
`commpolicy.AuditRecord` was written by ONE process while the delivery happened in ANOTHER. `cmd/seatcheck`
appended the decision line (`:176`), delivered, and appended the outcome (`:213`) through `appendAudit`
(`:379`), and `4genteam send` — the verb a seat actually sends with — wrote neither. The struct's own comment
names the consequence, and the architect's ruling (`qitem-20261015050158-c8120a7112fb`) quoted it: an allowed
decision line with no outcome line after it leaves the outcome UNKNOWN, so a reader cannot tell a delivery that
happened from one that did not. A guard a seat must remember to run is not a guard.

### Changed
- `internal/commpolicy/audit.go` (new): `AuditPath`, `EnsureAuditDir` and `AppendAudit` — the writer moved out
  of `cmd/seatcheck` with its body unchanged, because two writers would have been two definitions of one audit
  line. One seat's file is `<store>/<room>/<seat>/audit.jsonl`.
- `internal/commpolicy/intent.go` (new): `IntentWords`, `IntentNames` and `ParseIntent` — the five words in one
  home, since both surfaces take `--intent` (`cmd/seatcheck` and the client's send path) and a copied list
  would be a second definition.
- `cmd/seatcheck/main.go`: its own `intents` map and `appendAudit` are deleted in favour of those two homes.
- `internal/clientseat/sendpolicy.go` (new) — the send path now owns both halves:
  - the policy is built from **the room's declared links, read at send time** through
    `clientruntime.RoomLinks(room)` (never cached), from the daemon `--socket` names — the same daemon the
    delivery goes to, because a record read from one runtime and a write to another can only be right by luck;
  - `commpolicy.Decide(policy, to, intent)` decides BEFORE the delivery; the decision line is appended first,
    the delivery happens, and the outcome line closes the pair whatever the delivery did;
  - the sender's identity comes from THIS machine's resolver (`clientruntime.ResolveIdentity`), never from the
    aimed daemon's record, which does not hold this shell's seat;
  - the three refusals stay three: an unreadable source (`ExitUnavailable`, "room X's declared links could not
    be read from the runtime at SOCKET"), a policy denial (`ExitUsage`, "refused by ROOM's communication
    policy: FROM may not send a INTENT to TO (no link). The room's own declared links are what license a
    send"), and a failed audit ("the decision could not be audited in PATH") which stops a send that was
    otherwise allowed.
- `internal/clientseat/reseat.go`: `--intent` on both send shapes, with the **documented default `notice`**
  printed in the verb's own usage (the architect's ruling item 5), the intent actually used recorded on the
  decision line, and the shapes checked before anything is decided so a caller error stays a caller error. The
  canonical `ROOM-SEAT@ROOM` shape keeps its own split; a NATIVE session name is read back to a seat key
  through the aimed daemon's record, and a session no record lists is refused in words rather than authorized
  on a guess.
- Tests: `sendpolicy_test.go` (the four gate cases at the seat level, plus the default), `send_transport_test.go`
  (the fakes now answer the identity, links, room-status and session questions this path asks, as a daemon
  would), `send_test.go`.

### The reading, before the wiring
The policy source was measured first (`cmd/linksprobe`, a throwaway instrument): the DEFINITION
(`scripts/team/4genthub-min/team.json`) declared 0 links, the runtime served 0, and the cloud's per-seat read
answered the plan's 12. The architect ruled the local definition the source of truth and that fixing it comes
FIRST; that is done separately (23 links, `scripts/team/4genthub-min/team.json`, root-repo branch
`4genthub-min/declared-links`, and the live `spec.json` merged with them — `RoomLinks(4genthub-min)` now
answers 23, kind for kind). This unit is the code half; **the INSTALL waits for that**, per the ruling.

### Verified
- `go test ./...` in `agenthub_client`: **29 packages ok, 0 failures**.
- THE LAB, end to end at a scratch daemon and a scratch room whose definition declares EXACTLY ONE link
  (`go-dev -> dev`, `collaborates_with`) with a second seat in the same room for the refusal case — no live
  room touched, the client's own seed socket never written:
  - (a) an authorized send: DELIVERED (the frame reached the seat's mailbox, envelope and all), exit 0, and the
    audit pair closed — `{"Allowed":true,...,"Outcome":"delivered"}`;
  - (b) the unlinked peer in that same room: refused in words ("go-dev may not send a notice to solo (no
    link)"), one denied line, no frame at its mailbox;
  - (c) a room that declares no links at all: the same refusal, the same single denied line;
  - (d) FALSIFICATION, the pre-wiring binary at the same lab: BOTH of (b)'s and (c)'s sends were delivered and
    **no audit line was written** — the pair the gate turns on;
  - the DEFAULT: a send with no `--intent` at all was decided as `notice` and the decision line records
    `"Intent":"notice"`, so a sender who meant `task` is visible rather than invisible.
- Not verified: the live room. Nothing was installed, pushed or restarted; the running seats still run the
  pre-wiring binary.

### Known, not touched
- A sender that is not one of the room's seats (the owner's own shell, which sends with `--from owner`) has no
  links, so this gate refuses it with "this shell's own seat has no room or seat key". The ruling covers seats
  and the room's declared links; whether a non-seat sender is exempt is a decision nobody has made, and it is
  reported rather than assumed.
- The envelope's `From` label still defaults to "owner session" for a send made from a seat's own shell
  (`defaultSender`), so the delivered text can name the wrong sender while the audit names the right one. It
  predates this unit and is reported, not changed.
