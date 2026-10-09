#!/usr/bin/env python3
"""Keep the local OpenRig seats in step with the 4genthub cloud.

The cloud owns what a seat is (its guide, policy, tools, MCP servers) and resolves it into a
snapshot named by a hash. This client compares the hash each local seat is pinned to with the
cloud's current one, adopts the newer snapshot, and restarts the affected seat so it loads it.
The adopting is ``4genteam sync rig ROOM --update``, called as a command, so the pinning
rules (never fall back to another snapshot; a missing pin is an error) stay in one place.

Usage::

    4genteam seat status ROOM                      per seat: pinned, cloud, in sync or behind
    4genteam seat sync ROOM [--relaunch quiet]     adopt newer snapshots; optionally restart
    4genteam seat watch ROOM [--interval 60] ...   sync again and again until stopped

``--relaunch none`` (the default) only adopts: a running seat keeps what it loaded and picks the
change up at its next launch. ``--relaunch quiet`` also restarts each seat that changed, but only
after it has been idle for 30 seconds, so a seat is never stopped in the middle of a task; a seat
that does not go quiet within 25 minutes is reported and left running. ``--seat`` limits the work
to named seats. The room name is the rig name unless ``--rig`` says otherwise.

Environment: AGENTHUB_URL and AGENTHUB_TOKEN, as for ``4genteam sync``. The token is only
passed on in the environment; it is never printed or written.

Exit codes: 0 ok (``status``: everything in sync), 2 usage or environment error, 3 a sync,
relaunch or cloud step failed, 4 ``status`` found a seat behind or not pulled.
"""

import argparse
import importlib
import json
import subprocess
import sys
import time
from pathlib import Path

EXIT_OK = 0
EXIT_BEHIND = 4
EXIT_USAGE = 2
EXIT_FAILED = 3

QUIET_SECONDS = 30
QUIET_WAIT_SECONDS = 1500
POLL_SECONDS = 15


class ClientError(Exception):
    def __init__(self, message: str, code: int = EXIT_FAILED):
        super().__init__(message)
        self.code = code


def load_sync():
    return importlib.import_module("agenthub_client.seat_sync")


def cloud_hashes(sync, room: str) -> dict[str, str]:
    """seat -> hash of the snapshot the cloud would serve now."""
    rigspec = sync.fetch_rigspec(
        sync.require_env("AGENTHUB_URL"), sync.require_env("AGENTHUB_TOKEN"), room
    )
    return {entry["seat"]: entry["hash"] for entry in rigspec["seats"]}


def pinned_hashes(sync, out: Path, room: str, seats) -> dict[str, str | None]:
    pins: dict[str, str | None] = {}
    for seat in seats:
        lock = sync.read_lock(out / room / seat / "pinned.json")
        pins[seat] = lock["hash"] if lock else None
    return pins


def seats_behind(cloud: dict[str, str], pinned: dict[str, str | None]) -> list[str]:
    return [seat for seat, value in cloud.items() if pinned.get(seat) != value]


def run_json(*args: str):
    done = subprocess.run(args, capture_output=True, text=True, timeout=120)
    if done.returncode != 0:
        raise ClientError(
            f"{' '.join(args)} failed: {(done.stderr or done.stdout).strip()[:200]}"
        )
    return json.loads(done.stdout)


def rig_id(rig: str) -> str:
    rigs = run_json("rig", "ps", "--json")
    for entry in rigs if isinstance(rigs, list) else rigs.get("rigs", []):
        if entry.get("name") == rig:
            return entry["rigId"]
    raise ClientError(f"no running rig named {rig!r}")


def seat_activity(rig: str, seat: str) -> str | None:
    for node in run_json("rig", "ps", "--nodes", "--rig", rig, "--json"):
        if node["logicalId"] == f"{rig}.{seat}":
            return node["agentActivity"].get("state")
    return None


def seconds_since_last_write(rig: str, seat: str) -> float:
    sessions = (
        Path.home() / ".openrig" / "state" / "omp" / f"{rig}-{seat}@{rig}" / "sessions"
    )
    files = list(sessions.glob("*.jsonl"))
    if not files:
        return float("inf")
    return time.time() - max(file.stat().st_mtime for file in files)


def wait_until_quiet(
    rig: str,
    seat: str,
    activity=seat_activity,
    since=seconds_since_last_write,
    sleep=time.sleep,
    clock=time.monotonic,
) -> bool:
    """True once the seat is not running and its session has been still for QUIET_SECONDS, twice in a row."""
    deadline = clock() + QUIET_WAIT_SECONDS
    while clock() < deadline:
        if activity(rig, seat) != "running" and since(rig, seat) >= QUIET_SECONDS:
            sleep(POLL_SECONDS)
            if activity(rig, seat) != "running" and since(rig, seat) >= QUIET_SECONDS:
                return True
        sleep(POLL_SECONDS)
    return False


def relaunch(rig: str, seat: str) -> None:
    subprocess.run(
        ["tmux", "kill-session", "-t", f"{rig}-{seat}@{rig}"], capture_output=True
    )
    time.sleep(4)
    run = subprocess.run(
        ["rig", "launch", rig_id(rig), f"{rig}.{seat}"],
        capture_output=True,
        text=True,
        timeout=300,
    )
    if run.returncode != 0:
        raise ClientError(
            f"rig launch {rig}.{seat} failed: {(run.stderr or run.stdout).strip()[:200]}"
        )


def log(message: str) -> None:
    print(f"{time.strftime('%H:%M:%S')} {message}", flush=True)


def selected(seats, wanted):
    return [seat for seat in seats if not wanted or seat in wanted]


def cmd_status(args) -> int:
    sync = load_sync()
    out = Path(args.out).expanduser().resolve()
    cloud = cloud_hashes(sync, args.room)
    cloud = {
        seat: value
        for seat, value in cloud.items()
        if seat in selected(cloud, args.seat)
    }
    pins = pinned_hashes(sync, out, args.room, cloud)
    behind = seats_behind(cloud, pins)
    for seat, value in cloud.items():
        pinned = pins[seat]
        state = (
            "in sync"
            if seat not in behind
            else ("not pulled" if pinned is None else "BEHIND")
        )
        print(
            f"{seat:<14} pinned {str(pinned)[:12]:<12}  cloud {value[:12]:<12}  {state}"
        )
    return EXIT_BEHIND if behind else EXIT_OK


def sync_once(args, sync) -> int:
    out = Path(args.out).expanduser().resolve()
    rig = args.rig or args.room
    cloud = cloud_hashes(sync, args.room)
    cloud = {
        seat: value
        for seat, value in cloud.items()
        if seat in selected(cloud, args.seat)
    }
    behind = seats_behind(cloud, pinned_hashes(sync, out, args.room, cloud))
    if not behind:
        log(f"{args.room}: all {len(cloud)} seats in sync")
        return EXIT_OK
    log(f"{args.room}: behind: {', '.join(behind)}")
    done = subprocess.run(
        [
            sys.executable,
            "-m",
            "agenthub_client.seat_sync",
            "rig",
            args.room,
            "--out",
            str(out),
            "--update",
        ],
        capture_output=True,
        text=True,
    )
    if done.returncode != 0:
        raise ClientError(
            f"4genteam sync rig failed: {(done.stderr or done.stdout).strip()[:300]}"
        )
    still = seats_behind(cloud, pinned_hashes(sync, out, args.room, cloud))
    if still:
        raise ClientError(
            f"adopted nothing for: {', '.join(still)} (pin did not move to the cloud hash)"
        )
    log(f"adopted the newer snapshot for: {', '.join(behind)}")
    if args.relaunch == "none":
        log(
            "running seats keep what they loaded; the change applies at each seat's next launch"
        )
        return EXIT_OK
    failed = 0
    for seat in behind:
        if not wait_until_quiet(rig, seat):
            log(
                f"{seat}: not quiet within {QUIET_WAIT_SECONDS // 60} minutes, left running with the old snapshot"
            )
            failed += 1
            continue
        relaunch(rig, seat)
        log(f"{seat}: relaunched on the new snapshot")
    return EXIT_FAILED if failed else EXIT_OK


def cmd_sync(args) -> int:
    return sync_once(args, load_sync())


def cmd_watch(args) -> int:
    sync = load_sync()
    log(f"watching {args.room} every {args.interval}s; Ctrl-C to stop")
    while True:
        try:
            sync_once(args, sync)
        except (
            Exception
        ) as err:  # keep watching: the next pass retries from a clean read
            log(f"FAILED: {err}")
        time.sleep(args.interval)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)
    for name, func in (
        ("status", cmd_status),
        ("sync", cmd_sync),
        ("watch", cmd_watch),
    ):
        p = sub.add_parser(name)
        p.add_argument("room")
        p.add_argument(
            "--out", default=str(Path.home() / ".openrig" / "agenthub-seats")
        )
        p.add_argument(
            "--seat", action="append", help="limit to this seat (repeatable)"
        )
        if name != "status":
            p.add_argument("--rig", help="rig name when it differs from the room")
            p.add_argument("--relaunch", choices=("none", "quiet"), default="none")
        if name == "watch":
            p.add_argument("--interval", type=int, default=60)
        p.set_defaults(func=func)
    return parser


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except ClientError as err:
        print(f"error: {err}", file=sys.stderr)
        return err.code
    except Exception as err:  # agenthub_client.seat_sync.SyncError carries its own exit code
        code = getattr(err, "code", None)
        if not isinstance(code, int):
            raise
        print(f"error: {err}", file=sys.stderr)
        return EXIT_USAGE if code == EXIT_USAGE else EXIT_FAILED
    except KeyboardInterrupt:
        return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
