#!/usr/bin/env node
// check-responsive.mjs - does the page fit a desktop (1440), a laptop (1280) and an emulated phone (390)?
//
// Contract: no sideways drag, a correct viewport meta, and the entry buttons visible and inside the
// viewport. Overflow is tested BEHAVIORALLY: scrollTo(99999, y) then read scrollX. `scrollWidth >
// innerWidth` is not a test: it false-positives by the scrollbar width and is blind to layout-viewport
// expansion. Small tap targets (<44px) on the phone are reported, not failed.
import { VIEWPORTS, launchBrowser, parseArgs, reporter, target, waitForReady } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const site = await target(flags)
const browser = await launchBrowser(flags)
const routes = (typeof flags.routes === 'string' ? flags.routes : '/').split(',').map((s) => s.trim())

for (const [name, ctx] of Object.entries(VIEWPORTS)) {
  const context = await browser.newContext({ ...ctx })
  const page = await context.newPage()
  for (const route of routes) {
    try {
      await page.goto(`${site.base}${route}`, { waitUntil: 'load', timeout: 60_000 })
      await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
      if (!(await waitForReady(page))) { r.fail(`${route} @ ${name}: never reached the readiness floor - not measured`); continue }
      const m = await page.evaluate(() => {
        window.scrollTo(99999, 0)
        const dragX = window.scrollX
        window.scrollTo(0, 0)
        const meta = document.querySelector('meta[name=viewport]')?.getAttribute('content') ?? ''
        const buttons = [...document.querySelectorAll('button, [role=button]')]
          .filter((b) => { const q = b.getBoundingClientRect(); return q.width > 0 && q.height > 0 })
          .map((b) => { const q = b.getBoundingClientRect(); return { id: b.id || b.textContent.trim().slice(0, 20), w: Math.round(q.width), h: Math.round(q.height), right: Math.round(q.right) } })
        return { dragX, meta, innerWidth: window.innerWidth, buttons }
      })
      if (m.dragX === 0) r.pass(`${route} @ ${name} (${m.innerWidth}px): no sideways drag`)
      else r.fail(`${route} @ ${name}: page drags sideways by ${m.dragX}px (innerWidth ${m.innerWidth})`)
      if (!/width=device-width/.test(m.meta)) r.fail(`${route} @ ${name}: viewport meta is "${m.meta}", needs width=device-width`)
      const out = m.buttons.filter((b) => b.right > m.innerWidth + 1)
      for (const b of out.slice(0, 5)) r.fail(`${route} @ ${name}: button "${b.id}" ends at x=${b.right}, past the ${m.innerWidth}px viewport`)
      if (name === 'mobile') {
        const small = m.buttons.filter((b) => b.h < 44 || b.w < 44)
        if (small.length) r.info(`${route} @ mobile: ${small.length} button(s) under 44px (reported, not failed): ${small.slice(0, 5).map((b) => `${b.id} ${b.w}x${b.h}`).join(', ')}`)
      }
    } catch (err) {
      r.fail(`${route} @ ${name}: ${String(err).split('\n')[0]}`)
    }
  }
  await context.close().catch(() => {})
}
await browser.close().catch(() => {})
await site.close()
r.done('check-responsive')
