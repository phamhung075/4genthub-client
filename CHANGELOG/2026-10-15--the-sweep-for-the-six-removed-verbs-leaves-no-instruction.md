# The sweep for the six removed verbs leaves no instruction behind

`sync room`, `sync rig`, `sync status`, `seat sync`, `seat status` and `seat watch` (six names, three
commands and three sub-commands) went with the rigspec route (`GET /api/v2/rooms/{room}/rigspec`,
deleted server-side `cbc51bae`). The deletion was clean; what it left behind was prose and printed
advice that still told a reader to run them.

### Removed
- `internal/clientsync/status.go` and `internal/clientsync/status_test.go`. `SeatsBehind`,
  `StatusState`, `FormatStatus` and `RunStatus` were the `sync status` half, and after
  `statusverb.go` went, nothing in the build called any of them (measured: the only references were
  `status_test.go` itself).

### Changed
- **Printed messages of surviving verbs no longer name a removed one.** `sync switch`
  (`switchverb.go`), `sync bundle`'s missing-policy warning (`bundleverb.go`), `sync
  offline-install`'s refusal (`offlineinstallverb.go`) and the skills delivery's refusal
  (`clientseat/skills.go`) each stated what to build or where the file comes from instead of calling a
  verb that cannot run; the switch notice points at the launcher (`4genteam up <room> --team <dir>`).
- **Docs:** `README.md` (quickstart, the skills-tree section, the failure map), `docs/AGENT-SETUP.md`
  (the 2.5 walkthrough, the skills-delivery paragraph, the failure map), and the client skills
  `install`, `rig-runtime-switch`, `seat-runtime-switch` and `spawn-team` name the launcher, `seat
  reseat`, `sync pull` or the file that must exist, and state the removal where the old text was a description.
- **Comments:** `clientseat/reseat.go`, `clientseat/seat.go`, `clientseat/quiet.go`,
  `clientsync/lock.go`, `clientsync/messagesverb.go`, `clientskills/links_test.go` and
  `clientteam/prune.go` now mark the removed verbs as removed instead of describing their behavior in
  the present tense. `clientsync/lock.go` also drops a stale `(rigspec.go)` file reference.

### The two help texts, measured after the cut
- `4genteam seat --help` (built from this tip): `usage: 4genteam seat reseat RIG --seat NAME [--seat
  NAME]...` - no `status`, `sync` or `watch`.
- `4genteam sync --help`: the eight declared verbs (`pull`, `messages`, `connector`, `bundle`,
  `switch`, `install-checker`, `offline-install`, `respawn`) - no `room`.
- Each removed name answers a usage error and names what survives: `4genteam sync room x` and the other
  four print `<group>: unknown verb "<name>"; the verbs are <list>` and exit 2.

### Testing
- `go build ./...` and `go test ./...` pass in `agenthub_client` (25 packages). `switchverb_test.go`
  pins the new notice text; `offlineinstall_test.go`'s pin ("no pinned policy found") still holds.
- The sweep itself, re-run over the tree at this tip: a recursive grep for `rigspec|sync room|seat
  sync|sync status|seat status|seat watch`, excluding `CHANGELOG/` and `CHANGELOG.md`, returns only
  this file's own history notes, the comments and printed refusals that assert the removal, and prose
  about a *runtime* seat status - `internal/clientroom/room.go`,
  `rust/agenthub-runtime/src/supervisor/mod.rs` and `internal/secretscan/secretscan.go` use the words
  for the state a seat reports, not for a verb.
- **The first pass missed eight sites, and this paragraph is the re-run because of them.** Every one
  named the verb inside a sentence or a table row rather than inside a command block, so a sweep that
  looked at commands did not see them: `skills/client/rig-runtime-switch/SKILL.md` (step 4),
  `skills/client/seat-runtime-switch/SKILL.md` (the intro, the `## Steps` preamble, step 3 and
  "Switching back"), `skills/client/spawn-team/SKILL.md` (items 2 and 3) and
  `skills/client/install/SKILL.md` (the `has no skills to deliver` row of the failure map). Each now
  names the launcher (`4genteam up <room> --team <dir>`) or `seat reseat`, or states that nothing
  writes the store side since the verb went.
