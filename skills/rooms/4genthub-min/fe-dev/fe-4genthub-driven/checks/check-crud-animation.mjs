#!/usr/bin/env node
// check-crud-animation.mjs — the CRUD state-change animations: are they wired to actually play?
//
// The trigger chain (source of truth: src/services/AnimationFactory.ts, read 2026-10-11):
// every hook (useTaskAnimation, useSubtaskAnimation, useBranchAnimation, useProjectAnimation),
// the realtime sync and WebSocketAnimationService all call animationFactory.animate(id, type),
// which builds the CSS class as `${entityType}Row${Type}Animation` and applies it to the row for
// exactly the registry duration (create 800ms, update 1700ms, delete 800ms), then removes it.
// The class names are built at RUNTIME by string concatenation — TypeScript cannot know whether
// any CSS defines them, so a rename on either side silently kills the animation. That drift is
// what this check exists to catch, black-box, in whatever the origin actually ships.
//
// Assertions, per entity x type:
//   1. the built class is defined in the shipped CSS (a class no rule defines animates nothing)
//   2. applying it to a live element starts a real animation of the CSS duration
//   3. the applier's registry duration is not SHORTER than the CSS duration (a class removed
//      before its animation ends truncates it; a longer hold after the end is benign, reported)
//   4. under prefers-reduced-motion: reduce, applying the class starts nothing
//   5. at rest (2.5s after load, on pages nobody has changed), no row carries any CRUD class —
//      the trigger must fire on a state change, never on render
// What a read-only production walk cannot prove: that a real CRUD event CALLS animate() — that
// link is covered by the frontend's own tests (websocket-animations-e2e.test.tsx,
// taskUpdateAnimation.test.ts) and by running a write-capable local stack.
import { VIEWPORTS, baseURL, launchBrowser, parseArgs, readAccount, reporter, signIn } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const base = baseURL(flags)
const pages = (flags.routes ?? '/seats,/dashboard').split(',').map((s) => s.trim())

// The matrix the app can trigger (AnimationFactory.buildCssClass) and the registry durations the
// applier uses for class removal. Kept here as the contract the CSS has to satisfy.
const ENTITIES = ['task', 'subtask', 'seat', 'branch', 'project']
const TYPES = ['create', 'update', 'delete']
const REGISTRY = { create: 800, update: 1700, delete: 800 }
// The hidden initial-state classes: a row may start with these and must shed them when its
// create animation takes over — one still present at rest is an invisible row.
const INITIAL = ['taskRowNew', 'subtaskRowNew']
const CRUD = ENTITIES.flatMap((e) => TYPES.map((t) => `${e}Row${t[0].toUpperCase()}${t.slice(1)}Animation`))
// 'complete' is registered in AnimationFactory and the frontend defines the classes, but it is not
// in the asserted matrix: this walk cannot see a caller inside a bundle, so it is reported, not
// asserted. It still has to be in the map the check collects — otherwise the report below reads a
// map that never asked for these names and calls every one of them undefined (paid for 2026-10-11).
const COMPLETE = ENTITIES.map((e) => `${e}RowCompleteAnimation`)

// NOTE: nothing may be referenced from module scope inside a page.evaluate function — Playwright
// serializes the function alone, so a module-level helper is a ReferenceError in the page. That
// error was once swallowed by an over-wide catch and reported as "1 stylesheet unreadable" while
// every class it held came back absent (paid for 2026-10-11). Keep page functions self-contained.

// Read every same-origin rule once: className -> { ms, reduce }. Media rules are descended into;
// a rule sitting in a prefers-reduced-motion block is recorded on the reduce side.
const collectCss = (names) => {
  // A Set does not survive page.evaluate's argument serialization; it is rebuilt here.
  const wanted = new Set(names)
  const map = {}
  const parseDur = (v) => {
    const m = /([\d.]+)(ms|s)/.exec(v || '')
    return m ? Math.round(parseFloat(m[1]) * (m[2] === 's' ? 1000 : 1)) : null
  }
  const note = (rule, reduce) => {
    for (const part of rule.selectorText.split(',')) {
      const name = part.trim().replace(/^\./, '')
      if (!wanted.has(name)) continue
      const off = rule.style.animationName === 'none' || rule.style.animationDuration === '0s'
      const entry = (map[name] ??= { ms: 0, reduce: false, reduceOff: false })
      if (reduce) {
        entry.reduce = true
        if (off) entry.reduceOff = true
      } else if (!off) {
        entry.ms = Math.max(entry.ms, parseDur(rule.style.animationDuration || rule.style.animation) ?? 0)
      }
    }
  }
  const walk = (rules, reduce) => {
    for (const rule of rules) {
      // NOTE the order: a CSSStyleRule also EXPOSES a cssRules property in modern Chrome (CSS
      // nesting), empty for a flat rule — testing it first skipped every plain rule and this
      // check reported the whole shipped CSS as absent. selectorText decides first.
      if (rule.selectorText) note(rule, reduce)
      if (rule.cssRules && rule.cssRules.length) {
        walk(rule.cssRules, reduce || /prefers-reduced-motion/.test(rule.conditionText || ''))
      }
    }
  }
  const meta = { sheets: 0, rules: 0, unreadable: [] }
  for (const sheet of document.styleSheets) {
    meta.sheets++
    let rules
    try {
      rules = sheet.cssRules // the ONLY thing that can legitimately fail here: a cross-origin sheet
    } catch {
      meta.unreadable.push(sheet.href || '(inline style)')
      continue
    }
    meta.rules += rules.length
    // Outside the try, deliberately: a bug in the walk must break the check loudly, never be
    // laundered into "unreadable" (which is exactly how it once hid, see the NOTE above).
    walk(rules, false)
  }
  return { map, meta }
}

// Apply the class to a real, rendered probe element and measure what the engine actually starts.
const engineProbe = async (className) => {
  const el = document.createElement('div')
  el.setAttribute('aria-hidden', 'true')
  el.style.cssText = 'position:fixed;top:0;left:0;width:6px;height:6px;pointer-events:none'
  document.body.appendChild(el)
  el.classList.add(className)
  await new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(res)))
  const anims = el.getAnimations().map((a) => ({
    name: a.animationName ?? '?',
    ms: typeof a.effect?.getTiming().duration === 'number' ? Math.round(a.effect.getTiming().duration) : null,
  }))
  el.classList.remove(className)
  el.remove()
  return anims
}

// Any CRUD or initial-state class present on any element of the settled page.
const restScan = (names) => {
  const wanted = new Set(names) // rebuilt: a Set does not survive argument serialization
  const hits = []
  for (const el of document.querySelectorAll('body *')) {
    for (const cls of el.classList) {
      if (wanted.has(cls)) hits.push(`${cls} on ${el.tagName.toLowerCase()}${el.id ? `#${el.id}` : ''}`)
    }
  }
  return hits
}

const { browser, feDir } = await launchBrowser(flags)
const account = readAccount(flags, feDir)

const warm = await browser.newContext({ ...VIEWPORTS.desktop })
const warmPage = await warm.newPage()
let sessionOk = true
try {
  await warmPage.goto(`${base}/login`, { waitUntil: 'networkidle', timeout: 60_000 })
  await signIn(warmPage, account)
} catch (err) {
  sessionOk = false
  r.fail(`could not establish a session: ${String(err).split('\n')[0]}`)
}
const state = await warm.storageState()
await warm.close().catch(() => {})

const names = new Set([...CRUD, ...COMPLETE])
const css = {}
const found = {}
const reduced = {}

if (sessionOk) {
  const context = await browser.newContext({ ...VIEWPORTS.desktop, storageState: state })
  const page = await context.newPage()
  await page.goto(`${base}${pages[0]}`, { waitUntil: 'load', timeout: 60_000 })
  await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
  const res = await page.evaluate(collectCss, [...names])
  Object.assign(css, res.map)
  r.info(`stylesheets: ${res.meta.sheets}, ${res.meta.rules} top-level rules; unreadable: ${res.meta.unreadable.length ? res.meta.unreadable.join(', ') : 'none'}`)

  for (const className of CRUD) {
    const defined = css[className]?.ms
    if (!defined) { found[className] = null; continue }
    found[className] = await page.evaluate(engineProbe, className)
  }

  // At rest on each walked route: nothing above may be present after the settle window. 2.5s
  // covers the longest class lifetime (1700ms) plus the 100ms coordination cooldown.
  for (const route of pages) {
    if (route !== pages[0]) await page.goto(`${base}${route}`, { waitUntil: 'load', timeout: 60_000 })
    await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
    await page.waitForTimeout(2500)
    const hits = await page.evaluate(restScan, [...names, ...INITIAL])
    if (hits.length === 0) r.pass(`at rest on ${route}: no CRUD animation class present (the trigger fires on change, not on render)`)
    else for (const hit of hits.slice(0, 8)) r.fail(`at rest on ${route}: ${hit} — an animation class survived the settle window`)
  }
  await context.close().catch(() => {})

  // The reduced-motion pass: the same probe under emulation must start nothing.
  const reduceContext = await browser.newContext({ ...VIEWPORTS.desktop, storageState: state, reducedMotion: 'reduce' })
  const reducePage = await reduceContext.newPage()
  await reducePage.goto(`${base}${pages[0]}`, { waitUntil: 'load', timeout: 60_000 })
  await reducePage.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
  for (const className of CRUD) {
    if (!found[className]) continue
    reduced[className] = await reducePage.evaluate(engineProbe, className)
  }
  await reduceContext.close().catch(() => {})
}

for (const entity of ENTITIES) {
  const lines = []
  let clean = true
  for (const type of TYPES) {
    const className = `${entity}Row${type[0].toUpperCase()}${type.slice(1)}Animation`
    const entry = css[className]
    if (!entry) {
      r.fail(`${className}: built by the applier but defined in no shipped CSS rule — the trigger lands a dead class, so ${entity} ${type} changes animate nothing`)
      continue
    }
    if (!entry.ms) {
      // Distinct from "no rule": a rule exists but carries no readable animation duration (a
      // reduce-only or empty declaration) — the class still cannot animate.
      r.fail(`${className}: a CSS rule defines it but no animation duration could be read — applying it cannot animate`)
      continue
    }
    const cssMs = entry.ms
    const anims = found[className]
    if (!anims || !anims.length) {
      r.fail(`${className}: defined in CSS (${cssMs}ms) but applying it started no animation in the engine`)
      continue
    }
    const ms = Math.max(...anims.map((a) => a.ms ?? 0))
    const drift = ms - REGISTRY[type]
    lines.push(`${type} ${ms}ms${drift > 0 ? ` (registry removes the class after ${REGISTRY[type]}ms — the last ${drift}ms are cut)` : drift < 0 ? ` (class held ${-drift}ms past the end, benign)` : ' = registry'}`)
    if (drift > 0) { r.fail(`${className}: the animation runs ${ms}ms but AnimationFactory removes the class after ${REGISTRY[type]}ms — it cannot finish`); clean = false }
    if (!css[className].reduceOff) {
      r.fail(`${className}: no prefers-reduced-motion: reduce rule disables it`)
      clean = false
    } else {
      const rAnims = (reduced[className] ?? []).filter((a) => (a.ms ?? 0) > 0)
      if (rAnims.length) { r.fail(`${className}: still animates ${rAnims[0].ms}ms under prefers-reduced-motion: reduce`); clean = false }
    }
  }
  // The summary line's verb has to match the entity's whole state: a PASS on a list that includes
  // "the last 200ms are cut" contradicts itself. Clean entities PASS, the rest print as INFO.
  if (lines.length) (clean ? r.pass : r.info)(`${entity} rows: ${lines.join('; ')}`)
}

// The registered-elsewhere corner, reported not asserted: 'complete' is in the registry and the
// frontend defines the classes, but this walk cannot see a caller inside a bundle, so the sentence
// says only what the stylesheet shows. The map above asks for these names, so this reading is a
// measurement rather than a lookup against names nobody collected.
if (sessionOk) {
  const defined = COMPLETE.filter((c) => css[c]?.ms)
  const missing = COMPLETE.filter((c) => !css[c]?.ms)
  r.info(`'complete': ${defined.length}/${COMPLETE.length} class(es) defined in the shipped CSS${defined.length ? ` (${defined.join(', ')})` : ''}${missing.length ? `; not defined: ${missing.join(', ')}` : ''}`)
}

await browser.close().catch(() => {})
r.done('check-crud-animation')
