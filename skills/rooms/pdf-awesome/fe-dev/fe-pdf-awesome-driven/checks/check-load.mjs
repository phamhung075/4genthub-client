#!/usr/bin/env node
// check-load.mjs - the front door and the PDF import path, in a real browser.
//
// Contract: the page loads with ZERO console errors and zero failed or 4xx/5xx requests; the three entry
// controls exist (#btnPickFiles, #btnPickPdf, #dropZone); and importing a real 3-page PDF through the
// #pdfInput the user's "Import PDF" button drives produces 3 page thumbnails in #thumbGrid and enables
// the next step (#btnNext). A page that renders but cannot import is a broken app.
import { VIEWPORTS, launchBrowser, makePdf, parseArgs, reporter, target, waitForReady, watchPage } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const site = await target(flags)
const browser = await launchBrowser(flags)
const context = await browser.newContext({ ...VIEWPORTS.desktop })
const page = await context.newPage()
const watch = watchPage(page)
const PAGES = 3

try {
  await page.goto(`${site.base}/`, { waitUntil: 'load', timeout: 60_000 })
  await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
  if (!(await waitForReady(page))) r.fail('/: the view never reached the 120-character readiness floor within 30s - not measured')
  else r.pass('/: the page rendered')

  for (const sel of ['#btnPickFiles', '#btnPickPdf', '#dropZone', '#pdfDropZone', '#thumbGrid', '#btnNext']) {
    const n = await page.locator(sel).count()
    if (n === 1) r.pass(`entry control ${sel} present`)
    else r.fail(`entry control ${sel}: found ${n}, expected 1`)
  }

  const pdf = makePdf(PAGES)
  await page.locator('#pdfInput').setInputFiles(pdf)
  const ok = await page
    .waitForFunction((n) => document.querySelectorAll('#thumbGrid > *').length >= n, PAGES, { timeout: 30_000 })
    .then(() => true, () => false)
  const thumbs = await page.locator('#thumbGrid > *').count()
  if (ok) r.pass(`PDF import: ${thumbs} thumbnails for a ${PAGES}-page PDF`)
  else r.fail(`PDF import: ${thumbs} thumbnails after 30s for a ${PAGES}-page PDF (expected ${PAGES})`)
  const next = await page.locator('#btnNext').isEnabled()
  if (next) r.pass('#btnNext is enabled after import')
  else r.fail('#btnNext is still disabled after import')

  if (watch.consoleErrors.length) for (const e of watch.consoleErrors.slice(0, 8)) r.fail(`console error: ${e}`)
  else r.pass('zero console errors')
  for (const f of [...watch.failed, ...watch.bad].slice(0, 8)) r.fail(`request problem: ${f}`)
  if (!watch.failed.length && !watch.bad.length) r.pass('zero failed or 4xx/5xx requests')
} catch (err) {
  r.fail(`load check crashed: ${String(err).split('\n')[0]}`)
}

await context.close().catch(() => {})
await browser.close().catch(() => {})
await site.close()
r.done('check-load')
