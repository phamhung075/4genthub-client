# The evidence verb refuses a `tsc` that cannot compile, instead of recording its exit 0

`4genteam evidence --test` ran whatever argv it was handed and carried the exit code onto the task. npm
publishes a package NAMED `tsc`; it is not TypeScript, it prints `This is not the tsc command you are looking
for` and exits 0, and `npx tsc` resolves, installs and runs it whenever TypeScript is not installed. Measured
2026-10-21 on hub task `b7a2c44c`: `npx tsc --noEmit -p agenthub-frontend` at the hub root exited 0 in 0.88 s
having compiled nothing, and the completion gate recorded the typecheck row as passing. The same run at this
pin measures worse than the row said: `agenthub-frontend` has no `node_modules` at all (pnpm, lockfile
present, never installed in the worktree), and a bare `tsc` resolves on this machine to
`/mnt/c/Users/daihu/AppData/Roaming/npm/tsc` - the same placeholder through a global shim. So no typecheck
evidence this verb has stored from this tree has compiled anything.

`notTheCompiler` in `internal/clientevidence/evidence.go` refuses that run before it starts, in both shapes,
as a usage error (exit 2, no git, no dial, nothing submitted):

- a bare `tsc` must resolve, through `exec.LookPath`, inside a `node_modules/` directory. Outside one it is
  npm's package, and the refusal names the path it resolved to.
- a wrapper - `npx`, `npm exec`, `pnpm`/`pnpm exec`/`pnpm dlx`, `yarn`, `bun`/`bunx` - whose first non-flag,
  non-subcommand word is `tsc` must find `node_modules/typescript` in the directory the run happens in,
  because that is where the wrapper would resolve it. Without it, the wrapper goes to the registry and runs
  the placeholder; the refusal says `npm ci` first.

Everything else passes through untouched: a real typecheck under `node_modules`, and every command that is
not `tsc` in one of those two shapes. The rule is deliberately narrow - it does not try to judge any other
command's exit code.

Verified: `go test ./internal/clientevidence/... ./internal/clientcomplete/...` in `agenthub_client` - both
packages ok, with `TestEvidenceRefusesATscOutsideNodeModules`,
`TestEvidenceRefusesNpxTscWithoutALocalTypeScript` and `TestEvidenceRunsNpxTscWithALocalTypeScript` pinned to
the three shapes (refused, refused, run exactly as typed). Smoke through the built binary: `4genteam evidence
--task t --base main --test "npx tsc --noEmit -p agenthub-frontend"` at the hub root and again in
`agenthub-frontend` both exit 2 with the refusal, and `--test "tsc --noEmit"` names
`/mnt/c/Users/daihu/AppData/Roaming/npm/tsc`. The allowed shape is proven by the fixture test, since no
`node_modules/typescript` exists in this tree to run for real.

A room whose frontend is not installed can no longer submit a typecheck row as passing; it must `pnpm
install` first, or say the row was not run.
