## The reach inventory re-measured, and the one name-on-PATH reach closed

### What was wrong
Two things, both in the record and one in the code.

**The recorded count did not reproduce.** The entry committed at `ea68236`
(`CHANGELOG/2026-10-15--the-reach-inventory-which-exec-sites-can-reach-an-installed-cli.md`) recorded the
lead's grep as **26 hits, 25 sites**. Re-run at the same sha `892f867` AND at this tip, that grep answers
**17 non-test hits (20 with tests)** — the numbers as written could not be re-derived, which is the one property
an inventory has to have. Two omissions came with it: the `os.Executable()` class was recorded as 4 sites and
has **7** (`clientbridge/commands.go:226` and `clientskills/tree.go:76` were missed), and the sentence "that
grep returns nothing" is one line out — it returns exactly one line, and that line is the COMMENT the same
change introduced (`clientseat/sync.go:266` quotes `exec.LookPath("rig")` while describing it).

**The remainder was real, and it was not a call.** The inventory's answer — no site resolves an installed
client by name — was true of `exec.Command`/`LookPath` CALLS and wrong about one fallback:
`clientwatch/watch.go`'s `selfPath()` returned the bare string `"4genteam"` when `os.Executable()` failed, and
`paneCommand` wrote it into the line sent to a pane (`clear; exec <path> <verb>`). That line is executed by
**the pane's own shell**, which resolves the bare name on PATH — so a pane could start an INSTALLED client
rather than this one, and nothing in the client would know. It is the one reach the inventory named and did not
close. "Close the remainder" is P1's own wording.

### Changed
- `internal/clientwatch/watch.go`:
  - `var executable = os.Executable` — the file's third seam, beside `execCommand` and `lookPath`, because the
    fallback under it is what the pin has to drive.
  - `selfPath() (string, error)`: an unreadable `/proc/self/exe` is now a `CodedError` that names it
    ("this client cannot name its own binary …; a pane runs THIS binary and no other, so nothing was started
    rather than a bare \"4genteam\" for the pane's shell to resolve on PATH, which may be a different
    program"). The name fallback is deleted, not deprecated.
  - `paneCommand(verb) (string, error)`: answers the refusal instead of building a line.
- `internal/clientwatch/grid.go`: all three pane sites (`:178`, `:291`, `:383`) take the command and propagate
  the refusal; the lead-window table now carries the verb and builds the command in the loop, so no line is
  built before the path is known.

### The inventory, re-measured and re-runnable
At this tip, in `agenthub_client`, non-test sites:
`git grep -n "exec\.Command(" HEAD -- internal cmd | grep -v '_test.go'` → **17** (20 with tests);
`exec\.CommandContext(` → **6**; `exec\.LookPath(` → **9 hits = 8 calls** (`agenthub-runtime` ×2, `pgrep` ×2,
`pkill`, `opener`, `herdr`, `tail`) **+ 1 comment**; `os\.Executable(` → **7, all SELF** (this binary by absolute
path, every one of them refusing in words when the read fails); the client's own name (`LookPath("4genteam")` /
`LookPath("rig")`) → **0 calls**. So the answer stands and is now complete: **no site in this client starts an
installed CLI; the only reach that could have was the pane's name fallback, and it is gone.**

### Tested
- `internal/clientwatch/watch_test.go::TestAPaneIsNeverGivenABareNameForItsShellToResolve` (new): with the
  executable read failing, `paneCommand` answers an error naming what could not be read and builds NO command
  line; with it readable, the line is `clear; exec <absolute path> <verb>`.
- `go test ./...` in `agenthub_client`: **29 packages ok, 0 failures**.
- Not verified: the live effect. No pane was restarted and nothing was installed; `os.Executable()` fails only
  where `/proc/self/exe` cannot be read, which this run did not reproduce.
