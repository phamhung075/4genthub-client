#!/usr/bin/env node
// check-nav.mjs — every static route in the app loads and renders something real.
//
// Contract per route:
//   - the document answers with a status below 400 (a redirect is reported, not failed)
//   - at least 120 characters of visible text after load, so a blank shell or an error boundary
//     cannot pass
//   - no console error and no failed request while the route loads
// Anonymous routes are visited in their own context; signed-in routes after one sign-in, each on
// a fresh page so one route's console noise cannot fail the next. Dynamic routes (a project,
// branch, task, subtask, seat, or session id in the path) need data this check will not create:
// pass them with --routes and they are checked as-is, otherwise they are reported as skipped.
import { ROUTES, VIEWPORTS, baseURL, launchBrowser, parseArgs, readAccount, reporter, signIn, watchPage } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const anonymous = ROUTES.anonymous
const signedIn = flags.routes && flags.routes !== 'all' ? flags.routes.split(',').map((s) => s.trim()) : ROUTES.signedIn
const dynamic = signedIn.filter((route) => /:(?![a-z]+:\/\/)/.test(route) || /\/[0-9a-f-]{8,}\//.test(route))

const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)
// Two routes render outside the app shell on purpose (measured 2026-10-11): /docs is a standalone
// generated API reference, /help-setup a standalone guide with its own Expand All controls. They
// are checked for content and errors like the rest, only the account-chip assertion is skipped.
const SHELL_EXEMPT = new Set(['/docs', '/help-setup'])
const context = await browser.newContext({ ...VIEWPORTS.desktop })
const rows = []

async function visit(route, { shell }) {
  const page = await context.newPage()
  const { consoleErrors, failed } = watchPage(page)
  const row = { route, status: null, landed: null, heading: '(none)', chars: 0, errors: 0, failed: 0 }
  try {
    const response = await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
    row.status = response?.status() ?? null
    await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
    const seen = await page.evaluate(() => ({
      path: location.pathname,
      chars: document.body.innerText.trim().length,
      heading: (Array.from(document.querySelectorAll('h1,h2,h3')).find((h) => h.getBoundingClientRect().width > 0)?.innerText ?? '').trim().split('\n')[0].slice(0, 48),
      shell: !!Array.from(document.querySelectorAll('button')).find((b) => /@/.test(b.innerText)),
    }))
    row.landed = seen.path
    row.chars = seen.chars
    row.heading = seen.heading || '(none)'

    if (row.status !== null && row.status >= 400) r.fail(`${route} answered ${row.status}`)
    if (row.chars < 120) r.fail(`${route} rendered only ${row.chars} characters of text (an empty shell or an error boundary)`)
    if (SHELL_EXEMPT.has(route)) r.info(`${route} renders outside the app shell (documented exception)`)
    else if (shell && !seen.shell) r.fail(`${route} is a signed-in route but the shell did not render (no account chip)`)
    for (const e of consoleErrors.slice(0, 3)) r.fail(`${route} console error: ${e.text}`)
    for (const f of failed.slice(0, 3)) r.fail(`${route} request failed: ${f}`)
  } catch (err) {
    r.fail(`${route} did not load: ${String(err).split('\n')[0]}`)
  } finally {
    row.errors = consoleErrors.length
    row.failed = failed.length
    rows.push(row)
    await page.close().catch(() => {})
  }
}

for (const route of anonymous) await visit(route, { shell: false })

try {
  const page = await context.newPage()
  await page.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  await signIn(page, account)
  await page.close()
} catch (err) {
  r.fail(`sign-in failed, cannot check the signed-in routes: ${String(err).split('\n')[0]}`)
}
for (const route of signedIn) await visit(route, { shell: true })

await context.close().catch(() => {})
await browser.close().catch(() => {})

console.log('\nroute                          status  lands on                       heading                          chars  errs')
for (const row of rows) {
  console.log(
    `${row.route.padEnd(30)} ${String(row.status).padEnd(7)} ${String(row.landed).padEnd(29)} ${row.heading.padEnd(32)} ${String(row.chars).padEnd(6)} ${row.errors}/${row.failed}`,
  )
}
if (dynamic.length) console.log(`\nneeds an id, not visited: ${dynamic.join(', ')} — pass explicit --routes once the data exists`)
r.info(`${rows.length} routes visited (${anonymous.length} anonymous, ${signedIn.length} signed-in)`)
r.done('check-nav')
