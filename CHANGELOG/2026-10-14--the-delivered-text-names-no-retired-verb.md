# The delivered text names no retired verb

C3, third tranche: the *class* of defect the sweep closed one line at a time — text this client delivers
telling a reader to run the orchestrator the cutover removes. C3's own rule is "a printed line RUNS, or it is
ABSENT"; nothing enforced it, so the next edit was free to write one back. This is the pin, and it covers the
class rather than one verb (the reviewer's gate finding 1, routed by the lead as a scope decision).

## What was wrong

Two things, and the second is the reason a pin was needed rather than another sweep.

1. Two surviving mentions in `docs/WATCH.md` were **instructions, not contrasts** — the worse half of the
   class, because they read as procedure: the input pane said a line "is sent to the seat with `rig send`",
   and the evidence list printed `rig ps --nodes --rig <RIG>  # one row per seat`. Measured on this box `rig`
   resolves to the OpenRig CLI 0.6.10 whose daemon is not running, so following either line lands on
   `daemon unreachable … Resolved: partial via session name`. Repointed, not reworded around.

2. The remaining mentions are legitimate — a contrast that draws the distinction the skill exists to teach, or
   a prohibition that names what NOT to run — and prose is exactly what a grep cannot classify. A scan that
   guessed would either fail on `4genteam` text that merely mentions "the predecessor's `rig ps` shape", or
   pass by skipping the file (`internal/clientpolicy/policy.go` holds the parked `rig down*` guardrails; a
   path-skip there is the defect this design refuses).

## The change

A new package, `internal/retiredverbs`, that reads the delivered text and fails on a command span naming a
retired program unless an allowlist entry carries it **with a reason**.

- **The class is data.** `RetiredCommands` lists eleven commands — `rig` with `up`, `ps`, `send`, `create`,
  `launch`, `grow`, `capture`, `daemon`, `policy list`; the bare `pi-runner.js`; the `openrig daemon` — each
  line reviewable, each carrying the one-line *why* the failure message quotes (`the predecessor
  orchestrator's delivery`).
- **The delivered text is three surfaces.** Go string literals in non-test files (what the client PRINTS;
  parsed, not grepped, so comments are skipped by construction), and every file under `docs/` and `skills/`
  (what is handed to a seat or read by an operator). `CHANGELOG/` is skipped: an entry is the record of what
  was, so its text is quotation, and the sweeps that closed C3 are chronicled there.
- **A span is a command only in command position.** A backticked span whose first word is a retired program
  (`rig ps --json`), or the start of a line after an optional `$ ` prompt. Prose stays prose: `the rig up the
  tree was stale` is not a span, and neither is `cannot read the seat identity (no runtime, no rig daemon …)`.
- **The allowlist names the mention, not the file.** `{File, Program, Subverb, Match, Reason}` — `Match` must
  appear in the text the span was found in, so a reworded line stops being covered and has to be read again.
  One entry is a statement about a sentence, never a licence for a path.
- **The pin's own data is written as two fields, not one joined string.** This package is a delivered Go file,
  so a literal reading `rig ps` in code position would be a span by this package's own rules and would have to
  be allowlisted; `{Program: "rig", Subverb: "ps"}` is data *about* a command, the joined form is the command.
  The one exception is a program that IS the whole command (`pi-runner.js`), and it carries an entry saying so.

## Evidence

- **GREEN at the tip, with every survivor named.** `go test ./internal/retiredverbs/ -v`:
  `8 command spans name a retired program; 8 of them are allowlisted with a reason` — five in
  `skills/client/{rig-runtime-switch,spawn-team}/SKILL.md` (one contrast, four prohibitions/negations) and
  three in this package's own table and allowlist. `0` unexpected.
- **RED when a line is restored, on both surfaces.** The falsification, run as one edit-and-revert:
  `docs/WATCH.md` put back to `rig send`, and a non-test Go literal reading
  "Resume the seat: run `rig up <spec>.yaml` to bring the room back." added to the package.
  The pin reported both, by name —
  `docs/WATCH.md:84: rig send (the predecessor orchestrator's delivery)` and
  `internal/retiredverbs/zzfalsify.go:4: rig up (the predecessor orchestrator's up)` — with
  `2 command span(s) name a retired program and no allowlist entry carries them` and `FAIL`, each failure
  quoting the whole line it found under the span. The scratch file and the docs line were then reverted; the
  run above is the reverted tree.
- **The allowlist cannot rot into a licence.** `TestEveryAllowlistEntryStillCoversASpan` requires each entry to
  match a span that still exists and to carry both a `Match` fragment and a `Reason`; an entry left over from a
  reworded line fails by name and says to read the line again.
- **The scanner tells a command from prose**: `TestTheScannerTellsACommandFromProse`, plus the two corpus
  scans above, are the whole test file.
- **Nothing else moved**: `gofmt -l` on the touched tree prints nothing, `go vet ./...` exits 0, and
  `go test ./...` is **26 packages ok, 0 FAIL** (the suite was 25 before the new package).
- Not verified: the copies the live seats already hold. The skills reach a seat through `4genteam sync rig`
  from the installed build — the lead's call, not this branch's.

## Left as it is

- **Comments are not scanned**, deliberately: this pod's comments are the record of its own removals ("THE CLI
  REACH IS RETIRED RATHER THAN WRAPPED"), so scanning them would fill the allowlist with the history of the
  fix. What is *delivered* is what this pins; a comment is not delivered to anyone.
- **The scanner does not judge intent.** It cannot, and a heuristic that tried would be the path-skip this
  design exists to avoid. Every judgement lives in the allowlist, in words, one entry per sentence.
- **Text outside `docs/`, `skills/` and printed literals** (the runtime crate's own `--help` text, the root
  `README.md`) is not covered. The runtime's usage strings are a second surface with its own reader; naming one
  as covered while not scanning it would make this green mean less than it says.
