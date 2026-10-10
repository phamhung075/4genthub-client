# Safe File Removal Examples

Real-world patterns for removing a file reversibly — rename it to `.obsolete` instead of deleting it. This is what replaces a recursive forced `rm`, which the pre-tool hook and this rig's seat policy both refuse. There is no `safe-rm` binary; every command below is plain `mv -n`, one named file at a time.

## Pattern Summary

| # | Pattern | Command | When |
|---|---------|---------|------|
| 1 | Temp files | `mv -n dist dist.obsolete` (one entry at a time) | After build, before commit |
| 2 | Refactoring | `mv -n src/OldComponent.tsx src/OldComponent.tsx.obsolete` | During component replacement |
| 3 | Test files | one `mv -n <f> <f>.obsolete` per file | Test reorganization |
| 4 | Collision | `mv -n file.txt file.txt.obsolete` when the target exists | `-n` skips it; nothing is overwritten |
| 5 | Assets | `mv -n public/old_logo.png public/old_logo.png.obsolete` | Unused images/icons |
| 6 | Documentation | `mv -n docs/OLD_README.md docs/OLD_README.md.obsolete` | Outdated docs |
| 7 | Pattern cleanup | `find . -name "*.log" \| while read -r f; do mv -n "$f" "$f.obsolete"; done` | Many files, one rename per iteration |
| 8 | Scripting | See the cleanup script template | Automated workflows |
| 9 | Git untrack | `git rm --cached file; mv -n file file.obsolete` | Remove from git, keep local |
| 10 | Hook prevention | recursive forced `rm` refused → rename instead | Safety enforcement |

## Scenario 1: Temp Files

**Before** (refused):
```bash
rm -rf dist/ build/ .cache/   # ❌ refused - the hook's dangerous_rm_patterns match it, and the seat policy refuses it too
```

**After** — one named directory at a time:
```bash
mv -n dist dist.obsolete
mv -n build build.obsolete
mv -n .cache .cache.obsolete
```

**Recovery**: `mv dist.obsolete dist` — or `npm run build` to regenerate. (Regenerating is usually better than recovering a build directory.)

## Scenario 2: Refactoring Workflow

```bash
# 1. Create new
# [implement ComponentNew.tsx]

# 2. Update imports
# [change references]

# 3. Remove old (reversibly)
mv -n src/components/ComponentOld.tsx src/components/ComponentOld.tsx.obsolete

# 4. Verify
npm test && npm run build

# 5. If issues → mv src/components/ComponentOld.tsx.obsolete src/components/ComponentOld.tsx
# 6. After 1 week → /bin/rm src/components/ComponentOld.tsx.obsolete   (one named file)
```

## Scenario 3: Several Test Files

```bash
mv -n src/tests/old_test_1.spec.ts src/tests/old_test_1.spec.ts.obsolete
mv -n src/tests/old_test_2.spec.ts src/tests/old_test_2.spec.ts.obsolete
mv -n src/tests/deprecated src/tests/deprecated.obsolete

npm test                                # Verify

find src/tests -name "*.obsolete"       # Review what was set aside
```

## Scenario 4: Collision Handling

The rename never clobbers, so a second removal of the same name is simply skipped — and `mv -n` does not say so:

```bash
# First removal
mv -n config.old.json config.old.json.obsolete
# Result: config.old.json.obsolete

# Restore it
mv config.old.json.obsolete config.old.json

# Second removal while a .obsolete of that name exists
mv -n config.old.json config.old.json.obsolete
# exit status 0, but NOTHING happened - check, and choose another name if you want it out of the way:
ls config.old.json config.old.json.obsolete
```

**Benefit**: An existing `.obsolete` is never overwritten. The price: the check is on you, because `mv -n` reports nothing.

## Scenario 5: Asset Cleanup

```bash
mv -n public/images/old_logo.png public/images/old_logo.png.obsolete
mv -n public/icons/deprecated public/icons/deprecated.obsolete
mv -n assets/unused_banner.svg assets/unused_banner.svg.obsolete

npm run dev  # Verify UI loads, check for 404s
```

## Scenario 6: Documentation Cleanup

```bash
mv -n docs/OLD_README.md docs/OLD_README.md.obsolete
mv -n docs/deprecated/api-v1-guide.md docs/deprecated/api-v1-guide.md.obsolete

grep -r "OLD_README\|api-v1-guide" docs/  # Check for broken links
npm run docs:build                        # Verify docs site builds
```

## Scenario 7: Pattern Cleanup

One rename per iteration — never a recursive forced delete:

```bash
# All .log files
find . -name "*.log" | while read -r f; do mv -n "$f" "$f.obsolete"; done

# Files older than 30 days
find logs/ -type f -mtime +30 | while read -r f; do mv -n "$f" "$f.obsolete"; done

# Then review what moved
find . -name "*.obsolete" -mtime -1
```

## Scenario 8: Scripting Example

```bash
#!/bin/bash
# cleanup_build.sh

echo "Setting build artifacts aside..."
mv -n dist dist.obsolete
mv -n build build.obsolete

echo "Verifying build..."
npm run build

if [ $? -eq 0 ]; then
  echo "✓ Build successful"
  echo "⚠ Review .obsolete entries, then delete one named entry at a time"
else
  echo "✗ Build failed - recovering"
  mv dist.obsolete dist 2>/dev/null
  mv build.obsolete build 2>/dev/null
  exit 1
fi
```

## Scenario 9: Git Untrack + Local Backup

```bash
# Remove from git, keep a local copy
git rm --cached sensitive_config.json
mv -n sensitive_config.json sensitive_config.json.obsolete
git commit -m "chore: remove sensitive config from repo"

# File available as sensitive_config.json.obsolete
# Recover: mv sensitive_config.json.obsolete sensitive_config.json
```

## Scenario 10: Hook Prevention

```bash
# Agent attempts a recursive forced delete → REFUSED
rm -rf old_dir/
# Refused: the hook's dangerous_rm_patterns match it, and this rig's seat policy refuses it for every seat

# Agent renames instead → SUCCEEDS, and stays recoverable
mv -n old_dir old_dir.obsolete
ls old_dir.obsolete

# User reviews → decides restore or delete
```

## Recovery Examples

| Scenario | Command |
|----------|---------|
| Single file | `mv file.txt.obsolete file.txt` |
| Several | `mv f1.obsolete f1; mv f2.obsolete f2` |
| With a suffix you chose | `mv config.json.obsolete.20251109_110500 config.json` |
| All in dir | `find . -name "*.obsolete" \| while read f; do mv "$f" "${f%.obsolete}"; done` |

## Common Mistakes

| Mistake | Issue | Fix |
|---------|-------|-----|
| **Renaming an `.obsolete` again** | `file.obsolete.obsolete` clutter | `mv file.obsolete file` or `/bin/rm file.obsolete` |
| **No dependency check** | Breaks build | `grep -r "filename" src/` before the rename |
| **Never clean up** | Clutter accumulates | Weekly `find . -name "*.obsolete" -mtime +7` |
| **Trusting the exit status** | `mv -n` returns 0 on a skipped collision | Check with `ls` (see Scenario 4) |
| **Sensitive files** | Still visible in the filesystem | Use secure deletion for secrets |

## What You See

| Result | What it looks like |
|--------|--------------------|
| **Success** | source gone, `<name>.obsolete` present |
| **Collision** | source still present, existing `.obsolete` unchanged, exit status 0 — check with `ls` |
| **Not found** | `mv: cannot stat '<name>': No such file or directory` |

## Best Practices

1. **Mark obsolete first** → Don't delete immediately
2. **Verify system works** → `npm test && npm run build`
3. **Keep review period** → 1 day to 1 week depending on risk
4. **List periodically** → `find . -name "*.obsolete"`
5. **Permanent delete** → After confidence, one named file with `/bin/rm`

## Integration with Hooks

**Enforced by**: the PreToolUse hook `.claude/hooks/pre_tool_use.py` — `CommandValidator._is_dangerous_command()` and its `dangerous_rm_patterns` list — plus this rig's seat policy, which refuses a recursive forced `rm` for every seat.

**Flow**: Agent tries a recursive forced `rm` → refused → renames to `.obsolete` → user reviews → restores or deletes

**Benefit**: Prevents accidental data loss during AI operations

## Session Example (2025-11-09)

- Created safe-rm command (`.claude/bin/safe-rm`)
- Tested with temporary files
- Demonstrated collision handling (timestamp fallback)
- Used in skill restructuring (removed old skill.md/skill.json)
- Integrated with pre-tool hooks (rm blocked, safe-rm allowed)

*That entry records what that session did on 2025-11-09; it is history, not an instruction — the mechanism this skill teaches is the plain `mv -n` rename above.*
