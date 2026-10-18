#!/usr/bin/env node
// check-parts.mjs - the split-into-parts flow, end to end, in a real browser.
//
// Contract: import a generated 6-page PDF, go to the export step, create two named parts, assign pages
// 1-3 to the first and 4-6 to the second through the range controls, and see the "Ungrouped" count fall
// to 0. Each part's Save button must download a PDF named after the part. A path with no browser check is
// a path nobody can claim works: when the parts UI changes, change this file in the same commit.
// Selectors are the ids/classes in index.html and js/ui/parts-panel.js; a renamed id fails loudly here.
import { VIEWPORTS, launchBrowser, makePdf, parseArgs, reporter, target, waitForReady, watchPage } from '../lib.mjs'

const { flags } = parseArgs(process.argv.slice(2))
const r = reporter()
const site = await target(flags)
const browser = await launchBrowser(flags)
const context = await browser.newContext({ ...VIEWPORTS.desktop, acceptDownloads: true })
await context.addInitScript(() => { window.confirm = () => true })
const page = await context.newPage()
const watch = watchPage(page)
const PAGES = 6
const NAMES = ['Alice', 'Bob']

const step = async (label, fn) => {
  try { await fn(); r.pass(label) } catch (err) { r.fail(`${label}: ${String(err).split('\n')[0]}`); throw err }
}

try {
  await page.goto(`${site.base}/`, { waitUntil: 'load', timeout: 60_000 })
  await page.waitForLoadState('networkidle', { timeout: 30_000 }).catch(() => {})
  if (!(await waitForReady(page))) throw new Error('page never reached the readiness floor')

  await step(`import a ${PAGES}-page PDF`, async () => {
    await page.locator('#pdfInput').setInputFiles(makePdf(PAGES))
    await page.waitForFunction((n) => document.querySelectorAll('#thumbGrid > *').length >= n, PAGES, { timeout: 30_000 })
  })
  await step('reach the export step', async () => {
    for (let i = 0; i < 2; i++) {
      await page.locator('#btnNext:not([disabled])').click({ timeout: 15_000 })
      await page.waitForTimeout(400)
    }
    await page.locator('#viewExport.active').waitFor({ timeout: 15_000 })
    await page.locator('#partsPanel').waitFor({ state: 'visible', timeout: 15_000 })
  })
  await step('create two parts and name them', async () => {
    for (let i = 0; i < NAMES.length; i++) await page.locator('#btnAddPart').click()
    const inputs = page.locator('#partsList .part-name')
    if ((await inputs.count()) !== NAMES.length) throw new Error(`expected ${NAMES.length} part rows, found ${await inputs.count()}`)
    for (let i = 0; i < NAMES.length; i++) {
      await inputs.nth(i).fill(NAMES[i])
      await inputs.nth(i).dispatchEvent('change')
    }
  })
  const assign = async (from, to, partIndex) => {
    await page.selectOption('#selPartFrom', String(from))
    await page.selectOption('#selPartTo', String(to))
    const opt = await page.locator('#selAssignPart option').evaluateAll((os, i) => os[i + 1]?.value, partIndex)
    await page.selectOption('#selAssignPart', opt)
    await page.locator('#btnAssign').click()
  }
  await step('assign pages 1-3 to the first part and 4-6 to the second', async () => {
    await assign(1, 3, 0)
    await assign(4, 6, 1)
  })
  await step('the Ungrouped count falls to 0 and each part counts 3 pages', async () => {
    const un = await page.locator('#partsUngroupedLabel').innerText()
    if (!/:\s*0\s/.test(un)) throw new Error(`ungrouped label reads "${un}"`)
    const metas = await page.locator('#partsList .part-row').evaluateAll((rows) => rows.map((x) => x.innerText))
    if (metas.length !== NAMES.length || !metas.every((m) => /3\s/.test(m))) throw new Error(`part rows read ${JSON.stringify(metas)}`)
  })
  for (const name of NAMES) {
    await step(`Save downloads "${name}.pdf"`, async () => {
      const idx = NAMES.indexOf(name)
      const [download] = await Promise.all([
        page.waitForEvent('download', { timeout: 30_000 }),
        page.locator('#partsList .part-save').nth(idx).click(),
      ])
      const got = download.suggestedFilename()
      if (got !== `${name}.pdf`) throw new Error(`downloaded "${got}"`)
    })
  }
  if (watch.consoleErrors.length) for (const e of watch.consoleErrors.slice(0, 8)) r.fail(`console error: ${e}`)
  else r.pass('zero console errors through the whole flow')
} catch {
  // the failing step already reported itself
}

await context.close().catch(() => {})
await browser.close().catch(() => {})
await site.close()
r.done('check-parts')
