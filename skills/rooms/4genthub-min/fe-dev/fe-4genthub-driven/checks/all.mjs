#!/usr/bin/env node
// all.mjs — the fast check: every check, in sequence, one summary.
//
//   node checks/all.mjs --base https://www.4genthub.com --fe <frontend checkout>
//   node checks/all.mjs --base http://localhost:3800  --fe <frontend checkout>   (a local build)
//
// Each check runs in its own process (its own browser, its own session) so one failure cannot
// poison the next, and every flag given here is passed through unchanged. The exit code is 1
// when any check failed, so this is also the one command a CI step or fe-dev needs to run.
import { spawnSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const checks = ['check-login.mjs', 'check-nav.mjs', 'check-backend.mjs', 'check-responsive.mjs', 'check-theme.mjs', 'check-motion.mjs', 'check-contrast.mjs', 'check-crud-animation.mjs']
const passThrough = process.argv.slice(2)

const results = []
for (const check of checks) {
  console.log(`\n${'='.repeat(72)}\n${check}\n${'='.repeat(72)}`)
  const run = spawnSync(process.execPath, [path.join(here, check), ...passThrough], { stdio: 'inherit' })
  if (run.error) console.error(`${check} could not start: ${run.error.message}`)
  results.push({ check, code: run.status ?? 1 })
}

console.log(`\n${'='.repeat(72)}\nsummary\n${'='.repeat(72)}`)
for (const { check, code } of results) console.log(`${code === 0 ? 'PASS' : 'FAIL'}  ${check}`)
const failed = results.filter((x) => x.code !== 0)
console.log(failed.length ? `\n${failed.length} of ${checks.length} checks failed` : `\nall ${checks.length} checks passed`)
process.exit(failed.length ? 1 : 0)
