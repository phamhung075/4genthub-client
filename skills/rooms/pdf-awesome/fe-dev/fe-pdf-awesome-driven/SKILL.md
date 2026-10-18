---
name: fe-pdf-awesome-driven
description: "Recheck the pdf-awesome page in a real browser: the front door and a real PDF import, fit on desktop/laptop/phone, WCAG AA contrast in the light and dark theme, and reduced-motion. A measured toolkit, not opinions. Serves the repo as static files (no build, no backend, no login). Run it before handing a frontend change to the reviewer."
---

# fe-pdf-awesome-driven

pdf-awesome is static files. The browser is where its bugs show, so this skill proves a change with
one instead of assuming. Adapted from the 4genthub `fe-4genthub-driven` toolkit: the login, backend
and CRUD-animation checks are gone because this app has none of them; the rest is kept and fitted.

## Quick start

```bash
# run from inside the pdf-awesome worktree (or pass --root). Five checks, one summary, exit 1 on any failure.
node <this skill>/checks/all.mjs --pw <dir with node_modules/@playwright/test>

# one check
node <this skill>/checks/check-load.mjs --pw <dir>
```

- **No build.** The toolkit starts a read-only static file server on a free local port for the repo
  root (it never serves `node_modules`, `.git`, `.claude`, `agenthub_client`). Nothing is compiled,
  bundled or installed. `--base https://<origin>` checks a deployed origin instead.
- **Playwright is not a project dependency** and none is added. Pass `--pw <dir>` (a directory whose
  `node_modules` holds `@playwright/test`, `playwright` or `playwright-core`), or set `PLAYWRIGHT_DIR`.
  On this machine `~/__projects__/4genthub/agenthub-frontend` has one, and the browsers are already
  cached in `~/.cache/ms-playwright`; otherwise `npx playwright install chromium` once.
- The browser is Playwright's **bundled Chromium**. `PDF_BROWSER=chrome` opts into installed Chrome:
  headless Chrome 153 wedges its renderer on the first canvas 2D draw (measured 2026-10-17), and this
  app draws every page on a canvas, so the default avoids reading the browser's bug as the page's.

## What each check answers

| Check | The question | A FAIL means |
|---|---|---|
| [check-load](checks/check-load.mjs) | Does `/` render with zero console errors and zero failed requests, do the entry controls exist, and does importing a real generated 3-page PDF through `#pdfInput` give 3 thumbnails and enable `#btnNext`? | The app is broken, or import is. |
| [check-parts](checks/check-parts.mjs) | Import a 6-page PDF, reach export, create two named parts, assign pages 1-3 and 4-6 through the range controls, and does each part's Save download `<name>.pdf`? | The split-into-parts flow is broken. |
| [check-responsive](checks/check-responsive.mjs) | Desktop 1440, laptop 1280, phone 390 (emulated): no sideways drag, viewport meta right, buttons inside the viewport? | A form factor is unusable. Buttons under 44px on the phone are reported, not failed. |
| [check-contrast](checks/check-contrast.mjs) | Every text run against its effective background, AA 4.5:1 (3:1 large), in the **light and the dark theme** (`localStorage pdf_theme`)? | Text a user has to squint at. |
| [check-motion](checks/check-motion.mjs) | Under `prefers-reduced-motion: reduce`, does any visible element keep an animation over 500ms or loop forever? | A user who asked for less motion still gets looping decoration. |

## When to run which

- Any UI change: `all.mjs`. Quote the failing line (selector, px/weight, ratio) in your handoff.
- A change to import, parts or export: extend `check-parts.mjs` (or `check-load.mjs` for import) with the
  new path (drag-drop assignment, per-thumb assignment, rename, remove, one-PDF-per-part export) in the same commit as the feature; a path with no
  browser check is a path you cannot claim works. `makePdf(n)` in [lib.mjs](lib.mjs) builds an
  n-page PDF in memory.
- Never claim a browser result you did not capture this session. No browser: say the check is pending.

## Measured state - 2026-10-18 (first run, project at its current `main`)

- parts: PASS on main `ce7735c` (both parts download as Alice.pdf and Bob.pdf).
- load: PASS (3 thumbnails for a 3-page PDF, no console errors, no failed requests).
- responsive: PASS on all three viewports; 13 phone buttons are under 44px (theme toggle 28x28,
  language buttons 33x20, help 28x28).
- motion: PASS (nothing animates at rest).
- **contrast: FAIL, a real backlog**, 21 runs in light and 13 in dark below AA. Worst: white "Import
  PDF" on the orange button 2.15:1; the muted grey text (`rgb(148,160,184)` in light 2.63:1,
  `rgb(100,116,139)` in dark 3.68:1) on step labels, guide text, version, language buttons; indigo
  links 4.28:1. One token change per color family clears each list: re-run to confirm.

## Traps already paid for (kept from the parent toolkit)

1. A bare `{ width, height }` in `newContext` is silently ignored; use `{ viewport: {...} }`.
2. `scrollWidth > innerWidth` is not an overflow test; scroll right and read `scrollX`.
3. `networkidle` is not readiness; the check waits for visible text (`waitForReady`) and fails by
   name when the floor is not reached, rather than reading a placeholder and printing a false green.
4. A transparent `body` means `html` paints the page; the contrast walk composites up the ancestors.
5. `page.evaluate(fn)` serializes the function alone; the parent's helpers must travel with it
   (the color parser is injected with `addInitScript`).
6. A `catch` covers only the operation that can legitimately fail, never the analysis after it.

## Rules

- Read-only: the server serves files, the checks click nothing that writes outside the page. The
  generated PDF lives in the system temp directory and carries no real data.
- No screenshots or generated files into the repo. Report the measured line, not "contrast is bad".
- A new dependency, a build step, or Playwright in `package.json` needs the lead's approval first.

## Files

| File | Job |
|---|---|
| [lib.mjs](lib.mjs) | Static server, Playwright and project resolution, viewports, `waitForReady`, page watcher, `makePdf`, color maths. |
| [checks/all.mjs](checks/all.mjs) | The runner: each check in its own process, one summary, one exit code. |
| [checks/](checks) | The five checks above. |
