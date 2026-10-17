---
name: fe-4genthub-driven
description: "Recheck the 4genthub frontend where it actually runs — production or a local build — as a real signed-in user with the shared e2e account. A measured toolkit, not a checklist of opinions: the front door, backend-call truth, theme in both color modes, WCAG AA contrast, motion under prefers-reduced-motion, CRUD-animation wiring, and responsive layout on phone, laptop and desktop. Run the suite before shipping a frontend change, and diff a fresh capture against the committed baseline after deploying."
---

# fe-4genthub-driven

The frontend's job is to be **correct against the backend, tasteful in its color logic, helpful in
its animation, and usable on a telephone and on a PC**. This skill proves each of those with a
browser instead of assuming it. Every number in this document was measured on
https://www.4genthub.com on 2026-10-11 — the checks were run until they were honest (many of the
traps below were bugs in the checks themselves, found by disbelieving a result).

The walk is **read-only**: no token is minted, no room or seat or session is created, no setting is
changed. The account is read inside the process from the repo-root `e2e-test-account` file and is
never printed, logged, or committed.

## Quick start

```bash
# 1. The whole check — eight checks, one summary, exit 1 when any check failed.
node checks/all.mjs --fe /path/to/agenthub-frontend

# 2. NO LOCAL BUILD. Owner rule (2026-10-17): a seat never runs `vite build`, `npm run build` or
#    `npx vite` (local builds here cost ~4 GB RAM each and took the box to 4 GB free). Type-check
#    with `npx tsc --noEmit -p .`; the deploy builds and serves the bundle, and every browser check
#    below runs against a DEPLOYED origin (production, or a build some other system served).

# 3. Capture what an origin actually rendered (JSON only — never commit screenshots of signed-in pages).
node production-walk.mjs capture --base https://www.4genthub.com --out /tmp/cap --label production --routes all --no-shots --fe /path/to/agenthub-frontend

# 4. Diff today's rendering against the committed baseline (exit 1 on any structural difference).
node production-walk.mjs compare baseline /tmp/cap
```

The `--fe` checkout is needed for exactly two things: it hosts `@playwright/test` and the repo-root
`e2e-test-account` file (override with `--account <file>` or `E2E_ACCOUNT_FILE`). Any checkout of
agenthub-frontend works; the browser is Playwright's **bundled Chromium** (`FE_BROWSER=chrome` opts into
the installed Google Chrome — see trap 18).

## What each check answers

| Check | The question it answers | A FAIL means |
|---|---|---|
| [checks/check-login.mjs](checks/check-login.mjs) | Does the anonymous front door work — all six public routes, the login form, the password toggle, the forgot-password and signup links? | A signed-out visitor meets a broken or missing entry point. |
| [checks/check-nav.mjs](checks/check-nav.mjs) | Do all sixteen static routes load on a signed-in session with **zero** console errors and zero failed requests? | A route is broken or noisy. `/docs` and `/help-setup` are shell-exempt (they render without the top bar by design). |
| [checks/check-backend.mjs](checks/check-backend.mjs) | Did every `/api/` call the pages made come back 200, from the one expected origin? | The frontend is asking the backend something it cannot answer, or asking the wrong host. Post-sign-out WebSocket teardown noise is excluded by phase. |
| [checks/check-responsive.mjs](checks/check-responsive.mjs) | On desktop (1440), laptop (1280) and an emulated phone (390): does the page fit, is there a shell affordance (hamburger/sidebar), is the viewport meta right? | A form factor the app claims to serve is unusable — content drags sideways or the navigation is unreachable. |
| [checks/check-theme.mjs](checks/check-theme.mjs) | First visit: does the system's dark/light preference decide the surface? Does a saved preference outrank the system? Does the toggle flip the painted surface without writing to the API? | The color layer ignores the user — or the toggle secretly writes to the backend. |
| [checks/check-motion.mjs](checks/check-motion.mjs) | Under `prefers-reduced-motion: reduce`, does any visible element keep an animation >500ms or loop forever? | A user who asked for less motion is still shown looping decoration (taste skill section 6.B). |
| [checks/check-contrast.mjs](checks/check-contrast.mjs) | Every text run against its **effective** background: WCAG AA (4.5:1 normal, 3:1 large), computed by ratio, not by eye. | Text a user with ordinary eyesight has to squint at. |
| [checks/check-crud-animation.mjs](checks/check-crud-animation.mjs) | For every entity × create/update/delete: is the class the applier builds defined in shipped CSS, does applying it start a real animation of the registry's duration, is it gated under prefers-reduced-motion, and does nothing fire at rest? | A state-change animation silently cannot play — a renamed class, a missing rule, or a class removed before its animation ends. |

A known run against production on 2026-10-11 ends with four failing checks — contrast, motion,
responsive and CRUD animation (whose seven failures are: three dead `branchRow*` classes, three
dead `projectRow*` classes, one truncated subtask create). Those are the findings below, not
toolkit bugs. Exit code 0 means production passed everything.

## The three viewports — a phone is not a small laptop

[lib.mjs](lib.mjs) carries one `VIEWPORTS` table every check spreads into `browser.newContext`:

- **desktop** — 1440×900, the design target
- **laptop** — 1280×800, the honesty check: this is the narrowest common laptop, and it is where
  the shell's fixed min-width (measured ~1351px) shows up as a 71px sideways drag
- **mobile** — 390×844 with `isMobile`, `hasTouch` and `deviceScaleFactor: 2`, so a phone's real
  behavior (`width=device-width` honored, tap targets, the hamburger instead of the sidebar) is
  what gets measured

Two traps to keep in mind if you add a viewport: Playwright **silently ignores** a bare
`{ width, height }` spread into `newContext` (every number comes back at the 1280×720 default —
the first responsive run reported "innerWidth 1280" for a 390 phone), and without `isMobile` a
"phone" keeps the desktop layout viewport, hiding mobile-only breakage entirely.

## Procedure

**Before shipping a frontend change (owner rule: no local build):**
1. Type-check only: `npx tsc --noEmit -p .` from the frontend checkout, plus the frontend's own
   `npx vitest run` for the tests this skill does not cover. Do NOT run `vite build`, `npm run build`
   or `npx vite` — a seat-local build is forbidden (2026-10-17, 4 GB each); the deploy builds it.
2. Reason about the change against [baseline/production.json](baseline/production.json) from the
   source, not from a local rendering: what the route renders today is the baseline, and your diff
   is the delta you intend.
3. Ship (the owner/lead deploys). Then capture production and compare: what you see should be what
   you intended, and every diff should be attributable to a commit.

**When production is suspicious (a report, a regression, a deploy):**
1. Run the suite. The per-check output names routes, selectors and measured numbers — that output
   *is* the bug report; quote it.
2. For anything visual, `production-walk.mjs capture --no-shots` + `compare` against the baseline
   tells you what changed *structurally* (headings, fields, buttons, links, tokens) since 2026-10-11.
   Token/status moves are listed separately from structural ones — the token layer moves by design.

**Updating the baseline** — only after a deliberate, reviewed change: capture with `--no-shots`,
read the diff, then replace `baseline/production.json` and note the date here. Never blind-copy:
the baseline's whole value is that a human confirmed what it records.

**Dynamic routes** (a project, a branch, a task, a seat, a session) need an id and are out of the
static walk. Pass them explicitly: `--routes /seats/<id>`.

## Measured state of production — 2026-10-17 (new deploy, new engine)

**The deploy moved and so did the browser.** `www.4genthub.com` serves entry
`assets/index-dUxpxWZA.js` (295,147 B) instead of the Oct 8 `index-BMd9UnWH.js`, `/health` reports
`0.0.38`, and all 97 assets the entry and its chunks name return 200. Captured on Playwright's
bundled Chromium 141: **11/11 static routes render** — `/login` (3 fields, 3 buttons, 2 links),
`/dashboard` first heading "DECISION REQUESTS", `/tokens` (3 fields, 18 buttons — the presets are now
Client connector / Client MCP / Clear All, Full Access and Read Only gone), `/seats` (heading
"Seats"; sections Company version policy, Create a room, Rooms, Bridge machines), `/seats/authoring`
(20 fields, 225 buttons), `/sessions` (empty state, 30 buttons — no session is registered for the
account), `/topology`, `/feedback`, `/profile`, `/docs`, `/help-setup`.

**Run this toolkit on the bundled engine (the default now).** Under headless Google Chrome 153
every canvas page wedges (trap 18) and `/login` — built directly on `FallingGlitch`'s
`ctx.scale(dpr,dpr)` — reads as a dead page. `FE_BROWSER=chrome` is only for the question "is this
browser broken": headed Chrome 153 renders the same login page normally, so the app is not at fault.

**The rigd witnesses are on the live surface:** `seat_state` in `SessionsPage-C2eQtlu2.js` with the
ruled precedence (`status==="offline" ? "offline" : seat_state==="stopped" ? "stopped" : "live"`),
`agent_session` in `useRealtimeSync-Bs0iYGI9.js`, "Withheld locally" in `SessionsPage-C2eQtlu2.js`,
and no `SeatInputBox` anywhere in the served bundle.

**`compare` against the Oct 11 baseline is noisy by construction** — 776 entries, dominated by
positional `api.N.path` reordering and the deliberate palette move (`#3b82f6` → `#4f46e5`, etc.).
Read the per-route `headings`/`fields`/`buttons` keys, not the `api.N` keys, before calling anything
structural. The baseline was NOT replaced: a human should review the route-level diffs first.

## Measured state of production — 2026-10-11

**Passing (verified, not assumed):** 16/16 static routes return 200 with zero console errors; the
single API origin is `https://api.4genthub.com`; the login POST and `/health` answer 200; 47
distinct endpoints observed across the walk, all 200; on mobile every route fits (no sideways drag,
no scrollbar false positives) and the "Toggle mobile menu" affordance is present; the theme layer
passes everything — system dark paints `rgb(15, 23, 42)` (luminance 0.009), system light paints
white, a saved preference outranks the system, and the toggle provably makes no non-GET API call.

**Open findings for fe-dev:**

1. **Contrast — 7 WCAG AA violations** (AA needs 4.5:1), each with the color that produced it:
   - `/login` (anonymous): "Email Address" 3.68:1 `rgb(59, 130, 246)` (16px/400, `#email-label`),
     "Sign In" 3.68:1 white-on-blue (14px/500, `.MuiButton-root`), "Forgot password?" 3.68:1
     (14px/400), "Don't have an account? Sign Up" 3.68:1 (14px/400) — all `#3b82f6`, which
     measures 3.68:1 in both directions (as text on white and white text on it)
   - `/dashboard`: "Connected" 3.30:1 `rgb(22, 163, 74)` (`#16a34a` green, 12px/500,
     `span.text-xs.font-medium`), "Dashboard" 3.68:1 `#3b82f6` ×2 (14px/500, `span.text-sm.font-medium`)
   - Measured fix candidates: blue `#2563eb` → 5.17:1 (or `#1d4ed8` → 6.70:1); green `#15803d` →
     5.02:1. One token change per color family clears every listed run — re-run the check to confirm.

2. **Motion — `/dashboard` violates reduced-motion.** `shimmer-spin` (2500ms, infinite) runs on
   4 × `div.absolute.inset-0` even under `prefers-reduced-motion: reduce`. Gate it behind the
   media query (the taste skill section 6.B rule). `/login` is clean — only 10ms MUI autofill
   animations remain, and the longest normal transition is 300ms.

3. **Responsive — the shell does not fit a 1280 laptop.** The signed-in shell's minimum width
   measures ~1351px (culprits: `div.flex.items-center` spanning x 137–1351, and the account-chip
   row at x 1181–1351 — `div.relative.inline-block`, `div.cursor-pointer`, `button.flex.items-center`),
   so **every** signed-in shell route — `/dashboard`, `/tokens`, `/seats`, `/seats/authoring`,
   `/sessions`, `/topology`, `/feedback`, `/profile` — drags sideways by 71px at 1280×800 (client
   1280, inner 1280, scroll 1351; identical on all eight). Desktop and mobile are both clean; the
   fix is to let the top bar shrink (truncate, wrap, or hide the chip's email before 1280px), not
   to hide the overflow.

4. **CRUD state-change animations — three entities mis-wired, one truncated.** Measured per entity
   × create/update/delete: class defined in shipped CSS? applying it starts a real engine
   animation? registry removal duration ≥ CSS duration? reduce-gated? at rest clean?
   - **task** and **seat** rows play correctly: create 500ms (the registry holds the class 800ms —
     a benign 300ms linger), update 1700ms, delete 800ms — engine-measured, reduce-gated.
   - **subtask create is cut**: the CSS runs 1000ms, but AnimationFactory removes the class after
     800ms, truncating the slide-in for its last 200ms. Align the two (subtask CSS 0.5s, or the
     registry 1000ms).
   - **branch animates nothing**: the factory builds `branchRowCreateAnimation` but the CSS
     defines `branchCreateAnimation` — the name drifted (no "Row"), so all three branch classes
     are dead. Rename one side so they agree.
   - **project animates nothing**: the factory builds `projectRow*Animation`; no project CSS exists
     at all. Either add the rules or stop triggering.
   - the registered `'complete'` type is inert: no CSS defines it, no hook calls it — implement or
     remove.
   - At rest on `/seats` and `/dashboard`, no row carries any CRUD class 2.5s after load — the
     trigger fires on the state change, not on render (verified).
   - A read-only walk cannot prove a real CRUD event *calls* animate(); that link belongs to the
     frontend's own tests (`websocket-animations-e2e.test.tsx`, `taskUpdateAnimation.test.ts`).

## Traps already paid for — disbelieve a result before you trust it

Each of these cost a false result in this toolkit's own development. They are kept here because
the same traps will catch the next person writing a frontend measurement.

1. **`waitForURL` resolves before the view re-renders** (both directions: after sign-in the URL is
   `/dashboard` while the login form is still on screen; after sign-out likewise). Wait for the
   Dashboard text / the Email Address box itself.
2. **`storageState` replay carries localStorage** — including the pinned `theme` key. Replaying a
   session decides the theme, and the measurement proves nothing. Strip `theme` from replayed
   origins. And measure anonymous routes in an **anonymous** context: `/login` under a session
   *is* `/dashboard` — a check that measures it after signing in labels the post-login view
   "/login" (this happened).
3. **A bare `{ width, height }` in `newContext` is silently ignored** — the viewport option is
   `{ viewport: { … } }`. The first responsive run "measured" 1280 for every viewport because of
   this.
4. **The account chip's first text line is just the avatar letter** ("Q"). Identity detection must
   test the full `innerText` for `@`, not `.split('\n')[0]` — seven phantom "no account chip"
   failures came from this.
5. **`scrollWidth > innerWidth` is not a usable overflow test**: it false-positives by the
   scrollbar width (390 + 17 = 407 on `/help-setup`) and is blind to layout-viewport expansion
   (`/docs` on mobile reports innerWidth 515 while the page does not drag at all). The honest test
   is behavioral: `window.scrollTo(99999, y)` then read `window.scrollX`.
6. **A transparent `body` background means `html` paints the page** (CSS canvas propagation).
   Measuring the transparent body reads luminance 0 and lets a white page "pass" as dark. The
   painted layer decides: body if its alpha > 0, else html.
7. **`/docs` and `/help-setup` render without the shell** — no account chip. Sign-out must start
   from `/dashboard`, or `production-walk` prints "sign-out control not found" (fixed in both; the
   lesson generalizes: never assume the last-visited route has the chrome you need).
8. **WebSocket double-close logs appear after sign-out** — two INFO lines from the realtime
   socket's teardown. The collector tags every event with a phase so teardown noise cannot fail a
   check that measured the page before it.
9. **`parseArgs` rule: a `--flag` followed by another flag is boolean `true`** — `--no-shots --fe x`
   must not eat `--fe`. Before this rule, `--no-shots` swallowed the path and the walk died with
   "no frontend checkout found" although `--fe` had been passed.
10. **Console text is redacted before storage** (`token=…` and JWTs): the realtime socket carries a
    JWT in its query string, and a transcript or log must never hold one.
11. **WCAG 2.5.8 exempts inline links** from touch-target size; only `button, [role=button]` are
    measured, and small targets are reported, not failed (spacing exception).
12. **MUI labels carry a required asterisk** — sign in by role (`getByRole('textbox', { name: 'Email Address' })`),
    never by exact visible label text.
13. **A modern Chrome `CSSStyleRule` also exposes `.cssRules`** (CSS nesting — an empty list for a
    flat rule, but an object, hence truthy). Walking `if (rule.cssRules) recurse else record`
    classified every plain rule as a container and found nothing, silently: the check reported all
    15 CRUD classes "defined in no shipped CSS" while the stylesheet really ships them. A style
    rule is decided by `selectorText` first; a container by `cssRules.length`.
14. **`page.evaluate(fn)` serializes the function alone, not its module** — a module-level helper
    called inside it is a `ReferenceError` in the page. Worse, a `try` around the whole per-sheet
    walk turned that crash into `1 stylesheet unreadable`: a plausible statistic standing in for a
    bug. Page functions must carry everything they read, and a catch must cover exactly the
    operation that can legitimately fail (here: the cross-origin `cssRules` access) — never the
    analysis that follows it.
15. **Neither `load` nor `networkidle` is readiness.** The app is client-rendered: the document
    finishes loading while the view still holds its pre-data placeholder, and the `networkidle`
    wait is `.catch(() => {})`-swallowed, so the read lands on the placeholder. Measured
    2026-10-11 on the local chain (vite → 127.0.0.1:8000 relay → production): the SAME route
    rendered 6029 characters in one viewport and 10 in another viewport of the same run — a race,
    not a route defect. And a check that reads early does not fail loudly: `check-contrast` read
    `/dashboard` at 10 characters and printed `PASS /dashboard: all 1 measured text runs meet AA`,
    a false green, while the rendered page measures 23 runs. Every check that reads a rendered page
    now calls `waitForReady` (lib.mjs) and, when the floor is not reached within 30s, FAILS naming
    that timeout and does not read past it: `check-login`, `check-contrast`, `check-motion`,
    `check-theme`, `check-backend`, `check-crud-animation` — and `production-walk.mjs` stops the
    capture with `die`, because a pre-data capture must never become a baseline (it records
    structure, not lengths, so a placeholder reads as a diff of everything). Two of the readers
    annotate rather than refuse the read: `check-nav` (`:40`, `:52`) and `check-responsive` (`:121`,
    `:132`) still read the page when `ready === false` and append the reason to their own `chars < 120`
    FAIL, which still exits non-zero — so their row can be a measurement of a page that never
    reached the floor. The goto-level warm-up and sign-out navigations are deliberately left ungated:
    they assert nothing about the page.
16. **A report is only as wide as the map it reads.** `check-crud-animation` collected CSS names
    from the CRUD matrix, then filtered the five `*RowCompleteAnimation` classes out of that same
    map and reported them "defined in no CSS and called from no hook/component". The first clause
    was a lookup against names nobody had asked for; the second described code the walk never
    inspects. The classes are live (`AnimationFactory` + the five Complete components). Collect
    what you report on, and make every clause say exactly what was measured.

17. **The served stylesheet is not the page's CSS.** A static grep of the served `index-*.css` cannot
    settle a computed-style question: it is one sheet of the page's set, not the set. `/dashboard`
    carried **38 stylesheets — the linked `index-DW8gSl2a.css` plus 37 runtime-injected `<style>`** —
    and both `.shimmer-spin-layer` rules lived in an injected one (the `2.5s linear infinite
    shimmer-spin` base rule, and `animation: none` inside `@media (prefers-reduced-motion: reduce)`).
    So the served CSS carried **0** `.shimmer-spin-layer` while reduce-mode `div.absolute.inset-0`
    computed `animationName: none` — and this trap cost a wrong sentence in a shipped evidence
    artifact ("no CSS rule gates `shimmer-spin` … the gate is in the app path"), caught by the
    reviewer's cross-check: the observable was right, the location wrong. Distinct from 16: 16 is "a
    report is only as wide as the map it reads"; 17 is "the served stylesheet is not the page's CSS".
    **Do instead:** for a computed-style or gating question, enumerate `document.styleSheets` in the
    page (count them; `ownerNode` separates linked from injected) and walk every sheet's rules — a
    rule with `selectorText` is a style rule, one with `cssRules.length` and no `selectorText` is a
    container — recording the `CSSMediaRule.conditionText` chain over each match. Then quote the sheet
    that actually carries the rule, and say plainly when the served asset does not — keeping only the
    rules that **match the element**: its matched rules are the ones whose `selectorText` matches that
    node and whose declarations set the property in question, and a grep hit in the served asset is not
    one of them. That asset (`index-DW8gSl2a.css`, 162,357 bytes, re-checked 2026-10-12) carries 19
    `/shimmer/i` occurrences in **12 rules** — it is one minified line, so the unit is the
    `}`-delimited segment, not a line; three of the twelve are the ungated `.shimmer-modern` family —
    and **0** `.shimmer-spin-layer`: a hit there invites both the wrong rule and the wrong sheet.

18. **Headless Google Chrome 153 wedges its renderer on the first canvas 2D draw** — a browser
    defect that reads exactly like a page regression. Measured 2026-10-17 on a canvas page with no
    app code in the path (1280×800, `channel: 'chrome'`, headless): `getContext('2d')` alone is fine
    and `ctx.font` + `ctx.measureText('M')` is fine, while `ctx.scale(1,1)`, `ctx.scale(2,2)`,
    `ctx.setTransform(1,0,0,1,0,0)` and `ctx.fillRect` each leave the renderer permanently
    unresponsive — every CDP call to the page then times out (`Page.getFrameTree`, `DOM.getDocument`,
    `Runtime.evaluate`, `Performance.getMetrics`), no `crash` event fires, and a CDP trace shows
    **no long main-thread frame**, so the usual "busy thread" reading is wrong. `--disable-gpu` and
    `--use-angle=swiftshader` do not help; **headed** Chrome 153 on the same page, and bundled
    Chromium 141 headless, are both fine. Consequence on 4genthub: `/login` is the only canvas user
    (`FallingGlitch` calls `ctx.scale(dpr,dpr)`), so under headless Chrome 153 it never reaches the
    120-character floor and every check that reads it fails. The app is NOT broken for users. Do
    instead: launch the bundled build (the default in `lib.mjs`); keep `FE_BROWSER=chrome` for the
    question "is this browser broken", and read a wedged tab as the browser's verdict, never the
    page's.

## Rules

- **Read-only against production.** No writes, no token minted, no resource created. The only
  non-GET requests this toolkit may ever make are the sign-in POSTs, and nothing else — if a new
  check needs a write, it does not belong in this skill.
- **The account is never printed.** Use `e2e-test-account` at the repo root; never echo a value in
  a command, a log, a title, a screenshot or a transcript.
- **Baselines are JSON only.** Screenshots of signed-in pages never enter the repo. Identity is
  recorded as `userChip.present` — a boolean, not a name.
- **Quote the measured line** when reporting: selector, px/weight, ratio. "Contrast is bad" is not
  a finding; "3.30:1, 12px/500, span.text-xs.font-medium" is.

## Files in this skill

| File | Job |
|---|---|
| [lib.mjs](lib.mjs) | The shared core: routes table, viewports, argument parsing, account reading, browser launch, sign-in/out, `waitForReady` (the readiness floor a client-rendered view needs before anything is read from it), the page watcher (API calls, console errors, redaction), color math. |
| [production-walk.mjs](production-walk.mjs) | `capture` — sign in, walk routes, record what rendered; `compare` — structural diff of two captures. |
| [checks/all.mjs](checks/all.mjs) | The runner: each check in its own process, one summary, one exit code. |
| [checks/check-login.mjs](checks/check-login.mjs) · [checks/check-nav.mjs](checks/check-nav.mjs) · [checks/check-backend.mjs](checks/check-backend.mjs) · [checks/check-responsive.mjs](checks/check-responsive.mjs) · [checks/check-theme.mjs](checks/check-theme.mjs) · [checks/check-motion.mjs](checks/check-motion.mjs) · [checks/check-contrast.mjs](checks/check-contrast.mjs) · [checks/check-crud-animation.mjs](checks/check-crud-animation.mjs) | The eight checks, above. |
| [baseline/production.json](baseline/production.json) | What production rendered on 2026-10-11 — 11 routes (`/login` + the ten signed-in static routes). The `compare` target. |

The design rules this toolkit enforces (color lock, shape lock, the reduced-motion rule) live in
the seat's taste skill, one directory over: `../taste-skill/SKILL.md`.
