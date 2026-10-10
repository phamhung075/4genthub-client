# Safe File Removal Validation

Quality checks for reversible file removal — renaming a named file to `.obsolete` instead of deleting it. There is no `safe-rm` binary; the mechanism is `mv -n`.

## Quick Checklists

### Before Removal
- [ ] `grep -r "[NAME]" [DIR]` → No results
- [ ] No imports depending on file
- [ ] Know recovery: `mv [FILE].obsolete [FILE]`
- [ ] One named file at a time — never a recursive forced `rm`

### After Removal
- [ ] `npm test` → Pass
- [ ] `npm run build` → Success
- [ ] `npm run dev` → Works
- [ ] No browser console errors
- [ ] `find . -name "*.obsolete"` → Listed

### Before Permanent Delete
- [ ] Time passed (1-7 days review period)
- [ ] System stable (no issues discovered)
- [ ] `find . -name "*.obsolete" -mtime +[DAYS]` → Old enough

## Validation Commands

| Check | Command |
|-------|---------|
| **Check usage** | `grep -r "[FILENAME_WITHOUT_EXT]" [DIR]` |
| **Check imports** | `grep -r "from.*[FILENAME]" src/` |
| **File exists** | `ls -la [PATH_TO_FILE]` |
| **Verify rename landed** | `ls -la [FILE].obsolete` |
| **Verify no collision was skipped** | `ls -la [FILE] [FILE].obsolete` (both present = skipped) |
| **Run tests** | `npm test` |
| **Run build** | `npm run build` |
| **TypeScript check** | `npx tsc --noEmit` |
| **List obsolete** | `find . -name "*.obsolete"` |
| **Count obsolete** | `find . -name "*.obsolete" \| wc -l` |
| **Find old (7+ days)** | `find . -name "*.obsolete" -mtime +7` |
| **Disk space** | `du -sh $(find . -name "*.obsolete")` |

## Critical Files - Never Set These Aside This Way

| File/Directory | Why | Alternative |
|----------------|-----|-------------|
| `.git/` | Lose entire history | Use git commands |
| `.env*` | Contains secrets | Secure deletion + git history rewrite |
| `package.json` | Critical config | Version control |
| `database/` | Data loss | Backup + migration |

## Quality Checks by Type

| Type | Required Checks |
|------|-----------------|
| **Source code** | `grep -r "[NAME]" src/` → No imports → `npm test && npm run build` |
| **Assets** | Check references → Verify UI → No 404s in console |
| **Tests** | Remaining tests pass → Coverage maintained |
| **Documentation** | No broken links → Docs site builds |

## Common Pitfalls

| Pitfall | Detection | Prevention |
|---------|-----------|------------|
| **Still imported** | `grep -r "import.*[FILE]"` finds results | Check before removal |
| **Dynamic reference** | `grep -r "[FILENAME]"` shows string refs | Search for string usage |
| **Test dependency** | Tests fail after removal | Run tests immediately |
| **Build breaks** | Build errors appear | Run build immediately |
| **Silent collision skip** | source AND `.obsolete` both present after `mv -n` | check with `ls`; `mv -n` returns 0 either way |

## Validation Script

```bash
#!/bin/bash
# validate_removal.sh [FILE]

FILE="$1"
[ -z "$FILE" ] && echo "Usage: $0 [FILE]" && exit 1

echo "=== Pre-Removal ==="
[ ! -e "$FILE" ] && echo "✗ Not found: $FILE" && exit 1
echo "✓ File exists"

FILENAME=$(basename "$FILE" | sed 's/\.[^.]*$//')
USAGE=$(grep -r "$FILENAME" src/ 2>/dev/null | wc -l)

if [ $USAGE -gt 1 ]; then
  echo "⚠ Found $USAGE references"
  grep -r "$FILENAME" src/ | head -10
  read -p "Continue? (y/N) " -r
  [[ ! $REPLY =~ ^[Yy]$ ]] && exit 1
fi
echo "✓ No usage"

echo -e "\n=== Setting aside (rename, one named file) ==="
mv -n "$FILE" "$FILE.obsolete" || (echo "✗ Rename failed" && exit 1)
echo "✓ Rename command returned"

echo -e "\n=== Post-Removal ==="
[ ! -e "$FILE.obsolete" ] && echo "✗ .obsolete not found" && exit 1
[ -e "$FILE" ] && echo "✗ source still present - a collision was skipped; pick another name" && exit 1
echo "✓ source gone, .obsolete present"

echo -e "\nRunning tests..."
npm test > /dev/null 2>&1 || (echo "✗ Tests failed - recovering" && mv "$FILE.obsolete" "$FILE" && exit 1)
echo "✓ Tests pass"

echo -e "\nRunning build..."
npm run build > /dev/null 2>&1 || (echo "✗ Build failed - recovering" && mv "$FILE.obsolete" "$FILE" && exit 1)
echo "✓ Build succeeds"

echo -e "\n=== Complete ==="
echo "✓ Reversibly removed: $FILE → $FILE.obsolete"
echo "⚠ Review and delete later: /bin/rm $FILE.obsolete"
```

## Recovery Testing

```bash
# 1. Set a test file aside
echo "test" > test_file.txt
mv -n test_file.txt test_file.txt.obsolete

# 2. Verify the source is gone
ls test_file.txt 2>&1 | grep "No such file"

# 3. Verify .obsolete exists
ls test_file.txt.obsolete

# 4. Recover
mv test_file.txt.obsolete test_file.txt

# 5. Verify recovered
cat test_file.txt | grep "test"

# 6. Cleanup, one named file
/bin/rm test_file.txt
```

## Collision Testing

`mv -n` skips instead of overwriting — and its exit status does not tell you:

```bash
# 1. Set aside
echo "v1" > test.txt
mv -n test.txt test.txt.obsolete

# 2. Restore and modify
mv test.txt.obsolete test.txt
echo "v2" > test.txt

# 3. Try again while .obsolete exists
mv -n test.txt test.txt.obsolete
echo "exit status: $?"          # 0, although nothing happened

# 4. Verify both are still there and the old content survived
ls test.txt test.txt.obsolete
cat test.txt.obsolete           # v1 - not overwritten

# 5. Cleanup, one named file at a time
/bin/rm test.txt
/bin/rm test.txt.obsolete
```

## Error Handling

| Error | Cause | Fix |
|-------|-------|-----|
| **Not found** | File doesn't exist | Check path with `ls` |
| **Permission denied** | No write access | Fix ownership or permissions for the file you named |
| **Silent no-op** | `.obsolete` of that name already exists | `mv -n` skipped it; pick another name or restore the old one first |

## Monitoring

```bash
# Daily log
echo "$(date): $(find . -name '*.obsolete' | wc -l) files" >> .obsolete_log

# Weekly cleanup check
find . -name "*.obsolete" -mtime +7 | wc -l

# Space monitoring
du -sh $(find . -name "*.obsolete" 2>/dev/null)
```

## Integration Check

### The Enforcement
```bash
# The refusal comes from the hook's pattern list and from this rig's seat policy.
grep -nE "dangerous_rm_patterns|_is_dangerous_command" .claude/hooks/pre_tool_use.py
```

### The Removal Itself
```bash
# Verify the mechanism works on a file you made for the purpose, one named file at a time
echo test > test_file.txt
mv -n test_file.txt test_file.txt.obsolete
ls test_file.txt.obsolete        # present
mv test_file.txt.obsolete test_file.txt
ls test_file.txt                 # restored
```

## Best Practices Verification

- [ ] Renamed to `.obsolete` (one named file; never a recursive forced `rm`)
- [ ] Searched first (`grep -r`)
- [ ] Checked the rename landed (`ls`, not the exit status)
- [ ] Tested after (`npm test && npm run build`)
- [ ] Kept review period (1-7 days)
- [ ] Listed periodically (`find . -name "*.obsolete"`)

## Quick Reference

| Check | Command |
|-------|---------|
| **Pre-removal** | `grep -r "[NAME]" src/` |
| **Rename** | `mv -n [FILE] [FILE].obsolete` |
| **Post-removal** | `npm test && npm run build` |
| **List obsolete** | `find . -name "*.obsolete"` |
| **Find old** | `find . -name "*.obsolete" -mtime +7` |
| **Verify hook** | `grep -nE "dangerous_rm_patterns" .claude/hooks/pre_tool_use.py` |
