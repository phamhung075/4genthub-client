# The friction-loop skills land in the client tree

## What was wrong

The seat-friction-loop pack was written, reviewed and fixed in the client repository and then stranded: its
seven drafts sat in `bad2a53`, `b0354e8` and `4160add` on the `architect-skills` lane of one worktree's
gitdir, while `client` `main` at `1284d0a` carried none of them. A seat's skills are resolved from this
tree alone (`share`, then `rooms/<room>`, then `rooms/<room>/<seat>`) and installed by the runtime at every
start, so a skill absent from `main` reaches no seat however well it was reviewed. Measured 2026-10-18: the
six files below were untracked in the retired `agenthub_client.obsolete` checkout and absent from `main`.

## Changed

Six skills recovered at the pack tip `4160add`, byte-identical to their copies there and in the obsolete
checkout except for the one substitution below. The owner-gated `autonomy-charter` stays out, as ruled.

| Level | Path |
|---|---|
| share | `skills/share/friction-report/SKILL.md` |
| share | `skills/share/self-check-while-working/SKILL.md` |
| room | `skills/rooms/4genthub-min/architect-ecosystem-review/SKILL.md` |
| seat (`lead`) | `skills/rooms/4genthub-min/lead/friction-triage/SKILL.md` |
| seat (`architect`) | `skills/rooms/4genthub-min/architect/system-improvement-analysis/SKILL.md` |
| seat (`reviewer`) | `skills/rooms/4genthub-min/reviewer/improvement-review/SKILL.md` |

The one substitution: `friction-report` and `friction-triage` named `openrig` as a feedback layer. The
closed set the client and the server both accept is `runtime, client, cloud, seat-context, workspace,
other` (`internal/clientfeedback/feedback.go`), so both now say `client`.

## Tested

- `go build ./...` rc 0.
- `go test -count=1 ./...` in the client: 35 packages ok, `internal/clientskills` included - that package's
  guard reads every `SKILL.md` in the tree and fails on a local link that does not resolve.
- The resolver places all six on the seats the table names: `4genthub-min` resolved for `lead` carries
  `friction-triage` and not `improvement-review`; for `architect` it carries `system-improvement-analysis`;
  for `reviewer` it carries `improvement-review`; `architect-ecosystem-review`, `friction-report` and
  `self-check-while-working` are in every seat's set.
- Second commit (docs only): `go test -count=1 ./internal/clientskills/ ./internal/clientlifecycle/` ok -
  the first reads every `SKILL.md` in the tree and fails on a link that does not resolve.

## The two stale lines, fixed under the lead's ruling

The lead ruled them in scope, superseding "byte-identical except `openrig` -> `client`"; the second commit
on this branch fixes both, bytes otherwise unchanged:

- `skills/rooms/4genthub-min/architect-ecosystem-review/SKILL.md` reads stuck work from the `parked=` field
  on a `4genteam room status` seat row (`internal/clientroom/park.go`), not the retired `rig parked`.
- `skills/share/self-check-while-working/SKILL.md` names the `F1`-`F5` launch check as landed on client
  `main` (`617c283`, `992d184`) in `internal/clientlifecycle/skillscheck.go`, which `up`, `doctor` and
  `reseat` run - not "implemented on branch `4genthub-min/go-dev` (`63bf909`), not yet landed", a hash that
  resolves in no repository.

As found before the ruling, for the record: the live check landed with `up` reading what the runtime
installed and `doctor` reporting it as the seats row (`internal/clientlifecycle/skillscheck.go`,
`runUp`'s `roomSkillsVerdict`, `lifecycle.go`).

## Not in this change

- No pin bump: the hub's client pin is the lead's.
- No push: local commit only.
- `comm-guard-skill`, `queue-handoff`, `specification-system` and `context-engineering` remain drafts; they
  are outside this recovery's six, and the first two are cited by `friction-report`'s boundaries.
