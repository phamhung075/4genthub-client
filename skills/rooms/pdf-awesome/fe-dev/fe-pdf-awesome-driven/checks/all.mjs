#!/usr/bin/env node
// all.mjs - run every check in its own process, print one summary, exit 1 when any check failed.
// Usage: node checks/all.mjs [--root <pdf-awesome>] [--pw <dir with node_modules/@playwright/test>] [--base <origin>]
import { spawnSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const checks = ['check-load', 'check-responsive', 'check-contrast', 'check-motion']
const passthrough = process.argv.slice(2)
const results = []
for (const name of checks) {
  console.log(`\n=== ${name} ===`)
  const run = spawnSync(process.execPath, [path.join(here, `${name}.mjs`), ...passthrough], { stdio: 'inherit' })
  results.push({ name, code: run.status })
}
console.log('\n=== summary ===')
for (const { name, code } of results) console.log(`${code === 0 ? 'PASS' : 'FAIL'} ${name}${code > 1 ? ' (could not run)' : ''}`)
process.exit(results.some((x) => x.code !== 0) ? 1 : 0)
