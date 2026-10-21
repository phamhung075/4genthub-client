# The seat guard reads what a patch would write, and its push refusal stops inventing a deploy

The guard protects `rust/agenthub-runtime/assets/seat-guard.ts` by basename, in every directory, for every seat -
the file it lives in, so a seat that can edit it can lift every other rule. It enforced that on the tools that
NAME their path (`write`, `cp`, `tee`, `rm`), and `protectedPath` had exactly one call site: the write-target
rule. A git verb names a document to read and a tree to write, and neither is the protected path, so six probes
were allowed at the pin `6c25e316` and one of them rewrote the file: `git apply` of a patch that touches it,
`git am`, `git stash pop` over an entry that carries it, `git checkout -- <it>` and
`git restore --source=<rev> <it>`.

One rule closes them, `git-imports-a-tree`, after `git-destructive`. For `git apply`, `git am` and `patch` it
reads the patch document's diff headers - `diff --git a/X b/Y`, the `---`/`+++` pair, `rename`/`copy from|to`,
`Binary files … differ`, every message of an mbox, the whole document - unquoting git's octal quoting, and
refuses when a header names a protected path. The test is on the TAIL of a token (a basename, or a protected
directory anywhere in the path), which is what lets `-p`, `--include` and `--exclude` stay allowed: stripping a
prefix cannot hide a tail, and those flags are in measured use 23 times across the five seats, so refusing them
would cost the room its landing verb. `--directory`, `--prefix`, `--unsafe-paths` and `patch -d` are refused on
the forms that write, because they move the write to a path no header line names: composing the destination
means emulating the order in which the three flags apply, and a wrong order fails open. The stdin forms
(`cat p | git apply`, `git apply -`, `git am`, `patch` with no argument) and an unreadable source are refused -
there is no document, so the guard cannot see what it would write. The forms that write nothing
(`--check`/`--stat`/`--summary`/`--numstat`, `patch --dry-run`, `git am --continue/--abort/--skip/--resolved`)
are allowed unread. `git checkout -- <path>` and `git restore <path>` run `protectedPath` over the paths they
rewrite, and `git stash pop`/`apply`/`branch` reads the entry's file list through one `git diff --name-only`
subprocess plus `ls-tree` for a stash made with `-u`; a failed read is refused. `patch -d` is refused in the
attached spelling too (`-dhooks/pre`), which the earlier single-letter test missed.

Revision 6 widens the trigger from the verb to the SOURCE. A stash entry IS a commit and the owner makes stashes
unguarded, so `git checkout <stash>`, `git switch <stash>` (never in the verb set), `git cherry-pick <stash>`,
`git merge <stash>`, `git rebase <stash>` and `git stash branch` imported the guard: measured 2026-10-21, a base
tree whose asset read `const old = 1;` held `const victim = 1;` after one `git checkout -q <entry>`, with no
protected path named anywhere in the command. Every operand of those verbs is now resolved with
`git rev-parse --verify <op>^{commit}` and compared against `git reflog refs/stash --format=%H` (cached per
directory); an operand that lands in the stash is read the way `pop` is. A plain commit operand stays allowed - a
guard change in a commit was made by the lead or the owner, the sanctioned path - and an operand that names no
commit yet (`switch -c x <ref>` makes `x`) is allowed because the verb cannot import it. `git branch <name>
<stash>` writes nothing, so it is allowed on its own and REMEMBERED for the rest of the same tool call: at hook
time `git branch sneaky stash@{0} && git cherry-pick sneaky` has no `sneaky` to resolve, and without that memory
the pair wrote the guard. `git stash push` closes the destruction side: with `--` it checks the named paths, and
the path-free form reads `git diff --name-only` and `git diff --cached --name-only`, because it reverts that path
to HEAD and would silently remove a lead's uncommitted guard edit.

The cost of the shape is recorded rather than hidden: a reader bug is a silent hole, which is why the probe
table is the test suite (`tests/seat_guard.rs`: a patch naming the guard plain, `-p2`, binary, deletion and
quoted; `scripts/git-hooks`, `hooks/pre` and `.gemini/config/hooks.json` by path; the four destination flags,
`patch -d` split and attached; the stdin forms; an mbox whose second message carries it; the stash, checkout and
restore cases; the revision-6 rule with its nine refusals and five controls; and the rows that must stay
allowed). A reviewer adding a header form adds a row.

The same landing corrects the guard's own words. The header comment and the push refusal said that a push to main
ships a deploy; the owner's 2026-10-18 ruling says a push to main deploys nothing, the frontend ships from
the `frontend` branch, and a webhook 200 is delivery and not a build. Both now name no deploy at all: only the
lead pushes, after the reviewer gate and a green test run, and every other seat does not. The Docker refusal's
"production updates by a push the owner approves" carried the same wrong model and is gone with them.

The asset is `include_str!`-ed into the runtime (`supervisor/mod.rs:421`) and written into
`<seat dir>/hooks/pre/` at every seat start, so **the new rule reaches a seat only at its next start after the
client is rebuilt**; the team must be respawned for it to be live. The asset is the owner's to edit, so this
change lands as a patch the lead applies; the tests and this entry are the author's.

Verified: `cargo test --offline --test seat_guard` in the crate with the patch applied - 27 passed, 0 failed,
the eleven-row probe table and the revision-6 table included; against the pin asset 20 pass and 7 fail, the
revision-6 case among them, which is what pins each case to the rule rather than to something incidental.
a repository-wide grep for the old sentence over `agenthub_client` returns no hits once the patch is
applied (the asset in the worktree still carries it until the lead applies the patch; the check was run on the
patched copy).
