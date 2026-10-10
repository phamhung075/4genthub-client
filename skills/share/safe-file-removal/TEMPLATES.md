# Safe File Removal Templates

Copy-paste commands. Replace `[PLACEHOLDERS]` with actual values. Every removal is a rename to `.obsolete` — `mv -n`, one named file at a time. There is no `safe-rm` binary.

## Basic Operations

| Operation | Template |
|-----------|----------|
| **Single file** | `mv -n "[PATH_TO_FILE]" "[PATH_TO_FILE].obsolete"` |
| **A directory** | `mv -n "[PATH_TO_DIR]" "[PATH_TO_DIR].obsolete"` |
| **Several files** | one command per file, run them in sequence |
| **Never** | a recursive forced `rm` — refused by the hook and by this rig's seat policy |

**Example**:
```bash
mv -n src/components/OldComponent.tsx src/components/OldComponent.tsx.obsolete
mv -n old_file.txt old_file.txt.obsolete
mv -n legacy.js legacy.js.obsolete
```

## Check the Rename

`mv -n` returns 0 even when a collision made it skip, so verify rather than trust the status:

```bash
mv -n "[FILE]" "[FILE].obsolete"
[ -e "[FILE].obsolete" ] && [ ! -e "[FILE]" ] && echo "renamed" || echo "not renamed - a .obsolete of that name already exists"
```

## Recovery

| Operation | Template |
|-----------|----------|
| **Single** | `mv [FILENAME].obsolete [FILENAME]` |
| **Suffix you chose** | `mv [FILENAME].obsolete.[SUFFIX] [FILENAME]` |
| **Several** | `mv [F1].obsolete [F1]; mv [F2].obsolete [F2]` |
| **All in dir** | `find [DIR] -name "*.obsolete" \| while read f; do mv "$f" "${f%.obsolete}"; done` |

**Example**:
```bash
mv important.txt.obsolete important.txt
mv config.json.obsolete.20251109_110500 config.json
```

## Search & List

| Operation | Template |
|-----------|----------|
| **List all** | `find . -name "*.obsolete"` |
| **In directory** | `find [DIR] -name "*.obsolete"` |
| **Count** | `find . -name "*.obsolete" \| wc -l` |
| **With details** | `find . -name "*.obsolete" -ls` |
| **Disk space** | `du -sh $(find . -name "*.obsolete")` |
| **Recent (7 days)** | `find . -name "*.obsolete" -mtime -7` |
| **Old (30+ days)** | `find . -name "*.obsolete" -mtime +30` |

## Pattern Cleanup

One rename per iteration, never a recursive forced delete:

| Operation | Template |
|-----------|----------|
| **All .log files** | `find . -name "*.log" \| while read -r f; do mv -n "$f" "$f.obsolete"; done` |
| **Old files** | `find [DIR] -type f -mtime +30 \| while read -r f; do mv -n "$f" "$f.obsolete"; done` |
| **Pattern match** | `find [DIR] -name "[PATTERN]" \| while read -r f; do mv -n "$f" "$f.obsolete"; done` |

**Example**:
```bash
find temp/ -name "*.tmp" | while read -r f; do mv -n "$f" "$f.obsolete"; done
find logs/ -type f -mtime +30 | while read -r f; do mv -n "$f" "$f.obsolete"; done
```

## Cleanup (permanent, after the review period)

| Operation | Template |
|-----------|----------|
| **Delete one named file** | `/bin/rm [FILENAME].obsolete` |
| **Delete old (7+ days), one at a time** | `find . -name "*.obsolete" -mtime +7 \| while read -r f; do /bin/rm "$f"; done` |

A renamed **directory** still needs a recursive delete, which this rig's policy refuses — leave that one to a human in a plain terminal.

## Scripting

### Cleanup Script
```bash
#!/bin/bash
# cleanup_[NAME].sh

echo "Setting [DESCRIPTION] aside..."
mv -n [FILE_OR_DIR] [FILE_OR_DIR].obsolete

echo "Verifying..."
[VERIFICATION_COMMAND]

if [ $? -eq 0 ]; then
  echo "✓ Success - review the .obsolete entry, then delete it after the review period"
else
  echo "✗ Failed - recovering"
  mv [FILE_OR_DIR].obsolete [FILE_OR_DIR]
  exit 1
fi
```

### Git Untrack
```bash
git rm --cached [FILENAME]
mv -n [FILENAME] [FILENAME].obsolete
git commit -m "[MESSAGE]"
# File available as [FILENAME].obsolete
```

### Conditional Removal
```bash
if [ -e "[FILENAME]" ]; then
  mv -n "[FILENAME]" "[FILENAME].obsolete"
else
  echo "File not found: [FILENAME]"
fi
```

## Verification

### Before Removal
```bash
# Check usage
grep -r "[SEARCH_TERM]" [DIRECTORY]

# If no results, set it aside
mv -n "[FILE_TO_REMOVE]" "[FILE_TO_REMOVE].obsolete"
```

### After Removal
```bash
npm run build
npm test

# If issues
# mv [FILE].obsolete [FILE]
```

## Workflows

### Refactoring
```bash
# 1. Create new
[CREATE_NEW_FILE]

# 2. Update references
grep -r "[OLD_NAME]" src/
[UPDATE_IMPORTS]

# 3. Set old aside
mv -n "[OLD_FILE]" "[OLD_FILE].obsolete"

# 4. Verify
npm test && npm run build

# 5. If issues → mv [OLD_FILE].obsolete [OLD_FILE]
# 6. After confidence → /bin/rm [OLD_FILE].obsolete
```

### Asset Cleanup
```bash
# 1. Set aside
mv -n "[ASSET_FILE]" "[ASSET_FILE].obsolete"

# 2. Check references
grep -r "[ASSET_NAME]" src/

# 3. Verify UI
npm run dev

# 4. Check browser console for 404s
# 5. If issues → mv [ASSET].obsolete [ASSET]
```

## Quick Reference

| Placeholder | Example |
|-------------|---------|
| `[PATH_TO_FILE]` | `src/components/Old.tsx` |
| `[PATH_TO_DIR]` | `dist` or `build` |
| `[FILENAME]` | `config.json` |
| `[FILE_OR_DIR]` | `dist` |
| `[DIRECTORY]` | `src/` or `.` |
| `[PATTERN]` | `*.log` or `temp_*` |
| `[SUFFIX]` | `20251109_110500` |
| `[DESCRIPTION]` | `old build artifacts` |
| `[VERIFICATION_COMMAND]` | `npm run build` |
| `[MESSAGE]` | `chore: remove old config` |

## Safety Checklist

### Before
```
- [ ] grep -r "[NAME]" [DIR]
- [ ] Know recovery: mv [FILE].obsolete [FILE]
- [ ] One named file at a time - never a recursive forced rm
```

### After
```
- [ ] npm test
- [ ] npm run build
- [ ] npm run dev (manual test)
- [ ] find . -name "*.obsolete"
```
