#!/usr/bin/env python3
"""Live tool-call feed for the omp seats of an OpenRig rig. The feed is read-only.

    4genteam watch feed [--rig R] [--seat S ...] [--back N] [--width W] [--lines L]
        one merged stream: a line per tool call and per result, coloured by tool kind
    4genteam watch grid [--rig R] [--cols 2] [--back N] [--width W] [--lines L]
        a new herdr workspace with one pane per seat, each running its own ``feed``
    4genteam watch inputs open|hide [--rig R] [--seat S ...]
        open or hide a small input pane under each seat pane of the grid; a line typed
        there is sent to that seat with ``rig send`` (the grid itself stays read-only)
    4genteam watch watch [--rig R] [--cols 2]
        the grid, plus a second workspace for the lead: its feed on top, a ``lead > input`` pane below

The feed reads each seat's newest session jsonl under ~/.openrig/state/omp and follows it.
A policy refusal is shown white-on-red. See ai_docs/operations/watching-openrig-seats.md.
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

from . import paths

SELF = f"{sys.executable} -m agenthub_client.watch"
ROOT = Path.home() / ".openrig" / "state" / "omp"
RESET = "\033[0m"
BOLD = "\033[1m"


def fg(code: int) -> str:
    """256-colour foreground. Every colour below is a light one: readable on a black background."""
    return f"\033[38;5;{code}m"


SEAT_COLORS = [203, 114, 221, 75, 213, 87, 215, 183, 120, 229]
MCP_COLOR = 213
DEFAULT_TOOL_COLOR = 255
RESULT_COLOR = 249
STAMP_COLOR = 245
# tool name -> colour: reads blue, writes yellow, shell green, MCP magenta, anything else white
TOOL_COLORS = {
    "read": 117,
    "grep": 117,
    "find": 117,
    "ls": 117,
    "search": 117,
    "write": 221,
    "edit": 221,
    "ast_edit": 221,
    "bash": 120,
    "eval": 120,
}


CLAUDE_PROJECTS = Path.home() / ".claude" / "projects"
# Per-session context limit: past it the seat finishes its job, then the compaction supervisor
# (4genteam compact-run) sends it /compact. The harness itself only compacts at ~850k.
# Each limit is read from .env (or the environment) so it can be changed without editing code.
COMPACT_LIMIT = int(os.environ.get("COMPACT_LIMIT_TOKENS", 150_000))  # soft: told to compact when the job ends
WARN_LIMIT = int(os.environ.get("COMPACT_WARN_TOKENS", 250_000))  # told once that the forced compaction is close
HARD_LIMIT = int(os.environ.get("COMPACT_HARD_TOKENS", 300_000))  # hard: compacted at once, a working seat's turn is interrupted
BAR_CELLS = 16


def claude_log(rig: str, seat: str) -> Path | None:
    """The session log of a Claude Code seat, found from the --session-id of its process."""
    out = subprocess.run(
        ["pgrep", "-af", f"claude .*--name {rig}-{seat}@{rig}"],
        capture_output=True,
        text=True,
    ).stdout
    ids = re.findall(r"--(?:session-id|resume) ([0-9a-f-]{36})", out)
    found = [f for i in ids for f in CLAUDE_PROJECTS.glob(f"*/{i}.jsonl")]
    return max(found, key=lambda f: f.stat().st_mtime, default=None)


def seat_log(rig: str, seat: str) -> tuple[Path | None, str]:
    """(newest session log, runtime) of a seat: omp state first, else the Claude Code log."""
    seat_dir = ROOT / f"{rig}-{seat}@{rig}"
    if seat_dir.is_dir():
        return newest_session(seat_dir), "omp"
    return claude_log(rig, seat), "claude"


def context_tokens(line: str) -> int | None:
    """Tokens in the context after this record's reply, or None when the record carries no usage."""
    try:
        record = json.loads(line)
        usage = (record.get("message") or {}).get("usage")
    except (json.JSONDecodeError, AttributeError):
        return None
    if record.get("type") == "compaction" and "tokensAfter" in record:
        return int(record["tokensAfter"])
    if not isinstance(usage, dict):
        return None
    if "totalTokens" in usage:
        return int(usage["totalTokens"])
    keys = ("input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens")
    return sum(int(usage.get(k) or 0) for k in keys) or None


def seat_model(log: Path | None) -> str:
    """The model that answered last, read from the log's tail (``message.model`` in both runtimes)."""
    if log is None:
        return ""
    with open(log, "rb") as fh:
        fh.seek(max(log.stat().st_size - 300_000, 0))
        found = re.findall(r'"model":"([^"]+)"', fh.read().decode(errors="replace"))
    return found[-1] if found else ""


def kilo(n: int) -> str:
    return f"{n / 1000:.0f}k" if n < 1_000_000 else f"{n / 1_000_000:.2f}M"


def token_bar(tokens: int | None, limit: int) -> str:
    """A bar of the context filled so far, its percent, and tokens used of the compaction point."""
    if tokens is None:
        return f"{fg(STAMP_COLOR)}ctx …{RESET}"
    pct = tokens / limit
    if pct >= 1:
        return (
            f"{BOLD}\033[1;38;5;231;48;5;160m LIMIT REACHED {RESET} {BOLD}{fg(203)}"
            f"{kilo(tokens)}/{kilo(limit)}{RESET} {fg(RESULT_COLOR)}compacts when the job ends{RESET}"
        )
    cells = min(BAR_CELLS, round(pct * BAR_CELLS))
    colour = 120 if pct < 0.6 else 221 if pct < 0.8 else 203
    bar = "█" * cells + "░" * (BAR_CELLS - cells)
    return (
        f"{fg(colour)}{bar}{RESET} {BOLD}{fg(colour)}{pct:.0%}{RESET} "
        f"{fg(RESULT_COLOR)}{kilo(tokens)}/{kilo(limit)} to compact{RESET}"
    )


def newest_session(seat_dir: Path):
    files = list((seat_dir / "sessions").glob("*.jsonl"))
    return max(files, key=lambda f: f.stat().st_mtime, default=None)


def indent_cut_json(text: str) -> str:
    """Indent JSON that was cut off mid-value, which no parser accepts: break at every
    structural comma, brace and bracket that is outside a string."""
    out, depth, in_str, esc = [], 0, False, False
    for ch in text:
        out.append(ch)
        if in_str:
            esc = ch == "\\" and not esc
            in_str = ch != '"' or esc
            continue
        if ch == '"':
            in_str = True
        elif ch in "{[":
            depth += 1
            out.append("\n" + "  " * depth)
        elif ch == ",":
            out.append("\n" + "  " * depth)
        elif ch in "}]":
            depth = max(depth - 1, 0)
            out.insert(-1, "\n" + "  " * depth)
    return "".join(out)


def pretty(text: str) -> str:
    """A result that starts with JSON is shown indented, whatever follows it (a wall-time
    line, a truncation notice) as it is; JSON cut off by the log is indented as far as it goes."""
    stripped = text.strip()
    if stripped[:1] not in ("{", "["):
        return text
    try:
        value, end = json.JSONDecoder().raw_decode(stripped)
    except json.JSONDecodeError:
        head, sep, tail = stripped.partition("\n\n")
        return indent_cut_json(head) + sep + tail
    return json.dumps(value, indent=2, ensure_ascii=False) + stripped[end:]


def block(head: str, style: str, text: str, width: int, lines: int) -> str:
    """``head`` then ``text`` as real lines: the first beside the head, the rest indented.

    Each line is cut at ``width`` characters and at most ``lines`` lines are shown; what is hidden
    is counted, never silently dropped.
    """
    rows = text.rstrip().splitlines() or [""]
    out = [f"{head}{style}{rows[0][:width]}{RESET}"]
    out += [f"    {style}{row[:width]}{RESET}" for row in rows[1:lines]]
    if len(rows) > lines:
        out.append(f"    {fg(STAMP_COLOR)}… +{len(rows) - lines} more lines{RESET}")
    return "\n".join(out)


def call_body(args) -> tuple[str, str]:
    """(names of the arguments not shown, the main argument as text)."""
    if not isinstance(args, dict):
        return "", str(args)
    for key in ("command", "path", "intent", "prompt", "code"):
        if key in args:
            others = [k for k in args if k != key]
            extra = f" (+{', '.join(others)})" if others else ""
            return extra, f"{key}={args[key]}"
    return "", json.dumps(args, indent=2, ensure_ascii=False)


def detail_lines(role: str | None, part: dict, width: int, lines: int):
    """Reasoning, what the agent says, and what it is told: the parts OpenRig's own view omits."""
    kind = part.get("type")
    if role == "assistant" and kind == "thinking":
        text = str(part.get("thinking", "")).strip()
        if text:
            yield block(
                f"{BOLD}{fg(183)}~ think {RESET}",
                f"{fg(183)}\033[3m",
                text,
                width,
                lines,
            )
    elif role == "assistant" and kind == "text":
        text = str(part.get("text", "")).strip()
        if len(text) > 1:
            yield block(f"{BOLD}{fg(231)}▸ say {RESET}", fg(255), text, width, lines)
    elif role == "user" and kind == "text":
        text = str(part.get("text", "")).strip()
        if text:
            yield block(f"{BOLD}{fg(87)}◂ in {RESET}", fg(123), text, width, lines)


def claude_to_omp(record: dict) -> list[dict]:
    """A Claude Code log record as omp records: tool_use becomes toolCall, each tool_result its own toolResult."""
    msg = record.get("message") or {}
    content = msg.get("content")
    if not isinstance(content, list):
        return [record]
    if msg.get("role") == "assistant":
        parts = [
            {"type": "toolCall", "name": p.get("name"), "arguments": p.get("input")}
            if p.get("type") == "tool_use"
            else p
            for p in content
        ]
        return [{"message": {"role": "assistant", "content": parts}}]
    out = []
    for p in content:
        if p.get("type") != "tool_result":
            out.append({"message": {"role": msg.get("role"), "content": [p]}})
            continue
        body = p.get("content")
        if isinstance(body, list):
            body = " ".join(b.get("text", "") for b in body if isinstance(b, dict))
        out.append(
            {
                "message": {
                    "role": "toolResult",
                    "isError": bool(p.get("is_error")),
                    "content": [{"text": str(body)}],
                }
            }
        )
    return out


def events(line: str, width: int, detail: bool = False, lines: int = 25):
    try:
        record = json.loads(line)
    except json.JSONDecodeError:
        return
    if record.get("type") == "compaction":
        before, after = record.get("tokensBefore"), record.get("tokensAfter")
        if before and after:
            yield (
                f"\033[1;38;5;16;48;5;120m COMPACTED {RESET} {BOLD}{fg(120)}"
                f"{kilo(int(before))} -> {kilo(int(after))}{RESET}"
            )
        return
    for omp_record in claude_to_omp(record):
        yield from omp_events(omp_record, width, detail, lines)


def omp_events(record: dict, width: int, detail: bool, lines: int):
    msg = record.get("message") or {}
    content = msg.get("content")
    if msg.get("role") == "toolResult":
        text = " ".join(p.get("text", "") for p in content or [] if isinstance(p, dict))
        body = pretty(text)
        if text.startswith("Tool ") and "is blocked by tool policy" in text[:120]:
            head = f"\033[1;38;5;231;48;5;160m ✗ BLOCKED {RESET} "
            yield block(head, fg(210), body, width, lines)
        elif msg.get("isError"):
            yield block(f"{BOLD}{fg(203)}✗ {RESET}", fg(203), body, width, lines)
        else:
            yield block(
                f"{fg(RESULT_COLOR)}← {RESET}", fg(RESULT_COLOR), body, width, lines
            )
    elif isinstance(content, list):
        for part in content:
            if detail and isinstance(part, dict):
                yield from detail_lines(msg.get("role"), part, width, lines)
            if isinstance(part, dict) and part.get("type") == "toolCall":
                name = part.get("name") or "?"
                code = (
                    MCP_COLOR
                    if name.startswith("mcp__")
                    else TOOL_COLORS.get(name, DEFAULT_TOOL_COLOR)
                )
                extra, body = call_body(part.get("arguments"))
                head = f"{BOLD}{fg(code)}→ {name}{extra}{RESET} "
                yield block(head, fg(255), body, width, lines)


def rig_seats(rig: str) -> list[str]:
    """Seats with a live tmux session; the omp state dirs outlive removed seats."""
    out = subprocess.run(
        ["tmux", "list-sessions", "-F", "#{session_name}"],
        capture_output=True,
        text=True,
    ).stdout.split()
    return sorted(
        name.removeprefix(f"{rig}-").removesuffix(f"@{rig}")
        for name in out
        if name.startswith(f"{rig}-") and name.endswith(f"@{rig}")
    )


def seat_command(rig: str, seat: str, feed_args: str) -> str:
    return f"{SELF} feed --rig {rig} --seat {seat} {feed_args}"


def herdr(*args: str) -> dict:
    """Run a herdr API command; send-text and send-keys print nothing."""
    out = subprocess.run(
        ["herdr", *args], capture_output=True, text=True, check=True
    ).stdout
    return json.loads(out)["result"] if out.strip() else {}


def pane_id(result: dict) -> str:
    return (result.get("pane") or result["root_pane"])["pane_id"]


def close_workspaces(label: str) -> None:
    """Close every workspace with this label, so a relaunch leaves one wall, not a stack."""
    for w in herdr("workspace", "list")["workspaces"]:
        if w["label"] == label:
            herdr("workspace", "close", w["workspace_id"])


def grid(a: argparse.Namespace) -> None:
    """One pane per seat. Columns are split first, then each column into equal rows."""
    seats = rig_seats(a.rig)
    cwd = str(paths.REPO_ROOT)
    close_workspaces(f"{a.rig} grid")
    root = herdr("workspace", "create", "--cwd", cwd, "--label", f"{a.rig} grid")[
        "root_pane"
    ]["pane_id"]

    def split(pane: str, direction: str, ratio: float) -> str:
        return pane_id(
            herdr(
                "pane",
                "split",
                "--pane",
                pane,
                "--direction",
                direction,
                "--ratio",
                f"{ratio:.3f}",
                "--cwd",
                cwd,
            )
        )

    heads = [root]
    for left in range(a.cols - 1, 0, -1):
        heads.append(split(heads[-1], "right", 1 / (left + 1)))
    per_col = -(-len(seats) // a.cols)
    panes = []
    for head in heads:
        cur = head
        panes.append(cur)
        for left in range(per_col - 1, 0, -1):
            cur = split(cur, "down", 1 / (left + 1))
            panes.append(cur)
    for pane, seat in zip(panes, seats):
        cmd = seat_command(
            a.rig,
            seat,
            f"--back {a.back} --width {a.width} --lines {a.lines}"
            + (" --detail" if a.detail else ""),
        )
        herdr("pane", "rename", pane, seat)
        herdr("pane", "send-text", pane, cmd)
        herdr("pane", "send-keys", pane, "Enter")
    herdr("workspace", "focus", root.split(":")[0])
    print(f"grid: {len(seats)} seats in {a.cols} columns")


INPUT_SUFFIX = " > input"
INPUT_ROWS_RATIO = 0.65  # share of the seat pane that stays with the feed; the input needs rows to click on


def grid_workspace(rig: str) -> str:
    """The id of the newest ``<rig> grid`` workspace."""
    found = [
        w["workspace_id"]
        for w in herdr("workspace", "list")["workspaces"]
        if w["label"] == f"{rig} grid"
    ]
    if not found:
        raise SystemExit(f"no '{rig} grid' workspace: run grid first")
    return found[-1]


def grid_panes(rig: str) -> list[dict]:
    ws = grid_workspace(rig)
    return [p for p in herdr("pane", "list")["panes"] if p["workspace_id"] == ws]


def inputs(a: argparse.Namespace) -> None:
    """Open or hide the input pane under every seat pane of the grid."""
    panes = grid_panes(a.rig)
    seats = set(a.seat or rig_seats(a.rig))
    if a.action == "hide":
        closing = [p for p in panes if (p.get("label") or "").endswith(INPUT_SUFFIX)]
        for p in closing:
            herdr("pane", "close", p["pane_id"])
        print(f"inputs: hid {len(closing)}")
        return
    open_for = {p["label"] for p in panes if (p.get("label") or "").endswith(INPUT_SUFFIX)}
    opened = 0
    for p in panes:
        seat = p.get("label")
        if seat not in seats or seat + INPUT_SUFFIX in open_for:
            continue
        new = pane_id(
            herdr(
                "pane", "split", "--pane", p["pane_id"], "--direction", "down",
                "--ratio", f"{INPUT_ROWS_RATIO:.3f}", "--cwd", str(paths.REPO_ROOT),
            )
        )
        herdr("pane", "rename", new, seat + INPUT_SUFFIX)
        herdr("pane", "send-text", new, f"clear; exec {SELF} input --rig {a.rig} --seat {seat}")
        herdr("pane", "send-keys", new, "Enter")
        opened += 1
    print(f"inputs: opened {opened}")


def input_loop(a: argparse.Namespace) -> None:
    """Read lines and send each to the seat. Empty line skips; Ctrl-D or /hide leaves."""
    target = f"{a.rig}-{a.seat}@{a.rig}"
    while True:
        try:
            line = input(f"{a.seat} > ").strip()
        except EOFError:
            return
        if line == "/hide":
            return
        if not line:
            continue
        done = subprocess.run(["rig", "send", target, line], capture_output=True, text=True)
        print("sent" if done.returncode == 0 else f"failed: {done.stderr.strip()[:200]}")


LEAD_ROWS_RATIO = 0.8  # share of the lead workspace that stays with its feed


def lead_window(rig: str) -> None:
    """A workspace for the lead alone: its detailed feed on top, an input pane under it."""
    close_workspaces(f"{rig} lead")
    root = herdr("workspace", "create", "--cwd", str(paths.REPO_ROOT), "--label", f"{rig} lead", "--no-focus")[
        "root_pane"
    ]["pane_id"]
    below = pane_id(
        herdr(
            "pane", "split", "--pane", root, "--direction", "down",
            "--ratio", f"{LEAD_ROWS_RATIO:.3f}", "--cwd", str(paths.REPO_ROOT),
        )
    )
    herdr("pane", "rename", below, f"lead{INPUT_SUFFIX}")
    for pane, cmd in (
        (root, f"feed --rig {rig} --seat lead --back 40 --width 200 --lines 25 --detail"),
        (below, f"input --rig {rig} --seat lead"),
    ):
        herdr("pane", "send-text", pane, f"clear; exec {SELF} {cmd}")
        herdr("pane", "send-keys", pane, "Enter")


def watch(a: argparse.Namespace) -> None:
    """Everything needed to watch a rig work: the seat grid and the lead window."""
    grid(a)
    lead_window(a.rig)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = ap.add_subparsers(dest="command", required=True)
    for name, fn in (("feed", feed), ("grid", grid), ("watch", watch)):
        p = sub.add_parser(name)
        p.add_argument("--rig", default="4genthub-min")
        p.add_argument("--back", type=int, default=3 if name == "feed" else 40)
        p.add_argument("--width", type=int, default=170 if name == "feed" else 200)
        p.add_argument("--lines", type=int, default=60)
        p.add_argument(
            "--detail",
            action="store_true",
            default=name != "feed",
            help="also show the agent's reasoning, what it says and what it is told (on for grid)",
        )
        if name == "feed":
            p.add_argument("--seat", nargs="*")
        else:
            p.add_argument("--cols", type=int, default=2)
        p.set_defaults(func=fn)
    p = sub.add_parser("inputs")
    p.add_argument("action", choices=("open", "hide"))
    p.add_argument("--rig", default="4genthub-min")
    p.add_argument("--seat", nargs="*", help="open: only these seats (default all)")
    p.set_defaults(func=inputs)
    p = sub.add_parser("input")
    p.add_argument("--rig", default="4genthub-min")
    p.add_argument("--seat", required=True)
    p.set_defaults(func=input_loop)
    a = ap.parse_args()
    a.func(a)


class Header:
    """The seat name and its token bar as a line of the feed, repeated when the reading moves.

    A pinned row needs a terminal scroll region, and lines scrolled inside a region never reach the
    scrollback, so the pane could not be scrolled. A plain line keeps the scrollback.
    """

    REPEAT = 20  # seconds between bar lines, printed only when the reading moved

    def __init__(self, name: str, colour: int, limit: int, model: str = ""):
        self.name, self.colour, self.limit, self.model = name, colour, limit, model
        self.tokens: int | None = None
        self.last = ("", 0.0)

    def tag(self) -> str:
        """A short percent that prefixes every event line, so the meter is never scrolled out of view."""
        if self.tokens is None:
            return ""
        pct = self.tokens / self.limit
        colour = 120 if pct < 0.6 else 221 if pct < 0.8 else 203
        return f"{BOLD}{fg(colour)}{pct:>4.0%}{RESET} {fg(RESULT_COLOR)}{kilo(self.tokens):>5}{RESET} "

    def draw(self) -> None:
        who = f"{self.name} | {self.model}" if self.model else self.name
        title = f"{BOLD}{fg(self.colour)}== {who} =={RESET}  {token_bar(self.tokens, self.limit)}"
        text, at = self.last
        waited = time.time() - at
        if title != text and waited >= self.REPEAT:
            print(title, flush=True)
            self.last = (title, time.time())


def feed(a: argparse.Namespace) -> None:
    seats = rig_seats(a.rig)
    if a.seat:
        seats = [s for s in seats if s in a.seat]
    color = {s: SEAT_COLORS[i % len(SEAT_COLORS)] for i, s in enumerate(seats)}
    pos: dict[str, tuple[Path, int]] = {}
    # One seat in view (a grid pane): its name and token bar are the pinned top row, not a column.
    header = (
        Header(seats[0], color[seats[0]], COMPACT_LIMIT, seat_model(seat_log(a.rig, seats[0])[0]))
        if len(seats) == 1
        else None
    )

    def log_of(seat):
        return seat_log(a.rig, seat)[0]

    def show(seat, line):
        used = context_tokens(line)
        if header and used:
            header.tokens = used
        stamp = time.strftime("%H:%M:%S")
        name = header.tag() if header else f"{BOLD}{fg(color[seat])}{seat:<12}{RESET} "
        for text in events(line, a.width, a.detail, a.lines):
            print(f"{fg(STAMP_COLOR)}{stamp}{RESET} {name}{text}", flush=True)

    if not header:
        print(f"-- following {len(seats)} seats; Ctrl-C to stop", flush=True)
    for seat in seats:
        f = log_of(seat)
        if f is None:
            continue
        lines = f.read_text().splitlines()
        shown = [
            ln for ln in lines if any(True for _ in events(ln, 1, a.detail, a.lines))
        ][-a.back * 2 :]
        if header:
            header.tokens = next(
                (t for t in map(context_tokens, reversed(lines)) if t), None
            )
        for ln in shown:
            show(seat, ln)
        if header:
            header.draw()
        pos[seat] = (f, f.stat().st_size)
    while True:
        for seat in seats:
            f = log_of(seat)
            if f is None:
                continue
            old, off = pos.get(seat, (f, 0))
            if f != old:
                off = 0
            size = f.stat().st_size
            if size > off:
                with open(f, "rb") as fh:
                    fh.seek(off)
                    chunk = fh.read(size - off).decode(errors="replace")
                for ln in chunk.splitlines():
                    show(seat, ln)
            pos[seat] = (f, size)
        if header:
            header.draw()
        time.sleep(1)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        os._exit(0)
