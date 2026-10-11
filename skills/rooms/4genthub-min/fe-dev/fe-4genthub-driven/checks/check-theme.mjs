#!/usr/bin/env node
// check-theme.mjs — the color layer in both modes, measured through the app's own mechanism.
//
// How this app decides its theme (ThemeContext.tsx: saved localStorage 'theme' wins, else
// prefers-color-scheme, else light; the resolved value is then pinned into localStorage on the
// first render). That pinning is why every state this check replays has the 'theme' key stripped:
// replaying a session's storage would otherwise decide the theme and the measurement would prove
// nothing. The toggle writes localStorage only — no API call — so clicking it is safe, and this
// check proves that claim rather than trusting it: any non-GET request around the click fails.
//
// Contract:
//   - first visit (no saved theme), system dark -> dark surface; system light -> light surface
//   - a saved preference outranks the system setting (the user's choice is not overridden)
//   - the shell's toggle flips the surface and records the choice
//   - the two produced states differ across the semantic token watchlist
import { ROUTES, VIEWPORTS, baseURL, launchBrowser, parseArgs, parseColor, luminance, readAccount, reporter, signIn, waitForReady, watchPage } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const WATCH = ['--background', '--foreground', '--color-background', '--color-text', '--color-surface', '--color-primary', '--primary', '--sidebar', '--card', '--border']
const pages = (flags.routes ?? '/login,/dashboard').split(',').map((s) => s.trim())

const probe = (watch) => {
  const root = getComputedStyle(document.documentElement)
  const vars = {}
  for (const name of watch) vars[name] = root.getPropertyValue(name).trim()
  const control = Array.from(document.querySelectorAll('button')).find((b) => /dark|light|theme/i.test(b.innerText || b.getAttribute('aria-label') || ''))
  return {
    bodyBg: getComputedStyle(document.body).backgroundColor,
    htmlBg: getComputedStyle(document.documentElement).backgroundColor,
    rootClass: document.documentElement.className,
    saved: localStorage.getItem('theme'),
    colorSchemeMatches: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
    vars,
    control: control ? (control.getAttribute('aria-label') || control.innerText).trim().split('\n')[0].slice(0, 30) : null,
  }
}

const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)

// A session to replay into the signed-in measurements, with the pinned theme stripped.
let sessionOk = true
const warm = await browser.newContext({ ...VIEWPORTS.desktop })
const warmPage = await warm.newPage()
try {
  await warmPage.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  await signIn(warmPage, account)
} catch (err) {
  sessionOk = false
  r.fail(`could not establish a session for the signed-in pages: ${String(err).split('\n')[0]}`)
}
const session = await warm.storageState()
await warm.close().catch(() => {})
const withoutTheme = {
  ...session,
  origins: session.origins.map((o) => ({ ...o, localStorage: o.localStorage.filter((e) => e.name !== 'theme') })),
}

async function measure(route, { colorScheme, savedTheme, clickToggle }) {
  const context = await browser.newContext({ ...VIEWPORTS.desktop, colorScheme, storageState: ROUTES.anonymous.includes(route) ? undefined : withoutTheme })
  const page = await context.newPage()
  const watch = watchPage(page)
  try {
    if (savedTheme) await page.addInitScript((value) => localStorage.setItem('theme', value), savedTheme)
    await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
    await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
    // `networkidle` is not readiness (see lib.mjs waitForReady): measuring the painted surface of
    // a view that has not rendered measures the placeholder's surface, not the route's. `null`
    // tells the caller this route/arm was not measured; the callers skip it.
    if (!(await waitForReady(page))) {
      r.fail(`${route}: the view never reached the 120-character readiness floor within 30s — not measured`)
      return null
    }
    const before = await page.evaluate(probe, WATCH)
    if (!clickToggle) return { before, after: null, control: before.control, writes: [] }
    // The toggle test: click only if a control exists, then read the surface again.
    if (!before.control) return { before, after: null, control: null, writes: [] }
    const beforeCalls = watch.api.length
    const target = /switch|toggle/i.test(before.control) ? before.control : null
    await page.getByRole('button', { name: target ?? /dark|light|theme/i }).first().click()
    await page.waitForTimeout(400)
    const after = await page.evaluate(probe, WATCH)
    const writes = watch.api.slice(beforeCalls).filter((c) => c.method !== 'GET')
    return { before, after, control: before.control, writes }
  } finally {
    await context.close().catch(() => {})
  }
}

// The painted surface, per the CSS canvas rule: if the body background is transparent the browser
// paints the page from the html element instead. Measuring a transparent body would read
// luminance 0 and let a white page pass the dark assertion, so the painted layer decides here.
const surface = (state) => {
  const body = parseColor(state.bodyBg)
  const html = parseColor(state.htmlBg)
  const painted = body && body.a > 0 ? body : html
  if (!painted || painted.a === 0) return { lum: null, bg: `body ${state.bodyBg} over html ${state.htmlBg} (both transparent)` }
  return { lum: luminance(painted), bg: painted === body ? state.bodyBg : `${state.htmlBg} (via html)` }
}

for (const route of pages) {
  const anon = ROUTES.anonymous.includes(route)
  const label = `${route}${anon ? ' (anonymous)' : ''}`
  // Without a session a "signed-in" route would silently measure the login page it redirects to;
  // skip it rather than mislabel the numbers.
  if (!anon && !sessionOk) { r.info(`${label}: skipped, no session`); continue }

  // First visit, no saved theme: the system preference decides.
  const dark = await measure(route, { colorScheme: 'dark' })
  if (!dark) continue
  const light = await measure(route, { colorScheme: 'light' })
  if (!light) continue
  r.info(`${label}: emulation read back as system=${dark.before.colorSchemeMatches}/${light.before.colorSchemeMatches}, root class in dark = "${dark.before.rootClass}"`)
  const darkSurface = surface(dark.before)
  const lightSurface = surface(light.before)
  if (darkSurface.lum !== null && darkSurface.lum < 0.2) r.pass(`${label} first visit with system dark: ${darkSurface.bg} (luminance ${darkSurface.lum.toFixed(3)})`)
  else r.fail(`${label} first visit with system dark: body is ${darkSurface.bg} — the system preference was not honored`)
  if (lightSurface.lum !== null && lightSurface.lum > 0.5) r.pass(`${label} first visit with system light: ${lightSurface.bg} (luminance ${lightSurface.lum.toFixed(3)})`)
  else r.fail(`${label} first visit with system light: body is ${lightSurface.bg}, expected a light surface`)

  // A saved preference outranks the system setting.
  const pinned = await measure(route, { colorScheme: 'dark', savedTheme: 'light' })
  if (!pinned) continue
  const pinnedSurface = surface(pinned.before)
  if (pinnedSurface.lum !== null && pinnedSurface.lum > 0.5) r.pass(`${label} saved "light" under system dark: stays light (the user's choice wins)`)
  else r.fail(`${label} saved "light" under system dark: body is ${pinnedSurface.bg}, the saved preference was overridden`)

  // The two produced states must differ on the token watchlist.
  if (darkSurface.lum !== null && darkSurface.lum < 0.2) {
    for (const name of WATCH) {
      const a = dark.before.vars[name]
      const b = light.before.vars[name]
      if (!a && !b) continue
      if (a === b) r.fail(`${label}: ${name} is ${a || 'unset'} in both modes — the token layer does not follow the theme`)
    }
  }

  // Toggle (anonymous /login only: a signed-in toggle test would flip the same localStorage, but
  // one control proof is enough and the anonymous one cannot carry session state into the result).
  if (anon) {
    const toggled = await measure(route, { colorScheme: 'light', clickToggle: true })
    if (!toggled) continue
    if (!toggled.control) r.info(`${label}: no theme control found on this page`)
    else {
      r.info(`${label}: theme control "${toggled.control}"`)
      const before = surface(toggled.before)
      const after = toggled.after ? surface(toggled.after) : null
      const flipped = after && before.bg !== after.bg
      if (flipped) r.pass(`${label} toggle changed the painted surface ${before.bg} -> ${after.bg} and saved theme="${toggled.after.saved}"`)
      else r.fail(`${label} toggle did not change the painted surface (still ${before.bg})`)
      if (toggled.writes.length === 0) r.pass(`${label} toggle made no non-GET API call (localStorage only, as the source says)`)
      else r.fail(`${label} toggle made a write to the backend: ${toggled.writes.map((w) => `${w.method} ${w.path}`).join(', ')}`)
    }
  }
}
await browser.close().catch(() => {})
r.done('check-theme')
