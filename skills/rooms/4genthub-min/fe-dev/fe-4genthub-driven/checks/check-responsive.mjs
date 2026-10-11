#!/usr/bin/env node
// check-responsive.mjs — the app on a phone and on a PC, every route measured at both widths.
//
// Contract per route per viewport:
//   - <meta name="viewport" content="width=device-width…"> is present: without it a phone lays the
//     page out at a ~980px virtual width and shrinks it, and every number below becomes a lie
//   - the document does not scroll horizontally (scrollWidth <= innerWidth + 1px) — this is the
//     difference between a page and a wonky page on a handset
//   - at least 120 visible characters render (same floor as check-nav)
//   - no console error and no failed request while the route loads
//   - on signed-in routes the shell shows a way in (the account chip or a menu control), so a
//     collapsed sidebar cannot leave a phone with no navigation at all
// Measured and reported, not failed: the elements sticking out of a page that does overflow (the
// culprits, with coordinates), and — on mobile — buttons under 24x24 CSS px (WCAG 2.5.8 has a
// spacing exception no bounding box reveals, so this is a list to look at, not a verdict).
import { ROUTES, VIEWPORTS, baseURL, launchBrowser, parseArgs, readAccount, reporter, signIn, waitForReady, watchPage } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const routes = flags.routes && flags.routes !== 'all'
  ? flags.routes.split(',').map((s) => s.trim())
  : [...ROUTES.anonymous, ...ROUTES.signedIn]
const viewportNames = (flags.viewports ?? 'desktop,laptop,mobile').split(',').map((s) => s.trim())
for (const name of viewportNames) if (!VIEWPORTS[name]) r.fail(`unknown viewport "${name}" (known: ${Object.keys(VIEWPORTS).join(', ')})`)
const SHELL_EXEMPT = new Set(['/docs', '/help-setup'])

const probe = () => {
  const describe = (el) => {
    let out = el.tagName.toLowerCase()
    if (el.id) out += `#${el.id}`
    if (el.className && typeof el.className === 'string') out += `.${el.className.trim().split(/\s+/).slice(0, 2).join('.')}`
    return out.slice(0, 60)
  }
  const visible = (el) => {
    const rect = el.getBoundingClientRect()
    const s = getComputedStyle(el)
    return rect.width > 0 && rect.height > 0 && s.visibility !== 'hidden' && s.opacity !== '0' ? rect : null
  }
  const doc = document.documentElement
  // Can the user actually drag this page sideways? The scroll attempt is the honest signal:
  // innerWidth and scrollWidth differ by a scrollbar's width in mobile emulation (390 + 17 = 407
  // on a page that does not scroll sideways), so neither is a safe test on its own. Measured
  // 2026-10-11 on production.
  const startY = window.scrollY
  window.scrollTo(99999, startY)
  const sideScroll = Math.round(window.scrollX)
  window.scrollTo(0, startY)
  const wide = []
  for (const el of document.querySelectorAll('body *')) {
    if (wide.length >= 5) break
    const rect = visible(el)
    if (rect && (rect.right > window.innerWidth + 1 || rect.left < -1)) wide.push(`${describe(el)} [${Math.round(rect.left)}..${Math.round(rect.right)}]`)
  }
  const smallButtons = []
  const affordance = []
  for (const b of document.querySelectorAll('button, [role=button], a')) {
    const rect = visible(b)
    if (!rect) continue
    const label = (b.getAttribute('aria-label') || '').trim()
    // Match on the FULL text, display only the first 40 chars: the account chip's innerText is
    // three lines ("Q\nq987\nq987@yopmail.com") and a first-line-only test reads "Q", missing the
    // @ that identifies it (measured 2026-10-11 — seven false FAILs).
    const full = (b.innerText || '').replace(/\s+/g, ' ').trim()
    const text = full.slice(0, 40)
    const name = label || text
    // Only real controls: WCAG 2.5.8 exempts links inline in a sentence, and counting every
    // anchor as a touch target turns the report into noise (measured 2026-10-11).
    if (b.matches('button, [role=button]') && Math.min(rect.width, rect.height) < 24) smallButtons.push(`${describe(b)} ${Math.round(rect.width)}x${Math.round(rect.height)}`)
    // The chip read as text OR aria-label, never only one: a chip labelled "Open user menu" with
    // the email as its text was invisible to a name that stopped at the label (measured 2026-10-11).
    if (/@/.test(label) || /@/.test(full) || /menu|nav|hamburger|sidebar/i.test(`${label} ${text}`)) affordance.push(name)
  }
  return {
    innerWidth: window.innerWidth,
    clientWidth: doc.clientWidth,
    scrollWidth: doc.scrollWidth,
    sideScroll,
    viewportMeta: document.querySelector('meta[name="viewport"]')?.getAttribute('content') ?? null,
    chars: document.body.innerText.trim().length,
    wide,
    smallButtons: smallButtons.slice(0, 5),
    smallButtonCount: smallButtons.length,
    affordance: [...new Set(affordance)].slice(0, 3),
  }
}

const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)

// One session, replayed into the signed-in routes of both viewports; /login and the other
// anonymous routes are measured without it, as a first-time visitor would see them.
let sessionOk = true
const warm = await browser.newContext({ ...VIEWPORTS.desktop })
const warmPage = await warm.newPage()
try {
  await warmPage.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  await signIn(warmPage, account)
} catch (err) {
  sessionOk = false
  r.fail(`could not establish a session: ${String(err).split('\n')[0]}`)
}
const session = await warm.storageState()
await warm.close().catch(() => {})

const rows = []
for (const name of viewportNames) {
  const viewport = VIEWPORTS[name]
  if (!viewport) continue
  for (const route of routes) {
    const anon = ROUTES.anonymous.includes(route)
    if (!anon && !sessionOk) { r.info(`${name} ${route}: skipped, no session`); continue }
    const context = await browser.newContext({ ...viewport, storageState: anon ? undefined : session })
    const page = await context.newPage()
    const { consoleErrors, failed } = watchPage(page)
    const row = { viewport: name, route, clientWidth: null, innerWidth: null, scrollWidth: null, sideScroll: null, chars: 0, errors: consoleErrors.length, failed: failed.length }
    try {
      const response = await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
      await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
      // `networkidle` is not readiness for a client-rendered app: wait for the text the checks read.
      const ready = await waitForReady(page)
      const seen = await page.evaluate(probe)
      Object.assign(row, seen, { errors: consoleErrors.length, failed: failed.length })
      const status = response?.status() ?? null

      if (seen.viewportMeta === null) r.fail(`${name} ${route}: no <meta name="viewport"> — a phone renders this at a shrink-to-fit virtual width`)
      else if (!/width\s*=\s*device-width/.test(seen.viewportMeta)) r.fail(`${name} ${route}: viewport meta is "${seen.viewportMeta}", not width=device-width`)
      if (seen.sideScroll > 1) {
        r.fail(`${name} ${route}: the page can be dragged sideways by ${seen.sideScroll}px (client ${seen.clientWidth}, inner ${seen.innerWidth}, scroll ${seen.scrollWidth})`)
        for (const w of seen.wide) r.note(`sticks out: ${w}`)
      }
      if (seen.chars < 120) r.fail(`${name} ${route}: only ${seen.chars} visible characters rendered${ready ? '' : ' and the view never reached the readiness floor within 30s'}`)
      if (!anon && !SHELL_EXEMPT.has(route) && seen.affordance.length === 0) r.fail(`${name} ${route}: signed in, but no visible account chip or menu control — no way to navigate`)
      if (status !== null && status >= 400) r.fail(`${name} ${route}: answered ${status}`)
      for (const e of consoleErrors.slice(0, 2)) r.fail(`${name} ${route}: console error: ${e.text}`)
      for (const f of failed.slice(0, 2)) r.fail(`${name} ${route}: request failed: ${f}`)
      if (name === 'mobile' && seen.smallButtonCount) r.info(`${name} ${route}: ${seen.smallButtonCount} button(s) under 24px (WCAG 2.5.8, check spacing): ${seen.smallButtons.join('; ')}`)
    } catch (err) {
      r.fail(`${name} ${route}: did not load: ${String(err).split('\n')[0]}`)
      row.failed = failed.length
    } finally {
      rows.push(row)
      await context.close().catch(() => {})
    }
  }
}
if (viewportNames.filter((n) => VIEWPORTS[n]).length > 1) {
  const overflowing = rows.filter((row) => row.sideScroll !== null && row.sideScroll > 1)
  if (overflowing.length === 0) r.pass(`no route can be dragged sideways in any measured viewport`)
}
await browser.close().catch(() => {})

console.log(`\nviewport  route                          client inner scroll side  chars  errs/failed`)
for (const row of rows) {
  console.log(
    `${row.viewport.padEnd(9)} ${row.route.padEnd(30)} ${String(row.clientWidth).padEnd(6)} ${String(row.innerWidth).padEnd(5)} ${String(row.scrollWidth).padEnd(6)} ${String(row.sideScroll).padEnd(5)} ${String(row.chars).padEnd(6)} ${row.errors}/${row.failed}`,
  )
}
r.info(`${rows.length} route x viewport combinations measured (${viewportNames.join(', ')})`)
r.done('check-responsive')
