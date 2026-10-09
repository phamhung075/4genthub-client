"""Acceptance tests for the seatcheck guard, end to end.

Unlike the unit tests in test_seat_sync.py, these run the REAL binary that
`4genteam sync install-checker` builds and links, against a seat `pull`
materialized, so the pinned policy and the guard are exercised together. Each case is
classified the way G3's L2 ruling requires: an allowed peer is PERMITTED, a disallowed
peer is REFUSED with an audit row, and a forged direct `rig send` is DETECTED by
`audit-scan` — not prevented, because the guard is a strong default, not a boundary.
"""

import importlib.util
import json
import os
import shutil
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import pytest

# These tests are self-contained and must not spin up the test database.
pytestmark = pytest.mark.unit

MODULE_PATH = Path(__file__).resolve().parents[1] / "src" / "agenthub_client" / "seat_sync.py"

HASH_A = "a1" * 32
FILES = [{"path": "docs/readme.md", "content": "hello"}]

ACCEPT_RIG = "e2erig"
ACCEPT_SEAT = "alpha"
ACCEPT_PEER = "beta"
# A valid pinned policy: alpha may send a task to beta (delegates_to). ParsePolicy is strict,
# so the guard rejects anything else, which is why this is the real shape and not a stub.
ACCEPT_POLICY = {
    "Seat": ACCEPT_SEAT,
    "Links": [
        {"From": ACCEPT_SEAT, "To": ACCEPT_PEER, "Kind": "delegates_to", "Allow": True}
    ],
}


def _load_module():
    return importlib.import_module("agenthub_client.seat_sync")


seat_sync = _load_module()


class _SeatHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        route = self.server.routes.get(self.path.partition("?")[0])
        if route is None:
            self.send_error(404)
            return
        payload = json.dumps(route).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args):  # keep test output clean
        pass


class SeatServer:
    """Serves the one route a `pull` needs: the resolved seat, with its pinned policy."""

    def __init__(self):
        self.routes = {}
        self.httpd = HTTPServer(("127.0.0.1", 0), _SeatHandler)
        self.httpd.routes = self.routes
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return f"http://127.0.0.1:{self.httpd.server_port}"

    def set_seat(self, hash_value, room, seat, policy):
        self.routes[f"/api/v2/openrig/seats/{room}/{seat}"] = {
            "success": True,
            "resolved_seat": {
                "room": room,
                "seat": seat,
                "hash": hash_value,
                "runtime": "claude",
                "files": FILES,
                "policy": policy,
            },
        }

    def close(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.thread.join(timeout=5)


@pytest.fixture
def env(monkeypatch):
    """A stub cloud plus the PATH the guard checks. With no tmux server and no readable rig
    daemon PATH, `seat_path` returns this shell's PATH — where the fixture put the link."""
    server = SeatServer()
    monkeypatch.setenv("AGENTHUB_URL", server.url)
    monkeypatch.setenv("AGENTHUB_TOKEN", "test-token")
    monkeypatch.setattr(seat_sync, "tmux_global_path", lambda: None, raising=False)
    monkeypatch.setattr(seat_sync, "openrig_daemon_pid", lambda: None, raising=False)
    yield server
    server.close()


def run_cli(argv):
    try:
        return seat_sync.main(argv)
    except SystemExit as exc:
        return exc.code if isinstance(exc.code, int) else 1


def _write_fake_rig(bin_dir: Path, whoami_json: Path) -> Path:
    """A `rig` on PATH: `whoami --json` names the seat and its one peer, `send` appends the
    argv to $HOME/sent.log. seatcheck shells out to this, so what is under test is its
    decision and its audit, not a delivery stub."""
    bin_dir.mkdir(parents=True, exist_ok=True)
    whoami_json.write_text(
        json.dumps(
            {
                "identity": {"rigName": ACCEPT_RIG, "memberId": ACCEPT_SEAT},
                "peers": [
                    {
                        "logicalId": f"p.{ACCEPT_PEER}",
                        "sessionName": f"p-{ACCEPT_PEER}@{ACCEPT_RIG}",
                    }
                ],
            }
        )
    )
    rig = bin_dir / "rig"
    rig.write_text(
        "#!/bin/sh\n"
        'case "$1" in\n'
        f'  whoami) cat "{whoami_json}" ;;\n'
        '  send) printf \'SENT %s\\n\' "$*" >> "$HOME/sent.log" ;;\n'
        "esac\n"
    )
    rig.chmod(0o755)
    return rig


@pytest.fixture
def installed_checker(monkeypatch, tmp_path):
    """Install the checker the production way: build ./cmd/seatcheck into the seat store,
    link it from ~/.local/bin and put that directory on PATH, where a `rig` also answers.
    Skips when go is not installed, since the binary is the artifact under test."""
    go = shutil.which("go")
    if go is None:
        pytest.skip("go is not installed")
    # Read the real module cache before HOME moves; a scratch HOME would otherwise send
    # `go build` to the network for the module graph.
    module_cache = subprocess.run(
        ["go", "env", "GOMODCACHE"], capture_output=True, text=True
    ).stdout.strip()

    home = tmp_path / "home"
    store = home / ".openrig" / "agenthub-seats"
    link_dir = home / ".local" / "bin"
    link_dir.mkdir(parents=True)
    _write_fake_rig(link_dir, tmp_path / "whoami.json")

    # The link location is MACHINE-LEVEL and resolves from the passwd entry, not from HOME, so a
    # temp HOME can no longer redirect it: without this stub install-checker links into the REAL
    # ~/.local/bin while every assertion here looks in the temp one. Same seam, and the same
    # reason, as the sibling fixture in test_seat_sync.py.
    monkeypatch.setattr(seat_sync, "checker_link", lambda: link_dir / "seatcheck")
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv(
        "PATH",
        os.pathsep.join([str(link_dir), str(Path(go).parent), "/usr/bin", "/bin"]),
    )
    if module_cache:
        monkeypatch.setenv("GOMODCACHE", module_cache)

    assert run_cli(["install-checker", "--out", str(store)]) == 0
    return home, store


def _pulled_seat(env, store: Path) -> None:
    """Pull with the real checker requirement: this fails loudly unless the linked binary is
    the store's, which is how the test proves the guard and the policy are connected."""
    env.set_seat(HASH_A, policy=ACCEPT_POLICY, room=ACCEPT_RIG, seat=ACCEPT_SEAT)
    assert run_cli(["pull", ACCEPT_RIG, ACCEPT_SEAT, "--out", str(store)]) == 0


def _linked_seatcheck(home: Path) -> str:
    return str(home / ".local" / "bin" / "seatcheck")


def _audit_rows(store: Path) -> list[dict]:
    path = store / ACCEPT_RIG / ACCEPT_SEAT / "audit.jsonl"
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text().splitlines() if line]


def test_linked_guard_delivers_to_an_allowed_peer(env, installed_checker):
    """(b) PERMITTED, by policy: neither refused nor a bypass. The linked binary sends to the
    linked peer, exits 0, and writes the decision line and its delivered outcome."""
    home, store = installed_checker
    _pulled_seat(env, store)

    result = subprocess.run(
        [
            _linked_seatcheck(home),
            "send",
            "--to",
            ACCEPT_PEER,
            "--intent",
            "task",
            "--",
            "hello",
            "peer",
        ],
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0, result.stderr
    assert (home / "sent.log").read_text().strip() == (
        f"SENT send -- p-{ACCEPT_PEER}@{ACCEPT_RIG} hello peer"
    )
    rows = _audit_rows(store)
    assert [r["Allowed"] for r in rows] == [True, True]
    assert rows[0]["From"] == ACCEPT_SEAT and rows[0]["To"] == ACCEPT_PEER
    assert rows[0].get("Outcome", "") == ""
    assert rows[1]["Outcome"] == "delivered"


def test_linked_guard_refuses_a_disallowed_peer_and_audits_it(env, installed_checker):
    """(c) REFUSED with an audit row: an unlinked peer is denied (exit 3) before delivery,
    the audit carries the denied decision, and nothing was sent."""
    home, store = installed_checker
    _pulled_seat(env, store)

    result = subprocess.run(
        [
            _linked_seatcheck(home),
            "send",
            "--to",
            "gamma",
            "--intent",
            "task",
            "--",
            "hello",
            "gamma",
        ],
        capture_output=True,
        text=True,
    )

    assert result.returncode == 3, result.stderr
    assert result.stderr.startswith("denied: no link\n")
    assert result.stdout == ""
    assert not (home / "sent.log").exists()
    rows = _audit_rows(store)
    assert len(rows) == 1
    assert rows[0]["Allowed"] is False
    assert rows[0]["Reason"] == "no link"
    assert rows[0]["To"] == "gamma"
    assert "Outcome" not in rows[0]


def test_audit_scan_detects_a_forged_direct_rig_send(env, installed_checker, tmp_path):
    """(d) DETECTED, not refused: a direct `rig send` steps around the guard and writes no
    audit row, so the observed command line is its only trace. `audit-scan` flags it (exit 4)
    while a `seatcheck send` line stays clean — the net for an invisible bypass, not a block."""
    home, store = installed_checker
    _pulled_seat(env, store)
    observed = tmp_path / "observed.txt"
    observed.write_text(
        f"rig send p-{ACCEPT_PEER}@{ACCEPT_RIG} forged\n"
        f"seatcheck send --to {ACCEPT_PEER} --intent task -- hi\n"
    )

    result = subprocess.run(
        [
            _linked_seatcheck(home),
            "audit-scan",
            "--known",
            "seatcheck",
            "--file",
            str(observed),
        ],
        capture_output=True,
        text=True,
    )

    assert result.returncode == 4, result.stderr
    assert result.stdout == (
        f"bypass-suspected: rig send p-{ACCEPT_PEER}@{ACCEPT_RIG} forged\n"
    )
    # The forged send wrote no audit row — exactly why the scan has to flag it.
    assert _audit_rows(store) == []
