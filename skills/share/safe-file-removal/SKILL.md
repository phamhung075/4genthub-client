---
name: safe-file-removal
description: Remove a file reversibly by renaming it to .obsolete instead of deleting it - collision-safe, hook-compliant, one named file at a time. Never a recursive forced delete.
---

# Safe File Removal

Removal here means a rename: `mv -n "<file>" "<file>.obsolete"`. The file is out of the way, the change is reversible, and nothing is deleted.

## When to Use

- Removing temp files, build artifacts, old code, deprecated tests
- After refactoring (keep backup during testing)
- Any situation where a recursive forced `rm` is refused by the pre-tool hook or by this room's seat policy
- Need reversible "deletion"

## What Actually Enforces This

**Enforced by**: the PreToolUse hook `.claude/hooks/pre_tool_use.py` — `CommandValidator._is_dangerous_command()` and its `dangerous_rm_patterns` list — plus this room's seat policy, which refuses a recursive forced `rm` for every seat. Both of them refuse; neither gives you a removal tool. **There is no `safe-rm` binary in this tree — do not call one**, and this skill adds no script: the rename below is the whole mechanism.

| Issue | Solution |
|-------|----------|
| A recursive forced `rm` is refused | one named file at a time |
| `rm` permanently deletes | the rename is reversible |
| A rename can overwrite | `mv -n` refuses to clobber |
| No recovery | `mv file.obsolete file` restores |

## Command

```bash
mv -n "<file>" "<file>.obsolete"     # one named file; -n never overwrites an existing .obsolete
```

`mv -n` exits 0 even when it skips a collision, so check the result rather than the exit code:

```bash
[ -e "<file>.obsolete" ] && [ ! -e "<file>" ] && echo "renamed" || echo "not renamed - a .obsolete of that name already exists"
```

## Properties

| Property | How |
|----------|-----|
| **Reversible** | `mv file.obsolete file` recovers |
| **Collision safe** | `-n` will not overwrite, and the check above reports a skip |
| **One file at a time** | no bulk or recursive pass, by design |

## Basic Usage

| Operation | Command | Result |
|-----------|---------|--------|
| Single file | `mv -n file.txt file.txt.obsolete` | `file.txt.obsolete` |
| A directory | `mv -n dist dist.obsolete` | the directory itself, renamed |
| Restore | `mv file.txt.obsolete file.txt` | Original restored |

## Recovery

| Scenario | Command |
|----------|---------|
| Single | `mv file.obsolete file` |
| With a suffix you chose | `mv file.obsolete.20251109_110500 file` |
| Find all | `find . -name "*.obsolete"` |
| Restore all | `find . -name "*.obsolete" \| while read f; do mv "$f" "${f%.obsolete}"; done` |

## Workflow

1. Mark obsolete → `mv -n <file> <file>.obsolete` (one named file at a time)
2. Verify works → `npm test && npm run build`
3. Review periodic → `find . -name "*.obsolete"`
4. Permanent delete (when confident) → `/bin/rm <file>.obsolete` (a single named file; a renamed *directory* needs a recursive delete, which this room's policy refuses — leave that one to a human in a plain terminal)

## When NOT to Use

| Scenario | Use this? | Why |
|----------|-----------|-----|
| Secrets (.env, keys) | ❌ | Needs secure deletion + git history rewrite |
| .git folder | ❌ | Too risky |
| node_modules | ⚠️ | A recursive forced delete is refused here anyway; regenerate instead of renaming |
| Temp/build | ✅ | Reversible, safe |

## What You See

**Success**:
```
$ mv -n file.txt file.txt.obsolete
$ ls file.txt.obsolete
file.txt.obsolete
```

**Collision** — nothing was overwritten, and `mv -n` said nothing about it:
```
$ mv -n file2.txt file2.txt.obsolete     # file2.txt.obsolete already existed
$ cat file2.txt.obsolete                 # still the old content
PRECIOUS
$ ls file2.txt                           # and the source is still there too
file2.txt
```

## Quick Reference

| Task | Command |
|------|---------|
| Remove single | `mv -n <file> <file>.obsolete` |
| Restore | `mv <file>.obsolete <file>` |
| List obsolete | `find . -name "*.obsolete"` |
| Count | `find . -name "*.obsolete" \| wc -l` |
| Verify the enforcement | `grep -nE "dangerous_rm_patterns" .claude/hooks/pre_tool_use.py` |

## Supporting Files

- **[EXAMPLES.md](EXAMPLES.md)** - scenarios: temp files, refactoring, tests, collisions, assets, docs, loops, scripting, git, hooks
- **[TEMPLATES.md](TEMPLATES.md)** - Copy-paste commands for all operations
- **[VALIDATION.md](VALIDATION.md)** - Safety checklists, verification commands

## Key Insight

**Safety by design**: no deletion at all → reversible rename → an explicit collision check → review period → permanent delete only after confidence. Development workflow: Refactor → rename to `.obsolete` → Test → Review period → Permanent delete after confidence.
