#!/usr/bin/env python3
"""Keep every seat of a rig under the per-session context limit without cutting a job short.

Three limits. At ``COMPACT_LIMIT`` the seat is told once, in its terminal, to find its own safe point and
stop; when it then finishes (its session log is silent for ``--quiet`` seconds) the supervisor sends
``/compact``. At ``WARN_LIMIT`` the seat is told it is compacted now: it no longer waits for quiet. An omp
seat gets an RPC compact from ``forcecompact`` (omp applies it at its next turn boundary, the safe point
that still exists in a long job); another runtime is sent ``/compact`` as soon as it is idle (a /compact
sent to a working seat is read as text). At ``HARD_LIMIT``, when that has not worked, the turn is cut: an
omp seat is aborted and then compacted, another runtime is interrupted. A compaction counts only when a new
compaction record appears (omp) or the context drops (Claude Code); a send with no such witness is logged
as a failure and retried.
Once a compaction is witnessed the seat is told to resume, so it carries on without the owner typing continue.
Run it from the host, not inside a seat pane: a loop in a seat dies with the seats it watches.
"""

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path

from . import paths, watch

TAIL_BYTES = 2_000_000
RESUME = (
    "Context was compacted. Continue the job you were on: re-read your role file and the board item "
    "you held, check the working tree for what is already done, then carry on without waiting for me."
)
INTERRUPT_GAP = 60  # seconds between two interrupts of the same seat
COOLDOWN = 900  # seconds after a /compact before the same seat is considered again


def last_context(log: Path) -> int | None:
    """Tokens of the newest reading in the log; the tail is enough because it is a last reading."""
    with open(log, "rb") as fh:
        fh.seek(max(log.stat().st_size - TAIL_BYTES, 0))
        lines = fh.read().decode(errors="replace").splitlines()
    return next((t for t in map(watch.context_tokens, reversed(lines)) if t), None)


def compactions(log: Path) -> int:
    with open(log, "rb") as fh:
        return fh.read().decode(errors="replace").count('"type":"compaction"')


def say(rig: str, seat: str, text: str, raw: bool = False) -> None:
    """``raw`` sends the exact text: without it ``rig send`` wraps it in a From/To envelope, which makes /compact plain text."""
    cmd = ["rig", "send", f"{rig}-{seat}@{rig}", text, "--wait-for-idle", "120", *(["--raw"] if raw else [])]
    if raw:
        subprocess.run(cmd, capture_output=True, text=True)
    else:
        # a notice to a working seat waits for it to go idle: that wait must not hold up the other seats
        subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def idle(rig: str, seat: str) -> bool:
    """True when OpenRig reports the seat idle. A /compact typed into a working seat is queued as a
    steering message and read as text, not run, so it is sent only to an idle prompt."""
    out = subprocess.run(["rig", "ps", "--nodes", "--rig", rig, "--json"], capture_output=True, text=True).stdout
    try:
        nodes = json.loads(out)
    except json.JSONDecodeError:
        return False
    return any(
        n.get("canonicalSessionName") == f"{rig}-{seat}@{rig}" and (n.get("agentActivity") or {}).get("state") == "idle"
        for n in nodes
    )


def log(text: str) -> None:
    print(f"{time.strftime('%Y-%m-%d %H:%M:%S')} {text}", flush=True)


def notice(rig: str, seat: str) -> str:
    return (
        f"Context limit: your session is past {watch.kilo(watch.COMPACT_LIMIT)} tokens. "
        "Find the next safe point yourself: finish or checkpoint the step you are on, note in your board item "
        "what is done and what comes next, then stop and send nothing. "
        "Do not type /compact, a /compact sent while you work is only queued as text. "
        "The supervisor compacts you once you are idle for 15 seconds, then tells you to resume and you carry on."
    )


def warning() -> str:
    return (
        f"VERY IMPORTANT: your session is past {watch.kilo(watch.WARN_LIMIT)} tokens. "
        "Reach a safe point and stop NOW. "
        "A compaction is being requested now and runs at your next turn boundary. "
        f"If you are still running at {watch.kilo(watch.HARD_LIMIT)} your turn is aborted and /compact is forced, "
        "and whatever you had not written down is lost. Write down the job, what is done, what is next and the files you hold."
    )


def interrupt(rig: str, seat: str) -> None:
    """Escape in the seat's tmux pane ends the running turn."""
    subprocess.run(["tmux", "send-keys", "-t", f"{rig}-{seat}@{rig}", "Escape"], capture_output=True)


def abort(rig: str, seat: str) -> None:
    """``/abort`` is the one pane text the omp runner turns into an abort of the running turn. It is sent
    without waiting for idle, because the seat is working by definition."""
    subprocess.Popen(["rig", "send", f"{rig}-{seat}@{rig}", "/abort", "--raw"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def rpc_compact(rig: str, seat: str) -> str:
    """Send an RPC compact straight to the seat's omp, which reads it whatever the seat is doing: the runner
    turns pane text into a prompt or a steer message, so a typed /compact is only text to a working seat."""
    try:
        out = subprocess.run([str(paths.FORCECOMPACT), f"{rig}-{seat}@{rig}"], capture_output=True, text=True)
    except OSError as e:
        return f"forcecompact not runnable ({e}): cargo build --release in agenthub_client/rust/forcecompact"
    return (out.stdout or out.stderr).strip()


def step(rig: str, state: dict, quiet: int) -> None:
    now = time.time()
    for seat in watch.rig_seats(rig):
        path, runtime = watch.seat_log(rig, seat)
        if path is None:
            continue
        used = last_context(path)
        s = state.setdefault(seat, {"told": False, "warned": False, "interrupted": 0.0, "sent": 0.0, "before": None, "count": 0})
        if s["before"] is not None:
            done = compactions(path) > s["count"] or (used or 0) < s["before"] * 0.6
            if done or now - s["sent"] > 180:
                log(f"{seat}: /compact {'WITNESSED' if done else 'NOT witnessed'} ({watch.kilo(s['before'])} -> {watch.kilo(used or 0)})")
                s["before"] = None
                if not done:
                    s["sent"] = 0.0
                if done:
                    say(rig, seat, RESUME)
                    log(f"{seat}: told to resume")
        if used is None or used < watch.COMPACT_LIMIT:
            s["told"] = s["warned"] = False
            continue
        if not s["told"]:
            say(rig, seat, notice(rig, seat))
            s["told"] = True
            log(f"{seat}: told, {watch.kilo(used)} of {watch.kilo(watch.COMPACT_LIMIT)} ({runtime})")
        if used >= watch.WARN_LIMIT and not s["warned"]:
            say(rig, seat, warning())
            s["warned"] = True
            log(f"{seat}: warned, {watch.kilo(used)} of {watch.kilo(watch.HARD_LIMIT)}")
        silent = now - path.stat().st_mtime
        urgent, hard = used >= watch.WARN_LIMIT, used >= watch.HARD_LIMIT
        if now - s["sent"] <= COOLDOWN:
            continue
        if runtime == "omp" and urgent:
            if hard:
                abort(rig, seat)
            s.update(sent=now, before=used, count=compactions(path))
            log(f"{seat}: {watch.kilo(used)} past the {'hard' if hard else 'warn'} limit{', aborted' if hard else ''}: {rpc_compact(rig, seat)}")
            continue
        if hard and not idle(rig, seat):
            if now - s["interrupted"] > INTERRUPT_GAP:
                interrupt(rig, seat)
                s["interrupted"] = now
                log(f"{seat}: interrupted at {watch.kilo(used)} (hard limit)")
            continue
        if (urgent or silent >= quiet) and idle(rig, seat):
            s.update(sent=now, before=used, count=compactions(path))
            say(rig, seat, "/compact", raw=True)
            log(f"{seat}: quiet {silent:.0f}s at {watch.kilo(used)}, sent /compact{' (past the warn limit)' if urgent else ''}")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--rig", default="4genthub-min")
    ap.add_argument("--quiet", type=int, default=int(os.environ.get("COMPACT_QUIET_SECONDS", 15)), help="seconds of log silence that mean the job is finished")
    ap.add_argument("--every", type=int, default=10)
    ap.add_argument("--once", action="store_true")
    a = ap.parse_args()
    state: dict = {}
    log(f"supervising {a.rig}: limit {watch.kilo(watch.COMPACT_LIMIT)}, quiet {a.quiet}s")
    while True:
        step(a.rig, state, a.quiet)
        if a.once:
            return
        time.sleep(a.every)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
