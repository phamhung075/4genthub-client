# A set-but-empty marker is still a seat

### Fixed
- `internal/clientroom/limits.go`: the ceiling marker is read as a PAIR (`os.LookupEnv`) instead of
  `os.Getenv(...) != ""`, because the shell hands a seat `AGENTHUB_SESSION_NAME= 4genteam room limits <room> --usd
  999` for three characters and `Getenv` cannot tell an empty marker from an absent one. The OWNER is the caller
  with NO marker; a marker that is SET counts as a seat whatever it holds, and the refusal names the state it read
  ("a seat whose $AGENTHUB_SESSION_NAME is empty"). Measured with the real binary at d964b8b: the empty spelling
  WROTE the raise (999, exit 0, the ceilings 25 -> 999); it now exits 3 and leaves the file alone, while the owner
  (no marker) still raises. The usage and the file header both named the empty marker as a way past - they now say
  what the code does, and what still gets past is ABSENCE: `env --unset=AGENTHUB_SESSION_NAME`, a variable holding
  the name, or a program that cleans its own environment and execs the verb.
- `internal/clientroom/limits_test.go`: `TestAnEmptyMarkerIsStillASeat` pins the three raises (`--usd 999`,
  `--calls 301`, `--off`) as refusals that name the marker, print nothing, and leave the file at 25/300, and pins a
  seat LOWERING a ceiling through the same marker as allowed - the rule is about a raise, not about the marker. The
  marker is pinned in both blank spellings, EMPTY and WHITESPACE-ONLY, because the check trimmed before comparing:
  `AGENTHUB_SESSION_NAME='   '` was read as the owner for the same reason the empty one was, and measured with the
  binaries, before, it wrote 999 (exit 0). The tests that mean "the owner" now UNSET the marker (`asOwner`) rather
  than emptying it: emptying it is no longer the owner's spelling, and it never was - it was the hole.

### Testing
- RED before the fix: `go test -count=1 ./internal/clientroom/` failed only on the new case - "an empty marker
  asking [--usd 999]: exit 0, want 3", the run printing `per seat: 999.00 USD/24h` and leaving 999 in the file.
- GREEN after: `go test -count=1 ./internal/clientroom/` ok; `go test -count=1 ./...` = 32 packages ok.
- Both binaries, one state dir each, the same callers: BEFORE, the empty marker wrote 999 (exit 0) where the set
  marker exited 3, and the WHITESPACE-ONLY marker wrote 999 too; AFTER, both blank spellings exit 3 with the
  ceilings unchanged at 25 and the owner's own raise still writes 999.
