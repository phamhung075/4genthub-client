# The client's skills-delivery half leaves, and the link guard stays

Cut commit 1 (`rigverb.go`, the rigspec verbs) deleted the only caller of `internal/clientskills`, and
nothing replaced it: `go list -f '{{.ImportPath}} -> {{join .Imports " "}}' ./...` at the tip lists the
package as importing `clientcmd`, `yaml.v3` and the standard library, and no path in the module imports
it. So the module still carried the client's whole skills-delivery half - the tree locator, the copy
into a seat's skills path, the `agent.yaml` declaration - as 769 lines of production code plus 570 lines
of its tests, unreachable, in a cut whose stated subject is that the client no longer delivers skills.
Its refusal text was still the source of a README failure-map row (removed in the previous commit), and
its `--skills-root` flag is registered nowhere: `git grep -n '"skills-root"' -- '*.go'` matches only that
package's own error strings.

`internal/clientskills/links_test.go` is NOT dead with it. It is the guard the reviewer's 2026-10-10
finding produced (ten dead links in `skills/share/agent-browser/SKILL.md`), it uses nothing but the
standard library, and the tree it judges - `skills/` - is still shipped and still installed by the
runtime. It is kept, and its package gains the doc comment that says which half went and why.

## Changed

- **DELETED `internal/clientskills/tree.go`** (`Tree`, `Locate`, `Resolve`, `RootEnv`, `SkillsDir` and the
  package doc), **`internal/clientskills/install.go`** (`Install`, `Declare`), and their tests
  **`internal/clientskills/tree_test.go`** and **`internal/clientskills/install_test.go`**.
- **`internal/clientskills/links_test.go`**: the package doc comment now states that the delivery half is
  gone, that the runtime installs a seat's skills itself (`install_seat_skills`,
  `rust/agenthub-runtime/src/supervisor/mod.rs:258`), and that this guard is kept because the tree it
  judges is still shipped. Its check is otherwise untouched.

## Verified

- `go build ./...` rc 0, `go vet ./...` rc 0, `go test -count=1 ./...` rc 0 - 30 packages `ok`, including
  `internal/clientskills` itself (`0.007s`), so the guard still runs and still passes.
- The deletions are measured, not assumed: nothing outside the package referenced `Install`, `Declare`,
  `Tree`, `Locate` or `SkillsDir` at the tip (`git grep` over `*.go`, and `go list` over every package's
  imports), and the two shipped-skill citations of `internal/clientskills/links_test.go`
  (`skills/share/agent-browser/SKILL.md:20`, `VENDORING.md:25`) still name a file that exists.

## Not in this change

- **`CHANGELOG.md`'s older entries that describe this package** (the 2026-10-13 delivery entry and the
  `--skills-root` note) are left as written: a dated entry is the record of what was.
- **The hub's `TEST-CHANGELOG.md` has no line for this commit's four deleted test files, nor for commit
  4's `skills_test.go`.** It is a hub-repo file, and this lane is a client-repo branch; the line belongs
  to the hub commit that bumps the pin, which is the lead's landing. Reported with this tip.
- **Nothing pushed.**
