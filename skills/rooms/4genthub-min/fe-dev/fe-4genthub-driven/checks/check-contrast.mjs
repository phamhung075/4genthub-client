#!/usr/bin/env node
// check-contrast.mjs — the color logic, measured where it lands: text against its background.
//
// Contract (WCAG AA, and the taste skill's button/form contrast checks):
//   - normal text >= 4.5:1 against its effective background
//   - large text (>= 24px, or >= 18.66px bold) >= 3:1
//   - placeholders are reported in their own list: they label nothing on their own, so they warn
//     rather than fail, but a light placeholder on a near-white field is still a finding
// The effective background is the element's own background composited over its ancestors' until
// something opaque; text over a gradient or an image is counted as unknown and reported, never
// guessed. Each number printed is the real computed ratio, so it can be checked by eye in
// devtools.
import { ROUTES, VIEWPORTS, baseURL, contrastRatio, launchBrowser, parseArgs, parseColor, readAccount, reporter, signIn, waitForReady } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const pages = (flags.routes ?? '/login,/dashboard').split(',').map((s) => s.trim())
const MAX = 400 // candidates per page; the walk stays bounded on a dense screen

const collect = (max) => {
  const describe = (el) => {
    let out = el.tagName.toLowerCase()
    if (el.id) out += `#${el.id}`
    if (el.className && typeof el.className === 'string') out += `.${el.className.trim().split(/\s+/).slice(0, 2).join('.')}`
    return out.slice(0, 72)
  }
  const composite = (layers) => {
    let out = { r: 255, g: 255, b: 255 }
    for (let i = layers.length - 1; i >= 0; i--) {
      const l = layers[i]
      out = { r: l.a * l.r + (1 - l.a) * out.r, g: l.a * l.g + (1 - l.a) * out.g, b: l.a * l.b + (1 - l.a) * out.b }
    }
    return out
  }
  const bgOf = (el) => {
    const layers = []
    for (let node = el; node; node = node.parentElement) {
      const s = getComputedStyle(node)
      if (s.backgroundImage && s.backgroundImage !== 'none') return { unknown: `over a background-image (${describe(node)})` }
      const parsed = window.__parseColor(s.backgroundColor)
      if (parsed && parsed.a > 0) {
        layers.push(parsed)
        if (parsed.a >= 1) return { color: composite(layers) }
      }
    }
    layers.push({ r: 255, g: 255, b: 255, a: 1 })
    return { color: composite(layers) }
  }

  const candidates = []
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  const seen = new Set()
  for (let node = walker.nextNode(); node && candidates.length < max; node = walker.nextNode()) {
    const text = node.textContent.replace(/\s+/g, ' ').trim()
    if (text.length < 2) continue
    const el = node.parentElement
    if (!el || seen.has(el)) continue
    const rect = el.getBoundingClientRect()
    const s = getComputedStyle(el)
    if (rect.width === 0 || rect.height === 0 || s.visibility === 'hidden' || s.opacity === '0') continue
    seen.add(el)
    candidates.push({
      sel: describe(el),
      text: text.slice(0, 40),
      color: s.color,
      size: parseFloat(s.fontSize),
      weight: s.fontWeight,
      bg: bgOf(el),
    })
  }

  const placeholders = []
  for (const input of document.querySelectorAll('input[placeholder],textarea[placeholder]')) {
    const rect = input.getBoundingClientRect()
    if (rect.width === 0) continue
    placeholders.push({
      sel: describe(input),
      text: (input.getAttribute('placeholder') ?? '').slice(0, 30),
      color: getComputedStyle(input, '::placeholder').color,
      bg: bgOf(input),
    })
  }
  return { candidates, placeholders }
}

const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)
const context = await browser.newContext({ ...VIEWPORTS.desktop })
const page = await context.newPage()

// The page needs the color parser too; hand it the same function lib.mjs exports.
await context.addInitScript({ content: `window.__parseColor = ${parseColor.toString()}` })

// One route's worth of measurement, callable before or after sign-in.
async function measure(route) {
  try {
    await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
    await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
    // `networkidle` is not readiness for a client-rendered view: measured 2026-10-11, this read
    // landed on the 10-character pre-data placeholder and PASSed a page nobody had rendered yet.
    if (!(await waitForReady(page))) {
      r.fail(`${route}: the view never reached the 120-character readiness floor within 30s — not measured (the read would land on the pre-data placeholder)`)
      return
    }
    const { candidates, placeholders } = await page.evaluate(collect, MAX)
    const checked = []
    const unknown = []
    for (const c of candidates) {
      if (c.bg.unknown) { unknown.push(c); continue }
      const fg = parseColor(c.color)
      if (!fg) { unknown.push(c); continue }
      const large = c.size >= 24 || (c.size >= 18.66 && Number(c.weight) >= 700)
      checked.push({ ...c, ratio: contrastRatio(fg, c.bg.color), large })
    }
    checked.sort((a, b) => a.ratio - b.ratio)
    const violations = checked.filter((c) => c.ratio < (c.large ? 3 : 4.5))

    r.info(`${route}: ${checked.length} text runs measured, ${unknown.length} over images/gradients (not guessed)`)
    for (const v of violations.slice(0, 12)) {
      r.fail(`${route}: ${v.ratio.toFixed(2)}:1 (${v.large ? 'large' : 'normal'} text needs ${v.large ? '3' : '4.5'}:1) ${v.size}px/${v.weight} "${v.text}" in ${v.color} on ${v.sel}`)
    }
    if (violations.length > 12) r.note(`… and ${violations.length - 12} more below AA on ${route}`)
    if (!violations.length && checked.length) r.pass(`${route}: all ${checked.length} measured text runs meet AA`)
    if (violations.length > 12) r.fail(`${route}: ${violations.length} text runs are below AA in total`)

    const badPlaceholders = []
    for (const p of placeholders) {
      if (p.bg.unknown) continue
      const fg = parseColor(p.color)
      if (!fg) continue
      const ratio = contrastRatio(fg, p.bg.color)
      if (ratio < 4.5) badPlaceholders.push({ ...p, ratio })
    }
    for (const p of badPlaceholders.slice(0, 5)) {
      r.info(`${route}: placeholder "${p.text}" is ${p.ratio.toFixed(2)}:1 on ${p.sel} (reported, not failed)`)
    }
    for (const [i, u] of unknown.slice(0, 3).entries()) r.note(`not measured ${i + 1}: "${u.text}" on ${u.sel} ${u.bg.unknown}`)
  } catch (err) {
    r.fail(`${route}: contrast walk failed: ${String(err).split('\n')[0]}`)
  }
}

// Anonymous routes first, in a signed-out context: /login measured after sign-in is the post-login
// view mislabeled (measured 2026-10-11 — "Dashboard" text appeared under /login). No colorScheme
// emulation here, so both passes measure the default light surface.
const anonPages = pages.filter((p) => ROUTES.anonymous.includes(p))
const authPages = pages.filter((p) => !ROUTES.anonymous.includes(p))
for (const route of anonPages) await measure(route)

let signedIn = true
try {
  await page.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  await signIn(page, account)
} catch (err) {
  signedIn = false
  r.fail(`could not establish a session: ${String(err).split('\n')[0]}`)
}
for (const route of authPages) {
  if (!signedIn) { r.info(`${route}: skipped, no session`); continue }
  await measure(route)
}

await context.close().catch(() => {})
await browser.close().catch(() => {})
r.done('check-contrast')
