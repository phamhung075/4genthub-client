# A landing counts only for the skill whose tag it carries

The census counted every row the room closed inside a skill's window (`Evidence.Landed`), so `promotable` was
granted on other skills' work: a provisional skill that had served nothing was promoted because the room was busy.
The reviewer's verdict on `7c2c7d4e` measured it - the old count answers 3 where the skill's own evidence answers
1, 1, 0, 0.

A landing is a row, and the only fact a row carries is its tags, so a landing now counts for a skill when the row
carries one of **that skill's own** declared `row-tag:` values. `Evidence.LandedFor(signals, since)` replaces
`Landed(since)`; a skill that declares no `row-tag:` counts zero, because it has no landing of its own and the
promotion the census grants on landings is not for it. `applyWindow` passes the row's signals.

Tests (`internal/clientskills/windows_test.go`): `TestALandingCountsOnlyForTheSkillWhoseTagsItCarries` is the
reviewer's four-skill case - `row-tag: defect` and `row-tag: guard` each claim their one closed row, a guard-only
skill and a skill whose declared tag no closed row carries both count zero. `TestTheWindowsDemoteAndPromote`'s
fixture rows now carry the tag the promoted skill declares; its 3 is unchanged, and still excludes the two
landings recorded before its window opened.

Measured: `go test -count=1 ./internal/clientskills/` passes, and both cases run by name
(`-run 'TestALandingCountsOnly|TestTheWindowsDemoteAndPromote'`: PASS, PASS).
