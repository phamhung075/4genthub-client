#!/usr/bin/env python3
"""Materialize 4genthub resolved seats for the OpenRig client.

OpenRig is the client and 4genthub is the cloud. A *resolved seat* is an
immutable snapshot of a seat's files plus its runtime and policy metadata,
addressed by the hash the cloud assigns to it. This script downloads that
snapshot and lays it out on disk for OpenRig::

    <out>/<room>/<seat>/<hash>/...    immutable files for that snapshot
    <out>/<room>/<seat>/policy.json   policy of the adopted snapshot
    <out>/<room>/<seat>/pinned.json   {"hash": ..., "path": ...} pin

Each hash gets its own directory, so pinned versions coexist. ``pull`` is
pinned by default: once a lock exists it keeps serving that directory and only
prints a notice when the cloud has a newer snapshot. Pass ``--update`` to adopt
the newer snapshot and move the pin. A pin whose directory is missing on disk
is an error; the script never silently falls back to another snapshot.

``rig`` materializes a whole room in one command. It fetches the room's rigspec,
pulls every listed seat with the same pinning rules as ``pull``, then builds::

    <out>/<room>/rig/rig.yaml         the server's RigSpec with each member's cwd (the room
                                      directory) and permission policy set
    <out>/<room>/.env                 link to the one DeepSeek key, for a room with a deepseek/ seat
    <out>/<room>/rig/agents/<seat>    link to that seat's pinned hash directory

The ``agents/<seat>`` link makes the ``local:agents/<seat>`` references in the
YAML resolve. The rig directory is built in a staging directory and swapped in
only after every seat is pinned, so a failed run leaves a previous rig directory
untouched. **Everything in the rig directory that the build did not write is carried
over** — a credential link, a delegation file, an operator's notes — and named on
stderr rather than deleted, and what the build wrote is derived from the directories
at swap time, so a file that lands WHILE the seats are being pulled survives too.
Print only ``rig:<path to rig.yaml>`` on stdout so the operator can run
``rig up <that path>``.

Offline use is an explicit operator choice. ``bundle`` produces a self-contained
``.rigbundle`` from the pinned snapshot; it is never used automatically as a
fallback. To run a seat offline, operate the bundle yourself::

    rig up <bundle> --target <dir>

Environment:
  AGENTHUB_URL    base URL of the 4genthub server, e.g. https://api.4genthub.com
  AGENTHUB_TOKEN  bearer token. It is sent as an Authorization header and is
                  never written to disk or printed.

Usage:
  4genteam sync pull ROOM SEAT [--out DIR] [--update]
  4genteam sync rig ROOM [--out DIR] [--update]
  4genteam sync bundle ROOM SEAT --rig-yaml PATH --rig-root DIR [--out-dir DIR]
  4genteam sync install-checker [--out DIR]
  4genteam sync switch ROOM SEAT [--runtime R] [--model M]
                       [--apply none|set-model|restart] [--reason TEXT]

``install-checker`` builds ``agenthub_go/cmd/seatcheck`` to ``<DIR>/bin/seatcheck`` (DIR is the
seat store) and links ``~/.local/bin/seatcheck`` to it. Seats run the bare name
``seatcheck send ...``, so it must resolve on the PATH the seats inherit; ``pull`` and ``rig``
fail until it resolves to the store's binary. The check reads the PATH the seats inherit: while
a tmux server runs that is its global PATH, and at cold start it reads the running rig daemon's
PATH from ``/proc/<pid>/environ`` (the daemon starts the first tmux server, so the first seat
inherits the daemon's environment). Only when neither is available does it fall back to the
operator's shell PATH and say so. If the daemon's PATH does not resolve ``seatcheck``, restart
the daemon from a shell where it resolves (``rig daemon stop``, ``rig daemon start``).

``switch`` changes the LLM of one seat: 4genthub records the occupant, then
OpenRig applies it. Fields not given keep their current cloud value. A model
change is applied with ``rig seat set-model``; ``--apply restart`` also stops and
freshly launches the seat (interrupts it and loses live context). OpenRig cannot
change a runtime in place, so a runtime change prints the manual steps instead.

Exit codes:
  0  success
  1  network, HTTP or response/JSON error
  2  usage or validation error
"""

import argparse
import importlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path, PurePosixPath

from . import paths

EXIT_OK = 0
EXIT_REMOTE = 1
EXIT_USAGE = 2


class SyncError(Exception):
    """A failure that maps to one of the documented process exit codes."""

    def __init__(self, message: str, code: int):
        super().__init__(message)
        self.code = code


# The per-seat policy module carries the ONE home resolution this tree has: real_home() answers
# from the passwd entry, because HOME inside a seat points at the seat's own state directory.
# Loaded by path for the same reason the policy document is - a sibling module is not importable
# by name from every caller, as this module's own tests demonstrate.


def load_seat_policy():
    """The per-seat policy module of this package."""
    return importlib.import_module("agenthub_client.seat_policy")


# The ONE home resolution, taken from the module above rather than restated: every default that
# names the account home is built from this, so a seat and an operator resolve the same store.
real_home = load_seat_policy().real_home

DEFAULT_OUT = real_home() / ".openrig" / "agenthub-seats"
AGENTHUB_GO_DIR = paths.GO_DIR
CHECKER_NAME = "seatcheck"

# The rendered omp MCP document inside a seat's render files, and the file name omp reads it from in
# the seat's own agent directory. Both measured in step A (MCP-OMP-STEP-A-MEASUREMENT.md): the
# agent-dir file and the project-root file COMPOSE when their server names differ, and the AGENT-DIR
# entry WINS on a collision - which is why this install writes exactly the rendered document and
# never a name the render does not own.
OMP_MCP_RENDER_PATH = PurePosixPath("runtime/omp-mcp.json")
OMP_MCP_INSTALL_NAME = ".mcp.json"

# The second rendered file, and it is NOT the same kind of thing as the first. `runtime/omp-mcp.json`
# is wholly the render's document, so it installs verbatim; `runtime/omp-config.yml` carries one
# setting for a file that is OMP'S OWN (the agent directory's `config.yml`, which may already hold
# other settings), so it is merged AT THE KEY LEVEL and never copied over the file.
OMP_CONFIG_RENDER_PATH = PurePosixPath("runtime/omp-config.yml")
OMP_CONFIG_INSTALL_NAME = "config.yml"

# The seat's guide document. The renderer writes it from the seat's guide blocks (packet 6 step 1),
# and omp loads it from the agent directory into every session, so the client installs it where the
# script that hand-wrote it used to. Wholly the render's document, so it installs VERBATIM.
AGENTS_MD_RENDER_PATH = PurePosixPath("AGENTS.md")
AGENTS_MD_INSTALL_NAME = "AGENTS.md"

# Where omp keeps per-seat state: the runner's --state-root default, plus the session name. A module
# constant like DEFAULT_OUT, so a test can point it at a scratch tree. A launch that overrides
# --state-root cannot be followed from here: the install would place the files under this root.
OMP_STATE_ROOT = real_home() / ".openrig" / "state" / "omp"

# The one home of the DeepSeek key. omp reads the launch directory's `.env`, so every room whose
# seats run a `deepseek/` model gets a `.env` link here: the secret keeps one home and a new room
# needs no copy of it.
DEEPSEEK_ENV = real_home() / ".config" / "deepseek" / "env"
DEEPSEEK_MODEL_PREFIX = "deepseek/"
# Every seat of a materialized room launches with this policy: it resolves to a full-bypass launch
# posture, without which omp's always-ask floor cancels approvals headlessly and the seat parks.
SEAT_PERMISSION_POLICY = "builtin:yolo"
SEATS_PATH = "/api/v2/openrig/seats"
ROOMS_PATH = "/api/v2/openrig/rooms"

NAME_RE = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9_-]*")
MODEL_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}")
RUNTIMES = ("claude-code", "codex", "agy", "omp")
APPLY_MODES = ("none", "set-model", "restart")
HASH_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")
# How often `respawn` re-reads the seat while waiting for the dead reading to hold.
RESPAWN_POLL_SECONDS = 5.0
# Default hold before respawning. The reason field is the only discriminator, and it is
# RUNTIME-DEPENDENT: a just-launched agy seat reads exactly like a dead one (`unknown` +
# `no_runtime_hook`) until its hook attaches (observed ~15s), while an omp seat with no activity
# yet reports reason null and stays unknown. So one reading is never enough, and this hold must
# not be shortened without re-measuring per runtime.
DEFAULT_RESPAWN_AFTER_SECONDS = 30.0


def require_env(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        raise SyncError(f"{name} is not set", EXIT_USAGE)
    return value


def validate_name(kind: str, value: str) -> str:
    if not isinstance(value, str) or not NAME_RE.fullmatch(value):
        raise SyncError(
            f"invalid {kind} name: {value!r} (expected [a-zA-Z0-9][a-zA-Z0-9_-]*)",
            EXIT_USAGE,
        )
    return value


def validate_hash(value: str) -> str:
    """A hash becomes a directory name, so it must not escape the seat store."""
    if not isinstance(value, str) or not HASH_RE.fullmatch(value) or ".." in value:
        raise SyncError(f"server returned an unsafe seat hash: {value!r}", EXIT_USAGE)
    return value


def safe_relative(path: str) -> PurePosixPath:
    """Reject file paths that could escape the snapshot directory."""
    if (
        not isinstance(path, str)
        or not path
        or path.startswith("/")
        or path.startswith("\\")
        or "\\" in path
    ):
        raise SyncError(f"server returned an unsafe file path: {path!r}", EXIT_USAGE)
    parts = path.split("/")
    if any(part in ("", ".", "..") for part in parts):
        raise SyncError(f"server returned an unsafe file path: {path!r}", EXIT_USAGE)
    return PurePosixPath(*parts)


def request_json(
    method: str, base_url: str, token: str, path: str, payload: dict | None = None
) -> dict:
    headers = {"Authorization": f"Bearer {token}", "Accept": "application/json"}
    data = None
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(
        base_url.rstrip("/") + path, data=data, headers=headers, method=method
    )
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            body = json.load(response)
    except urllib.error.HTTPError as err:
        detail = err.read().decode("utf-8", "replace")[:300]
        raise SyncError(
            f"{method} {path} failed: HTTP {err.code} {detail}", EXIT_REMOTE
        )
    except urllib.error.URLError as err:
        raise SyncError(f"{method} {path} failed: {err.reason}", EXIT_REMOTE)
    except (TimeoutError, OSError) as err:
        raise SyncError(f"{method} {path} failed: {err}", EXIT_REMOTE)
    except json.JSONDecodeError as err:
        raise SyncError(f"{method} {path} returned invalid JSON: {err}", EXIT_REMOTE)

    if not isinstance(body, dict):
        raise SyncError(f"{method} {path} returned a malformed response", EXIT_REMOTE)
    return body


def get_json(base_url: str, token: str, path: str) -> dict:
    return request_json("GET", base_url, token, path)


def fetch_seat(base_url: str, token: str, room: str, seat: str) -> dict:
    path = f"{SEATS_PATH}/{room}/{seat}"
    body = get_json(base_url, token, path)
    if body.get("success") is not True:
        raise SyncError(f"GET {path} returned an error response", EXIT_REMOTE)
    resolved = body.get("resolved_seat")
    if not isinstance(resolved, dict):
        raise SyncError(f"GET {path} response has no resolved_seat", EXIT_REMOTE)
    return resolved


def fetch_rigspec(base_url: str, token: str, room: str) -> dict:
    path = f"{ROOMS_PATH}/{room}/rigspec"
    body = get_json(base_url, token, path)
    if body.get("success") is not True:
        raise SyncError(f"GET {path} returned an error response", EXIT_REMOTE)
    rigspec = body.get("rigspec")
    if not isinstance(rigspec, dict):
        raise SyncError(f"GET {path} response has no rigspec", EXIT_REMOTE)
    return rigspec


def extract_files(resolved: dict) -> list[tuple[PurePosixPath, str]]:
    files = resolved.get("files")
    if not isinstance(files, list):
        raise SyncError("resolved_seat has no files list", EXIT_REMOTE)
    entries: list[tuple[PurePosixPath, str]] = []
    for file in files:
        if (
            not isinstance(file, dict)
            or not isinstance(file.get("path"), str)
            or not isinstance(file.get("content"), str)
        ):
            raise SyncError(
                "resolved_seat contains a malformed file entry", EXIT_REMOTE
            )
        entries.append((safe_relative(file["path"]), file["content"]))
    return entries


def read_lock(lock_path: Path) -> dict | None:
    if not lock_path.is_file():
        return None
    try:
        data = json.loads(lock_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as err:
        raise SyncError(f"cannot read lock file {lock_path}: {err}", EXIT_USAGE)
    if (
        not isinstance(data, dict)
        or not isinstance(data.get("hash"), str)
        or not isinstance(data.get("path"), str)
    ):
        raise SyncError(f"lock file {lock_path} is malformed", EXIT_USAGE)
    return data


def write_json_atomic(path: Path, payload) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(temporary, path)
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def materialize(hash_dir: Path, entries: list[tuple[PurePosixPath, str]]) -> None:
    """Write a snapshot through a staging directory, then move it into place."""
    if hash_dir.is_dir():
        return
    hash_dir.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=f".{hash_dir.name}.", dir=hash_dir.parent))
    try:
        for relative, content in entries:
            destination = staging.joinpath(*relative.parts)
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text(content, encoding="utf-8")
        staging.rename(hash_dir)
    except BaseException:
        shutil.rmtree(staging, ignore_errors=True)
        raise


def pull_seat(
    base_url: str, token: str, room: str, seat: str, out: Path, update: bool
) -> Path:
    """Pin one seat and return the directory of the adopted snapshot.

    The lock is the pin: without ``update`` a newer cloud snapshot only prints a
    notice, and a pin whose directory is missing is a loud failure.
    """
    resolved = fetch_seat(base_url, token, room, seat)
    if resolved.get("room") != room or resolved.get("seat") != seat:
        raise SyncError(
            "server returned a snapshot for a different room/seat", EXIT_REMOTE
        )
    fetched_hash = validate_hash(resolved.get("hash"))
    entries = extract_files(resolved)
    policy = resolved.get("policy", {})

    seat_dir = out / room / seat
    seat_dir.mkdir(parents=True, exist_ok=True)
    lock_path = seat_dir / "pinned.json"
    lock = read_lock(lock_path)

    if lock is not None and not update and fetched_hash != lock["hash"]:
        pinned_hash = validate_hash(lock["hash"])
        pinned_dir = seat_dir / pinned_hash
        if not pinned_dir.is_dir():
            raise SyncError(
                f"pinned seat directory is missing: {pinned_dir} "
                f"(refusing silent fallback; fix the store or remove {lock_path})",
                EXIT_USAGE,
            )
        print(
            f"newer snapshot available: {fetched_hash} (run with --update to adopt)",
            file=sys.stderr,
        )
        return pinned_dir

    hash_dir = seat_dir / fetched_hash
    materialize(hash_dir, entries)
    write_json_atomic(seat_dir / "policy.json", policy)
    if lock is None or lock["hash"] != fetched_hash or update:
        write_json_atomic(lock_path, {"hash": fetched_hash, "path": str(hash_dir)})
    return hash_dir


def checker_binary(out: Path) -> Path:
    return out / "bin" / CHECKER_NAME


def checker_link() -> Path:
    return real_home() / ".local" / "bin" / CHECKER_NAME


TMUX_TIMEOUT_SECONDS = 5


def tmux_global_path() -> str | None:
    """The PATH new seats inherit (tmux's global environment); None without a tmux server."""
    try:
        result = subprocess.run(
            ["tmux", "show-environment", "-g", "PATH"],
            capture_output=True,
            text=True,
            timeout=TMUX_TIMEOUT_SECONDS,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0 or not result.stdout.startswith("PATH="):
        return None
    return result.stdout.strip()[len("PATH=") :]


def openrig_daemon_port() -> str | None:
    """The rig daemon port from OPENRIG_PORT, else the port in OPENRIG_URL."""
    port = os.environ.get("OPENRIG_PORT", "").strip()
    if port.isdigit():
        return port
    match = re.search(r":(\d+)(?:/|$)", os.environ.get("OPENRIG_URL", ""))
    return match.group(1) if match else None


def openrig_daemon_pid() -> int | None:
    """The rig daemon pid listening on the OpenRig port, or None when it cannot be found.

    The daemon starts the first tmux server, so at cold start the first seat inherits the
    daemon's environment; its PATH is readable from /proc while the daemon runs.
    """
    port = openrig_daemon_port()
    if port is None:
        return None
    try:
        result = subprocess.run(
            ["ss", "-ltnp"],
            capture_output=True,
            text=True,
            timeout=TMUX_TIMEOUT_SECONDS,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    for line in result.stdout.splitlines():
        if f":{port} " in line or f":{port}\t" in line:
            match = re.search(r"pid=(\d+)", line)
            if match:
                return int(match.group(1))
    return None


def proc_env_path(pid: int) -> str | None:
    """The PATH of process pid from /proc/<pid>/environ, or None when unreadable."""
    try:
        raw = Path(f"/proc/{pid}/environ").read_bytes()
    except OSError:
        return None
    for entry in raw.split(b"\0"):
        if entry.startswith(b"PATH="):
            return entry[len(b"PATH=") :].decode("utf-8", "surrogateescape")
    return None


SHELL_PATH_SOURCE = (
    "shell PATH (no tmux server and no readable rig daemon PATH were found, so no seat "
    "exists yet)"
)
DAEMON_PATH_SOURCE = "rig daemon PATH (the daemon starts the first tmux server, so the first seat inherits it)"


def seat_path() -> tuple[str, str]:
    """The PATH to check and its source: tmux global, else the daemon's, else this shell's.

    At cold start (no tmux server) the first seat inherits the rig daemon's environment, so
    the daemon's PATH is read from its /proc entry instead of assuming the operator's shell.
    """
    tmux_path = tmux_global_path()
    if tmux_path is not None:
        return tmux_path, "tmux global PATH"
    pid = openrig_daemon_pid()
    if pid is not None:
        daemon_path = proc_env_path(pid)
        if daemon_path:
            return daemon_path, DAEMON_PATH_SOURCE
    return os.environ.get("PATH", ""), SHELL_PATH_SOURCE


def resolve_checker(out: Path) -> bool:
    """True when the bare name ``seatcheck`` resolves to the seat store's binary."""
    path, source = seat_path()
    if source == SHELL_PATH_SOURCE:
        print(f"note: checked {CHECKER_NAME} on the {source}", file=sys.stderr)
    found = shutil.which(CHECKER_NAME, path=path)
    return found is not None and os.path.realpath(found) == os.path.realpath(
        checker_binary(out)
    )


PATH_LIMIT = (
    "seats inherit the tmux global PATH, which is what this checks while a tmux server "
    "runs (only the default tmux socket is queried); at cold start it reads the PATH of "
    "the running rig daemon (the daemon starts the first tmux server, so the first seat "
    "inherits the daemon's environment). If the daemon's PATH does not resolve "
    f"{CHECKER_NAME}, restart the daemon from a shell where it resolves "
    "(`rig daemon stop`, `rig daemon start`)"
)


def describe_found() -> str:
    path, source = seat_path()
    found = shutil.which(CHECKER_NAME, path=path)
    where = f"{found} -> {os.path.realpath(found)}" if found else "not on PATH"
    return f"{where}, on the {source}"


def require_checker(out: Path) -> None:
    if not resolve_checker(out):
        raise SyncError(
            f"{CHECKER_NAME} does not resolve to {checker_binary(out)} on PATH "
            f"(found: {describe_found()}); "
            "run `4genteam sync install-checker` and put "
            f"{checker_link().parent} on PATH ({PATH_LIMIT})",
            EXIT_USAGE,
        )


def cmd_install_checker(args: argparse.Namespace) -> None:
    out = (args.out or DEFAULT_OUT).expanduser().resolve()
    binary = checker_binary(out)
    binary.parent.mkdir(parents=True, exist_ok=True)
    cache, tmp = AGENTHUB_GO_DIR / ".gocache", AGENTHUB_GO_DIR / ".gotmp"
    for directory in (cache, tmp):
        directory.mkdir(exist_ok=True)
    try:
        subprocess.run(
            [
                "go",
                "build",
                "-ldflags",
                f"-X main.installedPinsDir={out}",
                "-o",
                str(binary),
                "./cmd/seatcheck",
            ],
            cwd=AGENTHUB_GO_DIR,
            env={**os.environ, "GOCACHE": str(cache), "TMPDIR": str(tmp)},
            check=True,
        )
    except FileNotFoundError:
        raise SyncError(
            "go is not installed; cannot build the seat checker", EXIT_USAGE
        )
    except subprocess.CalledProcessError as err:
        raise SyncError(f"go build failed with exit code {err.returncode}", EXIT_REMOTE)
    place_agent(binary, checker_link())
    if not resolve_checker(out):
        raise SyncError(
            f"installed {binary} and linked {checker_link()}, but {CHECKER_NAME} does not "
            f"resolve to it on PATH (found: {describe_found()}); "
            f"add {checker_link().parent} to PATH ({PATH_LIMIT})",
            EXIT_USAGE,
        )
    print(f"checker:{binary}")


def cmd_pull(args: argparse.Namespace) -> None:
    room = validate_name("room", args.room)
    seat = validate_name("seat", args.seat)
    out = (args.out or DEFAULT_OUT).expanduser().resolve()
    require_checker(out)
    base_url = require_env("AGENTHUB_URL")
    token = require_env("AGENTHUB_TOKEN")

    hash_dir = pull_seat(base_url, token, room, seat, out, args.update)
    print(f"path:{hash_dir}")


def remove_path(path: Path) -> None:
    if path.is_symlink() or path.is_file():
        path.unlink()
    elif path.is_dir():
        shutil.rmtree(path)


def place_agent(source: Path, target: Path) -> None:
    """Point ``target`` at ``source`` (a file or a directory) with a relative symlink."""
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.parent / f".{target.name}.{os.getpid()}.tmp"
    remove_path(temporary)
    try:
        os.symlink(os.path.relpath(source, target.parent), temporary)
    except OSError as err:
        raise SyncError(f"cannot link {target} to {source}: {err}", EXIT_USAGE) from err
    if target.is_dir() and not target.is_symlink():
        remove_path(target)
    os.replace(temporary, target)


def materialize_agent(source: Path, seat_dir: Path, target: Path) -> None:
    """Materialize the seat's agent directory at ``target`` from the pinned snapshot.

    The snapshot (``source``) holds only the rendered files. A bundle built from this rig
    root copies what sits here, so the seat's ``policy.json`` and ``pinned.json`` are
    copied in from ``seat_dir``: ``policy.json`` is the file ``seatcheck`` reads at runtime
    (``<pins>/<rig>/<member>/policy.json``), and without it an offline bundle seat cannot
    decide or audit (G4).
    """
    remove_path(target)
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(source, target, symlinks=False)
    for name in ("policy.json", "pinned.json"):
        candidate = seat_dir / name
        if candidate.is_file():
            shutil.copy2(candidate, target / name)


def swap_dir(staging: Path, target: Path) -> list[str]:
    """Move ``staging`` onto ``target``, restoring ``target`` if the move fails.

    Everything the caller did not write is carried over, and **what the caller wrote is derived
    HERE, from the two directories**: the entries of the old ``target`` that the new one does not
    have are moved back in, and their names are returned so the caller can say what it kept. A
    name the new directory already has is left alone — the caller's own content wins. Entries are
    moved rather than copied, so a symlink stays a symlink.

    Deriving this at swap time rather than from a list read earlier buys two things. It closes a
    real window, and the window is MEASURED rather than assumed: ``cmd_rig`` reads the old
    directory AFTER pulling the seats and BEFORE staging materializes them, so the gap is the
    materialization — a copy per seat — between that read and this swap. A file that lands in the
    gap is in neither the old list nor the new directory, so it used to be deleted by a build that
    promises the operator their files survive. (A file that lands during the PULLS survives under
    both the old and the new code; it was never the open phase.) And the derivation removes the
    need to enumerate what the build owns, an enumeration a new rendered artifact would silently
    invalidate.
    """
    kept: list[str] = []
    backup = None
    if target.exists() or target.is_symlink():
        backup = Path(
            tempfile.mkdtemp(prefix=f".{target.name}.old.", dir=target.parent)
        )
        backup.rmdir()
        target.rename(backup)
    try:
        staging.rename(target)
    except BaseException:
        if backup is not None:
            backup.rename(target)
        raise
    if backup is not None:
        written = {entry.name for entry in target.iterdir()}
        for entry in sorted(backup.iterdir(), key=lambda path: path.name):
            if entry.name in written:
                continue
            entry.rename(target / entry.name)
            kept.append(entry.name)
        shutil.rmtree(backup, ignore_errors=True)
    return kept


def cmd_rig(args: argparse.Namespace) -> None:
    room = validate_name("room", args.room)
    out = (args.out or DEFAULT_OUT).expanduser().resolve()
    require_checker(out)
    base_url = require_env("AGENTHUB_URL")
    token = require_env("AGENTHUB_TOKEN")

    rigspec = fetch_rigspec(base_url, token, room)
    if rigspec.get("name") != room:
        raise SyncError("server returned a rigspec for a different room", EXIT_REMOTE)
    yaml_text = rigspec.get("yaml")
    if not isinstance(yaml_text, str):
        raise SyncError("rigspec has no yaml text", EXIT_REMOTE)
    listed = rigspec.get("seats")
    if not isinstance(listed, list) or not listed:
        raise SyncError("rigspec has no seats", EXIT_REMOTE)

    seats: list[str] = []
    for entry in listed:
        if not isinstance(entry, dict) or not isinstance(entry.get("seat"), str):
            raise SyncError("rigspec contains a malformed seat entry", EXIT_REMOTE)
        if not isinstance(entry.get("hash"), str):
            raise SyncError("rigspec seat entry has no hash", EXIT_REMOTE)
        seat = validate_name("seat", entry["seat"])
        if seat in seats:
            raise SyncError(f"rigspec lists seat {seat!r} twice", EXIT_REMOTE)
        seats.append(seat)

    pinned: dict[str, Path] = {}
    for seat in seats:
        try:
            pinned[seat] = pull_seat(base_url, token, room, seat, out, args.update)
        except SyncError as err:
            raise SyncError(
                f"seat {seat} could not be pulled: {err}", EXIT_REMOTE
            ) from err

    # Resolve and validate every omp MCP install BEFORE the build writes anything, so a rig whose
    # seats have not been launched yet refuses with nothing half-applied.
    omp_installs, render_gaps = omp_render_installs(pinned, room, yaml_text)

    room_dir = out / room
    room_dir.mkdir(parents=True, exist_ok=True)
    rig_dir = room_dir / "rig"
    staging = Path(tempfile.mkdtemp(prefix=".rig.", dir=room_dir))
    try:
        (staging / "rig.yaml").write_text(launch_spec(yaml_text, room_dir), encoding="utf-8")
        agents = staging / "agents"
        agents.mkdir()
        for seat in seats:
            materialize_agent(pinned[seat], out / room / seat, agents / seat)
        # The swap carries over whatever the build did not write, derived from the directories at
        # that moment - so a file placed in the rig directory WHILE the seats were being pulled
        # survives too, which a list read before the pull could not promise.
        kept = swap_dir(staging, rig_dir)
    except BaseException:
        shutil.rmtree(staging, ignore_errors=True)
        raise

    if kept:
        print(
            f"kept {len(kept)} file(s) the build did not create in {rig_dir}: "
            f"{', '.join(kept)}",
            file=sys.stderr,
        )
    # The rendered omp MCP documents, once the build is in place. Written VERBATIM and only when
    # they differ, so a rebuild is not a diff; the operator's rig-root .mcp.json is never opened.
    for rendered, destination, mode in omp_installs:
        # A rig that has never been up has no agent directory yet; omp adopts the one placed here at
        # its first launch, so the render installs before the seat exists. Created HERE, at write
        # time, rather than in the resolve pass: that pass runs before the build, so a later refusal
        # (a pre-existing .env, a missing key file, a failed pull) would otherwise leave empty
        # directories under the LIVE state root for a run that installed nothing.
        destination.parent.mkdir(parents=True, exist_ok=True)
        if mode == "verbatim":
            changed = write_bytes_if_changed(rendered.read_bytes(), destination)
        else:
            changed = merge_config_key(rendered.read_bytes(), destination)
        if changed:
            print(f"installed {destination}", file=sys.stderr)
    for seat, missing in render_gaps:
        print(
            f"warning: {room}/{seat} rendered no {missing}, so that seat gets HALF of its omp MCP "
            "setup - a document without the startup setting mounts servers omp may not wait for, "
            "and the setting without a document waits for servers the seat has none of. A render "
            "that predates the setting produces the first case.",
            file=sys.stderr,
        )
    # The per-seat policy, from the one module that defines the rules. Applied AFTER the render's
    # installs so the shared `mcp` keys it also sets are already in place; the merge is key-level
    # either way, so the order is converging rather than load-bearing.
    #
    # A rig the table does not govern is skipped SILENTLY: the table is a registry of the rigs whose
    # seats have roles, and a rig outside it (an operator's scratch rig, another team) is not this
    # module's business - its own `apply --rig` refuses loudly when asked about such a rig, which is
    # where the operator learns it. Within a governed rig, a seat the table omits is a real gap and
    # is reported, because that is the shape drift takes in a rig that HAS a policy.
    policy_module = load_seat_policy()
    if room in getattr(policy_module, "SEAT_ROLES", {}):
        for pod_id, member_id in rig_members(yaml_text):
            document = policy_document(policy_module, room, member_id)
            if document is None:
                print(
                    f"warning: {room}/{member_id} has no role in "
                    f"seat_policy, so no per-seat policy was delivered. That "
                    "table is the single definition of the rules and an unlisted seat has no "
                    "default, so this seat is running unpoliced.",
                    file=sys.stderr,
                )
                continue
            agent_dir = omp_agent_dir(session_name(pod_id, member_id, room))
            if not agent_dir.is_dir():
                print(
                    f"warning: no omp agent directory at {agent_dir}, so {room}/{member_id} did not "
                    "get the per-seat policy. The directory appears once the seat has launched under "
                    "omp, so run the client again after that; a seat on another runtime never has "
                    "one.",
                    file=sys.stderr,
                )
                continue
            if apply_policy(document, agent_dir / OMP_CONFIG_INSTALL_NAME):
                print(
                    f"applied the per-seat policy {agent_dir / OMP_CONFIG_INSTALL_NAME}",
                    file=sys.stderr,
                )
    print(f"rig:{rig_dir / 'rig.yaml'}")


def session_name(pod_id: str, member: str, rig: str) -> str:
    """The session name the runner gives a member: ``<pod>-<member>@<rig>``.

    Corroborated rather than assumed: fourteen live state directories under this machine's omp state
    root match the shape, four of them from a different rig (``of4room-alpha@of4room``), and this
    seat's own ``rig whoami`` reports ``logicalId`` ``<pod>.<member>`` against ``sessionName``
    ``<pod>-<member>@<rig>``.
    """
    return f"{pod_id}-{member}@{rig}"


def omp_agent_dir(session: str) -> Path:
    """Where omp reads one seat's own MCP config: ``<state root>/<session>/agent``."""
    return OMP_STATE_ROOT / session / "agent"


def write_bytes_if_changed(data: bytes, target: Path) -> bool:
    """Write ``data`` to ``target`` only when it differs; True when the file actually changed.

    The render is deterministic, so a rebuild that produces the same document must leave the file -
    and its mtime - exactly as it was: a rebuild is not a diff.
    """
    if target.is_file():
        try:
            if target.read_bytes() == data:
                return False
        except OSError:
            pass
    target.write_bytes(data)
    return True


def _merged_config(rendered: dict, existing: dict, owned: tuple[str, ...] = ()) -> dict:
    """``existing`` with the RENDERED keys set on top: mappings merge, leaves overwrite.

    ``owned`` names the top-level keys the rendered document owns WHOLE: those are set rather than
    merged, because a list or a map inside them that the document no longer carries must not linger
    in the file. The per-seat policy uses it for ``bash`` and ``tools``, the two namespaces it
    defines outright; everything else merges key by key.
    """
    out = dict(existing)
    for key, value in rendered.items():
        if key in owned:
            out[key] = value
        elif isinstance(value, dict) and isinstance(out.get(key), dict):
            out[key] = _merged_config(value, out[key])
        else:
            out[key] = value
    return out


def merge_config_key(fragment: bytes, target: Path) -> bool:
    """Set the rendered keys in ``target``, preserving every other key. True when the file changed.

    THE MERGE IS THE POINT. ``target`` is OMP'S OWN settings file for that agent directory, not a
    document the render owns: it may already hold settings an operator or another seat put there.
    **Copying the rendered file over it would delete them** - the same destructive failure the
    composition rule prevents one layer down - so the rendered keys are SET and the rest is kept.
    The runtime's own `omp config set` was measured to merge at exactly this level; this is the
    equivalent, done without shelling out.

    IDEMPOTENCE IS SEMANTIC, NOT TEXTUAL: if the target already carries every rendered key at the
    rendered value, nothing is written and the mtime is untouched, so a rebuild is not a diff. A
    file that does NOT exist yet is written with the render's own bytes, so a fresh seat gets exactly
    what the renderer produced.

    NAMED LIMITATION: when a write IS needed the file is re-serialised, so comments and blank lines
    in the target are not preserved. The alternative - shelling out to the runtime's verb - rewrites
    the file the same way, and today every seat already carries the key, so the rewrite is the rare
    path rather than the normal one.
    """
    try:
        import yaml
    except ImportError as err:  # pragma: no cover - environment dependent
        raise SyncError(
            f"PyYAML is required to merge the rendered omp config: {err}", EXIT_USAGE
        ) from err
    try:
        rendered = yaml.safe_load(fragment.decode("utf-8"))
    except (UnicodeDecodeError, yaml.YAMLError) as err:
        raise SyncError(f"the rendered omp config is not readable YAML: {err}", EXIT_USAGE) from err
    if not isinstance(rendered, dict):
        raise SyncError("the rendered omp config is not a mapping", EXIT_USAGE)

    if not target.is_file():
        target.write_bytes(fragment)
        return True

    try:
        existing = yaml.safe_load(target.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as err:
        raise SyncError(
            f"refusing to set the rendered keys in {target}: it is not readable YAML ({err}). "
            "This merge must not clobber a file it cannot read.",
            EXIT_USAGE,
        ) from err
    if existing is None:
        existing = {}
    if not isinstance(existing, dict):
        raise SyncError(
            f"refusing to set the rendered keys in {target}: its top level is not a mapping",
            EXIT_USAGE,
        )

    merged = _merged_config(rendered, existing)
    if merged == existing:
        return False
    target.write_text(
        yaml.safe_dump(merged, sort_keys=False, default_flow_style=False), encoding="utf-8"
    )
    return True


# --- the per-seat policy, delivered rather than applied by hand ------------------------------
#
# THE RULES HAVE ONE DEFINITION: agenthub_client.seat_policy - its SEAT_ROLES table and its
# policy_for()/render_config() build each seat's config. This client IMPORTS that module and applies
# its document to the seat's agent-dir config.yml; it does not restate a single rule, because two
# copies of a policy is how a policy drifts.
#
# THE SEAM, stated because it is the choice rather than an implementation detail: THE CLIENT applies
# the policy; the RENDER does not emit it. The rules are Python and the client is Python, so the
# import IS the shared definition, while the render would have to carry the tables into Go - the
# second copy. The table is also a LOCAL fact (rig + seat -> role, resolved per machine), and the
# agent directory the document belongs to is the client's own install target, one layer along from
# the MCP document it already installs there.
#
# WHAT A SEAT OUTSIDE THE TABLE GETS: nothing, reported. The table's own stance is that an unlisted
# rig or seat is an error rather than a permissive default, so the client says so per seat and
# continues installing the rest - a rig with one unlisted seat must not stop the others syncing.
def policy_document(policy_module, rig: str, member: str):
    """The seat's policy document, or None when the table has no role for it."""
    roles = getattr(policy_module, "SEAT_ROLES", {}).get(rig)
    if not roles or member not in roles:
        return None
    return policy_module.render_config(member, roles[member], rig)


def apply_policy(document: str, target: Path) -> bool:
    """Apply the policy document to ``target``, the runtime's own config.yml. True if it changed.

    THE POLICY OWNS ITS NAMESPACES: ``bash`` and ``tools`` are set whole, because a deny the policy
    dropped must not linger in the file. ``mcp`` is SHARED - the render puts ``startupTimeoutMs``
    there - so it merges key by key, and any other key the runtime's file carries survives.

    When the file holds nothing but policy keys, the policy's own bytes are written VERBATIM, so the
    document keeps its header comment; a file that carries other keys is merged and re-serialised,
    which costs its comments (named here rather than discovered later).
    """
    try:
        import yaml
    except ImportError as err:  # pragma: no cover - environment dependent
        raise SyncError(f"PyYAML is required to apply the per-seat policy: {err}", EXIT_USAGE) from err
    if not target.is_file():
        target.write_text(document, encoding="utf-8")
        return True
    text = target.read_text(encoding="utf-8")
    try:
        rendered = yaml.safe_load(document)
        existing = yaml.safe_load(text) or {}
    except yaml.YAMLError as err:
        raise SyncError(
            f"refusing to apply the per-seat policy in {target}: it is not readable YAML ({err}).",
            EXIT_USAGE,
        ) from err
    if not isinstance(rendered, dict) or not isinstance(existing, dict):
        raise SyncError(
            f"refusing to apply the per-seat policy in {target}: its top level is not a mapping",
            EXIT_USAGE,
        )
    if set(existing) <= set(rendered):
        if text == document:
            return False
        target.write_text(document, encoding="utf-8")
        return True
    merged = _merged_config(rendered, existing, owned=("bash", "tools"))
    if merged == existing:
        return False
    target.write_text(
        yaml.safe_dump(merged, sort_keys=False, default_flow_style=False), encoding="utf-8"
    )
    return True


def launch_spec(rig_yaml_text: str, room_dir: Path) -> str:
    """The rig spec a launch needs: the server's spec with every member's ``cwd`` and policy set.

    ``cwd`` is the room directory, which no build replaces. The server's ``.`` would resolve to the
    ``rig`` directory, and ``swap_dir`` replaces that directory on every build, so a seat launched
    from it kept a deleted working directory. The ``.env`` link that carries the DeepSeek key sits
    in the same directory, and is created here for a room with a ``deepseek/`` seat.
    """
    import yaml  # rig_members has already shown a spec needs it

    members = []
    rig_members(rig_yaml_text)  # a spec that cannot be read raises here
    spec = yaml.safe_load(rig_yaml_text)
    for pod in spec.get("pods") or []:
        members.extend(member for member in (pod or {}).get("members") or [] if member)
    for member in members:
        member["cwd"] = str(room_dir)
        member["permission_policy"] = SEAT_PERMISSION_POLICY
    if any(str(member.get("model") or "").startswith(DEEPSEEK_MODEL_PREFIX) for member in members):
        link_provider_key(room_dir)
    return yaml.safe_dump(spec, sort_keys=False)


def link_provider_key(room_dir: Path) -> None:
    """Link ``<room_dir>/.env`` to the one DeepSeek key file, replacing only this client's OWN link.

    A regular file at that path is not ours: it is REFUSED and named rather than replaced, which is
    the same rule the rig directory keeps for the files the build did not create.
    """
    if not DEEPSEEK_ENV.is_file():
        raise SyncError(
            f"the room has a {DEEPSEEK_MODEL_PREFIX} seat and {DEEPSEEK_ENV} does not exist: "
            "omp reads the key from the launch directory's .env",
            EXIT_USAGE,
        )
    link = room_dir / ".env"
    if link.is_symlink():
        if link.resolve() == DEEPSEEK_ENV.resolve():
            return
        # Our own link, aimed at a key file that moved: retargeting is what the link is for.
        link.unlink()
    elif link.exists():
        raise SyncError(
            f"{link} already exists and is not a symlink: refusing to replace a file this client "
            f"did not create. Move it aside, or point it at {DEEPSEEK_ENV}, to use the one key.",
            EXIT_USAGE,
        )
    link.symlink_to(DEEPSEEK_ENV)


def rig_members(rig_yaml_text: str) -> list[tuple[str, str]]:
    """The ``(pod id, member id)`` pairs of a rig spec, in spec order; a bad spec raises.

    One definition, used by both passes that walk the spec: the MCP install and the per-seat policy.
    """
    try:
        import yaml  # lazy: only a caller that has a spec needs it
    except ImportError as err:  # pragma: no cover - environment dependent
        raise SyncError(f"PyYAML is required to read the rig spec: {err}", EXIT_USAGE) from err
    try:
        spec = yaml.safe_load(rig_yaml_text)
    except yaml.YAMLError as err:
        raise SyncError(f"cannot read the rig spec: {err}", EXIT_USAGE) from err
    if not isinstance(spec, dict):
        raise SyncError("cannot read the rig spec", EXIT_USAGE)
    pairs: list[tuple[str, str]] = []
    for pod in spec.get("pods") or []:
        pod_id = str((pod or {}).get("id") or "")
        for member in (pod or {}).get("members") or []:
            member_id = str((member or {}).get("id") or "")
            if pod_id and member_id:
                pairs.append((pod_id, member_id))
    return pairs


def omp_render_installs(
    snapshots: dict[str, Path], rig: str, rig_yaml_text: str
) -> tuple[list[tuple[Path, Path, str]], list[tuple[str, str]]]:
    """Resolve ``(rendered, destination, mode)`` for every seat's render, plus the half-renders.

    THREE files, and the first two are one setup while the third is independent:

    - ``runtime/omp-mcp.json`` -> ``<agent dir>/.mcp.json``, mode ``verbatim``: the file is wholly
      the render's document, and its ``Bearer ${AGENTHUB_TOKEN}`` stays literal text because the
      renderer resolves only the platform URL and leaves the rest for the runtime (step A measured
      the runtime expanding it, and sending the literal when the variable is unset).
    - ``runtime/omp-config.yml`` -> ``<agent dir>/config.yml``, mode ``merge``: one setting for a
      file that is omp's own, so the keys are SET and everything else in it survives.
    - ``AGENTS.md`` -> ``<agent dir>/AGENTS.md``, mode ``verbatim`` (packet 6 step 1): the seat's
      guide document, which the renderer now writes from the seat's guide blocks instead of a script
      writing it by hand. It is INDEPENDENT of the pair above - a seat with guides and no mcp block
      gets this file and neither of the others - which is why it is not part of the half-render check.

    A seat with no ``mcp`` block renders neither of the first two, and a seat whose resolution
    carries no guide renders no ``AGENTS.md``: it appears in no triple and nothing is written for it,
    because absence is the signal rather than an empty file.

    **Every destination is resolved and validated before anything is written**, which is why this is
    separate from the writing loop: a rig whose seats have not been launched yet must refuse without
    having installed a file for the seats that happen to be up.

    The second return value names the seats whose render carries ONE of the two files: that is the
    state the caller must not leave silent, because a document without the config mounts servers the
    runtime may not wait for, and a config without the document waits for servers the seat has no
    document for.
    """
    renders_by_seat: dict[str, dict[str, Path]] = {}
    for seat, snapshot in snapshots.items():
        present = {
            mode: snapshot / path
            for mode, path in (
                ("verbatim", OMP_MCP_RENDER_PATH),
                ("merge", OMP_CONFIG_RENDER_PATH),
                ("agents", AGENTS_MD_RENDER_PATH),
            )
            if (snapshot / path).is_file()
        }
        if present:
            renders_by_seat[seat] = present
    if not renders_by_seat:
        return [], []

    installs: list[tuple[Path, Path, str]] = []
    gaps: list[tuple[str, str]] = []
    for pod_id, member_id in rig_members(rig_yaml_text):
        if member_id not in renders_by_seat:
            continue
        present = renders_by_seat[member_id]
        agent_dir = omp_agent_dir(session_name(pod_id, member_id, rig))
        if "verbatim" in present:
            installs.append((present["verbatim"], agent_dir / OMP_MCP_INSTALL_NAME, "verbatim"))
        if "merge" in present:
            installs.append((present["merge"], agent_dir / OMP_CONFIG_INSTALL_NAME, "merge"))
        if "agents" in present:
            # Independent of the MCP pair: a seat with guides and no mcp block gets only this one.
            installs.append((present["agents"], agent_dir / AGENTS_MD_INSTALL_NAME, "verbatim"))
        mcp_halves = [mode for mode in ("verbatim", "merge") if mode in present]
        if len(mcp_halves) == 1:
            missing = OMP_CONFIG_RENDER_PATH if "verbatim" in present else OMP_MCP_RENDER_PATH
            gaps.append((member_id, str(missing)))
    return installs, gaps


def unpinned_seats(rig_yaml: Path, rig_root: Path) -> list[tuple[str, str]]:
    """Seats of a rig spec whose agent directory in the rig root carries no usable pinned policy.

    A bundle copies ``<rig-root>/agents/<agent>/`` verbatim, and ``seatcheck`` reads its policy
    from the pin that directory is supposed to hold - so a bundle built from a rig root without it
    holds seats that cannot decide or audit offline. Until now the build said nothing about that:
    ``rig bundle create`` answers "Bundle created" and the artifact passes its own integrity check,
    while the first sign of trouble is ``offline-install`` refusing the bundle later
    ("no pinned policy found under .../agents").

    Returns ``(seat, reason)`` pairs, and an empty list when there is nothing true to say: if the
    spec or the root is unreadable the build that got this far had a spec the CLI could read, so a
    warning about the check itself would be noise rather than a signal.
    """
    try:
        import yaml  # lazy: the other subcommands stay dependency-free
    except ImportError:  # pragma: no cover - environment dependent
        return []
    try:
        spec = yaml.safe_load(Path(rig_yaml).read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError):
        return []
    if not isinstance(spec, dict):
        return []

    missing: list[tuple[str, str]] = []
    for pod in spec.get("pods") or []:
        for member in (pod or {}).get("members") or []:
            seat = str((member or {}).get("id") or "")
            ref = str((member or {}).get("agent_ref") or "")
            agent = ref.partition("local:agents/")[2]
            if not seat or not agent or not safe_relative(agent):
                continue
            policy = Path(rig_root) / "agents" / agent / "policy.json"
            if not policy.is_file():
                missing.append((seat, f"no policy.json in agents/{agent}"))
                continue
            try:
                claimed = json.loads(policy.read_text(encoding="utf-8")).get("Seat")
            except (OSError, ValueError):
                missing.append((seat, f"the policy in agents/{agent} is unreadable"))
                continue
            # Two seats sharing one seat type share one agent directory, so only one of their
            # policies can travel; the other seat is here.
            if isinstance(claimed, str) and claimed != seat:
                missing.append((seat, f"the policy in agents/{agent} belongs to seat {claimed!r}"))
    return missing


def cmd_bundle(args: argparse.Namespace) -> None:
    room = validate_name("room", args.room)
    seat = validate_name("seat", args.seat)

    seat_dir = DEFAULT_OUT.expanduser().resolve() / room / seat
    lock = read_lock(seat_dir / "pinned.json")
    if lock is None:
        raise SyncError(
            f"no pinned snapshot for {room}/{seat}; run "
            f"`4genteam sync pull {room} {seat}` first",
            EXIT_USAGE,
        )
    pinned_hash = validate_hash(lock["hash"])

    out_dir = (args.out_dir or Path.cwd()).expanduser().resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    bundle_path = out_dir / f"{room}-{seat}-{pinned_hash[:8]}.rigbundle"

    command = [
        "rig",
        "bundle",
        "create",
        str(args.rig_yaml),
        "--rig-root",
        str(args.rig_root),
        "-o",
        str(bundle_path),
        "--name",
        f"{room}-{seat}",
        "--bundle-version",
        "1.0.0",
        "--notes",
        f"resolved_sha256={pinned_hash} source=4genthub",
    ]
    try:
        subprocess.run(command, check=True)
    except FileNotFoundError:
        raise SyncError("`rig` executable not found on PATH", EXIT_REMOTE)
    except subprocess.CalledProcessError as err:
        raise SyncError(
            f"rig bundle create failed with exit code {err.returncode}", EXIT_REMOTE
        )

    # The build succeeded and the artifact passes its own integrity check, so this is the only
    # place the operator can be told what it does NOT contain. WARNED rather than refused: a
    # bundle without a pin is still complete for everything that does not enforce or audit, and
    # `offline-install` already refuses it with a precise message - what was missing was the
    # SIGNAL AT BUILD TIME, which is what this adds.
    missing = unpinned_seats(args.rig_yaml, args.rig_root)
    if missing:
        detail = "; ".join(f"{seat} ({reason})" for seat, reason in missing)
        print(
            f"warning: {len(missing)} seat(s) in this bundle have no usable pinned policy, so a "
            f"seat launched from it offline cannot decide or audit and an offline install will "
            f"refuse it - {detail}. Build the rig root with `{Path(__file__).name} rig <room>`, "
            "which materializes each seat's policy.json into its agent directory.",
            file=sys.stderr,
        )
    print(str(bundle_path))


def cmd_offline_install(args: argparse.Namespace) -> None:
    """Install a materialized bundle's pinned policy into the seat store.

    ``seatcheck`` resolves its policy as
    ``<home>/.openrig/agenthub-seats/<rig>/<member>/policy.json`` (it is deliberately not a
    flag, so the guard cannot choose where its policy and audit live). A bundle
    materializes to ``<target>/agents/<agent>/``, so this copies the pinned
    ``policy.json``/``pinned.json`` that travel inside the bundle into that store path —
    without it an offline seat's allowed path cannot decide or audit (G4).
    """
    try:
        import yaml  # lazy: the other subcommands stay dependency-free
    except ImportError as err:  # pragma: no cover - environment dependent
        raise SyncError(f"PyYAML is required for offline-install: {err}", EXIT_USAGE)

    target = Path(args.target).expanduser().resolve()
    spec_path = target / "rig.yaml"
    try:
        spec = yaml.safe_load(spec_path.read_text(encoding="utf-8"))
    except OSError as err:
        raise SyncError(f"cannot read {spec_path}: {err}", EXIT_USAGE) from err
    except yaml.YAMLError as err:
        raise SyncError(f"{spec_path} is not valid YAML: {err}", EXIT_USAGE) from err
    if not isinstance(spec, dict):
        raise SyncError(f"{spec_path} is not a rig spec", EXIT_USAGE)
    rig = validate_name("room", str(spec.get("name") or ""))
    home = Path(args.home).expanduser().resolve() if args.home else real_home()
    store = home / ".openrig" / "agenthub-seats"

    installed: list[str] = []
    for pod in spec.get("pods") or []:
        for member in (pod or {}).get("members") or []:
            seat = validate_name("seat", str((member or {}).get("id") or ""))
            ref = str((member or {}).get("agent_ref") or "")
            agent = ref.partition("local:agents/")[2]
            if not agent or not safe_relative(agent):
                print(f"skipped {rig}/{seat}: no local agent ref", file=sys.stderr)
                continue
            agent_dir = target / "agents" / agent
            policy = agent_dir / "policy.json"
            if not policy.is_file():
                print(
                    f"skipped {rig}/{seat}: no pinned policy in the bundle "
                    f"({policy} is missing)",
                    file=sys.stderr,
                )
                continue
            # Two members that share one seat type share one agent directory in the bundle,
            # so only one of their policies can ride there. Refuse to install a policy that
            # names a different seat instead of silently giving this seat the wrong links.
            try:
                claimed = json.loads(policy.read_text(encoding="utf-8")).get("Seat")
            except (OSError, ValueError) as err:
                raise SyncError(f"cannot read {policy}: {err}", EXIT_USAGE) from err
            if isinstance(claimed, str) and claimed != seat:
                print(
                    f"skipped {rig}/{seat}: the bundle's {agent} policy belongs to seat "
                    f"{claimed!r} (members sharing one seat type share one agent directory "
                    "in a bundle; per-seat policy needs distinct seat types)",
                    file=sys.stderr,
                )
                continue
            seat_dir = store / rig / seat
            seat_dir.mkdir(parents=True, exist_ok=True)
            shutil.copy2(policy, seat_dir / "policy.json")
            pinned = agent_dir / "pinned.json"
            if pinned.is_file():
                shutil.copy2(pinned, seat_dir / "pinned.json")
            installed.append(f"{rig}/{seat}")
    if not installed:
        raise SyncError(
            f"no pinned policy found under {target}/agents; build the bundle from a rig "
            "root that carries it (4genteam sync rig / bundle does since 2026-10-05)",
            EXIT_USAGE,
        )
    for name in installed:
        print(f"installed {name} -> {store / name}")


def _seat_node(room: str, seat: str) -> dict | None:
    """The ``rig ps`` node for ``room.seat``, or None when OpenRig does not list it."""
    result = run_rig(["rig", "ps", "--json", "--nodes", "--rig", room])
    try:
        nodes = json.loads(result.stdout)
    except json.JSONDecodeError as err:
        raise SyncError(f"rig ps returned invalid JSON: {err}", EXIT_REMOTE)
    if isinstance(nodes, dict):
        nodes = nodes.get("items")
    for node in nodes if isinstance(nodes, list) else []:
        if isinstance(node, dict) and (node.get("logicalId") or "").partition(".")[2] == seat:
            return node
    return None


def _agent_is_gone(node: dict) -> bool:
    """The reading a dead agent leaves: the tmux session is up, the runtime hook is not."""
    activity = node.get("agentActivity") or {}
    return (
        node.get("sessionStatus") not in ("stopped", "exited")
        and activity.get("state") == "unknown"
        and activity.get("reason") == "no_runtime_hook"
    )


def cmd_respawn(args: argparse.Namespace) -> None:
    """Respawn a seat whose agent died outside `rig seat stop`.

    OpenRig has no automatic trigger for this: its installed CLI carries no respawn or
    auto-restart path (grep over `@openrig/cli/dist` for respawn/autoRestart/restartPolicy is
    empty); it offers only the deliberate primitive `rig seat launch <seat> [--fresh] [--stop]
    --reason <text>` (registered at `@openrig/cli/dist/commands/seat.js:419`), which creates a
    blank native occupant with a new session id and generation. This runs that primitive, and
    only for a seat that really reads dead: a just-launched agy seat produces the SAME reading
    (`agentActivity` unknown + `no_runtime_hook`) until its runtime hook attaches (~15s; an omp
    seat with no activity yet reports reason null instead), so the reading must hold for
    `--after-seconds` before anything is launched.
    """
    room = validate_name("room", args.room)
    seat = validate_name("seat", args.seat)
    deadline = time.monotonic() + args.after_seconds
    while True:
        node = _seat_node(room, seat)
        if node is None:
            raise SyncError(f"OpenRig lists no node {room}.{seat}", EXIT_USAGE)
        if not _agent_is_gone(node):
            raise SyncError(
                f"{room}.{seat} is not in the dead-agent state "
                f"(sessionStatus={node.get('sessionStatus')!r}, "
                f"agentActivity={node.get('agentActivity')!r}); refusing to respawn",
                EXIT_USAGE,
            )
        if time.monotonic() >= deadline:
            break
        time.sleep(RESPAWN_POLL_SECONDS)
    session = canonical_session(room, seat) or f"{room}.{seat}"
    command = [
        "rig",
        "seat",
        "launch",
        session,
        "--fresh",
        "--stop",
        "--reason",
        args.reason,
    ]
    try:
        result = run_rig(command)
    except subprocess.CalledProcessError as err:
        # `rig seat launch` can start the occupant and still exit non-zero (for example "Fresh
        # occupant started but runtime identity requires attention"), so decide on the seat's
        # actual state, not the exit code: an automated caller must not read a successful respawn
        # as a failure. Still dead -> error; running now -> success with the caveat on stderr.
        detail = (err.stderr or err.stdout or "").strip().splitlines()
        message = detail[-1] if detail else f"exit {err.returncode}"
        node = _seat_node(room, seat) or {}
        if _agent_is_gone(node):
            raise SyncError(f"rig seat launch reported: {message}", EXIT_REMOTE) from err
        print(f"respawned {room}.{seat}; rig seat launch warned: {message}", file=sys.stderr)
        return
    if result.stdout.strip():
        print(result.stdout.strip())


def validate_choice(
    kind: str, value: str, pattern: re.Pattern | None, allowed=None
) -> str:
    if allowed is not None and value not in allowed:
        raise SyncError(
            f"invalid {kind}: {value!r} (expected one of {', '.join(allowed)})",
            EXIT_USAGE,
        )
    if pattern is not None and not pattern.fullmatch(value):
        raise SyncError(
            f"invalid {kind}: {value!r} (expected {pattern.pattern})", EXIT_USAGE
        )
    return value


def current_occupant(base_url: str, token: str, room: str, seat: str) -> dict:
    path = f"{ROOMS_PATH}/{room}/seats"
    body = get_json(base_url, token, path)
    seats = body.get("seats")
    if body.get("success") is not True or not isinstance(seats, list):
        raise SyncError(f"GET {path} returned an error response", EXIT_REMOTE)
    for entry in seats:
        if isinstance(entry, dict) and entry.get("seat_key") == seat:
            return entry
    raise SyncError(f"seat {room}/{seat} not found in the cloud", EXIT_REMOTE)


def run_rig(command: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(command, check=True, capture_output=True, text=True)


def canonical_session(room: str, seat: str) -> str | None:
    """Find the live canonical session name of a seat, or None if OpenRig lacks it."""
    result = run_rig(["rig", "ps", "--json", "--nodes", "--rig", room])
    try:
        nodes = json.loads(result.stdout)
    except json.JSONDecodeError as err:
        raise SyncError(f"rig ps returned invalid JSON: {err}", EXIT_REMOTE)
    if isinstance(nodes, dict):
        nodes = nodes.get("items")
    for node in nodes if isinstance(nodes, list) else []:
        if not isinstance(node, dict):
            continue
        logical = node.get("logicalId")
        name = node.get("canonicalSessionName")
        if (
            isinstance(logical, str)
            and isinstance(name, str)
            and name
            and node.get("rigName", room) == room
            and logical.partition(".")[2] == seat
        ):
            return name
    return None


def apply_to_rig(room: str, seat: str, model: str, restart: bool, reason: str) -> str:
    """Apply the model through OpenRig; return the applied mode actually reached."""
    try:
        session = canonical_session(room, seat)
    except FileNotFoundError:
        print(
            "note: `rig` not found on PATH; the cloud change is recorded only",
            file=sys.stderr,
        )
        return "none"
    except subprocess.CalledProcessError as err:
        raise SyncError(f"rig ps failed with exit code {err.returncode}", EXIT_REMOTE)
    if session is None:
        print(
            f"note: seat {room}/{seat} is not in `rig ps`; the cloud change is recorded only",
            file=sys.stderr,
        )
        return "none"

    commands = []
    if model:
        commands.append(
            ["rig", "seat", "set-model", session, "--model", model, "--reason", reason]
        )
    if restart:
        print(
            f"warning: restarting {session} interrupts it and loses its live context",
            file=sys.stderr,
        )
        commands.append(["rig", "seat", "stop", session, "--reason", reason])
        commands.append(
            ["rig", "seat", "launch", session, "--fresh", "--reason", reason]
        )
    for command in commands:
        try:
            run_rig(command)
        except FileNotFoundError:
            raise SyncError("`rig` executable not found on PATH", EXIT_REMOTE)
        except subprocess.CalledProcessError as err:
            raise SyncError(
                f"`rig seat {command[2]}` failed with exit code {err.returncode}",
                EXIT_REMOTE,
            )
    if restart:
        return "restart"
    return "set-model" if commands else "none"


def cmd_switch(args: argparse.Namespace) -> None:
    room = validate_name("room", args.room)
    seat = validate_name("seat", args.seat)
    if args.runtime is None and args.model is None:
        raise SyncError("at least one of --runtime or --model is required", EXIT_USAGE)
    if args.runtime is not None:
        validate_choice("runtime", args.runtime, None, RUNTIMES)
    if args.model is not None:
        validate_choice("model", args.model, MODEL_RE)
    base_url = require_env("AGENTHUB_URL")
    token = require_env("AGENTHUB_TOKEN")

    current = current_occupant(base_url, token, room, seat)
    runtime = args.runtime if args.runtime is not None else current.get("runtime") or ""
    model = args.model if args.model is not None else current.get("model") or ""
    path = f"{ROOMS_PATH}/{room}/seats/{seat}/occupant"
    body = request_json(
        "PUT", base_url, token, path, {"runtime": runtime, "model": model}
    )
    if body.get("success") is not True:
        raise SyncError(f"PUT {path} returned an error response", EXIT_REMOTE)

    runtime_changed = runtime != (current.get("runtime") or "")
    model_changed = model != (current.get("model") or "")
    reason = args.reason or f"4genthub switch {room}/{seat}"
    if args.apply is not None:
        mode = args.apply
    elif runtime_changed:
        mode = "manual"
    else:
        mode = "set-model" if model_changed else "none"

    applied = "none"
    if mode != "none" and runtime_changed:
        applied = "manual"
        print(
            f"runtime changed: OpenRig cannot switch a runtime in place. Run:\n"
            f"  4genteam sync rig {room} --update\n"
            f"  rig down {room}\n"
            f"  rig up <path of the rig.yaml printed by the first command>",
            file=sys.stderr,
        )
    elif mode != "none":
        applied = apply_to_rig(
            room, seat, model if model_changed else "", mode == "restart", reason
        )
    print(f"switched:{room}/{seat} runtime={runtime} model={model} applied={applied}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="4genteam sync",
        description="Download and pin 4genthub resolved seats for OpenRig.",
        epilog=(
            "Offline use is an explicit operator choice: after `bundle` produces a "
            ".rigbundle, run it yourself with `rig up <bundle> --target <dir>`. This "
            "script never falls back to a bundle automatically."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    pull = subparsers.add_parser("pull", help="download a resolved seat and pin it")
    pull.add_argument("room")
    pull.add_argument("seat")
    pull.add_argument(
        "--out", type=Path, default=None, help=f"seat store (default {DEFAULT_OUT})"
    )
    pull.add_argument(
        "--update",
        action="store_true",
        help="adopt the fetched snapshot even if a different pin exists",
    )
    pull.set_defaults(func=cmd_pull)

    rig = subparsers.add_parser(
        "rig", help="materialize a whole room: rig.yaml plus agent links"
    )
    rig.add_argument("room")
    rig.add_argument(
        "--out", type=Path, default=None, help=f"seat store (default {DEFAULT_OUT})"
    )
    rig.add_argument(
        "--update",
        action="store_true",
        help="adopt fetched snapshots even if different pins exist",
    )
    rig.set_defaults(func=cmd_rig)

    bundle = subparsers.add_parser(
        "bundle", help="build a .rigbundle from the pinned snapshot"
    )
    bundle.add_argument("room")
    bundle.add_argument("seat")
    bundle.add_argument("--rig-yaml", type=Path, required=True)
    bundle.add_argument("--rig-root", type=Path, required=True)
    bundle.add_argument(
        "--out-dir",
        type=Path,
        default=None,
        help="where to write the .rigbundle (default: current directory)",
    )
    bundle.set_defaults(func=cmd_bundle)

    offline = subparsers.add_parser(
        "offline-install",
        help="install a materialized bundle's pinned policy into the seat store",
    )
    offline.add_argument(
        "target", type=Path, help="the directory a .rigbundle was materialized into"
    )
    offline.add_argument(
        "--home",
        type=Path,
        default=None,
        help="home whose .openrig/agenthub-seats is written (default: the real home)",
    )
    offline.set_defaults(func=cmd_offline_install)

    respawn = subparsers.add_parser(
        "respawn",
        help="respawn a seat whose agent died outside `rig seat stop`",
    )
    respawn.add_argument("room")
    respawn.add_argument("seat")
    respawn.add_argument(
        "--after-seconds",
        type=float,
        default=DEFAULT_RESPAWN_AFTER_SECONDS,
        help=(
            "how long the dead reading must hold before launching "
            f"(default {DEFAULT_RESPAWN_AFTER_SECONDS:g}s: a just-launched seat reads the same)"
        ),
    )
    respawn.add_argument(
        "--reason",
        default="agent died outside `rig seat stop` (respawn)",
        help="audit reason recorded on the seat.fresh_launched event",
    )
    respawn.set_defaults(func=cmd_respawn)

    install = subparsers.add_parser(
        "install-checker",
        help="build seatcheck into the seat store and link it on PATH",
    )
    install.add_argument(
        "--out", type=Path, default=None, help=f"seat store (default {DEFAULT_OUT})"
    )
    install.set_defaults(func=cmd_install_checker)

    switch = subparsers.add_parser(
        "switch", help="change the LLM of a seat: cloud records it, OpenRig applies it"
    )
    switch.add_argument("room")
    switch.add_argument("seat")
    switch.add_argument("--runtime", default=None)
    switch.add_argument("--model", default=None)
    switch.add_argument(
        "--apply",
        choices=APPLY_MODES,
        default=None,
        help="default: manual steps when the runtime changed, else set-model when "
        "the model changed, else none; "
        "restart also stops and relaunches the seat fresh",
    )
    switch.add_argument("--reason", default=None, help="audit reason passed to rig")
    switch.set_defaults(func=cmd_switch)

    args = parser.parse_args(argv)
    try:
        args.func(args)
    except SyncError as err:
        print(f"error: {err}", file=sys.stderr)
        return err.code
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
