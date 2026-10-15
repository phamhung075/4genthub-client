# The socket is stated, not guessed

A seat's own `4genteam send` and `4genteam room queue` reach a daemon through `$AGENTHUB_RUNTIME_SOCKET`, and
then through this machine's own path. Measured on the isolated run in flight (2026-10-14): two daemons were
listening — the run's own, started with `--socket /tmp/u3run4/rt/agenthub.sock`, and the box's live one at
`/mnt/wslg/runtime-dir/agenthub.sock` — the run's daemon **and** its own seat carried
`XDG_RUNTIME_DIR=/mnt/wslg/runtime-dir` and **no** `AGENTHUB_RUNTIME_SOCKET`, because the run's socket was
passed as a *flag* to the daemon and never exported into the seats it launches. A seat of the run therefore
addressed the LIVE runtime, which refused it for a mailbox under the live state directory
(`/home/daihu/.config/4genthub/rooms/cutover-lab/run/dev.sock`) — a message that had nothing to do with that
runtime. The same measurement showed the second half: `4genteam send` accepted no `--socket`/`--state-dir`, so
an operator had no way to aim it at the run's daemon either.

## The change

**The daemon states its own socket, and it is resolved before the supervisor exists** (`main.rs`), because the
supervisor hands it to every seat it starts. The seat's environment is now a pure function, `seat_env`, whose
contract is exactly what a seat's own client reads: `AGENTHUB_ROOM`, `AGENTHUB_SEAT`, `AGENTHUB_SESSION_NAME`,
`PI_CODING_AGENT_DIR` — and `AGENTHUB_RUNTIME_SOCKET`.

**A socket recorded in the room's spec does not win.** The socket that matters to a seat is the one the daemon
that *started* it answers on; a value written into a spec at `up` time can only be the socket of whichever
daemon existed then, which is the stale half of the same defect. So the client's `up` does **not** record the
socket in the room's seat env: one fact in two places is a fact that can disagree with itself, and the daemon
that is answering is the only authority on where it answers.

**`send` takes `--socket PATH`, and the flag aims the WHOLE verb.** The verb makes two requests — the room's
record, where the seat's session name is READ rather than composed, and the frame it writes — and aiming only
the second would pair a name read from one runtime with a message written to another. `ResolveSeatAddressOn`
and `RuntimeSendTextOn` carry the socket through both, and the refusal names the socket that answered, so an
operator who pointed the verb elsewhere reads which daemon it was.

`--state-dir` is deliberately **not** added. Nothing in this verb's path reads the state root: the record is a
socket read, and the mailbox is the daemon's own path (that is why the wrong daemon's refusal named a mailbox
under *its* state directory). Accepting a flag that changes nothing would be a printed option that lies about
what it does.

## Evidence

**Two live daemons, one client, four legs** (my build of the client and of the runtime; daemons `a` and `b` on
their own sockets, each with a room `smoke`; `AGENTHUB_SESSION_NAME=agenthub--smoke--dev`):

| leg | answer |
| --- | --- |
| `$AGENTHUB_RUNTIME_SOCKET=a`, no flag | `the runtime at /tmp/r6sock/a/rt/agenthub.sock refused it: no seat named …` |
| `$AGENTHUB_RUNTIME_SOCKET=a`, `--socket b` | `the runtime at /tmp/r6sock/b/rt/agenthub.sock refused it: …` |
| `$AGENTHUB_RUNTIME_SOCKET=b`, no flag | `the runtime at /tmp/r6sock/b/rt/agenthub.sock refused it: …` |
| unset, no flag | `the runtime at /mnt/wslg/runtime-dir/agenthub.sock refused it: no seat named … (4genthub-min, cutover-lab, …)` |

The last row is the defect's own shape: with nothing stated, the send lands on the box's live runtime; the
second row is the new capability, and the socket named in each answer is the daemon that actually served it.

**A seat really started by the daemon carries it.** `StartRoom` over the daemon's own socket with a seat whose
*runtime* is a one-line command that records its environment, then read back from the pane's own file:
`AGENTHUB_RUNTIME_SOCKET=/tmp/r6env/rt/agenthub.sock` (the daemon's socket, exactly), `AGENTHUB_ROOM=envroom`,
`AGENTHUB_SEAT=dev`, `AGENTHUB_SESSION_NAME=agenthub--envroom--dev`.

**The tests.** Go: `TestSendSocketAimsTheVerbAtThatDaemon` starts TWO fake daemons, points the environment at
one and the flag at the other, and asserts the aimed daemon saw the record and then the send while the
environment's own channel stayed **empty** — a leak to it cannot be mistaken for a connection error, because it
answers every well-formed request. `TestTheRoomRecordAnswersBothWays` now also pins that the record is read
from the socket the caller named. Rust: `a_seats_env_states_the_socket_of_the_daemon_that_started_it` pins the
env map, including that a spec's own `AGENTHUB_RUNTIME_SOCKET` is overridden.

**Suites:** `go vet ./...` 0, `go test ./...` 26 packages ok 0 FAIL, `gofmt -l` clean, `cargo test --offline`
90 passed 0 failed. The `send --help` text documents `--socket` beside `--raw`/`--json`.

- Not verified here: the acceptance's own seat-level leg (a seat of an isolated run messaging through its own
  daemon end to end), which needs a room of real seats and the lead's instrument.
