# The delivered skills name verbs this client has

C3, second tranche: printed references a reader would ACT ON, in the four text trees that reach a seat or the
operator — `skills/share/**` and `skills/rooms/**` (delivered to seats by `4genteam sync rig`), `skills/client/**`
(for the LLM driving the client) and `docs/AGENT-SETUP.md`. 53 `rig <verb>` spellings in 8 files.

## What was wrong

`skills/share/development-team/SKILL.md:14` told every seat reading it to run `rig whoami --json`. Measured on
this box, `rig` resolves to the OpenRig CLI 0.6.10, and that command answers `daemon unreachable … Resolved:
partial via session name` — a partial identity from a program whose daemon is not running. The same class ran
through the other trees: `rig send` as the message route, `rig ps --nodes --rig R --json` as the liveness read,
`rig policy list` for the permission postures, `rig capture` / `rig terminal open` for a pane, `rig usage series
--lane provider_window` for a provider window, `rig daemon status|start` for the daemon, and the spec/node verbs
(`rig up <spec>.yaml`, `rig down --delete`, `rig create`, `rig grow|add|shrink|remove`, `rig launch`, `rig
remove`, `rig parked`, `rig provider signals`). One more, in the same family: `4genteam watch watch --rig <R>`
passed a positional the watch verb does not take.

The C3 rule is unchanged: a printed line RUNS, or it is ABSENT. Two of these were also **dangerous** if followed,
not merely stale: the two runtime-switch skills printed the predecessor's teardown-then-relaunch sequence, and
measured, its middle line stops the MACHINE's safeguards (`rigd` reaped with its children, for every room) while
its last cannot run at all (`--team DIR is required`).

## The change

Repointed to the verb this client has, every one verified with `--help` on a binary built from this branch, and
where **no** native verb exists the text now says so instead of prescribing a dead program:

- identity and reads: `4genteam whoami [--json] [--session NAME]`, `4genteam ps <RIG> [--nodes|--json]`,
  `4genteam capture <session> [--lines N]`, `4genteam status <RIG>`, `4genteam doctor`, `4genteam room status`.
- messages: `4genteam send` (the backtick warning in three files, the input-pane route, and the relay section,
  which now also states that this verb REFUSES `--dangerously-interact` / `--reason` outright, so the staging path
  it described no longer exists).
- lifecycle: `4genteam up <RIG> --team DIR` (`--team` is required, measured: *"--team DIR is required - the
  room's seats are read from the team definition it holds, and there is no default"*), `4genteam continue`,
  `4genteam seat reseat`, `4genteam sync rig --update`, `4genteam sync switch`, `4genteam policy show`,
  `4genteam watch --rig R`.
- the two runtime-switch skills: their rig-CLI spine is replaced by the client's OWN pinned statement —
  `4genteam sync switch` prints that it cannot switch a runtime in place and names the two steps that exist
  (`sync rig --update`, `seat reseat`) with *"whether a restart carries a changed RUNTIME is not established"*.
  The teardown/relaunch sequence is deliberately not printed, with the two measured reasons above.
- stated absences, so a reader is not sent nowhere: no verb for the provider window (`4genteam offpeak` is the
  DeepSeek read), none for in-place room resizing, none that opens raw `tmux attach` tiles per seat, none for
  `parked`, and none that creates a room from a spec path (`team.json` + `4genteam up --team DIR` is the route).
- `docs/AGENT-SETUP.md`: the failure map's two daemon rows, the materialize block, the policy paragraph, the
  `can_observe` example, the worked example's ordering, and the seat's startup line (which also dropped "pod",
  the field `4genteam whoami` does not print).

## Evidence

- Sweep instrument: `grep -rnE '(^|[^A-Za-z0-9_./@-])rig [a-z]+'` over the four trees returned 53 hits before and
  **16** after, each of the 16 classified as NOT a prescription: `<rig>` / `<rig install>` placeholders, prose
  where "rig" means the room or the team, or an explicitly-labelled statement of what the predecessor had and
  this client does not ("the predecessor CLI's `provider signals`", "this client has no `rig create`", "never the
  predecessor's `rig up <rig> --existing`"). **0 spellings a reader would run.**
- Verb existence, each `--help` exit 0 on the built binary: `whoami`, `ps`, `send`, `capture`, `policy show`,
  `up`, `continue`, `room status|stop|reseat`, `seat reseat`, `sync rig`, `sync switch`, `sync respawn`,
  `doctor`, `status`, `watch`, `offpeak`.
- Markdown fences balance in all 8 files (even counts), and the suite is untouched by the edit: `go build ./...`
  0, `go vet ./...` 0, `go test ./...` 25 packages ok, 0 FAIL.
- Not verified: the delivered copies the live seats hold. Re-delivery is `4genteam sync rig` from the installed
  build, which is the lead's call, not this branch's.

## Left as it is

- `skills/share/development-team/SKILL.md:17`'s `(installed: <rig install>/reference/product-journey-sdlc.md…)` —
  a path root, not a verb, and the file it names exists nowhere on this box (searched). Repointing it would be
  inventing a path.
- `skills/client/install/SKILL.md:96` still says the launch directory has a linked `.env`; the native room layout
  has no room-level `.env` (measured: none under `~/.config/4genthub/rooms/4genthub-min`). A stale-PATH item, not
  a verb — reported, not rewritten.
- `skills/client/install/SKILL.md:151`'s symptom string `Cannot determine identity. Run inside an managed seat
  session` appears nowhere in this tree's Go source; the row's VERB is repointed, the quoted symptom is not this
  client's text.
- `docs/AGENT-SETUP.md:1`'s title still says "the rig orchestrator" — a product name, not a spelling a reader runs.
- Code-side naming, out of this unit: `internal/clientbridge/commands.go:260` prints an `openrig-bridge:` log
  prefix, and `clientfeedback` offers `--layer openrig`. Neither is a procedure.
- `internal/clientpolicy/policy.go:71-73` / `:185` (`rig down*`, `rig up*`, `rig remove*`, `rig whoami*`) — the
  parked guardrail unit, waiting on the architect's C5 ruling and sharing a file with owner item 16.
