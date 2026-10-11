#!/usr/bin/env node
// check-motion.mjs — what actually moves, and what still moves when the user asked it not to.
//
// Contract (the reduced-motion rule, taste skill section 6.B, WCAG 2.3.3):
//   under prefers-reduced-motion: reduce, no VISIBLE element keeps an animation longer than
//   500ms or an infinite iteration. Short hover transitions (~150ms feedback) are allowed;
//   looping decorations, parallax and scroll choreography are the ones that must collapse.
// It reports the motion inventory either way: how many elements transition, how many animate,
// the longest durations, and the first few selectors — the material to judge whether the motion
// is motivated, which no script can decide.
import { ROUTES, VIEWPORTS, baseURL, launchBrowser, parseArgs, readAccount, reporter, signIn } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const pages = (flags.routes ?? '/login,/dashboard').split(',').map((s) => s.trim())

const probe = () => {
  const describe = (el) => (el.tagName.toLowerCase() + (el.className && typeof el.className === 'string' ? `.${el.className.trim().split(/\s+/).slice(0, 2).join('.')}` : '')).slice(0, 60)
  const transitions = []
  const animations = []
  for (const el of document.querySelectorAll('body *')) {
    const rect = el.getBoundingClientRect()
    if (rect.width === 0 || rect.height === 0) continue
    const s = getComputedStyle(el)
    const maxTransition = Math.max(...s.transitionDuration.split(',').map(parseFloat), 0)
    if (maxTransition > 0) transitions.push({ sel: describe(el), ms: Math.round(maxTransition * 1000), props: s.transitionProperty.slice(0, 48) })
    if (s.animationName !== 'none') {
      animations.push({
        sel: describe(el),
        ms: Math.round(Math.max(...s.animationDuration.split(',').map(parseFloat)) * 1000),
        iterations: s.animationIterationCount,
        name: s.animationName.slice(0, 40),
      })
    }
  }
  return { transitions, animations, reduceMatches: matchMedia('(prefers-reduced-motion: reduce)').matches }
}

const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)
const warm = await browser.newContext({ ...VIEWPORTS.desktop })
const warmPage = await warm.newPage()
let sessionOk = true
try {
  await warmPage.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  await signIn(warmPage, account)
} catch (err) {
  sessionOk = false
  r.fail(`could not establish a session: ${String(err).split('\n')[0]}`)
}
const state = await warm.storageState()
await warm.close().catch(() => {})

// A context per (mode, route): replaying the signed-in state into an anonymous route would measure
// the page it redirects to, not the route asked for (/login under a session is /dashboard), and a
// fresh context per route also keeps one route's animations out of the next route's numbers.
const read = {}
for (const [label, reducedMotion] of [['normal', 'no-preference'], ['reduce', 'reduce']]) {
  read[label] = {}
  for (const route of pages) {
    if (!ROUTES.anonymous.includes(route) && !sessionOk) { r.info(`${route} under ${label}: skipped, no session`); continue }
    const context = await browser.newContext({ ...VIEWPORTS.desktop, reducedMotion, storageState: ROUTES.anonymous.includes(route) ? undefined : state })
    const page = await context.newPage()
    try {
      await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
      await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
      read[label][route] = await page.evaluate(probe)
    } catch (err) {
      r.fail(`${route} under ${label} failed to load: ${String(err).split('\n')[0]}`)
    }
    await context.close().catch(() => {})
  }
}
await browser.close().catch(() => {})

for (const route of pages) {
  const normal = read.normal[route]
  const reduced = read.reduce[route]
  if (!normal || !reduced) continue

  r.info(`${route}: ${normal.transitions.length} elements transition, ${normal.animations.length} animate (running unchanged: ${normal.reduceMatches === false})`)
  const longest = [...normal.animations].sort((a, b) => b.ms - a.ms)[0]
  const longestT = [...normal.transitions].sort((a, b) => b.ms - a.ms)[0]
  if (longestT) r.info(`${route}: longest transition ${longestT.ms}ms on ${longestT.sel} (${longestT.props})`)
  if (longest) r.info(`${route}: longest animation ${longest.ms}ms ${longest.iterations} on ${longest.sel} (${longest.name})`)
  for (const a of normal.animations.slice(0, 6)) r.note(`animates: ${a.sel} — ${a.name} ${a.ms}ms ${a.iterations}`)

  if (reduced.reduceMatches !== true) r.fail(`${route}: prefers-reduced-motion emulation did not take effect in the browser`)
  const survivors = reduced.animations.filter((a) => a.ms > 500 || /infinite/.test(a.iterations))
  if (survivors.length === 0) {
    r.pass(`${route}: under reduced motion no long or looping animation survives (${reduced.animations.length} short animations remain)`)
  } else {
    for (const a of survivors.slice(0, 10)) r.fail(`${route}: reduced motion still runs ${a.sel} — ${a.name} ${a.ms}ms ${a.iterations}`)
    if (survivors.length > 10) r.note(`… and ${survivors.length - 10} more`)
  }
  const longTransitions = reduced.transitions.filter((t) => t.ms > 500)
  if (longTransitions.length) r.info(`${route}: reduced mode keeps ${longTransitions.length} transition(s) over 500ms (reported, not failed: transitions are user-triggered feedback)`)
}
r.done('check-motion')
