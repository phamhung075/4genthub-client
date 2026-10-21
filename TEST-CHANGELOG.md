## 2026-10-21 - the rev-9 suite re-expressed on 109cbeca: quoting, unresolvable words, a pathspec file, and the result handler (rust, branch 4genthub-min/rev9-tests-fold)

`rust/agenthub-runtime/tests/seat_guard.rs`, cut from `owner/seat-guard-rev9` @ `109cbeca`. Four rows, each written
against this tree's own helpers (`rule_of`, `tracked`, `bash`) rather than replayed from `8b00747b`:
`a_quoted_target_is_judged_by_the_rule_its_unquoted_control_is` (B1) - one row per write verb of the ruling's
table, double- and single-quoted, beside its unquoted control, plus the controls that must stay allowed (a quoted
statement separator, a quoted `>` that starts no redirect, a path with a real space, `cp a b`) and a refusal whose
reason keeps the space and loses the quotes. `a_word_the_guard_cannot_resolve_is_refused` (B1) - a variable quoted
and unquoted, and a glob with no literal prefix, each `unresolvable-target`, beside the literal-prefix rows
(`rm -r rust/agenthub-runtime/assets/*` denied, `rm -r 'build'/*` and `rm -rf dist/*` allowed) and the two
frozen-path globs. `a_pathspec_file_is_refused_whatever_verb_reads_it` (B2) - ten spellings over seven verbs plus
`-` and `/dev/stdin`, each `pathspec-from-file`. `the_result_handler_redacts_what_the_model_reads` (M1) - the
asset's default export is imported with a fake `pi` that records the handlers it registers; `tool_call` opens a
call and `tool_result` carries a value, and the returned content must not contain it and must name the placeholder.
A patch that deletes the `tool_result` handler and keeps `--redact` passes every earlier row; this is the pin.

The five fold fixes are re-expressed on this tree: `rule_of` restores the per-rule pin (m2) - seven owner-asset
calls now assert `protected-paths` for the two `TargetFile` writes and `git-imports-a-tree` for the git forms, not
"some refusal"; the carried guard blob lives in a directory of the run's own (the fold's temp-dir collision);
bare `git checkout-index -f` stays out of the refusal list (the fold's over-refusal, probed: exits 0, writes
nothing); the redactor's child gets its own `PI_CODING_AGENT_DIR` and must exit 0; and the count comments are
corrected - twelve refusals and one chain in the pathspec test, eight in the no-poisoned-index block, whose
duplicate `--index-info` row is gone.

Measured (2026-10-21, `wt/go-dev-fold` at 109cbeca plus this change): `cargo test --offline` - 194 (bin), 1
(held_send), 39 (seat_guard: 36 passed), 1 (umask) - and of those the 3 failed are BY DESIGN - `a_quoted_target_is_judged_by_the_rule_its_unquoted_control_is`,
`a_word_the_guard_cannot_resolve_is_refused` and `a_pathspec_file_is_refused_whatever_verb_reads_it` are the
owner's asset fix and turn green when it lands. The three reds are the gate's B1/B2, the driver is green against
this asset (the handler exists; the row pins it against deletion).

## 2026-10-21 - a claude seat is handed the guard it never ran (rust, branch 4genthub-min/claude-guard-settings)

Two tests, both in `rust/agenthub-runtime`. `supervisor::tests::the_claude_settings_run_the_seat_guard_on_both_phases`
forces the writer on (`write_claude_guard_settings_when`) and reads the file it wrote, asserting each phase's
command is exactly `node "$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts" --claude-hook pre|post`; it then asserts a
foreign `settings.json` is left alone, while the writer's own file is rewritten when the guard carries the mode and
removed when it does not, and that production's gate is the `GUARD_HAS_CLAUDE_HOOK` const - with a one-way check
that a true flag implies the asset really carries the dispatch, so prose in a comment cannot open it.
`runner::tests::claude_argv_names_the_seats_settings_only_while_the_file_is_there` pins that the flag follows the
file the writer wrote: no `--settings` while the seat dir has none, `--settings <seat>/settings.json` once the writer
has run, and none again for a file this runtime did not write. Measured:
`cargo test --offline` in the crate at the tip, 194 passed 0 failed (bin), 1 (held_send), 27 (seat_guard), 1 (umask);
`go test ./...` in `agenthub_client` all packages ok at the same tip.

## 2026-10-21 - the seat guard's import rule and its revision-6 source table (rust, branch 4genthub-min/guard-imports-a-tree)

`rust/agenthub-runtime/tests/seat_guard.rs` gains `a_stash_entry_is_read_whatever_verb_imports_it`: a fixture
whose `stash@{1}` rewrites the guard, plus a branch that differs only outside the protected paths. Nine refusals
(`stash branch`, `cherry-pick`/`merge`/`rebase`/`checkout`/`switch` on the entry, `switch -c x <entry>`, the
`git branch sneaky && git cherry-pick sneaky` pair in ONE call, and `stash push -- <guard>`), the path-free
`stash push` over a modified guard, and five controls that must stay allowed (`cherry-pick HEAD`, `switch -c y
HEAD`, `checkout <a branch outside the protected paths>`, `stash list`, `stash show -p`). The probe table also
pins `hooks/pre` and `.gemini/config/hooks.json`, which criterion 2 named but the table did not carry, and
`patch -dhooks/pre` (attached) beside the split spelling. Measured: 27 passed, 0 failed with the asset patch;
against the pin asset 20 passed, 7 failed.

## 2026-10-21 - the evidence verb refuses a placeholder `tsc` (go, branch 4genthub-min/guard-imports-a-tree)

`internal/clientevidence/evidence_test.go` gains three cases over `notTheCompiler`: a bare `tsc` resolving
outside `node_modules` is usage and never runs (`withLookPath` stubs the resolution), `npx tsc` with no
`node_modules/typescript` in the run's directory is usage, and `npx tsc` with one present runs the argv as
typed and posts. Each refusal asserts the seam was never called and the server was never dialed, so the
cases pin the ordering, not just the message. A fourth case runs `pnpm --dir <fixture> exec tsc` twice - once
with `node_modules/typescript` under the flagged directory (runs once, exit 0), once with it removed (usage,
the flagged directory named, no run) - which pins the flag being read rather than skipped.

## 2026-10-20 - the queue-coalesce fixture's room is not named after the retired unit (go-dev)

`rust/agenthub-runtime/tests/seat_guard.rs`: `COALESCE_ROOM` is `demo-room`, not `rig`. The constant is the
fixture's room name - a directory segment and a `4genteam room queue create <room>` argument - so the
name was never the unit's word; it goes with the rest of slice A's tree. No assertion moved; the three
uses (room dir, queue create, the fixture's own paths) take the new value.

# Test Suite Changelog

Track test suite changes, fixes, and improvements for the client.

## 2026-10-21 - the safeguard frame's stamp is a number in millis (branch 4genthub-min/frame-at-millis)

`internal/clientsync/agent_test.go`, `TestSafeguardFramesOnlyAfterReadyEchoesTheCapability`: the case
pinned the frame's key set only, so an RFC3339 string satisfied it while the hub dropped the value and
stored `last_beat_at` NULL. It now asserts `at` is an `int64` equal to the harness clock's `UnixMilli()`.
Written before the fix and observed RED against it:
`frame[0] at = string 2026-10-11T12:00:00Z, want a unix-millisecond number`. GREEN after:
`go test ./internal/clientsync/ -count=1` -> ok.

## 2026-10-21 - the peak gate's tests read the daemon the socket flag names (branch 4genthub-min/peakgate-socket)

- `internal/clientroom/room_test.go`: `TestRoomReseatGatesTheDaemonTheSocketFlagNames` is new - two fake
  daemons, the `--socket` one hosting a DeepSeek seat and the environment's an omp seat; at a pinned peak the
  refusal names the DeepSeek seat and the environment daemon records no request. RED on `origin/main`
  (`exit 0, want 3`), green after the fix.
- `TestRoomReseatRefusesAtPeakBeforeTheSeatIsStopped` and `TestRoomReseatProceedsAtPeakWithTheFlag` drop the
  `t.Setenv(clientruntime.RuntimeSocketEnv, daemon.socket)` line that used to compensate for the gate reading
  the default socket, so each now proves the flag reached the gate on its own.
- The three reseat tests the client's CI was red on register the `GetRoomStatus` frame the gate sends and
  assert it first in their request sequence (`TestRoomReseatWritesStopSeatThenRestartSeat`,
  `TestRoomReseatCarriesTheHubsPolicyDocument`, `TestRoomReseatReportsASeatThatDidNotComeBack`).
- `internal/clientheal/heal_test.go`: `stubGate` takes the socket, and the two gate tests assert the recorded
  call names the socket `machineDeps` was built with.
- `internal/clientlifecycle`'s test seams follow the new `roomNodes(socket, name)` signature (no assertion
  moved).

Run: `env -u AGENTHUB_RUNTIME_SOCKET XDG_RUNTIME_DIR=/tmp/nosock go test ./... -count=1` - ok; the same suite
with the machine's own daemon - ok.

## 2026-10-20 - a path-only criterion is a refusal, not a warning (branch 4genthub-min/int03-cascades)

- `internal/clientcomplete/complete_test.go`: `TestAPathOnlyCriterionIsWarnedAndStillJudged` is deleted with
  the contract it pinned (the warning), and `TestAPathOnlyCriterionIsRefusedBeforeTheJudge` replaces it: the
  verdict is `unjudgeable`, `evaluator` is `deterministic`, the judge is never called, the refusal names
  `criterion_1` with the vocabulary the wire carries (test command, exit code, sha, counts), and the sibling
  criterion that keeps a fact after redaction stays judgeable.
- `TestPathOnlyCriterionWarnsOnStderrAndStillCompletes` (deleted) splits into
  `TestPathOnlyCriterionRefusesTheCompletion` - enforced mode, exit 6 and no server write - and
  `TestPathOnlyCriterionInShadowRecordsAndCompletes` - shadow mode, the verdict recorded and the completion
  still sent, as for any other non-accept verdict.
- `TestAShaAndCountCriterionIsNotWarned`, `TestAPathPlusCountCriterionIsNotWarned` and the table entry
  asserting `Warnings` is empty are renamed (or rewritten) to `TestAShaAndCountCriterionIsJudged` and
  `TestAPathPlusCountCriterionIsJudged`, which assert the judge call and the verdict instead of a warning.
- `TestACriterionIsJudgedOnlyWhenTheBundleCarriesItsFact` gains the boundary the new rule needs: a path plus
  this run's exit code is admitted, and a path alone is not.
- GREEN on 2026-10-20: `go test ./internal/clientcomplete/... -count=1` and `go test ./... -count=1` in
  `agenthub_client`, plus `go vet ./internal/clientcomplete/`.

## 2026-10-20 - the probe refusals have their own tests (branch 4genthub-min/int03-cascades)

- `internal/clientservices/services_test.go`: `TestVerifyRefusesWhenTheProcessProbeCannotRun` - `Live`
  answers an error and `Verify` must answer NO rows beside it (a partial report reads as complete).
- `internal/clientlifecycle/doctor_test.go`: `TestDoctorRefusesWhenTheProcessProbeCannotRun` - exit 3,
  the refusal names `pgrep`, and stdout carries no `: stopped`/`: running`/`: silent` line.
- `internal/clientlifecycle/lifecycle_test.go`: `TestUpRefusesBeforeTheDaemonWhenTheProbeCannotRun`
  gives the verb a `PATH` holding no pgrep and asserts exit 3 with no step recorded - the preflight runs
  before the daemon, which is the order the ruling fixes; `TestStopRefusesWhenTheSignalProbeCannotRun`
  asserts exit 3, no `was not running` and that `closeWatch` is never reached.
- `internal/clientlifecycle/respawn_test.go`: `TestRespawnRefusesWhenTheProbeCannotRun` - `Live` errors,
  `respawnDaemon` answers false, nothing is signalled, nothing is started.
- `internal/clientheal/heal_test.go`: `TestAProbeErrorRefusesTheHeal` - `Rows` errors, `Run` answers the
  error and no findings, and no repair was applied.
- `internal/clientbridge/pinnedhash_test.go`: `TestAMalformedPinRefusesTheSeatsReport` drives three
  unreadable pins (shape, garbage, truncated) through `BuildSeats` and requires an error naming
  `pinned.json`; `TestPinnedHashUsedWhenValid` keeps its parity halves and now asserts the malformed pin
  is an ERROR rather than `""`, with the missing-file `("", nil)` case beside it.
- `internal/clientbridge/runloop_test.go`: `TestACycleSendsNothingWhenASeatPinCouldNotBeRead` drives a
  cycle with a corrupt pin in the fixture's own pin dir and asserts no body was sent and the note names
  the file.

Every existing seam was widened to the new `(value, error)` signatures (`Live`, `stopPattern`,
`agentRunning`, `waitSafeguards`, `rowsFor`, `Rows`, `BuildSeats`, `BuildPayload`, `PinnedHash`, `Run`);
no assertion was relaxed, and `healRun` was added so a case that should fail on a probe error refuses
instead of reading `nil` as clean.

NOT DONE, deliberately: a permanent test of `bridge once --print`'s refusal. The verb resolves its pins
store from `clientenv.RealHome()` (the passwd entry, not `$HOME`), so a test would write the operator's
real `~/.config/4genthub/seats/.../pinned.json`. The branch is four lines over `BuildPayload`, whose
refusal IS tested, and the verb was verified by hand against a corrupt file in that store.

## 2026-10-19 - the watch feed has a timing gate (branch 4genthub-min/perf-06-feed-timing)

- `internal/clientwatch/feed_timing_test.go` (new): `TestFeedWritesEachLineWithinOnePoll` starts
  `runFeed` against a temp state root and a fake tmux, waits for the startup replay, then appends
  one distinguishable event line to the seat's session log and requires the rendered marker at the
  writer within 2 x `feedPollInterval`, carried by a `Write` of its own. A buffered writer in front
  of `out` holds the line until its buffer fills or the process ends, so the bound fails; GREEN in
  1.0 s on 2026-10-19.

## 2026-10-20 - the tmux server's environment is an allowlist (branch 4genthub-min/sec02-tmux-env)

- `rust/agenthub-runtime/src/pty/mod.rs` (changed): five tests added to `launch_env_tests`, one per
  mechanism the ruling names, and they drive real tmux servers on a root of the test's own:
  - `a_server_started_here_inherits_only_the_allowlist` starts a server and reads
    `show-environment -g`: every key it carries must pass `server_env_allowed`, and
    `DEEPSEEK_API_KEY` set in this process must not be one of them.
  - `a_launch_cleans_the_servers_environment_and_the_seats_session_copy` seeds a credential on an
    existing server (`HOST_LEGACY_TOKEN`), launches a seat, and requires the global environment
    cleaned, the session's own copy free of denied and seat keys, while the pane still reads its
    `-e` value; skipped only where `/proc` is absent, since the pane half IS that measurement.
  - `the_server_allowlist_admits_plumbing_and_refuses_a_seat_s_keys` is the predicate's unit test:
    host plumbing and the `LC_` family pass, `AGENTHUB_TOKEN`, `DEEPSEEK_API_KEY`,
    `TYPESAFE_API_KEY`, `AGENTHUB_CONNECTOR_TOKEN`, `AGENTHUB_URL`, `COMPACT_THRESHOLD` and
    `HERDR_STATE_ROOT` fail.
  - `only_the_set_form_of_show_environment_names_a_key_to_clean` pins the parser: `KEY=value` names
    a key, `-KEY` does not, and `unset_global_args` emits one `set-environment -g -u` per key.
  - `the_cleanup_names_the_key_and_not_a_pair` pins that tmux refuses `NAME=value` to
    `set-environment` and takes the two-argument form, which the cleanup depends on.
- `rust/agenthub-runtime/src/main.rs` (changed): `the_credential_is_removed_by_the_server_and_never_by_a_wrapper`
  drops its `denied_name_keys()` expectation with the function; the same guarantee is now read from
  the allowlist and from the pty tests above.
- GREEN on 2026-10-20 after the rebase onto `c21e702`: `cargo test --offline` in
  `rust/agenthub-runtime` - 192 + 1 + 20 + 1 passed, 0 failed; `launch_env_tests` 12 passed,
  the five above among them, in 0.3 s.

## 2026-10-20 — the client says room (rig -> room)

- Identifiers, fixtures and expectations follow the rename in the same commit: `rig`/`Rig`/`RIG`
  identifiers become `room`/`Room`/`ROOM` across `internal/**/*_test.go`, and every test name that
  carried the old word is renamed with its behaviour (`TestRoomSessionsReadsOnlyThisRoomsSeats`,
  `TestAgentRunsTheDetectorEveryTenMinutes`, ...).
- `internal/clientbridge/testdata/rig_ps.json` -> `room_ps.json`, with the captured node document's
  `rigId`/`rigName` keys renamed `roomId`/`roomName` - it is the runtime's own document, whose serde
  keys moved with it.
- `internal/clientbridge/testdata/python_dump.stderr.txt` and `builders_reference.json`: the captured
  Python wording ("in rig X") follows the Go message to "in room X", since the parity tests compare
  the Go text against the capture.
- Fixed in the code, not re-pinned: `sendToSession`'s inner declaration shadowed the outer room
  (`if room, key, ok := ...`), `stuckSeat` compared a variable to itself, three room readers
  (`clientrestore/tmux.go`, `clientwatch/grid.go`, `seatlog/seatlog.go`) compared the session's room
  to itself, and the worktree fixture held the room directory and the room name under the same field.
  Each was the old `rig`/`room` pair collapsing; the fixes keep the two values distinct.
- `commpolicy.TestBypassSuspected`: the predecessor's rows are removed with the deleted case (they pinned
  a channel no binary can use); `TestBypassSuspectedNilKnown` now proves the nil-known behaviour on
  `tmux send-keys`. `clientsync/agent_test.go` reads the frame key `room`.

## The seat cwd: filled from the derivation, or refused

`TestUpKeepsEachSeatsCwdFromTheRoomsOwnSpec` is DELETED - it pinned `carrySeatCwds`, carrying a live
`spec.json` cwd into a definition that declared none, which the architect's ruling removes. Two rows replace
it: `TestUpFillsABlankSeatCwdFromTheDerivation` (the fill equals `clientservices.DerivedWorktrees` for a lead,
a reviewer and a seat already at its path, trailing separator included) and
`TestUpRefusesASeatCwdThatIsNotTheDerivedWorktree` (the refusal names the seat, the declared shared checkout
and the derived path). `TestUpStartsARoomFromItsOwnSpecWithNoTeamDefinition` kept its subject - a room starts
from its own record - and changed its fixture: the record names no cwd and the test now asserts the derived
path reaches the room start.

Measured while building it, which is the inversion this change makes: with the fill in place and the old
fixtures untouched, the two tests that pinned the carried cwd failed - `TestUpKeepsEachSeatsCwdFromTheRoomsOwnSpec`
exited 3 with `seat go-dev works in /declared/go-dev, not in <room>/wt/go-dev`, and the own-spec test read
back the cwds the record no longer has. `go test ./...` in `agenthub_client`: every package ok; `go vet ./...` clean.

2026-10-21: `internal/clientskills` gains the S2 windows. `windows_test.go` builds its fixtures in a real
temporary git repository (`gitTree`, commits dated 2020 so a fact recorded "now" is inside every window)
and drives `CensusWithEvidence`: one `demote` from a fixture refusal spool, one `promotable` from a
fixture rail with three landings and two older ones that must not count, a trusted `command:` skill with
an errored TOOL line that stays `hold` (`commands: 1`, `occurrences: 0`), and a census with no state root
whose notes name the unread streams instead of promoting on silence. `TestLoadEvidenceReadsTheTypedStreams`
pins the loader's field-per-signal contract, including that an allowed send is not a fact.
Two more cases pin the rules that keep the census off a bad numerator: `TestASignalThatFiresOnHalfItsStreamIsTooBroad`
(a `row-tag: gate` signal on two of four window rows is reported `broad` and the skill stays `hold`, while the
one-of-one refusal above still demotes) and `TestASkippedStreamIsReportedAndNotJudged` (a `send:` skill with no
audit on disk keeps `hold` with `unread: send` even though three items landed - promotion is an assertion of
zero occurrences, and zero from an unopened file is not evidence).

## 2026-10-21 - `skills propose`: the write path and its refusals

`internal/clientskills/propose_test.go` (new). `TestProposeWritesAnUncommittedRecordAndDraft` reads the
proposal back through `ReadRecord`: `provisional`/`fix`, `since` = the fixture tree's `HEAD`, the cited row as
evidence, the `--risk` line it was given, and the draft's bytes as `SKILL.md`; then it asserts the repository
state a gate reviews - the two paths in `git status --porcelain`, nothing in `git diff --cached`, one commit
in `git log`. Each refusal has its own case and asserts that nothing was written:
`TestProposeRefusesAnUnresolvedEvidenceID` (the id is in the message),
`TestProposeRefusesASkillThatDeclaresNoSignalTheRowsCarry` (a `guard:` skill, and the message names `row-tag`), `TestProposeRefusesAProposalThatChangesNoLine` (the same proposal twice, the record byte-identical
afterwards), `TestProposeRefusesAnotherSeatsWorktree` (both paths in the message) and
`TestProposeRefusesAnIDThatIsNotAQueueRow`. `TestProposeNamesTheLevelWhenTwoLevelsDeliverTheName` pins the
ambiguity rule and that `room/tool` lands in the room-level copy.
`TestSeatFromSessionMatchesOnlyThisRoomsSeat` pins that the room inside a session name is the guard. The verb
reads the rail and the worktree through two package-level seams (`queueRow`, `callerWorktree`), so no test
reaches a daemon or a real worktree.

## 2026-10-21 - doctor's `skills` row

`internal/clientlifecycle/skillsrow_test.go` (new), five cases driven through two seams (`skillsTreeRoot`,
`censusFor`) over a real tree and written-down census rows, so the row's rules are tested rather than the
census's own fixtures: `TestASkillsRowFailsOnADemoteAndNamesTheRowsThatDidIt` (state `skills_demote`, the
reason names `share/queue-row-schema`, the signal, the occurrence count and `qitem-1,qitem-2`, and
`clientservices.NotRunning` counts the row down), `TestASkillsRowFailsOnABrokenRecordAndNamesTheMissingID`
(`skills_broken`, the reason carries `qitem-gone does not resolve`),
`TestAPromotableSkillIsADetailAndNotAFailure` (`running`, no reason lines at all, the detail carries
`landed 3`), `TestASkillsRowWithNoTreeSaysWhatItCouldNotLookAt` (an unset tree is a note, never a verdict)
and `TestASkillsRowSurfacesACensusNote` (an unreadable stream rides beside the row). The census composition
those cases stand in for is `clientskills.CensusRoom`, called by both this row and `skills evidence`.

## 2026-10-21 - the guard stream's two producers, and two record-path minors

`internal/clientskills/guardstream_test.go` (new), three cases against the reviewer's S2 finding:
`TestTheGuardStreamFoldsTheSpoolRule` (a spool line `docs_only` matches a declared `docs-only`, and the
occurrence's ref is the spool line's own `rf-9`), `TestTheGuardStreamReadsTheSeatsBlockedToolLines` (a blocked
`protected-paths` line under `rooms/<room>/seats/<seat>/tool-calls.jsonl` is one occurrence while an `ok` line
contributes none, and one readable producer keeps the stream out of `unread`) and
`TestTheGuardStreamIsUnreadOnlyWhenNoProducerExists` (no spool and no log is unread; a present but empty log is
measured silence). Two cases in `record_test.go`: `TestATrailingSecondRecordIsRefused` (a second JSON value is
refused by name) and `TestAQueueLookupThatCouldNotBeMadeIsNotBroken` (the daemon's `unknown qitem_id` is
checked-and-absent; a failed call is unchecked with its reason). `TestAJevOccurrenceCarriesARefAReaderCanFollow`
pins the JEV ref as the line's own `timestamp` and `tier`, and the JEV fixture no longer invents an `id` the
log never writes.

## 2026-10-21 - revision 9 of the seat guard's suite lands before its asset (rust, branch 4genthub-min/rev9-tests-on-main)

`rust/agenthub-runtime/tests/seat_guard.rs` arrives whole from `4genthub-min/guard-imports-a-tree` (`7f4512c`), on
a branch cut from client main `ec09aa0f` because the guard branch is not mergeable (a merge of it reverts
`supervisor.go` and `room.go`). Thirty rows: the twenty-seven already on main, the two index rows of revision 8
(`the_index_is_written_by_path_so_the_path_is_read`, `the_index_is_read_where_the_verb_names_no_path`) and
`the_pathspec_is_read_as_the_set_it_covers`, which reads a pathspec as the set of paths it covers - `rm -r` and `mv`
of the assets directory, `git add`/`commit`/`checkout HEAD --` over it, the whole-index forms `git add -u`, `-a`,
`-p` and `git commit -a`/`-am`, a three-statement chain denying at its first firing rule, plus controls that must
stay ALLOWED because over-refusal is the other failure mode.

Red before the asset lands, measured at this tip on main's revision-6 asset `3e7c033aa2e1`: 27 passed, 3 failed -
`the_index_is_written_by_path_so_the_path_is_read` (`tests/seat_guard.rs:1381`),
`the_index_is_read_where_the_verb_names_no_path` (`:1480`) and `the_pathspec_is_read_as_the_set_it_covers`
(`:1668`). With the revision-9 asset `44be3f195328` (`wt/lead/owner-actions/seat-guard-rev9-from-main.patch`,
sha256 `806536475a20`) applied the same suite is 30 passed, 0 failed. The three red rows are the point: they pin the
asset's rule, so the split landing cannot pass by accident.

## 2026-10-21 - the guard redacts results, refuses environment dumps, and answers claude-code (rust, branch owner/seat-guard-rev9)

`rust/agenthub-runtime/tests/seat_guard.rs`, 30 → 35 rows. The rev-9 lead-asset rows now assert that `lead` and
`go-dev` are both refused the guard (commit by pathspec, `git apply`, `patch -p1`, `git mv`, a TargetFile write).
New: `a_tool_result_loses_the_secret_values_and_keeps_its_shape` (through `node seat-guard.ts --redact`: a carried
value, a `NAME=value`, a JWT, an `sk-` key and a Bearer token are gone, newlines and clean text are kept),
`an_environment_dump_is_refused_and_a_scoped_environment_is_not`,
`a_gh_api_read_of_a_credential_surface_is_refused_and_ci_reads_are_not`,
`a_claude_call_reaches_the_rule_its_omp_name_reaches` (each claude tool name against the agy path's verdict for the
same call) and `a_claude_post_call_answers_empty_and_logs_one_line`. The file is rustfmt-formatted and one
single-element loop is unrolled for clippy. Measured: `cargo test --offline` 194 + 1 + 35 + 1 passed, 0 failed.
