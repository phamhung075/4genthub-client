#!/usr/bin/env node
// check-login.mjs — the login page and the sign-in / sign-out cycle, asserted.
//
// Contract (agenthub-frontend/e2e/smoke.spec.ts walks the same ground on every deploy):
//   - /login renders a textbox named "Email Address", a textbox named "Password" whose input
//     type is password, and exactly one button "Sign In"
//   - signing in with the shared e2e account lands on /dashboard AND the shell renders (the
//     banner's account chip), not merely the URL change
//   - the account chip opens a menu whose "Sign Out" returns to /login with the form back
//   - no console error and no failed request while a page is live; errors logged while the
//     session tears down at sign-out are reported as INFO, not failed (the realtime socket
//     logs an error as it closes)
// Reported but not asserted (login-page features a redesign may move): the password visibility
// toggle, the forgot-password and signup links, the login card's measured box.
//
// Read-only: signs in and out against --base and changes nothing. Credentials stay in-process.
import { VIEWPORTS, baseURL, launchBrowser, parseArgs, readAccount, reporter, signIn, signOut, waitForReady, watchPage } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)
const context = await browser.newContext({ ...VIEWPORTS.desktop })
const page = await context.newPage()
const watch = watchPage(page)

try {
  const response = await page.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  r.info(`GET /login from ${base} answered ${response?.status()}`)
  // `networkidle` is not readiness for a client-rendered view (see lib.mjs waitForReady): every read
  // below is instantaneous (locator.count(), boundingBox(), evaluate()), so a cold load reads a page
  // whose form has not mounted. Measured 2026-10-11: those three locators were found 0 times in an
  // eight-check run and present exactly once in 2 of 2 solo runs on the same build - a race, not a
  // page defect. A login page that never renders ends the check instead of reporting a false red.
  if (!(await waitForReady(page))) {
    r.fail(`${base}/login: the view never reached the 120-character readiness floor within 30s — the login form was not read (the reads below are instantaneous)`)
    await browser.close().catch(() => {})
    r.done('check-login')
  }

  const seats = {
    'textbox "Email Address"': page.getByRole('textbox', { name: 'Email Address' }),
    'textbox "Password"': page.getByRole('textbox', { name: 'Password' }),
    'button "Sign In"': page.getByRole('button', { name: 'Sign In' }),
  }
  for (const [what, locator] of Object.entries(seats)) {
    const count = await locator.count()
    if (count === 1) r.pass(`${what} present exactly once`)
    else r.fail(`${what} found ${count} time(s), expected exactly 1 — the deploy smoke locates it this way`)
  }

  const passwordType = await seats['textbox "Password"'].evaluate((el) => el.type).catch(() => null)
  if (passwordType === 'password') r.pass('Password input has type="password"')
  else r.fail(`Password input has type="${passwordType}", expected "password"`)

  const card = await page.locator('.MuiPaper-root').first().boundingBox().catch(() => null)
  if (card) r.info(`login card measured ${Math.round(card.width)}x${Math.round(card.height)} at the card's x=${Math.round(card.x)} y=${Math.round(card.y)} (1440x900 viewport)`)

  const toggle = page.locator('button[aria-label*="password" i]')
  if (await toggle.count()) {
    await toggle.first().click()
    const revealed = await seats['textbox "Password"'].evaluate((el) => el.type)
    await toggle.first().click()
    const hidden = await seats['textbox "Password"'].evaluate((el) => el.type)
    r.info(`password visibility toggle: ${revealed}/${hidden} (type after first/second click)`)
  } else {
    r.info('no password visibility toggle (button[aria-label*=password]) found')
  }
  for (const link of ['/forgot-password', '/signup']) {
    const n = await page.locator(`a[href="${link}"]`).count()
    r.info(`${link} link present: ${n > 0}`)
  }

  try {
    watch.phase('signin')
    await signIn(page, account)
    r.pass('sign-in reached /dashboard with the shell rendered')
  } catch (err) {
    r.fail(`sign-in did not reach a rendered /dashboard: ${String(err).split('\n')[0]}`)
  }

  const chip = page.getByRole('banner').getByRole('button', { name: /@/ })
  if (await chip.count()) r.pass('banner account chip present after sign-in')
  else r.fail('no banner account chip (button whose name contains @) after sign-in')

  try {
    watch.phase('signout')
    await signOut(page)
    const back = await page.getByRole('textbox', { name: 'Email Address' }).count()
    if (back === 1) r.pass('sign-out returned to /login with the form rendered')
    else r.fail('sign-out returned to /login but the form is not rendered')
  } catch (err) {
    r.fail(`sign-out failed: ${String(err).split('\n')[0]}`)
  }
} catch (err) {
  r.fail(`login page load failed: ${String(err).split('\n')[0]}`)
} finally {
  // Errors while the session is being torn down are reported, not failed: closing the realtime
  // socket logs an error on the way out, and that is not the login page's contract.
  const pageErrors = watch.consoleErrors.filter((e) => e.phase !== 'signout')
  const teardown = watch.consoleErrors.filter((e) => e.phase === 'signout')
  for (const e of pageErrors.slice(0, 5)) r.fail(`console error during ${e.phase}: ${e.text}`)
  if (pageErrors.length > 5) r.note(`… and ${pageErrors.length - 5} more console errors`)
  for (const e of teardown.slice(0, 3)) r.info(`console error at sign-out (teardown, reported not failed): ${e.text}`)
  for (const f of watch.failed.slice(0, 5)) r.fail(`request failed: ${f}`)
  if (watch.failed.length > 5) r.note(`… and ${watch.failed.length - 5} more failed requests`)
  await context.close().catch(() => {})
  await browser.close().catch(() => {})
  r.done('check-login')
}
