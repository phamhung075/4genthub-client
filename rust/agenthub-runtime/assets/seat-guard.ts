// The seat's tool hook, written into <seat dir>/hooks/pre/ by the runtime at every seat start; omp loads it
// from there because the seat dir is its agent dir (PI_CODING_AGENT_DIR).
//
// It does two jobs and nothing else:
//   1. GUARD: block the calls the owner's standing rules forbid a seat - printing a secret from a `.env`
//      file, running Docker, pushing (a push to main deploys production, and the owner approves every one), and changing the room's own tmux server.
//   2. LOG: append ONE line per tool call to <seat dir>/tool-calls.jsonl, so "what did this seat do, and was it
//      refused" is one `jq` away instead of a transcript read.
//
// The same file is also the agy seat's guard: agy's global `hooks.json` runs it as `node seat-guard.ts --agy-hook
// pre|post`, with the hook payload on stdin (see agyHook below). omp never passes that argument, so it loads the
// default export as before. One file keeps the rules, the log line and the redaction in one place.
//
// A line is {at, seat, tool, target, outcome, ms, rule?, error?}. `outcome` is "ok", "error" or
// "blocked"; a blocked call carries the `rule` that refused it. An MCP call is the tool mcp__<server>_<tool>
// (a seat reaches it by writing to xd://mcp__...), and its `target` is mcp:<tool>.<action>, the form to grep for.
import { appendFileSync } from "node:fs";
import { join } from "node:path";
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";

const seat = process.env.AGENTHUB_SEAT ?? "";
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
// `echo tmux is nice` does not. The capture is what follows the command name.
const callOf = (name: string): RegExp =>
  new RegExp(
    `^\\s*(?:(?:env|sudo|command|exec|nohup)\\s+(?:-\\S+\\s+(?:[A-Za-z_]+\\s+)?|[A-Za-z_]+=\\S+\\s+)*|[A-Za-z_]+=\\S+\\s+)*${name}\\b(.*)$`,
    "s",
  );
const TMUX_CALL = callOf("tmux");
const DOCKER_CALL = callOf("docker(?:-compose)?");
const GIT_PUSH_CALL = callOf("git\\s+(?:-C\\s+\\S+\\s+)?push");

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
        ? "Docker is not run from a seat; production updates by a push the owner approves"
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
    rule: "push",
    check: (tool, input) =>
      tool === "bash" && statements(input).some((part) => GIT_PUSH_CALL.test(part))
        ? "a seat does not push: a push to main deploys production and the owner approves every one. Report the commit to the lead"
        : "",
  },
];

function record(line: Record<string, unknown>): void {
  try {
    appendFileSync(logPath, JSON.stringify({ at: new Date().toISOString(), seat, ...line }) + "\n");
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

async function agyHook(phase: string): Promise<void> {
  let raw = "";
  for await (const chunk of process.stdin) raw += chunk;
  const payload = JSON.parse(raw || "{}") as { toolCall?: AgyCall; error?: string };
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
