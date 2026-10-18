#!/usr/bin/env node
// check-motion.mjs - what moves, and what still moves when the user asked it not to.
//
// Contract (WCAG 2.3.3): under prefers-reduced-motion: reduce, no VISIBLE element keeps an animation longer
// than 500ms or an infinite iteration. Short hover transitions are allowed; looping decoration must collapse.
import { VIEWPORTS, launchBrowser, parseArgs, reporter, target, waitForReady } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const site = await target(flags)
const browser = await launchBrowser(flags)
const routes = (typeof flags.routes === 'string' ? flags.routes : '/').split(',').map((s) => s.trim())

const probe = () => {
  const describe = (el) => (el.tagName.toLowerCase() + (el.id ? `#${el.id}` : '') + (el.className && typeof el.className === 'string' ? `.${el.className.trim().split(/\s+/).slice(0, 2).join('.')}` : '')).slice(0, 60)
  const animations = []
  for (const el of document.querySelectorAll('body *')) {
    const rect = el.getBoundingClientRect()
    if (rect.width === 0 || rect.height === 0) continue
    const s = getComputedStyle(el)
    if (s.animationName !== 'none') {
      animations.push({ sel: describe(el), ms: Math.round(Math.max(...s.animationDuration.split(',').map(parseFloat)) * 1000), iterations: s.animationIterationCount, name: s.animationName.slice(0, 40) })
    }
  }
  return { animations, reduceMatches: matchMedia('(prefers-reduced-motion: reduce)').matches }
}

for (const route of routes) {
  const seen = {}
  for (const [label, reducedMotion] of [['normal', 'no-preference'], ['reduce', 'reduce']]) {
    // a fresh context per mode: one mode's animations stay out of the other's numbers
    const context = await browser.newContext({ ...VIEWPORTS.desktop, reducedMotion })
    const page = await context.newPage()
    try {
      await page.goto(`${site.base}${route}`, { waitUntil: 'load', timeout: 60_000 })
      await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
      if (!(await waitForReady(page))) r.fail(`${route} under ${label}: never reached the readiness floor - not measured`)
      else seen[label] = await page.evaluate(probe)
    } catch (err) {
      r.fail(`${route} under ${label}: ${String(err).split('\n')[0]}`)
    }
    await context.close().catch(() => {})
  }
  if (!seen.normal || !seen.reduce) continue
  r.info(`${route}: ${seen.normal.animations.length} element(s) animate at rest`)
  for (const a of seen.normal.animations.slice(0, 6)) r.note(`animates: ${a.sel} - ${a.name} ${a.ms}ms ${a.iterations}`)
  if (seen.reduce.reduceMatches !== true) r.fail(`${route}: prefers-reduced-motion emulation did not take effect`)
  const survivors = seen.reduce.animations.filter((a) => a.ms > 500 || /infinite/.test(a.iterations))
  if (!survivors.length) r.pass(`${route}: under reduced motion no long or looping animation survives`)
  for (const a of survivors.slice(0, 10)) r.fail(`${route}: reduced motion still runs ${a.sel} - ${a.name} ${a.ms}ms ${a.iterations}`)
}
await browser.close().catch(() => {})
await site.close()
r.done('check-motion')
