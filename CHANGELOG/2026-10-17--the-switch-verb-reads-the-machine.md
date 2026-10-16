# The switch verb reads the machine

`4genteam sync switch ROOM SEAT --runtime R` decided whether a request was a runtime change by comparing
`R` with the CLOUD's occupant record alone (`internal/clientsync/switchverb.go`). The cloud carries the
occupant of record; the seat's own runtime is what a restart moves. While a switch is half done the two
disagree, and the decision then goes the wrong way.

### Fixed

- **A runtime change the cloud already records is still a runtime change.** Measured 2026-10-16 22:47Z on the
  reviewer seat: the cloud already said `agy` while the seat ran `omp`, so `--runtime agy --model
  gemini-3.8-flash-low --apply restart` read as a model-only change, took the restart path, and restarted the
  seat into a runtime nobody had asked it to run. The seat died before readiness (`agent_exited`, written
  after that launch) and nothing put it back; the same command class one minute later, run the other way,
  refused with `applied=manual`. One class of change, two behaviours, and one of them killed a seat.
- The verb now reads the RUNTIME's own room status (`clientruntime.GetRoomStatus`, through the `switchRoomSeats`
  seam) for the seat, whenever `--runtime` is given and the cloud already records it, and treats the request
  as a runtime change when the runtime the seat is ON differs from the one requested. The cloud record can
  therefore only ADD a runtime change, never remove one, and a runtime change goes back to printing the
  manual notice and touching no seat - the behaviour the file's own doctrine already claimed.
- A read that names no runtime (no daemon answering, or a room that does not list the seat) changes no
  decision: the apply step reports what it could not read, exactly as before.

### Testing

- `go test ./...` in the client: green. `internal/clientsync` gained
  `TestSwitchRuntimeTheCloudAlreadyRecordsStillSkipsTheSeat` (the incident: the cloud says `agy`, the runtime
  says `omp`, `--apply restart` - no runtime call is made and the answer is `applied=manual`) and
  `TestSwitchRestartStillRunsOnTheRuntimeTheMachineNames` (the boundary: the runtime names the requested
  runtime, so `--apply restart` still sets the model and restarts fresh, as it always did).
- The incident test FAILS on the pre-fix code with the measured shape of the defect: the stub records
  `set-model` then `restart-fresh` and the verb answers `applied=restart`.
- The changed surface, run against a client built from this tree: `4genteam sync switch 4genthub-min reviewer
  --runtime omp --apply none` (the reviewer's own runtime, so no change to record) exits 0 and prints
  `applied=none`, and the machine read behind it answers from the live daemon.

### What this does not do

- It does not make a runtime change appliable: the verb still prints the manual notice and touches no seat,
  because whether a restart carries a changed runtime is not established here. The manual notice's text is
  unchanged.
- It does not roll a seat back. A restart that kills the seat for some other reason - a broken runtime, a
  model that cannot start - is still the runtime's own supervision to repair; this change removes the path
  that requested such a restart on a runtime change, not the failure mode itself.
- It does not repair the registry entry the failed attempt left for one cycle (`4genteam ps` naming `agy`
  while the room read `omp`, repaired by the seat's own relaunch).
