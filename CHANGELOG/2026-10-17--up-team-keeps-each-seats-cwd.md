# up --team keeps each seat's working directory

## Fixed

- `internal/clientlifecycle/lifecycle.go`: `up` now folds each seat's cwd from the room's own
  `<root>/rooms/<room>/spec.json` into the definition's spec before `StartRoom`, because `StartRoom`
  WRITES that file from what it is sent and a team definition states a cwd only when someone meant to
  change it. A run that ignored the live file replaced five distinct per-seat cwds with nothing
  (measured 2026-10-17 in the `4genthub-min` room). A cwd the definition declares still wins; a live
  seat absent from the definition, a blank live cwd, or an unreadable/malformed live file carries
  nothing.
- `internal/clientruntime/sendseam.go`: `RoomSpecPath` names `<root>/rooms/<room>/spec.json`, the join
  the client had repeated by hand.
- `internal/clientwatch/seatlog.go`: the model lookup reads the spec through `RoomSpecPath`.

## Tested

- Red first: `TestUpKeepsEachSeatsCwdFromTheRoomsOwnSpec` failed with
  `lead cwd = "<nil>", want "/work/lead"` before the fold, and passes after it; it also pins the
  definition-wins case and the seat with no live entry.
- Live proof: the real `4genthub-min` spec folded against itself with every cwd stripped (the shape a
  definition has) returns all five distinct per-seat cwds unchanged - lead, go-dev, fe-dev, reviewer,
  architect.
- `go test ./...` in the client: pass.
