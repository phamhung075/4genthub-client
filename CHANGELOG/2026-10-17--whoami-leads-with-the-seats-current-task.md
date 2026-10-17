# `whoami` leads with the seat's current task and its resume brief

## Added

- `internal/clientruntime/currenttask.go`: `4genteam whoami` now asks the hub's
  `manage_task` `resume` for the task this seat holds `in_progress` and prints its brief as
  `current_task`, the document's FIRST key. The call names no `task_id`: it sends the identity's own
  `<room>/<seat>` in `X-Agenthub-Seat` (the value the rendered MCP block carries) and the hub
  resolves the task, so there is no argument through which one seat could ask for another's. The
  transport is the streamable-HTTP MCP one: JSON-RPC `tools/call`, answered as JSON or an SSE
  `data:` stream.
- The three answers stay distinct, which is the point of the shape: `{"id", "resume"}` for a task,
  `null` when the hub answers `no_task_for_seat` (the seat honestly holds nothing), and
  `"unavailable (<reason>)"` when no hub answered. A failed read is never a null - a compacted seat
  told null would stop looking, and the truth is that nothing answered. With `AGENTHUB_URL` or
  `AGENTHUB_TOKEN` unset the answer is `unavailable (...)` with no network call.
- The human form (`whoami` without `--json`) leads with `Task:`, the brief's `Verdict:` and the
  `Next:` action before the identity lines, in the same order as the document.

## Changed

- `internal/clientruntime/types.go`: `WhoamiResponse.CurrentTask any` is declared first and has no
  `omitempty`, so the key is always stated - `null` included. `internal/clientruntime/currenttask.go`
  reads the hub pair through `agentHubURLEnv`/`agentHubTokenEnv`, the constants every hub call
  this client makes shares.
- `internal/clientruntime/transport_test.go`: `hermeticSeat` now clears `AGENTHUB_URL` and
  `AGENTHUB_TOKEN` too. Those two leaked into the test environment from the seat running the suite,
  and a `whoami` test that did not seed them would have POSTed to the live hub.

## Verified

- `go test ./internal/clientruntime -count=1` passes, including the new
  `currenttask_test.go`: the brief leads the document and the human form; the wire body is one
  `manage_task resume` with no `task_id`; the real wire (httptest) sends `Authorization` and
  `X-Agenthub-Seat`; `no_task_for_seat` prints an explicit `null`; an unanswered hub prints
  `unavailable (`. `go test ./... -count=1` passes.
- Smoke, live hub (which predates the hub-side `resume`): `/tmp/4genteam-whoami whoami --json`
  exits 0 and prints `"current_task": "unavailable (task_id is required)"` as the first key - the
  client's honest answer until the hub half (`fe3708f7`, root repo) is deployed.
