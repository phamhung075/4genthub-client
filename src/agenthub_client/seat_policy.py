#!/usr/bin/env python3
"""Per-seat permission policy for omp seats, written as each seat's own config.

OpenRig launches an omp seat in exactly one of two postures: ``yolo`` (every tool
call is approved) or ``always-ask``, which the managed runner auto-cancels, so that
seat can call no tool at all. Nothing in between exists on the OpenRig side
(``rig seat set-permissions`` refuses omp). So every seat runs ``yolo`` and the
restrictions live where omp enforces them: ``<seat state>/agent/config.yml``.

omp honours two controls in every approval mode, and both are used here:

* ``bash.patterns`` - ordered shell-command rules. ``allowCompoundCommands`` makes it
  evaluate each command of ``a && b`` and ``a; b`` on its own, so an anchored pattern
  such as ``git push*`` also catches ``cd x && git push``.
* ``tools.approval`` - a per-tool ``deny``, including the MCP tools (``mcp__<server>_<tool>``).

``mcp.startupTimeoutMs: 0`` is written too. omp waits 250 ms by default for MCP tools at
startup; the HTTPS agenthub server is slower than that, so without it a seat's first turns
run without the 4genthub tools.

THIS IS A GUARD RAIL AGAINST ACCIDENTS, NOT A SANDBOX. A seat that can write files or run
``eval`` can still do anything these rules block by another route. The rules encode what
the project already forbids in prose: only the principal pushes, nobody amends or
force-resets the shared tree, no seat touches production or kills the tmux server, and
rig lifecycle is the lead's.

Usage::

    4genteam policy show SEAT --rig RIG        print one seat's config
    4genteam policy apply --rig RIG [--check]  write every seat's config.yml; with
                                                      --check write nothing and exit 1 on drift

A running seat reads its config at start, so ``apply`` takes effect on the next launch.
"""

import argparse
import json
import os
import pwd
import sys
from pathlib import Path

EXIT_OK = 0
EXIT_DRIFT = 1
EXIT_USAGE = 2


def real_home() -> Path:
    """The REAL user's home directory - which ``$HOME`` is not, inside an omp seat.

    OpenRig launches a seat with HOME set to the seat's own state directory
    (``/home/<user>/.openrig/state/omp/<rig>-<seat>@<rig>``), so ``Path.home()``,
    ``os.path.expanduser("~")`` and any read of ``$HOME`` all resolve there. A default built on
    them therefore appends the state root to itself, which is how the documented
    ``apply --rig RIG --check`` came to report every seat "no agent directory" and exit 2 while
    the policy was in fact applied.

    The passwd database answers for the user this process runs as, and that answer is the same
    inside a seat and out of one - which is the property a MACHINE-LEVEL default needs, since
    the state root is a machine-level location rather than a per-seat one. ``--state-root``
    stays the explicit override for any other layout.
    """
    return Path(pwd.getpwuid(os.getuid()).pw_dir)


DEFAULT_STATE_ROOT = real_home() / ".openrig" / "state" / "omp"

# rig -> seat -> role. An unlisted rig or seat is an error: there is no permissive default.
SEAT_ROLES = {
    "4genthub-min": {
        "lead": "lead",
        "go-dev": "dev",
        "fe-dev": "dev",
        "web-dev": "dev",
        "skills-dev": "dev",
        "context-dev": "dev",
        "feedback-dev": "dev",
        "reviewer": "reviewer",
        "writer": "writer",
    },
}

# Tokens of the newest conversation kept verbatim at a compaction; the rest becomes the summary.
# omp's default is 20000, which holds the post-compaction floor near 42k; 8000 brings it near 30k.
KEEP_RECENT_TOKENS = 8000

# Seats on a lower thinking level than omp's default (high). A trial: reasoning is ~41% of a live
# context, so a seat whose jobs are routine is measured at medium before the others follow.
SEAT_THINKING = {"4genthub-min": {"writer": "medium"}}

# Denied for every seat. Anchored: the compound-command evaluation matches each command.
COMMON_BASH_DENY = [
    "git push*",
    "git reset --hard*",
    "git clean*",
    "git checkout -- *",
    "git stash drop*",
    "git stash clear*",
    "git commit*--amend*",
    "git add -A*",
    "git add --all*",
    "git add .",
    "rm -rf*",
    "rm -fr*",
    "sudo *",
    "ssh *",
    "scp *",
    "docker *",
    "tmux kill-*",
    "pkill *",
    "killall *",
    "printenv*",
    "cat *deepseek/env*",
    "rig down*",
    "rig up*",
    "rig remove*",
]

# Rig lifecycle belongs to the lead, who restarts seats; the other roles only message and read.
NON_LEAD_BASH_DENY = [
    "rig launch*",
    "rig seat stop*",
    "rig seat launch*",
    "rig seat clean*",
    "rig seat set-*",
]

# The 4genthub MCP tools that manage agents, seats and the server itself are the lead's. The
# task, subtask, context, project and branch tools - the context sync - stay open to everyone.
NON_LEAD_TOOL_DENY = [
    "mcp__agenthub_http_manage_agent",
    "mcp__agenthub_http_manage_connection",
    "mcp__agenthub_http_manage_seat",
]

# A reviewer reads and reports; it does not rewrite source (it can still write its verdict files).
REVIEWER_TOOL_DENY = ["edit", "ast_edit"]


def policy_for(role: str) -> tuple[list[str], list[str]]:
    """Return (bash patterns to deny, tools to deny) for a role."""
    bash = list(COMMON_BASH_DENY)
    tools: list[str] = []
    if role != "lead":
        bash += NON_LEAD_BASH_DENY
        tools += NON_LEAD_TOOL_DENY
    if role == "reviewer":
        tools += REVIEWER_TOOL_DENY
    return bash, tools


def render_config(seat: str, role: str, rig: str) -> str:
    """The seat's config.yml. json.dumps gives YAML-safe double-quoted scalars.

    ``rig`` is REQUIRED rather than defaulted. It selects the per-rig thinking level, and a caller
    that omitted it used to get a document with that level silently ABSENT: no exception, no log
    line, nothing to fail - the seat would come up on the wrong level and only a reader of the
    rendered file could tell. The omission now fails at the call site instead.
    """
    bash, tools = policy_for(role)
    thinking = SEAT_THINKING.get(rig, {}).get(seat)
    lines = [
        f"# generated by agenthub_client.seat_policy for {seat} (role: {role}) - do not edit",
        "mcp:",
        "  startupTimeoutMs: 0",
        "compaction:",
        f"  keepRecentTokens: {KEEP_RECENT_TOKENS}",
        *([f"defaultThinkingLevel: {thinking}"] if thinking else []),
        "bash:",
        "  allowCompoundCommands: true",
        "  patterns:",
    ]
    # The startup call is exempt in EVERY approval mode, and it is the one entry that does not come
    # from a role table: the startup file orders `rig whoami` before anything else, and `ask.timeout`
    # is 0 live (which its own key text says disables the auto-select), so on an always-ask seat an
    # unanswered prompt blocks the seat on its first call. A layer that only exempts in some modes
    # cannot fix that; this file is the seat's own and omp honours `allow` in every mode.
    lines += [f"    - match: {json.dumps('rig whoami*')}", "      approval: allow"]
    for pattern in bash:
        lines += [f"    - match: {json.dumps(pattern)}", "      approval: deny"]
    if tools:
        lines += ["tools:", "  approval:"]
        lines += [f"    {tool}: deny" for tool in tools]
    return "\n".join(lines) + "\n"


def seat_roles(rig: str) -> dict[str, str]:
    if rig not in SEAT_ROLES:
        raise SystemExit(f"no policy table for rig {rig!r}; add it to SEAT_ROLES")
    return SEAT_ROLES[rig]


def config_path(state_root: Path, rig: str, seat: str) -> Path:
    return state_root / f"{rig}-{seat}@{rig}" / "agent" / "config.yml"


def cmd_show(args: argparse.Namespace) -> int:
    roles = seat_roles(args.rig)
    if args.seat not in roles:
        print(f"seat {args.seat!r} has no role in rig {args.rig!r}", file=sys.stderr)
        return EXIT_USAGE
    sys.stdout.write(render_config(args.seat, roles[args.seat], args.rig))
    return EXIT_OK


def policy_keys_match(want: str, have: str | None) -> bool:
    """Whether ``have`` carries the policy's own keys exactly as ``want`` states them.

    STRUCTURAL RATHER THAN BYTE-EQUAL, and the reason is that the pipeline now writes this file too:
    ``4genteam sync rig`` applies this document to the seat's ``config.yml`` and MERGES it,
    keeping whatever else the runtime's file carries, so a byte comparison would call a
    correctly-policed seat drifty. What must hold is exact: every key this module defines, at the
    value it defines. Keys it does not define are none of its business.
    """
    if have is None:
        return False
    try:
        import yaml
    except ImportError as err:  # pragma: no cover - environment dependent
        raise SystemExit(f"PyYAML is required to check a seat's config: {err}") from err
    try:
        wanted = yaml.safe_load(want)
        present = yaml.safe_load(have) or {}
    except yaml.YAMLError:
        return False
    if not isinstance(wanted, dict):
        return False
    if not isinstance(present, dict):
        return False
    return all(present.get(key) == value for key, value in wanted.items())


def cmd_apply(args: argparse.Namespace) -> int:
    state_root = Path(args.state_root)
    drift = 0
    for seat, role in seat_roles(args.rig).items():
        path = config_path(state_root, args.rig, seat)
        if not path.parent.is_dir():
            print(
                f"{seat}: no agent directory at {path.parent} (never launched?)",
                file=sys.stderr,
            )
            return EXIT_USAGE
        want = render_config(seat, role, args.rig)
        have = path.read_text() if path.exists() else None
        if policy_keys_match(want, have):
            print(f"{seat}: ok ({role})")
            continue
        drift += 1
        if args.check:
            print(f"{seat}: DRIFT ({role})")
            continue
        path.write_text(want)
        print(f"{seat}: written ({role})")
    return EXIT_DRIFT if args.check and drift else EXIT_OK


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)
    show = sub.add_parser("show", help="print one seat's config")
    show.add_argument("seat")
    show.add_argument("--rig", required=True)
    show.set_defaults(func=cmd_show)
    apply = sub.add_parser("apply", help="write every seat's config.yml")
    apply.add_argument("--rig", required=True)
    apply.add_argument("--state-root", default=str(DEFAULT_STATE_ROOT))
    apply.add_argument(
        "--check", action="store_true", help="write nothing; exit 1 on drift"
    )
    apply.set_defaults(func=cmd_apply)
    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
