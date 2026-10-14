# The rooms meant to run are not erased by a read that failed

Found by sweeping for the class this pod has been closing all day — a failed read reported as a positive fact —
after the two instances in tonight's other entries. This one is worse than a wrong line: three callers
**rewrite** `rigd.json` with what they read, so one unreadable file erased the machine's list of rooms that
are meant to run.

## The measured defect

`clientservices.ReadRigdState()` answered the empty state for BOTH "this machine's rigd has never run" and
"the file is there and could not be read", and its comment said so as a design:

    // ReadRigdState reads the state file. A missing or unreadable file answers the empty state rather than
    // an error: a machine whose rigd has never run has no rooms, and `doctor` must be able to say so.

Three writers read it and wrote it straight back:

| site | what it writes | what one unreadable file did |
|---|---|---|
| `clientroom/room.go` `room stop` | the list minus the stopped room | replaced the list with the empty state: every OTHER room dropped from "meant to run", so rigd stopped restarting them |
| `clientlifecycle/rigd.go` `recordRig` (`up`) | the list plus the new room | recorded the new room into the empty list: the same loss, at the moment a room is being brought up |
| `clientlifecycle/rigd.go` `persist` (per pass) | the list plus fresh safeguard rows | wrote the safeguards with an empty rigs list |

Demonstrated before the fix by the new test, which failed exactly this way: with `not json{ into the file`
written over a two-room list, `room stop` exited 0, reported the room stopped, and left the empty state
behind — the other room gone with it.

## The change

`ReadRigdState()` answers **two** things now — the state, and the error a read that FAILED made:

- a file that is not there → the empty state, **no error** (a machine whose rigd has never run has no rooms,
  and `doctor` must still be able to say so);
- a path the machine cannot resolve, a file that cannot be read, or content that is not parseable → an
  error naming the path. ("Present but empty" and "absent" stay the same answer, because they are the same
  fact: no records; the only distinction a caller needs is whether the read SUCCEEDED.)

Each caller then says what it does with each answer:

- `room stop` — **refuses**, names the room, and stops nothing: dropping it from a list it could not read is
  impossible, and stopping it while its row may still be in that list is what the ordering in `runStop`
  exists to prevent (rigd would bring it back). `ExitRoomRefused`, nothing written.
- `recordRig` (`up`) — **refuses to record** and returns false, naming the room: the same loss, at the same
  moment.
- `persist` (the supervisor's own pass) — **skips the write** and logs the reason; the next pass reads again.
  Nothing else in that pass changes.
- `rigd start` (`rigd.go` child start) — unchanged behaviour (no child is started from a list that could not
  be read) and the reason is now in the log instead of a silent empty list.
- `rigd status` — still prints the rows and the withheld counts (they are measured from pgrep and heartbeat
  files) and now says on stderr that the rigs list is empty BECAUSE the read failed.
- `safeguardRows` (`doctor`, `status --json`, `compact`, `up`'s wait loop) — unchanged: it chooses WHICH rooms
  get rows, and a report verb that neither writes nor acts may go on. Naming the failure there needs an error
  shape in the report seam (`rowsFor`), which is its own unit and is not taken here. **Named remainder, not a
  silent one.**

## Evidence

- Test first: `TestRoomStopRefusesToWriteAListItCouldNotRead` (clientroom) failed on the old code with
  `a stop over a list that could not be read exited 0`, and passes after the fix, asserting three things —
  non-zero exit, the failed read named with the room said **not dropped**, and the unreadable file byte-for-byte
  unchanged.
- `TestRigdStateIsPrivateAndAddsARigOnce` (clientservices) gained the two answers the contract now turns on:
  an unreadable file must return an error, a missing file must not.
- The two existing room-stop tests still pass unchanged, including the deliberate one that drops a room the
  runtime refuses to stop (a valid list is read fine, so the refusal must not leak into it).
- `go build ./...` 0, `go vet ./...` 0, `go test ./...` 25 packages ok, 0 FAIL.
- LIMIT, named: no CLI-level smoke. The seat store resolves from the REAL home (`clientenv.realHome`, not
  `$HOME`) and `room stop` takes no store flag, so a throwaway tree cannot be named from the shell; the suite
  drives the whole verb (`RoomCommand{}.Run`, real flags, a real Unix socket, real file I/O) with the store
  pointed at a temp dir through `RigdStatePath`, which is the seam the fixtures already use.
