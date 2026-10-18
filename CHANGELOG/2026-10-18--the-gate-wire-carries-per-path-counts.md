## The gate wire carries per-path counts, and a count key with this bundle's number is a register fact

### Changed

- `internal/clientevidence/numstat.go` (new): the ONE interpretation of `git diff --numstat` -
  `NumstatRow` (path, added, deleted, binary), `NumstatStats` with `Files()`, `Total()` and `WireRows()`,
  and a strict `ParseNumstat` that keeps the gate's two error strings verbatim. `WireRows` is a rule, not
  the numstat's path order: largest total first, then added, then deleted, binary last, stable on ties, so
  the sequence carries no file name. The four wire count keys (`files_changed`, `lines_changed`,
  `lines_added`, `lines_deleted`) live here too, so the spelling has one source.
- `internal/clientjev/countsonly.go`: `countsState` sends ten keys - the seven unchanged plus
  `lines_added`, `lines_deleted` and `changes` (`[{added, deleted}]`, a binary row `{binary: true}`) in
  `WireRows` order, and `lines_changed` stays added+deleted. The inline second numstat parser is gone; a
  numstat it cannot read is an error, never a zero summary.
- `internal/clientjev/jev.go`: `EvaluateCriteria` returns that error without a judge call (the gate's D5
  already refuses such a body; this is the decide path's guard).
- `internal/clientcomplete/gate.go`: `register` carries `NumstatStats` instead of a path slice; `names`
  returns a `facts` struct (command, exitCode, path, sha, count); `carries` is the OR of the five; the
  duplicate `parseNumstat` is deleted. A count fact is true only when the criterion spells a wire count key
  (snake or spaced form) and the FIRST integer in that key's own clause - from the end of the key to the
  next count-key occurrence or the end of the criterion - equals this bundle's number for it. A match
  anywhere in the text is rejected: the sha tokenizer splits `2c34515` into `2` and `34515`, so a global
  match admits `files_changed is 2` on a three-file change. `pathOnlyWarnings` gains `count` to its
  trigger, and the refusal lists the counts it does carry.

### Why widening is safe

The verdict mapping, the threshold and the seven existing keys are untouched; no key is removed. The
change widens only WHICH criteria reach the judge: a criterion admitted before is admitted by the same
fact. `changes` rows carry counts and no path, so the count-only wire (O9a) is unchanged, and the
path-only warning's premise stays true because `<path>` is still what the judge reads. A count the bundle
does not carry is refused exactly as an unlisted path or an absent sha is; zero is a carried value (a
binary-only change moves `lines_changed` 0). The doc-of-record paragraph is a separate item, not this
commit.

### Tests

- `clientevidence`: `ParseNumstat` rows, totals, binary rows, the preserved error strings, and `WireRows`
  order (ties stable, binary last, the stats' own rows not reordered).
- `clientjev`: the exact ten-key state with the `changes` rows (a missing key or row fails), and an
  unreadable numstat returns an error with no request sent.
- `clientcomplete`: a count key with the bundle's number is judged (`files_changed`, both spellings,
  `lines_changed`, `lines_added`/`lines_deleted`, a wordy clause); a wrong number, a cross-key value, a
  bare "2 files" and "fewer than 80 lines" are still refused; the sha-token collision is refused when the
  count is the only candidate fact, and a real cited sha still admits on its own; zero is carried; a
  path-plus-count criterion is not warned.

At this change: `gofmt` clean, `go vet` clean, `go test ./...` passes (31 packages), including the
replay of the measured register (`measuredCriteria` still judged 0.98/0.96/0.96/0.97/0.97 -> accept).
