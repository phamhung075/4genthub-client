#!/usr/bin/env python3
"""Report OpenRig seat status and herdr agent status up to 4genthub.

A background daemon for each PC. Status goes UP only: it reads local tools and
POSTs to ``{AGENTHUB_URL}/api/v2/openrig/seat-status``. It never receives
commands and never reads terminal content.

Every outgoing object is built by copying named fields (allow-list). Nothing
from a tool's JSON is passed through, enums are clamped to ``unknown``, names
are validated, and the only free text (``detail``) comes from a few short
fields and goes through ``openrig_scrub.scrub``.

OpenRig source: ``rig ps --json --nodes -A`` (local daemon, all rigs) -> a bare
list of nodes (an ``{"items": [...]}`` envelope is also accepted). Per node:
  rigName                       -> room
  logicalId (minus "<pod>.")    -> seat
  runtime                       -> runtime  (claude-code|codex|agy|terminal else unknown)
  state, first match wins:
    sessionStatus stopped|exited, or lifecycleState detached|recoverable -> stopped
    agentActivity.state unknown with reason "no_runtime_hook"              -> stopped
      (the agent process died outside `rig seat stop` while the tmux session stayed up;
       owner decision 2026-10-05: report stopped and respawn. A just-launched seat carries
       reason null, so it stays unknown rather than being called dead.)
    agentActivity.state needs_input, or startupStatus
      attention_required|failed                                         -> blocked
    agentActivity.state running                                          -> running
    agentActivity.state idle                                             -> idle
    anything else                                                        -> unknown
  lifecycleState attention_required is not a blocked signal in either direction: OpenRig keeps
  it for a dead agent and for a live busy seat, so it cannot decide on its own (see seat_state).
  The seat key is the member name only, so two pods of one rig with the same member name
  collide: the first node is sent, the others are skipped and named on stderr.
  detail  <- latestError, heldReason, agentActivity.reason (scrubbed, <=200)
  pinned_hash <- ~/.openrig/agenthub-seats/<room>/<seat>/pinned.json "hash", else ""

herdr source: ``herdr api snapshot`` -> result.snapshot.agents[]. Only
agent, agent_status and pane_id are kept. agent_status maps to
idle|working|blocked|done, anything else unknown.

If a tool is missing or fails, that source is reported as empty and the loop
continues; the condition is logged to stderr once per change.

Environment:
  AGENTHUB_URL            base URL of the 4genthub server
  AGENTHUB_MACHINE_TOKEN  THIS PC's machine token (sent as a header, never logged or written),
                          issued by `4genteam bridge register`, valid only on
                          POST /api/v2/openrig/seat-status and only for this machine id. One
                          variable never means two credentials: the bridge reads THIS name, and
                          `run` fails loudly when it is missing.
  AGENTHUB_TOKEN          the USER token, read ONLY by `register` to call
                          POST /api/v2/openrig/machines. `run` and `once` never read it.

Usage:
  4genteam bridge register [--machine-id ID] [--env-file PATH]   issue this PC's machine token
  4genteam bridge run [--interval 20] [--machine-id ID] [--once]
  4genteam bridge once [--print] [--machine-id ID]
  4genteam bridge install-service

`register` posts /api/v2/openrig/machines with the user token and writes the returned machine
token into the env file the service unit reads (%h/.config/agenthub-bridge.env, mode 0600). Use
the SAME --machine-id for register and run, because the server binds a token to its machine.

Exit codes: 0 success, 1 send failure (--once), 2 usage error.
"""

import argparse
import json
import os
import re
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from collections.abc import Callable
from datetime import datetime, timezone
from pathlib import Path

import yaml

from . import scrub as openrig_scrub
from . import seat_policy as openrig_seat_policy

# THE ONE HOME RESOLUTION THIS TREE HAS, LOADED RATHER THAN COPIED - the same resolution
# 4genteam policy carries and 4genteam sync loads. real_home() answers from the
# passwd entry, because OpenRig launches a seat with HOME pointed at the seat's OWN state
# directory (/home/<user>/.openrig/state/omp/<rig>-<seat>@<rig>): Path.home(),
# os.path.expanduser("~") and any read of $HOME all resolve THERE, so every MACHINE-LEVEL default
# built on them lands inside that seat's tree instead of the account's. The bridge IS run from a
# seat in practice, so all three defaults below resolve from the account.
real_home = openrig_seat_policy.real_home

STATUS_PATH = "/api/v2/openrig/seat-status"
REGISTER_PATH = "/api/v2/openrig/machines"
DEFAULT_ENV_FILE = real_home() / ".config" / "agenthub-bridge.env"
DEFAULT_PINS = real_home() / ".openrig" / "agenthub-seats"
# DEFAULT_SYNC_STATE records, per seat, the expected hash the cloud last answered in_sync for.
# It is the client half of drift visibility: without it a seat's pinned hash has nothing local
# to be compared against, so a drift is only ever visible by asking the server.
DEFAULT_SYNC_STATE = real_home() / ".openrig" / "bridge-sync.json"
HEARTBEAT_SECONDS = 60.0
MAX_BACKOFF = 120.0
TOOL_TIMEOUT = 15
SEND_TIMEOUT = 15
# MAX_RESPONSE_BYTES bounds what the bridge will read from a report answer. The answer is small
# (verdicts, not full seat bodies), and the bound is the one the server applies to its side.
MAX_RESPONSE_BYTES = 1 << 20

EXIT_OK = 0
EXIT_REMOTE = 1
EXIT_USAGE = 2

NAME_RE = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9_-]*")
HASH_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
PANE_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9:_-]{0,31}")

SEAT_STATES = {"running", "idle", "blocked", "stopped", "unknown"}
RUNTIMES = {"claude-code", "codex", "agy", "omp", "terminal", "unknown"}
AGENT_STATUSES = {"idle", "working", "blocked", "done", "unknown"}
# OpenRig's five edge kinds; the server's commpolicy.ValidKind is the other copy of this list.
EDGE_KINDS = {
    "delegates_to",
    "spawned_by",
    "can_observe",
    "collaborates_with",
    "escalates_to",
}

Runner = Callable[[list[str]], str]
# A sender returns the status AND the response body: the report answer carries the per-seat
# expected hash and verdict, which is the only way a bridge (holding no user token) can learn
# what the cloud expects. A sender that returns a status alone would answer the report and then
# throw away the answer.
Sender = Callable[[bytes], tuple[int, bytes]]


def run_command(argv: list[str]) -> str:
    """Run a local tool and return stdout; raise on absence, failure or timeout."""
    result = subprocess.run(
        argv, capture_output=True, text=True, timeout=TOOL_TIMEOUT, check=False
    )
    if result.returncode != 0:
        first = (result.stderr or "").strip().splitlines()[:1]
        raise RuntimeError(
            f"exit {result.returncode}: {first[0][:80] if first else ''}"
        )
    return result.stdout


def _str(value) -> str:
    return value if isinstance(value, str) else ""


def _dict(value) -> dict:
    return value if isinstance(value, dict) else {}


def _valid(pattern: re.Pattern[str], value) -> bool:
    return isinstance(value, str) and pattern.fullmatch(value) is not None


def seat_state(node: dict) -> str:
    session = _str(node.get("sessionStatus"))
    life = _str(node.get("lifecycleState"))
    startup = _str(node.get("startupStatus"))
    activity = _str(_dict(node.get("agentActivity")).get("state"))
    if session in ("stopped", "exited") or life in ("detached", "recoverable"):
        return "stopped"
    # A live session whose runtime hook is gone is an agent that died outside `rig seat stop`
    # (OpenRig reports agentActivity.state "unknown" with reason "no_runtime_hook" and keeps the
    # tmux session). Owner decision 2026-10-05: that reads "stopped" and the seat is respawned.
    # The reason is the only discriminator, and it is RUNTIME-DEPENDENT: measured 2026-10-05, a
    # just-launched agy seat ALSO reports reason "no_runtime_hook" until its hook attaches (~15s),
    # so it reads stopped for that window, while an omp seat with no activity yet reports reason
    # null and stays unknown (live inventory, 4genthub-deepseek.supervisor). That is why `respawn`
    # requires the dead reading to hold (default 30s) and why the hold must not be shortened
    # without re-measuring per runtime.
    if (
        activity == "unknown"
        and _str(_dict(node.get("agentActivity")).get("reason")) == "no_runtime_hook"
    ):
        return "stopped"
    # `lifecycleState: attention_required` is deliberately NOT a "blocked" signal: OpenRig keeps it
    # for a dead agent AND for a live busy seat, so it cannot tell a seat waiting on a human from
    # one that is gone or working; the agent's own activity is the truth source.
    if activity == "needs_input" or startup in (
        "attention_required",
        "failed",
    ):
        return "blocked"
    if activity in ("running", "idle"):
        return activity
    return "unknown"


def seat_name(node: dict) -> str:
    # OpenRig ids never contain a dot, so "<pod>.<member>" splits at the first one.
    return _str(node.get("logicalId")).partition(".")[2] or _str(node.get("logicalId"))


def pinned_hash(pins_dir: Path, room: str, seat: str) -> str:
    try:
        data = json.loads(
            (pins_dir / room / seat / "pinned.json").read_text(encoding="utf-8")
        )
    except (OSError, ValueError):
        return ""
    value = _dict(data).get("hash")
    return value if _valid(HASH_RE, value) else ""


def seat_detail(node: dict) -> str:
    parts = [
        _str(node.get("latestError")),
        _str(node.get("heldReason")),
        _str(_dict(node.get("agentActivity")).get("reason")),
    ]
    return "; ".join(p for p in parts if p)


def seat_pod(node: dict) -> str:
    logical_id = _str(node.get("logicalId"))
    return logical_id.partition(".")[0] if "." in logical_id else ""


def duplicate_message(room: str, seat: str, pods: list[str]) -> str:
    names = sorted({pod or "(no pod)" for pod in pods})
    if len(names) == 1:
        return (
            f"seat {seat!r} in rig {room} is listed twice in pod {names[0]}; rename one"
        )
    return (
        f"seat {seat!r} in rig {room} exists in pods "
        f"{', '.join(names[:-1])} and {names[-1]}; rename one"
    )


def build_seats(
    nodes: list, secrets: dict[str, str], pins_dir: Path, strip_detail: bool
) -> tuple[list[dict], int, list[str]]:
    """Return (seats, invalid-name count, one message per duplicated seat key)."""
    seats: list[dict] = []
    pods_of: dict[tuple[str, str], list[str]] = {}
    invalid = 0
    for node in nodes:
        node = _dict(node)
        room, seat = _str(node.get("rigName")), seat_name(node)
        if not (_valid(NAME_RE, room) and _valid(NAME_RE, seat)):
            invalid += 1
            continue
        if (room, seat) in pods_of:
            pods_of[(room, seat)].append(seat_pod(node))
            continue
        pods_of[(room, seat)] = [seat_pod(node)]
        if strip_detail:
            detail, redacted = "", True
        else:
            detail, redacted = openrig_scrub.scrub(seat_detail(node), secrets)
        runtime = _str(node.get("runtime"))
        seats.append(
            {
                "room": room,
                "seat": seat,
                "state": seat_state(node),
                "runtime": runtime if runtime in RUNTIMES else "unknown",
                "pinned_hash": pinned_hash(pins_dir, room, seat),
                "detail": detail,
                "redacted": redacted,
            }
        )
    duplicates = [
        duplicate_message(room, seat, pods)
        for (room, seat), pods in pods_of.items()
        if len(pods) > 1
    ]
    return seats, invalid, duplicates


def build_agents(raw_agents: list) -> list[dict]:
    agents: list[dict] = []
    for raw in raw_agents:
        raw = _dict(raw)
        name, pane = raw.get("agent"), raw.get("pane_id")
        if not (_valid(NAME_RE, name) and _valid(PANE_RE, pane)):
            continue
        status = _str(raw.get("agent_status"))
        agents.append(
            {
                "agent": name,
                "status": status if status in AGENT_STATUSES else "unknown",
                "pane_id": pane,
            }
        )
    return agents


def parse_rig_nodes(output: str) -> list:
    data = json.loads(output)
    items = data.get("items") if isinstance(data, dict) else data
    if not isinstance(items, list):
        raise ValueError("unexpected rig ps JSON shape")
    return items


def parse_rig_edges(output: str) -> list[tuple[str, str, str]]:
    """The (from, to, kind) of every pod edge in one `rig export` spec.

    The CLI ends its output with an `Exported to <path>` status line that is not YAML.
    """
    spec = yaml.safe_load(output.partition("\nExported to ")[0])
    pods = _dict(spec).get("pods")
    if not isinstance(pods, list):
        raise ValueError("unexpected rig export YAML shape")
    return [
        (_str(edge.get("from")), _str(edge.get("to")), _str(edge.get("kind")))
        for pod in pods
        for edge in _dict(pod).get("edges") or []
        if isinstance(edge, dict)
    ]


def build_edges(rigs: dict[str, str], read_edges: Callable[[str], list]) -> list[dict]:
    """The reported topology: one entry per distinct, valid, directed link of every rig.

    The server refuses the WHOLE report for one bad edge (an unknown kind, a self edge, a
    repeated link), which reads as the machine going offline, so a bad edge is dropped here.
    """
    edges: list[dict] = []
    seen: set[tuple[str, str, str, str]] = set()
    for rig_id, room in rigs.items():
        for src, dst, kind in read_edges(rig_id):
            key = (room, src, dst, kind)
            if (
                kind in EDGE_KINDS
                and src != dst
                and _valid(NAME_RE, room)
                and _valid(NAME_RE, src)
                and _valid(NAME_RE, dst)
                and key not in seen
            ):
                seen.add(key)
                edges.append({"room": room, "from": src, "to": dst, "kind": kind})
    return edges


def parse_herdr_agents(output: str) -> list:
    agents = _dict(_dict(_dict(json.loads(output)).get("result")).get("snapshot")).get(
        "agents"
    )
    if not isinstance(agents, list):
        raise ValueError("unexpected herdr snapshot JSON shape")
    return agents


def sanitize_machine_id(name: str) -> str:
    cleaned = re.sub(r"[^a-zA-Z0-9_-]", "-", name).lstrip("-_")
    return cleaned or "machine"


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def make_sender(base_url: str, token: str) -> Sender:
    url = base_url.rstrip("/") + STATUS_PATH

    def send(body: bytes) -> tuple[int, bytes]:
        request = urllib.request.Request(
            url,
            data=body,
            method="POST",
            headers={
                "Authorization": f"Bearer {token}",
                "Content-Type": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=SEND_TIMEOUT) as response:
                return response.status, response.read(MAX_RESPONSE_BYTES)
        except urllib.error.HTTPError as err:
            # The answer is read on an error status too: it is small, and a caller naming what
            # the server said is better than a bare code. Bounded, so a hostile answer cannot
            # be unbounded.
            return err.code, err.read(MAX_RESPONSE_BYTES)

    return send


class RegistrationError(Exception):
    """`register` could not obtain a machine token."""


def register_machine(base_url: str, user_token: str, machine_id: str) -> str:
    """Issue this machine's token with the USER credential and return it.

    The token is never printed. A non-2xx answer or a body without a token is a loud failure:
    a bridge that cannot report must not start quietly without a credential.
    """
    request = urllib.request.Request(
        base_url.rstrip("/") + REGISTER_PATH,
        data=json.dumps({"machine_id": machine_id}).encode("utf-8"),
        method="POST",
        headers={
            "Authorization": f"Bearer {user_token}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=SEND_TIMEOUT) as response:
            body = response.read()
    except urllib.error.HTTPError as err:
        raise RegistrationError(
            f"machine registration refused: HTTP {err.code}"
        ) from err
    except OSError as err:
        raise RegistrationError(
            f"machine registration failed: {type(err).__name__}: {err}"
        ) from err
    try:
        token = json.loads(body.decode("utf-8")).get("token")
    except ValueError as err:
        raise RegistrationError("machine registration returned no JSON token") from err
    if not isinstance(token, str) or not token:
        raise RegistrationError("machine registration returned no token")
    return token


def write_env_file(path: Path, url: str, machine_token: str) -> None:
    """Write AGENTHUB_URL and AGENTHUB_MACHINE_TOKEN into path at mode 0600.

    Other lines are preserved, so an existing file keeps whatever else it held. The mode is set
    explicitly after writing because O_CREAT's mode is masked by the umask.
    """
    values = {"AGENTHUB_URL": url, "AGENTHUB_MACHINE_TOKEN": machine_token}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError:
        lines = []
    kept = [line for line in lines if line.split("=", 1)[0].strip() not in values]
    kept.extend(f"{key}={value}" for key, value in values.items())
    path.parent.mkdir(parents=True, exist_ok=True)
    with os.fdopen(
        os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600),
        "w",
        encoding="utf-8",
    ) as handle:
        handle.write("\n".join(kept) + "\n")
    os.chmod(path, 0o600)


def read_sync_state(path: Path) -> dict[str, str]:
    """The recorded last-in-sync expected hash per seat key, or empty.

    A missing, unreadable or malformed record reads as empty rather than raising: a bridge whose
    own state file is gone must still report, and every verdict then reads unknown until the
    cloud answers again. It never invents a hash.
    """
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}
    seats = data.get("seats") if isinstance(data, dict) else None
    if not isinstance(seats, dict):
        return {}
    return {str(k): str(v) for k, v in seats.items() if isinstance(v, str) and v}


def write_sync_state(path: Path, machine_id: str, seats: dict[str, str]) -> None:
    """Record the expected hash per seat through a temporary file and a rename.

    The rename is what makes the record survive a restart: a reader sees either the previous
    whole record or the new one, never a half-written file. A write that fails is loud but not
    fatal - a bridge that cannot record must still report, and it says so rather than looping.
    """
    payload = {"machine_id": machine_id, "seats": seats}
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_name(path.name + ".tmp")
        temporary.write_text(
            json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        temporary.replace(path)
    except OSError as err:
        print(f"openrig-bridge: cannot record the sync state in {path}: {err}", file=sys.stderr)


def verdicts_of(body: bytes):
    """The per-seat verdicts in a report answer, or None when the answer carries none.

    None means the record is left as it was: an answer that is absent, not JSON, not an object,
    or carries no verdicts list is not evidence that anything moved.
    """
    try:
        answer = json.loads(body.decode("utf-8"))
    except (AttributeError, UnicodeDecodeError, ValueError):
        return None
    verdicts = answer.get("verdicts") if isinstance(answer, dict) else None
    return verdicts if isinstance(verdicts, list) else None


def register_command(args) -> int:
    """`register`: issue this machine's token and store it where the service unit reads it."""
    url, user_token = (
        os.environ.get("AGENTHUB_URL", ""),
        os.environ.get("AGENTHUB_TOKEN", ""),
    )
    if not url or not user_token:
        print(
            "AGENTHUB_URL and AGENTHUB_TOKEN (the user token) must be set to register a machine",
            file=sys.stderr,
        )
        return EXIT_USAGE
    machine_id = args.machine_id or sanitize_machine_id(socket.gethostname())
    if not NAME_RE.fullmatch(machine_id):
        print(f"invalid machine id: {machine_id!r}", file=sys.stderr)
        return EXIT_USAGE
    try:
        token = register_machine(url, user_token, machine_id)
    except RegistrationError as err:
        print(f"openrig-bridge: {err}", file=sys.stderr)
        return EXIT_REMOTE
    env_file = Path(args.env_file) if args.env_file else DEFAULT_ENV_FILE
    write_env_file(env_file, url, token)
    print(
        f"registered machine {machine_id!r}; its token is in {env_file} (mode 0600, not printed). "
        f"Run the bridge with --machine-id {machine_id!r} so the token and the report agree."
    )
    return EXIT_OK


class Bridge:
    """Builds payloads and decides when to send them."""

    def __init__(
        self,
        machine_id: str,
        interval: float,
        send: Sender,
        runner: Runner = run_command,
        secrets: dict[str, str] | None = None,
        pins_dir: Path = DEFAULT_PINS,
        sync_state_path: Path = DEFAULT_SYNC_STATE,
        clock: Callable[[], float] = time.monotonic,
    ):
        self.machine_id = machine_id
        self.interval = interval
        self.send = send
        self.runner = runner
        self.secrets = secrets or {}
        self.pins_dir = pins_dir
        self.sync_state_path = sync_state_path
        self.sync_state = read_sync_state(sync_state_path)
        self.clock = clock
        self.strip_detail = False
        self.backoff = 0.0
        self._last_key: str | None = None
        self._last_sent: float | None = None
        self._notes: dict[str, str] = {}

    def note(self, key: str, message: str) -> None:
        if self._notes.get(key) != message:
            self._notes[key] = message
            print(f"openrig-bridge: {message}", file=sys.stderr, flush=True)

    def _read(self, key: str, argv: list[str], parse: Callable[[str], list]) -> list:
        try:
            items = parse(self.runner(argv))
        except FileNotFoundError:
            self.note(key, f"{argv[0]} not installed; source absent")
        except Exception as err:
            self.note(key, f"{argv[0]} unavailable: {type(err).__name__}: {err}")
        else:
            self.note(key, f"{argv[0]} ok")
            return items
        return []

    def build_payload(self) -> dict:
        nodes = self._read(
            "rig", ["rig", "ps", "--json", "--nodes", "-A"], parse_rig_nodes
        )
        raw_agents = self._read(
            "herdr", ["herdr", "api", "snapshot"], parse_herdr_agents
        )
        seats, invalid, duplicates = build_seats(
            nodes, self.secrets, self.pins_dir, self.strip_detail
        )
        if invalid:
            self.note("invalid", f"skipped {invalid} seat(s) with invalid names")
        for message in duplicates:
            self.note(f"duplicate {message}", message)
        return {
            "machine_id": self.machine_id,
            "reported_at": utc_now(),
            "seats": seats,
            "agents": build_agents(raw_agents),
            "edges": build_edges(
                {
                    _str(_dict(n).get("rigId")): _str(_dict(n).get("rigName"))
                    for n in nodes
                },
                lambda rig_id: self._read(
                    f"edges {rig_id}",
                    ["rig", "export", rig_id, "-o", "/dev/stdout"],
                    parse_rig_edges,
                ),
            ),
        }

    def _state_key(self, room: str, seat: str) -> str:
        return f"{self.machine_id}/{room}/{seat}"

    def local_record(self, seats: list[dict]) -> dict[str, dict]:
        """What this machine can say about each seat from its own record, and NOTHING more.

        It answers exactly one question: has this seat changed since the cloud last CONFIRMED it
        in sync? So its values are ``unchanged`` / ``changed`` / ``unknown`` - deliberately not
        the cloud's words. It CANNOT establish ``in_sync``: that is the cloud comparing the
        running hash against its NEWEST stored snapshot, and the cloud can move while this
        machine does not. The answer to a report is the only channel that knows the cloud's
        current expectation, which is why a verdict that is not ``in_sync`` is also named from
        that answer (see apply_verdicts). No record, or no running hash, reads ``unknown``.
        """
        out: dict[str, dict] = {}
        for seat in seats:
            room, name = _str(seat.get("room")), _str(seat.get("seat"))
            running = _str(seat.get("pinned_hash"))
            confirmed = self.sync_state.get(self._state_key(room, name), "")
            if not confirmed or not running:
                since = "unknown"
            elif running == confirmed:
                since = "unchanged"
            else:
                since = "changed"
            out[f"{room}/{name}"] = {
                "running": running,
                "last_in_sync": confirmed,
                "since_last_in_sync": since,
            }
        return out

    def apply_verdicts(self, body: bytes) -> None:
        """Take what the cloud's answer says: record the ``in_sync`` ones, NAME the rest.

        This answer is the ONLY channel that knows the cloud's CURRENT expectation, so a seat the
        cloud calls drift or unknown is named here even while this machine's own record still
        matches it - the cloud moved and the machine did not, which the local record cannot see
        by construction. A seat the cloud confirms ``in_sync`` is recorded, so the local view has
        a confirmed hash to compare against next cycle. An answer that is absent, not JSON or
        carries no verdicts leaves the record as it was rather than clearing it.
        """
        verdicts = verdicts_of(body)
        if verdicts is None:
            return
        changed = False
        for verdict in verdicts:
            if not isinstance(verdict, dict):
                continue
            room, seat = _str(verdict.get("room")), _str(verdict.get("seat"))
            if not room or not seat:
                continue
            where = f"{room}/{seat}"
            expected = _str(verdict.get("expected_hash"))
            sync = _str(verdict.get("sync"))
            if sync == "in_sync":
                if not expected:
                    continue
                key = self._state_key(room, seat)
                if self.sync_state.get(key) != expected:
                    self.sync_state[key] = expected
                    changed = True
                continue
            self.note(
                f"cloud:{where}",
                f"the cloud reports {where} {sync or 'without a verdict'}"
                + (f" (it expects {expected})" if expected else ""),
            )
        if changed:
            write_sync_state(self.sync_state_path, self.machine_id, self.sync_state)

    def cycle(self) -> float:
        """Run one cycle; return seconds to wait before the next one."""
        try:
            payload = self.build_payload()
            key = json.dumps(
                {k: v for k, v in payload.items() if k != "reported_at"}, sort_keys=True
            )
            # What this machine knows from its OWN record, said BEFORE the report: it answers only
            # whether the seat has changed since the cloud last confirmed it, so the words are
            # unchanged/changed and never the cloud's in_sync. The cloud's own verdicts are named
            # from the answer below, which is the only channel that knows them.
            for where, record in self.local_record(payload["seats"]).items():
                if record["since_last_in_sync"] == "changed":
                    self.note(
                        f"changed:{where}",
                        f"{where} has changed since the cloud last confirmed it in sync "
                        f"(running {record['running']}, confirmed {record['last_in_sync']})",
                    )
            now = self.clock()
            due = self._last_sent is None or now - self._last_sent >= HEARTBEAT_SECONDS
            if key == self._last_key and not due:
                return self.interval
            status, body = self.send(json.dumps(payload).encode("utf-8"))
        except Exception as err:
            return self._fail(f"cycle failed: {type(err).__name__}: {err}")
        if 200 <= status < 300:
            self.apply_verdicts(body)
            self._last_key, self._last_sent = key, now
            self.strip_detail = False
            self.backoff = 0.0
            self.note("send", "sent")
            return self.interval
        if status == 422:
            self.strip_detail = True
            self.note(
                "send",
                "server rejected text as secret-bearing (422); dropping detail next cycle",
            )
            return self.interval
        if status == 401:
            # Loud and actionable: a rejected credential never recovers by retrying, and a bridge
            # that cannot report must say so rather than loop silently.
            return self._fail(
                "machine token rejected (HTTP 401): run `4genteam bridge register` to issue this "
                "machine's token, and check that --machine-id matches the one it was issued for"
            )
        return self._fail(f"server answered HTTP {status}")

    def _fail(self, message: str) -> float:
        self.note("send", message)
        self.backoff = min(
            MAX_BACKOFF, self.backoff * 2 if self.backoff else self.interval
        )
        return self.backoff

    def run(self, stop: threading.Event, once: bool = False) -> int:
        while not stop.is_set():
            delay = self.cycle()
            if once:
                return (
                    EXIT_OK
                    if self.backoff == 0.0 and not self.strip_detail
                    else EXIT_REMOTE
                )
            stop.wait(delay)
        return EXIT_OK


SERVICE_UNIT = """[Unit]
Description=4genthub OpenRig status bridge
After=network-online.target

[Service]
EnvironmentFile=%h/.config/agenthub-bridge.env
ExecStart={python} -m agenthub_client.bridge run
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
"""


def service_unit() -> str:
    return SERVICE_UNIT.format(python=sys.executable)


def build_bridge(args, send: Sender) -> Bridge:
    machine_id = args.machine_id or sanitize_machine_id(socket.gethostname())
    if not NAME_RE.fullmatch(machine_id):
        print(f"invalid machine id: {machine_id!r}", file=sys.stderr)
        raise SystemExit(EXIT_USAGE)
    return Bridge(
        machine_id,
        getattr(args, "interval", 20.0),
        send,
        # named here rather than left to the class default: the local tools a bridge shells out
        # to are resolved when the bridge is built, which is also what makes the CLI path
        # testable without the tools on the host.
        runner=run_command,
        secrets=openrig_scrub.secret_values_from_env(os.environ),
    )


def sender_from_env() -> Sender:
    url, token = (
        os.environ.get("AGENTHUB_URL", ""),
        os.environ.get("AGENTHUB_MACHINE_TOKEN", ""),
    )
    if not url or not token:
        print(
            "AGENTHUB_URL and AGENTHUB_MACHINE_TOKEN must be set; run "
            "`4genteam bridge register` once (it needs AGENTHUB_TOKEN, the user token) to "
            "issue this machine's token",
            file=sys.stderr,
        )
        raise SystemExit(EXIT_USAGE)
    return make_sender(url, token)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="4genteam bridge")
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run")
    run.add_argument("--interval", type=float, default=20.0)
    run.add_argument("--machine-id")
    run.add_argument("--once", action="store_true")
    once = sub.add_parser("once")
    once.add_argument("--print", action="store_true", dest="print_only")
    once.add_argument("--machine-id")
    register = sub.add_parser("register")
    register.add_argument("--machine-id")
    register.add_argument("--env-file")
    sub.add_parser("install-service")
    args = parser.parse_args(argv)
    if args.command == "run" and args.interval <= 0:
        parser.error("--interval must be positive")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    if args.command == "install-service":
        print(service_unit(), end="")
        return EXIT_OK
    if args.command == "register":
        return register_command(args)
    if args.command == "once" and args.print_only:
        bridge = build_bridge(args, lambda body: (200, b""))
        payload = bridge.build_payload()
        # The local record rides this dump and NOT the reported payload: the server refuses
        # unknown report fields, and this is what the operator's machine knows on its own - which
        # is only whether a seat has CHANGED since the cloud last confirmed it, not the cloud's
        # verdict, which the answer's own verdicts carry.
        payload["local_record"] = bridge.local_record(payload["seats"])
        print(json.dumps(payload, indent=2))
        return EXIT_OK
    bridge = build_bridge(args, sender_from_env())
    stop = threading.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stop.set())
    return bridge.run(stop, once=args.command == "once" or args.once)


if __name__ == "__main__":
    sys.exit(main())
