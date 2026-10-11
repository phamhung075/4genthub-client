#!/usr/bin/env node
// production-walk.mjs — capture what a 4genthub frontend origin actually renders, and diff two captures.
//
//   node production-walk.mjs capture --base https://www.4genthub.com --out <dir> [--label production]
//   node production-walk.mjs capture --base http://localhost:3800   --out <dir> [--label local]
//   node production-walk.mjs capture --routes all --out <dir>          (every static signed-in route)
//   node production-walk.mjs capture --no-shots --out <dir>            (JSON only — use this for a
//                                                                       baseline that will be committed:
//                                                                       full-page PNGs of signed-in
//                                                                       screens never belong in a repo)
//   node production-walk.mjs compare <baselineDir> <captureDir>
//
// The walk is read-only: it signs in with the shared e2e account (read inside this process, never
// printed anywhere), visits the routes, and signs out. Nothing is created on the far side, no
// setting is changed. The /login page is always captured before sign-in; --routes names the
// signed-in routes to capture after it (default: the four the deploy smoke walks).
import fs from 'node:fs'
import path from 'node:path'
import { ROUTES, VIEWPORTS, baseURL, die, launchBrowser, parseArgs, readAccount, signIn, signOut, waitForReady, watchPage } from './lib.mjs'

const DEFAULT_ROUTES = ['/dashboard', '/topology', '/sessions']

// Keys that describe WHEN and WHERE a capture ran, not what it rendered.
const META_KEYS = new Set(['capturedAt', 'label', 'base'])

function wantedRoutes(flags) {
  if (!flags.routes) return DEFAULT_ROUTES
  if (flags.routes === 'all') return ROUTES.signedIn
  return flags.routes.split(',').map((r) => r.trim()).filter(Boolean)
}

async function capture(flags) {
  const { browser, feDir } = await launchBrowser(flags)
  const account = readAccount(flags, feDir)
  const base = baseURL(flags)
  const label = flags.label ?? new URL(base).hostname.replace(/\./g, '-')
  const outDir = flags.out ?? path.join('captures', label)
  fs.mkdirSync(outDir, { recursive: true })

  const context = await browser.newContext({ ...VIEWPORTS.desktop })
  const page = await context.newPage()
  const { api } = watchPage(page)
  const routes = {}

  const dumpRoute = async (route) => {
    await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
    // A capture is the record of what rendered; capturing a pre-data page would write the
    // placeholder into a baseline (or into a diff against one), so the walk stops instead.
    if (!(await waitForReady(page))) die(`${route}: the view never reached the 120-character readiness floor within 30s — not captured (a pre-data capture must not become a baseline)`)
    if (!('no-shots' in flags)) await page.screenshot({ path: path.join(outDir, `${label}-${route.replace(/\W+/g, '_') || 'root'}.png`), fullPage: true })
    return page.evaluate(() => {
      const described = (el) => {
        const r = el.getBoundingClientRect()
        return {
          tag: el.tagName.toLowerCase(),
          type: el.getAttribute('type'),
          id: el.id || null,
          name: el.getAttribute('name'),
          aria: el.getAttribute('aria-label'),
          text: (el.innerText || '').trim().split('\n')[0].slice(0, 60),
          href: el.getAttribute('href'),
          visible: r.width > 0,
        }
      }
      const styleOf = (el) => {
        const s = getComputedStyle(el)
        return { font: `${s.fontSize}/${s.fontWeight} ${s.fontFamily.split(',')[0]}`, color: s.color, background: s.backgroundColor, radius: s.borderRadius, padding: s.padding }
      }
      const root = getComputedStyle(document.documentElement)
      const vars = {}
      const semantic = /^--(color|sidebar|chart|radius|primary|secondary|muted|accent|background|foreground|card|popover|border|input|ring|destructive|ok|warn|danger|info|success|error|warning)/
      for (const prop of Array.from(root)) {
        if (semantic.test(prop) && !prop.startsWith('--tw-')) vars[prop] = root.getPropertyValue(prop).trim()
      }
      const samples = ['body', 'h1', 'header', 'main', '.MuiPaper-root', '.MuiButton-root', '.MuiTextField-root']
        .map((sel) => {
          const el = document.querySelector(sel)
          return el ? { sel, ...styleOf(el) } : null
        })
        .filter(Boolean)
      const visible = (sel) => Array.from(document.querySelectorAll(sel)).filter((e) => e.getBoundingClientRect().width > 0)
      return {
        title: document.title,
        headings: visible('h1,h2,h3').map((h) => h.innerText.trim()),
        fields: visible('input,textarea,select').map(described),
        buttons: visible('button,[role=button]').map(described),
        links: visible('a[href]').map(described),
        // The account's identity is not recorded: a capture is committed, and a name is not evidence.
        userChip: { present: !!Array.from(document.querySelectorAll('button')).find((b) => /@/.test(b.innerText)) },
        tokens: { vars, samples },
      }
    })
  }

  try {
    await page.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
    routes['/login'] = await dumpRoute('/login')

    await signIn(page, account)

    for (const route of wantedRoutes(flags)) {
      await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
      routes[route] = await dumpRoute(route)
    }

    try {
      // Sign out from /dashboard, not wherever the last route left off: /docs and /help-setup
      // render without the shell, so they have no account chip to open (measured 2026-10-11).
      await page.goto(`${base}/dashboard`, { waitUntil: 'load', timeout: 60_000 })
      await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
      await signOut(page)
    } catch {
      console.error('note: sign-out control not found; the session was left signed in on this browser profile only')
    }
  } finally {
    fs.writeFileSync(path.join(outDir, `${label}.json`), JSON.stringify({ label, base, capturedAt: new Date().toISOString(), api, routes }, null, 1))
    await context.close().catch(() => {})
    await browser.close().catch(() => {})
  }

  console.log(`captured ${Object.keys(routes).length} routes from ${base} -> ${outDir}/${label}.json`)
  for (const [route, doc] of Object.entries(routes)) {
    console.log(`  ${route}: "${doc.headings[0] ?? '(no heading)'}" — ${doc.fields.length} fields, ${doc.buttons.length} buttons, ${doc.links.length} links`)
  }
}

function flatten(node, prefix = '', out = {}) {
  if (node !== null && typeof node === 'object') {
    for (const [k, v] of Object.entries(node)) {
      if (META_KEYS.has(k) && prefix === '') continue
      flatten(v, prefix ? `${prefix}.${k}` : k, out)
    }
  } else {
    out[prefix] = node
  }
  return out
}

function compare(baselineDir, captureDir) {
  const single = (dir) => {
    const files = fs.readdirSync(dir).filter((f) => f.endsWith('.json'))
    if (files.length !== 1) die(`${dir} must hold exactly one capture .json (found ${files.length}); run \`capture\` first`)
    return JSON.parse(fs.readFileSync(path.join(dir, files[0]), 'utf8'))
  }
  const baseline = flatten(single(baselineDir))
  const capture = flatten(single(captureDir))

  const structural = []
  const tokens = []
  for (const key of new Set([...Object.keys(baseline), ...Object.keys(capture)])) {
    const a = baseline[key]
    const b = capture[key]
    if (JSON.stringify(a) === JSON.stringify(b)) continue
    const line = `${key}: ${JSON.stringify(a)} -> ${JSON.stringify(b)}`
    // Token values and HTTP statuses are expected to move; structure and copy are not.
    if (/(^|\.)tokens\.|\.status$/.test(key)) tokens.push(line)
    else structural.push(line)
  }

  const show = (title, lines) => {
    if (!lines.length) return
    console.log(`\n${title} (${lines.length})`)
    for (const l of lines.slice(0, 200)) console.log(`  ${l}`)
    if (lines.length > 200) console.log(`  … ${lines.length - 200} more`)
  }
  show('STRUCTURAL differences — fix them or record why they are deliberate', structural)
  show('Token/status differences — expected while the token layer moves; list them', tokens)
  if (!structural.length) console.log('\nno structural difference — the capture matches the baseline')
  process.exit(structural.length ? 1 : 0)
}

const { mode, flags, positional } = parseArgs(process.argv.slice(2))
if (mode === 'capture') await capture(flags)
else if (mode === 'compare') {
  if (positional.length !== 2) die('usage: node production-walk.mjs compare <baselineDir> <captureDir>')
  compare(positional[0], positional[1])
} else {
  die('usage: node production-walk.mjs capture --base URL --out DIR [--label NAME] [--routes all|/a,/b] [--no-shots] [--fe DIR] [--account FILE]\n       node production-walk.mjs compare <baselineDir> <captureDir>')
}
