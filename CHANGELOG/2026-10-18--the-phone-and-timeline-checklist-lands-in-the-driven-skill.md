# The phone and timeline checklist is in the skill, and the stale measured state is trimmed

Measured 2026-10-18 on `4genthub-min`. `skills/rooms/4genthub-min/fe-dev/fe-4genthub-driven` was
321 lines carrying two dated "Measured state" sections (2026-10-11 and 2026-10-17) and no procedure
for the two faults the owner reported this week. Three things were missing from it:

- the phone walk is no longer a throwaway script: it is `agenthub-frontend/e2e/phone-layout.spec.ts`
  (20 route-viewports at 375x667 and 320x568), and the skill still pointed only at its own 390-wide
  viewport, so the seat's own acceptance instrument was not in the procedure;
- a timeline row that says "Context updated" and nothing else is the owner's reported defect, and
  nothing in the skill said what a timeline row owes a reader;
- the deploy line is the `frontend` branch, not `main` - a fact discovered three times this week,
  each time after a fix that was "landed" was absent from the served bundle.

## Changed

- `skills/rooms/4genthub-min/fe-dev/fe-4genthub-driven/SKILL.md`
  - Quick start gains step 0, the phone spec; the viewport section names the room's two widths and
    says the phone verdict is the spec, not this toolkit's 390 emulation.
  - New section "Pre-merge checklist: phone and timeline" - eight phone lines and seven timeline
    lines, each a command or a measurement, with the measured min-content floors (550, 549, 634) and
    the 40px touch floor named.
  - The two dated "Measured state" sections collapse into one dated 2026-10-18: what the deploy line
    is, where the phone verdict lives with its 8/12 baseline, which engine to run, and the four
    2026-10-11 findings explicitly marked "not re-measured since". The bundle-name archaeology
    (`index-dUxpxWZA.js`, 295,147 B, the rigd witness chunks) is dropped: it described a bundle
    nobody can fetch any more.
  - Trap 15 gains the amendment the phone spec paid for: a character floor alone let a data-driven
    route pass by racing, so readiness is floor + a 750ms /api quiet window.
  - Traps 19-22 added: the deploy line is a branch and not always `main`; a push is not a deploy and
    the served entry asset plus a grep is the cheapest proof; a size class on the inner span is not
    the control's box (39 = 36 + 2x1.5px); `overflow-x-hidden` is never a responsive fix, because
    `main` being `overflow-y-auto` means a too-wide page scrolls inside the pane and leaves
    `window.scrollX` at 0.
