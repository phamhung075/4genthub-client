# The queue row body is four fixed lines at create

## Added

- `rust/agenthub-runtime/src/queue/mod.rs`: `validate_body_schema` + `BODY_FIELDS` enforce the architect's 3/8
  ruling at `queue create`: exactly four lines in order (`facts: `, `rule: `, `decision: `, `next: `), one
  occurrence of each key, single-line non-empty values, per-field caps 280/110/100/78 (chars, as the existing
  120/600 caps count), and at least one allowed tag. Six refusals, each naming the rule and the field:
  - `body schema: field '<k>' is missing` (one line per missing field, schema order; an empty body names all four)
  - `body schema: unexpected line <n> ('<line>'); the body is facts, rule, decision, next, in that order`
  - `body schema: field '<k>' appears twice`
  - `body schema: field '<k>' is empty`
  - `body field '<k>' cap: <n> chars exceeds the <cap>-char cap`
  - `tag rule: a create carries at least one allowed tag`
  The rule is create-only: `queue update` takes no body, so rows written earlier keep theirs verbatim and there is
  no compat path. The existing whole-body 600 check stays; a conforming body cannot reach it because the
  prefixes and newlines (32) plus the four caps (568) sum to exactly 600 - the boundary test pins that arithmetic.
- `internal/clientruntime/queue.go`: `QueueCreate` refuses the same six cases before writing a frame, with
  `RowSchemaRefused` carrying the identical sentence. One wording serves both seams; a seat gets the refusal in
  one line and no round trip.
- `internal/clientruntime/queue_test.go`: a table pins all six messages and asserts no frame is written; the
  wire-contract tests now carry a conforming body and one tag.
- `internal/clientroom/queue_test.go`: the create frame test sends `--body`/`--tag`; a new test pins the CLI
  refusal (exit 2, the missing field named, no frame).
- `rust/agenthub-runtime/src/queue/mod.rs` tests: eight new tests - stored verbatim, missing fields in order
  (1, 2 and 4 lines missing), out-of-order line, duplicate key, empty value, the 600-char cap arithmetic, a value
  over its cap, and no tags. Existing row fixtures across the queue and supervisor suites were migrated to the
  four-line body and an allowed tag.

## Changed

- `internal/clientlifecycle/budgetguard.go`: the park row is migrated to the four-line body (the record path, the
  remove-first/reseat order and the close request are kept, now inside the ruled caps) and its tag is
  `assignment` - `status` was retired on 2026-10-17, so the old row would have been refused for the tag alone.

## Measured

- Daemon end to end, a temp `agenthub-runtime` on a temp socket and room: a prose body is refused
  (`body schema: unexpected line 1 ('prose text'); ...`), an empty tag list is refused
  (`tag rule: a create carries at least one allowed tag`), and the four-line body with one tag is stored and
  returned with its newlines intact.
- The client door on the same socket: `room queue create ... --body "prose text" --tag gate` prints the daemon's
  sentence and exits 2 with no frame; the conforming create prints the row and exits 0.

## Tested

- RED first: `cargo test --offline queue::tests` failed 6 of the new tests (each `unwrap_err` returned a Row)
  before the parser existed; `go test ./internal/clientruntime ./internal/clientroom` failed on the missing
  `RowSchemaRefused` and on the CLI test's frame. Both then went green.
- `cargo test --offline --manifest-path rust/agenthub-runtime/Cargo.toml queue::tests` from the repo root: 31
  passed, 0 failed.
- `cargo test --offline` (whole crate): 173 passed, 1 failed -
  `supervisor::tests::a_multi_line_message_reaches_a_runner_seat_as_one_text_frame`, a pre-existing environment
  failure (it cannot bind its reply socket: "path must be shorter than SUN_LEN"). It fails identically at base
  d6a7be6, verified in a detached worktree, and the queue schema does not touch that path.
- `go test ./...`: 33 packages ok, 0 failures. `go vet` on the touched packages: ok.
