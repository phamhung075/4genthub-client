"""Tests for agenthub_client.seat_sync.

These exercise the client against a local HTTP server serving canned resolved
seats and rigspecs, so no real 4genthub server or `rig` binary is needed.
"""

import importlib.util
import json
import os
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import pytest
import yaml

# These tests are self-contained and must not spin up the test database.
pytestmark = pytest.mark.unit

MODULE_PATH = Path(__file__).resolve().parents[1] / "src" / "agenthub_client" / "seat_sync.py"

HASH_A = "a1" * 32
HASH_B = "b2" * 32
HASH_LONG = "c3d4e5f6" + "0" * 56

FILES = [
    {"path": "docs/readme.md", "content": "hello"},
    {"path": "nested/deeper/notes.txt", "content": "notes"},
]
POLICY = {"max_turns": 5, "runtime": "claude"}

RIG_YAML = (
    "name: room1\n"
    "pods:\n"
    "  - id: main\n"
    "    members:\n"
    "      - id: seat1\n"
    "        agent_ref: local:agents/seat1\n"
)


def _load_module():
    return importlib.import_module("agenthub_client.seat_sync")


seat_sync = _load_module()


class _SeatHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.server.gets.append(self.path)
        route = self.server.routes.get(self.path.partition("?")[0])
        if route is None:
            self.send_error(404)
            return
        status, body = route
        payload = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_PUT(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.server.puts.append(
            (
                self.path,
                json.loads(self.rfile.read(length)),
                self.headers.get("Authorization"),
            )
        )
        route = self.server.routes.get(("PUT", self.path))
        if route is None:
            self.send_error(404)
            return
        status, body = route
        payload = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args):  # keep test output clean
        pass


class SeatServer:
    def __init__(self):
        self.routes = {}
        self.httpd = HTTPServer(("127.0.0.1", 0), _SeatHandler)
        self.httpd.routes = self.routes
        self.puts = []
        self.httpd.puts = self.puts
        self.gets = []
        self.httpd.gets = self.gets
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return f"http://127.0.0.1:{self.httpd.server_port}"

    def set_seat(
        self,
        hash_value,
        files=None,
        policy=None,
        status=200,
        room="room1",
        seat="seat1",
    ):
        path = f"/api/v2/openrig/seats/{room}/{seat}"
        if status == 200:
            body = {
                "success": True,
                "resolved_seat": {
                    "room": room,
                    "seat": seat,
                    "hash": hash_value,
                    "runtime": "claude",
                    "files": FILES if files is None else files,
                    "policy": POLICY if policy is None else policy,
                },
            }
        else:
            body = {"success": False, "error": "boom"}
        self.routes[path] = (status, body)

    def set_rigspec(
        self,
        yaml_text=None,
        seats=None,
        status=200,
        room="room1",
        error="room has no active seats",
    ):
        path = f"/api/v2/openrig/rooms/{room}/rigspec"
        if status == 200:
            body = {
                "success": True,
                "rigspec": {
                    "name": room,
                    "yaml": RIG_YAML if yaml_text is None else yaml_text,
                    "seats": (
                        [{"seat": "seat1", "hash": HASH_A}] if seats is None else seats
                    ),
                },
            }
        else:
            body = {"success": False, "error": error}
        self.routes[path] = (status, body)

    def set_occupants(self, seats, room="room1"):
        self.routes[f"/api/v2/openrig/rooms/{room}/seats"] = (
            200,
            {"success": True, "seats": seats},
        )

    def set_put(self, status=200, room="room1", seat="seat1"):
        path = f"/api/v2/openrig/rooms/{room}/seats/{seat}/occupant"
        body = (
            {"success": True, "seat": {}}
            if status == 200
            else {"success": False, "error": "no"}
        )
        self.routes[("PUT", path)] = (status, body)

    def close(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.thread.join(timeout=5)


@pytest.fixture
def server():
    seat_server = SeatServer()
    yield seat_server
    seat_server.close()


@pytest.fixture
def env(monkeypatch, server):
    monkeypatch.setenv("AGENTHUB_URL", server.url)
    monkeypatch.setenv("AGENTHUB_TOKEN", "test-token")
    return server


REAL_REQUIRE_CHECKER = getattr(seat_sync, "require_checker", None)
REAL_TMUX_GLOBAL_PATH = getattr(seat_sync, "tmux_global_path", None)
REAL_OPENRIG_DAEMON_PID = getattr(seat_sync, "openrig_daemon_pid", None)


@pytest.fixture(autouse=True)
def checker_present(monkeypatch):
    """Most tests are not about the checker binary: stub the requirement."""
    monkeypatch.setattr(seat_sync, "require_checker", lambda out: None, raising=False)


@pytest.fixture(autouse=True)
def no_tmux_server(monkeypatch):
    """Tests do not talk to a real tmux server or the real rig daemon: the checker falls
    back to the shell PATH (a test that wants the cold-start daemon PATH stubs these)."""
    monkeypatch.setattr(seat_sync, "tmux_global_path", lambda: None, raising=False)
    monkeypatch.setattr(seat_sync, "openrig_daemon_pid", lambda: None, raising=False)


@pytest.fixture
def real_checker_requirement(monkeypatch):
    monkeypatch.setattr(seat_sync, "require_checker", REAL_REQUIRE_CHECKER)


@pytest.fixture
def checker_home(monkeypatch, tmp_path):
    """A temp ~/.local/bin for the checker link, and the only PATH entry.

    The link location is MACHINE-LEVEL and now resolves from the passwd entry rather than from
    HOME, so a temp HOME can no longer redirect it - on a machine where the real link exists,
    "the link is missing" would stop being simulable at all. The fixture patches the resolution
    where it is used, which is the same seam the other path defaults are stubbed through.
    """
    home = tmp_path / "home"
    link_dir = home / ".local" / "bin"
    link_dir.mkdir(parents=True)
    monkeypatch.setattr(seat_sync, "checker_link", lambda: link_dir / "seatcheck")
    monkeypatch.setenv("PATH", str(link_dir))
    return link_dir


def test_seat_path_reads_the_daemon_path_at_cold_start(monkeypatch):
    """At cold start the first seat inherits the rig daemon's PATH (read from the daemon's
    process), not the operator's shell; with no daemon it falls back to the shell and says so."""
    monkeypatch.setenv("PATH", "/operator/shell/bin")
    monkeypatch.setattr(
        seat_sync,
        "proc_env_path",
        lambda pid: "/daemon/bin" if pid == 4242 else None,
        raising=False,
    )

    monkeypatch.setattr(seat_sync, "openrig_daemon_pid", lambda: 4242, raising=False)
    assert seat_sync.seat_path() == ("/daemon/bin", seat_sync.DAEMON_PATH_SOURCE)

    monkeypatch.setattr(seat_sync, "openrig_daemon_pid", lambda: None, raising=False)
    assert seat_sync.seat_path() == ("/operator/shell/bin", seat_sync.SHELL_PATH_SOURCE)


def test_openrig_daemon_port_prefers_openrig_port_then_url(monkeypatch):
    """OPENRIG_PORT wins; otherwise the port is parsed from OPENRIG_URL; neither is None."""
    monkeypatch.setenv("OPENRIG_PORT", "7433")
    monkeypatch.setenv("OPENRIG_URL", "http://127.0.0.1:1")
    assert seat_sync.openrig_daemon_port() == "7433"

    monkeypatch.setenv("OPENRIG_PORT", "")
    monkeypatch.setenv("OPENRIG_URL", "http://127.0.0.1:7433")
    assert seat_sync.openrig_daemon_port() == "7433"

    monkeypatch.setenv("OPENRIG_URL", "not-a-url")
    assert seat_sync.openrig_daemon_port() is None


def test_openrig_daemon_pid_parses_ss_output(monkeypatch):
    """The listening pid is parsed from ss -ltnp; a matching line with no pid= is None."""

    class _Proc:
        def __init__(self, stdout):
            self.stdout = stdout
            self.returncode = 0

    # the autouse fixture stubs openrig_daemon_pid; this test exercises the real parser
    monkeypatch.setattr(
        seat_sync, "openrig_daemon_pid", REAL_OPENRIG_DAEMON_PID, raising=False
    )
    monkeypatch.setenv("OPENRIG_PORT", "7433")
    ss_with_pid = (
        "State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"
        'LISTEN 0 511 127.0.0.1:7433 0.0.0.0:* users:(("MainThread",pid=17485,fd=32))\n'
        'LISTEN 0 511 127.0.0.1:17433 0.0.0.0:* users:(("MainThread",pid=14914,fd=32))\n'
    )
    monkeypatch.setattr(
        seat_sync.subprocess, "run", lambda argv, **kw: _Proc(ss_with_pid)
    )
    assert seat_sync.openrig_daemon_pid() == 17485

    monkeypatch.setenv("OPENRIG_PORT", "9999")
    monkeypatch.setattr(
        seat_sync.subprocess,
        "run",
        lambda argv, **kw: _Proc("LISTEN 0 511 127.0.0.1:9999 0.0.0.0:*\n"),
    )
    assert seat_sync.openrig_daemon_pid() is None


def test_proc_env_path_reads_the_path_of_this_process():
    """proc_env_path parses /proc/<pid>/environ; this process is the only safe live pid."""
    if not Path("/proc/self/environ").exists():
        pytest.skip("no /proc on this host")
    assert seat_sync.proc_env_path(os.getpid()) == os.environ.get("PATH")


def make_binary(out):
    binary = out / "bin" / "seatcheck"
    binary.parent.mkdir(parents=True, exist_ok=True)
    binary.write_text("#!/bin/sh\n")
    binary.chmod(0o755)
    return binary


def run_cli(argv):
    try:
        return seat_sync.main(argv)
    except SystemExit as exc:
        return exc.code if isinstance(exc.code, int) else 1


def test_pull_writes_files_and_creates_lock(env, tmp_path, capsys):
    env.set_seat(HASH_A)

    code = run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    seat_dir = tmp_path / "room1" / "seat1"
    assert code == 0
    assert out.strip().splitlines() == [f"path:{seat_dir / HASH_A}"]
    assert err == ""

    assert (seat_dir / HASH_A / "docs" / "readme.md").read_text() == "hello"
    assert (
        seat_dir / HASH_A / "nested" / "deeper" / "notes.txt"
    ).read_text() == "notes"

    lock = json.loads((seat_dir / "pinned.json").read_text())
    assert lock == {"hash": HASH_A, "path": str(seat_dir / HASH_A)}
    assert json.loads((seat_dir / "policy.json").read_text()) == POLICY


def test_second_pull_keeps_lock_and_prints_notice(env, tmp_path, capsys):
    env.set_seat(HASH_A)
    assert run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    env.set_seat(HASH_B, files=[{"path": "docs/readme.md", "content": "newer"}])
    code = run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    seat_dir = tmp_path / "room1" / "seat1"
    assert code == 0
    assert out.strip().splitlines() == [f"path:{seat_dir / HASH_A}"]
    assert f"newer snapshot available: {HASH_B} (run with --update to adopt)" in err
    assert json.loads((seat_dir / "pinned.json").read_text())["hash"] == HASH_A
    assert not (seat_dir / HASH_B).exists()
    assert (seat_dir / HASH_A / "docs" / "readme.md").read_text() == "hello"


def test_update_moves_lock_and_materializes_new_hash(env, tmp_path, capsys):
    env.set_seat(HASH_A)
    assert run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    env.set_seat(HASH_B, files=[{"path": "docs/readme.md", "content": "newer"}])
    code = run_cli(["pull", "room1", "seat1", "--out", str(tmp_path), "--update"])
    out, err = capsys.readouterr()

    seat_dir = tmp_path / "room1" / "seat1"
    assert code == 0
    assert out.strip().splitlines() == [f"path:{seat_dir / HASH_B}"]
    assert err == ""
    assert json.loads((seat_dir / "pinned.json").read_text())["hash"] == HASH_B
    assert (seat_dir / HASH_B / "docs" / "readme.md").read_text() == "newer"
    # The previous pinned snapshot is immutable and survives.
    assert (seat_dir / HASH_A / "docs" / "readme.md").read_text() == "hello"


@pytest.mark.parametrize(
    "bad_path",
    ["../evil.txt", "/etc/passwd", "a\\b.txt", "a//b.txt", "", "./x.txt", ".."],
)
def test_unsafe_file_path_rejected(env, tmp_path, capsys, bad_path):
    env.set_seat(HASH_A, files=[{"path": bad_path, "content": "x"}])

    code = run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 2
    assert out == ""
    assert "unsafe file path" in err
    assert not (tmp_path / "evil.txt").exists()


@pytest.mark.parametrize("bad_name", ["../etc", "_x", "", "a/b", "room.1", "room name"])
def test_unsafe_room_name_rejected(env, tmp_path, capsys, bad_name):
    code = run_cli(["pull", bad_name, "seat1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 2
    assert out == ""
    assert "invalid room name" in err


def test_unsafe_seat_name_rejected(env, tmp_path, capsys):
    code = run_cli(["pull", "room1", "seat.1", "--out", str(tmp_path)])
    _, err = capsys.readouterr()

    assert code == 2
    assert "invalid seat name" in err


def test_uppercase_room_and_seat_names_accepted(env, tmp_path, capsys):
    env.set_seat(HASH_A, room="Room1", seat="Seat1")

    code = run_cli(["pull", "Room1", "Seat1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    seat_dir = tmp_path / "Room1" / "Seat1"
    assert code == 0
    assert out.strip().splitlines() == [f"path:{seat_dir / HASH_A}"]
    assert err == ""
    assert (seat_dir / HASH_A / "docs" / "readme.md").read_text() == "hello"


def test_missing_pinned_dir_fails_loudly(env, tmp_path, capsys):
    seat_dir = tmp_path / "room1" / "seat1"
    seat_dir.mkdir(parents=True)
    (seat_dir / "pinned.json").write_text(
        json.dumps({"hash": HASH_A, "path": str(seat_dir / HASH_A)})
    )
    env.set_seat(HASH_B)

    code = run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 2
    assert out == ""
    assert "pinned seat directory is missing" in err
    assert HASH_A in err
    # No silent fallback to the fetched snapshot.
    assert not (seat_dir / HASH_B).exists()
    assert json.loads((seat_dir / "pinned.json").read_text())["hash"] == HASH_A


def test_http_error_exits_1(env, tmp_path, capsys):
    env.set_seat(HASH_A, status=500)

    code = run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 1
    assert out == ""
    assert "HTTP 500" in err


def test_invalid_json_exits_1(monkeypatch, tmp_path, capsys):
    class _BadHandler(BaseHTTPRequestHandler):
        def do_GET(self):
            body = b"not json"
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    httpd = HTTPServer(("127.0.0.1", 0), _BadHandler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    try:
        monkeypatch.setenv("AGENTHUB_URL", f"http://127.0.0.1:{httpd.server_port}")
        monkeypatch.setenv("AGENTHUB_TOKEN", "test-token")
        code = run_cli(["pull", "room1", "seat1", "--out", str(tmp_path)])
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join(timeout=5)

    out, err = capsys.readouterr()
    assert code == 1
    assert out == ""
    assert "invalid JSON" in err


def test_rig_writes_yaml_verbatim_and_materializes_each_seat_with_its_policy(
    env, tmp_path, capsys
):
    yaml_text = (
        "name: room1\n"
        "pods:\n"
        "  - id: main\n"
        "    members:\n"
        "      - id: coder\n"
        "        agent_ref: local:agents/coder\n"
        "      - id: checker\n"
        "        agent_ref: local:agents/checker\n"
    )
    env.set_rigspec(
        yaml_text,
        [{"seat": "coder", "hash": HASH_A}, {"seat": "checker", "hash": HASH_B}],
    )
    env.set_seat(HASH_A, room="room1", seat="coder")
    env.set_seat(HASH_B, room="room1", seat="checker")

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    rig_dir = tmp_path / "room1" / "rig"
    assert code == 0
    assert out.strip().splitlines() == [f"rig:{rig_dir / 'rig.yaml'}"]
    assert err == ""
    # The server's YAML is written verbatim, byte for byte.
    assert (rig_dir / "rig.yaml").read_text() == yaml_text

    coder = rig_dir / "agents" / "coder"
    checker = rig_dir / "agents" / "checker"
    # Materialized copies, not symlinks: a bundle built from this rig root copies what sits
    # here, so the seat's pinned policy must travel next to the rendered files (G4 fix).
    assert not coder.is_symlink() and coder.is_dir()
    assert not checker.is_symlink() and checker.is_dir()
    for seat, hash_value in (("coder", HASH_A), ("checker", HASH_B)):
        agent = rig_dir / "agents" / seat
        assert (agent / "docs/readme.md").read_text() == "hello"
        assert (agent / "nested/deeper/notes.txt").read_text() == "notes"
        assert json.loads((agent / "pinned.json").read_text())["hash"] == hash_value
        assert json.loads((agent / "policy.json").read_text()) == POLICY


def test_rig_accepts_uppercase_names(env, tmp_path, capsys):
    env.set_seat(HASH_A, room="Room1", seat="Coder")
    env.set_rigspec(RIG_YAML, [{"seat": "Coder", "hash": HASH_A}], room="Room1")

    code = run_cli(["rig", "Room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    rig_dir = tmp_path / "Room1" / "rig"
    assert code == 0
    assert out.strip().splitlines() == [f"rig:{rig_dir / 'rig.yaml'}"]
    assert err == ""
    coder = rig_dir / "agents" / "Coder"
    assert coder.is_dir() and not coder.is_symlink()
    assert (coder / "docs/readme.md").read_text() == "hello"
    assert json.loads((coder / "pinned.json").read_text())["hash"] == HASH_A
    assert json.loads((coder / "policy.json").read_text()) == POLICY


def test_rig_second_run_keeps_pin_and_prints_notice(env, tmp_path, capsys):
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_A}])
    env.set_seat(HASH_A)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    shifted_yaml = RIG_YAML + "# shifted\n"
    env.set_rigspec(shifted_yaml, [{"seat": "seat1", "hash": HASH_B}])
    env.set_seat(HASH_B, files=[{"path": "docs/readme.md", "content": "newer"}])
    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    seat_dir = tmp_path / "room1" / "seat1"
    rig_dir = tmp_path / "room1" / "rig"
    assert code == 0
    assert out.strip().splitlines() == [f"rig:{rig_dir / 'rig.yaml'}"]
    assert f"newer snapshot available: {HASH_B} (run with --update to adopt)" in err
    assert json.loads((seat_dir / "pinned.json").read_text())["hash"] == HASH_A
    # The materialized agent still comes from the pinned directory, not the newer one.
    agent = rig_dir / "agents" / "seat1"
    assert agent.is_dir() and not agent.is_symlink()
    assert json.loads((agent / "pinned.json").read_text())["hash"] == HASH_A
    assert json.loads((agent / "policy.json").read_text()) == POLICY
    assert not (seat_dir / HASH_B).exists()
    # rig.yaml always comes from the current server response.
    assert (rig_dir / "rig.yaml").read_text() == shifted_yaml


def test_rig_build_keeps_operator_files_it_did_not_create(env, tmp_path, capsys):
    """A rebuild replaces what it RENDERS and nothing else.

    The reported defect (OF4 run, 2026-10-06) was a credential loss: an operator's file placed in
    the rig directory — the documented home of a rig-root ``.env`` — was gone after the next
    build, and the seats then launched with no credential. The build owns ``rig.yaml`` and
    ``agents/``; anything else is the operator's and survives, named on stderr.
    """
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_A}])
    env.set_seat(HASH_A)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    rig_dir = tmp_path / "room1" / "rig"
    notes = rig_dir / "operator-notes.txt"
    notes.write_text("placed by the operator\n")
    target = tmp_path / "credential-target"
    target.write_text("not a credential\n")
    link = rig_dir / "operator-link"
    link.symlink_to(target)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    # stdout stays exactly the machine-readable line the launcher parses.
    assert out.strip().splitlines() == [f"rig:{rig_dir / 'rig.yaml'}"]
    assert notes.read_text() == "placed by the operator\n"
    assert link.is_symlink() and os.readlink(link) == str(target)
    assert "kept 2 file(s) the build did not create" in err
    assert "operator-link" in err and "operator-notes.txt" in err


def test_rig_build_keeps_a_file_placed_while_it_materializes(
    env, tmp_path, capsys, monkeypatch
):
    """The window the preserve list used to leave open.

    cmd_rig reads the rig directory, then materializes the staging tree, then swaps. A file that
    lands in between is in neither the old list nor the new directory — so a build that promises
    the operator their files survive used to delete it. The preserved set is now derived from the
    directories AT SWAP TIME, which makes the promise independent of when the file arrived.

    MEASURED, both ways, with this exact hook: reverting the derivation (a list read before the
    swap) makes this test fail with the marker gone.
    """
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_A}])
    env.set_seat(HASH_A)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    rig_dir = tmp_path / "room1" / "rig"
    original = seat_sync.materialize_agent

    def materialize_then_drop(source, seat_dir, target):
        original(source, seat_dir, target)
        if rig_dir.is_dir():
            (rig_dir / "dropped-while-building.txt").write_text("landed mid-build\n")

    monkeypatch.setattr(seat_sync, "materialize_agent", materialize_then_drop)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert (rig_dir / "dropped-while-building.txt").read_text() == "landed mid-build\n"
    assert "dropped-while-building.txt" in err
    # The build's own content is still the build's.
    assert (rig_dir / "rig.yaml").read_text() == RIG_YAML


def test_rig_build_replaces_its_own_rendered_content(env, tmp_path, capsys):
    """The other half of the same rule, so a future 'preserve everything' change fails here.

    ``agents/`` is the build's: a seat the room no longer lists does not survive the next build,
    because that is exactly what the staging-and-swap build is for.
    """
    env.set_rigspec(
        RIG_YAML,
        [{"seat": "seat1", "hash": HASH_A}, {"seat": "seat2", "hash": HASH_B}],
    )
    env.set_seat(HASH_A, seat="seat1")
    env.set_seat(HASH_B, seat="seat2")
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    rig_dir = tmp_path / "room1" / "rig"
    assert (rig_dir / "agents" / "seat2").is_dir()

    # seat2 leaves the room; the next build must not carry its rendered directory over.
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_A}])
    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert (rig_dir / "agents" / "seat1").is_dir()
    assert not (rig_dir / "agents" / "seat2").exists()
    # Nothing of the operator's was there, so there is nothing to report.
    assert "kept" not in err


BUNDLE_RIG_YAML = (
    'version: "0.2"\n'
    "name: room1\n"
    "pods:\n"
    "  - id: room1\n"
    "    members:\n"
    "      - id: seat1\n"
    "        agent_ref: local:agents/seat1\n"
)


def _bundle_fixture(monkeypatch, tmp_path, policy=None):
    """A pinned seat in a scratch store, plus a rig root whose agent dir may carry a policy."""
    seats = tmp_path / "seats"
    seat_dir = seats / "room1" / "seat1"
    seat_dir.mkdir(parents=True)
    (seat_dir / "pinned.json").write_text(
        json.dumps({"hash": HASH_LONG, "path": str(seat_dir / HASH_LONG)})
    )
    monkeypatch.setattr(seat_sync, "DEFAULT_OUT", seats)

    rig_yaml = tmp_path / "rig.yaml"
    rig_yaml.write_text(BUNDLE_RIG_YAML)
    agent_dir = tmp_path / "rig-root" / "agents" / "seat1"
    agent_dir.mkdir(parents=True)
    (agent_dir / "agent.yaml").write_text('name: seat1\nversion: "1.0.0"\n')
    if policy is not None:
        (agent_dir / "policy.json").write_text(json.dumps(policy))

    monkeypatch.setattr(
        seat_sync.subprocess,
        "run",
        lambda command, **kwargs: subprocess.CompletedProcess(command, 0),
    )
    return rig_yaml, tmp_path / "rig-root", tmp_path / "bundles"


def _run_bundle(rig_yaml, rig_root, out_dir):
    return run_cli(
        [
            "bundle",
            "room1",
            "seat1",
            "--rig-yaml",
            str(rig_yaml),
            "--rig-root",
            str(rig_root),
            "--out-dir",
            str(out_dir),
        ]
    )


def test_bundle_warns_when_the_rig_root_carries_no_pin(monkeypatch, tmp_path, capsys):
    """The build used to say nothing: `rig bundle create` answered Bundle created, the artifact
    passed its own integrity check, and the first sign of trouble was offline-install refusing it
    later. The operator is now told at BUILD time what the bundle does not contain."""
    rig_yaml, rig_root, out_dir = _bundle_fixture(monkeypatch, tmp_path, policy=None)

    code = _run_bundle(rig_yaml, rig_root, out_dir)
    out, err = capsys.readouterr()

    assert code == 0
    # stdout stays exactly the machine-readable line callers parse.
    assert out.strip() == str(out_dir / f"room1-seat1-{HASH_LONG[:8]}.rigbundle")
    assert "warning:" in err
    assert "seat1" in err and "no policy.json in agents/seat1" in err
    assert "offline install will refuse it" in err


def test_bundle_warns_when_the_pin_belongs_to_another_seat(
    monkeypatch, tmp_path, capsys
):
    """Two seats sharing one seat type share one agent directory in a bundle, so only one of their
    policies can ride there. The seat that lost is named, with the reason, rather than being left
    to offline-install's refusal."""
    rig_yaml, rig_root, out_dir = _bundle_fixture(
        monkeypatch, tmp_path, policy={"Seat": "someone-else", "Links": []}
    )

    code = _run_bundle(rig_yaml, rig_root, out_dir)
    out, err = capsys.readouterr()

    assert code == 0
    assert "warning:" in err
    assert "belongs to seat 'someone-else'" in err


def test_bundle_says_nothing_when_the_pin_is_this_seats(monkeypatch, tmp_path, capsys):
    """No noise: a rig root that carries this seat's own policy produces no warning at all."""
    rig_yaml, rig_root, out_dir = _bundle_fixture(
        monkeypatch, tmp_path, policy={"Seat": "seat1", "Links": []}
    )

    code = _run_bundle(rig_yaml, rig_root, out_dir)
    out, err = capsys.readouterr()

    assert code == 0
    assert err == ""
    assert out.strip() == str(out_dir / f"room1-seat1-{HASH_LONG[:8]}.rigbundle")


OMP_MCP_DOCUMENT = {
    "mcpServers": {
        "agenthub_http": {
            "type": "http",
            "url": "https://api.example.test/mcp",
            "headers": {"Authorization": "Bearer ${AGENTHUB_TOKEN}"},
        }
    }
}

# Exactly what the renderer emits for the omp startup setting (renderer.go's
# ompMCPStartupTimeoutConfig), trailing newline included.
OMP_CONFIG_FRAGMENT = "mcp:\n  startupTimeoutMs: 0\n"

# What the renderer emits for a seat with guide blocks (packet 6 step 1): provenance stamps, then the
# guides with their OWN headings, which the renderer must not re-head.
AGENTS_MD_DOCUMENT = (
    "<!-- seat-hash: a1a1 -->\n"
    "<!-- seat-type-version: 1.2.0 -->\n\n"
    "## Guide: every seat\n\nshared words\n\n"
    "## Guide: seat1\n\nits own words\n"
)

# RIG_YAML is `pods: [{id: main, members: [{id: seat1}]}]` under `name: room1`, so the session is
# main-seat1@room1 - and this is what pins the POD-id derivation rather than the rig name, which
# would give room1-seat1@room1.
OMP_SESSION = "main-seat1@room1"


def _omp_agent_dir(tmp_path):
    return tmp_path / "ompstate" / OMP_SESSION / "agent"


def _omp_config_path(tmp_path):
    return _omp_agent_dir(tmp_path) / "config.yml"


POLICY_MODULE_PATH = (
    Path(__file__).resolve().parents[1] / "src" / "agenthub_client" / "seat_policy.py"
)


def _policy_module_with_room1(monkeypatch):
    """The REAL policy module with the fixture's rig added to its table.

    The rules stay the module's own - only the registry gains a row - so a test that compares the
    written file with ``render_config`` is asserting the single-source property rather than a copy.
    """
    spec = importlib.util.spec_from_file_location(
        "openrig_seat_policy_for_test", POLICY_MODULE_PATH
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.SEAT_ROLES["room1"] = {"seat1": "dev"}
    monkeypatch.setattr(seat_sync, "load_seat_policy", lambda: module)
    return module


def test_rig_applies_the_per_seat_policy_from_the_single_source(
    env, tmp_path, capsys, monkeypatch
):
    """A launched seat gets its policy from the render's rig, with no script run by hand.

    The file must equal ``render_config``'s own document, which is what makes this an IMPORT rather
    than a second copy of the rules: the allow/deny lists are never restated in this client.
    """
    policy = _policy_module_with_room1(monkeypatch)
    _omp_rig(env, monkeypatch, tmp_path)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    config = _omp_config_path(tmp_path)
    assert config.read_text() == policy.render_config("seat1", "dev", "room1")
    assert "applied the per-seat policy" in err

    parsed = yaml.safe_load(config.read_text())
    bash, tools = policy.policy_for("dev")
    patterns = parsed["bash"]["patterns"]
    # THE EXEMPTION IS FIRST AND IS NOT A DENIAL: the startup `rig whoami` is allowed in every
    # approval mode, so the deny list is what follows it and every one of those is still a deny.
    assert patterns[0] == {"match": "rig whoami*", "approval": "allow"}
    assert [rule["match"] for rule in patterns[1:]] == bash
    assert all(rule["approval"] == "deny" for rule in patterns[1:])
    assert sorted(parsed["tools"]["approval"]) == sorted(tools)
    assert parsed["mcp"]["startupTimeoutMs"] == 0


def test_rig_policy_merge_keeps_what_the_runtime_file_already_carries(
    env, tmp_path, capsys, monkeypatch
):
    """The agent-dir config.yml is the runtime's own file: the policy's keys are set, the rest stays."""
    _policy_module_with_room1(monkeypatch)
    _omp_rig(env, monkeypatch, tmp_path)
    _omp_config_path(tmp_path).write_text("model: something-else\n")

    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    parsed = yaml.safe_load(_omp_config_path(tmp_path).read_text())
    assert (
        parsed["model"] == "something-else"
    ), "a key the policy does not define was lost"
    assert parsed["mcp"]["startupTimeoutMs"] == 0
    assert parsed["bash"]["patterns"], "the policy's own keys did not arrive"
    assert parsed["tools"]["approval"]


def test_rig_policy_application_is_idempotent(env, tmp_path, capsys, monkeypatch):
    """A second run writes nothing and touches no mtime: the file already carries the policy."""
    _policy_module_with_room1(monkeypatch)
    _omp_rig(env, monkeypatch, tmp_path)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()
    config = _omp_config_path(tmp_path)
    before = config.read_bytes()
    stamp = config.stat().st_mtime_ns

    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    out, err = capsys.readouterr()

    assert config.read_bytes() == before
    assert config.stat().st_mtime_ns == stamp
    assert "applied the per-seat policy" not in err


def test_rig_reports_a_seat_the_policy_table_does_not_cover(
    env, tmp_path, capsys, monkeypatch
):
    """A governed rig with an unlisted seat is a real gap, and silence would hide it."""
    spec = importlib.util.spec_from_file_location(
        "openrig_seat_policy_for_test", POLICY_MODULE_PATH
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.SEAT_ROLES["room1"] = {"somebody-else": "dev"}
    monkeypatch.setattr(seat_sync, "load_seat_policy", lambda: module)
    _omp_rig(env, monkeypatch, tmp_path)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert "room1/seat1 has no role" in err and "unpoliced" in err
    config = _omp_config_path(tmp_path)
    if config.exists():
        assert "bash" not in (yaml.safe_load(config.read_text()) or {})


def test_rig_leaves_a_rig_outside_the_policy_table_alone(
    env, tmp_path, capsys, monkeypatch
):
    """The table is a registry: a rig it does not govern gets no policy and no editorial line."""
    _omp_rig(env, monkeypatch, tmp_path)

    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    out, err = capsys.readouterr()

    # No editorial line and no policy: the render's own installs may still be reported.
    assert "no role" not in err and "applied the per-seat policy" not in err
    assert "unpoliced" not in err
    config = _omp_config_path(tmp_path)
    if config.exists():
        assert "bash" not in (yaml.safe_load(config.read_text()) or {})


def _omp_rig(
    env,
    monkeypatch,
    tmp_path,
    with_render=True,
    with_config=True,
    with_agents=False,
    make_agent_dir=True,
):
    """A rig whose seat render may carry any of the omp files, plus a state root for the seat."""
    monkeypatch.setattr(seat_sync, "OMP_STATE_ROOT", tmp_path / "ompstate")
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_A}])
    files = list(FILES)
    if with_render:
        files.append(
            {"path": "runtime/omp-mcp.json", "content": json.dumps(OMP_MCP_DOCUMENT)}
        )
    if with_config:
        files.append({"path": "runtime/omp-config.yml", "content": OMP_CONFIG_FRAGMENT})
    if with_agents:
        files.append({"path": "AGENTS.md", "content": AGENTS_MD_DOCUMENT})
    env.set_seat(HASH_A, files=files)
    if make_agent_dir:
        agent_dir = _omp_agent_dir(tmp_path)
        agent_dir.mkdir(parents=True)
    return _omp_agent_dir(tmp_path) / ".mcp.json"


def test_rig_installs_the_rendered_omp_mcp_document_verbatim(
    env, tmp_path, capsys, monkeypatch
):
    """The rendered document is installed into the seat's OWN agent directory, byte for byte.

    Verbatim matters: the renderer resolves the platform URL and leaves every other variable alone,
    so `Bearer ${AGENTHUB_TOKEN}` must arrive as literal text for the runtime to expand.
    """
    installed = _omp_rig(env, monkeypatch, tmp_path)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert out.strip().splitlines() == [
        f"rig:{tmp_path / 'room1' / 'rig' / 'rig.yaml'}"
    ]
    assert "installed" in err and str(installed) in err
    assert json.loads(installed.read_text()) == OMP_MCP_DOCUMENT
    assert "${AGENTHUB_TOKEN}" in installed.read_text()


def test_rig_leaves_the_operators_rig_root_mcp_json_alone(
    env, tmp_path, capsys, monkeypatch
):
    """The operator's file is never opened for writing: it is read by every seat and holds their own
    servers, so a render must add to the seat's directory and touch nothing at the rig root."""
    _omp_rig(env, monkeypatch, tmp_path)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    operator_file = tmp_path / "room1" / "rig" / ".mcp.json"
    operator_file.write_text(
        '{"mcpServers": {"deepseek": {"type": "stdio", "command": "x"}}}\n'
    )
    before = operator_file.read_bytes()

    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    assert operator_file.read_bytes() == before


def test_rig_sets_the_omp_startup_setting_without_clobbering_other_keys(
    env, tmp_path, capsys, monkeypatch
):
    """THE MERGE, and this is the destructive-failure test.

    `<agent dir>/config.yml` is OMP'S OWN settings file, so it may already hold settings an operator
    or another seat put there. Copying the rendered file over it would delete them - the runtime's own
    `omp config set` was measured to merge at the KEY level and leave an unrelated key alone, and this
    install has to do the same.
    """
    _omp_rig(env, monkeypatch, tmp_path)
    config = _omp_config_path(tmp_path)
    config.write_text("mcp:\n  renderMarkdownResults: true\nmodel: something-else\n")

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    merged = yaml.safe_load(config.read_text())
    assert merged["mcp"]["startupTimeoutMs"] == 0, "the rendered key was not set"
    assert (
        merged["mcp"]["renderMarkdownResults"] is True
    ), "an unrelated nested key was lost"
    assert merged["model"] == "something-else", "an unrelated top-level key was lost"
    assert str(config) in err


def test_rig_writes_the_omp_config_verbatim_when_the_seat_has_none(
    env, tmp_path, capsys, monkeypatch
):
    """A seat with no config file gets the render's own bytes in a new file, not a re-serialised
    equivalent: the fresh case is byte-exact."""
    _omp_rig(env, monkeypatch, tmp_path)

    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    assert _omp_config_path(tmp_path).read_text() == OMP_CONFIG_FRAGMENT


def test_rig_omp_config_merge_is_idempotent(env, tmp_path, capsys, monkeypatch):
    """Idempotence is SEMANTIC for this file: when the key already has the rendered value nothing is
    written and the mtime is untouched, so a rebuild is not a diff."""
    _omp_rig(env, monkeypatch, tmp_path)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()
    config = _omp_config_path(tmp_path)
    before = config.read_bytes()
    stamp = config.stat().st_mtime_ns

    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    out, err = capsys.readouterr()

    assert config.read_bytes() == before
    assert config.stat().st_mtime_ns == stamp
    assert str(config) not in err


def test_rig_warns_when_only_half_the_omp_render_is_present(
    env, tmp_path, capsys, monkeypatch
):
    """The asymmetry between the two files must never be silent: a render carrying the document but
    not the startup setting mounts servers omp may not wait for (the pre-9b0e55ac shape), and the
    other direction waits for servers the seat has no document for."""
    installed = _omp_rig(env, monkeypatch, tmp_path, with_config=False)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert installed.exists(), "the half that IS rendered must still be installed"
    assert not _omp_config_path(tmp_path).exists()
    assert "warning:" in err and "runtime/omp-config.yml" in err and "HALF" in err


def test_rig_install_is_idempotent(env, tmp_path, capsys, monkeypatch):
    """A rebuild that renders the same document must not rewrite the file: a rebuild is not a diff."""
    installed = _omp_rig(env, monkeypatch, tmp_path)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()
    first = installed.read_bytes()
    stamp = installed.stat().st_mtime_ns

    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    out, err = capsys.readouterr()

    assert installed.read_bytes() == first
    assert installed.stat().st_mtime_ns == stamp
    assert "installed" not in err


def test_rig_installs_the_rendered_guide_document_verbatim(
    env, tmp_path, capsys, monkeypatch
):
    """Packet 6 step 1's delivery half: the render's AGENTS.md reaches the agent directory.

    Byte for byte, because the guides' headings are the blocks' own and a re-serialised copy would be
    a second place the text could drift.
    """
    _omp_rig(env, monkeypatch, tmp_path, with_agents=True)
    installed = _omp_agent_dir(tmp_path) / "AGENTS.md"

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert "installed" in err and str(installed) in err
    assert installed.read_text() == AGENTS_MD_DOCUMENT

    # A rebuild that renders the same guide is not a diff.
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    out, err = capsys.readouterr()
    assert str(installed) not in err


def test_rig_installs_the_guide_document_for_a_seat_with_no_mcp_block(
    env, tmp_path, capsys, monkeypatch
):
    """The guide is INDEPENDENT of the MCP pair, and the half-render warning must know that.

    A seat with guide blocks and no ``mcp`` block renders only AGENTS.md. Before the third mode
    existed the half-render check counted FILES rather than the MCP pair, so this seat would have been
    reported as missing half its MCP setup - a warning about something it never had.
    """
    _omp_rig(
        env,
        monkeypatch,
        tmp_path,
        with_render=False,
        with_config=False,
        with_agents=True,
    )
    installed = _omp_agent_dir(tmp_path) / "AGENTS.md"

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert installed.read_text() == AGENTS_MD_DOCUMENT
    assert "HALF" not in err, err
    assert not (_omp_agent_dir(tmp_path) / ".mcp.json").exists()
    assert not _omp_config_path(tmp_path).exists()


def test_rig_writes_nothing_for_a_seat_with_no_mcp_block(
    env, tmp_path, capsys, monkeypatch
):
    """A seat with no mcp block renders no document, so nothing is installed for it - the second half
    of the acceptance, satisfied by absence rather than by an empty file."""
    installed = _omp_rig(
        env, monkeypatch, tmp_path, with_render=False, with_config=False
    )

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 0
    assert not installed.exists()
    assert "installed" not in err


def test_rig_refuses_when_the_seat_agent_directory_is_absent(
    env, tmp_path, capsys, monkeypatch
):
    """A seat that has not been launched has no agent directory, and the install must say so rather
    than miss quietly: the refusal names the path, the SEQUENCE that creates it, and the possibility
    that the launch used a different --state-root. Nothing is written, including the rig itself."""
    installed = _omp_rig(env, monkeypatch, tmp_path, make_agent_dir=False)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 2
    # The message names the missing DIRECTORY rather than the file that would go in it: the
    # directory is what is absent, and it is what the operator has to look for.
    assert str(_omp_agent_dir(tmp_path)) in err
    assert "launched" in err and "--state-root" in err
    assert not installed.exists()
    # Validated before the build wrote anything: no half-applied rig directory.
    assert not (tmp_path / "room1" / "rig").exists()


def test_rig_update_moves_pin_and_materialized_agent(env, tmp_path, capsys):
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_A}])
    env.set_seat(HASH_A)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_B}])
    env.set_seat(HASH_B, files=[{"path": "docs/readme.md", "content": "newer"}])
    code = run_cli(["rig", "room1", "--out", str(tmp_path), "--update"])
    out, err = capsys.readouterr()

    seat_dir = tmp_path / "room1" / "seat1"
    rig_dir = tmp_path / "room1" / "rig"
    assert code == 0
    assert err == ""
    assert json.loads((seat_dir / "pinned.json").read_text())["hash"] == HASH_B
    agent = rig_dir / "agents" / "seat1"
    assert (agent / "docs/readme.md").read_text() == "newer"
    assert json.loads((agent / "pinned.json").read_text())["hash"] == HASH_B
    assert out.strip().splitlines() == [f"rig:{rig_dir / 'rig.yaml'}"]


def test_rig_missing_pinned_dir_exits_1(env, tmp_path, capsys):
    seat_dir = tmp_path / "room1" / "seat1"
    seat_dir.mkdir(parents=True)
    (seat_dir / "pinned.json").write_text(
        json.dumps({"hash": HASH_A, "path": str(seat_dir / HASH_A)})
    )
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_B}])
    env.set_seat(HASH_B)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 1
    assert out == ""
    assert "seat seat1 could not be pulled" in err
    assert "pinned seat directory is missing" in err
    assert not (tmp_path / "room1" / "rig").exists()


def test_rig_empty_room_409_exits_1_with_server_message(env, tmp_path, capsys):
    env.set_rigspec(status=409, error="room has no active seats")

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 1
    assert out == ""
    assert "HTTP 409" in err
    assert "room has no active seats" in err
    assert not (tmp_path / "room1" / "rig").exists()


@pytest.mark.parametrize("bad_name", ["../etc", "_x", "", "a/b", "room.1", "room name"])
def test_rig_unsafe_room_name_rejected(env, tmp_path, capsys, bad_name):
    env.set_rigspec()

    code = run_cli(["rig", bad_name, "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 2
    assert out == ""
    assert "invalid room name" in err


def test_rig_unsafe_seat_name_rejected(env, tmp_path, capsys):
    env.set_rigspec(RIG_YAML, [{"seat": "seat.1", "hash": HASH_A}])

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 2
    assert out == ""
    assert "invalid seat name" in err
    assert not (tmp_path / "room1" / "rig").exists()


def test_rig_failed_seat_pull_leaves_previous_rig_untouched(env, tmp_path, capsys):
    env.set_rigspec(RIG_YAML, [{"seat": "seat1", "hash": HASH_A}])
    env.set_seat(HASH_A)
    assert run_cli(["rig", "room1", "--out", str(tmp_path)]) == 0
    capsys.readouterr()

    rig_dir = tmp_path / "room1" / "rig"
    old_yaml = (rig_dir / "rig.yaml").read_text()

    env.set_rigspec(RIG_YAML + "# changed\n", [{"seat": "seat1", "hash": HASH_B}])
    env.set_seat(HASH_B, status=500)
    code = run_cli(["rig", "room1", "--out", str(tmp_path)])
    out, err = capsys.readouterr()

    assert code == 1
    assert out == ""
    assert "seat seat1 could not be pulled" in err
    # The previous rig directory was not touched or partially overwritten.
    assert (rig_dir / "rig.yaml").read_text() == old_yaml
    agent = rig_dir / "agents" / "seat1"
    assert agent.is_dir() and not agent.is_symlink()
    assert json.loads((agent / "pinned.json").read_text())["hash"] == HASH_A
    leftovers = [
        p.name for p in (tmp_path / "room1").iterdir() if p.name.startswith(".rig.")
    ]
    assert leftovers == []


def test_bundle_without_lock_exits_2(monkeypatch, tmp_path, capsys):
    monkeypatch.setattr(seat_sync, "DEFAULT_OUT", tmp_path / "seats")

    code = run_cli(
        [
            "bundle",
            "room1",
            "seat1",
            "--rig-yaml",
            str(tmp_path / "rig.yaml"),
            "--rig-root",
            str(tmp_path),
            "--out-dir",
            str(tmp_path / "bundles"),
        ]
    )
    out, err = capsys.readouterr()

    assert code == 2
    assert out == ""
    assert "no pinned snapshot" in err


def test_bundle_uses_pinned_hash_and_argument_list(monkeypatch, tmp_path, capsys):
    seats = tmp_path / "seats"
    seat_dir = seats / "room1" / "seat1"
    seat_dir.mkdir(parents=True)
    (seat_dir / "pinned.json").write_text(
        json.dumps({"hash": HASH_LONG, "path": str(seat_dir / HASH_LONG)})
    )
    monkeypatch.setattr(seat_sync, "DEFAULT_OUT", seats)

    calls = []

    def fake_run(command, **kwargs):
        calls.append((list(command), dict(kwargs)))
        return subprocess.CompletedProcess(command, 0)

    monkeypatch.setattr(seat_sync.subprocess, "run", fake_run)

    rig_yaml = tmp_path / "rig.yaml"
    rig_root = tmp_path / "rig-root"
    out_dir = tmp_path / "bundles"
    code = run_cli(
        [
            "bundle",
            "room1",
            "seat1",
            "--rig-yaml",
            str(rig_yaml),
            "--rig-root",
            str(rig_root),
            "--out-dir",
            str(out_dir),
        ]
    )
    out, err = capsys.readouterr()

    bundle_path = out_dir / f"room1-seat1-{HASH_LONG[:8]}.rigbundle"
    assert code == 0
    assert err == ""
    assert out.strip() == str(bundle_path)
    assert calls == [
        (
            [
                "rig",
                "bundle",
                "create",
                str(rig_yaml),
                "--rig-root",
                str(rig_root),
                "-o",
                str(bundle_path),
                "--name",
                "room1-seat1",
                "--bundle-version",
                "1.0.0",
                "--notes",
                f"resolved_sha256={HASH_LONG} source=4genthub",
            ],
            {"check": True},
        )
    ]


def test_help_states_offline_use_is_manual(capsys):
    code = run_cli(["--help"])
    out, _ = capsys.readouterr()

    assert code == 0
    assert "rig up <bundle> --target <dir>" in out
    assert "never falls back" in out


# --- switch -----------------------------------------------------------------

PS_NODES = [
    {
        "rigName": "room1",
        "logicalId": "main.other",
        "canonicalSessionName": "other-1@room1",
    },
    {
        "rigName": "room1",
        "logicalId": "main.seat1",
        "canonicalSessionName": "seat1-2@room1",
    },
]


class FakeRig:
    """Stands in for subprocess.run; records every rig command in order."""

    def __init__(self, nodes=None, missing=False):
        self.nodes = PS_NODES if nodes is None else nodes
        self.missing = missing
        self.calls = []

    def __call__(self, command, **kwargs):
        self.calls.append(list(command))
        if self.missing:
            raise FileNotFoundError("rig")
        stdout = json.dumps(self.nodes) if command[1] == "ps" else ""
        return subprocess.CompletedProcess(command, 0, stdout=stdout, stderr="")

    def seat_calls(self):
        return [c for c in self.calls if c[1] == "seat"]


@pytest.fixture
def rig(monkeypatch):
    fake = FakeRig()
    monkeypatch.setattr(seat_sync.subprocess, "run", fake)
    return fake


def occupant(runtime="claude-code", model="old-model"):
    return {"seat_key": "seat1", "runtime": runtime, "model": model}


def setup_switch(env, **kwargs):
    env.set_occupants([occupant(**kwargs)])
    env.set_put()


def test_switch_model_puts_body_keeps_runtime_and_sets_model(env, rig, capsys):
    setup_switch(env)

    code = run_cli(
        ["switch", "room1", "seat1", "--model", "new-model", "--reason", "why"]
    )
    out, err = capsys.readouterr()

    assert code == 0
    assert env.puts == [
        (
            "/api/v2/openrig/rooms/room1/seats/seat1/occupant",
            {"runtime": "claude-code", "model": "new-model"},
            "Bearer test-token",
        )
    ]
    assert rig.seat_calls() == [
        [
            "rig",
            "seat",
            "set-model",
            "seat1-2@room1",
            "--model",
            "new-model",
            "--reason",
            "why",
        ]
    ]
    assert (
        out.strip()
        == "switched:room1/seat1 runtime=claude-code model=new-model applied=set-model"
    )
    assert "test-token" not in out + err


def test_switch_accepts_the_agy_runtime(env, rig, capsys):
    setup_switch(env)

    code = run_cli(["switch", "room1", "seat1", "--runtime", "agy"])
    out, _ = capsys.readouterr()

    assert code == 0
    assert env.puts[0][1] == {"runtime": "agy", "model": "old-model"}
    assert (
        out.strip() == "switched:room1/seat1 runtime=agy model=old-model applied=manual"
    )


def test_switch_runtime_only_keeps_current_model_and_prints_manual_steps(
    env, rig, capsys
):
    setup_switch(env)

    code = run_cli(["switch", "room1", "seat1", "--runtime", "codex"])
    out, err = capsys.readouterr()

    assert code == 0
    assert env.puts[0][1] == {"runtime": "codex", "model": "old-model"}
    assert rig.calls == []
    assert "rig down room1" in err
    assert (
        out.strip()
        == "switched:room1/seat1 runtime=codex model=old-model applied=manual"
    )


def test_switch_explicit_apply_none_with_runtime_change_skips_manual(env, rig, capsys):
    setup_switch(env)

    code = run_cli(
        ["switch", "room1", "seat1", "--runtime", "codex", "--apply", "none"]
    )
    out, err = capsys.readouterr()

    assert code == 0
    assert rig.calls == []
    assert err == ""
    assert out.strip().endswith("applied=none")


def test_switch_restart_stops_then_launches_fresh_with_warning(env, rig, capsys):
    setup_switch(env)

    code = run_cli(
        [
            "switch",
            "room1",
            "seat1",
            "--model",
            "new-model",
            "--apply",
            "restart",
            "--reason",
            "r",
        ]
    )
    out, err = capsys.readouterr()

    assert code == 0
    assert [c[2] for c in rig.seat_calls()] == ["set-model", "stop", "launch"]
    assert rig.seat_calls()[1] == [
        "rig",
        "seat",
        "stop",
        "seat1-2@room1",
        "--reason",
        "r",
    ]
    assert rig.seat_calls()[2] == [
        "rig",
        "seat",
        "launch",
        "seat1-2@room1",
        "--fresh",
        "--reason",
        "r",
    ]
    assert "warning" in err and "loses its live context" in err
    assert out.strip().endswith("applied=restart")


def test_switch_runtime_change_prints_manual_steps_and_skips_rig(env, rig, capsys):
    setup_switch(env)

    code = run_cli(["switch", "room1", "seat1", "--runtime", "codex", "--model", "m2"])
    out, err = capsys.readouterr()

    assert code == 0
    assert rig.calls == []
    assert "4genteam sync rig room1 --update" in err
    assert "rig down room1" in err
    assert "rig up" in err
    assert out.strip() == "switched:room1/seat1 runtime=codex model=m2 applied=manual"


def test_switch_apply_none_never_calls_rig(env, rig, capsys):
    setup_switch(env)

    code = run_cli(
        ["switch", "room1", "seat1", "--model", "new-model", "--apply", "none"]
    )
    out, _ = capsys.readouterr()

    assert code == 0
    assert rig.calls == []
    assert out.strip().endswith("applied=none")


def test_switch_missing_rig_binary_records_cloud_and_exits_0(env, monkeypatch, capsys):
    setup_switch(env)
    monkeypatch.setattr(seat_sync.subprocess, "run", FakeRig(missing=True))

    code = run_cli(["switch", "room1", "seat1", "--model", "new-model"])
    out, err = capsys.readouterr()

    assert code == 0
    assert len(env.puts) == 1
    assert "`rig` not found" in err
    assert out.strip().endswith("applied=none")


def test_switch_seat_not_in_rig_ps_exits_0(env, monkeypatch, capsys):
    setup_switch(env)
    fake = FakeRig(nodes=[])
    monkeypatch.setattr(seat_sync.subprocess, "run", fake)

    code = run_cli(["switch", "room1", "seat1", "--model", "new-model"])
    out, err = capsys.readouterr()

    assert code == 0
    assert "not in `rig ps`" in err
    assert fake.seat_calls() == []
    assert out.strip().endswith("applied=none")


def test_switch_rig_command_failure_exits_1(env, monkeypatch, capsys):
    setup_switch(env)

    def failing(command, **kwargs):
        if command[1] == "seat":
            raise subprocess.CalledProcessError(3, command)
        return subprocess.CompletedProcess(command, 0, stdout=json.dumps(PS_NODES))

    monkeypatch.setattr(seat_sync.subprocess, "run", failing)

    code = run_cli(["switch", "room1", "seat1", "--model", "new-model"])
    out, err = capsys.readouterr()

    assert code == 1
    assert out == ""
    assert "exit code 3" in err


@pytest.mark.parametrize("status", [404, 409])
def test_switch_put_error_exits_1(env, rig, capsys, status):
    env.set_occupants([occupant()])
    env.set_put(status=status)

    code = run_cli(["switch", "room1", "seat1", "--model", "new-model"])
    out, err = capsys.readouterr()

    assert code == 1
    assert out == ""
    assert f"HTTP {status}" in err
    assert rig.calls == []
    assert "test-token" not in err


def test_switch_unknown_seat_in_cloud_exits_1(env, rig, capsys):
    env.set_occupants([])

    code = run_cli(["switch", "room1", "seat1", "--model", "new-model"])
    _, err = capsys.readouterr()

    assert code == 1
    assert "not found in the cloud" in err
    assert env.puts == []


@pytest.mark.parametrize(
    "argv, message",
    [
        (["switch", "room1", "seat1"], "at least one of"),
        (["switch", "room1", "seat1", "--runtime", "gpt"], "invalid runtime"),
        (["switch", "room1", "seat1", "--model=-bad"], "invalid model"),
        (["switch", "room1", "seat1", "--model", ""], "invalid model"),
        (["switch", "../x", "seat1", "--model", "m"], "invalid room name"),
        (["switch", "room1", "se.at", "--model", "m"], "invalid seat name"),
    ],
)
def test_switch_usage_errors_exit_2(env, rig, capsys, argv, message):
    code = run_cli(argv)
    out, err = capsys.readouterr()

    assert code == 2
    assert out == ""
    assert message in err
    assert env.puts == [] and rig.calls == []


def test_switch_invalid_apply_choice_exits_2(env, rig, capsys):
    code = run_cli(["switch", "room1", "seat1", "--model", "m", "--apply", "bogus"])

    assert code == 2


# --- rig permission policy comes from the spec --------------------------------

RIGSPEC_PATH = "/api/v2/openrig/rooms/room1/rigspec"


def test_rig_requests_plain_path_and_has_no_permission_policy_flag(
    env, tmp_path, capsys
):
    env.set_rigspec()
    env.set_seat(HASH_A)

    code = run_cli(["rig", "room1", "--out", str(tmp_path)])

    assert code == 0
    assert RIGSPEC_PATH in env.gets
    assert not any("permission_policy" in g for g in env.gets)

    code = run_cli(
        ["rig", "room1", "--out", str(tmp_path), "--permission-policy", "yolo"]
    )
    _, err = capsys.readouterr()

    assert code == 2
    assert "unrecognized arguments" in err


# --- seat checker binary ---


@pytest.mark.parametrize("command", [["pull", "room1", "seat1"], ["rig", "room1"]])
def test_pull_and_rig_fail_loudly_without_the_link(
    env, real_checker_requirement, checker_home, tmp_path, capsys, command
):
    env.set_seat(HASH_A)
    pins = tmp_path / "pins"
    code = run_cli([*command, "--out", str(pins)])
    _, err = capsys.readouterr()
    assert code == 2
    assert str(pins / "bin" / "seatcheck") in err
    assert "install-checker" in err and str(checker_home) in err
    assert "rig daemon stop" in err and "inherit" in err
    assert env.gets == []
    assert not (pins / "room1").exists()


def test_pull_fails_when_seatcheck_resolves_elsewhere(
    env, real_checker_requirement, checker_home, tmp_path, capsys
):
    pins = tmp_path / "pins"
    make_binary(pins)
    other = make_binary(tmp_path / "other")
    (checker_home / "seatcheck").symlink_to(other)
    assert run_cli(["pull", "room1", "seat1", "--out", str(pins)]) == 2
    err = capsys.readouterr().err
    assert str(other) in err and "install-checker" in err


def test_pull_runs_when_the_link_points_at_the_store_binary(
    env, real_checker_requirement, checker_home, tmp_path, capsys
):
    env.set_seat(HASH_A)
    pins = tmp_path / "pins"
    (checker_home / "seatcheck").symlink_to(make_binary(pins))
    assert run_cli(["pull", "room1", "seat1", "--out", str(pins)]) == 0


def fake_go_build(calls):
    def run(command, **kwargs):
        calls.append((command, kwargs))
        make_binary(Path(command[command.index("-o") + 1]).parent.parent)
        return subprocess.CompletedProcess(command, 0)

    return run


def test_install_checker_builds_and_links_on_path(
    real_checker_requirement, checker_home, monkeypatch, tmp_path, capsys
):
    pins = tmp_path / "pins"
    old = make_binary(tmp_path / "old")
    (checker_home / "seatcheck").symlink_to(old)
    calls = []
    monkeypatch.setattr(seat_sync.subprocess, "run", fake_go_build(calls))

    code = run_cli(["install-checker", "--out", str(pins)])
    out, _ = capsys.readouterr()

    binary = pins.resolve() / "bin" / "seatcheck"
    assert code == 0
    assert out.strip() == f"checker:{binary}"
    assert (checker_home / "seatcheck").resolve() == binary
    ((command, kwargs),) = calls
    assert command == [
        "go",
        "build",
        "-ldflags",
        f"-X main.installedPinsDir={pins.resolve()}",
        "-o",
        str(binary),
        "./cmd/seatcheck",
    ]
    assert (
        kwargs["cwd"]
        == seat_sync.AGENTHUB_GO_DIR
        == MODULE_PATH.parents[3] / "agenthub_go"
    )
    assert kwargs["env"]["GOCACHE"] == str(seat_sync.AGENTHUB_GO_DIR / ".gocache")
    assert kwargs["env"]["TMPDIR"] == str(seat_sync.AGENTHUB_GO_DIR / ".gotmp")
    assert kwargs["check"] is True
    # The installed link now satisfies pull's check.
    assert seat_sync.resolve_checker(pins.resolve())


def test_install_checker_fails_loudly_when_the_link_dir_is_not_on_path(
    real_checker_requirement, checker_home, monkeypatch, tmp_path, capsys
):
    monkeypatch.setenv("PATH", str(tmp_path / "elsewhere"))
    monkeypatch.setattr(seat_sync.subprocess, "run", fake_go_build([]))
    assert run_cli(["install-checker", "--out", str(tmp_path / "pins")]) == 2
    err = capsys.readouterr().err
    assert f"add {checker_home} to PATH" in err
    assert "rig daemon stop" in err and "restart the daemon" in err
    assert (checker_home / "seatcheck").is_symlink()


def test_install_checker_without_go_exits_2_and_links_nothing(
    checker_home, monkeypatch, tmp_path, capsys
):
    def no_go(command, **kwargs):
        raise FileNotFoundError("go")

    monkeypatch.setattr(seat_sync.subprocess, "run", no_go)
    assert run_cli(["install-checker", "--out", str(tmp_path / "pins")]) == 2
    assert "go is not installed" in capsys.readouterr().err
    assert not (checker_home / "seatcheck").exists()


def test_install_checker_build_failure_exits_1_and_links_nothing(
    checker_home, monkeypatch, tmp_path, capsys
):
    def failing(command, **kwargs):
        raise subprocess.CalledProcessError(3, command)

    monkeypatch.setattr(seat_sync.subprocess, "run", failing)
    assert run_cli(["install-checker", "--out", str(tmp_path / "pins")]) == 1
    assert "go build failed with exit code 3" in capsys.readouterr().err
    assert not (checker_home / "seatcheck").exists()


def fake_tmux(stdout="", returncode=0, missing=False, hangs=False):
    calls = []

    def run(command, **kwargs):
        calls.append(command)
        if missing:
            raise FileNotFoundError("tmux")
        if hangs:
            assert kwargs["timeout"] == 5
            raise subprocess.TimeoutExpired(command, kwargs["timeout"])
        return subprocess.CompletedProcess(command, returncode, stdout=stdout)

    return run, calls


def test_tmux_global_path_reads_the_global_environment(monkeypatch):
    run, calls = fake_tmux("PATH=/a/bin:/b/bin\n")
    monkeypatch.setattr(seat_sync.subprocess, "run", run)
    assert REAL_TMUX_GLOBAL_PATH() == "/a/bin:/b/bin"
    assert calls == [["tmux", "show-environment", "-g", "PATH"]]


@pytest.mark.parametrize(
    "kwargs",
    [
        {"returncode": 1},
        {"stdout": "-PATH\n"},
        {"missing": True},
        {"hangs": True},
    ],
)
def test_tmux_global_path_is_none_without_an_answer(monkeypatch, kwargs):
    run, _ = fake_tmux(**kwargs)
    monkeypatch.setattr(seat_sync.subprocess, "run", run)
    assert REAL_TMUX_GLOBAL_PATH() is None


def test_pull_checks_the_tmux_global_path_not_the_shell_path(
    env, real_checker_requirement, checker_home, monkeypatch, tmp_path, capsys
):
    env.set_seat(HASH_A)
    pins = tmp_path / "pins"
    (checker_home / "seatcheck").symlink_to(make_binary(pins))
    monkeypatch.setenv("PATH", str(tmp_path / "elsewhere"))
    monkeypatch.setattr(seat_sync, "tmux_global_path", lambda: str(checker_home))
    assert run_cli(["pull", "room1", "seat1", "--out", str(pins)]) == 0


def test_pull_fails_when_the_tmux_global_path_lacks_the_checker(
    env, real_checker_requirement, checker_home, monkeypatch, tmp_path, capsys
):
    pins = tmp_path / "pins"
    (checker_home / "seatcheck").symlink_to(make_binary(pins))
    monkeypatch.setattr(
        seat_sync, "tmux_global_path", lambda: str(tmp_path / "elsewhere")
    )
    assert run_cli(["pull", "room1", "seat1", "--out", str(pins)]) == 2
    err = capsys.readouterr().err
    assert "tmux global PATH" in err and "install-checker" in err


def test_shell_path_fallback_is_said_in_the_output(
    env, real_checker_requirement, checker_home, tmp_path, capsys
):
    env.set_seat(HASH_A)
    pins = tmp_path / "pins"
    (checker_home / "seatcheck").symlink_to(make_binary(pins))
    assert run_cli(["pull", "room1", "seat1", "--out", str(pins)]) == 0
    assert "no readable rig daemon PATH" in capsys.readouterr().err


def test_path_limit_says_only_the_default_tmux_socket_is_queried():
    assert "default tmux socket" in seat_sync.PATH_LIMIT


def _ps_node(**overrides):
    node = {
        "logicalId": "room1.alpha",
        "canonicalSessionName": "room1-alpha@room1",
        "sessionStatus": "running",
        "lifecycleState": "attention_required",
        "agentActivity": {"state": "unknown", "reason": "no_runtime_hook"},
    }
    node.update(overrides)
    return node


def test_respawn_launches_only_when_the_seat_reads_dead(monkeypatch, capsys):
    calls = []

    def fake_rig(command):
        calls.append(command)
        if command[:4] == ["rig", "ps", "--json", "--nodes"]:
            return subprocess.CompletedProcess(command, 0, json.dumps([_ps_node()]), "")
        return subprocess.CompletedProcess(command, 0, "launched", "")

    monkeypatch.setattr(seat_sync, "run_rig", fake_rig)
    code = run_cli(
        ["respawn", "room1", "alpha", "--after-seconds", "0", "--reason", "agent died"]
    )
    out, err = capsys.readouterr()

    assert code == 0
    assert err == ""
    assert out.strip() == "launched"
    assert [
        "rig",
        "seat",
        "launch",
        "room1-alpha@room1",
        "--fresh",
        "--stop",
        "--reason",
        "agent died",
    ] in calls


def test_respawn_reports_success_when_the_launch_warned_but_the_seat_came_up(
    monkeypatch, capsys
):
    """An automated caller must not read a successful respawn as a failure: rig can start the
    occupant and still exit non-zero (runtime identity notice), so the seat's own state decides."""
    ps_calls = []

    def fake_rig(command):
        if command[:4] == ["rig", "ps", "--json", "--nodes"]:
            ps_calls.append(command)
            node = (
                _ps_node()
                if len(ps_calls) == 1
                else _ps_node(
                    agentActivity={
                        "state": "running",
                        "reason": "window_activity_motion",
                    }
                )
            )
            return subprocess.CompletedProcess(command, 0, json.dumps([node]), "")
        raise subprocess.CalledProcessError(
            1,
            command,
            output="",
            stderr="Fresh occupant started but runtime identity requires attention\n",
        )

    monkeypatch.setattr(seat_sync, "run_rig", fake_rig)
    code = run_cli(["respawn", "room1", "alpha", "--after-seconds", "0"])
    out, err = capsys.readouterr()
    assert code == 0
    assert "respawned room1.alpha" in err
    assert "runtime identity requires attention" in err
    assert out == ""


def test_respawn_fails_when_the_seat_is_still_dead_after_the_launch(
    monkeypatch, capsys
):
    """The other half: if the launch failed and the seat still reads dead, it is a failure."""

    def fake_rig(command):
        if command[:4] == ["rig", "ps", "--json", "--nodes"]:
            return subprocess.CompletedProcess(command, 0, json.dumps([_ps_node()]), "")
        raise subprocess.CalledProcessError(
            1, command, output="", stderr="launch refused\n"
        )

    monkeypatch.setattr(seat_sync, "run_rig", fake_rig)
    code = run_cli(["respawn", "room1", "alpha", "--after-seconds", "0"])
    err = capsys.readouterr().err
    assert code == 1
    assert "rig seat launch reported: launch refused" in err


def test_respawn_refuses_a_live_seat(monkeypatch, capsys):
    """On a runtime that behaves like agy a just-launched seat reads exactly like a dead one until
    its runtime hook attaches (~15s), so anything that is not the dead reading must stop the
    command before it launches."""
    calls = []

    def fake_rig(command):
        calls.append(command)
        return subprocess.CompletedProcess(
            command,
            0,
            json.dumps(
                [_ps_node(agentActivity={"state": "idle"}, lifecycleState="running")]
            ),
            "",
        )

    monkeypatch.setattr(seat_sync, "run_rig", fake_rig)
    code = run_cli(["respawn", "room1", "alpha", "--after-seconds", "0"])
    err = capsys.readouterr().err

    assert code == 2
    assert "not in the dead-agent state" in err
    assert not any(command[:3] == ["rig", "seat", "launch"] for command in calls)


def test_respawn_refuses_an_unknown_seat(monkeypatch, capsys):
    def fake_rig(command):
        return subprocess.CompletedProcess(command, 0, json.dumps([_ps_node()]), "")

    monkeypatch.setattr(seat_sync, "run_rig", fake_rig)
    code = run_cli(["respawn", "room1", "beta", "--after-seconds", "0"])
    err = capsys.readouterr().err
    assert code == 2
    assert "lists no node room1.beta" in err


def test_respawn_surfaces_the_launch_message_instead_of_a_traceback(
    monkeypatch, capsys
):
    """`rig seat launch` can start the occupant and still exit 1 (runtime identity notice), so
    its own words must reach the operator."""

    def fake_rig(command):
        if command[:4] == ["rig", "ps", "--json", "--nodes"]:
            return subprocess.CompletedProcess(command, 0, json.dumps([_ps_node()]), "")
        if command[:3] == ["rig", "seat", "launch"]:
            raise subprocess.CalledProcessError(
                1,
                command,
                output="",
                stderr="Fresh occupant started but runtime identity requires attention\n",
            )
        return subprocess.CompletedProcess(command, 0, "", "")

    monkeypatch.setattr(seat_sync, "run_rig", fake_rig)
    code = run_cli(["respawn", "room1", "alpha", "--after-seconds", "0"])
    err = capsys.readouterr().err
    assert code == 1
    assert "rig seat launch reported: Fresh occupant started" in err


def _offline_target(tmp_path: Path) -> Path:
    """A materialized-bundle shape: two members share agent dev, one uses agent rev."""
    target = tmp_path / "mat"
    (target / "agents" / "dev").mkdir(parents=True)
    (target / "agents" / "rev").mkdir(parents=True)
    (target / "rig.yaml").write_text(
        "name: room1\n"
        "pods:\n"
        "  - id: main\n"
        "    members:\n"
        "      - id: alpha\n"
        "        agent_ref: local:agents/dev\n"
        "      - id: beta\n"
        "        agent_ref: local:agents/dev\n"
        "      - id: gamma\n"
        "        agent_ref: local:agents/rev\n"
    )
    (target / "agents" / "dev" / "policy.json").write_text(
        json.dumps({"Seat": "alpha", "Links": [{"Allow": True, "From": "alpha"}]})
    )
    (target / "agents" / "dev" / "pinned.json").write_text(json.dumps({"hash": HASH_A}))
    (target / "agents" / "rev" / "policy.json").write_text(
        json.dumps({"Seat": "gamma", "Links": []})
    )
    return target


def test_offline_install_writes_the_store_and_skips_a_foreign_policy(tmp_path, capsys):
    """seatcheck reads <home>/.openrig/agenthub-seats/<rig>/<member>/policy.json, so this is
    what makes a materialized bundle decidable offline. Members that share one seat type
    share one agent directory, so only one of their policies can ride there: installing must
    refuse a policy that names another seat instead of handing this seat the wrong links."""
    target = _offline_target(tmp_path)
    home = tmp_path / "home"

    code = run_cli(["offline-install", str(target), "--home", str(home)])
    out, err = capsys.readouterr()

    store = home / ".openrig" / "agenthub-seats"
    assert code == 0
    assert out.strip().splitlines() == [
        f"installed room1/alpha -> {store / 'room1' / 'alpha'}",
        f"installed room1/gamma -> {store / 'room1' / 'gamma'}",
    ]
    assert (
        json.loads((store / "room1" / "alpha" / "policy.json").read_text())["Seat"]
        == "alpha"
    )
    assert (
        json.loads((store / "room1" / "alpha" / "pinned.json").read_text())["hash"]
        == HASH_A
    )
    assert (
        json.loads((store / "room1" / "gamma" / "policy.json").read_text())["Seat"]
        == "gamma"
    )
    assert not (store / "room1" / "beta").exists()
    assert "skipped room1/beta" in err and "belongs to seat 'alpha'" in err


def test_offline_install_without_any_policy_fails_loudly(tmp_path, capsys):
    target = tmp_path / "mat"
    (target / "agents" / "dev").mkdir(parents=True)
    (target / "rig.yaml").write_text(
        "name: room1\n"
        "pods:\n"
        "  - id: main\n"
        "    members:\n"
        "      - id: alpha\n"
        "        agent_ref: local:agents/dev\n"
    )
    code = run_cli(["offline-install", str(target), "--home", str(tmp_path / "home")])
    err = capsys.readouterr().err
    assert code == 2
    assert "no pinned policy found" in err


def test_the_store_and_state_root_do_not_follow_home(monkeypatch, tmp_path):
    """The defaults resolve from the ACCOUNT, not from HOME.

    A seat is launched with HOME pointed at its own state directory
    (/home/<user>/.openrig/state/omp/<rig>-<seat>@<rig>), so a default built on Path.home()
    appends the state root to itself: the client writes into the seat's own tree while the send
    guard reads the account home, and the two halves of one mechanism disagree about where the
    data lives. Stated as an equality across two HOMEs, the shape the guard's own test uses.
    """
    seat_like = tmp_path / "4genthub-min-lead@4genthub-min"
    seat_like.mkdir()
    monkeypatch.setenv("HOME", str(seat_like))
    first = _load_module()

    assert not str(first.DEFAULT_OUT).startswith(str(seat_like))
    assert not str(first.OMP_STATE_ROOT).startswith(str(seat_like))
    assert not str(first.checker_link()).startswith(str(seat_like))
    assert str(first.DEFAULT_OUT).endswith("agenthub-seats")

    other = tmp_path / "another-home"
    other.mkdir()
    monkeypatch.setenv("HOME", str(other))
    second = _load_module()

    assert second.DEFAULT_OUT == first.DEFAULT_OUT
    assert second.OMP_STATE_ROOT == first.OMP_STATE_ROOT
    assert second.checker_link() == first.checker_link()
    assert second.real_home() == first.real_home()
