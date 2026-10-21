# The seat guard stays the owner's, redacts what a seat reads, and runs for claude-code seats

Three owner commits on `owner/seat-guard-rev9`, on top of `d5249fc0` (the revision-9 suite):

### Changed

- **Revision 9 without the lead exemption** (`94444d52`, task 1c06bcd0). The rev-9 patch
  (`seat-guard-rev9-from-main.patch`, sha256 `806536475a20`) lands, except `assetOwner = pushAuthority`:
  `assets/seat-guard.ts` stays refused to every seat, the lead included (owner ruling 2026-10-21). The suite's
  lead-asset rows now assert the refusal for `lead` and `go-dev` alike.

### Added

- **Tool-result redaction and two refusals** (`b04121d1`, row 58e4dde11e9a). The omp `tool_result` hook returns
  `{content}` with `redactSecrets` applied: every value the seat's environment carries under a
  TOKEN/KEY/SECRET/PASSWORD name, and JWT, `sk-`, Bearer and `NAME=value` shapes. The measured leak was a value
  printed without its name (the CapRover triggerbuild token through `gh api .../hooks`). New rules: `env-dump`
  (`printenv`, bare `env`, `export|declare|typeset -p`, bare `set`) and `gh-api-secret` (`gh api` of hooks,
  secrets, keys, tokens, installations). agy's post payload has no result content, so agy gets the refusals only.
- **A `--claude-hook pre|post` mode** (`c9dc09c4`, row 0fe6369fc14a). claude's tool names map to the omp names the
  rules read; the answer is `hookSpecificOutput` allow/deny. `GUARD_HAS_CLAUDE_HOOK` flips to `true`, so the
  supervisor writes `<seat>/settings.json` and the runner passes `--settings`. codex still has no hook.

### Fixed

- **Quoted words are paths, and a patch path list is a document** (patch sha256 `2c06477d6031`, rows
  `qitem-20261021175414-01510c9cbd55`/`qitem-20261021181739-9da6703153f7`). One scanner (`scan`) replaces the regex
  that blanked every quoted span before the extractors ran, so a quoted target is judged as the path it names
  (`cp evil.ts "rust/agenthub-runtime/assets/seat-guard.ts"` is `protected-paths`) while the rules that judge what is
  RUN still see the erasure's own spelling. A word with no literal prefix is refused by name
  (`unresolvable-target`) instead of being dropped, and `--pathspec-from-file` in any spelling on any verb is
  `pathspec-from-file`: the file names a set the guard never sees. The `cd` the extractor resolves a relative target
  against is now read from the scanner's word, so a quoted `cd` no longer hides the directory the next statement
  writes in (`cd "rust/agenthub-runtime" && rm -r assets` is `protected-paths`; `git -C "<tree>"` is read the same
  way).
- `tests/seat_guard.rs` is rustfmt-clean, and one `clippy::single_element_loop` from the rev-9 suite is gone; CI's
  `cargo fmt --check` and `cargo clippy -D warnings` refused both at `d5249fc0`.

## Verification

At `c9dc09c4` in `rust/agenthub-runtime`: `cargo fmt --check` clean, `cargo clippy --offline --all-targets -D
warnings` clean, `cargo test --offline` 194 + 1 + 35 + 1 passed, 0 failed. `go test ./...` in the client: all ok.
Final asset sha256 `7e122de891f3`, 1267 lines. On top of it the quoting/pathspec patch
(`PATCH-2026-10-21-guard-quoting-pathspec.diff`, sha256 `2c06477d6031`) rebuilds the asset to 1432 lines, sha256
`52b88b01a899`: `cargo test --offline` 194 + 1 + 40 + 1 passed, `cargo fmt --check` and `cargo clippy --offline
--all-targets -D warnings` clean.

## After the gate

The lead fast-forwards client main, bumps the hub pin, rebuilds and reseats; the new guard is written into every
seat dir at start. The credentials already in transcripts stay leaked until the owner rotates them.
