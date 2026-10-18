# The gate warns when a criterion names only a path

## Changed

- `internal/clientcomplete/gate.go`: `register.pathOnlyWarnings` names every criterion that carries a
  numstat path and none of the test command, the exit code or a sha. Such a criterion is ADMITTED by the
  register and then judged blind, because the count-only wire (`clientjev.RedactForWire`, owner ruling O9a)
  turns the path into `<path>` before Jev sees it. The warning is advisory: it does not refuse, the
  criterion stays in the judge's set, and the verdict is unchanged. It names the admissible form - the test
  command with its exit code, a base/head sha prefix, and the counts (files changed, lines moved).
- `internal/clientcomplete/gate.go`: `result.Warnings` carries them (`omitempty`, so a compliant task's JSON
  is unchanged), on both the refusal and the judged path.
- `internal/clientcomplete/complete.go`: `4genteam complete` prints each warning to stderr before the gate
  line, so the operator sees it in shadow and enforced runs alike.
- `register.carries` is now the OR of `register.names`, which reports the facts separately; the refusal
  semantics are untouched.

## Tested

- `go test ./internal/clientcomplete/ -count=1` -> ok.
- `go test ./...` -> ok (28 packages).
- `cargo test --offline` in `rust/agenthub-runtime` -> ok.
