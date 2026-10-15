## The checker build bakes nothing in

### What was wrong
`internal/clientsync/installcheckerverb.go` still built the checker with
`-ldflags -X main.installedPinsDir=<store>`, and `README.md` still promised that "each installed checker
carries the pins directory of the seat store it was built for". The variable went with the checker's `send`
verb, and `-X main.<var>` is SILENT when the variable is absent, so a build that exits 0 was never evidence
that the flag did anything: `git grep -n 'installedPins'` finds no declaration and no reader in `cmd/` or
`internal/`. Its only substantive reader was the pinned-policy load the deleted verb performed; the surviving
path builds the sending seat's policy at send time from the room's declared links (`internal/clientseat`,
`authorizeSend`).

### Changed
- `internal/clientsync/installcheckerverb.go`: the command is `go build -o <binary> ./cmd/seatcheck` — the
  `-ldflags` element is gone, and every other element and the flag order are unchanged. The usage text now
  says what the build does: nothing is baked in, so one build serves every seat.
- `README.md` (~:82): the install section's build-input sentence records that nothing is baked into the
  checker, and that the ldflag is gone with the `send` verb that was its only reader.
- `internal/clientsync/installchecker_test.go`: `TestInstallCheckerBuildsAndLinksOnPath`. The fake compiler
  reads the output path from `asked[3]` (was `asked[5]`) and the expected argv loses those two elements. The
  assertion is still an EXACT argv comparison (`strings.Join` equality), not a substring.
- `CHANGELOG/2026-10-15--the-comments-name-the-path-that-owns-the-decision.md` flagged this as "NOT fixed".
  That entry is left as the dated record of the state it measured; this one is the fix.

### Verified
- `gofmt -l cmd internal` empty · `go build ./...` rc 0 · `go test ./...` rc 0 (30 packages ok).
- The REAL verb, twice, in a mount namespace whose `/root` is a scratch directory (so the link lands under
  `/tmp`, never in the operator's `~/.local/bin`, whose sha256 is identical before and after every run):
  `go run ./cmd/4genteam sync install-checker --go-dir <checkout> --out <store>` -> `checker:<store>/bin/seatcheck`,
  rc 0 — once with `<store>/4genthub-min/go-dev/{policy.json,pinned.json}` present and once with an empty
  store. The name resolves to the freshly built binary, and that binary answers `--help` rc 0, `send --to x
  --intent task -- hi` rc 2 with `unknown subcommand "send"` naming `4genteam send`, and `audit-scan` rc 4 on
  a bypass line / rc 0 on a clean one.
- Remaining `installedPins` hits after the sweep: `CHANGELOG.md` 1996 and 2404 (dated records) and the
  previous entry's own text (dated record) — no live code, and the README hit now says the flag is GONE.
