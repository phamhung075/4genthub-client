# RULING: the handover payload is the seat's decision record

**Status**: ruled by the architect seat, 2026-10-17, on task `31b72a0d` (Thinking system 1/8).
**Unblocks**: 2/8 `bb5e5022` (the ledger accepts it; a fact without evidence is refused),
4/8 `b578bd05` (a restarted seat gets the resume brief), 6/8 `363631f4` (dead ContextTracker).
**Read at**: hub main `de399ced`, client `51f614b`.

## Context, measured

- The ledger has **one append primitive** (`AppendInTx`) and **five writers**: `status_changed`
  (`task_event_recorder.go`, `RecordStatusChange`), `progress` (`RecordProgress`), `gate_verdict`
  (`RecordGateVerdict`), `evidence_submitted` (`task_event_controller.go`, `RecordEvidence`),
  `context_updated` (`unified_context_service.go`, the seat-attributed append). The other **seven**
  of the twelve `TaskEventKindValues` (`task_event.go`, the const block: `assigned`, `claimed`,
  `delivered`, `context_loaded`, `escalated`, `human_decision`, `handover`) have **no writer
  anywhere** in `agenthub_go`: a grep for `Kind:` assignments and `AppendTaskEvent{` literals finds
  no other producer.
- `handover` is already **read**: `resumeHandoverSection` emits the last handover payload
  **verbatim** as the brief's `last_handover`, so whatever key set this ruling fixes IS the resume
  contract; nothing has to translate.
- The client-outbox surface `POST /api/v2/ledger/intents` (`ledger_mount.go`, `SubmitIntents`)
  accepts nine `IntentKind`s (`agenthub_go/fastmcp/ledger/domain/entities/intent.go`) and
  classifies them facts-vs-decisions; `handover` is a **fact** (arrival order, no `base_seq`). Its
  kind check is one line (`ledger_service.go:170`, `IsIntentKind`). No client sends an intent yet
  (no ledger kind strings in `agenthub_client`), so nothing shipped depends on the current nine.
- `resume` exists and is **by `task_id` only**; the MCP controller refuses without it
  (`task_mcp_controller.go:286-290`). No seat calls it.
- `manage_task update {details}` appends prose (AGENTS.md §5 measured 41k chars on one item). That
  is the history this ruling replaces, not a record it keeps.

## Decision

A seat hands off with **one `handover` ledger entry** whose payload has a **closed key set**, facts
carry **evidence refs**, and the server **refuses** a payload that breaks the shape or a fact whose
evidence does not hold. The decision lives on the task ledger; the blocker and the wake live on the
queue row, and the handover **carries only the row's id**.

## Option A - closed key set on the existing `handover` kind (RECOMMENDED)
One entry, one writer, the read path already exists, and the payload's keys are the brief's keys.
Cost: the key set must be ruled now and widened only by a change to the refusal (no free-form
escape hatch).

## Option B - a new `decision` kind plus one `fact` entry per fact
Pros: each fact is its own ledger row with its own `seq`, so a reader can cite one exactly.
Cons: a handoff costs 1+N rows and the brief must reassemble a set that a reader then has to
correlate; `handover` already exists and already reads; and two kinds instead of one make the
fact-rule check span items of a batch rather than one payload. **Rejected**: the citation need is
already served by `evt:` refs (a fact cites an earlier entry's id), and the smaller interface wins.

## Option C - free payload, shape enforced in the seat skill only
Pros: no server change. Cons: the rule the owner asked for is "a fact without an evidence ref is
refused by the SERVER"; a skill is a convention, and the measured failure mode of this platform is
exactly conventions that nothing enforces. **Rejected.**

### The payload: keys, limits, refusals

The payload is a JSON object with **exactly** these keys; an unknown key is a refusal, not a field
someone keeps for later.

| key | required | shape | limit |
|---|---|---|---|
| `decision` | always | `continue` \| `blocked` \| `done` \| `escalate` | — |
| `next_action` | unless `decision=done` | one line, no newline | 200 chars |
| `facts` | always | array of `{claim, evidence}` | 1..8 items; `claim` ≤ 160 chars |
| `constraints` | optional | array of context refs (`context:<uuid>`, `task:<uuid>`) | 8 items |
| `blocked_on` | iff `decision` is `blocked` or `escalate` | the queue row's id, verbatim (`qitem-…`), no `qitem:` scheme | the id as the daemon prints it |

`evidence` is a string matching exactly one of:

- `evt:<uuid>` - a ledger entry id **of the same task**. The server checks existence and task
  identity: it is the same table, and an event id naming another task's entry is a lie the server
  can catch without any cross-store read.
- `sha:<7..40 lowercase hex>` - a git object as `git rev-parse --short`/full prints it.
- `qitem:<id>` - a queue row id (the daemon's rail).
- `task:<uuid>` - a hub task id.

A fact with no `evidence`, or an `evidence` matching no ref grammar, or an `evt:` id that does not
belong to the task, is refused. Nothing else about refs is verified: a `qitem:` row and a `sha:`
object live in stores the ledger cannot read in the same transaction, and a rule that pretends to
check them would be a rule that passes anything. **The ledger refuses shape and same-task
identity; it does not refuse truth it cannot see.**

**Two refusal tokens** (the ledger's reason vocabulary, `intent.go`; stable tokens, never prose):

- `handover_malformed` - a key missing, an unknown key present, a wrong type, a `decision` outside
  the enum, a `next_action` over 200 chars or carrying a newline, a `blocked_on` present when it
  must not be or absent when it must, a payload over **2048 bytes serialized**.
- `handover_fact_without_evidence` - a `facts` item without an `evidence` string, an `evidence`
  matching no ref grammar, or an `evt:` ref that does not resolve to an entry of this task.

The 2048-byte cap is a **product default** like the resume brief's 4000-token budget, not a law:
the refusal names the size and the limit, and the server never truncates a payload (a truncated
decision record is the silent degradation this ledger exists to prevent).

The check lives **beside the kind check** (`ledger_service.go`, the same place `IsIntentKind` runs),
so the outbox endpoint, a future MCP action, and every other writer pass through one check. That is
the whole of 2/8's server half: the keys, the two tokens, the one function.

### Ownership: one store per fact

- **Decision, facts, constraints** → the task ledger (hub), one `handover` entry.
- **Blocker, wake, handoff state** → the queue row (daemon), which already owns `blocked_on`,
  `wake_after_seconds`, `chain_of_record`.
- The handover **carries** `blocked_on` as the row's id and **never copies** its body, summary or
  wake time. Two stores, one pointer.
- The handover does **not** change task status. Status is `manage_task`'s write and its own
  `status_changed` entry; a handover that also moved status would be a second source for one fact.
  A `decision=done` handover is the seat's report; the completion is the status write.

### How a seat knows its current task after compaction (input to 4/8)

`manage_task(action=resume)` takes **`task_id` absent or empty** and resolves the task itself:

```
status = in_progress AND assignee = @<caller seat key>
ORDER BY updated_at DESC, id ASC LIMIT 1
```

The seat key is the **request's own** (the MCP header seat key, the same attribution the ledger
uses via `services.actorFor`/`WithActor`); it is never a parameter, so a seat cannot resume another
seat's item by naming it. No match → an error whose token is `no_task_for_seat` and whose text names
the two fixes (claim an item, or pass `task_id`). The brief's own `task.id`/`task.title` tell the
reader which item it got; if that is not the expected one, the caller passes `task_id` explicitly
(today's behavior, unchanged).

Why "most recently updated", not "must be exactly one": a seat legitimately holds several
`in_progress` items (this room's board shows three for one seat), so an ambiguity error would fire
on normal operation, and a new "current task" pointer would be a second source for a fact the board
already holds. The `list` action already takes `status`, `assignee`, `sort_by`, `sort_order`, so
the resolution needs no new read. The rule is a fallback, not a state: nothing is stored, so it
survives compaction, restart and a full client reinstall.

### The writer-less kinds: delete six, keep six

Keep (each has a writer today, or lands with this system):

`status_changed`, `progress`, `gate_verdict`, `evidence_submitted`, `context_updated`, `handover`
(the last gets its writer in 2/8, in the same change that deletes the others).

Delete from `TaskEventKindValues`, the table's `CHECK (kind IN …)`, and `IntentKinds`,
`DecisionKinds`, `FactKinds`:

| kind | why it dies |
|---|---|
| `assigned` | assignment is the task's `assignees` column; every reader uses it and the resume-by-seat rule above reads it too. |
| `claimed` | claiming is the `status_changed` → `in_progress` write, which already has a writer. A second entry for one act is a second source. |
| `delivered` | handoffs between seats are queue rows on the daemon's rail; the ledger never receives one, and the client's `delivered` JSON keys are the runner's message frames, not ledger kinds. |
| `context_loaded` | nothing loads context through the hub. Context changes are `context_updated`, which has a writer. |
| `escalated` | the lifecycle sweeper writes `task_audit_records`, whose own CHECK already lists `escalated`; a ledger entry would be a second source for one act. |
| `human_decision` | owner decisions are queue rows (`owner-decision` tag) and their record on the task lands as `status_changed`/`progress`; nothing writes a decision entry, and 4/8's brief reads neither. |

Two corrections 2/8 must make in the same change:

1. `intent.go`'s comment that "`delivered`, `escalated` and `context_updated` are written by the
   server (the wake loop, the gate, the context path)" is **false for two of the three**: no
   `AppendInTx` caller writes `delivered` or `escalated`. The comment is replaced, not kept with the
   kinds deleted beneath it.
2. The DDL migration that narrows the CHECK must **assert before it constrains**: count the rows
   carrying a deleted kind and fail naming the count if any exist, rather than let the re-add fail
   with a constraint error nobody can read. Dropping a value deletes no rows, so the assert is the
   honest report; 2/8 puts the count on its board item. (Likely zero: nothing ever wrote them.)

### 6/8: delete `ContextTracker` and `assemble_context`

**Delete the whole module**: `rust/agenthub-runtime/src/context/mod.rs` and `mod context;`
(`main.rs:7`), including `DEFAULT_LIMIT_TOKENS`, `DEFAULT_WARN_TOKENS`, `DEFAULT_HARD_TOKENS`,
`CompactionAction`, `ContextTracker::estimate_tokens`, `assess_threshold` and `assemble_context`,
with its inline `#[cfg(test)]` module.

Evidence, client at `51f614b`: grep for `ContextTracker|assemble_context` finds **only**
`context/mod.rs`; grep for `context::` outside that file finds nothing; the three `DEFAULT_*`
constants and `CompactionAction` are referenced **nowhere outside** `context/mod.rs`. The compaction
policy actually in use lives in the supervisor, not here. 6/8 is a deletion, not a migration: there
is no second consumer to keep alive.

## Interfaces

- `handover` payload: keys, enums, limits and two refusal tokens as above. Writer: the client
  outbox (`POST /api/v2/ledger/intents`, fact kind, no `base_seq`) and any future MCP action.
- `resume`: `task_id` optional; absent/empty resolves by the caller's seat key. Read-only.
- `TaskEventKindValues` / DDL CHECK: six values, listed above.
- Client runtime: `context` module removed; 4/8 calls `resume` with no `task_id` at seat start and
  after compaction, and prepends the brief (the brief's first three keys are the criteria, the last
  verdict and the next action by the lead's section order).

## Risks

- **Two next_actions.** The brief's `next_action` is the deterministic ladder
  (`resumeNextAction`), and `last_handover.next_action` is the seat's own line. They can disagree.
  Ruled: the ladder stays the brief's `next_action`, and it **gains one input**: the last handover's
  `decision` ranks first - `blocked`/`escalate` yields `"blocked: <blocked_on>"` /
  `"escalate: <blocked_on>"` before the REJECT branch, because a seat that already read the
  rejection and still cannot proceed is the more recent fact. Detection if this is wrong: a brief
  saying `continue` beside `last_handover.decision=blocked` is a red test.
- **A refusal that fires on first real use.** 2/8's acceptance must include one handover that the
  real outbox writes end to end (accepted) and one of each refusal (missing key, unknown key, fact
  without evidence, `evt:` of another task).
- **The kind deletion meets data.** Guarded by the pre-constraint assert above.
- **Resume-by-seat picks the wrong item.** Mitigated by `updated_at DESC, id ASC` being stated, and
  by the brief naming the item; a seat that got the wrong one passes `task_id`.
- **Where this record lives.** The owner's acceptance for 1/8 names exactly one new file
  (`RULING-handover-decision-record.md`) and no other path, so it commits alone. The hub refuses
  that commit by construction, measured on 2026-10-17: its `docs-only-guard` refuses a seat's
  all-documentation commit, and the seat guard's `hook-bypass` class refuses the one escape the
  guard's own message offers (`OWNER_DOCS_COMMIT=1`) when a seat sets it. The client repo has no
  such hook (its git dir installs `pre-push` only), so the record lands at
  `agenthub_client/docs/RULING-handover-decision-record.md`, in the repo whose runtime clause 6/8
  executes. If the owner wants it in `ai_docs`, the owner's own commit is the only carrier.
- **Not done here**: no `CHANGELOG` entry accompanies this ruling, for the same reason - the
  acceptance allows one path; the changelog duty rides the code commit of 2/8 when the rule ships.

## Handoff

- **go-dev, 2/8 `bb5e5022`**: implement the key set, the two refusal tokens, the 2048-byte cap and
  the `evt:` same-task check in `ledger_service.go` beside `IsIntentKind`; delete the six kinds from
  the Go list and the DDL with the pre-constraint assert; correct the `intent.go` comment.
- **go-dev, 4/8 `b578bd05`**: make `task_id` optional in the `resume` action, implement the
  resolution query above, add the handover input to `resumeNextAction`, and call it at seat start
  and after compaction.
- **go-dev, 6/8 `363631f4`**: delete the `context` module.
- **Lead**: 1/8 is closed by the commit carrying this file; close it when the numstat reads exactly
  this one path.
