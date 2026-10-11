#!/usr/bin/env node
// check-backend.mjs — the frontend/backend seam, observed from the browser.
//
// Contract:
//   - the frontend talks to one API origin, and this check prints which one it is (a build that
//     points at the wrong backend shows up in the first line, not in a broken page)
//   - GET /health answers 200 (VersionDisplay fetches it on the login page)
//   - POST /api/auth/login answers 200 with the shared e2e account
//   - on every route it visits, no call the page makes to /api/ answers 400 or above.
//     One documented exception: /api/auth/refresh, where 401 means "no session to refresh" and
//     is the normal answer for an anonymous visitor.
// The run ends with the endpoint inventory: every method + path seen, with its statuses, which
// is the list fe-dev compares a local backend against.
import { ROUTES, VIEWPORTS, baseURL, launchBrowser, parseArgs, readAccount, reporter, signIn, signOut, waitForReady, watchPage } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const routes = flags.routes && flags.routes !== 'all' ? flags.routes.split(',').map((s) => s.trim()) : ROUTES.signedIn
const NORMAL_401 = /^\/api\/auth\/refresh/

const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)
const context = await browser.newContext({ ...VIEWPORTS.desktop })
const page = await context.newPage()
const { api } = watchPage(page)
const calls = []

try {
  await page.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  await signIn(page, account)
  for (const route of routes) {
    await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
    await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
    // The inventory is only meaningful for a route that rendered: a view still pre-data has not
    // made the calls it would make, so a 4xx/5xx behind them would be missed (a false green).
    if (!(await waitForReady(page))) {
      r.fail(`${route}: the view never reached the 120-character readiness floor within 30s — its calls were not observed, so this route is missing from the inventory`)
      continue
    }
  }
  // Sign out from /dashboard, not wherever the last route left off: /docs and /help-setup render
  // without the shell, so they have no account chip to open.
  await page.goto(`${base}/dashboard`, { waitUntil: 'load', timeout: 60_000 })
  await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
  await signOut(page)
} catch (err) {
  r.fail(`walk failed part-way: ${String(err).split('\n')[0]}`)
} finally {
  calls.push(...api)
  await context.close().catch(() => {})
  await browser.close().catch(() => {})
}

const origins = [...new Set(calls.map((c) => c.origin))]
if (origins.length === 1) r.pass(`one API origin: ${origins[0]}`)
else if (origins.length === 0) r.fail('no /api/ call was observed at all — the frontend may not be reaching a backend')
else r.fail(`more than one API origin in one session: ${origins.join(', ')}`)

const login = calls.find((c) => c.method === 'POST' && c.path === '/api/auth/login')
if (!login) r.fail('POST /api/auth/login was never observed')
else if (login.status === 200) r.pass('POST /api/auth/login answered 200')
else r.fail(`POST /api/auth/login answered ${login.status}`)

const health = calls.find((c) => c.path.endsWith('/health'))
if (!health) r.info('GET /health not observed (the login page may not have finished its version fetch)')
else if (health.status === 200) r.pass(`GET ${health.path} answered 200`)
else r.fail(`GET ${health.path} answered ${health.status}`)

const inventory = new Map()
for (const call of calls) {
  const key = `${call.method} ${call.path}`
  if (!inventory.has(key)) inventory.set(key, new Map())
  const statuses = inventory.get(key)
  statuses.set(call.status, (statuses.get(call.status) ?? 0) + 1)
}

for (const [key, statuses] of inventory) {
  for (const [status, count] of statuses) {
    if (status >= 400 && !(status === 401 && NORMAL_401.test(key.split(' ')[1]))) {
      r.fail(`${key} answered ${status}${count > 1 ? ` (${count}x)` : ''}`)
    }
  }
}

console.log('\nendpoint inventory (method path — status x count)')
for (const [key, statuses] of [...inventory].sort()) {
  const shown = [...statuses].map(([s, n]) => `${s}${n > 1 ? ` x${n}` : ''}`).join(', ')
  console.log(`  ${key.padEnd(46)} ${shown}`)
}
r.info(`${inventory.size} distinct endpoints observed across ${routes.length + 1} routes`)
r.done('check-backend')
