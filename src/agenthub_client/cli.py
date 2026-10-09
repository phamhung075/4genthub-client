"""``4genteam``: one entry point for everything the local client runs.

Team lifecycle (a rig defaults to ``4genthub-min``)::

    4genteam [up] [RIG]     OpenRig daemon, compaction supervisor + herdr watch view + OpenRig UI
    4genteam compact [RIG]  (re)start the compaction supervisor, detached
    4genteam stop [RIG]     stop it
    4genteam status [RIG]   its process and the last log lines
    4genteam log [RIG]      follow its log
    4genteam ui             open the OpenRig web UI

Tools, each the command line of one module (``4genteam <tool> --help``). EVERY verb answers
``--help`` with its own usage, the lifecycle verbs above included::

    sync     seat_sync     pull and pin seats from the cloud
    seat     seat_client   status / sync / watch of a room's seats
    policy   seat_policy   per-seat policy: show, apply
    team     team_setup    create the team in the cloud: apply, import-project, publish-skills, drift-check
    bridge   bridge        this machine's status to the cloud: register, run, once, install-service
    watch    watch         herdr view: watch (grid + lead window), grid, feed, inputs
    feedback seat_feedback.sh  what went wrong, to the friction channel - the door for a runtime with no MCP
    compact-run            the supervisor loop in the foreground (what ``compact`` starts)
"""

import os
import subprocess
import sys
from pathlib import Path

from . import bridge, compact, paths, seat_client, seat_policy, seat_sync, team_setup, watch

DEFAULT_RIG = "4genthub-min"
LIFECYCLE = ("up", "compact", "stop", "status", "log", "ui")
WATCH_VERBS = ("feed", "grid", "inputs", "input")

EXIT_OK = 0
# A start that did not survive is its own exit condition: the verb is the documented way to
# restart the supervisor, so returning 0 there would say it restarted when it did not.
EXIT_START_FAILED = 3

# One line per verb, printed when the verb is asked for help. Every entry starts with the verb's own
# invocation, which is what a reader is looking for and what the test asserts.
LIFECYCLE_USAGE = {
    "up": f"4genteam up [RIG]        OpenRig daemon, compaction supervisor, herdr watch view and the OpenRig UI (default RIG: {DEFAULT_RIG})",
    "compact": "4genteam compact [RIG]   (re)start the compaction supervisor, detached",
    "stop": "4genteam stop [RIG]      stop the compaction supervisor",
    "status": "4genteam status [RIG]    the supervisor's process and the last log lines",
    "log": "4genteam log [RIG]       follow the supervisor's log",
    "ui": "4genteam ui             open the OpenRig web UI",
    "feedback": "4genteam feedback [ARGS]  the friction channel: run the packaged seat-feedback client",
}


def supervisor_pattern(rig: str) -> str:
    return f"agenthub_client.compact --rig {rig}"


# How long a started supervisor must survive before the start is called a success. Long enough for
# an argument error or an import failure to end the child, short enough that the verb stays snappy.
SUPERVISOR_START_GRACE = 1.5


def log_path(rig: str):
    return paths.LOG_DIR / f"compact-supervisor-{rig}.log"


def start_supervisor(rig: str) -> bool:
    """Start the supervisor detached and report whether it SURVIVED the start.

    A child that exits immediately is a FAILED start, and `4genteam compact` is the documented way
    to restart the supervisor - so reporting a pid for a child that is already dead tells the user
    it restarted when it did not. That is exactly what happened with `--help` as the rig name
    before the per-verb help existed: the child died on `error: argument --rig: expected one
    argument` while this function printed its pid and the verb returned 0. The status is therefore
    read from the child rather than assumed from a successful Popen.
    """
    stop_supervisor(rig, quiet=True)
    paths.LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(log_path(rig), "ab") as log:
        proc = subprocess.Popen(
            [sys.executable, "-m", "agenthub_client.compact", "--rig", rig],
            stdout=log, stderr=log, stdin=subprocess.DEVNULL, start_new_session=True,
        )
    try:
        proc.wait(timeout=SUPERVISOR_START_GRACE)
    except subprocess.TimeoutExpired:
        print(f"compact supervisor: pid {proc.pid}, log {log_path(rig)}")
        return True
    print(
        f"4genteam: the compaction supervisor exited immediately (status {proc.returncode}); "
        f"nothing is supervising this rig. Its output is in {log_path(rig)}",
        file=sys.stderr,
    )
    return False


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


FEEDBACK_SCRIPT = Path(__file__).with_name("seat_feedback.sh")


def feedback(rest: list[str]) -> int:
    """The door for a seat whose runtime has no MCP: run the packaged shell client.

    The script owns the contract - it validates the layer before dialing, takes the identity from
    the client, and posts to the same route the MCP tool uses - so this verb passes its arguments
    through and reports only a missing file. It must never fall through to `up`: a seat asking for
    the friction channel must not get a started stack instead.
    """
    if not FEEDBACK_SCRIPT.exists():
        sys.exit(f"4genteam: the feedback script is missing from this install ({FEEDBACK_SCRIPT})")
    return subprocess.run(["sh", str(FEEDBACK_SCRIPT), *rest]).returncode


def lifecycle(command: str, rest: list[str]) -> int:
    # `--help` never reaches here: main() answers it per verb. It must not, because this function
    # reads rest[0] as the RIG, so a help flag in that position was started as one - the fault this
    # comment replaces.
    rig = rest[0] if rest else DEFAULT_RIG
    if command == "ui":
        open_ui()
    elif command == "compact":
        return EXIT_OK if start_supervisor(rig) else EXIT_START_FAILED
    elif command == "stop":
        stop_supervisor(rig)
    elif command == "status":
        supervisor_status(rig)
    elif command == "log":
        os.execvp("tail", ["tail", "-f", str(log_path(rig))])
    else:
        ensure_daemon()
        if not start_supervisor(rig):
            print(
                "4genteam: continuing without a running compaction supervisor (see above)",
                file=sys.stderr,
            )
        argv_tool(watch.main, ["watch", "--rig", rig])
        open_ui()
        if not os.environ.get("HERDR_ENV"):
            os.execvp("herdr", ["herdr"])
    return EXIT_OK


def main(argv: list[str] | None = None) -> int:
    args = list(sys.argv[1:] if argv is None else argv)
    command, rest = (args[0], args[1:]) if args else ("up", [])
    if command in ("-h", "--help", "help"):
        print(__doc__)
        return 0
    if command in LIFECYCLE_USAGE and rest[:1] and rest[0] in ("-h", "--help"):
        # HELP IS HELP FOR EVERY VERB. Before this, only the FIRST token was inspected, so
        # `4genteam compact --help` made `--help` the rig of a lifecycle verb and `feedback --help`
        # handed the flag to the shell client, which refused it as an unknown option.
        print(LIFECYCLE_USAGE[command])
        return 0
    if command in LIFECYCLE:
        return lifecycle(command, rest)
    if command == "feedback":
        return feedback(rest)
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
