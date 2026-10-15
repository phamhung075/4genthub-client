# The printed rig procedure is gone from the help

C3, the unit after the rig-exec count was re-measured at the tip. The count and what it says about this tree
are in the report to the lead; this entry is the one thing in the unit that changed a line a reader sees.

## What was wrong

`4genteam sync --help` closed with the ported Python epilog: *offline use is an explicit operator choice: after
`bundle` produces a `.rigbundle`, run it yourself with `rig up <bundle> --target <dir>`.* Two facts make that
line a defect rather than a stale noun:

1. `rig` is not this client's tool. The symlink that made `rig` reach `4genteam` is gone (`8193820`), and what
   resolves on this box is the 4genteam node CLI in nvm's bin — a program this stack is retiring.
2. Bare `rig up` is the launch the client's own skills forbid, because it bypasses `rigd`, the compaction
   supervisor, the watchdog and the bridge.

C3's rule for a printed line is that it RUNS or it is ABSENT. This one could not run as written in the client's
own vocabulary, and it named a program the client has removed. There is also no native verb that STARTS a room
from a bundle, so repointing was not available: `sync offline-install TARGET` puts the bundle's PINNED POLICY
into the seat store, which is a different act, and claiming otherwise would be the same false equivalence as
naming a verb that is not there.

## The change

- `internal/clientsync/sync.go` — the paragraph names the verb this client has (`4genteam sync offline-install
  <TARGET>`) and says the loss in words: the client never falls back to a bundle automatically, and it has no
  verb that starts a room from one. The `rig up` procedure is deleted rather than repaired.
- `internal/clientsync/sync_test.go` — `TestHelpStatesOfflineUseIsManual` keeps the row's name and its two
  sentences, and gains the assertion with teeth: `sync --help` prints no `rig up` line at all. That is the half
  that fails if the dead procedure comes back.
- `internal/clientsync/respawnverb.go` — the two remaining places that named the retired program in *text*
  rather than as a statement of what this verb does not do: the usage line's "died outside `rig seat stop`" and
  the `--reason` default, which is the string an audit reason would carry. Both now say "the runtime's own
  stop", which is the fact the verb's own `RunRespawnVerb` doc already used. The usage's statement that the verb
  "never falls back to `rig`" stays: that sentence is true and is the C3 shape.

## Evidence

- `go build ./...` 0, `go vet ./...` 0, `go test ./...` 25 packages ok, 0 FAIL, in a clean checkout of my own
  worktree at `4567c37` plus these three edits.
- The new assertion is load-bearing: with the old `rig up <bundle> --target <dir>` sentence put back in
  `printUsage`, `TestHelpStatesOfflineUseIsManual` FAILS on the absence check; restored byte-identically
  (md5 compared) afterwards.
- The replacement line RUNS, verified on the built binary with no `rig` resolvable on `PATH`:
  `4genteam sync offline-install --help` exits 0 and prints its usage.
