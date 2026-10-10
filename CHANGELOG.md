# Changelog

Notable changes to this repository, in the [Keep a Changelog](https://keepachangelog.com/)
format. Versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- The Go client becomes this repository's own module, and its binary is `4genteam` (13ccbdc).
- `CHANGELOG.md`, this file.
- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md`: how to watch a running room — the seat grid,
  the lead window, the input panes, the option list and the rules. Generic, with placeholders, so
  it works in any project; the skill is the same procedure packaged for a project's skills path.
- `docs/AGENT-SETUP.md`: setting a machine up for the 4genthub MCP, OpenRig and herdr, then the
  procedure an agent follows to build a room and its seats. The README now links it, the watch doc
  and the watch skill.
- `4genteam team apply`, `import-project`, `publish-skills` and `drift-check` are implemented in Go
  (`f7a64c7`, `internal/clientteam`), with a Python-compatible JSON emitter because the cloud
  compares module content byte for byte. `--team`, `--inventory` and the library root are required
  rather than defaulted.
- The lifecycle and per-seat verbs are implemented in Go (`2307e2b`): `up`, `compact`,
  `compact-run`, `stop`, `status`, `log`, `ui`, `seat status|sync|watch` and `policy show|apply`,
  plus the one env file (`internal/clientenv`) and the seat-log reader the supervisor and the watch
  view need. Every verb that takes a rig requires it, the seat-role table is data from the team
  file, and the log directory is the XDG state location instead of the checkout.
- The `team` and `watch` verb groups are registered in the binary's dispatcher (`b311b86`).
- `4genteam watch`, `grid`, `feed`, `inputs` and `input` are implemented in Go (`5fd1216`,
  `internal/clientwatch`), with `docs/WATCH.md` as the contract: `--rig` is required, the state root
  is `--state-root`, else `$OPENRIG_STATE_ROOT`, else the machine's own `~/.openrig/state/omp`, and a
  machine without herdr or without a readable state root exits non-zero with one message. The input
  panes send with `rig send`, never a raw tmux attach.
- The supervisor's witnessed-compaction path is tested (`internal/clientlifecycle`): a new
  compaction record sends the seat the resume text, and an unwitnessed one is reported and retried
  rather than assumed.
- `4genteam sync install-checker` is implemented in Go (`internal/clientsync`): it builds
  `cmd/seatcheck` with the pinned store compiled in, installs it at `<out>/bin/seatcheck`, links it
  where seats resolve it with a relative symlink, and refuses when the NAME still does not resolve
  rather than leaving behind a binary nothing can call.
- `4genteam sync offline-install` is implemented in Go (`internal/clientsync`): it reads a
  materialized bundle's `rig.yaml` and copies each member's pinned `policy.json`/`pinned.json` into
  `<home>/.openrig/agenthub-seats/<room>/<member>/`, which is the one place `seatcheck` looks, and
  refuses a policy that names another seat instead of handing a seat the wrong links.
- `4genteam sync respawn` is implemented in Go (`41335a4`, `internal/clientsync`): it launches a
  fresh occupant only for a seat that holds the dead reading for `--after-seconds` (default 30s),
  and it decides on the seat's own state rather than on `rig seat launch`'s exit code, because a
  launch can start the occupant and still exit non-zero. A live or an unknown seat is refused
  without launching anything.
- `4genteam feedback` is implemented in Go (`e0a9b98`, `internal/clientfeedback`): the friction
  channel's second door is native now - the flags are parsed in-process, the body is marshalled by
  `encoding/json` and sent in one POST to `/api/v2/openrig/feedback`, so no `.sh` file, no `python3`
  and no `curl` is needed. The layer is validated before any dial, the identity defaults come from
  `rig whoami --json` (read lazily, so a fully-specified call needs no rig at all), and
  `$AGENTHUB_TOKEN` is sent as a bearer token that is never printed. Deviations from the script,
  named rather than hidden: the URL default is `$AGENTHUB_URL`, this client's one server variable,
  instead of the script's MCP/public chain; any 2xx counts as stored; and a missing credential or URL
  is refused before dialing at exit 2.
- `4genteam sync rig` is implemented in Go (`4214f18`, `internal/clientsync`): it fetches the room's
  rigspec, pulls every listed seat under the same pinning rules as `sync pull`, then builds the rig
  root in a staging directory and swaps it in, so a file the operator placed there while it
  materializes survives and is named on stderr. Two of the Python's own-team defaults are gone: the
  per-room `.env` link now points at the client's ONE env file and is made for any member whose model
  is written `provider/model`, and the per-seat policy table is a seam a governed rig supplies instead
  of a compiled seat-role table.
- `4genteam sync bundle` and `4genteam sync switch` are implemented in Go (`3b5d6ec`): bundle builds
  `<room>-<seat>-<hash8>.rigbundle` from the LOCKED pin with the Python's exact `rig bundle create`
  arguments (the source label is this tree's own), warns about the seats a rig root leaves unpinned,
  and exits 2 when there is no pinned snapshot; switch records the new occupant in the cloud first,
  applies a model change with `rig seat set-model` (or stops and relaunches fresh with
  `--apply restart`), prints the manual steps for a runtime change because OpenRig cannot swap one in
  place, and exits 0 with `applied=none` when rig is missing or the seat is absent from `rig ps` -
  the cloud change is the authority, and only the local half is outstanding.
- Every verb answers `--help` (`5cb9206`): one decision in `internal/clientcmd` replaced the per-verb
  flag loops and closed the last five verbs that treated `--help` as an unknown flag - `bridge run`,
  `bridge once`, `sync pull`, `sync status` and `sync messages` - and the one name that still refuses,
  `seatcheck`, answers help too. `sync watch` is dropped from the `sync` group: Python's argparse
  registers no such verb under sync, so the name is gone rather than advertised, and the group's usage
  now describes only what this build implements.
- `skills/`: this repository's skills tree, in four kinds — `share/<skill>` for every seat of every
  room, `rooms/<room>/<skill>` for one room, `rooms/<room>/<seat>/<skill>` for one seat, and
  `client/<skill>` for an LLM running the client. A directory holding `SKILL.md` is a skill; a
  directory without one is a seat folder. Thirteen skills come across from the main repository's
  `.agents/skills` (its `.claude/skills` entry is a symlink to it), de-privatised and corrected for the
  Go client; `README.md` and `docs/AGENT-SETUP.md` describe the tree and how a skill reaches a seat.
- `internal/clientskills`: the client delivers the skills it owns. The tree is found at `--skills-root`,
  else `$OPENRIG_CLIENT_SKILLS`, else `skills/` beside the binary; a seat receives `share/`, then its
  room's room-level skills, then its own seat-level skills — so a more specific copy replaces a shared
  name — and never another room's, another seat's or `client/`'s. Installing copies each skill whole
  into the seat's own skills path (`<agent dir>/skills/<id>/`, the directory its agent spec already
  points at) and declares the ids in that spec's `resources.skills` and in every profile's
  `uses.skills`, because OpenRig projects only the skills a profile selects.
- `sync rig` delivers them (`3bc73b9`): the build is the step that writes each seat's agent directory, so
  it is the step that delivers — `internal/clientskills` resolves what that seat receives (`share/`, the
  room's room-level skills, its own seat-level ones, and nothing from another room, another seat or
  `client/`), installs them whole into `<rig>/agents/<seat>/skills/<id>/`, and declares the ids in that
  seat's `agent.yaml` (`resources.skills[]` and every profile's `uses.skills`). The tree is at
  `--skills-root`, else `$OPENRIG_CLIENT_SKILLS`, else `skills/` beside the binary, and a tree that is
  not there is refused by name BEFORE the room is fetched. A seat whose render carries no `agent.yaml`
  is named on stderr and receives nothing: files nothing loads are dead weight.

- The bridge's run loop is tested (`internal/clientbridge/runloop_test.go`, 15 tests): the on-change and
  heartbeat send decision, the 422 detail strip and its next-cycle recovery, the failure classes and the
  backoff doubling to its cap and its reset, the `once` exit codes, the recorded expected hash and the
  named drift that does not advance it, the record surviving a restart, the invalid-name skip with its
  sentence, and the sender's own wire contract. Four of them are the credential behaviours: the sender
  reads the user token, a missing token exits 2 naming it, a 401 names the variable to check, and a
  failure can put the token in neither a report, a note, nor the sender's error text. The transport is
  replaced in-process (`statusClient`), so the suite needs no network, no rig and no seat.

- The daemon resolution, the filtered `inputs open` and the last seat model are tested
  (`internal/clientsync/installchecker_test.go`, `internal/clientwatch/render_test.go`,
  `internal/clientwatch/commands_test.go`, `internal/clientwatch/watch_test.go`; 7d78395, 7615cd3,
  ba3b09e): the REAL `seatPath` resolver - the var every earlier test stubbed - returns the tmux global
  PATH while a server runs, else the cold-start daemon own PATH with `daemonPathSource` read out of
  `<pid>/environ`, else this process PATH with `shellPathSource`, and reads no process table at all
  while no daemon port is known. The daemon port is `OPENRIG_PORT` before the port in `OPENRIG_URL`, a
  non-numeric `OPENRIG_PORT` falls through to the URL, and a URL without a numeric port is the empty
  fallback; the pid comes out of a real `ss -ltnp` listing (a decoy OpenRig on another port is ignored)
  and is `0` when the port line names no pid, when nothing listens, and when `ss` itself fails.
  `inputs open --seat` splits and renames exactly the named seat pane, prints `inputs: opened 1`, and
  gives a seat outside the filter no pane; and the three `pretty` cases pin what the log does to JSON -
  a trailer line kept as it is, a truncation notice kept under cut-off JSON, and prose returned
  untouched. The process table is read through the package own `rigProcRoot` seam (`/proc` by default,
  so a real machine behaves identically), so the suite needs no network, no rig and no seat.

- The pinned hash is tested the way its Python row tested it (`internal/clientbridge/pinnedhash_test.go`):
  a well-formed pin IS the seat's hash, and a value that does not match `hashPattern` is not used at all -
  the row's own two values, a valid `ab12` and `../evil`, driven through the exported `PinnedHash` and
  then through the `Hash` field `BuildSeats` fills. The invalid half is the guard at `payload.go:177`,
  which no test in that package asserted until now. The invariant is the pattern and not the dot-dot:
  the first character must be alphanumeric, so `../evil` fails on its leading `.` where `aa..bb` would be
  accepted. Non-vacuity was proven in a clone - with the guard bypassed the test goes red on both halves
  (`pinned hashes = [ab12 ../evil], want [ab12 ]`) and green again once the patch is reverted.

- The seat state and runtime tables are asserted against their Python rows (`internal/clientbridge/seatstate_test.go`):
 all ten rows of `test_seat_state_mapping` in the Python order, including the two blocked readings and `idle`, which the
 captured dump never carries (stopped 26 / running 5 / unknown 5) so no other test in the package reached them; and every
 runtime a report may carry through `BuildSeats` - `codex` and `omp` included, because the Python dump emits `omp` while
 its parametrize list omits it - with a value outside the set reading `unknown`. Both rows were previously aliased to the
 dump-only parity test, which cannot reach the missing states. Non-vacuity: with the blocked arm and the passthrough
 broken the test goes red on exactly those three readings, and green again when the patch is reversed.

- The bridge PRODUCES agent-to-human notifications (`internal/clientbridge/notifications.go`): each cycle it lists the
  rigs this machine sees (`rig ps --json`), reads the rows addressed to that rig's human
  (`rig queue list --destination human@<rig> --json` — the daemon's own destination form), and posts ONE frame per row
  to `/api/v2/broadcast/notify`: `event_type`/`entity_type` `notification`, `entity_id` the OpenRig row id, and
  `data.message` the row's summary (its first non-empty body line as the fallback, bounded to 300 BYTES - the slice is
  by byte, so a multi-byte character on the boundary can be split). The
  contract is fixed by the consumer rather than chosen here: the route requires those three fields and takes the target
  user from the caller's TOKEN (so the body carries no user id), the server copies the body's `data` into
  `payload.data.primary`, which the dashboard reads and DROPS when its message is empty
  (`agenthub-frontend/src/hooks/useRealtimeSync.ts:911-937`), and the toast is deduplicated on `metadata.entity_id`, so
  the row id is what makes a reconnect toast exactly once. No metadata is sent at all, because the store path reads
  `metadata.user_id` as an ADDITIONAL offline target — sending it would be a way to address someone else's inbox. A row
  whose summary and body are both blank is skipped, and a row addressed to a seat is ignored even though the read is
  already destination-filtered. Sent ids live in the bridge's own state file and an id is written only AFTER the cloud
  accepted the frame, so a refused post is retried on the next cycle instead of being counted as delivered, and a
  restarted bridge does not re-notify; the record keeps the newest 500 ids and one cycle posts at most 20 rows, so
  neither the file nor a first run against a long human queue is unbounded.
- The producer's cases (`internal/clientbridge/notifications_test.go`, 11 cases): the frame's fields, the ABSENCE of
  `user_id` and `metadata` from the body, one post per row, no second post on the next cycle, survival across a restart,
  the retry after a 500, the no-text skip, the seat-addressed row, the sweep running while the CYCLE SKIPS ITS REPORT,
  the sweep disabled without a sender, and the bounded record. FIVE of them are the red for the change, not a
  description of it: with the cycle's sweep call commented out, `TestNotifyPostsOneFrameForAHumanRow`,
  `...DoesNotPostTheSameRowTwice`, `...StateSurvivesARestart`, `...RetriesARefusedPost` and
  `TestNotifyRunsEvenWhenTheCycleSkipsItsReport` FAIL, and they pass again once the call is restored. That last one is
  also the only case that discriminates the ORDER: it is red under BOTH mutations, and with the sweep moved below the
  cycle's early return — so it runs only when a report goes out — it alone FAILS (`notification frames = 0, want 1: the
  sweep sits behind the report's early return`) while the other four pass, and it passes again when the call is put back.
  The count here is the reviewer's measurement at the gate (`GATE-fa486d9`), correcting this entry's first draft, which
  said four and called the order case a distinct fifth; the correction is recorded here rather than in the gated
  commit's message, because that hash is what the gate names and its message cannot change under it. The acceptance
  itself was measured end
  to end rather than argued: the producer's own bytes, POSTed through the real route on a real database with the target
  user OFFLINE, were replayed as exactly one frame on the next connect and as none on the one after
  (`go test -count=1 -run TestZZD3ProducerAcceptance -v ./fastmcp/server/httpapp/` → PASS 3.48s, in `agenthub_go`). That
  harness was a throwaway and was deleted: a byte-exact copy of this repository's frame parked in another repository's
  suite would couple the two.

- `4genteam offpeak`, and the DeepSeek peak gate in the client itself (`internal/clientoffpeak`): the port of the
  shell gate `~/.openrig/bin/deepseek-offpeak.sh`, which the client now answers instead of shelling out. The default
  form prints the one human line and the exit code IS the answer (0 off-peak, 1 peak), so it still works as
  `offpeak --quiet && run-the-work`; `--json` emits one object (`state`, `utc`, `weekday`, `holiday`, `reason`,
  `next`); `status` prints the setting, where it came from and the state now; `set on|off` writes the ONE
  `OFFPEAK_ONLY` line. Peak is 01:00-04:00 and 06:00-10:00 UTC Monday-Friday; every other hour, all of Saturday and
  Sunday, and every date in `DEEPSEEK_HOLIDAYS` are off-peak; `OFFPEAK_UTC` pins the instant the schedule is read at.
  Measurements 2026-10-10, `4genteam` against the script it replaces, comparing the JSON, the human line AND the exit
  code: 12 instants covering every edge (Fri 00:59, 01:00, 03:59, 04:00, 05:59, 06:00, 09:59, 10:00, 23:30, Sat 12:00,
  Sun 07:00, Mon 05:00), the holiday file case and a holiday file that does not exist — identical in all 14 runs. ONE
  deliberate divergence: a malformed `OFFPEAK_UTC` is refused by name with exit 2, where the script printed a
  half-empty JSON object and exited 0; a broken clock must not read as off-peak, because that is the direction in
  which the gate silently stops refusing, and every other blank-or-typo value in this client is refused by name too.
- The gate is enforced on the three verbs that START or RESUME a seat — `up`, `compact` and `sync respawn`
  (`internal/clientlifecycle`, `internal/clientsync`). With `OFFPEAK_ONLY` on (the default) a room holding a seat
  whose model begins with `deepseek/` is refused at peak with exit 3 BEFORE the resume, the supervisor spawn or the
  launch — in `up` after the daemon and nothing else — and the refusal names the seat, its model, the peak window it
  is standing in and when off-peak resumes. The models come from each verb's own `rig ps --json --nodes --rig ROOM`
  call, which the gate and the restore now share as ONE seam (`rigNodes`), so the gate costs no extra call and no
  cloud round trip, and a room with NO DeepSeek seat is never refused. A reading that fails is not a refusal either:
  the verb's own error says what is wrong, and guessing "peak" from an unreadable room would block a room this client
  knows nothing about. `--allow-peak` — the ONE flag those verbs take, and the name the order fixed — skips the check
  for one run and wins over the environment or the env file; an argument they do not know is refused by name.
- `clientenv.Set` writes ONE key into the client's env file and prints nothing else from it (`internal/clientenv`):
  it replaces the FIRST assignment of that key — `export ` prefix included, which is the line `Load` reads — creates
  the file `0600` and its directory `0700` when missing, keeps every other byte and the mode the operator chose,
  follows a symlink instead of replacing it (the room `.env` files ARE links at this file), and refuses a name or a
  value that would corrupt the file. The verb that needed it is `offpeak set`, and the reason it lives here rather
  than in the verb is the same file's credential: a verb must be able to write one switch without reading, echoing
  or rewriting anything else in it.
- The gate's tests (`internal/clientoffpeak/schedule_test.go`, `gate_test.go`, `command_test.go`,
  `internal/clientenv/set_test.go`, `internal/clientlifecycle/offpeak_gate_test.go`,
  `internal/clientsync/respawn_offpeak_test.go`, 0 network, rig or seat): the 12-instant boundary table is asserted
  against the SHELL GATE's own printed bytes, both the JSON and the human form, with the exit code taken through
  `runReport`; both `OFFPEAK_ONLY` values and the flag-over-environment precedence, including that the flag reaches
  past a setting that says on and that the name this package first used, `--any-time`, is NOT swallowed; the
  refusal text of each gated verb; and that a refusal happens BEFORE the effect — `up` runs the daemon and not the
  resume, `compact` never spawns, `respawn` never launches. Writing the env-writer's test found a real defect in it:
  `Set` on a file whose last line carried no newline left its own assignment unterminated, so the next tool to append
  to that file would have glued its first line onto ours; the writer now always ends the file with one newline, and a
  test pins that, the symlink case, the first-assignment rule and the private modes.
- `README.md`, `.env.sample`, `docs/AGENT-SETUP.md` and the client skill `skills/client/offpeak-gate/SKILL.md`
  document the gate: the schedule, the three commands, the exit codes, what a refusal means and the three legitimate
  answers to it, that an unreadable room is not a refusal, and that `offpeak set` is the only way to change the
  setting — never an editor, `sed` or a shell redirect into the file that holds the credential.

- `4genteam continue` and `4genteam watchdog` are implemented in Go (`internal/clientrestore`, PORTED FROM
  `~/.openrig/bin/rig-continue.sh` and `~/.openrig/bin/rig-watchdog.sh`, which stay where they are). `continue`
  restores a stopped rig from its latest snapshot and briefs every seat from `~/.openrig/briefs/<rig>.txt`, with
  `briefs/<rig>/<seat>.txt` replacing the shared text for that seat; with no argument it lists the rigs and the
  briefs on disk, and `--all` walks every rig that has a brief, one rig's failure never stopping the others.
  `watchdog` reports (`--check`, `<rig>`), watches (`<rig> --watch [SECONDS] [HEARTBEAT_POLLS]`, `--all --watch`),
  restores a rig whose tmux sessions have vanished only after TWO consecutive dead polls, and self-tests its
  decision rule (`--self-test`). Both keep the scripts' log paths (`~/.openrig/logs/rig-continue.log`,
  `rig-watchdog.log`) and their line shape, and `continue` keeps `RIG_CONTINUE_WATCH=0` and the wall step.
  FOUR THINGS DIFFER ON PURPOSE: the watchdog counts running seats with `encoding/json` instead of piping
  `rig ps --json` through `python3`; both verbs run the DeepSeek peak gate before a restore - the same gate
  `up` and `compact` obey, now exported as `clientlifecycle.PeakGate` SO THERE IS ONE DECISION and not a
  second copy of it - so a resumed room can no longer start peak-rate work (a rig whose seats are already
  live is not gated, because nothing is starting); an argument a verb does not know is refused by name
  instead of being read as a brief path; and `--check` exits 2 when ANY rig came back dead - the shell
  exited with the LAST rig's status, so a dead rig that was not last was invisible to a caller reading the
  exit code - while a `--check` on a machine with no rig tool refuses in one message rather than reporting
  every rig as deliberately stopped from a reading it could not take. The watchdog's restore calls
  `continue`'s own restore function rather than the script, so the two cannot drift apart.
- The restore pair's tests (`internal/clientrestore/continue_test.go`, `watchdog_test.go`, 0 network, rig, seat,
  tmux or real home): the live-rig skip and the dead-rig restore with the seat count read AFTER it, the per-seat
  brief overriding the shared one, `--no-brief`, the abort when no seats came up, `--all` over several rigs, the
  gate refusing BEFORE the restore at a pinned peak instant (and `--allow-peak` letting the same room through,
  and a live rig never being gated), the watchdog's two-miss rule with its counter reset, its heartbeat cadence,
  its server-identity transition lines, an unreadable `rig ps` counting as zero expected seats, and that a rig
  name that merely CONTAINS the rig's name is not one of its seats.

### Changed

- The bridge carries TWO transports instead of one (`internal/clientbridge/bridge.go`, `commands.go`): the report's and,
  new, the notification path's. Both are built from the same `AGENTHUB_URL`/`AGENTHUB_TOKEN` by one helper and differ
  only in the endpoint, and both are required at startup for the same reason — a bridge that cannot report must not
  start quietly. The notification sweep runs BEFORE the decision to send a report, because that decision returns early
  when nothing changed and a row addressed to the human is news on its own; a test pins that order by notifying while
  the cycle's own report is unchanged.

- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md`: the `--seat` row is the exhaustive set now —
  `feed` and `inputs` take it repeatably as a filter, and `input` takes it once as the seat to send
  to, where it is required — so the row no longer names a set one verb short.
- `docs/AGENT-SETUP.md`: the install-target passage names the profile half too — the same render that
  fills `resources.skills` also lists each id in the default profile's `uses.skills`, so a pool entry
  no profile uses is inert exactly as a file no spec declares is.
- `docs/AGENT-SETUP.md` names `HERDR_ENV`: `4genteam up` ends by launching `herdr` and stops before
  that when the variable holds a non-empty value, which is the escape its own refusal prints on a
  machine with no `herdr` on `PATH` — and which no file in this repository documented until now.
- The usage errors speak this client's own dialect instead of Python's argparse (`9bf0c9d`): one form per
  kind — `missing argument: <name>`, `unknown argument: <flag>`, `<flag> needs a value` — in the six verbs
  that parse their own arguments (`sync rig`, `bundle`, `switch`, `respawn`, `offline-install`,
  `install-checker`). The behaviour is unchanged: a non-zero exit, offline, naming the offending argument.
  A table test pins the exit code and the name, never the sentence.
- A machine whose real home cannot be resolved now REFUSES the state root instead of answering a
  relative `.openrig/state/omp` (`42132ff`): `clientenv.DefaultStateRoot` returns no path at all and the
  new `clientenv.ResolveStateRoot` refuses with one message naming both escapes (`--state-root DIR` or
  `OPENRIG_STATE_ROOT`), so no path this client derives can depend on the working directory. The
  resolution points that take the root refuse the same way — `compact`/`compact-run`, `policy apply`,
  `seat sync|watch`, `watch` and `sync rig` — and the four clauses before it are unchanged.
- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md` (both tables, so the mirrors stay mirrors):
  the options gain `--seat` — `feed` and `inputs` only, repeatable — and `--cols` is stated as `grid`
  and `watch` only, matching the help text the code prints; `--state-root` is confirmed at the tip.
- `docs/AGENT-SETUP.md` names the install target for a published skill: the per-agent skills
  directory the seat spec's `resources.skills[].path` points at, with the module's `id` declared in
  that spec, because a copy alone is inert — the runtime loads what the spec declares.
- `skills/client/watch-team-grid/SKILL.md` is folded into `skills/client/watch-team/SKILL.md`, which
  survives, in the client kind's generic voice. Every lesson of the grid playbook is in it: the
  per-pane invocation and header, the `grid: <n> seats in <cols> columns` line, the checks to run
  when the view stays silent, the `--cwd` fact, the compaction ladder with the supervisor restart,
  the `rig-continue.sh` hook, the zero-size-file restore trap and the backtick warning. The
  `watch-team-grid` directory is gone, and the skill's own options table carries `--state-root` as
  `docs/WATCH.md` now does.
- `docs/WATCH.md` names `--state-root` in its options table: where the session logs are read from
  is the flag, else `$OPENRIG_STATE_ROOT`, else `~/.openrig/state/omp`.
- `.env.sample` states the one-credential, one-file contract: `AGENTHUB_URL` and `AGENTHUB_TOKEN`
  in `~/.config/4genthub/.env`, with a variable already in the environment winning over the file.
  It carries no token value, and every key it lists is one the client reads.
- `skills/watch-team/SKILL.md` moves to `skills/client/watch-team/SKILL.md` unchanged, byte for byte
  (the same sha256), as the first skill of the tree's client kind. `README.md` and the two entries
  above now name the new path.

- `4genteam sync connector` sends the seat identity it observed with the session: `rig ps --json` is
  read once, the session is addressed by its `canonicalSessionName` (the name the UI shows and a person
  passes to `--session`; `logicalId` still matches, it is simply no longer the address), and the frame
  carries `room` (the rig) and `seat` (the member after the first dot of `logicalId`) beside the
  session key — or NEITHER, when the rig names no seat for the session.

- `bridge` names a skipped invalid-name node with the Python's own word: `skipped N seat(s) with invalid
  names`, where the first Go draft said `node(s)`. The deleted test asserted that sentence, so the Go is
  the side that moved rather than the sentence.
- The delivered skills name no owner room: `skills/client/rig-runtime-switch/SKILL.md` sent a reader on
  the wait-for-reset path to a launcher named after one room of the owner's own setup. It names the
  seats bin directory and describes the launchers instead, which is the `<RIG>`-placeholder rule the
  rest of that file already follows — so `skills/` carries no room name of the owner's.

### Removed

- The Python client is gone: `pyproject.toml`, `src/agenthub_client/` (11 modules and
  `seat_feedback.sh`) and `tests/` (10 test modules and the `scan_cases.json` fixture) are deleted, and
  `.gitignore` drops its Python-era entries (`.venv/`, `build/`, `*.egg-info/`, `__pycache__/`,
  `.pytest_cache/`). Every verb the Python client offered is served by the Go binary, and
  `rust/forcecompact` stays: it was always a self-contained module. The fixture the Go test owns,
  `internal/secretscan/testdata/scan_cases.json`, is untouched.

### Fixed

- `cmd/4genteam`'s free-commands test no longer reads the HOST: it pins `clientcmd.LookPath` with the
  new `rigPresent` helper (mirroring `rigMissing`) and asserts BOTH answers to the no-argument
  invocation - `up`'s own missing-rig-name error with rig on PATH, and the platform matrix's one
  message with rig absent. It left the seam alone and so asserted whichever answer the machine
  happened to give; a masked clone at `b01a211` with no `rig` on PATH failed the package on exactly
  the machine the client must stay honest on. The suite is 17/17 with `rig` off the child's PATH.
- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md`: the three `inputs` examples pass the `--rig` the
  verb requires.
- `4genteam watch`, `grid` and `feed` now resolve the state root exactly as the doc comment and
  `docs/WATCH.md` promised: `--state-root`, else `$OPENRIG_STATE_ROOT`, else the machine's own
  `~/.openrig/state/omp` (`clientenv.DefaultStateRoot()`). The third clause was documented but
  missing, so the feed refused on a machine where the doc says it reads. The usage text now states the
  home-relative default (`~/.openrig/state/omp`) rather than only the variable name - the resolution
  stays absolute - and a root that is named but not there is refused by its path whichever clause
  named it.
- `4genteam sync --help` prints the verb group's usage and exits 0; it used to be read as an
  unknown verb and exited 2 (`1e23ca2`).
- `4genteam bridge install-service` renders the client's OWN environment file
  (`clientenv.DefaultPath()`, `~/.config/4genthub/.env`) instead of a Python-era name nothing ever
  wrote, so the installed systemd unit and the running bridge read the same file by construction.
- One resolver decides the state root for every verb that takes one: `sync rig`, `compact-run` and
  `policy apply` now call `clientenv.ResolveStateRoot` like `seat sync|watch` already did, so
  `$OPENRIG_STATE_ROOT` is honored by all of them. The defect was that these three named the variable
  in their refusal and then ignored it: whoever set it got the machine's tree with no error, which is
  the wrong tree read or written silently. The order is unchanged — the flag, else the variable, else
  this machine's own default — and nothing resolvable still refuses with the one message naming both
  escapes. The verb-local state-root seams went with it: `rigDefaultStateRoot`, the `rig` flag-presence
  field, and `compact-run`'s flag default that pre-empted the resolution. Tested clause by clause at
  the source (`internal/clientenv`) and on the live path per verb: `sync rig` installs under the
  variable's root, `compact-run` reads it with no flag, `policy apply` writes under it, and the flag
  still wins.
- The seat store is resolved the way the state root is: ONE implementation in `internal/clientenv`
  (`DefaultSeatStore()`, `ResolveSeatStore()`), the account home's `.openrig/agenthub-seats`, so it is
  the same store inside a seat and out. It used to be read under `$HOME` in three packages, and a seat
  runs with `HOME` pointed at its own state directory - so the same command resolved a different store
  per seat, and the watch verbs then refused a room that exists, because the store they looked in had no
  room in it. It also fell back to a RELATIVE `.openrig/agenthub-seats` when the home could not be read,
  which made the store depend on the working directory. Both are gone: a store that cannot be resolved
  refuses with one message naming the reason and both escapes, and every verb that takes a store shares
  that refusal - `sync pull|status|bundle|messages|rig|install-checker`, `bridge` and `seat`.
  `clientbridge`'s path defaults and `clientseat`'s `~` expansion stopped following `$HOME` with it, and
  the per-package copies of the default went away. Tested where each clause is reachable: `clientenv`'s
  two store tests, `clientsync`'s store/state-root home-independence test, `clientbridge`'s path test,
  and the built binary with `HOME` pointed at a seat's state directory, where the store still resolves
  from the account home and a real room still streams.
- `4genteam sync pull` refuses a room or seat name that is not `[a-zA-Z0-9][a-zA-Z0-9_-]*` before any
  network call, so a name like `../../evil` can no longer name a path outside the store.
- `4genteam watch`, `grid` and `feed` check that the ROOM exists rather than that it has live seats, so a
  room whose seats are all stopped streams `-- following 0 seats` instead of refusing. A count-based check
  refused a room that exists, which is what a stopped room hits by definition. The one check every verb
  that resolves a room or a seat goes through now refuses an unknown room (2) and an unknown `--seat` (2)
  before any pane is opened, and the store it looks in is resolved from the account home, never `$HOME`.
- The watch verbs' state root is resolved by the ONE shared implementation too: `resolveStateRoot` calls
  `clientenv.ResolveStateRoot` and keeps only this group's two additions - the root must exist and be a
  directory, because the panes read session logs under it, and a root this group cannot resolve exits
  unavailable (3) where the shared paths answer usage (2). The clauses were written twice and had already
  drifted apart by exactly that exit code, so the difference is a wrapper decision with a test pinning it
  now. The test-only seam that pointed the default at a scratch tree went with the duplicate, and the
  tests assert agreement with the shared resolver instead.
- The seat store's refusal names the escape where the verb really has one: `--out DIR` is offered by
  `sync pull|status|messages|rig|install-checker` and by `seat`, while the reason alone stays on `bridge`,
  on `sync bundle` (its `--out-dir` is where the bundle is written, not the store) and on the watch verbs -
  so no verb advertises a flag it does not accept. The rule is one function in `internal/clientenv`
  (`SeatStoreRefusal`), which preserves the exit code and is pinned on both forms.
- `cmd/seatcheck` stops carrying its own copy of the home and store resolution: the guard takes the
  client's (`clientenv.ResolveSeatStore`), so the two cannot disagree about where a seat's policy was
  installed - the duplication that let a week-stale installed binary read as current.
- A transport that never answered is reported by its REASON instead of as an answer: `bridge` printed
  `server answered HTTP 0` for a request that timed out or a cloud that was unreachable, a sentence about
  an answer that never existed, where the Python named the reason (its sender raised and the loop printed
  the exception). The reason now travels out of the sender as an ERROR - `Sender` returns
  `(status, body, error)`, and a transport that never answered returns no status at all - so the
  `server answered HTTP %d` branch is reachable only by a real answer, which still names its status. The
  push path is otherwise unchanged: the credential rides in the `Authorization` header and a transport
  error carries the URL, never the token, which the credential test pins on the sender's error AND on the
  loop's note. Spec: `tests/test_bridge.py:581` asserted the timeout's own words reach the output, and
  `:587` that the token does not.
- `4genteam feedback` names the reason a request failed, not only its URL. The shell client it replaced
  showed one - `curl -sS` prints its own diagnostic before the script's line - so a compiled
  replacement that printed the line alone told the operator less than the door it replaced. The verb's
  client is a seam now, so the reason is asserted verbatim in the suite with no network.
- `4genteam sync --help` states that offline use is the operator's own choice and where the bundle is
  run (`rig up <bundle> --target <dir>`), and that the client never falls back to a bundle by itself.
  Python's argparse printed that as the sync parser's epilog, and the Go help had dropped it, which left
  `tests/test_seat_sync.py::test_help_states_offline_use_is_manual` with no Go witness. The sentence is
  ported, and the Go test carries the row's name so the instrument pairs it by name.
- `4genteam <verb> --help` is answered even on a machine that has no `rig` on PATH. The dispatcher
  resolved the tool before it called the verb, so `up --help`, `ui --help`, `bridge --help` and bridge's
  `run`, `once` and `install-service` exited 3 with the matrix's message and printed nothing - exactly the
  six shapes, of forty measured, that refused while every other verb answered. Help now wins over the
  prerequisite check, decided by the one predicate every verb answers help with
  (`clientcmd.HelpRequested`, which also replaces `clientlifecycle`'s first-position copy of it, so a verb
  reached for help never has to touch a rig), and the gate is moved rather than removed: the same verbs
  without `--help` still exit 3, naming what the machine is missing.
