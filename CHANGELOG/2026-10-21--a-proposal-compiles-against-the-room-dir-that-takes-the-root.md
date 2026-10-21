# A proposal compiles against the RoomDir that takes the root

`git merge-tree` said the skills work and the RoomDir change merge cleanly, and the merged tree did not
build: `internal/clientskills/propose.go:62: not enough arguments in call to clientservices.RoomDir` (the S3
gate's verdict measured it as `merge+RoomDir build=FAIL`). A textual merge is not a compile.

The call now passes the state root the verb had already resolved, which is the rule the RoomDir change
exists to enforce: one verb must not derive a room under one store and read another.

Verification: on `4genthub-min/skills-final` (the merge of `skills-doctor-row` and `room-dir-root`) `go build
./...` is clean and `go test ./...` is green across every package; `skillscheck.go` already took the
root-qualified call, so the proposal was the only stale caller.
