// The seat's tool hook, written into <seat dir>/hooks/pre/ by the runtime at every seat start; omp loads it
// from there because the seat dir is its agent dir (PI_CODING_AGENT_DIR).
//
// It does two jobs and nothing else:
//   1. GUARD: block the calls the owner's standing rules forbid a seat - printing a secret from a `.env`
//      file, running Docker, changing the room's own tmux server, pushing (only the lead pushes, after the
//      reviewer gate and a green test run; every other seat does not), bypassing the git hooks, destructive
//      git, importing a tree from outside (a patch the guard cannot read), killing processes by pattern,
//      building the frontend locally (a vite build took 3.9 GB), and writing outside the directories
//      the launcher names for the seat ($AGENTHUB_WORKTREES, set by the supervisor from the room spec) or into the
//      frozen plan files and the guard's own sources.
//   2. LOG: append ONE line per tool call to <seat dir>/tool-calls.jsonl, so "what did this seat do, and was it
//      refused" is one `jq` away instead of a transcript read.
//
// The same file is also the agy seat's guard: agy's global `hooks.json` runs it as `node seat-guard.ts --agy-hook
// pre|post`, with the hook payload on stdin (see agyHook below). omp never passes that argument, so it loads the
// default export as before. One file keeps the rules, the log line and the redaction in one place.
//
// A line is {at, seat, session, tool, target, target_class, outcome, ms, bytes_out?, rule?, blocked_rule?, error?}. `outcome` is "ok", "error" or
// "blocked"; a blocked call carries the `rule` that refused it. An MCP call is the tool mcp__<server>_<tool>
// (a seat reaches it by writing to xd://mcp__...), and its `target` is mcp:<tool>.<action>, the form to grep for.
import { spawnSync } from "node:child_process";
import { appendFileSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { homedir, tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";

const seat = process.env.AGENTHUB_SEAT ?? "";

/// The one seat the owner's 2026-10-18 ruling lets push: the lead, after the reviewer gate and a green test
/// run. The exception is the seat KEY, not a room, a path or a name that merely starts with it, and it is
/// read from either spelling a runtime sets - `agenthub--<room>--<seat>` or `<room>-<seat>@<room>`.
const PUSH_AUTHORITY_SEAT = "lead";

/// The seat a session name names, in either spelling; "" when it names none.
function sessionSeat(name: string): string {
  const trimmed = name.trim();
  if (trimmed.startsWith("agenthub--")) {
    const rest = trimmed.slice("agenthub--".length);
    const at = rest.lastIndexOf("--");
    if (at <= 0 || at === rest.length - 2) return "";
    return rest.slice(at + 2);
  }
  const at = trimmed.lastIndexOf("@");
  if (at <= 0 || at === trimmed.length - 1) return "";
  const room = trimmed.slice(at + 1);
  const left = trimmed.slice(0, at);
  const prefix = `${room}-`;
  return left.startsWith(prefix) ? left.slice(prefix.length) : "";
}

function pushAuthority(): boolean {
  if (seat === PUSH_AUTHORITY_SEAT) return true;
  return [process.env.AGENTHUB_SESSION_NAME ?? "", process.env.OPENRIG_SESSION_NAME ?? ""].some(
    (name) => sessionSeat(name) === PUSH_AUTHORITY_SEAT,
  );
}
const logPath = join(process.env.PI_CODING_AGENT_DIR ?? ".", "tool-calls.jsonl");
const MAX_TARGET = 240;
// A refused command is kept whole: the rule that blocked it is judged by reading what it blocked.
const MAX_BLOCKED = 2000;

// A secret never reaches the log: a token or key assignment keeps its name and loses its value.
const SECRET = /\b([A-Za-z0-9_]*(?:TOKEN|KEY|SECRET|PASSWORD)[A-Za-z0-9_]*\s*[=:]\s*)\S+|(Bearer\s+)\S+/gi;

// One log line is one line: whitespace runs, newlines included, collapse to a single space.
const redact = (text: string): string =>
  text.replace(SECRET, (_m, name, bearer) => `${name ?? bearer}<redacted>`).replace(/\s+/g, " ").trim();

function describe(tool: string, input: Record<string, unknown>): string {
  if (tool.startsWith("mcp__")) {
    const name = tool.replace(/^mcp__.*?_http_/, "");
    return `mcp:${name}${input.action ? "." + String(input.action) : ""}`;
  }
  if (tool === "bash") return String(input.command ?? "");
  return String(input.path ?? input.pattern ?? "");
}

// The command a bash rule judges: the text a seat typed, minus heredoc bodies. A body is data (a note, a queue
// reason, a script fed to a tool) and its lines are not commands, yet the rules below split on newlines, so a body
// line that began with `tmux` or `git push` blocked an unrelated call (fe-dev's `queue close`, 2026-10-15).
const commandOf = (input: Record<string, unknown>): string =>
  String(input.command ?? "").replace(/(<<-?\s*(['"]?)(\w+)\2[^\n]*)\n[\s\S]*?\n[ \t]*\3[ \t]*(?=\n|$)/g, "$1");

// A .env FILE NAME: `.env` or `.env.<suffix>` standing alone as a path segment, not `import.meta.env` and not a
// template (`.env.sample`, `.env.example`), which hold no secret.
const ENV_FILE = /(^|[\s/"'=])\.env(?!\.(sample|example|template)\b)(\.[\w-]+)?(?=$|[\s"'|;&)])/;

// The statements of a command, for the rules that judge what is RUN: heredoc bodies are gone (commandOf), the
// text inside quotes is blanked, and the rest is split at `&&`, `||`, `;`, newline and pipe. A word inside a quoted
// note, `--body "run git push --dry-run"`, is data and not a command, and it refused a reviewer's queue row twice
// (2026-10-15). The price: a command hidden in quotes (`sh -c 'git push'`) is not seen, and the guard is a net for
// a seat's mistakes, not a sandbox.
const statements = (input: Record<string, unknown>): string[] =>
  commandOf(input)
    .replace(/"(?:\\.|[^"\\])*"|'[^']*'/g, (q) => q[0] + q[0])
    .split(/&&|\|\||;|\n|\|/);

// A statement whose COMMAND is `name`, after env/sudo/VAR= noise: `tmux ...` and `env -u TMUX tmux ...` match,
// `echo tmux is nice` does not. The capture is what follows the command name. An assignment's value may be EMPTY
// (`FOO= cmd`, which every shell accepts, and which is how a seat empties a marker it was handed), so the value is
// `\S*` and not `\S+`: with `\S+` the head was not eaten as noise, the anchor never reached the command, and EVERY
// rule built on `callOf` was stepped past (measured 2026-10-16 at d964b8b: `FOO= 4genteam room limits <rig> --usd
// 999`, `FOO= git push origin main` and `FOO= rm scripts/git-hooks/pre-commit` were all allowed).
const callOf = (name: string): RegExp =>
  new RegExp(
    `^\\s*(?:(?:env|sudo|command|exec|nohup)\\s+(?:-\\S+\\s+(?:[A-Za-z_]+\\s+)?|[A-Za-z_]+=\\S*\\s+)*|[A-Za-z_]+=\\S*\\s+)*${name}\\b(.*)$`,
    "s",
  );
const TMUX_CALL = callOf("tmux");
const DOCKER_CALL = callOf("docker(?:-compose)?");
const GIT_PUSH_CALL = callOf("git\\s+(?:-C\\s+\\S+\\s+)?push");
const GIT_CALL = callOf("git");
const PKILL_CALL = callOf("(?:pkill|killall)");
const PGREP_CALL = callOf("pgrep");
const KILL_CALL = callOf("kill");
const XARGS_CALL = callOf("xargs");
const TEE_CALL = callOf("tee");
const COPY_CALL = callOf("(?:cp|install|rsync|ln)");
// A move removes its source as well as filling its destination, so it contributes every operand.
const MOVE_CALL = callOf("mv");
// A statement that DELETES its operands or rewrites a file in place. Neither is a redirect, a `tee` or a copy
// target, so before this every path built this way was judged by NO rule: `rm <seat dir>/config.yml` and
// `rm <seat dir>/hooks/pre/seat-guard.ts` both left the seat unguarded while the edit-shaped forms were refused.
const DESTROY_CALL = callOf("(?:rm|unlink|truncate|shred)");
// A statement that makes or removes a DIRECTORY ENTRY without writing a byte: `touch` creates the file, `mkdir`
// and `rmdir` make and remove the directory. None of them is a redirect, a `tee` a copy target, a move, an
// in-place edit or a `dd`, so before this the path each of them built was judged by NO rule: the guard refused
// `echo x > <seat dir>/config.yml` while `touch <seat dir>/config.yml` and `mkdir -p <anywhere outside>` went
// through, and the same rule that reads this list (protected-paths, worktree-scope) never saw them.
const EMPTY_PATH_CALL = callOf("(?:touch|mkdir|rmdir)");
const IN_PLACE_CALL = callOf("(?:sed|perl)");
const DD_CALL = callOf("dd");
const IN_PLACE = /(^|\s)(-\S*i\S*|--in-place)\s/;
const DD_OF = /\bof=(\S+)/g;

// The verb that WRITES the room's ceilings. It goes through the client and never names a file, so no path rule can
// see it: measured 2026-10-16, `4genteam room limits <rig> --usd 999` was allowed while every FILE shape against
// the same ceiling (rm, sed -i, truncate -s 0, dd, the write tool) was refused. The binary may be reached by path,
// so its name is matched after any leading path segment.
const ROOM_LIMITS_CALL = callOf("(?:\\S*\\/)?4genteam\\s+room\\s+limits");
const LIMITS_WRITE = /(^|\s)--(?:usd|calls|on|off)(?:\s|=|$)/;

// The verb that APPENDS a row to the room's rail. Like room limits it names no file, so no path rule sees it, and
// a seat that says the same thing twice costs the rail a row nothing will read (on 2026-10-17 at 06:46Z the rail
// held 311 rows, ~30 of them repeats of a seat's own earlier row). The create's destination and tag are parsed out
// of the statement; the rows it could duplicate are read from the room's own store, below.
const QUEUE_CREATE_CALL = callOf("(?:\\S*\\/)?4genteam\\s+room\\s+queue\\s+create");
// The window the predicate is read in: a second pending row to the same destination with a tag in common is refused
// while the first is younger than this. Inclusive: a row created 600 s ago still refuses; 601 s does not.
const COALESCE_WINDOW_MS = 10 * 60 * 1000;

// A frontend build in a seat is the RAM the owner does not have: two vite builds in /tmp/fe-tip took 3.9 GB and
// free RAM sat at 4 of 16 (2026-10-17, the likely 04:00 OOM, and the owner killed both). The deploy builds the
// frontend. Every spelling that BUILDS is refused; a --version/--help probe builds nothing and passes.
const VITE_CALL = callOf("(?:(?:\\S*\\/)?node_modules\\/\\.bin\\/)?vite");
const VITE_VIA_RUNNER = callOf("(?:npx|pnpm|yarn|bun)\\s+(?:exec\\s+|dlx\\s+|x\\s+)?(?:(?:\\S*\\/)?node_modules\\/\\.bin\\/)?vite");
const RUN_BUILD_CALL = callOf("(?:npm|pnpm|yarn|bun)\\s+(?:run\\s+)?build(?::\\S+)?(?:\\s|$)");
const PROBE_ONLY = /^\s*(?:--version|-v|--help|-h)(?:\s+(?:--version|-v|--help|-h))*\s*$/;
const FRONTEND_BUILD_REFUSAL =
  "a local frontend build takes the RAM the owner does not have (two vite builds took 3.9 GB; free RAM 4 of 16): the deploy builds the frontend, and the local check is `npx tsc --noEmit`";

// A git statement as verb and arguments, after the global options (`-C dir`, `-c k=v`, `--no-pager`, ...).
function gitOf(part: string): { verb: string; args: string[] } | null {
  const call = part.match(GIT_CALL);
  if (!call) return null;
  let rest = call[1];
  for (let next = rest.replace(/^\s*(?:-C\s+\S+|-c\s+\S+|--no-pager|-P|--git-dir=\S+|--work-tree=\S+)/, ""); next !== rest; ) {
    rest = next;
    next = rest.replace(/^\s*(?:-C\s+\S+|-c\s+\S+|--no-pager|-P|--git-dir=\S+|--work-tree=\S+)/, "");
  }
  const [verb = "", ...args] = rest.trim().split(/\s+/);
  return { verb, args };
}

const hasShort = (args: string[], letter: string): boolean => args.some((a) => new RegExp(`^-[A-Za-z]*${letter}[A-Za-z]*$`).test(a));

// What a git statement destroys, or "" when it destroys nothing: work in the tree, the index, history or the remote.
function destroys({ verb, args }: { verb: string; args: string[] }): string {
  if (verb === "reset" && args.includes("--hard")) return "git reset --hard discards every uncommitted change in the tree";
  if (verb === "clean" && (args.includes("--force") || hasShort(args, "f"))) return "git clean -f deletes untracked files";
  if (verb === "checkout" && (args[0] === "." || args.join(" ").includes("-- ."))) return "git checkout -- . discards every uncommitted change in the tree";
  if (verb === "restore" && args.includes(".")) return "git restore . discards every uncommitted change in the tree";
  if (verb === "stash" && /^(drop|clear)$/.test(args[0] ?? "")) return "git stash drop/clear deletes stashed work";
  if (verb === "push" && (args.some((a) => /^--force/.test(a) || a.startsWith("+")) || hasShort(args, "f"))) return "a force push rewrites the remote";
  if (verb === "add" && (args.includes("--all") || args.includes(".") || hasShort(args, "A"))) return "git add -A/. stages other seats' and stray files; commit with a pathspec: git commit -- <paths>";
  if (verb === "commit" && args.includes("--amend")) return "git commit --amend rewrites a commit; make a new commit";
  return "";
}

// ---- what a patch would write ---------------------------------------------------------------------------------
// A patch NAMES a file to read and a tree to write: the protected paths are never arguments, so no write-target
// rule could see them, and `git apply guard.patch` / `patch -p1 < guard.diff` wrote the guard that refuses them
// (measured 2026-10-20, task 1c06bcd0). The test below is on the TAIL of a header token - a basename, or a
// protected directory anywhere in the path - so it never has to emulate `-p`: stripping a prefix shortens a path
// and cannot hide a tail, while -p is in measured use 23 times across the five seats. A destination-rewriting flag
// (`--directory`, `--prefix`, `--unsafe-paths`, `patch -d`) moves the write to a path no header line names, so
// those are refused instead of composed: a wrong composition order fails OPEN, which is the hole itself.
const unquote = (token: string): string => {
  const quoted = token.match(/^"(.*)"$/) ?? token.match(/^'(.*)'$/);
  if (!quoted) return token;
  return quoted[1]
    .replace(/\\([0-7]{3})/g, (_, oct: string) => String.fromCharCode(Number.parseInt(oct, 8)))
    .replace(/\\\\/g, "\\")
    .replace(/\\"/g, '"');
};

// One header value: a quoted path, else the value up to a tab (a plain `---`/`+++` line may carry a timestamp).
const headerPath = (rest: string): string => {
  const text = rest.trim();
  if (text.startsWith('"')) {
    const end = text.slice(1).search(/(?<!\\)"/);
    return unquote(end < 0 ? text : text.slice(0, end + 2));
  }
  return text.split("\t")[0].trim();
};

// Every path a patch document names, on either side of every form git emits: `diff --git a/X b/Y`, the `---`/`+++`
// pair (a deletion's `+++ /dev/null` still leaves the `-` side naming the file), rename and copy, and the
// "Binary files a/X and b/Y differ" line. The WHOLE document is read, so an mbox with several messages is covered,
// and a `GIT binary patch` block is caught by the `diff --git` line that precedes it.
function patchNames(text: string): string[] {
  const names: string[] = [];
  for (const line of text.split("\n")) {
    if (line.startsWith("diff --git ")) {
      const pair = line.slice("diff --git ".length).match(/^("(?:\\.|[^"\\])*"|\S+)\s+("(?:\\.|[^"\\])*"|\S+)$/);
      if (pair) names.push(unquote(pair[1]), unquote(pair[2]));
      continue;
    }
    if (line.startsWith("--- ") || line.startsWith("+++ ")) names.push(headerPath(line.slice(4)));
    const moved = line.match(/^(?:rename|copy) (?:from|to) (.*)$/);
    if (moved) names.push(headerPath(moved[1]));
    const binary = line.match(/^Binary files (.*) and (.*) differ$/);
    if (binary) names.push(headerPath(binary[1]), headerPath(binary[2]));
  }
  return names.filter((n) => n && n !== "/dev/null");
}

// The patch a statement reads: the word after a `<` redirect, else its first operand. A bare `-`, a pipe with no
// operand and a `patch` with no argument name no document at all, and null is how that is reported - the caller
// refuses, because the alternative is a document the guard never saw.
function patchSource(part: string, args: string[]): string | null {
  const redirect = part.match(/(?<![0-9&>])<\s*([^\s<>|;&]+)/);
  if (redirect) return unquote(redirect[1]);
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === "-p" || arg === "-d") {
      i += 1;
      continue;
    }
    if (arg.startsWith("-")) continue;
    return unquote(arg);
  }
  return null;
}

// The refusal every one of these verbs shares when it would write a protected path.
const OWNERS_GUARD =
  "the git hooks and the seat guard are the owner's to change: this would write a protected path. Land the change as a commit the lead merges, or ask the owner to apply it";

// ---- what a REF would import ----------------------------------------------------------------------------------
// A stash entry IS a commit, and the owner makes stashes unguarded, so `git checkout <stash>`, `git switch <stash>`,
// `git cherry-pick <stash>`, `git merge <stash>` and `git rebase <stash>` imported the guard through a source no rule
// read: measured 2026-10-21 (task 1c06bcd0) - the base tree's asset read `const old = 1;`, a stash entry's tree read
// `const victim = 1;`, and after `git checkout -q <entry>` the working tree held the entry's bytes with no protected
// path named anywhere in the command. The rule therefore triggers on the SOURCE of an import, not on the verb: every
// operand is resolved, and one resolving into the stash reflog is read the way `git stash pop` already is. A plain
// commit operand stays allowed - a guard change in a commit was made by the lead or the owner, the sanctioned path.
const stashSeen = new Map<string, Set<string>>();
function stashEntries(here: string): Set<string> {
  const seen = stashSeen.get(here);
  if (seen) return seen;
  // A reflog that cannot be read yields no entries: with no stash there is no entry to import, so nothing is written.
  const out = spawnSync("git", ["-C", here, "reflog", "refs/stash", "--format=%H"], { encoding: "utf8" });
  const set = new Set(
    out.status === 0 ? String(out.stdout ?? "").split("\n").map((line) => line.trim()).filter(Boolean) : [],
  );
  stashSeen.set(here, set);
  return set;
}

// What an operand names as a commit, or null when it names no commit at all - `switch -c x <ref>` CREATES `x`, so an
// operand that does not resolve yet is allowed: the verb fails or writes something the guard did not have to guess.
function resolved(here: string, op: string): string | null {
  const out = spawnSync("git", ["-C", here, "rev-parse", "--verify", `${op}^{commit}`], { encoding: "utf8" });
  if (out.status !== 0) return null;
  return String(out.stdout ?? "").trim() || null;
}

// The paths a stash entry would write, read the way `git stash pop` is read, or null when the entry cannot be read.
// An unreadable entry is null and not an empty list: this read is the one that exists to see the guard.
function stashWrites(here: string, ref: string): string[] | null {
  const tracked = spawnSync("git", ["-C", here, "diff", "--name-only", "--no-renames", `${ref}^1`, ref], { encoding: "utf8" });
  if (tracked.status !== 0) return null;
  // A stash made with -u keeps its untracked files in a third parent; that read failing means there are none.
  const untracked = spawnSync("git", ["-C", here, "ls-tree", "-r", "--name-only", `${ref}^3`], { encoding: "utf8" });
  return `${String(tracked.stdout ?? "")}\n${String(untracked.stdout ?? "")}`
    .split("\n")
    .map((name) => name.trim())
    .filter(Boolean);
}

// The paths git itself lists for the tree or the index, which is how `git stash push` names what it would revert.
function listedPaths(here: string, ...args: string[]): string[] {
  const out = spawnSync("git", ["-C", here, ...args, "--name-only"], { encoding: "utf8" });
  return out.status === 0
    ? String(out.stdout ?? "").split("\n").map((name) => name.trim()).filter(Boolean)
    : [];
}

const unreadableStash =
  "the stash entry cannot be read, so the guard cannot see what it writes; look at it with `git stash show -p` and apply it with the owner";


// A pid is owned when it descends from the guard's process or its parent (the seat's agent): a seat may stop what
// it started, not another seat's processes or a daemon.
const parentOf = (pid: number): number => {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    return Number(stat.slice(stat.lastIndexOf(")") + 2).split(" ")[1]);
  } catch {
    return 0;
  }
};
const owned = (pid: number): boolean => {
  const roots = new Set([process.pid, process.ppid]);
  for (let p = pid, hops = 0; p > 1 && hops < 64; p = parentOf(p), hops++) if (roots.has(p)) return true;
  return false;
};

// Why a statement kills processes by pattern or kills a pid the seat does not own, or "".
function killReason(part: string): string {
  if (PKILL_CALL.test(part)) return "pkill/killall kill by name and take other seats' processes with them";
  const pgrep = part.match(PGREP_CALL);
  if (pgrep && (/(^|\s)--full\b/.test(pgrep[1]) || hasShort(pgrep[1].trim().split(/\s+/), "f")))
    return "pgrep -f matches every seat's command line";
  const xargs = part.match(XARGS_CALL);
  if (xargs && /(^|\s)kill\b/.test(xargs[1])) return "kill fed from a pipe kills pids nobody checked";
  const kill = part.match(KILL_CALL);
  if (!kill) return "";
  const args = kill[1].trim().split(/\s+/).filter(Boolean);
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "-s" || args[i] === "-n") i++;
    else if (args[i].startsWith("-")) continue;
    else if (!/^\d+$/.test(args[i])) return "kill takes literal pids; a pid from a pattern or substitution can be another seat's process";
    else if (!owned(Number(args[i]))) return `pid ${args[i]} was not started by this seat`;
  }
  return "";
}

// ---- writes ---------------------------------------------------------------------------------------------------
// The paths a call writes: an edit tool's path, and from a bash statement the target of a redirect, `tee`, and the
// last argument of cp/mv/install/rsync. A path with a variable or a substitution cannot be judged and is not.
const WRITE_TOOLS = new Set(["edit", "write", "ast_edit", "write_to_file", "replace_file_content", "multi_replace_file_content"]);
const REDIRECT = /(?<![-=>])>>?\s*([^\s|;&<>()]+)/g;

function writeTargets(tool: string, input: Record<string, unknown>): string[] {
  const raw: string[] = [];
  if (WRITE_TOOLS.has(tool)) raw.push(String(input.path ?? ""));
  else if (tool === "bash") {
    for (const part of statements(input)) {
      for (const m of part.matchAll(REDIRECT)) raw.push(m[1]);
      const operands = (rest: string) => rest.trim().split(/\s+/).filter((a) => a && !a.startsWith("-"));
      const tee = part.match(TEE_CALL);
      if (tee) raw.push(...operands(tee[1]));
      const copy = part.match(COPY_CALL);
      if (copy) raw.push(...operands(copy[1]).slice(-1));
      const move = part.match(MOVE_CALL);
      if (move) raw.push(...operands(move[1]));
      const destroy = part.match(DESTROY_CALL);
      if (destroy) raw.push(...operands(destroy[1]));
      const emptyPath = part.match(EMPTY_PATH_CALL);
      if (emptyPath) raw.push(...operands(emptyPath[1]));
      const inPlace = part.match(IN_PLACE_CALL);
      if (inPlace && IN_PLACE.test(inPlace[1])) raw.push(...operands(inPlace[1]).slice(-1));
      const dd = part.match(DD_CALL);
      if (dd) for (const m of dd[1].matchAll(DD_OF)) raw.push(m[1]);
    }
  }
  return raw.filter((p) => p && p !== "/dev/null" && !/^(""|'')$/.test(p) && !/[$`]/.test(p));
}

// The directories the launcher gives this seat, `AGENTHUB_WORKTREES`: the room spec's own directory for it first -
// where its pane starts, and what a relative path is resolved against - then the seat's own worktrees under the
// room's wt/, one per repo it edits. The guard derives none of it: the runtime owns the room layout, and a guard
// that had to guess could not know which checkout is the one every seat on this host shares. A list that never
// arrived leaves the seat its own directory and the temp dir and nothing else: the session environment decides the
// workspace, so failing closed is the only honest reading.
const worktrees = (process.env.AGENTHUB_WORKTREES ?? "").split(":").filter(Boolean).map((p) => resolve(p));
const absolute = (path: string): string =>
  resolve(worktrees[0] ?? process.env.PI_CODING_AGENT_DIR ?? ".", path.replace(/^~(?=\/|$)/, homedir()));
const within = (path: string, root: string): boolean => path === root || path.startsWith(root + "/");

// A LANE THE SEAT OPENS MID-SESSION is admitted by NAME, not by a list the guard would have to re-read: the runtime
// hands the room's `wt/` (AGENTHUB_LANES_ROOT) beside the list, and a first segment equal to the seat key or
// starting `<seat>-` under it is that seat's own lane. The room refuses a spec whose seat keys would make two seats
// share such a prefix, so the test stays a test. Unset (an older launch) admits no lane, exactly as before.
const lanesRoot = process.env.AGENTHUB_LANES_ROOT ? resolve(process.env.AGENTHUB_LANES_ROOT) : "";
const lane = (abs: string): boolean => {
  if (!lanesRoot || !seat || !abs.startsWith(lanesRoot + "/")) return false;
  const first = abs.slice(lanesRoot.length + 1).split("/")[0] ?? "";
  return first === seat || first.startsWith(`${seat}-`);
};

// The plan files are frozen: NEXT-GEN*.md, GATE-*.md and ai_docs/reports-status/ were written by a past review and
// a seat that edits them is writing a report instead of shipping.
const frozen = (abs: string): boolean =>
  /^NEXT-GEN.*\.md$/.test(basename(abs)) || /^GATE-.*\.md$/.test(basename(abs)) || abs.includes("/ai_docs/reports-status/");
// What enforces the rules is not the seat's to edit: the git hooks, the guard source, the installed guard, agy's hook
// list - and, inside the seat's own directory, the two files the guard rides: the omp config its deny patterns are
// rendered into (`4genteam policy apply`, header "do not edit") and the log its refusals are recorded in. worktree-scope
// ALLOWS the seat its own directory, so these two are named by DIRECTORY and not by file name: a project's own
// config.yml stays the seat's to edit.
const seatDir = resolve(process.env.PI_CODING_AGENT_DIR ?? ".");
// `parked.json` is the breaker's record that this seat is stopped, in the seat's own directory; the ROOM's
// limits.json is the budget the breaker enforces and the guard derives from the same seat dir (two levels up).
const SEAT_FILES: Record<string, true> = { "config.yml": true, "tool-calls.jsonl": true, "parked.json": true };
const roomBudget = resolve(seatDir, "..", "..", "limits.json");
// The three of these name a DIRECTORY as well as the files inside it. A trailing slash alone covered only the
// CONTENTS, and the seat's own directory is inside worktree-scope, so `mkdir -p <seat dir>/hooks/pre` and
// `rmdir <seat dir>/hooks/pre` - like an `rm -rf` of either hooks directory - were judged by NO rule: the path
// that keeps the guard installed was itself the one path no rule saw.
const PROTECTED_DIRS = /(^|\/)(hooks\/pre|scripts\/git-hooks|\.git\/hooks)(\/|$)/;
const protectedPath = (abs: string): boolean =>
  PROTECTED_DIRS.test(abs) || basename(abs) === "seat-guard.ts" || abs.endsWith("/.gemini/config/hooks.json") ||
  (dirname(abs) === seatDir && SEAT_FILES[basename(abs)] === true) || abs === roomBudget;

// ---- the room's rail, read-only --------------------------------------------------------------------------------
// A create is judged against the room's OWN store: `<state root>/rooms/<room>/queue.sqlite`, beside the room's
// limits.json, written by the room daemon. The client never opens it, and neither does this guard write it - the
// handle is opened read-only, and a store this runtime cannot open (no node:sqlite and no bun:sqlite) lets the
// create through: the guard is a net for a seat's repeats, not a second rail.
// The seat dir is `<state root>/rooms/<room>/seats/<seat>`: three levels up is the ROOMS root, and a create names
// its room, so the store judged is always the one the daemon would write.
const roomsRoot = resolve(seatDir, "..", "..", "..");
const VALUE_FLAGS: Record<string, true> = {
  "--to": true,
  "--summary": true,
  "--body": true,
  "--priority": true,
  "--reply-to": true,
  "--tag": true,
  "--state-root": true,
  "--socket": true,
};

/// The create's own arguments: the ROOM positional, `--to`, and `--tag` (one comma-separated list, as the client
/// parses it). A value the statement quotes is blanked by `statements` and the rule then skips - a net, not a parser.
function createOf(part: string): { room: string; to: string; tags: string[] } | null {
  const call = part.match(QUEUE_CREATE_CALL);
  if (!call) return null;
  const words = call[1].trim().split(/\s+/).filter(Boolean);
  let room = "", to = "", raw = "";
  for (let i = 0; i < words.length; i++) {
    const w = words[i];
    const eq = w.match(/^(--[a-z-]+)=(.*)$/);
    if (eq) {
      if (eq[1] === "--to") to = eq[2];
      if (eq[1] === "--tag") raw = eq[2];
      continue;
    }
    if (VALUE_FLAGS[w] === true) {
      const v = words[++i] ?? "";
      if (w === "--to") to = v;
      if (w === "--tag") raw = v;
      continue;
    }
    if (w.startsWith("-") || !room) room ||= w;
  }
  return { room, to, tags: raw.split(",").map((t) => t.trim()).filter(Boolean) };
}

type QueueReader = { rows: (sql: string, params: string[]) => Record<string, unknown>[]; close: () => void };

let cjsRequire: ((id: string) => any) | null | undefined;
function openQueue(path: string): QueueReader | null {
  if (cjsRequire === undefined) {
    try { cjsRequire = createRequire(import.meta.url); } catch { cjsRequire = null; }
  }
  if (!cjsRequire) return null;
  const req = cjsRequire;
  try {
    const { DatabaseSync } = req("node:sqlite");
    const db = new DatabaseSync(path, { readOnly: true });
    return { rows: (sql, params) => db.prepare(sql).all(...params), close: () => db.close() };
  } catch { /* not node: try Bun, which omp runs in */ }
  try {
    const { Database } = req("bun:sqlite");
    const db = new Database(path, { readonly: true });
    return { rows: (sql, params) => db.query(sql).all(...params), close: () => db.close() };
  } catch { return null; }
}

/// The pending row this create would duplicate, named as its refusal, or "" when nothing already says it: the row
/// is PENDING, the SOURCE is this seat's session (`AGENTHUB_SESSION_NAME`, the store's own source of a create),
/// the DESTINATION is the create's `--to`, the TAG SETS INTERSECT, and the row was created within the window.
function coalescingRow(create: { room: string; to: string; tags: string[] }): string {
  const session = process.env.AGENTHUB_SESSION_NAME ?? "";
  if (!session || !create.room || !create.to || !create.tags.length) return "";
  const db = openQueue(join(roomsRoot, create.room, "queue.sqlite"));
  if (!db) return "";
  try {
    const rows = db.rows(
      "SELECT qitem_id, tags, ts_created FROM queue_rows \
       WHERE state = 'pending' AND source_session = ? AND destination_session = ? \
       ORDER BY ts_created DESC",
      [session, create.to],
    );
    for (const row of rows) {
      let tags: string[] = [];
      try { tags = JSON.parse(String(row.tags ?? "[]")); } catch { continue; }
      const shared = create.tags.find((t) => Array.isArray(tags) && tags.includes(t));
      if (!shared) continue;
      const age = Date.now() - Date.parse(String(row.ts_created ?? ""));
      if (!Number.isFinite(age) || age < 0 || age > COALESCE_WINDOW_MS) continue;
      return `${row.qitem_id} is already pending to ${create.to} with tag ${shared} (created ${row.ts_created}); update that row with \`room queue update\` instead of opening a second one inside 10 minutes`;
    }
  } catch {
    // A store this reader cannot read must not stop the seat's work.
  } finally {
    try { db.close(); } catch { /* a handle that is already closed is fine */ }
  }
  return "";
}

// The rules. Each returns the reason a call is refused, or "" to let it through.
const RULES: { rule: string; check: (tool: string, input: Record<string, unknown>) => string }[] = [
  {
    rule: "env-secret",
    check: (tool, input) => {
      if (tool === "read" && /(^|\/)\.env[^/]*$/.test(String(input.path ?? "")))
        return "reading a .env file prints its secrets; check a name with `grep -c NAME FILE` instead";
      if (tool !== "bash") return "";
      const cmd = commandOf(input);
      // A command that names a .env file and prints from it. `grep -c/-q/-l/-o` and `source` print no value.
      for (const part of cmd.split(/&&|\|\||;|\n/)) {
        if (!ENV_FILE.test(part)) continue;
        if (/\b(cat|head|tail|less|more|bat|sed|awk|cut|od|xxd|strings)\b/.test(part))
          return "this command prints a .env file; check a name with `grep -c NAME FILE` instead";
        const grep = part.match(/\bgrep\s+((?:-[A-Za-z]+\s+)*)/);
        if (grep && !/-[A-Za-z]*[cqlo]/.test(grep[1]))
          return "this grep prints .env lines; use `grep -c`, `-q`, `-l` or `-o '^NAME'` so no value is printed";
      }
      return "";
    },
  },
  {
    rule: "docker",
    check: (tool, input) =>
      tool === "bash" && statements(input).some((part) => DOCKER_CALL.test(part))
        ? "Docker is not run from a seat; report what the container change is for to the lead"
        : "",
  },
  {
    // $TMUX is set inside a seat's pane, so a bare tmux command that CHANGES something talks to THE ROOM'S OWN
    // server: `tmux kill-server` there ended every seat (fe-dev's repro, 2026-10-15), and `new-session` or
    // `send-keys` would write into the seats' own panes. `TMUX_TMPDIR` does not help, because $TMUX wins. A
    // command that names a server with -S or -L, or reads only (list-*, display-message, capture-pane, show-*),
    // passes.
    rule: "tmux-mutate",
    check: (tool, input) =>
      tool === "bash" &&
      statements(input).some((part) => {
          const call = part.match(TMUX_CALL);
          if (!call || /\s-[SL]\s/.test(call[1])) return false;
          const verb = call[1].match(/\b([a-z][a-z-]+)\b/);
          return !verb || !/^(list-[a-z]+|ls|display-message|display|capture-pane|show-[a-z]+|has-session|info|-V)$/.test(verb[1]);
        })
        ? "a bare tmux command that changes anything acts on the room's own tmux server, because $TMUX points there; use `env -u TMUX tmux -L <scratch-name> ...`, or read with list-*, display-message or capture-pane"
        : "",
  },
  {
    rule: "hook-bypass",
    check: (tool, input) => {
      if (tool !== "bash") return "";
      for (const part of statements(input)) {
        const git = gitOf(part);
        if (/\bOWNER_DOCS_COMMIT\s*=/.test(part))
          return "OWNER_DOCS_COMMIT belongs to the owner: the docs-only commit hook is how the owner's rule is enforced";
        if (/\b(?:env\b.*\s-u\s*|unset\s+)AGENTHUB_SESSION_NAME\b/.test(part))
          return "AGENTHUB_SESSION_NAME is the seat's identity for the commit hooks; do not remove it";
        if (git && /\bcore\.hooksPath\b/i.test(part)) return "core.hooksPath turns the git hooks off; they are not the seat's to change";
        if (git?.verb === "commit" && (git.args.includes("--no-verify") || hasShort(git.args, "n")))
          return "git commit --no-verify skips the hooks; fix what the hook reports";
        if (git?.verb === "merge" && git.args.includes("--no-verify")) return "git merge --no-verify skips the hooks";
      }
      return "";
    },
  },
  {
    rule: "git-destructive",
    check: (tool, input) => {
      if (tool !== "bash") return "";
      for (const part of statements(input)) {
        const git = gitOf(part);
        const reason = git ? destroys(git) : "";
        if (reason) return reason;
      }
      return "";
    },
  },
  {
    // WHAT A VERB IMPORTS IS NOT AN ARGUMENT. `git apply`, `git am` and `patch` name a document to read and a tree
    // to write, and neither is the protected path, so the write-target rules saw nothing: all six were allowed at
    // the pin and one of them rewrote this file (measured 2026-10-20, task 1c06bcd0). `git checkout -- <path>`,
    // `git restore <path>` and `git stash pop|apply|branch` import from the index or a stash entry, so they are read
    // here too - a stash may have been made by the owner, unguarded. The verbs that import COMMITTED content
    // (checkout, switch, cherry-pick, merge, rebase) stay allowed on a plain commit, and are read on an operand that
    // resolves into the stash reflog, because a stash entry IS a commit (measured 2026-10-21, revision 6).
    rule: "git-imports-a-tree",
    check: (tool, input) => {
      if (tool !== "bash") return "";
      let dir = process.cwd();
      // A name a statement in THIS call makes at a stash entry: `git branch sneaky stash@{0} && git cherry-pick sneaky`
      // is one tool call, and at hook time `sneaky` does not exist yet, so the alias has to be remembered rather than
      // resolved (measured 2026-10-21, revision 6 - without this the pair was allowed and wrote the guard).
      const aliases = new Map<string, string>();
      for (const part of statements(input)) {
        // The directory a patch path resolves against: a `cd` in this statement, else the tracked one. A `cd` inside
        // a subshell is not tracked, and the read then fails closed.
        const cd = part.trim().match(/^cd\s+(\S+)/);
        if (cd) {
          dir = resolve(dir, unquote(cd[1]));
          continue;
        }
        const git = gitOf(part);
        const words = part.trim().split(/\s+/);
        const isPatch = !git && words[0] === "patch";
        if (!isPatch && !(git && /^(apply|am|branch|checkout|restore|stash|switch|cherry-pick|merge|rebase)$/.test(git.verb))) continue;
        const here = git ? resolve(dir, part.match(/(?:^|\s)-C\s+(\S+)/)?.[1] ?? ".") : dir;

        if (git && /^(checkout|restore)$/.test(git.verb) && (git.verb === "restore" || git.args.includes("--"))) {
          const after = git.verb === "checkout" ? git.args.slice(git.args.indexOf("--") + 1) : git.args;
          if (after.filter((a) => !a.startsWith("-")).some((p) => protectedPath(absolute(resolve(here, unquote(p))))))
            return OWNERS_GUARD;
          continue;
        }
        if (git && git.verb === "branch") {
          // Making a name is harmless on its own - nothing is written - but the name it makes is remembered for the
          // rest of this call, so a cherry-pick of it in the same statement resolves to the entry it points at.
          const [name, start] = git.args.filter((a) => !a.startsWith("-"));
          const at = name && start ? resolved(here, unquote(start)) : null;
          if (name && at) aliases.set(name, at);
          continue;
        }
        if (git && /^(checkout|switch|cherry-pick|merge|rebase)$/.test(git.verb)) {
          // The SOURCE of the import: an operand that already names a commit, read against the stash entries. A
          // `switch -c x <ref>` operand that names no commit is its own new branch, so it lands in `resolved` as null.
          const entry = git.args
            .filter((a) => !a.startsWith("-"))
            .map((op) => aliases.get(unquote(op)) ?? resolved(here, unquote(op)))
            .find((sha) => sha !== null && stashEntries(here).has(sha));
          if (entry) {
            const names = stashWrites(here, entry);
            if (!names) return unreadableStash;
            if (names.some((p) => protectedPath(absolute(resolve(here, p))))) return OWNERS_GUARD;
          }
          continue;
        }
        if (git && git.verb === "stash") {
          const sub = git.args[0] ?? "";
          if (/^(pop|apply|branch)$/.test(sub)) {
            // The ref is the word AFTER the subcommand (`stash branch <name> [<stash>]` names its entry third); with
            // none, the entry is stash@{0}.
            const rest = git.args.slice(sub === "branch" ? 2 : 1);
            const ref = rest.find((a) => !a.startsWith("-")) ?? "stash@{0}";
            const names = stashWrites(here, ref);
            if (!names) return unreadableStash;
            if (names.some((p) => protectedPath(absolute(resolve(here, p))))) return OWNERS_GUARD;
            continue;
          }
          if (sub === "push") {
            // The destruction side of the same class: `stash push` reverts what it stashes to HEAD, so a path named
            // after `--` - or, in the path-free form, whatever the tree and the index say changed - is read.
            const dash = git.args.indexOf("--");
            const named =
              dash >= 0
                ? git.args.slice(dash + 1).filter((a) => !a.startsWith("-"))
                : [...listedPaths(here, "diff"), ...listedPaths(here, "diff", "--cached")];
            if (named.some((p) => protectedPath(absolute(resolve(here, p))))) return OWNERS_GUARD;
            continue;
          }
          continue;
        }

        const args = git ? git.args : words.slice(1);
        // The forms that write nothing: `git am`'s own control verbs, and the dry runs.
        if (git && git.verb === "am" && args.some((a) => /^--(continue|abort|skip|resolved)$/.test(a))) continue;
        if (args.some((a) => /^--(check|stat|summary|numstat)$/.test(a) || a === "--dry-run")) continue;
        if (args.some((a) => /^--(directory|prefix|unsafe-paths)(=|$)/.test(a)) || (isPatch && args.some((a) => /^-[A-Za-z]*d/.test(a))))
          return "this command names its own destination (--directory, --prefix, --unsafe-paths, patch -d), so the guard cannot see the paths it writes: cd into the directory and apply there";
        const source = patchSource(part, args);
        if (!source)
          return "this command takes its patch from stdin, so the guard cannot see what it writes. Name the patch file, or land the change as a commit";
        let text: string | null = null;
        try {
          text = readFileSync(resolve(here, source.replace(/^~(?=\/|$)/, homedir())), "utf8");
        } catch {
          text = null;
        }
        if (text === null)
          return "the patch file cannot be read, so the guard cannot see what it writes; name a file inside your worktrees";
        if (patchNames(text).some((p) => protectedPath(absolute(resolve(here, p))))) return OWNERS_GUARD;
      }
      return "";
    },
  },
  {
    rule: "process-kill",
    check: (tool, input) => {
      if (tool !== "bash") return "";
      for (const part of statements(input)) {
        const reason = killReason(part);
        if (reason) return reason;
      }
      return "";
    },
  },
  {
    rule: "frozen-paths",
    check: (tool, input) =>
      writeTargets(tool, input).some((p) => frozen(absolute(p)))
        ? "NEXT-GEN*.md, GATE-*.md and ai_docs/reports-status/ are frozen; record the result in the task, not in a file"
        : "",
  },
  {
    rule: "protected-paths",
    check: (tool, input) =>
      writeTargets(tool, input).some((p) => protectedPath(absolute(p)))
        ? "the git hooks, the seat guard, and the config, log, budget and park record it rests on, are the owner's to change"
        : "",
  },
  {
    // The launcher names the directories this seat may write, its own first (AGENTHUB_WORKTREES, set by the
    // supervisor from the room spec): the spec's directory for the seat and its worktrees under the room's wt/.
    // Writes may go there, to the seat's own dir and to the temp dir, nowhere else. Another seat's worktree and the
    // checkout every seat on this host shares are simply not in the list - the guard is never told which checkout
    // is main, and does not have to be, because it never has to judge one by name.
    rule: "worktree-scope",
    check: (tool, input) => {
      const roots = [...worktrees, process.env.PI_CODING_AGENT_DIR ?? "", tmpdir()].filter(Boolean).map((r) => resolve(r));
      const outside = writeTargets(tool, input).find((p) => {
        const abs = absolute(p);
        return !roots.some((r) => within(abs, r)) && !lane(abs);
      });
      const lanes = lanesRoot ? ` (a lane of yours is ${lanesRoot}/${seat}-<name>)` : "";
      return outside
        ? `${outside} is outside your worktrees ${worktrees.join(", ") || "(none named for this seat)"}; edit inside them and report the commit to the lead${lanes}`
        : "";
    },
  },
  {
    // The lead is the one seat the owner's 2026-10-18 ruling lets push, after the reviewer gate and a green
    // test run. Every other seat keeps the refusal below.
    rule: "push",
    check: (tool, input) =>
      pushAuthority() || tool !== "bash" || !statements(input).some((part) => GIT_PUSH_CALL.test(part))
        ? ""
        : "a seat does not push: only the lead pushes, after the reviewer gate and a green test run. Report the commit to the lead",
  },
  {
    // A ceiling a seat can raise is not a ceiling. READING the room's limits stays the seat's business - it should
    // know what it may spend - and the write flags are the owner's.
    rule: "budget-ceiling",
    check: (tool, input) => {
      if (tool !== "bash") return "";
      for (const part of statements(input)) {
        const call = part.match(ROOM_LIMITS_CALL);
        if (call && LIMITS_WRITE.test(call[1]))
          return "the room's ceilings are the owner's: a seat does not raise its own budget (--usd/--calls/--on/--off)";
      }
      return "";
    },
  },
  {
    // A SEAT DOES NOT SAY THE SAME THING TWICE. The predicate is the store's own: a pending row FROM THIS SEAT to
    // the same destination, sharing a tag, created inside the window. The refusal NAMES that row, so the seat reads
    // it and updates it instead of opening a second one nothing will read.
    rule: "queue-coalesce",
    check: (tool, input) => {
      if (tool !== "bash") return "";
      try {
        for (const part of statements(input)) {
          const create = createOf(part);
          const reason = create ? coalescingRow(create) : "";
          if (reason) return reason;
        }
      } catch {
        // A statement this guard cannot parse, or a store it cannot read, must not take the hook down: the rule
        // lets the create through and the rail still records it.
      }
      return "";
    },
  },
  {
    // A SEAT DOES NOT BUILD THE FRONTEND. Two vite builds in a seat's /tmp took 3.9 GB and free RAM fell to 4 of
    // 16; the deploy builds it and `npx tsc --noEmit` is the local check. A --version/--help probe is not a build.
    rule: "local-frontend-build",
    check: (tool, input) => {
      if (tool !== "bash") return "";
      for (const part of statements(input)) {
        const vite = part.match(VITE_CALL) ?? part.match(VITE_VIA_RUNNER);
        if (vite && !PROBE_ONLY.test(vite[1])) return FRONTEND_BUILD_REFUSAL;
        if (RUN_BUILD_CALL.test(part)) return FRONTEND_BUILD_REFUSAL;
      }
      return "";
    },
  },
];

// The class of a call's target, safe to analyse and to keep: a bash command's verb (`git commit`, `grep`), an MCP
// call's tool.action, a file tool's doc or code. The Go metrics extractor derives the same class for older lines.
const LEADING = /^((cd|export|echo)\s+("[^"]*"|'[^']*'|[^;&]*?)\s*(&&|;)\s*|[A-Z_][A-Z0-9_]*=\S*\s+)+/;
const VERBED = new Set(["git", "4genteam", "go", "cargo", "npm", "docker", "tmux"]);
function targetClass(tool: string, target: string): string {
  if (!target) return "";
  if (tool.startsWith("mcp__")) return target;
  if (tool === "bash") {
    const words = target.trim().replace(LEADING, "").split(/\s+/).filter(Boolean);
    if (!words.length) return "";
    const verb = basename(words[0]);
    if (!/^[A-Za-z0-9_.+-]+$/.test(verb)) return "other";
    return VERBED.has(verb) && /^[a-z][a-z-]*$/.test(words[1] ?? "") ? `${verb} ${words[1]}` : verb;
  }
  return /\.md$|CHANGELOG|NEXT-GEN|ai_docs\//.test(target) ? "doc" : "code";
}

function record(line: Record<string, unknown>): void {
  try {
    const target_class = targetClass(String(line.tool ?? ""), String(line.target ?? ""));
    const blocked = line.rule ? { blocked_rule: line.rule } : {};
    const session = process.env.AGENTHUB_SESSION_NAME ?? "";
    appendFileSync(logPath, JSON.stringify({ at: new Date().toISOString(), seat, session, ...line, target_class, ...blocked }) + "\n");
  } catch {
    // A log that cannot be written must not stop the seat's work.
  }
}

export default function hook(pi: ExtensionAPI): void {
  const started = new Map<string, { at: number; tool: string; target: string }>();

  pi.on("tool_call", async (event) => {
    const input = (event.input ?? {}) as Record<string, unknown>;
    const tool = event.toolName;
    const full = redact(describe(tool, input));
    const target = full.slice(0, MAX_TARGET);
    for (const { rule, check } of RULES) {
      const reason = check(tool, input);
      if (reason) {
        record({ tool, target: full.slice(0, MAX_BLOCKED), outcome: "blocked", rule, error: reason });
        return { block: true, reason: `blocked by seat guard (${rule}): ${reason}` };
      }
    }
    started.set(event.toolCallId, { at: Date.now(), tool, target });
  });

  pi.on("tool_result", async (event) => {
    const call = started.get(event.toolCallId);
    if (!call) return;
    started.delete(event.toolCallId);
    const text = (event.content ?? []).map((c: { type: string; text?: string }) => (c.type === "text" ? c.text : "")).join("");
    record({
      tool: call.tool,
      target: call.target,
      outcome: event.isError ? "error" : "ok",
      ms: Date.now() - call.at,
      bytes_out: Buffer.byteLength(text),
      ...(event.isError ? { error: redact(text).slice(0, MAX_TARGET) } : {}),
    });
  });
}

// ---- agy ----------------------------------------------------------------------------------------------------
// agy runs a hook command with a JSON payload on stdin and reads a JSON answer from stdout. `pre` runs before a
// tool step: it answers {"decision":"deny"} for a refused call, "allow" for the rest. `post` runs after: it logs
// the outcome and answers {}. A payload carries toolCall {name, args}, and an agy tool is not an omp tool, so the
// two it shares the rules' vocabulary with are mapped: run_command is bash and view_file is read.

type AgyCall = { name?: string; args?: Record<string, unknown> };

function fromAgy(call: AgyCall): { tool: string; input: Record<string, unknown> } {
  const args = call.args ?? {};
  const name = call.name ?? "";
  if (name === "run_command") return { tool: "bash", input: { command: args.CommandLine } };
  if (name === "view_file") return { tool: "read", input: { path: args.AbsolutePath } };
  const key = ["AbsolutePath", "TargetFile", "SearchPath", "DirectoryPath", "Query", "Pattern"].find((k) => args[k]);
  return { tool: name, input: { path: key ? args[key] : "" } };
}

// agy starts a NEW conversation at every seat start, and nothing else tells the client which one is the seat's:
// `watch`, `feed` and `ps` read the transcript of the id in <seat dir>/agy-conversation. The hook payload names
// the conversation it runs for, so the seat's own hook keeps that file current.
function recordConversation(id: string): void {
  const path = join(process.env.PI_CODING_AGENT_DIR ?? ".", "agy-conversation");
  try {
    if (id && readFileSync(path, "utf8").trim() !== id) writeFileSync(path, id);
  } catch {
    if (id) try { writeFileSync(path, id); } catch { /* the seat's work does not depend on it */ }
  }
}

async function agyHook(phase: string): Promise<void> {
  let raw = "";
  for await (const chunk of process.stdin) raw += chunk;
  const payload = JSON.parse(raw || "{}") as { toolCall?: AgyCall; error?: string; conversationId?: string };
  recordConversation(payload.conversationId ?? "");
  const { tool, input } = fromAgy(payload.toolCall ?? {});
  const full = redact(describe(tool, input));
  if (phase === "post") {
    record({ tool, target: full.slice(0, MAX_TARGET), outcome: payload.error ? "error" : "ok",
      ...(payload.error ? { error: redact(payload.error).slice(0, MAX_TARGET) } : {}) });
    console.log("{}");
    return;
  }
  for (const { rule, check } of RULES) {
    const reason = check(tool, input);
    if (reason) {
      record({ tool, target: full.slice(0, MAX_BLOCKED), outcome: "blocked", rule, error: reason });
      console.log(JSON.stringify({ decision: "deny", reason: `blocked by seat guard (${rule}): ${reason}` }));
      return;
    }
  }
  console.log(JSON.stringify({ decision: "allow" }));
}

const agyFlag = process.argv.indexOf("--agy-hook");
if (agyFlag >= 0) await agyHook(process.argv[agyFlag + 1] ?? "");
