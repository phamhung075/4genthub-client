`complete` warns when a criterion measures another base

The gate judges `<--base>..HEAD`, and a criterion that states a different base is refused as
unjudgeable only after the reviewer passed and the work is pushed - the seat then has no act but to
rewrite the criterion. Measured by the lead on 2026-10-19, twice: task 60e2ef5b (criteria named a
cross-repo pair) and task c898d908 (criterion_1 named the branch commit's parent; the same task
closed rc 0 when completed with `--base ca311e4d`). The verb knows `--base` and the task's criteria
before the gate runs, so it now says the mismatch first.

Before the gate call, each acceptance criterion is scanned for 7-40 char hex tokens; a token is a
candidate only when it resolves to a commit in this repository (`git rev-parse --verify --end-of-
options <token>^{commit}` - the full sha, so an abbreviated token compares equal to the canonical
`--base`). A resolved sha that is neither `--base` nor the bundle's `head_sha` (both diff facts the
bundle carries) prints one line per criterion:

    warning: criterion_N names <sha>, --base is <base>; the gate measures <base>..HEAD

The first foreign sha of each criterion is named, resolution is cached per distinct token, and a token
that names no commit (an ordinary word that looks like hex, a sha from another repository) warns about
nothing. Exit codes are unchanged; the lines sit on the same stderr surface as the existing
blind-criterion warning and change no verdict.

Changed files:
- `internal/clientcomplete/gate.go` - `baseMismatchWarnings` (the extraction, the per-token cache and
  the one-line-per-criterion rule) and `commitNamed` (the `git rev-parse` resolver).
- `internal/clientcomplete/complete.go` - the warnings are printed after the task read and before
  `evaluate`, so a mismatched criterion is named before the gate spends a Jev call on it.

Tests: `go test ./internal/clientcomplete/ -count=1` - a criterion naming a foreign commit warns with
the exact line and stays exit 0 while the criterion naming `--base` prints none; criteria naming
`--base` or `head_sha` print none; a hex word that resolves to no commit prints none. Red-first: with
only the two implementation files restored to the parent, the warning test fails on the missing line.
Smoke: the built binary against a stub hub in a three-commit repository printed the one warning line
and exited 0.
