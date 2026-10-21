# A skill carries its own evidence record, and `skills evidence` prints a verdict

## Changed

- `internal/clientskills/record.go` (new): the `record.json` a skill carries beside its `SKILL.md` -
  `state` (`provisional` | `trusted`), `since` (the commit that set the state), `change` (`fix` |
  `derived` | `captured`), `evidence` (queue ids and commits) and a one-line `risk`. `ReadRecord` treats
  an ABSENT file as `provisional` with no `since` and refuses a present one it cannot parse or validate
  (`DisallowUnknownFields`, so a misspelled key is a refusal rather than a decision that silently did not
  happen). `WriteRecord` refuses a directory holding no `SKILL.md`. Decisions only: there are no counters,
  because nothing in this client observes which skill a model loaded.
- `internal/clientskills/signals.go` (new): the front matter's `signals:` list - `guard:`, `send:`,
  `jev:`, `row-tag:`, `command:` - parsed from `SKILL.md` and sorted by kind then value. The vocabulary is
  closed (an unknown kind is an error, an entry declaring two kinds is an error, a blank value is an
  error); no front matter or no `signals:` key returns nil and is a finding, not a parse failure.
- `internal/clientskills/census.go` (new): `Tree.RoomSkills` lists every skill a room delivers - `share/`,
  the room's own, and each seat folder's, with `Level` naming which (two levels holding one name are two
  rows; the collision refusal stays where it belongs, in `Tree.Resolve` for one seat's install).
  `Tree.Census` decides S1's three verdicts: `hold`, `no-signal` (no declared signals), `broken-record`
  (a record that cannot be read, or an evidence id that does not resolve). A resolution carries `Checked`,
  so an id the checker could not look at - queue down, tree outside git - is NOT reported broken.
- `internal/clientskills/command.go` (new) plus one registration in `cmd/4genteam/main.go`:
  `4genteam skills evidence [ROOM] [--skill NAME] [--skills-root DIR] [--json]`. Read-only; exit 0
  whatever the verdicts. The tree is `--skills-root`, else `$AGENTHUB_CLIENT_SKILLS`, else `./skills` or
  `./agenthub_client/skills` - a candidate counts only when it holds `share/`.

## Tested

- `go test ./... -count=1` in `agenthub_client` -> ok (whole module).
- `TestCensusDecidesTheThreeVerdicts` - one case per verdict over a fixture tree: a `trusted` record with
  a resolving queue id is `hold`, a skill declaring nothing is `no-signal`, and a record citing an id the
  resolver checked and did not find is `broken-record` with the id named.
- `TestAnUncheckedEvidenceIDIsNotBroken` pins the distinction the design needs: an id the resolver could
  not check leaves the verdict `hold` and is not listed as broken.
- `TestAnUnreadableRecordIsBrokenWithItsFault`, `TestAMalformedOrInvalidRecordIsRefused` (five invalid
  shapes), `TestAnAbsentRecordIsProvisionalAndAWrittenOneRoundTrips`,
  `TestWriteRecordRefusesADirectoryThatIsNotASkill`, `TestSignalsAreParsedFromTheFrontMatter`,
  `TestSignalParsingAcceptsSilenceAndRefusesUnknownKinds`, `TestRoomSkillsListsShareRoomAndEverySeat`.
- Smoke: `4genteam skills evidence 4genthub-min` on this tree prints `skills: 38 hold: 0 no-signal: 38
  broken-record: 0 trusted: 0` and exits 0 - 22 share, 1 room-level and 15 seat skills, every one
  `no-signal` because no `SKILL.md` declares a `signals:` list yet. The design note measured 32 skills
  before the room and seat skills landed; 38 is what the tree holds today.
