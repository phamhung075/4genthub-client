#!/usr/bin/env node
// check-contrast.mjs - text against its effective background, WCAG AA, in the light AND the dark theme.
//
// Contract: normal text >= 4.5:1, large text (>= 24px, or >= 18.66px bold) >= 3:1. The effective background
// is the element's own background composited over its ancestors until something opaque; text over a
// gradient or image is counted as unknown and reported, never guessed. The theme is set the way the app
// reads it: localStorage `pdf_theme`, before the page loads (the head script applies it with no flash).
// Placeholders warn instead of fail. Each ratio printed is the real computed ratio, checkable in devtools.
import { VIEWPORTS, contrastRatio, launchBrowser, parseArgs, parseColor, reporter, target, waitForReady } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const site = await target(flags)
const browser = await launchBrowser(flags)
const routes = (typeof flags.routes === 'string' ? flags.routes : '/').split(',').map((s) => s.trim())
const MAX = 500

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
    candidates.push({ sel: describe(el), text: text.slice(0, 40), color: s.color, size: parseFloat(s.fontSize), weight: s.fontWeight, bg: bgOf(el) })
  }
  return { candidates }
}

for (const theme of ['light', 'dark']) {
  const context = await browser.newContext({ ...VIEWPORTS.desktop, colorScheme: theme })
  await context.addInitScript({ content: `window.__parseColor = ${parseColor.toString()}; try { localStorage.setItem('pdf_theme', '${theme}') } catch (e) {}` })
  const page = await context.newPage()
  for (const route of routes) {
    const label = `${route} [${theme}]`
    try {
      await page.goto(`${site.base}${route}`, { waitUntil: 'load', timeout: 60_000 })
      await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
      if (!(await waitForReady(page))) { r.fail(`${label}: never reached the readiness floor - not measured`); continue }
      const { candidates } = await page.evaluate(collect, MAX)
      const checked = []
      const unknown = []
      for (const c of candidates) {
        const fg = parseColor(c.color)
        if (c.bg.unknown || !fg) { unknown.push(c); continue }
        const large = c.size >= 24 || (c.size >= 18.66 && Number(c.weight) >= 700)
        checked.push({ ...c, ratio: contrastRatio(fg, c.bg.color), large })
      }
      checked.sort((a, b) => a.ratio - b.ratio)
      const violations = checked.filter((c) => c.ratio < (c.large ? 3 : 4.5))
      r.info(`${label}: ${checked.length} text runs measured, ${unknown.length} over images/gradients (not guessed)`)
      for (const v of violations.slice(0, 12)) r.fail(`${label}: ${v.ratio.toFixed(2)}:1 (${v.large ? 'large' : 'normal'} needs ${v.large ? '3' : '4.5'}:1) ${v.size}px/${v.weight} "${v.text}" in ${v.color} on ${v.sel}`)
      if (violations.length > 12) r.fail(`${label}: ${violations.length} text runs are below AA in total`)
      if (!violations.length && checked.length) r.pass(`${label}: all ${checked.length} measured text runs meet AA`)
    } catch (err) {
      r.fail(`${label}: contrast walk failed: ${String(err).split('\n')[0]}`)
    }
  }
  await context.close().catch(() => {})
}
await browser.close().catch(() => {})
await site.close()
r.done('check-contrast')
