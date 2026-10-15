# The retired-verb pin allowlists a negation, not a licence

The reviewer's gate on the two units after `a58e684` (`GATE-ad03a16-0a968fc-two-units-2026-10-14.md`) approved
the socket unit and left **one required edit** on the retired-verb pin: `Allowed[4]`'s `Match` was
`<spec>.yaml`, the retired command's own form rather than the words that negate it. Coverage is
`strings.Contains(finding.Text, Match)`, so the one entry that covers the pin's own target verb was a licence
for that verb's exact shape, in the file holding three of the four real mentions. Measured in the gate, both
edits to `skills/client/spawn-team/SKILL.md`:

    + rig up <spec>.yaml --room demo     ->  pin GREEN (silently allowlisted)
    + rig up --room demo                 ->  pin FAILS, naming `rig up`

## The change

**`Allowed[4].Match` is `and no`** - the negating words of the sentence that names the retired form ("This
client has no `rig create` and no `rig up <spec>.yaml`: a room is created from its local definition"), and its
`Reason` now says so. The command itself is deliberately **not** in the entry: the delivered text carries it
backticked, and a `Match` holding a backticked command would be a command span by this package's own rules, in
the pin's own file, with no entry to carry it. The entry stops covering a line that writes the command without
the negation, which is the whole point.

**The count is split along the line that matters.** The pin is itself a non-test Go file, so the scan finds its
own table row and `Allowed[0]`'s own `Program`/`Match` literals - three spans that are statements about the
class rather than text a reader is handed. The headline read "8 of 8 allowlisted" and counted them as
delivered; it now reads the two numbers apart.

**The read set's boundary is stated in the package doc**, not left to be inferred from the walk: non-test
`*.go` files of this module, every file under `docs/`, every file under `skills/`. The root `README.md` and the
runtime crate's usage strings are outside it - a repo-wide search for this class currently leaves only the
parked patterns in `internal/clientpolicy/policy.go`, which escape the pin because their sub-verb carries a
`*`, incidentally rather than by an allowlist entry.

## Evidence

`go test ./internal/retiredverbs/ -v`, both shapes falsified against the delivered corpus and reverted:

    + rig up <spec>.yaml --room demo   (skills/client/spawn-team/SKILL.md)
      FAIL  1 command span(s) ... no allowlist entry carries them:
            skills/client/spawn-team/SKILL.md:169: `rig up` (the predecessor orchestrator's up)
      (the same line was silently allowlisted before this change)
    + rig ps --nodes lists the seats   (skills/share/token-economy/SKILL.md, at a line start)
      FAIL  1 command span(s) ... no allowlist entry carries them:
            skills/share/token-economy/SKILL.md:109: `rig ps` (the predecessor orchestrator's seat listing)

Green at the tip: `8 command span(s) name a retired program: 5 in the delivered text, 5 of them allowlisted
with a reason; 3 in this pin's own data under internal/retiredverbs/` - the reviewer's "5 delivered + 3 the
pin's own data". `TestEveryAllowlistEntryStillCoversASpan` and `TestTheScannerTellsACommandFromProse` pass;
`gofmt -l` on the two touched files prints nothing, `go build ./...` exits 0, `go vet ./internal/retiredverbs/`
exits 0.
