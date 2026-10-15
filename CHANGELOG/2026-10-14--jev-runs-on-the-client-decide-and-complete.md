## Jev runs on the user's client: `decide` answers locally and `complete` is the gated completion verb

### Changed
- `4genteam decide` no longer calls the server. It parses and validates the questions with the server's limits, secret-scans summary and detail, asks Jev from this machine and runs the ladder: every question at or above `JEV_DECISION_THRESHOLD` decides the tier (exit 0), anything else escalates one tier (exit 5, `escalate_to`, `reason`, per-question `option_id`, `confidence`, `recommended`). `credentials`, `spend` and `DECISION_OWNER_ONLY_CATEGORIES` go to the owner by code with no Jev call. An evaluator error escalates. The `--url` flag and the need for `AGENTHUB_URL`/`AGENTHUB_TOKEN` are gone; a missing `TYPESAFE_API_KEY` is a usage error (exit 2).
- `internal/clientevidence`: the collection and the post are exported as `Collect` and `Post` (`Body`, `CollectFailure`); `4genteam evidence` behaves as before.

### Added
- `internal/clientjev`: the System One client (`SelectChoices`, `EvaluateCriteria`), the question limits and the decision log (`~/.config/4genthub/jev_decisions.log`, one JSON line per decision or gate verdict).
- `4genteam complete --task ID --base SHA --summary TEXT [--test CMD] [--notes TEXT] [--url U]`: collects the evidence, reads the task's `acceptance_criteria`, runs the gate locally (D3 test run present and green, D4 no keep-out path, D5 numstat parses and is at most 20000 lines, then Jev on each criterion: any answer at or below 0.3 rejects, all at or above 0.8 accept, else uncertain), then stores the evidence and calls `POST /api/v2/tasks/{id}/complete`. Shadow by default; `VALIDATION_GATE_ENFORCEMENT=true` stops a reject or uncertain verdict before any server write (exit 6).
- `internal/clientcomplete`; registered in `cmd/4genteam/main.go`.

### Docs
- `README.md`, `docs/AGENT-SETUP.md`, `.env.sample`, `skills/client/install/SKILL.md`, `skills/share/room-decision-request/SKILL.md`.

### Tested
`go vet ./... && go test ./...`; new tests in `internal/clientjev`, `internal/clientcomplete` (a real git repository, a fake System One and a fake task server) and a rewritten `internal/clientdecide` suite.
