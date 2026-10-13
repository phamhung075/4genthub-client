# Changelog

Notable changes to this repository, in the [Keep a Changelog](https://keepachangelog.com/)
format. Versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 2026-10-13 — the seat send goes to the runtime instead of shelling out to `rig`

`sendCommand.Run`'s own `RIG SEAT MESSAGE` shape tried the native runtime send and then, on failure, fell
back to `exec.LookPath("rig")` + `exec.Command(path, "send", <session>, <text>)`. On this machine `rig` is a
symlink to THIS client, so the "fallback" was a self-call: the two-argument OpenRig form went back into the
same verb, whose three-positional entry then refused it, and the reader got a failure report about a command
nobody had asked for. The route is the FALLBACK — which is why the same failure appeared both on the
installed binary and on a tip build; the tip build had no arity bug of its own, it delegated.

### Changed
- `internal/clientseat/reseat.go`: the seat send goes to the local runtime (`clientruntime.SendTextToSeat`),
  and a refusal is reported as the runtime's own reason, naming the target it could not reach. The
  `exec.LookPath("rig")` / `exec.Command` fallback is DELETED rather than repaired: after this client's rename
  that name resolves to this binary.
- `internal/clientseat/reseat.go`: the verb's help claimed the shape "sends MESSAGE to the seat with
  `rig send`"; it now says the send goes through the local runtime and does not shell out to `rig`, with the
  reason.

### Verified
- `gofmt -l internal/clientseat/` clean; `go build ./...` exit 0; `go test ./internal/clientseat/` ok;
  `go test ./...` 23 packages ok, 0 failed — so the two-argument daemon path, `--verify`, `--wait-for-idle`,
  `--json` and the refused-flag list are untouched.
- The deletion is pinned as an ABSENCE, not as a message, by
  `send_test.go:TestTheRigSeatMessageShapeNeverFallsBackToShellingOutToRig`, RUN BY NAME → PASS. It empties
  `PATH`: with the fallback present the verb answers `"rig is not on PATH"` (the `exec.LookPath` failure), and
  without it the only possible failure is the runtime's, which must name the target. A deletion nobody can
  observe is a deletion nobody can defend; this makes the missing call fail a test.

## 2026-10-13 — an omitted permission policy takes the default, and a blank one is refused by name

`SeatSpec.permission_policy` was `#[serde(default)] String`, so an ABSENT key arrived as `""` — and the
blank-refusal in `approval_mode` then swallowed the omitted case too: a seat that said nothing about its
policy was refused, and the documented unattended default was unreachable. The client already sends the pair
distinctly (`clientruntime.SeatSpec.PermissionPolicy` is a pointer for exactly this reason); the runtime's own
type could not represent the difference, so the distinction died at the deserialization layer.

### Fixed
- `rust/agenthub-runtime/src/types.rs`: `SeatSpec.permission_policy` is `Option<String>`. serde gives `None`
  for an absent key and `Some("")` for a present blank one, so the two stop collapsing.
- `rust/agenthub-runtime/src/runner/mod.rs`: `approval_mode` takes `Option<&str>` — `None` returns `"yolo"`,
  the documented unattended default; `Some("")` is still refused BY NAME, with the message that names the way
  out. `child_argv` passes `seat.permission_policy.as_deref()`.

### Verified
- `cargo test` in `rust/agenthub-runtime`: 35 passed, 0 failed, 0 ignored.
- The new `runner::tests::an_absent_policy_and_a_blank_one_deserialize_differently` was RUN BY NAME
  (`cargo test deserialize_differently -- --nocapture` → 1 passed, 34 filtered), so the both-cases assertion
  is proven rather than inferred: a JSON seat with no `permission_policy` key deserializes to `None` and
  launches `yolo`, while a seat with `"permission_policy": ""` deserializes to `Some("")` and is refused with
  the `empty` message. `runner::tests::every_documented_policy_maps_and_an_unknown_one_is_refused` now asserts
  the omitted case too.
- TOOLCHAIN — a reproducibility constraint, not a local quirk: this machine's rustup has NO default toolchain
  (`rustup toolchain list` returns none although the directory exists), so the run went through
  `/home/daihu/.rustup/toolchains/stable-x86_64-unknown-linux-gnu/bin/cargo` with `RUSTUP_TOOLCHAIN=stable`
  set. A fresh box with no toolchain installed cannot run these tests at all.
- `cargo fmt --check` reports ONE pre-existing diff, in `src/context/mod.rs` (a leading blank line). It is NOT
  drift from this change and it is deliberately left alone: reformatting an unrelated file is how a fix
  acquires a second, unreviewable subject.
- The INSTALLED `agenthub-runtime` binary is unchanged, so live launch behaviour is unchanged until it is
  rebuilt and reinstalled. That is a change to the owner's box — the ENDPOINT of this cutover rather than a
  step inside it — and it does not block acceptance: a runtime built from the pinned commit launches side by
  side via `--state-dir` and `--socket` without touching the installed one, which is exactly how the native
  `up` acceptance already ran. The harness needs a build of the pinned commit, never the installed binary.
- Repo and path, so nothing has to guess which checkout: this is the SAME repository as the native `up` commit
  `354831d` — `agenthub_client` (remote `git@github.com:phamhung075/4genthub-client.git`, main checkout
  `/home/daihu/__projects__/4genthub/agenthub_client`), branch `4genthub-min/u2-native-runner` in the linked
  worktree `/home/daihu/.openrig/agenthub-seats/4genthub-min/wt/u2-native-runner`, with the crate at
  `rust/agenthub-runtime/` inside it. `354831d` is NOT an ancestor of that repo's `HEAD`, so a reviewer builds
  from the hash explicitly.
- The reviewer's B1p check goes green by ITS OWN re-run of the two tests above, not by this note.

## 2026-10-13 — `up` starts a room from the local team definition, and `--room-only` says what it excludes

`up` restored rooms through OpenRig (`rig up RIG --existing`) and started the runtime daemon with no state
dir at all: `clientruntime.EnsureDaemon` spawns `agenthub-runtime --socket PATH daemon` and drops both
streams, so the daemon landed on this machine's default state tree and a start that failed had nothing to
read. A room that did not already exist could not be started by that path either: `StartRoomByName` sends
only `{room, cwd}` and the runtime cannot invent seats.

### Added
- `internal/clientteam/roomspec.go`: `LoadRoomSpec(teamDir)` reads `<teamDir>/team.json` into a
  `clientruntime.RoomSpec` — `room.slug` to the room, seats to `[]SeatSpec`, `links` to `[]SeatLink`,
  `seat_overlays` to the overlays — beside `loadTeam`, because team.json has ONE reader. An unreadable
  directory, a room with no slug, or a seat with no key is a usage error NAMING the directory.
- `up RIG --team DIR [--state-dir DIR] [--room-only]`. `--team` is required and has no default, and the room
  name must equal the definition's `room.slug` — the rule clientpolicy applies to its own `--rig`.
  `--state-dir` resolves through `clientenv.ResolveStateRoot`. `--room-only` runs the room and stops: it
  starts the daemon, starts the room from the definition, prints one line per seat, and EXCLUDES rigd, the
  bridge, the watch view, the UI and herdr, whose failures are not this verb's verdict. The help carries the
  acceptance invocation so a reader re-reads the signature instead of remembering it.
- `internal/clientruntime/ipc.go`: `(*Client).Reachable()`, the reading `EnsureDaemon` now polls, named once
  so a caller that starts the daemon itself — with its own state dir and log file — asks the same question.

### Fixed
- `up`'s daemon step passes an ABSOLUTE `--state-dir` and sends the daemon's own output to
  `<stateDir>/daemon.log`, which is what lets a partial room name its log; the daemon is `detach`ed so it
  outlives the verb.
- `up`'s room step is `StartRoom(LoadRoomSpec(teamDir))`, a `GetRoomStatus` poll to a window held in a
  variable, then one line per seat: exit `0` every seat running, `2` the arguments or the definition wrong,
  `3` a seat not running with the seats and the daemon log path named — this repo's own `ExitUsage` and
  `ExitUnavailable`, not new numbers.
- `SeatSpec.PermissionPolicy` is a pointer. An OMITTED key means the documented unattended default and a
  present-but-empty one is refused by name, and a plain string with `omitempty` cannot say both on the wire:
  one of the two collapses into the other. `definedField` reads it for that reason (`optionalField` folds an
  empty string into "absent", which is right for `model` and wrong here). The runtime's own `Option<String>`
  is the other half of the same row.

### Verified
- `go vet ./internal/...` exits 0; `go test ./...` 23 packages ok, 0 failed; `gofmt -l` clean on every file
  this change touches.
- Live acceptance, scrubbed `HOME`, zero cloud environment, no `XDG_RUNTIME_DIR`:
  `up u1demo --team DIR --state-dir DIR --room-only` printed `u1demo/solo: running` and exited 0. The tree it
  wrote (`rooms/u1demo/spec.json`, `seats/`, `logs/`, `audit/`, `daemon.log`) is ALL under the `--state-dir`
  it was given — the runtime materialised the spec from the bytes the client sent, which `StartRoomByName`
  could not have done — and the per-process instrument is the daemon's own argv,
  `agenthub-runtime --state-dir … --socket …/run/runtime.sock daemon`, under the scrubbed HOME. The `2` path
  (`up other-room` against that definition) exits before the daemon is reached.
- Declared limit: the live `3` partial verdict was NOT reproduced. Three seat shapes — runtime
  `no-such-runtime`, cwd `/definitely/not/here`, and a non-omp seat — were each reported `running`, so a
  started seat may never be reported not-running and the path is unforceable until the runtime stops
  over-reporting readiness. The `3` verdict's evidence is the seam test and the code path, not a live run.
- The full `up` was NOT run here: `recordRig` writes the SHARED rigd.json and `startRigd` starts this
  machine's rigd, which is the owner's live state — the second reason `--room-only` exists.

## 2026-10-13 — the connector speaks TLS, and a test that dials proves it

`DialConnector` never installed TLS: for an `https` base URL it mapped the scheme to `wss`, appended `:443`,
dialled a plain TCP socket and wrote the HTTP/1.1 upgrade onto it. The TLS terminator answered its own
`400 The plain HTTP request was sent to HTTPS port`, so the handshake never reached the application, no
session was ever registered, and `GET /api/v2/sessions` served `{"sessions": []}` for a machine whose rigd
had been retrying for two days (`rigd.log`'s first line and its last are the same refusal, 5155 lines
apart). It survived its gate because no test dialled: every rigd case replaces the `dialConnector` seam, and
this file's tests are frame- and redaction-level by their own stated rule.

### Fixed
- `internal/clientsync/connector.go`: `DialConnector` wraps a `wss` socket in `crypto/tls` with the dialled
  host as `ServerName` and ordinary certificate verification. `connectorTLS` is the seam a test replaces to
  trust its own listener; there is no insecure mode and nothing sets `InsecureSkipVerify`. The handshake
  runs under the deadline the dial already set, and a `ws://` base URL keeps the plaintext path unchanged.
  The socket then needs a token carrying `sessions:write`: the server applies no scope default, so a token
  minted without scopes is refused at the next step.

### Tests
- `internal/clientsync/connector_test.go`: `TestDialConnectorSpeaksTLSOverARealListener` stands up a real
  `httptest.NewTLSServer` whose handler answers the upgrade by hand, so the client's `Sec-WebSocket-Accept`
  check can only pass for a request that arrived inside the tunnel, and a plaintext dial cannot reach the
  handler at all. It fails before the fix and passes after it.
  `TestDialConnectorLeavesPlaintextAlone` is the control that `ws://` still dials plaintext.

### Verified
- `go test ./internal/clientsync/... -run 'TestDialConnectorSpeaksTLSOverARealListener|TestDialConnectorLeavesPlaintextAlone' -v`
  exits 1 before the change and 0 after; `go build ./...` and `go vet ./internal/clientsync/...` exit 0;
  the whole module `go test ./...` exits 0 with no failing package.
- Credential-free, read-only probe against the deployed route: plaintext to `api.4genthub.com:443` answers
  nginx's `400 … The plain HTTP request was sent to HTTPS port`, while TLS to `/ws/connector` answers the
  application's own `400 Expected WebSocket upgrade` (`ws_mount.go:918`) - the transport the fixed client
  now uses reaches the application rather than the terminator.

## 2026-10-13 — `rig queue` answers from the daemon, `rig capture` exists, and a queue note is readable

Row A of the R7 follow-up. R7's compat list names `whoami`, `send`, `queue`, `ps`; the daemon's own
`commands.captureExamples` advertises `rig capture`. Both are fixed here on top of the verdict changes.

### Fixed
- **`rig queue` answered a false empty.** Measured before: `rig queue --json` → `[]`, exit 0, and the bare
  form printed "No pending items in local queue." while this rig's OpenRig queue held the rows the lead had
  routed. On a rig whose durable work *is* the queue, a false empty is worse than an error: a seat trusts it
  and believes it owes nothing. The verb now reads the daemon (`GET /api/queue/list` with
  `destinationSession` of this seat's session, `activeOnly=1`, `compact=1`) and renders one line per row. An
  empty document is reported only as a **measurement of a named session against a named daemon**
  (`Measured 0 pending items for <session> on the OpenRig daemon at <url>.`); with no daemon configured it
  says nothing was measured and exits 3 (`set OPENRIG_URL or OPENRIG_PORT`); a daemon refusal is reported
  with its status and reason, never flattened to `[]`.
- **The list's own parameter was dropped.** The route reads `destinationSession`; `--destination` now
  reaches it, so `rig queue --destination 4genthub-min-reviewer@4genthub-min` answers with that seat's rows
  (live). Same class as the defect, in the same verb, found by reading the route rather than the output.
- **A queue write is refused by name, in both its spellings.** `queue update <id> --note ...`,
  `queue update <id> --state done`, `claim`, `block` and the rest are writes this shim does not perform:
  exit 2, nothing sent, stderr naming the write and pointing at the OpenRig CLI — the subcommand is judged
  **before** the flag parser runs, so the flag spelling is not answered with a complaint about the write's
  own argument. (Only an undeclared flag on a *read*, e.g. `queue list --nope`, is answered by the parser.)
  Measured before: a write reported success while its note was unreadable anywhere the caller could look.
- **`capture` reported a successful read as a failure when the body omitted `ok`.** The transport's status
  is the refusal channel (any non-2xx is an error before the body is read) and the daemon's success shape is
  `{ok, sessionName, content, lines}` — but a 200 body without an `ok` field was read as a refusal and
  printed the raw payload to stderr with exit 1. An absent `ok` is now a success; only an explicit
  `ok:false` is a refusal. (Found by the reviewer's request-capture stub, which names the pane `text` and
  omits `ok`; the daemon's own keys were re-measured live to confirm the strict half still bites.)
- **`send` dropped `deliveryId`.** The route reads it (`transport.js:76`) and the OpenRig CLI always sends
  one (`send.ts:475`, `deliveryId: randomUUID()`); the daemon uses it as this delivery's outbox id
  (`session-transport.js:1143`) and refuses a reuse that names other content instead of re-delivering it
  (`:1160`). The shim now sends a fresh v4 identity per invocation — never a constant, which would make a
  deliberately repeated message a replay of the first.

### Added
- **`rig capture <session> [--lines <n>] [--json]`** (`internal/clientruntime`): the verb the daemon's own
  `whoami` advertises, through `POST /api/transport/capture` on the same route the R7 receiver-side proof
  used. The pane goes out verbatim, no heading added.
- **`rig queue show <qitemId>`** and **`rig queue transitions <qitemId>`**: a note the queue accepts does not
  ride the row — `update --note` writes it into the row's transition log, and the row document carries no
  note field at all (`transitionNote`, `humanDetail` and `resolution` are null on every row read, including
  the ones with notes). `transitions` reads the log, which is where the note actually is.

### Changed
- One parser for the verb surface this row touches: `queue` and `capture` take their options before, between
  or after the positionals through `clientruntime.SplitFlags`, the helper the verdict commit added for
  `send` (an undeclared flag stops the verb rather than becoming a positional). The private
  `reorderFlags` helper is gone. `whoami` keeps its own flag set: it takes no positionals.

### Tests
- `internal/clientruntime/openrigdaemon_test.go`: the queue list asks for THIS seat's `destinationSession`
  with `activeOnly=1`/`compact=1` and prints every row the daemon returned; an empty document is a named
  measurement; a daemonless shell gets the refusal, not an empty list; a daemon failure is surfaced;
  `transitions` prints the note the row does not carry; `show` names the row's state and blocker; the write
  shapes (`update <id> --note`, `update <id>`, `claim <id>`) exit 2 with **zero requests**; `--json` prints
  the daemon's own document; `capture` prints the pane and takes `--lines` on either side of the session;
  `OpenRigSend` names the delivery with a fresh v4 id on every invocation.

### Verified (live, 4genthub-min rig, binary built from this commit at `/tmp/r7-verdict/4genteam`)
- `4genteam queue --json` → 4 rows, including three **this seat did not create** (U2 `…86cab906`, U1+U0
  `…da1e5cce`, and the blocked owner-action row `…416a3da42a595995`); `4genteam queue` → the same rows, one
  tab-separated line each.
- `4genteam queue --destination 4genthub-min-reviewer@4genthub-min` → that seat's two rows (the delta gate
  and U3). `queue show qitem-20261013193957-ee8cadf348fe93c9` → `done`, `closed: handed_off_to ->
  4genthub-min-reviewer@4genthub-min`, `tsUpdated: 2026-10-13T20:00:30.422Z`. `queue transitions` on the same
  row → my own custody note (`custody-verified: …`) and the delivery note, both readable.
- `4genteam queue update <id> --note "…"` → exit 2, `unknown flag --note for this verb; nothing was read`,
  and the row's `tsUpdated` unchanged (nothing written); `queue claim <id>` → exit 2, `"claim" is not a read
  this shim performs … Nothing was read or written.`
- `env -u OPENRIG_URL -u OPENRIG_PORT 4genteam queue` → exit 3, `no OpenRig daemon is configured in this
  environment (set OPENRIG_URL or OPENRIG_PORT); nothing was measured`.
- `4genteam capture 4genthub-min-reviewer@4genthub-min --lines 4` → the pane; two sessions → usage, exit 2;
  `--nope` → exit 2 naming the flag; daemonless → exit 3.
- `go build ./...`, `go vet ./...`, `go test ./...` exit 0 (23 packages ok); `gofmt -l` clean on both
  changed packages. A live `send --raw` through the binary returned `Sent to …` plus the advisory, and
  recorded its sender-side outbox row.

### Named, not silently fixed
- **`queue list --json` prints `[]` for an empty queue.** That is the daemon's own document, printed
  verbatim — the same rule the rest of this shim follows for `--json` — and the *human* form is the one that
  carries the named measurement (`Measured 0 pending items for <session> on the OpenRig daemon at <url>.`).
  It is only a true empty because the request reached a named daemon: a shell that names none exits 3 and
  prints no list at all. Named here because `[]` is the exact string this row exists to banish from the
  surface a seat reads.
- **A `--raw` send is not a wrapped delivery, so a receiver cannot count it as one.** `--raw` (and
  `--dangerously-interact`, which implies it) sends the text exactly, with no `From:`/`To:`/`Sent:`
  envelope — the CLI's own semantics (`send.ts:474-475`: `raw = opts.raw || opts.dangerouslyInteract`, then
  `outboundText = raw ? payload : wrapSendBody(...)`, and the envelope marker is omitted). The outbox row
  still holds the text and the daemon still reports the delivery, but **consumption on the raw path is
  unconfirmed**: a receiving harness that counts only enveloped sends sees a bare tag, and the shim's own
  `--verify` note says the same in the CLI's words. Recorded rather than changed; `--raw` means exactly this.
- **`deliveryId`'s effect, precisely.** With the typing guard off the id is inert: the outbox store mints
  its own row id (`outbox-<stamp>-<hash>`, measured live on this daemon), and the emitted send is unchanged.
  The id matters on the guarded path (the idempotency check at `session-transport.js:1160`) and for
  cross-host relay naming (`:2373`). The shim matches the CLI's request; no behaviour change was observed on
  an unguarded daemon, and none is claimed.
- **The CLI's own `queue update --note` read path is not fixed here.** The note is written to the transition
  log while the row it prints carries no note (the lead measured this through the CLI, not the shim). This
  shim now reads the log — the surface where the note is — and refuses writes it cannot perform; the
  daemon/CLI row read is a separate change for whoever owns that surface.

## 2026-10-13 — the R7 shim's `send` flags, and the daemon's advisory the shim was dropping

The two majors from `GATE-08c41a5-r7-shim-2026-10-13.md` (verdict: approve with changes), plus the
minors in the same files. Stacked on `08c41a5` on branch `4genthub-min/r7-shim`.

### Fixed
- **M1 — the CLI's documented `send` flags were parsed as message text.** `internal/clientseat`: the verb used Go's `flag` package, which stops at the first positional, so the exact form the daemon's own `whoami` prints as `commands.sendExamples` — `rig send <session> 'message' --verify` — read as rig=`<session>`, seat=`"message"`, message=`--verify` and delivered **nothing to the named seat**; `--verify` in front was rejected outright (`flag provided but not defined`). Every documented option is now parsed **wherever it sits on the line** (`internal/clientruntime/flags.go`, `SplitFlags`): an undeclared flag is an error and an error stops the send (`nothing was sent`), never a positional. Implemented because this shim has a daemon: `--verify`, `--force`, `--wait-for-idle <s>`, `--raw`, `--dangerously-interact` (implies `--raw`, carries `--reason`), `--json`, and `--from` (for this shim's own native `RIG SEAT MESSAGE` shape). **Refused, because they address a surface this local shim does not have**: `--to`, `--pod`, `--rig` (fan-out), `--host` (remote), `--context` (pack resolves) — exit 2, nothing sent, each naming why. The invariant: no invocation may deliver to a destination other than the named seat, and no flag may silently change what is delivered.
- **M2 — the daemon's `warning` was discarded.** `internal/clientruntime/openrigdaemon.go` + `internal/clientseat`: the success payload carries `warning` and the shim printed a bare `Sent to X`. As of OPR.0.4.3.28 the readiness guard sends-with-advisory (only `needs_input` hard-refuses), so that warning is the **only** signal a seat gets that its message went into a pane that was not reading. It is now surfaced the way the CLI does — `Advisory: <warning>` on human output — and `--json` prints the daemon's answer verbatim, so the field rides both. A `retained` outcome is reported as the CLI reports it (`Retained, not delivered to <session>. <warning>`), never as a delivery.

### Changed
- `whoami --json` asks the daemon for `compact=1` by default and prints the CLI's **identity-recovery allowlist** (`projectCompactWhoami`: `resolvedBy`, `identity`'s eight fields, `peers`' three, `peersNote`, `edges`, `transcript`'s `path`/`tailCommand`); `--full` (and the CLI's `--verbose`) skips both and prints the daemon's bytes as they arrived. The previous behaviour printed the full document always and re-indented it.
- A refused daemon call on these verbs now exits **1** (the CLI's code for a 4xx; `whoami` previously answered the platform-matrix 3 — for both 404 and 500). Exit 3 stays for a shell that names no daemon and for a daemon that is not answering.
- The envelope renders the sender label **exactly as it arrived** (only the blank *test* is trimmed) and the occupant generation by **presence**, truncated to eight characters — both the CLI's own conditions (`m3`, `m4`).

### Tests
- `internal/clientruntime/openrigdaemon_test.go`: `SplitFlags` takes an option before, between and after the positionals, honours `--name=value` and `--`, refuses an undeclared flag, a value where none is taken and a missing value; the optioned body carries `verify`/`force`/`waitForIdleMs` under the route's own names, `--raw` sends the text exactly, `--dangerously-interact` carries its reason; the envelope's padded label and 3-character generation; `whoami` asks for `compact=1` and its projection drops `contextUsage`/`commands`/`attachmentType`/a peer's `label`, while `--full` prints the daemon's bytes; a 404 exits 1.
- `internal/clientseat/send_test.go`: the daemon's own trailing `--verify` form and its leading form both deliver to the named seat with the flag carried, never in the text; the advisory reaches stdout; `--to` is refused with nothing sent; an unknown flag is refused; `--verify` prints the daemon's verdict plus the named un-run probe; a `retained` outcome is not a delivery; `--wait-for-idle 30` becomes 30000 ms and `0` is a caller error; `--json` prints the daemon's answer verbatim.

### Verified (live, 4genthub-min rig, binary built from this commit at `/tmp/r7-verdict/4genteam`)
- The daemon's own printed example, `4genteam send 4genthub-min-reviewer@4genthub-min "<text>" --verify` → exit 0, `Sent to …`, `Advisory: target pane appears mid-task; sent anyway (busy is advisory, not a block).`, `Verified: no`, `Delivery: rendered-unconfirmed (landed; pane re-render not confirmed - confirm with: rig capture …)`, and the Note below. The receiver's pane (captured through the daemon) shows the envelope and the text: `To:`, `Sent: 10-13 19:56Z · gen 17ec5cb7`, the probe line.
- `--raw --json` → the daemon's answer verbatim on stdout, exit 0. `--wait-for-idle 5` against a busy pane → the daemon's own `HTTP 409 Target was still busy (bash) when the 5000ms wait ended (10 observations). No text was sent.`, exit 1. `--to a@r,b@r` → exit 2, `--to is a fan-out to several sessions; this shim sends to one; nothing was sent`; `--nope` → exit 2; `--wait-for-idle 0` → exit 2.
- `go build ./...`, `go vet ./...`, `go test ./...` exit 0 (23 packages ok); `gofmt -l` clean on both changed packages.

### Named, not silently fixed
- **m5 (the `Sent:` stamp) is counter-evidenced, not changed.** The verdict read the stamp as coming from the daemon's transport time; both of the CLI's `wrapSendBody` call sites (`send.ts:471`, `:851`) pass `stampISO: new Date().toISOString()` — the sender's own clock, which is what the shim already did. No daemon-supplied stamp exists on this path.
- The `· gen <first8>` suffix is the shim's **addition**: no CLI send path passes `meta.genUuid`, so the CLI's own envelope has no gen line. Kept (the gate accepted the envelope, and the daemon sets the occupant generation this shell carries), with the renderer now matching the CLI's condition.
- `--verify` reports the **daemon's** verification (`outcome`/`verified`; `delivered` means the text and the Enter landed and the post-send capture re-confirmed the snippet) and prints the CLI's own line for an effect check that **could not run**, naming the reason: the CLI's client-side staged-residual probe plus its one guarded Enter is not implemented here, so nothing presses Enter into a peer's pane uninvited and no verdict claims more than was measured.
- The compact projection's field SET is the CLI's; its key ORDER is Go's (sorted), not the CLI's insertion order.

## 2026-10-13 — `rig send` and `rig whoami` work on a rig the OpenRig daemon hosts

### Fixed
- `rig send <session> <text>`: `internal/clientseat` accepted only `RIG SEAT MESSAGE`, so R7's drop-in promise (`NEXT-GEN2.md` §1.5 — "existing seat prompts and skills work without modification") failed at the first call. Measured on the live 4genthub-min rig: `rig send 4genthub-min-architect@4genthub-min "..."` → `4genteam send: RIG, SEAT and MESSAGE are required`, exit 2; the verb's own fallback shells `rig send <rig>-<seat>@<rig> <text>` (two arguments) through the binary that IS the shim, so it re-entered the same refusal. Two arguments are now that shape: the message goes to the OpenRig daemon's `POST /api/transport/send`, wrapped in the OpenRig CLI's own `From:`/`To:`/`Sent:` envelope and attributed to `OPENRIG_SESSION_NAME` through `X-OpenRig-Session`. The three-argument shape is unchanged.
- `rig whoami`: the verb asked only the native runtime, whose room store (`~/.config/4genthub/rooms`) holds no row for a seat the OpenRig daemon started, and answered `seat identity could not be resolved from active rooms` (exit 3) on this rig. A shell that names a daemon (`OPENRIG_URL`, else `OPENRIG_PORT`) now asks it — `GET /api/whoami?sessionName=<OPENRIG_SESSION_NAME>` — and prints its document: `--json` prints the daemon's own bytes, the shape the OpenRig CLI returns. A shell naming no daemon keeps the native path unchanged.

### Added
- `internal/clientruntime/openrigdaemon.go` — `OpenRigSendMessage` plus the pieces both verbs share: the envelope renderer, the base URL (`OPENRIG_URL`, else the loopback address at `OPENRIG_PORT`), the optional `$OPENRIG_HOME/terminal-token` bearer (omitted when absent, as the CLI does), and the identity renderer.
- The client's own fifth safeguard is unblocked by the same fix: `sync messages`' deliverer shells `rig send <session> <text>` (`internal/clientsync/messagesverb.go`) and was refused by that arity bug.

### Tests
- `internal/clientruntime/openrigdaemon_test.go`: the daemon is asked for the environment's session and receives the sender header; `--json` prints the daemon's document; a 404 becomes exit 3 naming the daemon's reason; the envelope is pinned byte for byte (stamp, `· gen <first8>`, the `@external` reply spelling, the `<unknown sender>` label); base-URL and token precedence.
- `internal/clientseat/send_test.go`: two arguments post the envelope to `/api/transport/send` and name the session on stdout; the daemon's refusal exits non-zero; one argument is still a caller error and never reaches the daemon.

### Verified
- Live 4genthub-min rig, before (installed shim): `rig whoami --json` → exit 3, `seat identity could not be resolved from active rooms`; `rig send 4genthub-min-fe-dev@4genthub-min "probe"` → exit 2, `RIG, SEAT and MESSAGE are required`. After, built from this commit: `rig whoami --json` → exit 0 with the daemon's document (identity, four peers, transcript path); `rig send 4genthub-min-reviewer@4genthub-min "..."` → exit 0, `Sent to 4genthub-min-reviewer@4genthub-min`, and the receiver's own pane shows the envelope (`From: 4genthub-min-go-dev@4genthub-min`, `To: 4genthub-min-reviewer@4genthub-min`, `Sent: 10-13 18:56Z · gen 17ec5cb7`, `---`, text, `---`, `↩ Reply: rig send 4genthub-min-go-dev@4genthub-min "..."`).
- `go build ./...`, `go vet ./...`, `go test ./...` exit 0 with every package ok; `gofmt -l .` reports only the two pre-existing files (`internal/clientoutbox/commands.go`, `internal/clientwatch/render_test.go`).
- `internal/clientruntime/command.go` was run through `gofmt` while the branch was added, which normalizes the file's pre-existing drift (`shimCommand`/`queueCommand` method declarations); the other two drifted files are untouched.

## 2026-10-13 — `4genteam ps`, and the runtime-switch skills name the right `rig`

### Added
- `4genteam ps [RIG] [--nodes|--compact|--grid|--json]` (`internal/clientps`), reading tmux and the seat transcripts. A seat whose transcript tail shows a quota or credit message reads `quota`. `--nodes --rig R --json` answers the node-record keys the other verbs read (`logicalId`, `canonicalSessionName`, `sessionStatus`, `startupStatus`, `runtime`, `model`).
- `clientwatch.SeatSnapshot`, the exported log, runtime, model and context-token read that `ps` shares with `watch`.

### Changed
- The `rig` symlink maps `ps` to `ps` (it mapped to `status`), and `status --nodes|--compact|--grid` routes to `ps`.
- `skills/client/rig-runtime-switch` and `seat-runtime-switch`: `rig` on PATH is the 4genteam shim, so spec and node operations call the OpenRig CLI by path (`$OPENRIG`). `sync switch` only records the occupant, `seat reseat` must not be used to change a runtime (it left a seat dead), and a teardown blocked by "no server running" is cleared with a throwaway tmux session.

## 2026-10-13 — the seat-message deliverer ran only when a human typed it; it is now the fifth supervised safeguard

### Fixed
- A message the frontend sent to a seat (cloud `seat_messages` row `8df26eb8-9526-420b-9dad-e04ff4d59bd1`,
  room `4genthub-min`/seat `lead`, 2026-10-12T22:00:26Z) sat unread: the only consumer of that store was
  the manual `4genteam sync messages <room> <seat>` verb, the supervising rigd tree had died ~2.7 minutes
  before the POST (its rows' last beats 21:56:48–21:57:39Z), and nothing brought the deliverer back. The
  pass typed the stranded text into the lead's session and acked it — at-least-once: ledger append
  (`delivered/<room>-<seat>.ids`) first, ack last, so a crash between the two re-acks without re-typing.

### Added
- `4genteam sync messages --rig R [--watch]` (`internal/clientsync/messagesverb.go`): the rig shape derives
  each seat's session from `rig ps --nodes --rig R --json` (running seats only, by canonical session), and
  `--watch` runs one pass every 10 s until the context is cancelled — a pass that fails is logged and the
  loop goes on, because a deliverer that stops is how the message reached the cloud and stopped there.
  Sends are stamped `OPENRIG_SESSION_NAME=human@kernel` so the receiving seat sees the human sender.
- `MessagesLogPath` (`internal/clientlifecycle/paths.go`): `messages-<rig>.log`, what every pass printed,
  so a message that would not land stays readable after the fact.

### Changed
- The `messages` safeguard joins the health tables (`internal/clientservices/services.go`): `Safeguards()`,
  `RigScoped()`, `DefaultSilentAfter()` (10 s pass, 30 s silence), `Pattern()` (`4genteam sync messages
  --rig <rig> --watch`) and `Verify()`'s capacity — the five definitions that must move together, or the
  report and the process table start disagreeing about which process is the deliverer.
- `rigd` supervises it as each rig's third child (`internal/clientlifecycle/rigd.go`), beside compact and
  the watchdog, so delivery restarts with the machine's other safeguards rather than by hand. `doctor` and
  `lifecycle`'s summaries name it (`internal/clientlifecycle/doctor.go`, `lifecycle.go`).

## 2026-10-12 — trap 17: the served stylesheet is not the page's CSS

### Added
- `SKILL.md` trap 17, paid for on a live recheck of the redesigned frontend. The toolkit's own evidence
  artifact first concluded "no CSS rule gates `shimmer-spin` under reduce … the gate is in the app
  path", after grepping only the served `index-DW8gSl2a.css`. The reviewer's cross-check found the
  gate: `.shimmer-spin-layer { animation: none }` inside `@media (prefers-reduced-motion: reduce)`, in
  one of the **37 runtime-injected `<style>`** sheets — the page carries **38 stylesheets** (1 linked +
  37 injected), so a static grep of the served asset cannot settle a computed-style question. The
  observable was right (`div.absolute.inset-0` computes `animationName: none` under reduce) and the
  location was wrong.
- The trap records the procedure, not only the lesson: enumerate `document.styleSheets` in the page and
  walk each sheet's rules (a rule with `selectorText` is a style rule; one with `cssRules.length` and no
  `selectorText` is a container), recording the `CSSMediaRule.conditionText` chain over each match, then
  quote the sheet that actually carries the rule. Deliberately distinct from trap 16: 16 is "a report is
  only as wide as the map it reads", 17 is "the served stylesheet is not the page's CSS".

### Changed
- Trap 17 keeps only the rules that **match the element**: its matched rules are the ones whose
  `selectorText` matches that node and whose declarations set the property in question. The served
  `index-DW8gSl2a.css` (162,357 bytes, re-checked 2026-10-12) carries 19 `/shimmer/i` occurrences in
  **12 rules** — the asset is a single minified line, so the segments are the unit, not lines; three of
  the twelve are the ungated `.shimmer-modern` family, which gates nothing — and 0 `.shimmer-spin-layer`,
  so a grep hit in the served asset invites naming both the wrong rule and the wrong sheet. Source: the
  reviewer's cross-check of the trap item.

## 2026-10-11 — trap 15 listed two readers that annotate instead of stopping

### Fixed
- `SKILL.md` trap 15 said every page reader that misses the 30s readiness floor "FAILS naming that
  timeout and does not read past it", and listed `check-nav` and `check-responsive` among them. Both
  do call `waitForReady` (`check-nav.mjs:40`, `check-responsive.mjs:121`), but on `ready === false`
  they read the page anyway and append the reason to their own `chars < 120` FAIL (`:52`, `:132`), so
  a report can carry a row measured from a page that never reached the floor. Trap 15 now names them
  as the two that annotate rather than refuse; their behaviour is unchanged and both still exit
  non-zero, so no false green follows.
- Source: the parent gate's one minor — `GATE-7f318f6-range-readiness-2026-10-12.md` recorded the
  same reading ("annotate rather than refuse": `check-nav.mjs:40,52`, `check-responsive.mjs:121,132`)
  and the lead ruled to fix the TEXT, not the code. No code or test change; both checks are untouched.
- Follow-up, the lead's read of the sentence itself: "Two of them" was ambiguous, because its nearest
  antecedent is the seven-item list directly before it and the two are precisely NOT in that list —
  the same defect class this range exists to fix. It now reads "Two of the readers annotate rather
  than refuse the read:". Nothing else changed.

## 2026-10-11 — the seventh file: `check-login` waits for its page too

### Fixed
- `checks/check-login.mjs` carried the same un-gated read as the six files of the entry below, and it
  is the last one in the toolkit: after `goto('/login', { waitUntil: 'networkidle' })` it read the
  login form with instantaneous calls — three `locator.count()` assertions (`:38`), the card's
  `boundingBox()`, the toggle's `count()`, the password-type `evaluate()` (also an assertion) and two
  link counts (`:61`). A cold load therefore measured a page whose form had not mounted.
- Found by the census that entry asked for, and measured rather than argued: in an eight-check
  `all.mjs` run the same three locators were found `0 time(s)` (`textbox "Email Address"`,
  `textbox "Password"`, `button "Sign In"`), while the same check run alone on the same build passed
  2 of 2 with each locator present exactly once — the page renders them (`LoginForm.tsx:210`
  `label="Email Address"`, `:284` the `Sign In` button), so the red was a race, not a page defect.
- The gate is the same line as the six: `waitForReady` after the navigation, and when the floor is not
  reached within 30s the check FAILS naming that timeout and ends (`browser.close()` + `r.done`), so
  nothing below it is read. No assertion, locator or expectation changed. `SKILL.md`'s trap 15 now
  lists this file with the rest.

### Tests
- Fail path, deterministic — `node checks/check-login.mjs --base http://localhost:8123 --fe <checkout>`
  against a 60-byte `/login` printed
  `FAIL http://localhost:8123/login: the view never reached the 120-character readiness floor within 30s — the login form was not read (the reads below are instantaneous)`,
  exited 1, and printed no login-form verdict for that page.
- Happy path — `node checks/check-login.mjs --base http://localhost:3800 --fe <checkout>` twice: exit 0
  both runs, 7 `PASS` lines each (the three locators, the password input's type, sign-in reaching
  `/dashboard` with the shell, the banner account chip, sign-out back to `/login`), no `FAIL`.
  `node --check` clean.
- Full runner after the gate — `node checks/all.mjs --base http://localhost:3800 --fe <checkout>` →
  **all 8 checks passed** (exit 0, 284.63s), where the same command before the gate reported
  `1 of 8 checks failed`, this check and falsely.

## 2026-10-11 — every check that reads a rendered page waits for it (the ruling's six)

### Fixed
- The `waitForReady` gate is now in the measure path of the six checks that still read a
  client-rendered page too early: `check-contrast`, `check-motion`, `check-theme`, `check-backend`,
  `check-crud-animation` and `production-walk`. Each waits for the 120-character floor after its
  network wait and, when the floor is not reached within 30s, FAILS naming that timeout and does not
  read past it — so the six now behave like `check-nav` and `check-responsive` (fe56321) and no
  check can print a verdict from the pre-data page.
- `check-contrast` is the measured case: it read `/dashboard` at 10 characters and printed
  `PASS /dashboard: all 1 measured text runs meet AA` — a false green. Gated, the same command
  measures 23 runs and passes on the evidence (`/login`: 10 runs).
- `check-theme` returns `null` from an unready arm and its four call sites skip that route;
  `check-crud-animation` ends the check when the stylesheet map cannot be read (that map decides
  every verdict) and skips an unready route in the at-rest loop; `check-backend` records the failure
  and moves on (its inventory is only meaningful for a route that rendered); `production-walk` calls
  `die`, because a capture of a page that never rendered must never become a baseline or a diff. The
  reporter already tolerates a missing motion read, so `check-motion` needed no caller change.
- The goto-level warm-up and sign-out navigations are deliberately left ungated: they assert nothing
  about the page. `SKILL.md`'s trap 15 now records the whole sweep, with the false green that
  proved it.

### Tests
- All six against the a9d6c328 tree on the local chain (`vite → 127.0.0.1:8000 relay → production
  API`), each exit 0, no new red anywhere: `check-contrast --routes /dashboard,/login` (23 and 10
  text runs, 0 violations); `check-motion --routes /login,/dashboard` (`/dashboard`: longest
  animation 2500ms infinite `shimmer-spin` on `div.absolute.inset-0`; under reduce no long or
  looping animation survives); `check-theme --routes /login,/dashboard`; `check-backend` (endpoint
  inventory, no call ≥400); `check-crud-animation --routes /seats,/dashboard` (`'complete': 5/5
  class(es) defined in the shipped CSS`); `production-walk capture --routes /dashboard --no-shots
  --out /tmp/gate-capture` (captured `/login` and `/dashboard`, heading "Choose a workspace").
  `node --check` clean on all six files.

## 2026-10-11 — the recheck's own two defects: readiness, and the Complete report

### Fixed
- `fe-4genthub-driven/lib.mjs` gains `waitForReady(page)`; `check-nav` and `check-responsive` call it
  after their network wait. Neither `load` nor `networkidle` is readiness for a client-rendered
  app: the document is up while the view still holds its pre-data placeholder, and the swallowed
  `networkidle` timeout let the probe read that placeholder. Measured on the local chain (vite →
  127.0.0.1:8000 relay → production) on 2026-10-11: the same route rendered 6029 characters in one
  viewport and 10 in another viewport of the same run — a race, not a route defect; the same
  instrument failed 4 of 5 `/dashboard` viewport combinations at 10 visible characters. Both checks
  now wait on the very text they read and still fail a page that never reaches the floor, naming
  that timeout. After: `check-nav` 16 of 16 routes pass (6029 characters on `/`, 305 on
  `/dashboard`), `check-responsive` 48 of 48 combinations pass, no placeholder reads.
- `checks/check-crud-animation.mjs` no longer reports the five live `*RowCompleteAnimation` classes
  as undefined. It filtered those names out of a map collected from the CRUD matrix alone, and its
  sentence added "called from no hook/component" — a caller is not observable from a black-box walk.
  The map now asks for the Complete names and the line reports only what the stylesheet shows.
  Measured against the same origin, both runs on the same build: before `'complete': registered in
  AnimationFactory but defined in no CSS and called from no hook/component — inert until a caller
  exists`; after `'complete': 5/5 class(es) defined in the shipped CSS
  (taskRowCompleteAnimation, subtaskRowCompleteAnimation, seatRowCompleteAnimation,
  branchRowCompleteAnimation, projectRowCompleteAnimation)`. Both defects are recorded in `SKILL.md`
  as traps 15 and 16.

## 2026-10-11 — the fe-dev room gains a measured production recheck toolkit

### Added
- `skills/rooms/4genthub-min/fe-dev/fe-4genthub-driven/`: a read-only recheck of the deployed
  frontend, walked as a real signed-in user with the shared e2e account (read in-process, never
  printed). Eight checks — login, nav, backend, responsive, theme, motion, contrast, CRUD
  animation — run via `node checks/all.mjs`; `production-walk.mjs` captures an origin and diffs it
  against `baseline/production.json` (11 routes, measured 2026-10-11). The SKILL.md records the
  measured state honestly: a production run fails 4 of 8 checks — 7 WCAG AA contrast violations
  (#3b82f6 = 3.68:1, #16a34a = 3.30:1), an ungated `shimmer-spin` on /dashboard under
  prefers-reduced-motion, a 71px sideways drag at 1280 on all eight shell routes, and CRUD
  animation wiring (six dead branch/project classes, a subtask create truncated by 200ms) — plus
  the fourteen traps the toolkit's own development paid for.
- `skills/rooms/4genthub-min/fe-dev/taste-skill/` (`design-taste-frontend`): the design-rules skill
  the recheck references at `../taste-skill/SKILL.md`, included so the reference resolves in a
  fresh checkout.

### Tests
- `go test ./internal/clientskills/...` ok — the new tree's relative links resolve and the files
  follow the per-skill directory layout.

## 2026-10-11 — the watch grid showed no events for a Claude Code seat

### Fixed
- `internal/clientwatch/seatlog.go`, `internal/seatlog/seatlog.go`: a seat whose omp state directory existed was
  always read as an omp seat, even with no session in it. OpenRig writes that directory (holding only
  `agent/AGENTS.md`) for a Claude Code seat too, so the architect's grid pane showed its header and no events.
  An omp seat now needs a session in the directory; without one the Claude Code log is looked for.
- Test: `TestSeatLogPrefersOmpStateThenClaude` gains the empty-directory case; it fails without the change.

## 2026-10-11 — the withheld copy's mode and its seven-day window are asserted

### Tests
- `internal/clientsync/rigd_test.go`: `TestTheWithheldCopyIsPrivateAndKeptSevenDays` covers the two promises 4.4
  makes about the withheld raw copy — `<seat store>/rigd-withheld/<session>/<ts>.json` mode 0600 under a
  directory only its owner may enter, kept seven days (qitem-20261010234730, the coverage gap the rigd gate
  named). The case above it checked that the directory was non-empty and stopped there, so neither the mode
  (file 0600, directory 0700) nor the prune (a copy an hour past `withheldKeepDays` is gone after the next
  write; a copy an hour inside it survives) had a test at all.
- This is a coverage test, not a bug fix: `writeWithheld` and `pruneWithheld` already hold both properties, so
  there is no RED state to show. Both halves were instead INDUCED to fail by mutating the production lines and
  reverting them, because an assertion nobody has seen fail is an assertion nobody knows bites:
  mode loosened to `0o644` → `the withheld copy is mode 0644, want 0600: the raw event must not be readable by
  anyone else`; the cutoff comparison reversed → `a copy an hour past 7 days survived the write path` and `a
  copy an hour inside 7 days was pruned`. `git diff` on `rigd.go` after the reverts is empty.
- The cutoff instant itself is deliberately not asserted: it is computed from `d.Now()`, so pinning it to the
  nanosecond would fail on a clock change rather than on a behaviour change. An hour either side fails the same
  way if the comparison is reversed, which is what the induction shows.
- Result, this seat's own run: `gofmt -l` empty on both files; `go build ./...` rc 0; the case ok; the whole
  `internal/clientsync` package ok 1.139s.

## 2026-10-11 — `failing` beats a live process in `doctor`'s row verification

### Fixed
- `internal/clientservices/services.go` (`verifyRow`): the recorded-failing check now sits ahead of the
  liveness switch, as the architect ruled on 2026-10-11 (3fa2125d): the precedence is stopped -> failing ->
  silent -> running, and `failing` beats a LIVE process. rigd counted the deaths inside its window, and a fresh
  heartbeat does not undo a crash loop — before this, a crash-looping child read `running` for the whole
  interval in which its heartbeat was fresh, so 7.5's exit condition ("every row is `running`") could be met
  while the row was in fact being retried at the cap. The dead-process case is unchanged: a dead-and-failing
  row still reads `failing`, and a dead row with no recorded failure still reads `stopped`. This was the one
  the reviewer's gate to the rigd slice named (GATE-39e51529-rigd-phase1-go-half, minor m1), and the ruling
  document was already in main while the code did not match it.

### Tests
- `internal/clientservices/services_test.go`: the second half of `TestFailingIsDecidableOnlyByRigd` asserted
  the retiring reading — a live, beating child is `running` even while rigd records it as having failed. That
  expectation was retired with the ruling that changed the semantics, and now asserts `failing` with the
  restart count kept. It was not re-pinned to keep a green run.
- **RED FIRST, quoted:** this seat's run before the fix — `services_test.go:215: row = {Rig:demo
  Safeguard:compact State:running PID:77 StartedAt:2026-10-11T11:00:00Z Restarts:3 LastBeatAt:2026-10-11T11:59:59Z
  AgeS:1}, want failing: rigd counted the deaths, and failing beats a live process`.
- Result, this seat's own run: `gofmt -l` empty on both files; `go build ./...` rc 0; the case ok; the whole
  `internal/clientservices` package ok.

## 2026-10-11 — a relaunched omp seat gets the skills `sync rig` built for it

### Fixed
- `internal/clientseat/skills.go`: an omp seat reads skills from `<state root>/<seat session>/agent/skills` and
  nothing copied the store's `rig/agents/<seat>/skills` there, so a restarted fe-dev answered "No such skill" for
  `seat-worktree` while the skill sat in the store. `relaunch` (so `seat reseat` and `seat sync --relaunch quiet`)
  now replaces that directory with the store copy before it kills the session. It replaces and does not merge, so a
  skill the client tree no longer carries leaves the seat. A seat with no agent directory (claude-code) is left
  alone, and an omp seat whose store has no skills is refused.
- `internal/clientseat/skills_test.go`: replace not merge, non-omp seat untouched, refusal on a missing store copy.

## 2026-10-11 — `4genteam seat reseat` and `4genteam send`

### Added
- `internal/clientseat/reseat.go`: `seat reseat RIG --seat NAME...` restarts each named seat at its own
  quiet point, through the same quiet wait and relaunch as `seat sync --relaunch quiet`. `seat sync` only
  restarts seats whose pin changed, so a seat holding an old skill or a deleted cwd while its pin was
  current was never restarted and nothing in the client could do it. Unknown seats are refused before
  anything runs, and the DeepSeek peak gate applies (`--allow-peak` skips it).
- `seat reseat --force` skips the quiet wait for the owner who decides a restart cannot wait. The seat resumes its
  session; only the turn in progress is lost.
- `send [--from NAME] RIG SEAT MESSAGE`: one signed message to one seat. The text starts with
  `From NAME:` because `rig send` does not carry the sender outside a seat shell, and it reaches `rig`
  as one argument, so no shell expands backticks in it.
- `internal/clientseat/sync.go`: a `rig launch` that times out after rig's 5 s ("outcome UNKNOWN") counts as done
  when the seat's tmux session comes up within 3 minutes. The launch had landed and the client had stopped on it.
- `internal/clientseat/reseat_test.go`: order and quiet gating, a seat that is not quiet stays running,
  an unknown seat is refused before any seat is touched.

## 2026-10-11 — the post-compact resume passes the off-peak gate like every other automatic wake

### Fixed
- `internal/clientlifecycle/supervisor.go`: the witnessed-compaction resume was the one automatic wake the
  off-peak gate did not cover — 7.9's rule is that the gate covers every automatic wake, and the architect
  measured zero references to it on that path. `Step` now calls
  `clientoffpeak.Check(false, s.Nodes(s.Rig), s.Getenv)` before `Say(resumeText)`. `allowPeak` is false, so
  `--allow-peak` is never automatic on this path, and the predicate is the same one the gated verbs take, in
  UTC. A held-back resume is written through the supervisor's own log seam together with the gate's reason,
  so it is observable rather than silent.
- Two seams carry the gate's inputs, both defaulted in `New`: `Nodes` (the room's node reading — the same
  `rigNodes` this package already takes for its other gate, reached through the one platform matrix,
  `clientcmd.RequireRig`) and `Getenv` (the environment and clock the gate resolves from). An unreadable
  node reading yields no seats and therefore no refusal, which is the semantics the existing gate already
  has.

### Tests
- `internal/clientlifecycle/supervisor_test.go`: three cases, with the clock and the environment pinned
  through the `Getenv` seam rather than real time or the real environment: at peak the resume is NOT sent and
  the log carries `PEAK`; off-peak it IS sent, exactly `resumeText`; and `OFFPEAK_ONLY=0` under the SAME peak
  pin sends it — the old behaviour, and the control that makes the first case mean something.
- Red first, and quoted: before the fix the peak case failed with `the resume was sent at peak: [Context was
  compacted. Continue the job you were on: …]`, while the off-peak and gate-off cases already passed.
- Verified by the seat that committed it, not relayed: `gofmt -l` empty on both files; `go build ./...` rc 0;
  `go vet ./internal/clientlifecycle/ ./internal/clientoffpeak/` rc 0;
  `go test -count=1 ./internal/clientlifecycle/ ./internal/clientoffpeak/` ok; and the whole module,
  `go test -count=1 ./...` — 20 packages ok, 0 FAIL.

## 2026-10-11 — `4genteam rigd`: one long-running process supervises the rig's safeguards, and `doctor` names the one that is down

**What it is.** `rigd` is the always-on half the rig never had: one process per machine that keeps the
compaction supervisor, the rig watchdog and the connector bridge alive, reports their state, and refuses to
let `up` continue when one of them did not come up. It is a verb of the client binary, per
`rigd-boundaries.md` section 1 — it extends `internal/clientsync/connector.go`; it is not a second program.

**The supervision.** `internal/clientlifecycle/rigd.go` builds the child list from the listed rigs — one
compaction run and one watchdog per rig, ONE bridge per machine, each with the silence threshold of section
7.4 (30 s supervisor, 180 s watchdog, 360 s bridge, 75 s rigd, overridable by the flags it launched with) —
spawns them through the existing `spawnChild`/`awaitStart` seam, adopts a live child instead of duplicating
it, restarts a dead one with 1 s doubling to 120 s, and marks a safeguard `failing` after three deaths
inside ten minutes while still retrying at the cap. The old
`continuing without a running compaction supervisor` escape hatch is gone: `compact` no longer spawns
anything, it records the room and refuses with exit 3 when rigd is down.
`StartSupervisor`/`StopSupervisor` are deleted for the same reason — starting and stopping are rigd's now.

**The health report.** `internal/clientservices` holds the one process table (`Pattern`), the four states
(`running`, `silent`, `stopped`, `failing`), the heartbeat layout of section 7.2 (a temp-then-rename
`{pid,at}` per safeguard, plus rigd's own beat on every pass), the 7.5 state line, and the precedence rule
that decides them: the process table first, and the heartbeat only when the process is live. The facts a
pgrep and a heartbeat cannot answer — restarts, `started_at`, `failing` — are persisted in
`<seat store>/rigd.json`, mode 0600.

**The verb.** `4genteam doctor [RIG] [--json]` is read-only (its test records that spawn, signal and nodes
are never called), walks every listed room when no rig is named, prints the tail of `alerts.log`, exits 3
naming each down row on stderr, and exits 0 only when every row is running.

**Transitions.** A safeguard changing state is published to the socket and appended to `alerts.log` with
its UTC stamp — latest-wins, so a flap cannot bury the current state.

**Tests.** 30 cases across `clientservices`, `clientlifecycle/rigd.go` and `clientlifecycle/doctor.go`: the
7.4 threshold table and its 3× derivation, process-over-heartbeat precedence in all five shapes, the
strictly-past boundary, backoff asserted as start instants, three deaths inside ten minutes, adoption, one
heartbeat per compaction Step, the `rigd.json` mode and once-only `AddRig`, and the two `up` refusals (a
missing credential named without its value; the safeguards that did not come up named before the view
opens). **The acceptance carries its own falsification rather than an assertion:**
`TestKilledOrSilentSafeguardIsNamedAndDoctorRefuses` writes the down fixture and asserts exit 3 with the
exact line `<rig> <safeguard>: <state>, age <n>s`, and
`TestEverySafeguardRunningIsExitOKAndARunningRow` runs the SAME fixture with the safeguard alive and beating
and asserts exit 0 — a doctor that simply refused everything fails the second.

**Verified by the seat that committed it, not relayed:** `gofmt -l` empty on all 24 touched files;
`go build ./...` rc 0; `go vet ./internal/clientservices/ ./internal/clientlifecycle/` rc 0;
`go test -count=1 ./...` in this module — **20 packages ok, 0 FAIL**. The red-first inductions are the
worker's record; the reviewer's gate re-runs them.

## 2026-10-11 — `4genteam team prune`: remove the seats a team no longer defines

### Added
- `internal/clientteam/prune.go`: `4genteam team prune --team DIR [--dry-run]` lists the cloud room's seats, deletes those `team.json` does not define (`DELETE /api/v2/openrig/rooms/{room}/seats/{seat}`), and refuses to delete anything when the listing cannot be read. `apply` only creates, so a seat dropped from `team.json` stayed in the room and `sync rig` kept building it into the rig.
- `internal/clientteam/team.go`: the verb is registered and listed in the usage.

### Verified
- `go test ./internal/clientteam/` -> ok, including four new cases: deletes only the undefined seats, a dry run deletes nothing, an unreadable listing deletes nothing, `--team` is required.

## 2026-10-11 — `4genteam evidence`: the client half of the evidence route

### Added
- `internal/clientevidence/`: the `evidence` verb — `4genteam evidence --task <id> --base <sha> [--test "<cmd>"] [--url BASE]`. It resolves both SHAs to full 40-hex commits, keeps `git diff --numstat`'s output VERBATIM as the numstat field, and runs the `--test` command (split in-process, never through a shell) to report its exit code and the failing test NAMES it recognises (`go test -v`, pytest's short summary, TAP). The payload is `base_sha`, `head_sha`, `numstat` and the test block and nothing else: no file contents and no command output ever reach the wire, which is the O3 box's privacy rule. `--url` defaults to `$AGENTHUB_URL`; the credential is `$AGENTHUB_TOKEN` as a bearer header, the same path `4genteam feedback` uses, and it is never printed.
- `cmd/4genteam/main.go`: the verb is registered on the dispatcher (one append, the package's own `Commands()`).
- Exit codes: `0` stored; `5` when the evidence WAS stored but the test command exited non-zero — a plain `0` would swallow the failure the submission exists to describe, and the remote-failure code would claim the submission failed when it did not; and a `409` reads as a REFUSAL naming the head_sha that already has evidence, on stderr, never as a success.

### Verified
- `go test ./internal/clientevidence/ -count=1 -v` -> 15 cases ok, including the frozen payload's exact key set, the leak-freedom assertion, the conflict-as-refusal case, and the transport-failure case that names the reason and never the token. `go build ./...` rc 0, `go vet ./internal/clientevidence/... ./cmd/4genteam/...` clean, `gofmt -l` empty, `go test ./... -count=1` -> 19 packages ok, no FAIL.
- NOT COVERED, named rather than implied: no case talks to the real route. The server half of O3 is being built to the same frozen contract in `agenthub_go`, and this verb's HTTP seam is stubbed in every case, because a push path cannot be exercised against a cloud from the suite.

## 2026-10-10 (late night) — a refused send is audited even when the policy cannot be read

### Fixed
- `cmd/seatcheck/main.go`: a send that never reached a decision now appends its audit line before it
  fails, instead of failing silently. The friction (`cbb1777a`, found by the writer on the live rig)
  was `seatcheck send` exiting 2 on a policy path resolved under the seat state dir and writing
  NOTHING, while the box this guard exists to satisfy claims every message is audited — allowed or
  denied, with its reason — so a refusal that happened before the decision was invisible in the store.
  The three failures that can block the decision now write `Allowed:false` with a reason naming the
  failure — `policy unreadable: ...`, `policy unparsable: ...`, `policy belongs to seat "x", not "y"` —
  and then return the same non-zero code as before. A seat DIRECTORY that does not exist is refused
  WITHOUT a line: there is no store to append to, and the guard does not create one in order to record
  its own refusal. A failed audit WRITE keeps its own exit code (1), because the reason is then
  unrecorded and the caller must see that.

### Added
- `cmd/seatcheck/main_test.go`: `TestSendPolicyMissingOrCorruptFailsClosed` now asserts the line —
  exactly one, `Allowed:false`, from the calling seat, to the requested recipient, with a reason naming
  the failure — for both the missing and the corrupt policy, and `TestSendRefusesAPolicyOfAnotherSeat`
  does the same for the wrong-seat policy. Both asserted `len(records) == 0` before this change: the
  defect was pinned in the tests themselves, which is why they are rewritten rather than extended.

### Verified
- FAIL-first, measured rather than argued: with the expectations in place and the guard untouched,
  `go test ./cmd/seatcheck/ -run 'TestSendPolicyMissingOrCorruptFailsClosed|TestSendRefusesAPolicyOfAnotherSeat' -count=1`
  -> `audit = [], want exactly one line for the attempt` (missing and corrupt) and
  `audit = [], want one denied line naming the mismatch`. After the change: `ok`.
- Through the REAL binary, in the friction's own configuration (store baked in with
  `-ldflags -X main.installedPinsDir=<store>`, a seat directory present with no `policy.json`):
  `seatcheck send --to lead --intent report -- probe` -> `rc=2` and the store gains
  `{"At":"...","From":"go-dev","To":"lead","Intent":"report","Allowed":false,"Reason":"policy unreadable: open <store>/4genthub-min/go-dev/policy.json: no such file or directory","PolicyHash":""}`.
  With the seat directory absent instead: `rc=2`, and the store still contains no seat directory.
- Whole module: `gofmt -l cmd/seatcheck` empty, `go vet ./cmd/seatcheck/` rc 0, `go build ./...` rc 0,
  `go test ./... -count=1` -> rc 0, 18 packages ok, no FAIL.

## 2026-10-10 (late night) — the shipped agent-browser skill stops linking files it never shipped

### Fixed
- `skills/share/agent-browser/SKILL.md`: the ten local links in the "Deep-Dive Documentation" and
  "Ready-to-Use Templates" tables pointed at `references/*.md` and `templates/*.sh` that this
  repository never vendored — no commit in its history ever tracked one — so every seat that received
  the skill dead-ended on the first `read`, and the `./templates/...` block told a seat to execute
  three paths that do not exist. The rows are plain file names now, both tables say the files are
  upstream-only, and the invocation block is deleted. No body prose was rewritten. Finding 5 of
  `GATE-49e8d433-post-hoc-six` measured the ten; this entry is the fix half.

### Added
- `internal/clientskills/links_test.go`: `TestShippedSkillLinksResolve` — every local link in every
  shipped `SKILL.md` and in the companions beside it (`OPENRIG.md`, `LOCAL-INSIGHTS.md`) must resolve
  against its own directory. It fails on exactly the ten links above before this change and passes
  after it. Its header states the scope and the three exclusions (fenced code blocks, fill-in template
  documents, off-site targets), because a check whose scope is unstated is read as "everything
  resolves" — and 12 of this tree's 35 local link-shaped targets are placeholders inside template
  documents rather than links to files a skill promises to ship.

### Changed
- `skills/share/agent-browser/OPENRIG.md`: a modification-table row for the repair, and the
  vendoring-completeness statement — `add-supplementary-files` adds to an upstream body rather than
  completing its file set, so this canonical example ships the supplementary half and not every path
  its body names. Its frontmatter records the divergence.

## 2026-10-10 (late night) — the client starts its own bridge

### Added
- `internal/clientlifecycle/ensure_bridge.go`: `EnsureBridge`, called from `main` before every verb. When
  `AGENTHUB_URL` and `AGENTHUB_TOKEN` are set and no `4genteam bridge run` process exists, it starts the
  bridge detached (log `bridge.log`), so a machine reports to the cloud whenever the client is in use and
  no service or question about one is needed. Skipped for `help`, `version`, `bridge`, `stop`, `up` (which
  starts its own) and `seatcheck` (runs on every seat tool call). A failed start prints one stderr line and
  never fails the verb. Tests: the verb table and the no-credentials case.

### Fixed
- `BridgePattern` was the bare text `bridge run`, which `pgrep -f` and `pkill -f` matched against ANY command
  line containing it (a shell, an editor): `stop` and `up` could kill an unrelated process, and a presence
  check read as running. It is now `^[^ ]*4genteam bridge run`, anchored to the process's own command line.
  Measured: with the bare text the first live run started nothing; anchored, the first verb starts one
  bridge and a second verb starts none.

## 2026-10-10 (night) — one build step installs the binary and the checkout's .env

### Added
- `scripts/build.sh`: `go vet`, `go test`, build, an atomic install of `~/.local/bin/4genteam`
  (built beside the target and renamed, so a running seat keeps its old inode), and, when the checkout
  has a `.env`, a copy of it over `~/.config/4genthub/.env` at mode 600. Without a checkout `.env` the
  installed file is left alone. The file's content is never printed.

### Changed
- `README.md`: the from-a-checkout install names `scripts/build.sh`.

## 2026-10-10 (evening) — the peak gate's verb list is five, not three

The `offpeak` verb's own usage text and three documents named `up`, `compact` and `sync respawn` as the
verbs the DeepSeek peak gate covers. The tree gates five. `internal/clientlifecycle.PeakGate` is called
from `lifecycle.go:176` (`up`) and `:367` (`compact`), and from the one restore path,
`internal/clientrestore/restore.go:204`, which `continue` reaches at `continue.go:171` and `:212` and
`watchdog` at `watchdog.go:264`; `sync respawn` runs the same check inline at `respawnverb.go:153-161`,
against the node records the daemon already serves for that restore. The two newer verbs' own usage
lines said so, so the client contradicted itself in its own help.

Corrected in five places, no behaviour change: `internal/clientoffpeak/command.go:32` (the `offpeak`
usage text), `README.md` (the gate's paragraph and its failure-map row), `docs/AGENT-SETUP.md` (the
failure-map row and the `up` section), and `skills/client/offpeak-gate/SKILL.md` (its description and
its refusal section). No test pinned the sentence, and none changed.

One more correction in the same passage: `--allow-peak` was called "the ONE flag those verbs take",
which is false for the three verbs that carry arguments of their own — `continue` takes a brief file or
`--no-brief`, `watchdog` takes `--check`, `--watch` and `--self-test`, `sync respawn` takes
`--after-seconds` and `--reason`. The text now says it is the one flag this check adds, and the skill
names those arguments.

The exit code the page quotes is confirmed at its source: `clientcmd.ExitUnavailable = 3`
(`internal/clientcmd/command.go:29`), which is what `PeakGate` returns on a refusal, and
`ExitUsage = 2` for a clock or setting it cannot read.

## 2026-10-10 (later) — the class-(ii) reason list, corrected against the tree

**The change the sign-off below was made subject to is done** (row `3c94814d`). The 25 rows it names are
corrected in the two room artifacts — `PORT-MAP.md` section (c), and `P6-TEST-DISPOSITION.md`, which
copies its reason column from those rows — and every anchor they cite was re-read against this tree at
`13c399c`:

- The 25 reasons now say what the Go client does instead of what the Python plumbing did: each names the
  Go cover and what it asserts, or the mechanism the port replaced. The one row the P6 verdict marked
  correct is untouched.
- Seven line anchors were still wrong and are fixed. `35a40a5` added the peak gate's seam to
  `restoreAll` and pushed every function below `lifecycle_test.go:34` down seven lines, so `:70` became
  `:77`, `:97` `:104`, `:183` `:190`, `:202` `:209` and `:242` `:249`. Two more never landed on the line
  they named: `publish_test.go:238` became `:242` (the comment above the test; it read `:237` when the
  map was written), and `internal/seatlog/seatlog.go:178` became `:173`, the `RigSeats` function, whose
  `tmux list-sessions` call is `:174` in every revision, the signed tip included.
- Verified rather than asserted: every Go test the rows cite exists in the tree (19 names, 0 missing);
  the counts are unchanged (233 Python tests, 209 class (i), 24 class (ii), 205 same-name mirrors, 2
  probable under a shorter name); and re-running `P6-TEST-DISPOSITION.py` against its declared baselines
  changed those seven rows and nothing else.
- Two of the table's own summaries said something the tree does not: "no mirror yet" counted a NAME
  miss, and one of the two rows it called the remaining parity work is mirrored by
  `TestFeedbackHelpPrintsUsageAndExitsZero` — its own reason said so — while the other is the recorded
  no-mirror reason, because Go's `RenderConfig` takes no rig at all. The table now says a row no test
  names is a prompt to look, not an uncovered row.
- `P6-TEST-DISPOSITION.py`'s default Python baseline is pinned to `1663257`: `HEAD` no longer carries the
  tree it extracts, so the generator could not run at all until the default named a revision that does.

The two artifacts are room files, not files of this repository, and the reviewer's gate files were
measured at the signed tip, where their anchors were right; neither is edited here.

## 2026-10-10 — the Python client is gone: the reviewer's sign-off, and the help surface above it

**The deletion is signed.** `1eca7de` (Python sources, `tests/`, `pyproject.toml`, `build/`) is signed by
the reviewer against the owner's four conditions: every verb is in Go and its help diffed (a 43-shape help
sweep answers every shape); every Python test has a Go mirror or a recorded reason — 201 mirrors and one
no-mirror row — subject to the correction that 25 of the class-(ii) "not ported" reasons state something
the tree does not do (work list: row `3c94814d`; artifacts `PORT-MAP.md` §(c) and
`P6-TEST-DISPOSITION.md`); a fresh archive of the tip holds no Python (`0 *.py`, `0 pyproject.toml`, no
`build/`) and `go build`, `go vet` and `go test ./...` (16/16 packages) pass in it; verdict
`GATE-3b2feacd-P6-signoff-2026-10-10.md` plus `GATE-3b2feacd-ADDENDUM-1-reason-count-corrected-2026-10-10.md`,
both in the room directory.

**The tip above it is gated too.** `8cf6a34` — a verb answers its own help before the platform matrix — is
APPROVE WITH CHANGES: the six shapes that refused now answer, the gate is moved rather than removed (the
same verbs without `--help` still exit 3, naming what the machine lacks), and 43 of 43 shapes answer. The
one change owed: `bridge install-service --help` prints the systemd unit rather than usage
(`internal/clientbridge/commands.go:48`), and the new test asserts only that stdout is non-empty, so it
cannot see that. Verdict `GATE-8cf6a34-help-before-matrix-2026-10-10.md`.

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

- The skill tree tolerates an absent or EMPTY `rooms/`, and that is pinned (`internal/clientskills/tree_test.go`): the
 tree that ships has zero paths under `skills/rooms` at HEAD, while the checkout on disk has an empty `skills/rooms/`
 directory, and git cannot carry an empty directory. The loader accepts a share-only root (`share/` is the check, not
 `rooms/`), resolves it to the shared skills alone with no error, and creates NOTHING. Three cases are covered: no
 `rooms/` at all, an empty `rooms/`, and an empty `rooms/<room>/`.

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
  Sun 07:00, Mon 05:00), the holiday file case and a holiday file that does not exist — identical in all 14 runs,
  which is how the 23:30 row was found to be missing from this file while the claim above said it was covered. TWO
  deliberate divergences: (1) a malformed `OFFPEAK_UTC` is refused by name with exit 2, where the script printed a
  half-empty JSON object and exited 0; a broken clock must not read as off-peak, because that is the direction in
  which the gate silently stops refusing, and every other blank-or-typo value in this client is refused by name too.
  (2) after a Friday's last peak, `next` names MONDAY 01:00 UTC (`01:00 UTC Monday (peak starts)`), where the script
  printed "01:00 UTC tomorrow (peak starts)" — tomorrow is Saturday, off-peak in full, so the script's line told a
  caller to schedule around a peak that does not exist, and that line exists only to be scheduled on. Both Friday
  instants (10:00 and 23:30) are pinned in `schedule_test.go`, whose other rows remain the script's bytes verbatim.
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
  briefs on disk, and `--all` walks every rig that has a brief, one rig's failure never stopping the others,
  exiting non-zero when at least one visited rig is not up.
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
  brief overriding the shared one, `--no-brief`, the abort when no seats came up, `--all` over several rigs and
  its report when one of them is not up - a failed restore and a peak refusal both counted, with the rigs after
  it still visited - the
  gate refusing BEFORE the restore at a pinned peak instant (and `--allow-peak` letting the same room through,
  and a live rig never being gated), the watchdog's two-miss rule with its counter reset, its heartbeat cadence,
  its server-identity transition lines, an unreadable `rig ps` counting as zero expected seats, and that a rig
  name that merely CONTAINS the rig's name is not one of its seats.

### Changed

- A notification the cloud refuses for a reason another attempt cannot change is no longer retried forever
  (`internal/clientbridge/notifications.go`, `notifyTerminal`). The sender recorded nothing on a non-2xx, so a
  permanently refused row was posted again on EVERY cycle - the same 4xx, once per cycle, for as long as the bridge
  ran. A 4xx that names the REQUEST (400, 404, 409, 410, 413, 415, 422 and the rest of that range) is now recorded
  as handled, with the status and the cloud's answer in the note so the drop is visible; 401/403 (a credential that
  can be fixed), 408, 429 and every 5xx stay retryable, because those can be answered differently later - a human's
  row must not be thrown away because the token was wrong for an hour.
- The one-binary question is answered for the client, in `README.md`'s install section: the seat checker stays
  a SECOND binary in this module rather than a `4genteam` subcommand. `install-checker` compiles
  `cmd/seatcheck` with `-ldflags -X main.installedPinsDir=<store>`, so each installed checker carries the pins
  directory of the store it was built for - a per-install build input the operator's `4genteam` must not carry
  - while the guard keeps ONE source (this `cmd/seatcheck`, the copy `install-checker` builds; the server's
  copy was removed in the server repo and does not come back). The name is fixed too: the seat's rendered
  settings allow exactly `Bash(seatcheck send:*)`, so `seatcheck` must resolve on the seat's `PATH` to the
  binary this client built, and `pull` refuses otherwise. Older notes that name `agenthub_go/cmd/agenthubclient`
  as the single binary are stale: that binary is not the one that ships.
- `continue --all` reports what happened instead of ending 0 whatever did (`internal/clientrestore/continue.go`).
  Every rig that has a brief is still visited after one of them fails - the shell's `|| true` inside the loop,
  kept, because a room that is not on DeepSeek must be restored while a refused or broken one is not - but the
  run now ends non-zero (`ExitRestoreFailed`, 1) when at least one visited rig is not up, whether its restore
  failed or the peak gate refused it, and logs `--all: N of M rigs are not up: <rigs>`. The shell ended 0
  however many rigs failed, so a caller reading only the exit code could not tell a run that resumed every room
  from one that resumed none - the invisibility `watchdog --check` was fixed for, answered here the same way
  (`watchdog --all --check`). A refusal is deliberate rather than a failure, but it is still a room this run
  left stopped, so it counts; `--allow-peak` is how a caller asks for the peak-hour restore.
- The watchdog's restore line names the truth (`internal/clientrestore/watchdog.go`): `Restoring via continue's
  restore.` The ported watchdog calls `continue`'s own restore function, not the script, and the line named
  `rig-continue.sh` - a script this binary never invokes and never ships. Its stamp and shape are unchanged, so
  the log still reads as one file with the script's, and the pinned line in `watchdog_test.go` moved with it.
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

- The human notification is no longer the client's ONE unredacted egress (`internal/clientbridge/notifications.go`).
  `notifyText` copied the row's summary (or its first non-empty body line) into `POST /api/v2/broadcast/notify`
  verbatim, while the report's sibling path redacts its seat detail with the same map (`payload.go`,
  `secretscan.Redact`), and the receiving route does not scan either - so a human-addressed row quoting a known value
  was copied from the LOCAL daemon queue into the cloud's notification store raw. The text is now redacted with the
  bridge's own `secretscan.KnownValuesFromEnv` map BEFORE it is bounded, so the marker that replaced a long value is
  never the thing the bound cuts, and `Redact` fails closed (an empty text), which posts nothing rather than posting
  raw text. FOUR cases pin it: the posted message carries `[REDACTED:DEMO_TOKEN]` and not the value while the
  rest of the sentence survives, a 422 is recorded as handled with the status in the note, the classification
  table (400/404/409/410/413/415/422 final; 401/403/408/429/5xx retryable), and - because the other three build
  their OWN bridge, so they would all stay green if the call site handed it no map - a case that drives the real
  verb (`bridge once --print`, a stub `rig` on PATH and the value in the environment) and fails the moment
  `commands.go` passes its `known` map as nil. MEASURED: with that call site mutated to nil,
  `TestBridgeOncePrintRedactsThroughTheEnvironment` fails on "the printed detail carries the environment's secret
  value" while `TestNotifyRedactsAKnownSecretBeforeItLeavesTheMachine` still passes. The record's own count is
  corrected in the same commit: `fa486d9`'s "four" tests fail with the sweep call removed, MEASURED - it is FIVE
  (`TestNotifyPostsOneFrameForAHumanRow`, `TestNotifyRunsEvenWhenTheCycleSkipsItsReport`,
  `TestNotifyDoesNotPostTheSameRowTwice`, `TestNotifyStateSurvivesARestart`, `TestNotifyRetriesARefusedPost`).
- `seatcheck --help` answers usage on STDOUT and exits 0, at every level (`cmd/seatcheck/main.go`). The bare forms
  answered `unknown subcommand` with no usage, and `send --help` / `audit-scan --help` printed the usage on STDERR -
  all with exit 2, the code this command documents for a usage or policy refusal, so a help request was
  indistinguishable from a caller's mistake (and `-h` is the flag package's own trigger, which is how the usage was
  unreachable that way). `run()` gained the help case ahead of `default:`, and both flag sets answer through one
  `helpOrUsageError`: `flag.ErrHelp` goes to stdout with 0, every other parse failure keeps stderr with 2 - the
  `errors.Is` gate is what keeps `TestSendUsageErrorsExit2` and `TestSendHasNoPinsFlag` meaningful. Nothing on PATH
  was touched, and `sync install-checker` was not run: the link swap is the owner's call.
- Bare `4genteam continue` printed `Usage: usage: 4genteam continue ...`: the listing ran `continueUsage`
  through its own `Usage: %s` while that constant already opens with `usage: `. The listing now prints the
  shell's own line once, from a constant of its own (`continueListingUsage`, the shape
  `watchdogListingUsage` already used), and the listing's test asserts the line and that the doubled prefix
  is gone - the old assertion pinned the doubled string by building it the same way the code did.
- `bridge install-service --help` answers its usage instead of printing the systemd unit. The unit is this
  sub-verb's ANSWER, so `--help` got the unit, exit 0 and no stderr - and the help sweep added with
  `8cf6a34` asked only that stdout be non-empty, which is exactly why it passed. Help now comes first, as it
  already did inside `bridge run` and `bridge once`; without `--help` the unit still prints, so the gate
  moved rather than loosened. The sweep now asserts that each shape prints a USAGE (`usage:` or the bridge
  group's header) and, for this one, that the unit is absent.
- `team publish-skills` refuses an inventory that carries no `generated_from` at all, naming the file it
  read (`internal/clientteam/publish.go`). The pin guard read an absent pin as "nothing to check" and fell
  through to the digest check, so an inventory whose provenance nobody can name published silently - the
  one case in which the guard cannot exist, and worse than a stale one, because nothing downstream can
  detect the divergence. Census first: every `*.json` under this project and under the OpenRig tree was
  scanned for the inventory shape (a top-level `skills` array whose rows carry `canonical`/`plugin`) - 2,749
  files, four matches, and all four are the same committed record `ai_docs/agent-system/skill-library.json`
  (52 skills, `generated_from` naming `31fe301b`), so no real inventory omits the pin. Red first: with the
  refusal forced off, the fixture's unpinned inventory was published - `publishing 3 skill block(s)`,
  `module alpha-skill@1.0.0: applied`, `publish summary: 3 pushed, 0 new version(s), 0 skipped`, exit 0 -
  and the new case was the only failure in the package (41 passed, 1 failed, 0 skipped). The library
  fixture is now the inventory GENERATOR's output: committed, with `generated_from` naming that revision,
  and a case that moves bytes and digests regenerates the inventory the same way. `go vet` clean;
  `go test ./internal/clientteam/` 42 passed, 0 failed, 0 skipped; the committed inventory still passes
  against a checkout AT `31fe301b` (exit 0, `plan: publish-skills 52 skill block(s)`).
- Two source comments in `internal/clientbridge` no longer name the owner's own rigs. They recorded a live
  store's destinations as `human@kernel` and `human@4genthub-min`, and this repository is public: the
  measurement survives as what it measured - the daemon addresses one `human@<rig name>` destination per rig,
  each naming the rig it belongs to - with no rig named. Comments only; no behaviour and no test changed.
- `policy apply` writes only the seats whose runtime it serves (`internal/clientpolicy`): a seat of another
  runtime is refused BY NAME, and the whole set is decided before the first write. The document the verb
  renders is an omp config and the state root it resolves defaults to `~/.openrig/state/omp`, so on a mixed
  room the old loop wrote the omp seats and then stopped at the other runtime's seat with `no agent directory
  … (never launched?)` - a seat that HAD been launched, under its own runtime's root - leaving a partial
  application, a false reason, and nothing saying what had been written. Measured on a scratch room before the
  fix: the omp seat's `config.yml` on disk with exit 2; and with that seat's directory present under the same
  root, an omp config written INTO a claude-code seat and exit 0, which is worse than the abort. After: both
  cases print `<seat>: runtime claude-code is not omp …` and `4genteam policy apply: nothing was written`,
  exit 2, zero files; an all-omp room still applies every seat (exit 0); `--check` refuses identically, since
  an unserved seat is not a drift question. A team file that names no runtime is still served: that is the
  shape the Python's own role table had, and the state root is the omp one.
- `team publish-skills` no longer publishes an inventory whose `generated_from` names a revision it does
  not describe (`internal/clientteam/publish.go`). The recorded digests were verified only against the
  checkout `--source-root` points at, so an inventory regenerated from a MOVED checkout kept its old pin
  and the verb published bytes the pin does not describe, silently. Reproduced on this pod: with the
  committed inventory's digests recomputed from the checkout (23 of the 52 skills had to change to be
  accepted), `team publish-skills --dry-run --inventory … --source-root <the moved checkout>` planned all
  `52 skill block(s)` and exited 0 while `generated_from` still named `31fe301b`. The verb now reads the
  named revision back out of git itself (`git archive`, unpacked in memory through `archive/tar`, never
  through an external `tar` binary so a missing binary cannot read as a pass), digests the same paths
  there and refuses on a mismatch, naming the skill, the path, the digest the revision holds, the digest
  the inventory records and the revision. The committed inventory still passes against a checkout AT
  `31fe301b` (`exit 0`, `plan: publish-skills 52 skill block(s)`), and the pod's moved checkout is
  refused by the pre-existing staleness check, so the pin is an added claim rather than a replacement
  for that one. A pin that cannot be read back - no git repository, an unknown revision - is refused
  rather than trusted; an inventory carrying no `generated_from` at all still fell through to the digest
  check in this commit, and the entry above closes that.
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
