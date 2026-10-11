// Shared helpers for the fe-4genthub-driven scripts. One place for: argument parsing, the e2e
// account (read in-process, never printed), the frontend checkout that hosts Playwright, the
// browser, the sign-in/sign-out flow, and the route table this skill checks.
import { createRequire } from 'node:module'
import fs from 'node:fs'
import path from 'node:path'

// The routes the deployed app ships, split by what they need. Kept in one place so every script
// checks the same set; the audit (ai_docs/frontend-ui-audit-2026-10-11.md) is where these were
// measured. Dynamic routes (a project, branch, task, subtask, seat, session) need an id; a script
// that wants them must discover one and pass --routes explicitly.
export const ROUTES = {
  anonymous: ['/', '/login', '/signup', '/register', '/registration-success', '/auth/verify'],
  signedIn: ['/dashboard', '/tokens', '/seats', '/seats/authoring', '/sessions', '/topology', '/feedback', '/profile', '/docs', '/help-setup'],
}

// The form factors the app must serve. Each entry is a ready-to-spread newContext option
// fragment — the viewport size lives under the `viewport` key Playwright expects, so
// `browser.newContext({ ...VIEWPORTS.desktop, storageState })` cannot silently drop it (spreading
// a bare {width, height} is ignored by Playwright and every number comes back at its 1280x720
// default: measured 2026-10-11, the first responsive run reported innerWidth 1280 for a 390 phone).
// A phone context carries mobile emulation flags, so `width=device-width` behaves as it does on a
// real handset (without isMobile, Chrome keeps the desktop layout viewport and a phone-only
// breakage stays invisible).
export const VIEWPORTS = {
  desktop: { viewport: { width: 1440, height: 900 } },
  laptop: { viewport: { width: 1280, height: 800 } },
  mobile: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 },
}

export function die(msg) {
  console.error(msg)
  process.exit(2)
}

// argv may start with a mode word (`capture`) or straight with a flag (`--base`); both parse.
// A flag followed by another flag (or by nothing) is a boolean: `--no-shots --fe x` must not eat
// `--fe`. Measured 2026-10-11: before this rule, `--no-shots` swallowed the next flag and the walk
// died with "no frontend checkout found" although --fe had been passed.
export function parseArgs(argv) {
  const isMode = argv.length > 0 && !argv[0].startsWith('--')
  const [first, ...rest0] = argv
  const mode = isMode ? first : null
  const rest = isMode ? rest0 : argv
  const flags = {}
  const positional = []
  for (let i = 0; i < rest.length; i++) {
    if (rest[i].startsWith('--')) {
      const next = rest[i + 1]
      flags[rest[i].slice(2)] = next === undefined || next.startsWith('--') ? true : rest[++i]
    } else positional.push(rest[i])
  }
  return { mode, flags, positional }
}

export function findUp(start, name) {
  let dir = path.resolve(start)
  for (;;) {
    const candidate = path.join(dir, name)
    if (fs.existsSync(candidate)) return candidate
    const parent = path.dirname(dir)
    if (parent === dir) return null
    dir = parent
  }
}

export function resolveFrontend(flags) {
  const candidates = [flags.fe, process.env.FE_REPO, process.cwd()].filter(Boolean)
  for (const c of candidates) {
    const pkg = findUp(c, 'package.json')
    if (pkg) {
      const dir = path.dirname(pkg)
      if (fs.existsSync(path.join(dir, 'node_modules', '@playwright', 'test'))) return dir
    }
  }
  die('no frontend checkout with @playwright/test found: pass --fe <path>, set FE_REPO, or run from inside agenthub-frontend')
}

export function readAccount(flags, feDir) {
  const file = flags.account ?? process.env.E2E_ACCOUNT_FILE ?? findUp(feDir, 'e2e-test-account')
  if (!file) die('no account file: pass --account <file>, set E2E_ACCOUNT_FILE, or create e2e-test-account with USERNAME= and PASSWORD= lines (no value is logged)')
  const values = {}
  for (const line of fs.readFileSync(file, 'utf8').split('\n')) {
    const m = /^\s*(USERNAME|PASSWORD)\s*=\s*(.*)$/.exec(line)
    if (m) values[m[1]] = m[2].trim()
  }
  if (!values.USERNAME || !values.PASSWORD) die(`${path.basename(file)} must carry USERNAME= and PASSWORD= lines; no value is logged`)
  return { username: values.USERNAME, password: values.PASSWORD }
}

export async function launchBrowser(flags) {
  const feDir = resolveFrontend(flags)
  const require = createRequire(path.join(feDir, 'package.json'))
  const { chromium } = require('@playwright/test')
  try {
    return { browser: await chromium.launch({ channel: 'chrome', headless: true }), feDir }
  } catch (err) {
    die(`could not launch Google Chrome (channel 'chrome'): ${String(err).split('\n')[0]}\ninstall Chrome, or run \`npx playwright install chromium\` and change the launch call to the bundled build`)
  }
}

export function baseURL(flags) {
  return (flags.base ?? 'https://www.4genthub.com').replace(/\/$/, '')
}

// The sign-in flow the deploy smoke (agenthub-frontend/e2e/smoke.spec.ts) uses. Roles, not labels:
// the MUI label carries a required asterisk, so an exact label match misses.
export async function signIn(page, account) {
  await page.getByRole('textbox', { name: 'Email Address' }).fill(account.username)
  await page.getByRole('textbox', { name: 'Password' }).fill(account.password)
  await page.getByRole('button', { name: 'Sign In' }).click()
  await page.waitForURL(/\/dashboard/, { timeout: 45_000 })
  // waitForURL resolves when the URL changes, BEFORE the view re-renders: wait for the shell itself.
  await page.waitForFunction(() => /Dashboard/.test(document.body.innerText), { timeout: 30_000 })
}

export async function signOut(page) {
  await page.getByRole('banner').getByRole('button', { name: /@/ }).click()
  await page.getByText('Sign Out').click()
  await page.waitForURL(/\/login/, { timeout: 30_000 })
  // Same trap as sign-in: the URL changes before the view does. Wait for the form itself.
  await page.getByRole('textbox', { name: 'Email Address' }).waitFor({ timeout: 30_000 })
}

// One collector shared by every check: the /api/ calls, the console errors, and the requests that
// never completed. ERR_ABORTED is dropped because a navigation cancels its own in-flight fetches.
// Console text is redacted before it is stored: the realtime socket carries a JWT in its query
// string, and a transcript or a log file must never hold one.
export function watchPage(page) {
  const api = []
  const consoleErrors = []
  const failed = []
  const state = { phase: 'default' }
  const redact = (text) =>
    text
      .replace(/token=[^&\s"']+/gi, 'token=<redacted>')
      .replace(/eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]+/g, '<jwt redacted>')
  page.on('console', (m) => {
    if (m.type() === 'error' && !/favicon|DevTools|source map/i.test(m.text())) consoleErrors.push({ phase: state.phase, text: redact(m.text()).slice(0, 200) })
  })
  page.on('pageerror', (e) => consoleErrors.push({ phase: state.phase, text: redact(String(e)).slice(0, 200) }))
  page.on('requestfailed', (r) => {
    const why = r.failure()?.errorText ?? ''
    if (!/ERR_ABORTED/.test(why)) failed.push(`${r.method()} ${redact(r.url()).slice(0, 120)} ${why}`)
  })
  page.on('response', (r) => {
    const t = r.request().resourceType()
    if (t !== 'xhr' && t !== 'fetch') return
    // Anything the app fetches, plus anything cross-origin: /health lives on the API host without
    // an /api/ segment, so a pathname filter alone would silently miss it.
    const u = new URL(r.url())
    if (!u.pathname.includes('/api/') && u.origin === new URL(page.url()).origin) return
    api.push({ method: r.request().method(), path: u.pathname, origin: u.origin, status: r.status() })
  })
  // phase() labels everything collected from here on, so a check can tell teardown noise from a
  // failure on the page it is checking.
  return { api, consoleErrors, failed, phase: (name) => { state.phase = name } }
}

// A check reports through this: every line is PASS/FAIL/INFO, and the exit code is 1 when any
// FAIL was recorded. Facts are printed as INFO so a passing run still shows what it measured.
export function reporter() {
  const failures = []
  const line = (verb, text) => console.log(`${verb.padEnd(4)} ${text}`)
  return {
    pass: (text) => line('PASS', text),
    info: (text) => line('INFO', text),
    fail: (text) => { failures.push(text); line('FAIL', text) },
    note: (text) => console.log(`     ${text}`),
    done(title) {
      if (failures.length) {
        console.log(`\n${title}: ${failures.length} failure(s)`)
        process.exit(1)
      }
      console.log(`\n${title}: all checks passed`)
    },
  }
}

// Color maths for the theme and contrast checks. Chrome hands computed styles back as
// `rgb(r, g, b)` or `rgba(r, g, b, a)`, so the parser only needs those two forms.
export function parseColor(value) {
  const m = /rgba?\(([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\)/.exec(value || '')
  return m ? { r: +m[1], g: +m[2], b: +m[3], a: m[4] === undefined ? 1 : +m[4] } : null
}

// WCAG relative luminance (sRGB coefficients, 0.03928 knee).
export function luminance({ r, g, b }) {
  const [rl, gl, bl] = [r, g, b].map((v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * rl + 0.7152 * gl + 0.0722 * bl
}

export function contrastRatio(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}
