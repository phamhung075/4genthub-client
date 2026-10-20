# A path-only criterion is refused before the judge, not judged blind

A completion whose acceptance criteria name only a path the numstat carries used to be ADMITTED and then
judged on `the numstat names <path>`: the count-only wire (`clientjev.RedactForWire`, owner ruling O9a)
removes the one fact the criterion resolved to, so the judge was asked an opinion and the seat spent a
completion cycle to learn the gate already knew. Board row `9e9b62f7`: three rejections of `4670f48e` in 18
minutes (0.33/0.37/0.20, then 0.83/0.73/0.97, then 0.84/0.74/0.97) were all criteria naming a script path and
a test file.

`internal/clientcomplete/gate.go`:

- `facts.pathOnly` is the new test: a carried path and none of the command, the exit code, a sha or a count.
  `evaluate` now splits the register's answer three ways - `blind` (path-only), `judgeable` (any fact), and
  `silent` (no fact) - and a completion with either `blind` or `silent` is `unjudgeable` with no judge call.
- A criterion that carries a path AND another fact stays admitted: the claim survives the redaction
  (precedent: `go -C agenthub_go test <path> -count=1 exits 0` closed at 0.96-0.98).
- `register.pathOnlyWarnings`, `register.carries` and `result.Warnings` are deleted with the contract they
  described. The refusal's own words replace them: each blind criterion is named by number with what the
  wire would have shown the judge, and the advice is the vocabulary the wire does carry - the test command
  with its exit code, a base/head sha prefix, the counts with this bundle's value. The numstat paths are still
  printed as measured evidence, with the sentence that says why a path is not enough on its own.

`skills/share/verification-before-completion/SKILL.md` stated the old contract ("a path-only criterion is
admitted then judged blind (0.16-0.34)") and is corrected, as is
`ai_docs/agent-system/task-workflow-and-reporting.md` in the hub.

Proven 2026-10-20 in `agenthub_client`: `go test ./... -count=1` green, `go vet ./internal/clientcomplete/`
clean, and the real refusal text printed from the package's own `jevServer` fixture - it names
`criterion_1 ... names only a carried path, and the wire redacts a path to <path> before Jev sees it`, keeps
`criterion_3` judgeable, and ends with the register the bundle does carry.
