# The runtime's flag leaves every client verb

`--state-dir` is the runtime binary's own flag (`agenthub-runtime --state-dir DIR`). On `4genteam` it was a
second spelling for the client's one directory, still living on `up`, every `room` sub-verb and `heal` after
C7 gave the other eight root-taking entries `--state-root`. One directory, two names, and two programs
sharing one of them: a script written against `ps` failed on `room status`, and a `--state-dir` typed into
`up` silently wrote a room to a tree the rest of the run had not been told about.

### Changed

- **`--state-root DIR` and `--state-root=DIR` are the client's one spelling**, everywhere a root is read:
  `up`, `room` (every sub-verb, including the four that parse their own flags - `queue`, `report`, `metrics`,
  `limits`), and `heal` joined the eight that already spoke it. The runtime's flag did not move: `up` still
  spawns `agenthub-runtime --state-dir <root> --socket <sock> daemon`.
- **One parser, no verb's flag set names the root.** `clientenv.StateRootFromFlags` is the only reader, and
  the verbs that still declared the flag themselves (`compact-run`, `idle-run`, `budget-run`, `watch` and
  its four siblings, `policy render`) now call it instead. So "taken wherever it sits - including after the
  positionals", "the LAST occurrence wins" and `--state-root=DIR` hold for every verb alike, and a root is
  never half-read.
- **The retired spelling is refused, not ignored.** `--state-dir` and `--state-dir=DIR` return one typed
  refusal - `--state-dir is the runtime's own flag, not the client's: pass --state-root DIR` - printed once
  and exit **2**, not the room group's 3 and not unavailable: the flag is a word to change. It is refused
  before any side effect, so `up` starts no daemon, `room` answers nothing and `heal` fixes nothing.
- No alias, no deprecation window, no compatibility reader.

### Testing

- `go test ./...` in the client: green.
- New tests pin the behaviour, not its source: `clientenv` (taken wherever it sits, last wins, both refusal
  forms, the typed code); `up`/`idle-run`/`budget-run`/`compact-run` (refusal before any lifecycle seam
  runs); every `room` sub-verb, every `watch` verb, `policy render`, `heal`, `ps`, `continue`, `watchdog`
  (exit 2, the sentence, nothing on stdout); and the daemon spawn's argv still carrying `--state-dir`.
- RED first: with the refusal branch deleted from `StateRootFromFlags`, the refusal tests fail (`up` exited 3
  with "unknown argument"); restored, they pass.
- Measured through the built binary: all thirteen converted invocations exit 2 with the sentence;
  `room status room-a --state-root /a --state-root /tmp/b --socket /tmp/nope.sock` prints
  `room: room-a@/tmp/b` (last wins, flag after the positional) and then the socket's own refusal at 3.

### What this does not do

- No rename of the runtime's flag, and no change to the `state dir:` line `up` prints - it labels the
  directory, the flag is what was unified.
- Historical records that quote `--state-dir` in harness calls (the hub's dated gate records, the older
  `CHANGELOG.md` entries) are records, not instructions, and are left as they are.

### Landing

- The lane was cut from `1e0d6f0`, before the log-home commit that gave `startAuthority` the state dir; the
  merge into main is textually clean but leaves four `StateRootFromFlags` call sites in
  `internal/clientlifecycle` (doctor, agent, status, log) written against the two-value shape. They now take
  the parser's error and answer it with the verb's own sentence, and the lane's `rootflag_test.go` hook
  literal takes the state dir. `go build ./...`, `go vet`, `gofmt -l` clean, `go test ./...` 32 packages rc 0
  at the merged tip.
