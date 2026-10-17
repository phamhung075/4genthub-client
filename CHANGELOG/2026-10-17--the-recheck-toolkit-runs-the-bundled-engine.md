# The recheck toolkit runs the engine the app renders in

The tracked store copy of `skills/rooms/4genthub-min/fe-dev/fe-4genthub-driven` launched the
installed Google Chrome headless and told the reader to run a local build before shipping. Both were
wrong, and the first one was wrong in a way that reads as a page regression.

### Fixed

- **The engine is Playwright's bundled Chromium by default.** Measured 2026-10-17 on a bare canvas
  page with no app code in the path (1280×800, `channel: 'chrome'`, headless): `getContext('2d')`
  alone is fine and `ctx.font` + `ctx.measureText('M')` is fine, while `ctx.scale(1,1)`,
  `ctx.scale(2,2)`, `ctx.setTransform(1,0,0,1,0,0)` and `ctx.fillRect` each leave headless Google
  Chrome 153.0.8010.36's renderer permanently unresponsive — every CDP call to the page times out
  (`Page.getFrameTree`, `DOM.getDocument`, `Runtime.evaluate`, `Performance.getMetrics`), no `crash`
  event fires, `--disable-gpu` and `--use-angle=swiftshader` do not help, and a CDP trace shows no
  long main-thread frame. Headed Chrome 153 on the same page and bundled Chromium 141 headless are
  both fine. `/login` is the app's only canvas user (`FallingGlitch` calls `ctx.scale(dpr,dpr)`), so
  every check that read it was reporting the browser, not the page. `lib.mjs` now launches the
  bundled build, and `FE_BROWSER=chrome` is the explicit opt-in for the question "is this browser
  broken".
- **No seat-local build, at all.** The quick start and the pre-ship procedure prescribed
  `--base http://localhost:3800`, which needs `vite build` + `vite preview`. Owner rule 2026-10-17:
  a seat never runs `vite build`, `npm run build` or `npx vite` (measured ~4 GB RAM per build in this
  tree). The procedure now type-checks with `npx tsc --noEmit -p .` (verified rc 0, 17.5 s), runs the
  frontend's own `vitest` for what the toolkit does not cover, reviews the delta against the
  baseline from source, and compares production after the deploy — the deploy builds the bundle.
- **`SKILL.md` trap 18 and a dated measured state of production** carry the measurement and the
  deploy evidence: entry `assets/index-dUxpxWZA.js` (295,147 B), `/health` `0.0.38`, all 97
  referenced assets 200, 11/11 static routes rendering on the bundled engine, and the note that
  `compare` against the Oct 11 baseline is noisy by construction (`api.N.path` reordering plus the
  deliberate palette move), so its per-route `headings`/`fields`/`buttons` keys are the ones to read.

### Testing

- The store copy is byte-identical to the seat's delivered copy for the two edited files
  (`diff -rq` clean); the other files in the skill were already identical.
- `node --check lib.mjs` → rc 0; `launchBrowser` launched the bundled engine and reported
  `141.0.7390.37`, and `FE_BROWSER=chrome` launched `153.0.8010.36`.
- `production-walk.mjs capture --routes all` then `compare baseline` ran from the store copy against
  `https://www.4genthub.com` on the bundled engine: 11 routes captured, every route rendering.
- `npx tsc --noEmit -p .` in `agenthub-frontend` → rc 0 (the sanctioned type-check the skill now
  prescribes).
