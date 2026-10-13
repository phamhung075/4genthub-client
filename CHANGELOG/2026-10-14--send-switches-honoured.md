## The switches stop riding along: a daemon-only flag decides the transport, and the refusal names the party that stopped the send (U3, gate fix)

### What was wrong

The U3 gate at `c55fa87` returned REQUEST CHANGES on one claim, and it was the claim that matters most for a
person: `send RIG SEAT MESSAGE` — the client's OWN shape — never consulted the daemon-only switch list. It built
its text and called `SendTextToSeat` directly, so every switch on that line was parsed and then dropped. Forced in
the gate, with a live runtime and a dead `OPENRIG_URL`, one flag at a time:

```
$ 4genteam send lab dev "gate text" --wait-for-idle 5        # types immediately; does not wait
$ 4genteam send lab dev "gate text" --verify                 # reports no verification
$ 4genteam send lab dev "gate text" --dangerously-interact   # types as an ordinary send; --reason unrecorded
rc=0  "Sent to lab/dev"  wire: SendText session=agenthub--lab--dev text='From owner session: gate text'
```

`--wait-for-idle` silently not waiting is the worst of the three: it is the switch a caller passes precisely to
avoid typing into a busy seat.

### What it is now

One rule in both shapes: **a switch the transport cannot honour goes to the transport that can, and the send
stops when neither can.** The RIG SEAT shape consults its own list — `seatShapeSwitches`, the daemon-only four
plus `--raw` and `--json`, which the runtime path honours for a SESSION but this shape's seat send cannot, since
`SendTextToSeat` takes no options at all — and routes to the daemon at the seat's own session name
(`agenthub--RIG--SEAT`) whenever one is present. Measured on the built binary, against a recording daemon and a
live runtime socket:

| line | runtime written? | what the daemon received |
|---|---|---|
| `--wait-for-idle 5` | no | `session=agenthub--lab--dev waitForIdleMs=5000` |
| `--verify` | no | `verify=true`, and the verdict rendered |
| `--dangerously-interact --reason probe` | no | `dangerouslyInteract=true reason=probe` |
| `--raw` | no | `text="gate text"` exactly, with no `From NAME:` prefix |
| `--json` | no | the daemon's own document on stdout |
| no switch | yes (`SendText`) | not asked at all |

`--raw` is the one switch that also changes the TEXT, because raw means the text exactly.

### And the refusal stopped blaming a healthy transport (gate finding 2)

A daemon-only switch with a HEALTHY runtime and a dead daemon used to print

```
4genteam send: no runtime is answering at /tmp/u3gate/rt.sock (set OPENRIG_RUNTIME_SOCKET, ...);
the OpenRig fallback at http://127.0.0.1:1 is not available either; nothing was sent
```

— whose first clause is false, and which aims the reader at a transport that answered. The two reasons a send
reaches the daemon need different sentences, so the refusal is now chosen by the reason:
`DaemonSwitchMissing` names the flag when a switch forced it, `FallbackMissing` keeps saying the runtime is
missing when the runtime really was the transport that failed. Measured, healthy runtime + dead daemon + `--verify`:

```
rc=3  4genteam send: --verify needs the OpenRig daemon, and the OpenRig fallback at http://127.0.0.1:1
is not available either; nothing was sent
```

### Evidence

- `internal/clientseat/send_transport_test.go` gains three tests and all three FAIL on the pre-change tree
  (`git archive HEAD`), where the wire shows the switches dropped and 0 daemon calls — the gate's observation,
  reproduced as an assertion rather than answered by argument.
- `internal/clientruntime/sendseam.go` is new, and deliberately small: the seat-session name both transports
  address, and that one refusal sentence. Two other writers held `transport.go` and `ipc.go` while it was written;
  those two files each build the same session name inline today and should call this one when they are free.
- `go vet ./...` clean; `go test ./... -count=1` green.
