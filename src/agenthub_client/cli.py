"""``4genteam``: one entry point for everything the local client runs.

Team lifecycle (a rig defaults to ``4genthub-min``)::

    4genteam [up] [RIG]     OpenRig daemon, compaction supervisor + herdr watch view + OpenRig UI
    4genteam compact [RIG]  (re)start the compaction supervisor, detached
    4genteam stop [RIG]     stop it
    4genteam status [RIG]   its process and the last log lines
    4genteam log [RIG]      follow its log
    4genteam ui             open the OpenRig web UI

Tools, each the command line of one module (``4genteam <tool> --help``)::

    sync     seat_sync     pull and pin seats from the cloud
    seat     seat_client   status / sync / watch of a room's seats
    policy   seat_policy   per-seat policy: show, apply
    team     team_setup    create the team in the cloud: apply, import-project, publish-skills, drift-check
    bridge   bridge        this machine's status to the cloud: register, run, once, install-service
    watch    watch         herdr view: watch (grid + lead window), grid, feed, inputs
    compact-run            the supervisor loop in the foreground (what ``compact`` starts)
"""

import os
import subprocess
import sys

from . import bridge, compact, paths, seat_client, seat_policy, seat_sync, team_setup, watch

DEFAULT_RIG = "4genthub-min"
LIFECYCLE = ("up", "compact", "stop", "status", "log", "ui")
WATCH_VERBS = ("feed", "grid", "inputs", "input")


def supervisor_pattern(rig: str) -> str:
    return f"agenthub_client.compact --rig {rig}"


def log_path(rig: str):
    return paths.LOG_DIR / f"compact-supervisor-{rig}.log"


def start_supervisor(rig: str) -> None:
    stop_supervisor(rig, quiet=True)
    paths.LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(log_path(rig), "ab") as log:
        proc = subprocess.Popen(
            [sys.executable, "-m", "agenthub_client.compact", "--rig", rig],
            stdout=log, stderr=log, stdin=subprocess.DEVNULL, start_new_session=True,
        )
    print(f"compact supervisor: pid {proc.pid}, log {log_path(rig)}")


def stop_supervisor(rig: str, quiet: bool = False) -> None:
    stopped = subprocess.run(["pkill", "-f", supervisor_pattern(rig)]).returncode == 0
    if not quiet:
        print("compact supervisor: " + ("stopped" if stopped else "was not running"))


def supervisor_status(rig: str) -> None:
    found = subprocess.run(["pgrep", "-af", supervisor_pattern(rig)], capture_output=True, text=True).stdout
    lines = [line for line in found.splitlines() if "pgrep" not in line]
    print("\n".join(lines) if lines else "compact supervisor: not running")
    if log_path(rig).exists():
        print("\n".join(log_path(rig).read_text(errors="replace").splitlines()[-5:]))


def ensure_daemon() -> None:
    """Every rig command needs the OpenRig daemon, so `up` starts it before anything else."""
    if subprocess.run(["rig", "daemon", "status"], capture_output=True).returncode == 0:
        return
    if subprocess.run(["rig", "daemon", "start"]).returncode != 0:
        sys.exit("4genteam: the OpenRig daemon did not start (see: rig daemon logs)")


def open_ui() -> None:
    if subprocess.run(["rig", "ui", "open"]).returncode != 0:
        print("4genteam: could not open the OpenRig UI (see: rig daemon status)", file=sys.stderr)


def tool(module_main, argv: list[str]) -> int:
    return module_main(argv) or 0


def argv_tool(module_main, argv: list[str]) -> int:
    """The modules whose main() reads sys.argv."""
    sys.argv = ["4genteam", *argv]
    module_main()
    return 0


def lifecycle(command: str, rest: list[str]) -> int:
    rig = rest[0] if rest else DEFAULT_RIG
    if command == "ui":
        open_ui()
    elif command == "compact":
        start_supervisor(rig)
    elif command == "stop":
        stop_supervisor(rig)
    elif command == "status":
        supervisor_status(rig)
    elif command == "log":
        os.execvp("tail", ["tail", "-f", str(log_path(rig))])
    else:
        ensure_daemon()
        start_supervisor(rig)
        argv_tool(watch.main, ["watch", "--rig", rig])
        open_ui()
        if not os.environ.get("HERDR_ENV"):
            os.execvp("herdr", ["herdr"])
    return 0


def main(argv: list[str] | None = None) -> int:
    args = list(sys.argv[1:] if argv is None else argv)
    command, rest = (args[0], args[1:]) if args else ("up", [])
    if command in ("-h", "--help", "help"):
        print(__doc__)
        return 0
    if command in LIFECYCLE:
        return lifecycle(command, rest)
    if command == "sync":
        return tool(seat_sync.main, rest)
    if command == "seat":
        return tool(seat_client.main, rest)
    if command == "policy":
        return tool(seat_policy.main, rest)
    if command == "team":
        return tool(team_setup.main, rest)
    if command == "bridge":
        return tool(bridge.main, rest)
    if command == "watch":
        return argv_tool(watch.main, rest if rest and rest[0] in WATCH_VERBS + ("watch",) else ["watch", *rest])
    if command in WATCH_VERBS:
        return argv_tool(watch.main, args)
    if command == "compact-run":
        return argv_tool(compact.main, rest)
    return lifecycle("up", args)


if __name__ == "__main__":
    sys.exit(main())
