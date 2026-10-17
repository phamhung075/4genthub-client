# The queue row body is four fixed lines: facts, rule, decision, next

Decision: every `queue create` writes a body of exactly four lines - `facts:`, `rule:`, `decision:`,
`next:`, in that order - and the per-field caps sum to the existing 600-char body cap; 4/8 enforces it at
create in `rust/agenthub-runtime/src/queue/mod.rs` and `internal/clientruntime/queue.go`.
Date: 2026-10-17 13:52Z. Seat: architect. Task: afe7f042 (Thinking 3/8). Enforced by: cd47f19c (4/8).

## Context (measured, not assumed)

- The daemon already types a row: `rust/agenthub-runtime/src/queue/mod.rs` holds `SUMMARY_MAX_CHARS = 120`,
  `BODY_MAX_CHARS = 600`, `PRIORITIES = low|normal|high`, `high` requires tag `prod-break`, and
  `ALLOWED_TAGS` = prod-break, gate, ruling, owner-decision, finding, guide, docs, defect, skill-check,
  landing, assignment, handoff, board, reseat (14). `status`, `ack`, `checkpoint` and `report` were retired
  on 2026-10-17; a create naming one is refused by name, and rows written before the retirement keep theirs.
- Nothing validates the body's SHAPE today: `validate_typed_fields` checks the caps, the tag vocabulary and
  the `high`-implies-`prod-break` rule only. Bodies on the live rail are free prose of 250-600 chars, so a
  destination must read the whole body to find the one thing it acts on.
- The body is create-only: `queue update` takes `--note`, `--summary`, `--state`, not `--body`. So the
  schema needs no migration and no update path; the rule governs what may be ADDED.
- The act words the rail already uses in summaries (GATE PASS, ASSIGN, DEFECT, RULING, OWNER, ANSWER,
  DECISION) are conventions, not validation. 4/8 does not touch summaries beyond the 120 cap.
- 2/8 owns the fact vocabulary (`4genteam facts --json`: git, queue, CI, offpeak, pin). 3/8 owns the
  container and its limits only, so the two cannot disagree: `facts:` values are verb field paths verbatim.

## The schema

### Body grammar (create only)

```
facts: <v1>
rule: <v2>
decision: <v3>
next: <v4>
```

- Exactly four lines, separated by `\n`, in that order. One occurrence of each key, lowercase ASCII.
- A key is the literal text `facts`, `rule`, `decision`, `next` followed by `: ` (colon, one space).
- A value is a single line (no `\n` inside), at least 1 char, at most its field cap. Char counting is
  `chars().count()` / `utf8.RuneCountInString`, as the existing 120/600 caps already count.
- The whole body is at most 600 chars, as today.

### Field caps

| Field | Prefix | Max value chars | What it carries |
|---|---|---|---|
| `facts` | `facts: ` (7) | 280 | The measured state, as `key=value` pairs joined by `; `, keys being `4genteam facts` field paths or a named source (`file=`, `commit=`, `ci.run=`). No re-derivation by bash chains. |
| `rule` | `rule: ` (6) | 110 | The policy, owner order or gate check that governs this row - named, not paraphrased away. |
| `decision` | `decision: ` (10) | 100 | What was decided, in one sentence. `none - <why>` only for a finding that needs no decision. |
| `next` | `next: ` (6) | 78 | The one action the destination takes, and who takes it. |

Prefixes + newlines cost 32 chars; the four caps sum to 568; 32 + 568 = 600 exactly, so a body at every
per-field cap is exactly the body cap - no legitimate row is refused, and no row can exceed 600 while
respecting its fields.

### Which fields each tag requires

All four fields are REQUIRED for every allowed tag. The tag changes what the fields SAY, never which
exist - one rule for 14 tags, and the one rule 4/8 checks. Each create additionally carries at least one
allowed tag; `high` still requires `prod-break` (existing rule, unchanged).

| Tag | Summary act word | `facts` says | `rule` says | `decision` says | `next` says |
|---|---|---|---|---|---|
| prod-break | BREAK | what broke, with the failing sha/run and the observed error | the owner rule breached | the stop/park taken | who restarts what |
| gate | GATE | task, sha, base, test run, numstat | the checks D3/D4/D5 and their verdicts | pass/fail | land or hold, and by whom |
| ruling | RULING | the decision inputs (ids, measured values) | which order/policy governs | the ruling | who applies it |
| owner-decision | OWNER | the options with their costs | why only the owner may choose | the question put | the owner's act, and what stays parked |
| finding | FINDING | the observation with its evidence | the expectation it violates | accept / accept as defect | file or watch, and by whom |
| guide | GUIDE | the steps and their source | the convention it encodes | the recommended path | the reader's first step |
| docs | DOCS | the files and the values that changed | the doc/changelog convention | what the doc now states | who reads or links it |
| defect | DEFECT | repro, tool/route, evidence | the contract broken | severity and owner | the fix task and its owner |
| skill-check | SKILL | seat, skill, version, install path | the skill-tree resolution rule | pass/fail | install or reseat, or nothing |
| landing | LANDING | commit, files, base, hook rewrites | the pathspec/keep-out rules | landed | verification and carrier |
| assignment | ASSIGN | task id, acceptance, paths | the ownership rule | assigned to whom | the taker's first act |
| handoff | HANDOFF | what is done, what is left, the evidence file | the seat contract boundary | handed over now | the taker's first act |
| board | BOARD | task id, criteria, record location | the board/detail conventions | what the record now settles | who closes what |
| reseat | RESEAT | seat, runtime, model, build/commit | the build-then-reseat rule | reseat now / hold | what the lead verifies after it |

### Summary (unchanged cap, ruled shape)

At most 120 chars. It begins with the tag's act word from the table above (`GATE PASS task ...`,
`ASSIGN task ...`), then the object, then the one thing the destination must know. This is a convention
the daemon does not validate; only the 120 cap is enforced, as today.

### Refusals 4/8 adds (each names the rule and the field)

- `body schema: field 'rule' is missing` - one line per missing field, fields listed in schema order.
- `body schema: unexpected line 2 ('...'); the body is facts, rule, decision, next, in that order`
- `body schema: field 'facts' appears twice`
- `body schema: field 'decision' is empty`
- `body field 'next' cap: 79 chars exceeds the 78-char cap` (then the whole-body 600 check as today)
- `tag rule: a create carries at least one allowed tag` (new; empty tag list is refused)

Old rows are never re-read against the schema: it runs on create only, so the rail keeps its history
verbatim. There is no compat path and no free-form body accepted.

## Options weighed

- A: free prose until 600 chars, with a SHOULD-conform skill line. Costs nothing in code, but 4/8 cannot
  refuse anything, the destination still reads the whole body, and 2/8's verb output is re-typed by hand.
  Rejected: it leaves the behaviour the order names unchanged.
- B: JSON body (`{"facts":{...},"rule":...}`) parsed by both languages. Strict and lossless, but costs
  ~20 chars of punctuation out of 600, hides the fields from `queue show`'s plain render, and the rail's
  readers are humans in `watch` and `show`. Rejected on readability at this cap.
- C: four fixed lines with per-field caps that sum to the cap (chosen). One parser shape in both
  languages (split, 4 lines, prefix, cap), legible in `show`, and the facts line is 2/8's field paths
  verbatim so the two rulings cannot drift. Costs: a value cannot wrap, so a long fact list must pick the
  facts the destination acts on - which is the point.

Recommendation: C. What would change it: if a destination ever needs more than ~280 chars of facts, the
right move is more verbs (2/8's remit), never a longer body.

## Worked examples (one per allowed tag; summary then body)

```
tag: prod-break (priority high)
summary: BREAK: hub main's /mcp mount 502s after 08:40Z; seats lose all nine tools
facts: ci.run=39015693990 red; hub.main=dd3edf62; mount=/mcp; http=502 since 08:40:12Z
rule: owner rule 5: no prod touch by a seat; only main carries the fix
decision: stop the deploy lane; the hub rollout stays parked
next: lead asks owner to restart the hub router; nothing lands till then
```

```
tag: gate
summary: GATE PASS task b6a8d202: /docs tests 37 green, tsc 0
facts: sha=8e706e18; base=d902b7e0; tests=37 passed; tsc=0; numstat=12 files +1014 -9
rule: gate D3 test run, D4 keep-out paths, D5 diff size; all green
decision: pass; the commit stands as landed
next: go-dev carries 4/8 on this base; no edit to the gated paths
```

```
tag: ruling
summary: RULING: row body is facts/rule/decision/next; 600 cap unchanged, 4/8 enforces
facts: tags=14 allowed; caps=summary 120, body 600; status/ack/checkpoint/report retired 10-17
rule: architect owns the schema; go-dev owns enforcement at create
decision: four fixed lines, order pinned, per-field caps 280/110/100/78
next: go-dev implements 4/8 from seats/architect/RULING-queue-row-schema.md
```

```
tag: owner-decision
summary: OWNER: may fe-dev run one-shot 'sync connector' - it posts redacted bytes to prod
facts: verb=sync connector; payload=redacted transcript bytes; target=prod; runs=1
rule: privacy rule: no transcript bytes leave the machine without the owner
decision: ask the owner; a seat cannot waive the privacy rule
next: owner answers yes or no; fe-dev waits, no run
```

```
tag: finding
summary: FINDING: /docs/api-reference shows tool descriptions as raw markdown pipes
facts: route=/docs/api-reference; symptom=pipes and fences in one paragraph; component=ApiReferenceView
rule: docs render markdown through the same sanitizer on every page
decision: accept as a defect; not a doc edit
next: lead files the fix; no edit to the doc text
```

```
tag: guide
summary: GUIDE: how to read a row - facts are verb output, rule is policy, next is your act
facts: fields=facts, rule, decision, next; caps=body 600, summary 120; verbs=list, show, create, update, claim, close
rule: a seat acts on next; facts are not re-derived by a bash chain
decision: follow the four fields in order
next: open the row's next line; if it names you, claim the row
```

```
tag: docs
summary: DOCS: self-compaction limits now name the COMPACT_* vars and this team's values
facts: file=ai_docs/public-docs/03-production-performance/02-self-compaction.md; values=150k/200k/250k
rule: a public doc states what the running client does, and names its source
decision: the table names each variable and its unset default
next: fe-dev rechecks links after the build; no reseat needed
```

```
tag: defect
summary: DEFECT: manage_task list renders an id its own get/update reject - 5ac4a259 unrecordable
facts: tool=manage_task; action=list; returned=5ac4a259; get=refuses the same id
rule: a tool's list output must be usable by its own verbs
decision: defect accepted; it belongs to the hub tool surface
next: lead opens the hub task; hub code is not in this lane
```

```
tag: skill-check
summary: SKILL PASS: architect-role 1.4.12 installs under the seat, no client/ read
facts: seat=architect; skill=architect-role; version=1.4.12; path=seats/architect/skills
rule: the tree resolves share, then room, then seat; client/ is for the client LLM
decision: pass; the installed copy matches the hub seed
next: no reseat; a changed tree reaches a seat at its next start
```

```
tag: landing
summary: LANDING: TEST-CHANGELOG carries the /docs entry; pathspec commit, no stage
facts: commit=8e706e18; files=TEST-CHANGELOG.md; base=d902b7e0; hook=0 rewrites
rule: commit by pathspec; a new file gets add -N; never add -A
decision: landed; the file carried no other seat's lines
next: lead reads the numstat; no follow-on carry
```

```
tag: assignment
summary: ASSIGN task cd47f19c: enforce the row schema at create in Rust and Go
facts: task=cd47f19c; schema=queue-row-schema; paths=queue/mod.rs, clientruntime/queue.go
rule: one owner per rule; 3/8 rules the schema, 4/8 enforces it
decision: assigned to go-dev; the seat-guard change stays a parked patch
next: read the ruling; write the red tests first, then the parse
```

```
tag: handoff
summary: HANDOFF to go-dev: 3/8 schema is ruled; 4/8 starts from the ruling file
facts: from=architect; done=3/8 ruling; left=4/8 enforcement; file=seats/architect/RULING-queue-row-schema.md
rule: a handoff names what is done, what is left, and the evidence
decision: hand over now; no wait for a build
next: go-dev claims cd47f19c and returns the green runs
```

```
tag: board
summary: BOARD: 3/8 records the ruling in details; the four criteria settle from the file
facts: task=afe7f042; criteria=4; file=seats/architect/RULING-queue-row-schema.md
rule: the board item is the record; details appends one section per call
decision: criteria settle from the ruling file and the 37-test run
next: lead closes 3/8 after reading the file; 4/8 unblocks
```

```
tag: reseat
summary: RESEAT fe-dev: omp seat restarted on the built /docs bundle
facts: seat=fe-dev; runtime=omp; model=deepseek-flash; build=after 8e706e18
rule: a client change is live only after a build and a reseat
decision: reseat now; no code change rides it
next: lead checks the seat lists the nine tools after the restart
```

Every example is measured by `queue-schema-check.py` beside this file: 14 bodies, each <= 600 chars and
inside its per-field caps, every summary <= 120 chars, fields present once each in order.

## Interfaces

- `queue create` (CLI and daemon): input gains nothing; `--body` is now validated as the four lines. A
  refusal is one line naming the rule and the field, exit 2, nothing written (the existing typed-row path).
- `queue show` / `list`: unchanged; `show` renders the body as stored, so the four lines read as four lines.
- 4/8's parser: one function per language, `split('\n')`, exactly 4 lines, prefix match, value length,
  then the existing 600 and 120 checks. No new dependency, no second validation path.
- 2/8's verb: owns the `facts` key vocabulary; 3/8 only fixes `key=value; key=value` and the 280 cap.

## Risks

- A fact that needs >280 chars: the row must pick the facts the destination acts on; the remedy is a new
  verb field in 2/8, not a longer body. Detect: a row whose `next` references a fact it does not carry.
- A tag used for two kinds (e.g. `finding` for a defect): the act word keeps the summary legible, and the
  `decision` line names what changed; detect by a `finding` row whose `next` opens a fix task.
- Existing rows do not match: accepted and left as written (create-only rule). Detect: none needed - the
  rail's history is not a queue input.
- Per-field caps enforced where a value is long Klingon-free prose: the caps are chars, not bytes, matching
  the existing counter, so a non-ASCII value is measured the same way.

## Handoff

go-dev, task cd47f19c (4/8): add the parse and the six refusals above to
`rust/agenthub-runtime/src/queue/mod.rs` and `internal/clientruntime/queue.go`, red tests first (Rust and
Go), no compat path for the old free-form body; the seat-guard change stays a parked patch. When green,
return the run output; the lead closes 4/8.
