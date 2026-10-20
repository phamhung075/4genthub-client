# The lead's deploy rule names the `frontend` branch, not `main`

The lead's workflow skill body said "a push to `main` deploys production". It does not: a push to `main`
deploys nothing, and the app ships from the `frontend` branch.

## Why it was wrong

Measured 2026-10-20 by the architect (task `8f5d98d7`, ruling `jev=2026-10-20T20:22Z`, note
`seats/architect/DECISION-2026-10-20-deploy-ref.md`):

1. `main` was pushed at 20:08:52Z; production measured 20:22:41Z served `/assets/index-CHspHOsu.js` with
   `Last-Modified` 19:48:12Z - 14 minutes later, past the measured 4-11 minute CapRover build window, so the
   main push built nothing.
2. `origin/frontend` was `45a034df`, committed 19:33:02Z and unmoved since, so the 19:48:12Z build belongs to
   the frontend line.
3. `.github/workflows` holds only `ci.yml` (go, frontend, scripts) with no deploy step.
4. `ai_docs/operations/complete-operations-guide.md` (measured 2026-10-19) records that a push to `frontend`
   fires CapRover's build for app `4genthub-frontend`, and that a webhook 200 is delivery, not a build.

## What changed

`skills/rooms/4genthub-min/lead/lead-workflow/SKILL.md` - the durable source of the lead seat's `AGENTS.md`,
rendered at every seat start - now states that a push to `main` deploys nothing, that the app ships from
`frontend`, and that only the served entry proves a build:
`python3 scripts/check_served_frontend.py --base-url https://www.4genthub.com --expect-dist agenthub-frontend/build`
exits 0.

Committed by the lead, which the wrapper exempts from the docs-only check (owner ruling 2026-10-18).

## Not in this commit

`rust/agenthub-runtime/assets/seat-guard.ts` carries the same false claim in its header comment (`:6`) and in
the refusal string a seat receives (`:529`). No seat can change it: the guard's own `protected-paths` rule
refuses any write to `seat-guard.ts`, lead included - the file is the rulebook that restrains every seat.
That half is the owner's to apply; the guard is embedded with `include_str!`, so a corrected message reaches
seats only after the next client build and pin.
