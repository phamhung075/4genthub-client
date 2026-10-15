## The link source is not empty, and the comment now says when it was

### Fixed

- `internal/clientseat/sendpolicy.go`: the header paragraph that measured the room's link source is now dated and
  scoped. It read "MEASURED 2026-10-15, BEFORE WIRING ... for 4genthub-min that source is EMPTY. The runtime's
  `spec.json` declares no links, the cloud's room record carries none that any client route can read, and the
  room definition has declared none in any revision" - present tense, and by the same evening BOTH halves were
  false: the room definition declares its 23 links (`scripts/team/4genthub-min/team.json`) and the surface does
  read them (`GET /rooms/<room>/seats/<seat>/links` answers 200, the architect's measurement at 05:13Z). The
  paragraph now keeps the pre-wiring measurement as history, states the after-measurement separately, and sends
  the reader to the live room's `spec.json` - the file the runtime reads - rather than to a comment. This is the
  follow-up commit the lead ordered on this branch; the `PROVENANCE` `built:` stamp half of that order was
  settled by the accepted rebuild (u3bin `4genteam 6fdf9fae` / `agenthub-runtime 4ac8af76`).

### Testing

- Comment-only change: `go build ./...` and `go vet ./internal/clientseat/` clean, `gofmt -l` on the file prints
  nothing. The client suite was green on the parent commit (`go test -count=1 ./...`, exit 0) and nothing it
  asserts is touched here.
