#!/usr/bin/env python3
"""Keep every seat of a rig under the per-session context limit without cutting a job short.

A seat may run past the limit, up to ``HARD_LIMIT`` where it is compacted at once: a working seat
has its turn interrupted first (a /compact sent to a working seat is read as text). At ``WARN_LIMIT``
the seat is told once that the forced compaction is close. When its context crosses ``COMPACT_LIMIT`` the supervisor tells the
seat once, in its terminal, that the limit is reached and how to compact itself. When the seat then
finishes (its session log is silent for ``--quiet`` seconds) and is still over the limit, the
supervisor sends ``/compact`` for it. A compaction counts only when a new compaction record appears
(omp) or the context drops (Claude Code); a send with no such witness is logged as a failure.
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

from . import watch

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
    subprocess.run(
        ["rig", "send", f"{rig}-{seat}@{rig}", text, "--wait-for-idle", "120", *(["--raw"] if raw else [])],
        capture_output=True,
        text=True,
    )


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
        "Reach a safe point and stop NOW so you are compacted cleanly. "
        f"At {watch.kilo(watch.HARD_LIMIT)} your turn is interrupted and /compact is forced, "
        "and whatever you had not written down is lost. Write down the job, what is done, what is next and the files you hold."
    )


def interrupt(rig: str, seat: str) -> None:
    """Escape in the seat's tmux pane ends the running turn, so the next step finds it idle."""
    subprocess.run(["tmux", "send-keys", "-t", f"{rig}-{seat}@{rig}", "Escape"], capture_output=True)


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
        due = silent >= quiet or used >= watch.HARD_LIMIT
        if used >= watch.HARD_LIMIT and now - s["sent"] > COOLDOWN and not idle(rig, seat):
            if now - s["interrupted"] > INTERRUPT_GAP:
                interrupt(rig, seat)
                s["interrupted"] = now
                log(f"{seat}: interrupted at {watch.kilo(used)} (hard limit)")
            continue
        if due and now - s["sent"] > COOLDOWN and idle(rig, seat):
            s.update(sent=now, before=used, count=compactions(path))
            say(rig, seat, "/compact", raw=True)
            log(f"{seat}: quiet {silent:.0f}s at {watch.kilo(used)}, sent /compact{' (hard limit)' if used >= watch.HARD_LIMIT else ''}")


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
