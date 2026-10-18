## 7.8's Obligatory table is data, and `doctor --json` carries each row's reasons

Batch 5 of the supervisor-port table, part 1: `0eec1b6` and `4bd832e` onto `01a2837`. The rest of the batch is
not carried here, and the reason each is left out is named below rather than implied.

### Added

- `internal/clientservices`: `Scope`, `Service` and `Obligatory` — 7.8's table as DATA, in the report's own
  order. `Verify` now walks the table instead of repeating the row names, so a service added to it is reported
  without a second edit, and `Env.forRig` is the one channel by which a rig-scoped row learns its rig.
  `RestartBy` names what brings each row back: `agent` for its five children and the bridge, nothing for
  `agent` itself (`up` starts it; its death is reported, not repaired).
- The walk stays RIG-MAJOR: rows for one room together, then the next room's, then the two machine rows. Main's
  order is preserved exactly, which its existing ordering test asserts.

### Fixed

- `doctor --json` printed 7.2's fields alone, so a monitor reading only `--json` saw `orphan_handoffs` with no
  qitem and no destination — while the human report named both. Each row now carries `reasons`: the same lines
  the human surface prints, from the same function, matched on `(safeguard, rig)`. The key is `omitempty`, so a
  healthy row prints byte for byte what it printed before.

### Tests

- `TestTheObligatoryTableAgreesWithTheScopeRule` holds the table against `RigScoped`, the predicate the
  heartbeat file's name and the row's rig are both decided by: two answers to "once per rig" is how a per-rig
  service ends up writing a machine-wide heartbeat.
- `TestDoctorJSONCarriesTheReasonsTheLinesShow`: red without the marshal change (`the orphan row's JSON reasons
  do not name the stranded handoff: ""`), green with it; and a healthy row's reasons stay empty.
- `gofmt -l` empty, `go vet ./...` rc 0, `go test -count=1 ./...` 34 packages ok.

### Left out of this part, and why

- The clone row (`04e7140`) resolves the shared checkout through `clientworktree.MainRoot`, which exists only on
  the gated `4genthub-min/batch-2-worktree` branch (`9584d1f`), not on main. Porting it now would be a second
  resolution of "which checkout are these worktrees in" — the exact disagreement its own comment forbids.
- The hooks row (`73d0be5`), the identity row (`9a530e9`), the access and guide checks (`6161be8`, `b9cdc52`),
  the launch/reseat wiring (`1d8efca`, `63bf909`) and the seats row they feed are the remaining batch-5 slices;
  they are not in this commit and are not claimed.
