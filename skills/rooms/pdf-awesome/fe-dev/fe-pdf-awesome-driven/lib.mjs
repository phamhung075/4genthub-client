// Shared helpers for the fe-pdf-awesome-driven scripts. One place for: argument parsing, a static
// file server for the repo (pdf-awesome is static files: no bundler, no backend, no login), the
// Playwright resolution, the browser, the viewport table, the page watcher and the color maths.
import { createRequire } from 'node:module'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'

// Routes of the app. It is one page whose views (import / edit / export) are switched in the DOM,
// so the static walk is the front door. Add a route with --routes /about.html,... when one exists.
export const ROUTES = ['/']

// Form factors the app must serve. Each entry is a ready-to-spread newContext fragment: the size
// lives under `viewport`, because Playwright SILENTLY ignores a bare { width, height } (every number
// comes back at the 1280x720 default). A phone carries isMobile/hasTouch so `width=device-width`
// behaves as on a handset; without isMobile a "phone" keeps the desktop layout viewport.
export const VIEWPORTS = {
  desktop: { viewport: { width: 1440, height: 900 } },
  laptop: { viewport: { width: 1280, height: 800 } },
  mobile: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 },
}

export function die(msg) {
  console.error(msg)
  process.exit(2)
}

// A flag followed by another flag (or by nothing) is a boolean: `--serve --pw x` must not eat `--pw`.
export function parseArgs(argv) {
  const flags = {}
  const positional = []
  for (let i = 0; i < argv.length; i++) {
    if (argv[i].startsWith('--')) {
      const next = argv[i + 1]
      flags[argv[i].slice(2)] = next === undefined || next.startsWith('--') ? true : argv[++i]
    } else positional.push(argv[i])
  }
  return { flags, positional }
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

// The project root is the nearest ancestor holding index.html + sw.js + package.json named pdf-awesome.
export function resolveProject(flags) {
  const start = flags.root ?? process.env.PDF_AWESOME_ROOT ?? process.cwd()
  const pkg = findUp(start, 'package.json')
  if (pkg && fs.existsSync(path.join(path.dirname(pkg), 'index.html'))) return path.dirname(pkg)
  die('no pdf-awesome checkout found: run from inside it, pass --root <path>, or set PDF_AWESOME_ROOT')
}

// The project does not depend on Playwright (it is test tooling, not an app dependency, and no new
// dependency enters the repo). Find one that exists: --pw <dir holding node_modules/playwright-core or
// @playwright/test>, PLAYWRIGHT_DIR, the project's own node_modules, or `npx playwright` in the cwd.
function loadChromium(root, flags) {
  const dirs = [flags.pw, process.env.PLAYWRIGHT_DIR, root, process.cwd()].filter(Boolean)
  for (const dir of dirs) {
    for (const name of ['@playwright/test', 'playwright', 'playwright-core']) {
      try {
        return createRequire(path.join(path.resolve(dir), 'package.json'))(name).chromium
      } catch { /* try the next place */ }
    }
  }
  die('Playwright not found: pass --pw <dir containing node_modules/@playwright/test>, or set PLAYWRIGHT_DIR (a browser is already cached under ~/.cache/ms-playwright)')
}

const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.webmanifest': 'application/manifest+json',
  '.png': 'image/png', '.jpg': 'image/jpeg', '.svg': 'image/svg+xml', '.ico': 'image/x-icon', '.txt': 'text/plain',
}

// Serve the repo root on a free local port, read-only. Never serves node_modules, .git or agenthub_client.
export async function serveProject(root) {
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://x')
    let rel = decodeURIComponent(url.pathname)
    if (rel.endsWith('/')) rel += 'index.html'
    const file = path.join(root, rel)
    const blocked = /(^|\/)(node_modules|\.git|agenthub_client|\.claude)(\/|$)/.test(rel)
    if (blocked || !file.startsWith(root) || !fs.existsSync(file) || !fs.statSync(file).isFile()) {
      res.writeHead(404).end('not found')
      return
    }
    res.writeHead(200, { 'content-type': MIME[path.extname(file)] ?? 'application/octet-stream' })
    fs.createReadStream(file).pipe(res)
  })
  await new Promise((ok) => server.listen(0, '127.0.0.1', ok))
  return { base: `http://127.0.0.1:${server.address().port}`, close: () => new Promise((ok) => server.close(ok)) }
}

// `--base https://...` checks a deployed origin; otherwise the project is served locally. A static
// server is NOT a build: nothing is compiled, bundled or installed.
export async function target(flags) {
  if (typeof flags.base === 'string') return { base: flags.base.replace(/\/$/, ''), close: async () => {} }
  return serveProject(resolveProject(flags))
}

export async function launchBrowser(flags) {
  const root = typeof flags.base === 'string' ? process.cwd() : resolveProject(flags)
  const chromium = loadChromium(root, flags)
  // Bundled Chromium by default. Headless Google Chrome 153 wedges its renderer on the first canvas 2D
  // draw (measured 2026-10-17, no app code in the path); this app draws every page on a canvas, so the
  // installed Chrome would report the browser, not the page. PDF_BROWSER=chrome opts in when that IS the question.
  const useChrome = process.env.PDF_BROWSER === 'chrome'
  try {
    return await chromium.launch(useChrome ? { channel: 'chrome', headless: true } : { headless: true })
  } catch (err) {
    die(`could not launch the browser: ${String(err).split('\n')[0]}\nthe browser is already cached under ~/.cache/ms-playwright when a Playwright was installed once; else run: npx playwright install chromium`)
  }
}

// The app is client-rendered, so neither `load` nor `networkidle` is readiness: the document is up while
// the view still holds a placeholder, and a check that reads then reports a false green. Wait for the
// thing the checks read - visible text - and leave the caller's floor as the verdict.
export async function waitForReady(page, { floor = 120, timeout = 30_000 } = {}) {
  try {
    await page.waitForFunction((n) => document.body.innerText.trim().length >= n, floor, { timeout })
    return true
  } catch {
    return false
  }
}

// One collector for every check: console errors, page errors and requests that never completed.
// ERR_ABORTED is dropped (a navigation cancels its own in-flight fetches).
export function watchPage(page) {
  const consoleErrors = []
  const failed = []
  const bad = []
  page.on('console', (m) => {
    if (m.type() === 'error' && !/favicon|DevTools|source map/i.test(m.text())) consoleErrors.push(m.text().slice(0, 200))
  })
  page.on('pageerror', (e) => consoleErrors.push(String(e).slice(0, 200)))
  page.on('requestfailed', (r) => {
    const why = r.failure()?.errorText ?? ''
    if (!/ERR_ABORTED/.test(why)) failed.push(`${r.method()} ${r.url().slice(0, 120)} ${why}`)
  })
  page.on('response', (r) => {
    if (r.status() >= 400 && !/favicon/.test(r.url())) bad.push(`${r.status()} ${r.request().method()} ${r.url().slice(0, 120)}`)
  })
  return { consoleErrors, failed, bad }
}

// A check reports through this: PASS/FAIL/INFO lines, and exit code 1 when any FAIL was recorded.
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

// A real, tiny multi-page PDF built in memory (no file, no dependency): used to drive the import path.
// `pages` blank A4 pages, each carrying its number as text so a page can be told apart on screen.
export function makePdf(pages) {
  const objs = []
  const add = (body) => { objs.push(body); return objs.length }
  const catalog = add('<< /Type /Catalog /Pages 2 0 R >>')
  const kids = Array.from({ length: pages }, (_, i) => 3 + i * 2)
  add(`<< /Type /Pages /Kids [${kids.map((k) => `${k} 0 R`).join(' ')}] /Count ${pages} >>`)
  const font = 3 + pages * 2
  for (let i = 0; i < pages; i++) {
    add(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents ${4 + i * 2} 0 R /Resources << /Font << /F1 ${font} 0 R >> >> >>`)
    const text = `BT /F1 48 Tf 200 400 Td (Page ${i + 1}) Tj ET`
    add(`<< /Length ${text.length} >>\nstream\n${text}\nendstream`)
  }
  add('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>')
  let out = '%PDF-1.4\n'
  const offsets = []
  objs.forEach((body, i) => { offsets.push(out.length); out += `${i + 1} 0 obj\n${body}\nendobj\n` })
  const xref = out.length
  out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n${offsets.map((o) => `${String(o).padStart(10, '0')} 00000 n \n`).join('')}`
  out += `trailer\n<< /Size ${objs.length + 1} /Root ${catalog} 0 R >>\nstartxref\n${xref}\n%%EOF`
  const file = path.join(os.tmpdir(), `pdf-awesome-check-${pages}p-${process.pid}.pdf`)
  fs.writeFileSync(file, out, 'latin1')
  return file
}

// Color maths for the contrast check. Chrome hands computed styles back as rgb() or rgba().
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
