# Agent setup: 4genthub, the rig orchestrator and herdr, then rooms and seats

For an AI agent (or a person) bringing a machine into service for 4genthub, and then building
rooms and seats when the user asks for them.

Three rules run through the whole page.

- **Ask the user for every value that depends on the machine, the room or the operator's choice.**
  The placeholders are `<SERVER-URL>`, `<ROOM>`, `<RIG>`, `<SEAT>`, `<RUNTIME>`, `<MODEL>`,
  `<TEAM-DIR>`, `<SKILLS-PATH>`. Never guess one, never copy one off another machine.
- **If a prerequisite is missing, stop and tell the user which one.** Do not invent a workaround.
- **Never print, log, commit or send the token.** It lives in one file, named below, and nowhere
  else.

Commands run in the project directory unless a step says otherwise.

## Part 1 — Set up a machine

### 1.1 What has to be present

| Piece | What it is | Install check | Version check |
|---|---|---|---|
| 4genthub | the hosted service: accounts, rooms, seats, tasks, and the web app | `curl -fsS <SERVER-URL>/health` answers | the `version` field of that answer |
| 4genthub MCP | the same service over MCP, at `<SERVER-URL>/mcp` | the agent's MCP client lists the `agenthub_http` tools | the `initialize` result's `serverInfo` |
| `4genteam` | client supervisor: sync, safeguards, status bridge, watch view, team | `4genteam --help` exits 0 | the verb list it prints |
| tmux | hosts the seat terminals | `tmux -V` exits 0 | the number it prints |
| herdr | the terminal provider the watch grid is displayed in | `herdr --version` exits 0 | the number it prints |

Ask the user for `<SERVER-URL>` (the hosted service is `https://api.4genthub.com`, and its web app
is `https://www.4genthub.com/`) and for the account's key. Both are theirs to give.

### 1.2 The checks, in order

```bash
4genteam --help            # the client; prints the verb list
4genteam doctor            # reports safeguard stack health (agent, compact, watchdog, roomguard, bridge)
tmux -V
herdr --version
curl -fsS <SERVER-URL>/health
```

`/health` answers JSON with `status` (`healthy` when the service is up), `timestamp`, `server`,
`version` (the release the backend is running), `auth_enabled`, `connections`
(`active_connections`, `uptime_seconds`) and `status_broadcasting` (`active`,
`registered_clients`). A 200 with a different `status` is still a reachable server; a connection
error is not, and is one of the two things to take back to the user.

Stop here and name the missing piece if any check fails. A seat needs tmux to have a terminal, and
the watch grid needs herdr; both are installed, not worked around. `4genteam up` ends by launching
`herdr`, and stops before that when `HERDR_ENV` is set to a non-empty value; with no `herdr` on
`PATH` it prints `4genteam up: herdr is not on PATH; the watch view needs it (set HERDR_ENV to skip
this step)` and exits non-zero.

### 1.3 One credential, one file

The client has exactly one credential and reads exactly one file.

- `AGENTHUB_URL` — the server's base URL, no trailing slash.
- `AGENTHUB_TOKEN` — the account key.
- `AGENTHUB_CONNECTOR_TOKEN` — the upload token for the `agent` daemon, scope `sessions:write` only.
- The file is `~/.config/4genthub/.env`.
- **A variable already set in the environment wins over the file.** The file is a default, not an
  override.

```bash
mkdir -p ~/.config/4genthub
[ -f ~/.config/4genthub/.env ] || cp .env.sample ~/.config/4genthub/.env   # then fill in AGENTHUB_TOKEN
chmod 600 ~/.config/4genthub/.env
```

Initialize it once, before the first verb that talks to the server. Never copy over an existing file:
it holds the user's key. `scripts/build.sh` creates it from `.env.sample` when none exists.

Ask the user for the key; do not generate one, and do not put one in this file yourself. The
sample also lists the optional settings (`TYPESAFE_API_KEY`, which Jev uses on this machine for `4genteam decide` and `4genteam complete`; `DEEPSEEK_API_KEY`, the `COMPACT_*` limits,
and `OFFPEAK_ONLY`); a blank value means the default shown in the sample. Only the
`AGENTHUB_URL` and `AGENTHUB_TOKEN` have to be set for the client to talk to the server.

### 1.4 Connect a project to the 4genthub MCP

Every project reaches the same endpoint with the same key. Add this to the **project's**
`.mcp.json`, creating the file if it is absent:

```json
{
  "mcpServers": {
    "agenthub_http": {
      "type": "http",
      "url": "<SERVER-URL>/mcp",
      "headers": {
        "Accept": "application/json, text/event-stream",
        "Authorization": "Bearer ${AGENTHUB_TOKEN}"
      }
    }
  }
}
```

`${AGENTHUB_TOKEN}` is expanded from the environment, so export it in the shell that starts the
agent, or start the agent from a shell that has it. **Never write the token into `.mcp.json`.**

Check the connection by listing the `agenthub_http` tools in the agent's MCP view. The endpoint
answers `POST /mcp` (JSON-RPC) and `GET /mcp` (the event stream the `Accept` header above asks
for). A `403 Not authenticated` means no bearer token reached the server; a `401 Invalid
authentication credentials` means one arrived and is invalid or expired. Both are the user's to
fix: ask for a key, and never paste one into a message.

### 1.5 Failure map

| Symptom | Cause | What to do |
|---|---|---|
| the client says `AGENTHUB_URL` and `AGENTHUB_TOKEN` must be set | neither the file nor the environment has them | ask the user for the key, write `~/.config/4genthub/.env` |
| `403 Not authenticated` from `<SERVER-URL>` or the MCP endpoint | no bearer token reached the server | ask the user for a key; never paste one into a message |
| `401 Invalid authentication credentials` | a token arrived and is invalid or expired | ask the user for a new key; nothing else in the client or `.mcp.json` needs to change |
| `4genteam doctor` reports a safeguard row that is not running | the runtime daemon or that safeguard is stopped | `4genteam up <RIG> --team <DIR>`; `doctor` itself is read-only and starts nothing |
| `4genteam ps <RIG> --nodes` shows no seat for the room | the room was never materialized on this machine | `4genteam sync room <ROOM>`, then `4genteam up <RIG> --team <DIR>` (step 2.5) |
| the watch view does not open | herdr or tmux is missing | `herdr --version`, `tmux -V`; stop and tell the user |
| `… run a DeepSeek model (…) and DeepSeek bills at PEAK right now` from `4genteam up`, `compact`, `continue`, `watchdog` or `sync respawn` | the room holds a seat on a `deepseek/` model, and `OFFPEAK_ONLY` is on (the default) so the client will not start or resume it inside a peak window (01:00-04:00 and 06:00-10:00 UTC, Monday-Friday) | not a fault. Start it at the peak rate with `--allow-peak`, or `4genteam offpeak set off` to allow it from now on, or wait for the off-peak instant the message names. `4genteam offpeak` reports the state, `4genteam offpeak status` the setting |
| a seat exits at once after launch | its runtime is not installed, or its runtime and model disagree | read the seat's log and report the missing piece to the user; do not switch the model to make it start |
| `team apply` refuses a version | a version must be a concrete `x.y.z`, and the value lives in `team.json` — `team apply` has no `--version` option | fix `modules[].version` in `team.json`; `latest` and other aliases are rejected. `--version` belongs to `team import-project` and `team publish-skills` |

## Part 2 — Build a room and its seats, when the user asks

A room is the cloud record: the seats, what each seat is told, who may message whom, and which
skills each seat carries. A room's seats are hosted locally by `agenthub-runtime` and supervised by
this client (`4genteam`). The two share
a name unless the user wants them different (`--room` says otherwise).

### 2.1 Ask first

Collect four answers before writing anything. Show the user the placeholder you need and the
choices the software accepts. Do not answer any of them yourself.

**(a) The seats.** For each seat:

- its **seat key** — the name the room uses for it. Letters, digits, `-` and `_`, starting with a
  letter or a digit.
- its **seat type** — a slug the server stores. The curated catalog carries `lead`, `developer`,
  `reviewer`, `writer`, `tester`, `architect`, `planner`, `researcher`, `debugger`. Ask before
  using anything else; the seat type decides what the seat is told and which MCP servers it mounts.
- its **runtime** — one of `claude-code`, `codex`, `agy`, `omp`. Ask, and confirm the runtime is
  installed on this machine; a runtime the seat cannot run is a seat that exits at launch.
- its **model** — ask for the exact id the runtime accepts. A blank model is allowed and means the
  runtime picks its own default. `codex` cannot run Claude models; `agy` can run Claude, GPT and
  Gemini models; `claude-code` also takes aliases such as `sonnet`.
- whether it **pins a version** (`pinned_version`, a concrete `x.y.z`) or **follows the latest**.

**(b) The permission policy, one answer per seat.** Read the five choices to the user and get an
explicit answer for each seat. `4genteam policy show <SEAT> --room <RIG> --team <FILE>` prints one
seat's rendered policy:

| Policy | Means | Stored as |
|---|---|---|
| `locked` | the most restrictive packaged posture — for seats that must not touch anything unattended | `builtin:locked` |
| `standard` | the packaged default posture for managed working seats | `builtin:standard` |
| `open` | a permissive packaged posture for trusted, high-autonomy seats | `builtin:open` |
| `yolo` | the operator/no-guardrails posture — everything the harness allows; the launch posture is **full bypass** | `builtin:yolo` |
| `none` | the reserved deliberate-none choice: the posture is the floor, the same as naming nothing, but the choice is visible | `none` |

This client has no `policy list`: `4genteam policy show <SEAT> --room <RIG> --team <FILE>` prints one
seat's rendered policy, and the five postures above are the choices a 4genthub
seat may record.

A seat that does not name one gets `standard`. **Say what `yolo` means before the user picks it: a
`yolo` seat runs unattended with no approval prompts.**

**What the client does with the choice today, and say this to the user:** the policy is recorded on
the seat, and the server's default is `standard` — but the client that materializes the room on
this machine overrides it at launch and starts **every** seat as `builtin:yolo`. So a seat the user
put on `standard` does not get approval prompts either; only the record keeps the choice. Do not
present the per-seat answer as the posture the seat will run under until that override is fixed.

**(c) The links.** A link is one direction and one kind. **A seat with no link in a direction
cannot send in that direction** — the default is deny, and nothing routes a message without an
explicit link. Ask which seat may send to which, and with what kind:

| Kind | Authorizes |
|---|---|
| `delegates_to` | assigning work (`task`) |
| `collaborates_with` | questions and notices |
| `escalates_to` | reports and escalations up the ladder |
| `can_observe` | read-only watch of the other seat |
| `spawned_by` | the seat that started this one |

**Every room is created with its edges.** A room with `"links": []` is not finished: its seats cannot
message each other, `whoami` returns no `edges`, and the topology page draws the seats with no lines.
Do not run `team apply` until `links` holds at least: the lead `delegates_to` every other seat,
each worker `delegates_to` the reviewer, and every non-lead seat `escalates_to` the lead. `team apply` and its dry run refuse a team of more than one seat with no `links`. The dry
run in 2.3 must show one `link` line per edge; if it shows none, stop and add them. After apply,
read back `GET /api/v2/rooms/<room>/seats/<seat>/links` for each seat, and put the same
list in the local `spec.json` so `whoami` returns it.
| `escalates_to` | reports and escalations |

`spawned_by` and `can_observe` are also link kinds: `spawned_by` affects launch order and never
authorizes a send, and `can_observe` allows reading another seat's output (`4genteam capture
<session>`, or that seat's session log through `4genteam watch`) but not sending to it.

One link authorizes one direction only. An answer needs its own link the other way: a developer
that reports to the lead has `dev escalates_to lead` as well as whatever the lead uses to assign.

**(d) The skills paths.** Ask the user for each skills path to give all the agents of the room.
Never guess one, and never assume a path from another machine. Two shapes, and they publish
differently:

- a **project's** skills tree — each skill is a directory holding `SKILL.md`, under the project's
  `.claude/skills`. Publish with `4genteam team import-project`, which also pushes the project's
  `.mcp.json` servers as modules.
- a **skill library** that carries an inventory file. Publish with
  `4genteam team publish-skills --inventory <FILE>`, and pass `--source-root <SKILLS-PATH>` (or set
  `AGENTHUB_SKILLS_ROOT`) to the checkout the inventory's paths are relative to.

Both push module versions. `4genteam team drift-check` then reports stored skills whose recorded
file has changed under the roots it resolves them against. The published modules are what
`seat_overlays` in step 2.2 attaches to seats.

A repository that keeps skills of its own arranges them in four kinds, and the path says which is
which:

| Path | Who gets the skill |
|---|---|
| `skills/share/<skill>/SKILL.md` | every seat of every room |
| `skills/rooms/<room>/<skill>/SKILL.md` | every seat of that one room |
| `skills/rooms/<room>/<seat>/<skill>/SKILL.md` | that one seat |
| `skills/client/<skill>/SKILL.md` | an LLM running the client itself, not a seat |

The reader finds a skill by one rule: **a directory holding `SKILL.md` is a skill, and a directory
without one is a seat folder.** So `skills/rooms/<room>/<name>` is a room-level skill when it holds
`SKILL.md` and the seat folder `<name>` when it does not. A shared or client skill stays generic — no
rig, seat, host or personal path in it.

**The client also delivers this tree itself.** `4genteam sync room ROOM` is the step that writes each
seat's agent directory, so it is also the step that gives a seat the skills it resolves to: it copies
them into that seat's skills path (`<out>/<room>/rig/agents/<seat>/skills/<skill>/`) and declares the
ids in that seat's `agent.yaml` — `resources.skills[]` and each profile's `uses.skills` — because the
declaration is what the runtime loads from, exactly as below. The tree is found at `--skills-root`,
else `$AGENTHUB_CLIENT_SKILLS`, else `skills/` beside the binary, and a tree that is not there is
refused by name rather than skipped. This route needs no cloud module and no overlay: a seat receives
`share/`, then its room's room-level skills, then its own seat-level skills, and nothing from another
room, another seat or `client/`.

Two facts about the publish route, because they decide which tree to point it at: `team
import-project` reads a **project's** own tree at `.claude/skills`, one directory per skill, each
holding `SKILL.md`, and refuses a directory that has none — so it publishes a flat tree, not a
four-kind one.

**Where a skill lands, and what makes it count: the seat's own spec.** Publishing puts the file in
place; the install is the seat spec naming it. The render declares every skill under
`resources.skills[]` with the module's `id` and the `path` it is delivered at (`skills/<slug>`), and
that declaration is what the runtime loads from — and it lists the same ids in the profile's
`uses.skills` (`profiles.default.uses.skills`, built in the same loop), which is the half that
activates them. A copy on disk that the spec does not declare is inert, and so is a pool entry no
profile uses: an install has two halves, the file at the spec's path and the `id` in the spec that
names it.

### 2.2 Write `<TEAM-DIR>/team.json`

One file names the whole room. Ask the user for `<TEAM-DIR>` (a directory you may create), and
give every module a file next to it.

```json
{
  "room": { "slug": "<ROOM>", "name": "<a display name>" },
  "modules": [],
  "seats": [
    {
      "seat_key": "<SEAT>",
      "seat_type": "<SEAT-TYPE>",
      "runtime": "<RUNTIME>",
      "model": "<MODEL>",
      "pinned_version": "1.0.0",
      "follow_latest": true,
      "permission_policy": "<POLICY>"
    }
  ],
  "links": [
    { "from": "<SEAT>", "kind": "delegates_to", "to": "<OTHER-SEAT>" }
  ],
  "company_overlay": [],
  "seat_overlays": { "<SEAT>": ["<module-slug>", "<module-slug>"] }
}
```

| Field | Holds |
|---|---|
| `room` | `slug` (the room name) and `name` (what the user sees) |
| `modules` | `slug`, `kind`, `version`, `file` — a module's text is read from `file`, relative to `<TEAM-DIR>` |
| `seats` | one entry per seat, as in (a) and (b) above |
| `seats[].pinned_version` | a concrete `x.y.z` to pin the seat to |
| `seats[].follow_latest` | `true`: the seat follows the latest version. `false`: it takes the latest version now and stays there. A seat that names neither uses the account's seat-settings default |
| `links` | `from`, `kind`, `to`, as in (c) above |
| `company_overlay` | module slugs every seat of the account gets (account-wide; applying replaces it) |
| `seat_overlays` | seat key → the module slugs that seat gets |

A seat is pinned or it follows, not both: send `pinned_version` for the first, `follow_latest`
for the second, and delete the key you are not using. The example above shows both so you can see
where each goes; `pinned_version` wins if both are present.

Ask the user before adding a `company_overlay`: it applies to their whole account, not just this
room.

### 2.3 Dry run, show the plan, get approval

```bash
4genteam team apply --team <TEAM-DIR> --dry-run
```

It writes nothing, and it prints one `plan: <label> (METHOD <path>)` line per step, in the order
apply will send them: the seat types to seed, each module version, the room, each seat, each link,
then the overlays.

A dry run **still reads the server when credentials are set**: it asks which seat types it already
stores, so the seed step disappears when they are already there and the list is what apply would
really send. With no credentials configured it prints the plan without that classification.

`team import-project --dry-run` and `team publish-skills --dry-run` print their own header
(`plan: import-project from <dir> (N module(s))`) and then one line per module: `PUSH`, `SKIP
(identical content already stored)`, `NEW-VERSION (<version> stored with different content)`, or,
with no credentials, `PUSH unless stored content is identical`.

Count the `link` lines against the edges you agreed in (c): fewer, or none, is a refusal to proceed. Show the plan to the user and get an explicit yes before the next step.

### 2.4 Apply

```bash
4genteam team apply --team <TEAM-DIR>
```

Each step prints its outcome (`created`, `applied`, or `exists` for a room or seat that is already
there). A failure stops the run and names the step.

### 2.5 Materialize the room on this machine

The cloud now knows the room; this machine has no seats yet. Ask the user which `<RIG>` name to
use (the room name by default), then:

```bash
4genteam sync room <ROOM>         # writes the room's definition and the seat files
4genteam up <RIG> --team <DIR>   # launches daemon, seats, supervisor, bridge and watch wall
4genteam ps <RIG> --nodes        # one row per seat of the room, with its lifecycle and activity
```

`4genteam up <RIG> --team <DIR>` is the way to bring a room back after a restart: in one command it
checks the runtime daemon, starts the room from the local team definition that directory holds, starts the
status bridge, then the compaction supervisor, opens the watch view, opens the rig UI and hands
over to herdr. `--team DIR` is required — there is no default team — and its positional is the **rig** name, the room name unless the user asked for a
different one.

`4genteam up`, `4genteam compact`, `4genteam continue`, `4genteam watchdog` and `sync respawn` are
the five verbs that start or resume seats, so each of them runs the DeepSeek peak gate before it does:
with `OFFPEAK_ONLY` on (the default), a room holding a seat on a `deepseek/` model is refused at peak
(exit 3) with nothing resumed, and the message names the seat, its model and when off-peak resumes. A
room with no such seat, or an unreadable room, is never refused. See
[README.md](../README.md#deepseek-peak-billing) for the schedule, `4genteam offpeak` and the two flags.

To watch the room work — the seat grid and the lead's own window — see
[docs/WATCH.md](WATCH.md).

`sync room` refuses to replace a real `.env` that sits in the room directory — it names the path
instead. That file is not this client's to overwrite; show the path to the user.

### 2.6 Brief the lead

Tell the lead, in one message: the room, each seat with its type, runtime, model and policy, the
links, and the skills attached to each seat. Use the room's own messaging rule (`4genteam send`,
or the queue for durable handoffs); the lead routes the work from there.

### 2.7 Worked example: a three-seat room

The user asks for a room with a lead, a developer and a reviewer. Nothing below is a default —
every value shown came from the user.

| Seat | Type | Runtime | Model | Policy |
|---|---|---|---|---|
| `lead` | `lead` | `<RUNTIME>` | `<MODEL>` | `standard` |
| `dev` | `developer` | `<RUNTIME>` | `<MODEL>` | `standard` |
| `reviewer` | `reviewer` | `<RUNTIME>` | `<MODEL>` | `standard` |

Links: the lead assigns to the developer and to the reviewer, the developer hands work to the
reviewer, and both report to the lead. The reviewer needs to ask the developer questions, so that
direction gets its own link.

```json
"links": [
  { "from": "lead",     "kind": "delegates_to",      "to": "dev" },
  { "from": "lead",     "kind": "delegates_to",      "to": "reviewer" },
  { "from": "dev",      "kind": "delegates_to",      "to": "reviewer" },
  { "from": "dev",      "kind": "escalates_to",      "to": "lead" },
  { "from": "reviewer", "kind": "collaborates_with", "to": "dev" },
  { "from": "reviewer", "kind": "escalates_to",      "to": "lead" }
]
```

Then, in order: the dry run (2.3), approval, the apply (2.4), `4genteam sync room <ROOM>`,
`4genteam up <RIG> --team <DIR>`, `4genteam ps <RIG> --nodes`, and the brief
to the lead (2.6).

### 2.8 The questions, in the order to ask them

1. "What is the server URL for this account, and what is the key?" → `AGENTHUB_URL`,
   `AGENTHUB_TOKEN`.
2. "What should the room be called, and what name should the display show?" → `room.slug`,
   `room.name`.
3. "Which seats do you want? For each: a short name (the seat key), what kind of seat it is, and
   whether it pins a version or follows the latest." → `seat_key`, `seat_type`,
   `pinned_version` / `follow_latest`.
4. "For each seat: which runtime, and which model? Is that runtime installed on this machine?" →
   `runtime`, `model`.
5. "For each seat: which permission policy — `locked`, `standard`, `open`, `yolo` or `none`?
   `yolo` means the seat runs unattended with no approval prompts." → `permission_policy`.
6. "Who may send to whom, and in which direction? Assigning work is `delegates_to`, questions and
   notices are `collaborates_with`, reports and escalations are `escalates_to`." → `links`.
7. "Which skills paths should all the agents of this room get?" → `team import-project` or
   `team publish-skills --inventory`, then `seat_overlays`.
8. "May I write the room to your account now?" → the approval before step 2.4.

A question with no answer is not a licence to choose. If the user does not know, say which field
is waiting and stop.

### 2.9 Working in the room

Once the room is up, a seat works the same way on any project.

- **Startup.** `4genteam whoami --json` prints this seat's identity (`rigName`, `memberId`, runtime,
  session name, role, model), its peers, the edges and the transcript path. Run it after any
  restart, before concluding anything about where the work stands.
- **Find work.** The task board is the MCP server: `manage_task` with `{"action":"list"}`,
  `{"action":"next","git_branch_id":"<uuid>"}`, or `{"action":"search","query":"<words>"}`. Create
  a task only after searching, and assign it with the `@seat-key` form.
- **Record as you go.** `manage_task` `update` to start (it needs `details`), `manage_subtask` for
  the pieces, `manage_context` for the decisions, numbers and open questions the next session
  needs.
- **Finish and hand off.** `4genteam complete --task <id> --base <sha> --summary <what changed> --test "<cmd>"` (it runs the Jev gate on this machine, then completes the task), then tell
  the lead the commit hash or the path you wrote. A room's work is not finished until the seat it
  names for review has seen it.
- **Report friction.** `submit_feedback` (or `4genteam feedback` when a runtime has no MCP) sends
  what went wrong in the layer it went wrong in.

The `agenthub_http` server mounts ten tools for this: `manage_task`, `manage_subtask`,
`manage_context`, `manage_git_branch`, `manage_project`, `manage_agent`, `manage_connection`,
`manage_seat`, `call_seat` and `submit_feedback`. Each one answers `--help`-style discovery by
reading its own schema, so read the tool before you call it.
