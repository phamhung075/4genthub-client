## `sync bundle` packs the bundle itself, without `rig bundle create`

### What was wrong
`4genteam sync bundle ROOM SEAT` built its archive by exec-ing `rig bundle create`. A machine without the 4genteam tool could not ship a seat, and the verb exited with "`rig` executable not found".

### Changed
- `internal/clientsync/bundlepack.go` (new): `bundlePack` writes a gzip tar holding `manifest.json` (name, version, notes, and each file's size and sha256), `rig.yaml`, and every regular file under `<rig root>/agents`. The archive is deterministic: sorted entries, zero mtime, mode 0644. Symlinks are skipped, never followed. It is written to a temporary file and renamed into place, so a failed build leaves nothing under the bundle's name.
- `internal/clientsync/bundleverb.go`: the `bundleRunRig` seam, `errBundleRigNotFound` and `bundleExitCode` are removed. The output name (`<room>-<seat>-<hash8>.rigbundle`), the stdout path, the pinned-hash notes and the unpinned-seat warning are unchanged.
- The archive is now this client's own format rather than the previous client's `.rigbundle`. `offline-install` reads the unpacked `rig.yaml` and `agents/<agent>/` directory, which the archive carries; nothing in this repo read the old format.

### Tested
- `bundleverb_test.go`: the exec stub tests are replaced by tests that unpack the archive and check the manifest, the notes, the carried files and their hashes; a determinism test (a changed mtime does not change the bytes); and an unreadable-spec test (exit 1, no bundle left). The warning, no-pin and no-lock tests are kept.
- `go vet ./...` and `go test ./...` from a clean `git archive`.
