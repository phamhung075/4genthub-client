## The owner step of the decision ladder names its destination

### Changed

- `skills/share/jev-decisions/SKILL.md`: exit `5` now splits on `escalate_to` as the verb prints it.
  `escalate_to: "lead"` keeps the one-word re-ask at `--tier lead`; `escalate_to: "owner"` (from the lead, or
  any tier on an owner-only category) raises the ask on the room's rail as
  `4genteam room queue create <room> --to principal@<room> --tag owner-decision …`, prints the `qitem-<…>` id,
  and names that id as the thing the report carries.
- `skills/share/room-decision-request/SKILL.md`: the lead's owner step carries the same command with the
  body's two caps, and says what was previously left to a guess: `principal@<room>` is the principal
  session's address (session `agenthub--<room>--principal`), and the daemon accepts any session name, so a
  misaddressed row is silent rather than refused.

### Why

C6's fourth case asks that a question escalated to the owner be reachable as a row: the ladder's exit `5`
carried no destination anywhere a seat reads first, so the owner step could be "done" by sending the lead a
message. Measured 2026-10-17 at `a40d9a0` in a throwaway root against a private daemon: the create with
`--to principal@<room> --tag owner-decision` is accepted, prints `qitem-20261017060627-63788f539ee7`, and
`room queue show` reads that row back with its body and tag intact. The same run measured the verb itself:
A seat/0.90 decides with one Jev request and names no lead; B seat/0.60 and lead/0.60 exit 5 with the
recommended option carried and `escalate_to` lead then owner; C credentials, spend and a
`DECISION_OWNER_ONLY_CATEGORIES` name at 0.95 escalate to the owner by code with zero Jev requests; E one log
line per outcome with tier, option and confidence, and a usage refusal logs nothing.

### Not in this change

No verb, no threshold, no owner-only list. The dashboard's mirror of an owner-decision row up to the cloud
(`POST /api/v2/decisions`) is not in this client at `a40d9a0`; this lane documents and proves the row's own
path, which is the half C6 names.

### Tested

`go test ./internal/clientskills/...` (every local link in every shipped skill document still resolves);
`go vet ./internal/...`; the five cases above, run from a script under a throwaway root with stub Jev.
