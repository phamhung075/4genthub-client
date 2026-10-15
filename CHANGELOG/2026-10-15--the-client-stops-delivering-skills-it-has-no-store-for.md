# The client stops delivering skills it has no store for

`clientseat/deliverSkills` (`internal/clientseat/skills.go`) copied
`<store>/<rig>/rig/agents/<seat>/skills` into the seat's own directory, and it was the FIRST step of
`relaunch` (`internal/clientseat/quiet.go`) - before `restartSeat`. Its source store had no writer left: the
removed `sync room` verb was the only one, `clientsync/rigverb.go` (deleted in `0eb9901`) was the only
thing that ever declared those skills in an `agent.yaml`, and the path does not exist on this box. So the
function took its own refusal branch - "seat s has no skills to deliver: <path> is missing" - and
`relaunch` returned before asking the runtime for anything. `4genteam seat reseat`, the surviving verb and
the one the client's own `seat-runtime-switch` skill drives, therefore failed at a step whose source the
same cut removed. THE RUNTIME INSTALLS THE SEAT'S SKILLS ITSELF: `start_seat` writes the seat's `mcp.json`,
its tool hook, and calls `install_seat_skills` - every start, including a restart - and that installer
clears `<seat dir>/skills` before copying the tree `AGENTHUB_CLIENT_SKILLS` names. The client has nothing
left to deliver and no store to read.

## Changed

- **DELETED `internal/clientseat/skills.go`** (`deliverSkills`, `deliverSkillsDefault`) **and
  `internal/clientseat/skills_test.go`** (its three cases: the copy replaces rather than merges, a non-omp
  seat is left alone, and a missing source is refused). The refusal case existed to pin the failing
  behaviour that this change removes.
- **`internal/clientseat/quiet.go`**: the `deliverSeatSkills` seam is gone, `relaunch` calls `restartSeat`
  directly, and the comment that lists what the runtime took over gains a fifth entry naming the deletion,
  the store that no longer has a writer, and the installer that replaces it
  (`install_seat_skills`, `rust/agenthub-runtime/src/supervisor/mod.rs:258`).
- **`internal/clientseat/sync_test.go`**: `TestRelaunchDeliversSkillsBeforeItStartsTheSeat` is deleted with
  the property it pinned, and the two surviving relaunch tests lose the seam (the no-shell-out test keeps
  its PATH trap and its "the runtime was asked for room1/alpha" assertion).

## Verified

- `cd agenthub_client && go build ./...` rc 0; `go test -count=1 ./...` rc 0 (no `FAIL`, no cache).
- The relaunch pair passes: `TestRelaunchAsksTheRuntimeForTheSeatAndNeverShellsOut` and
  `TestRelaunchReportsTheRuntimesRefusal`.
- The retired-verb pin still holds: `TestTheDeliveredTextNamesNoRetiredCommandAsAnInstruction`,
  `TestEveryAllowlistEntryStillCoversASpan`, `TestTheScannerTellsACommandFromProse` all PASS. The new
  comment names deleted code, not retired commands, and the scanner reads string literals rather than Go
  comments.
- The runtime half is read, not assumed: `start_seat` (`supervisor/mod.rs:534`) writes `mcp.json` (`:539`),
  the seat-guard hook (`:543`) and calls `install_seat_skills` (`:545`, defined at `:258`), and
  `install_seat_skills` removes the target directory before copying.

## Not in this change

- **The end-to-end run of `4genteam seat reseat` is NOT exercised here.** It restarts a live seat, and this
  room's five seats are all running, so the run needs either the lead's or the owner's say-so on a named
  seat. What is measured without it: the deleted refusal is gone from the code path, the build and the
  suite are green, and the restart it now reaches is the same call the verb always made.
- **No live seat was restarted and nothing was pushed.**
- **`docs/AGENT-SETUP.md`'s "The client also delivers this tree itself" paragraph still says the reseat step
  copies skills into `<out>/<room>/rig/agents/<seat>/skills/` and declares them in the seat's
  `agent.yaml`.** That is this deletion's own defect class one doc over, and it also carries the removed
  `agent.yaml` writer, so it is reported to the lead rather than folded in here.
- **`CHANGELOG.md`'s 2026-10-13 entry names `deliverSeatSkills` as one of two seams.** It is left as
  written: this repo's own scanner skips `CHANGELOG/` because "an entry is the record of what was, and its
  text is quotation rather than instruction".
- **`internal/clientseat/quiet_test.go` is `gofmt -l` dirty (one trailing blank line at EOF) and is not
  mine** - it arrived with the `0eb9901` rename of `seat_test.go` and is untouched by this change.
