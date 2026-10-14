# `room stop` names what it leaves behind

`4genteam room stop` reports the room's directory as left in place, and the list it printed named `logs/` —
a directory the product does not create and the acceptance's own hygiene deletes as run-state. The same claim
was in the verb's `--help`, so an operator reading either surface was told the layout holds a directory that
is not part of it. A directory named by a report is evidence, and this one was evidence of nothing.

## The change

Drop the word, keep the sentence: the help text, the stop report and the two comments that echo them now say
the room's directory holds `spec.json` and `seats/`. The behavior is untouched — `StopRoom` is still the whole
of the verb, the room's directory is still left in place, and nothing outside the room is touched.

## Evidence

- The report's own test, `TestRoomStopWritesStopRoomAndNothingElse`, asserts the printed line names the
  directory as left in place and names `spec.json`; it passes unchanged, because the sentence it pins is the
  sentence that stayed.
- `go test ./internal/clientroom/` ok, `go vet ./...` exits 0, `gofmt -l` prints nothing.
- Checkable against a real room: `ls ~/.config/4genthub/rooms/<room>/` holds `spec.json`, `seats/`, `run/` and
  — once a queue verb has run — the rail. `logs/` appears only where an instrument or an earlier build put it.
