---
name: fe-dev-workflow
description: "Use at the start of every session and before every task. The fe-dev seat's working method in pdf-awesome - take one task, read the design spec, change small modules in js/domain, js/ui and js/export, test with plain node, check the browser by hand, and hand to the reviewer."
---

# fe-dev - how this seat works

Your contract is `fe-dev-role` (smallest safe change, tests with the change, report only what you
ran). This is HOW you do it here.

## Job

pdf-awesome is a browser-only image/PDF tool: vanilla ES modules, no framework, no backend, no
bundler. You deliver changes to the repo root. Current feature: split one merged PDF into named
PARTS (one per person), move pages into parts, export one PDF per part. Specs live in
`docs/superpowers/specs/` (read the two `2026-10-18-*parts*` specs first); do not contradict them,
ask the lead.

## Code map (one concern per layer)

- `js/domain/` - pure logic, no DOM. Parts model: `js/domain/parts.js`. Put new rules here first.
- `js/state.js` - the one shared state. No second copy of state in a UI file.
- `js/ui/` - DOM and events only (`parts-panel.js`, `thumbs.js`, `reorder-strip.js`, `pdf-import.js`,
  `export-panel.js`). Call domain functions; never put business rules here.
- `js/export/pdf-export.js` - PDF output. `js/infra/` - i18n, storage, loaders, DOM helpers.
- `js/infra/i18n.js` - every user-visible string goes through it, in every language it carries.
- `tests/` - plain `node tests/<file>.js`, no test framework. `pages/content.mjs` feeds the SEO pages.

## Session start

1. `4genteam whoami --json`, then `4genteam facts`.
2. `seat-worktree`: your own worktree and branch, never `main`.
3. `4genteam room queue list pdf-awesome`; `claim` your row; read its task with
   `xd://mcp__agenthub_http_manage_task` `get`.

## The loop

1. **Understand** the acceptance criteria. A UX choice you cannot settle from the task and specs:
   `4genteam decide --tier seat`, then `lead`.
2. **Survey** the files you will touch and their callers (`grep -rn`), and the existing test for the
   module. Reuse the existing helpers and CSS classes before adding new ones.
3. **Domain first, test first**: write the failing `tests/test_<name>.js` for the pure rule
   (`test-driven-development`), make it pass in `js/domain/`, then wire the UI.
4. **Verify yourself**, and read the output:
   - `npm run lint` and `npm run validate` (lint plus every test script; CI runs lint, `npm test`,
     `npm run test:pages`). Add new test scripts to `package.json` and to `validate`;
   - `git diff` - only what the task needs;
   - behaviour in a real browser with the `fe-pdf-awesome-driven` skill: `node checks/all.mjs --pw <dir>`
     (front door, real PDF import, three viewports, AA contrast in both themes, reduced motion). A new
     path (parts, move pages, export) gets its own assertions in `check-parts.mjs` in the same commit.
     If you could not run it, say the browser check is pending. Never claim a result you did not see
     this session.
5. **Record**: one `=== Progress N ===` on the task. If the project keeps a changelog, one line.
6. **Commit by pathspec** (no `git add` first; a new file takes `git add -N -- <new>` first):
   `git commit -m "<type(scope): subject>" -- <paths>`, then `git show --numstat HEAD`.
7. **Hand off**: a queue row to `reviewer` (branch, commit range, commands run and results, what
   the browser check still needs). Then tell the lead the branch is ready.

## Do not

- No push, no deploy, no `git add .` / `-A` / `--amend`; the owner approves every push.
- No new dependency, framework or build step without the lead's approval; the app runs as static files.
- No backward-compat shims or fallback code for old state shapes; follow the current model.
- No business logic in `js/ui/`, no DOM in `js/domain/`, no hard-coded user-visible strings.
- No Playwright in package.json, no build step: the browser toolkit runs from the skill, outside the repo.
- No commit of screenshots, `test_image/` or secrets.
