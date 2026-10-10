"""Tests for agenthub_client.bridge using fake tool runners and a local HTTP server."""

import importlib.util
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

pytestmark = pytest.mark.unit

MODULE_PATH = Path(__file__).resolve().parents[1] / "src" / "agenthub_client" / "bridge.py"

FAKE_TOKEN = "sk-FAKEFAKEFAKEFAKEFAKE12345678"


def _load_module():
    return importlib.import_module("agenthub_client.bridge")


bridge_mod = _load_module()


def rig_output(*extra_nodes):
    nodes = [
        {
            "rigName": "eng",
            "logicalId": "dev.coder",
            "runtime": "claude-code",
            "sessionStatus": "running",
            "lifecycleState": "running",
            "agentActivity": {"state": "running", "reason": "busy"},
            "cwd": f"/work/{FAKE_TOKEN}",
            "canonicalSessionName": f"dev-coder@eng-{FAKE_TOKEN}",
            "resumeToken": FAKE_TOKEN,
        },
        {
            "rigName": "eng",
            "logicalId": "dev.reviewer",
            "runtime": "weird-runtime",
            "sessionStatus": "running",
            "agentActivity": {"state": "needs_input"},
            "latestError": f"auth failed token={FAKE_TOKEN}",
        },
        *extra_nodes,
    ]
    return json.dumps(nodes)


def herdr_output(*agents):
    agents = agents or (
        {
            "agent": "claude",
            "agent_status": "working",
            "pane_id": "w5:p3",
            "cwd": f"/home/{FAKE_TOKEN}",
            "terminal_title": f"user@host: {FAKE_TOKEN}",
            "terminal_title_stripped": FAKE_TOKEN,
            "agent_session": {"value": FAKE_TOKEN},
        },
    )
    return json.dumps({"result": {"snapshot": {"agents": list(agents)}}})


def fake_runner(rig=None, herdr=None):
    def runner(argv):
        out = rig if argv[0] == "rig" else herdr
        if out is None:
            raise FileNotFoundError(argv[0])
        if isinstance(out, Exception):
            raise out
        return out

    return runner


class FakeClock:
    def __init__(self):
        self.now = 1000.0

    def __call__(self):
        return self.now


def make_bridge(tmp_path, runner, send, **kwargs):
    kwargs.setdefault("secrets", {"AGENTHUB_TOKEN": FAKE_TOKEN})
    return bridge_mod.Bridge(
        "pc-1", 20.0, send, runner=runner, pins_dir=tmp_path, **kwargs
    )


def test_payload_shape_state_and_runtime_mapping(tmp_path):
    bridge = make_bridge(
        tmp_path, fake_runner(rig_output(), herdr_output()), lambda b: (200, b"")
    )
    payload = bridge.build_payload()
    assert set(payload) == {"machine_id", "reported_at", "seats", "agents", "edges"}
    coder, reviewer = payload["seats"]
    assert (coder["room"], coder["seat"], coder["state"], coder["runtime"]) == (
        "eng",
        "coder",
        "running",
        "claude-code",
    )
    assert (reviewer["seat"], reviewer["state"], reviewer["runtime"]) == (
        "reviewer",
        "blocked",
        "unknown",
    )
    assert payload["agents"] == [
        {"agent": "claude", "status": "working", "pane_id": "w5:p3"}
    ]


@pytest.mark.parametrize(
    "node,expected",
    [
        (
            {"sessionStatus": "stopped", "agentActivity": {"state": "running"}},
            "stopped",
        ),
        ({"lifecycleState": "recoverable"}, "stopped"),
        # A dead agent, captured from the F4 reproduction (2026-10-04): the tmux session is still
        # running and OpenRig keeps lifecycleState attention_required, but the runtime hook is gone.
        # Owner decision 2026-10-05: this reads STOPPED (and the seat is respawned), not blocked
        # and not unknown.
        (
            {
                "sessionStatus": "running",
                "startupStatus": "ready",
                "lifecycleState": "attention_required",
                "agentActivity": {"state": "unknown", "reason": "no_runtime_hook"},
            },
            "stopped",
        ),
        # An omp-style runtime with no activity signal yet reports reason null, so it must NOT be
        # called dead: the no_runtime_hook reason is the discriminator. (An agy seat in the same
        # cold-start window does report it - that is what the respawn hold covers.)
        (
            {
                "sessionStatus": "running",
                "startupStatus": "ready",
                "lifecycleState": "running",
                "agentActivity": {"state": "unknown"},
            },
            "unknown",
        ),
        # attention_required alone is not a blocked signal; the agent's own activity decides.
        ({"lifecycleState": "attention_required"}, "unknown"),
        (
            {
                "lifecycleState": "attention_required",
                "agentActivity": {"state": "needs_input"},
            },
            "blocked",
        ),
        ({"startupStatus": "failed"}, "blocked"),
        ({"agentActivity": {"state": "idle"}}, "idle"),
        ({"agentActivity": {"state": "bogus"}}, "unknown"),
        ({}, "unknown"),
    ],
)
def test_seat_state_mapping(node, expected):
    assert bridge_mod.seat_state(node) == expected


@pytest.mark.parametrize(
    "runtime,expected",
    [
        ("claude-code", "claude-code"),
        ("codex", "codex"),
        ("agy", "agy"),
        ("terminal", "terminal"),
        ("weird-runtime", "unknown"),
    ],
)
def test_runtime_mapping_keeps_every_supported_runtime(tmp_path, runtime, expected):
    node = {
        "rigName": "eng",
        "logicalId": "dev.solo",
        "runtime": runtime,
        "agentActivity": {"state": "idle"},
    }
    bridge = make_bridge(
        tmp_path, fake_runner(json.dumps([node]), herdr_output()), lambda b: (200, b"")
    )
    (seat,) = bridge.build_payload()["seats"]
    assert seat["runtime"] == expected


def test_allow_list_never_sends_foreign_fields_or_secrets(tmp_path):
    sent = []
    bridge = make_bridge(
        tmp_path,
        fake_runner(rig_output(), herdr_output()),
        lambda b: sent.append(b) or 200,
    )
    bridge.cycle()
    body = sent[0].decode()
    assert FAKE_TOKEN not in body
    for leaked in (
        "cwd",
        "terminal_title",
        "agent_session",
        "resumeToken",
        "canonicalSessionName",
    ):
        assert leaked not in body
    payload = json.loads(body)
    assert set(payload["seats"][0]) == {
        "room",
        "seat",
        "state",
        "runtime",
        "pinned_hash",
        "detail",
        "redacted",
    }
    assert payload["seats"][1]["redacted"] is True
    assert "[REDACTED" in payload["seats"][1]["detail"]


def test_bad_names_are_skipped(tmp_path):
    bad = [
        {"rigName": "bad room", "logicalId": "x"},
        {"rigName": "eng", "logicalId": "../etc"},
        {"rigName": "eng", "logicalId": "dev.coder"},
        {"rigName": "ok", "logicalId": 7},
    ]
    agents = [
        {"agent": "bad name", "agent_status": "idle", "pane_id": "w1:p1"},
        {"agent": "claude", "agent_status": "idle", "pane_id": "../x"},
        {"agent": "codex", "agent_status": "weird", "pane_id": "w1:p2"},
    ]
    bridge = make_bridge(
        tmp_path,
        fake_runner(json.dumps(bad), herdr_output(*agents)),
        lambda b: (200, b""),
    )
    payload = bridge.build_payload()
    assert [(s["room"], s["seat"]) for s in payload["seats"]] == [("eng", "coder")]
    assert payload["agents"] == [
        {"agent": "codex", "status": "unknown", "pane_id": "w1:p2"}
    ]


def _payload_and_stderr(tmp_path, capsys, nodes):
    bridge = make_bridge(
        tmp_path, fake_runner(json.dumps(nodes), None), lambda b: (200, b"")
    )
    payload = bridge.build_payload()
    return payload, capsys.readouterr().err


def test_same_member_in_two_pods_of_one_rig_is_a_named_duplicate(tmp_path, capsys):
    nodes = [
        {"rigName": "4genthub-go", "logicalId": "dev.check"},
        {"rigName": "4genthub-go", "logicalId": "agy.check"},
    ]
    payload, err = _payload_and_stderr(tmp_path, capsys, nodes)
    assert [(s["room"], s["seat"]) for s in payload["seats"]] == [
        ("4genthub-go", "check")
    ]
    assert (
        "seat 'check' in rig 4genthub-go exists in pods agy and dev; rename one" in err
    )
    assert "invalid" not in err


def test_invalid_names_have_their_own_message(tmp_path, capsys):
    nodes = [
        {"rigName": "eng", "logicalId": "dev.coder"},
        {"rigName": "bad room", "logicalId": "dev.x"},
        {"rigName": "eng", "logicalId": "dev.../etc"},
    ]
    payload, err = _payload_and_stderr(tmp_path, capsys, nodes)
    assert [(s["room"], s["seat"]) for s in payload["seats"]] == [("eng", "coder")]
    assert "skipped 2 seat(s) with invalid names" in err
    assert "rename one" not in err


def test_duplicate_message_wording():
    msg = bridge_mod.duplicate_message
    assert msg("r", "check", ["dev", "agy", "ops"]) == (
        "seat 'check' in rig r exists in pods agy, dev and ops; rename one"
    )
    assert msg("r", "check", ["dev", "dev"]) == (
        "seat 'check' in rig r is listed twice in pod dev; rename one"
    )


def test_same_member_in_two_rigs_is_not_a_duplicate(tmp_path, capsys):
    nodes = [
        {"rigName": "a", "logicalId": "dev.check"},
        {"rigName": "b", "logicalId": "dev.check"},
    ]
    payload, err = _payload_and_stderr(tmp_path, capsys, nodes)
    assert [(s["room"], s["seat"]) for s in payload["seats"]] == [
        ("a", "check"),
        ("b", "check"),
    ]
    assert "skipped" not in err and "rename one" not in err


def test_pinned_hash_used_when_valid(tmp_path):
    pin = tmp_path / "eng" / "coder"
    pin.mkdir(parents=True)
    (pin / "pinned.json").write_text(json.dumps({"hash": "ab12"}))
    other = tmp_path / "eng" / "reviewer"
    other.mkdir(parents=True)
    (other / "pinned.json").write_text(json.dumps({"hash": "../evil"}))
    bridge = make_bridge(
        tmp_path, fake_runner(rig_output(), None), lambda b: (200, b"")
    )
    seats = bridge.build_payload()["seats"]
    assert [s["pinned_hash"] for s in seats] == ["ab12", ""]


def test_absent_tools_yield_empty_sources_and_log_once(tmp_path, capsys):
    bridge = make_bridge(tmp_path, fake_runner(None, None), lambda b: (200, b""))
    for _ in range(3):
        payload = bridge.build_payload()
    assert payload["seats"] == [] and payload["agents"] == []
    err = capsys.readouterr().err
    assert err.count("rig not installed") == 1
    assert err.count("herdr not installed") == 1


def test_failing_tool_is_absent_not_fatal(tmp_path):
    bridge = make_bridge(
        tmp_path, fake_runner(RuntimeError("exit 1"), "not json"), lambda b: (200, b"")
    )
    payload = bridge.build_payload()
    assert payload["seats"] == [] and payload["agents"] == []


def test_send_on_change_and_heartbeat(tmp_path):
    sent = []
    clock = FakeClock()
    runner_state = {"rig": rig_output()}

    def runner(argv):
        return runner_state["rig"] if argv[0] == "rig" else herdr_output()

    bridge = make_bridge(
        tmp_path, runner, lambda b: (sent.append(b) or 200, b""), clock=clock
    )
    bridge.cycle()
    assert len(sent) == 1
    clock.now += 20
    bridge.cycle()
    assert len(sent) == 1
    runner_state["rig"] = rig_output({"rigName": "eng", "logicalId": "dev.qa"})
    clock.now += 20
    bridge.cycle()
    assert len(sent) == 2
    clock.now += 61
    bridge.cycle()
    assert len(sent) == 3


def test_422_drops_detail_next_cycle_then_recovers(tmp_path):
    statuses = iter([422, 200, 200])
    sent = []

    def send(body):
        sent.append(json.loads(body))
        return next(statuses), b""

    bridge = make_bridge(tmp_path, fake_runner(rig_output(), herdr_output()), send)
    assert bridge.cycle() == 20.0
    assert bridge.strip_detail is True
    bridge.cycle()
    assert all(s["detail"] == "" and s["redacted"] is True for s in sent[1]["seats"])
    assert bridge.strip_detail is False
    assert sent[0]["seats"][1]["detail"] != ""


def test_backoff_doubles_to_cap_and_resets(tmp_path):
    results = [OSError("down")] * 6 + [200]

    def send(body):
        result = results.pop(0)
        if isinstance(result, Exception):
            raise result
        return result, b""

    bridge = make_bridge(tmp_path, fake_runner(rig_output(), None), send)
    delays = [bridge.cycle() for _ in range(6)]
    assert delays == [20.0, 40.0, 80.0, 120.0, 120.0, 120.0]
    assert bridge.cycle() == 20.0
    assert bridge.backoff == 0.0


@pytest.mark.parametrize("status", [400, 401, 500])
def test_other_http_errors_back_off_without_crashing(tmp_path, status):
    bridge = make_bridge(
        tmp_path, fake_runner(rig_output(), None), lambda b: (status, b"")
    )
    assert bridge.cycle() == 20.0
    assert bridge.cycle() == 40.0


class _Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.server.requests.append(
            (self.path, self.headers.get("Authorization"), self.rfile.read(length))
        )
        if self.server.hang:
            self.server.release.wait(30)
        self.send_response(self.server.status)
        body = getattr(self.server, "body", b"")
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def log_message(self, *args):
        pass


@pytest.fixture
def server():
    srv = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    srv.requests, srv.status, srv.hang, srv.body = [], 200, False, b""
    srv.release = threading.Event()
    thread = threading.Thread(target=srv.serve_forever, daemon=True)
    thread.start()
    yield srv
    srv.release.set()
    srv.shutdown()
    srv.server_close()


def test_sender_posts_to_status_path_with_bearer(server, tmp_path):
    send = bridge_mod.make_sender(f"http://127.0.0.1:{server.server_port}/", "tok-abc")
    bridge = make_bridge(tmp_path, fake_runner(rig_output(), herdr_output()), send)
    bridge.cycle()
    path, auth, body = server.requests[0]
    assert path == "/api/v2/openrig/seat-status"
    assert auth == "Bearer tok-abc"
    assert FAKE_TOKEN not in body.decode()


def test_sender_reports_422_status_code(server, tmp_path):
    server.status = 422
    send = bridge_mod.make_sender(f"http://127.0.0.1:{server.server_port}", "t")
    bridge = make_bridge(tmp_path, fake_runner(rig_output(), None), send)
    bridge.cycle()
    assert bridge.strip_detail is True


def test_run_once_exit_codes(tmp_path):
    stop = threading.Event()
    ok = make_bridge(tmp_path, fake_runner(rig_output(), None), lambda b: (200, b""))
    assert ok.run(stop, once=True) == bridge_mod.EXIT_OK
    bad = make_bridge(tmp_path, fake_runner(rig_output(), None), lambda b: (500, b""))
    assert bad.run(stop, once=True) == bridge_mod.EXIT_REMOTE


def test_run_stops_on_event(tmp_path):
    stop = threading.Event()

    def send(body):
        stop.set()
        return 200, b""

    bridge = make_bridge(tmp_path, fake_runner(rig_output(), None), send)
    assert bridge.run(stop) == bridge_mod.EXIT_OK


def test_machine_id_sanitized():
    assert bridge_mod.sanitize_machine_id("my host.local") == "my-host-local"
    assert bridge_mod.sanitize_machine_id("..") == "machine"


def test_the_path_defaults_do_not_follow_home(monkeypatch, tmp_path):
    """The bridge's three machine-level defaults resolve from the ACCOUNT, not from HOME.

    A seat is launched with HOME pointed at its own state directory
    (/home/<user>/.openrig/state/omp/<rig>-<seat>@<rig>), and the bridge is run from a seat in
    practice rather than by expectation: the owner ran it from their own session, fe-dev ran it
    from a seat to document the hazard, and go-dev ran its --print from inside this rig. A default
    built on Path.home() therefore points into the seat's tree while the rest of the mechanism
    reads the account's, and the two halves disagree about where the data lives.

    Stated as an EQUALITY ACROSS TWO HOMEs - the shape the sync script's own test uses - so
    anything that follows HOME cannot survive the second load.
    """
    seat_like = tmp_path / "4genthub-min-lead@4genthub-min"
    seat_like.mkdir()
    monkeypatch.setenv("HOME", str(seat_like))
    first = _load_module()

    monkeypatch.setenv("HOME", str(tmp_path / "other-home"))
    second = _load_module()

    for name in ("DEFAULT_ENV_FILE", "DEFAULT_PINS", "DEFAULT_SYNC_STATE"):
        assert (
            getattr(first, name) == getattr(second, name)
        ), f"{name} followed HOME: {getattr(first, name)} against {getattr(second, name)}"
    # And each one is the ACCOUNT's path, not a path under either fake home.
    for module in (first, second):
        for name in ("DEFAULT_ENV_FILE", "DEFAULT_PINS", "DEFAULT_SYNC_STATE"):
            value = str(getattr(module, name))
            assert str(seat_like) not in value, f"{name} = {value}"
            assert "other-home" not in value, f"{name} = {value}"


def test_usage_errors_exit_2(monkeypatch):
    monkeypatch.delenv("AGENTHUB_URL", raising=False)
    # `run` reads the machine token, never the user token: one variable must not mean two
    # credentials (owner ruling, 2026-10-05).
    monkeypatch.delenv("AGENTHUB_MACHINE_TOKEN", raising=False)
    with pytest.raises(SystemExit) as err:
        bridge_mod.main(["run", "--once"])
    assert err.value.code == 2
    with pytest.raises(SystemExit) as err:
        bridge_mod.main(["run", "--interval", "0"])
    assert err.value.code == 2
    with pytest.raises(SystemExit) as err:
        bridge_mod.main(["once", "--print", "--machine-id", "bad id"])
    assert err.value.code == 2


def test_install_service_prints_unit(capsys):
    assert bridge_mod.main(["install-service"]) == 0
    out = capsys.readouterr().out
    assert "Restart=on-failure" in out
    assert "EnvironmentFile=%h/.config/agenthub-bridge.env" in out
    assert "-m agenthub_client.bridge run" in out


class _RecordingStop(threading.Event):
    """Stop event that records every wait the run loop asks for and stops after `limit` waits."""

    def __init__(self, limit):
        super().__init__()
        self.limit, self.waits = limit, []

    def wait(self, timeout=None):
        self.waits.append(timeout)
        if len(self.waits) >= self.limit:
            self.set()
        return self.is_set()


def _run_against(server, tmp_path, monkeypatch, steps):
    """Run the loop over one real HTTP exchange per step; a step is (status, hang); a hanging request never answers."""
    monkeypatch.setattr(bridge_mod, "SEND_TIMEOUT", 1.0)
    send = bridge_mod.make_sender(f"http://127.0.0.1:{server.server_port}", FAKE_TOKEN)
    bridge = make_bridge(tmp_path, fake_runner(rig_output(), None), send)
    stop = _RecordingStop(len(steps))
    original = server.RequestHandlerClass.do_POST

    def stepped(handler):
        server.status, server.hang = steps[len(server.requests)]
        original(handler)

    monkeypatch.setattr(server.RequestHandlerClass, "do_POST", stepped)
    bridge.run(stop)
    return bridge, stop.waits


def test_run_loop_backs_off_on_401_500_and_timeout_then_resets(
    server, tmp_path, monkeypatch
):
    steps = [(401, False), (500, False), (200, True), (200, False), (200, False)]
    bridge, waits = _run_against(server, tmp_path, monkeypatch, steps)
    # 401 -> 20, 500 -> 40, timeout (a request that never answers) -> 80, success resets -> 20; the unchanged
    # payload is not resent, so the last cycle just waits one interval.
    assert waits == [20.0, 40.0, 80.0, 20.0, 20.0]
    assert bridge.backoff == 0.0
    assert all(
        w >= bridge.interval for w in waits
    ), "the loop must never wait less than one interval"
    assert len(server.requests) == 4


def test_run_loop_failures_never_leak_the_token(server, tmp_path, monkeypatch, capsys):
    steps = [(401, False), (500, False), (200, True)]
    _run_against(server, tmp_path, monkeypatch, steps)
    err = capsys.readouterr()
    output = err.out + err.err
    assert "HTTP 401" in output or "HTTP 500" in output
    assert "timed out" in output
    assert FAKE_TOKEN not in output


def test_run_loop_backoff_stops_at_the_cap(tmp_path):
    stop = _RecordingStop(8)
    bridge = make_bridge(
        tmp_path, fake_runner(rig_output(), None), lambda b: (500, b"")
    )
    bridge.run(stop)
    assert stop.waits == [20.0, 40.0, 80.0, 120.0, 120.0, 120.0, 120.0, 120.0]


MACHINE_TOKEN = "mt-machine-token-value"


def test_sender_reads_the_machine_token_not_the_user_token(
    server, tmp_path, monkeypatch
):
    monkeypatch.setenv("AGENTHUB_URL", f"http://127.0.0.1:{server.server_port}")
    monkeypatch.setenv("AGENTHUB_TOKEN", "user-token")
    monkeypatch.setenv("AGENTHUB_MACHINE_TOKEN", MACHINE_TOKEN)
    bridge = make_bridge(
        tmp_path, fake_runner(rig_output(), None), bridge_mod.sender_from_env()
    )
    bridge.cycle()
    _, auth, _ = server.requests[0]
    assert (
        auth == "Bearer " + MACHINE_TOKEN
    ), "run must send the machine token, not the user token"


def test_sender_without_the_machine_token_exits_2_naming_it(monkeypatch, capsys):
    monkeypatch.setenv("AGENTHUB_URL", "http://127.0.0.1:1")
    monkeypatch.setenv("AGENTHUB_TOKEN", "user-token")
    monkeypatch.delenv("AGENTHUB_MACHINE_TOKEN", raising=False)
    with pytest.raises(SystemExit) as err:
        bridge_mod.sender_from_env()
    assert err.value.code == 2
    assert "AGENTHUB_MACHINE_TOKEN" in capsys.readouterr().err


def test_401_names_the_register_step(tmp_path, capsys):
    bridge = make_bridge(
        tmp_path, fake_runner(rig_output(), None), lambda body: (401, b"")
    )
    bridge.cycle()
    err = capsys.readouterr().err
    assert "401" in err and "4genteam bridge register" in err


def test_register_writes_env_file_0600_and_never_prints_the_token(
    server, tmp_path, monkeypatch, capsys
):
    server.status = 200
    server.body = json.dumps(
        {"success": True, "machine_id": "pc-1", "token": MACHINE_TOKEN}
    ).encode()
    env_file = tmp_path / "agenthub-bridge.env"
    monkeypatch.setenv("AGENTHUB_URL", f"http://127.0.0.1:{server.server_port}")
    monkeypatch.setenv("AGENTHUB_TOKEN", "user-token-xyz")

    code = bridge_mod.main(
        ["register", "--machine-id", "pc-1", "--env-file", str(env_file)]
    )

    assert code == 0
    printed = capsys.readouterr()
    assert MACHINE_TOKEN not in printed.out and MACHINE_TOKEN not in printed.err
    assert "pc-1" in printed.out
    text = env_file.read_text(encoding="utf-8")
    assert f"AGENTHUB_MACHINE_TOKEN={MACHINE_TOKEN}" in text
    assert f"AGENTHUB_URL=http://127.0.0.1:{server.server_port}" in text
    assert (env_file.stat().st_mode & 0o777) == 0o600
    path, auth, body = server.requests[0]
    assert path == "/api/v2/openrig/machines"
    assert auth == "Bearer user-token-xyz"
    assert json.loads(body) == {"machine_id": "pc-1"}


def test_register_preserves_other_env_lines_and_replaces_the_token(
    server, tmp_path, monkeypatch
):
    server.status = 200
    server.body = json.dumps({"token": MACHINE_TOKEN}).encode()
    env_file = tmp_path / "agenthub-bridge.env"
    env_file.write_text(
        "SOMETHING_ELSE=keep\nAGENTHUB_MACHINE_TOKEN=old\n", encoding="utf-8"
    )
    monkeypatch.setenv("AGENTHUB_URL", f"http://127.0.0.1:{server.server_port}")
    monkeypatch.setenv("AGENTHUB_TOKEN", "u")

    assert (
        bridge_mod.main(
            ["register", "--machine-id", "pc-1", "--env-file", str(env_file)]
        )
        == 0
    )

    text = env_file.read_text(encoding="utf-8")
    assert "SOMETHING_ELSE=keep" in text
    assert text.count("AGENTHUB_MACHINE_TOKEN=") == 1
    assert "AGENTHUB_MACHINE_TOKEN=old" not in text


def test_register_refused_is_loud_and_writes_nothing(
    server, tmp_path, monkeypatch, capsys
):
    server.status = 401
    env_file = tmp_path / "agenthub-bridge.env"
    monkeypatch.setenv("AGENTHUB_URL", f"http://127.0.0.1:{server.server_port}")
    monkeypatch.setenv("AGENTHUB_TOKEN", "u")

    code = bridge_mod.main(
        ["register", "--machine-id", "pc-1", "--env-file", str(env_file)]
    )

    assert code == bridge_mod.EXIT_REMOTE
    assert "HTTP 401" in capsys.readouterr().err
    assert not env_file.exists()


def test_register_without_the_user_token_is_a_usage_error(monkeypatch, capsys):
    monkeypatch.setenv("AGENTHUB_URL", "http://127.0.0.1:1")
    monkeypatch.delenv("AGENTHUB_TOKEN", raising=False)
    assert (
        bridge_mod.main(["register", "--machine-id", "pc-1"]) == bridge_mod.EXIT_USAGE
    )
    assert "AGENTHUB_TOKEN" in capsys.readouterr().err


# --- the recorded last-in-sync expected hash: drift the operator can see on this machine -----


def _pin(tmp_path, room, seat, value):
    target = tmp_path / room / seat
    target.mkdir(parents=True, exist_ok=True)
    (target / "pinned.json").write_text(json.dumps({"hash": value}), encoding="utf-8")


def _record(machine_id, seats):
    return json.dumps({"machine_id": machine_id, "seats": seats})


def _verdicts(*rows):
    """A report answer in the shape the server sends: per-seat expected hash and verdict."""
    return json.dumps(
        {
            "success": True,
            "machine_id": "pc-1",
            "seats": len(rows),
            "verdicts": [
                {"room": room, "seat": seat, "expected_hash": expected, "sync": sync}
                for room, seat, expected, sync in rows
            ],
        }
    ).encode("utf-8")


def test_an_in_sync_answer_records_the_expected_hash(tmp_path):
    _pin(tmp_path, "eng", "coder", "cloud456")
    state = tmp_path / "sync.json"
    bridge = make_bridge(
        tmp_path,
        fake_runner(rig_output(), None),
        lambda b: (200, _verdicts(("eng", "coder", "cloud456", "in_sync"))),
        sync_state_path=state,
    )
    bridge.cycle()
    # on disk, not only in memory: this is what a restart reads
    assert json.loads(state.read_text(encoding="utf-8"))["seats"] == {
        "pc-1/eng/coder": "cloud456"
    }


def test_the_record_survives_a_restart_and_still_names_a_changed_seat(tmp_path, capsys):
    """The requirement's whole point. A restarted bridge still knows what it was last in sync
    with, and a seat that has changed since then is named locally - here with the cloud
    UNREACHABLE, so nothing but the record on disk can be producing it."""
    _pin(tmp_path, "eng", "coder", "cloud456")
    state = tmp_path / "sync.json"
    first = make_bridge(
        tmp_path,
        fake_runner(rig_output(), None),
        lambda b: (200, _verdicts(("eng", "coder", "cloud456", "in_sync"))),
        sync_state_path=state,
    )
    first.cycle()

    _pin(tmp_path, "eng", "coder", "run999")  # the seat moves on, the record does not

    def unreachable(body):
        raise OSError("cloud unreachable")

    restarted = make_bridge(
        tmp_path, fake_runner(rig_output(), None), unreachable, sync_state_path=state
    )
    assert restarted.sync_state == {"pc-1/eng/coder": "cloud456"}
    restarted.cycle()
    err = capsys.readouterr().err
    assert "eng/coder has changed since the cloud last confirmed it in sync" in err
    assert "run999" in err and "cloud456" in err

    record = restarted.local_record(restarted.build_payload()["seats"])
    assert record["eng/coder"] == {
        "running": "run999",
        "last_in_sync": "cloud456",
        "since_last_in_sync": "changed",
    }
    # a seat with no record reads unknown, never ``unchanged`` by accident
    assert record["eng/reviewer"]["since_last_in_sync"] == "unknown"


def test_the_cloud_moving_ahead_is_named_even_though_the_machine_did_not_move(
    tmp_path, capsys
):
    """THE CASE THAT WAS SILENT, and the inversion the review found: the cloud's expectation
    moves, the seat does not. The local record rightly reads ``unchanged`` - the machine IS
    unchanged - so the local comparison CANNOT see this by construction, and the answer's
    verdict is the only channel that knows the cloud's current expectation. It must be named,
    and the record must NOT advance on it, because a drift answer is not a confirmation."""
    _pin(tmp_path, "eng", "coder", "h1")
    state = tmp_path / "sync.json"
    state.write_text(_record("pc-1", {"pc-1/eng/coder": "h1"}), encoding="utf-8")
    # the cloud now expects h2 for a seat that is unchanged here, so its answer is drift
    bridge = make_bridge(
        tmp_path,
        fake_runner(rig_output(), None),
        lambda b: (200, _verdicts(("eng", "coder", "h2", "drift"))),
        sync_state_path=state,
    )

    record = bridge.local_record(bridge.build_payload()["seats"])
    assert record["eng/coder"]["since_last_in_sync"] == "unchanged"

    bridge.cycle()

    err = capsys.readouterr().err
    assert "the cloud reports eng/coder drift" in err, err
    assert "h2" in err
    assert bridge.sync_state == {"pc-1/eng/coder": "h1"}


def test_an_answer_without_verdicts_leaves_the_record_intact(tmp_path):
    _pin(tmp_path, "eng", "coder", "cloud456")
    state = tmp_path / "sync.json"
    state.write_text(_record("pc-1", {"pc-1/eng/coder": "cloud456"}), encoding="utf-8")
    for answer in (b"", b"not json", json.dumps({"success": True}).encode()):
        bridge = make_bridge(
            tmp_path,
            fake_runner(rig_output(), None),
            lambda b: (200, answer),
            sync_state_path=state,
        )
        bridge.cycle()
        assert bridge.sync_state == {"pc-1/eng/coder": "cloud456"}


def test_an_unreadable_record_reads_as_empty_rather_than_inventing_a_hash(tmp_path):
    state = tmp_path / "sync.json"
    state.write_text("{not json", encoding="utf-8")
    bridge = make_bridge(
        tmp_path,
        fake_runner(rig_output(), None),
        lambda b: (200, b""),
        sync_state_path=state,
    )
    assert bridge.sync_state == {}
    record = bridge.local_record(bridge.build_payload()["seats"])
    assert {v["since_last_in_sync"] for v in record.values()} == {"unknown"}


def test_once_print_carries_the_local_view_and_not_the_reported_payload(
    tmp_path, capsys, monkeypatch
):
    _pin(tmp_path, "eng", "coder", "run999")
    state = tmp_path / "sync.json"
    state.write_text(_record("pc-1", {"pc-1/eng/coder": "cloud456"}), encoding="utf-8")
    monkeypatch.setattr(bridge_mod, "DEFAULT_SYNC_STATE", state)
    monkeypatch.setattr(bridge_mod, "DEFAULT_PINS", tmp_path)
    # the dump path builds its bridge the way the CLI does, so the local tools it shells out to
    # are injected here rather than assumed to exist on the test host
    monkeypatch.setattr(
        bridge_mod, "run_command", fake_runner(rig_output(), herdr_output())
    )

    assert (
        bridge_mod.main(["once", "--print", "--machine-id", "pc-1"])
        == bridge_mod.EXIT_OK
    )

    printed = json.loads(capsys.readouterr().out)
    # the dump carries a per-seat local RECORD ... (this host's own record is not this test's to
    # write, so a seat it has no record for reads unknown - and the words are the record's own,
    # unchanged/changed, never the cloud's in_sync; the discrimination is pinned above)
    assert printed["local_record"]["eng/coder"]["since_last_in_sync"] == "unknown"
    assert printed["local_record"]["eng/coder"]["last_in_sync"] == ""
    # ... and the POSTed payload must NOT grow that key: the server refuses unknown report
    # fields, so a local view put on the wire would turn every report into a 400
    payload = bridge_mod.Bridge(
        "pc-1", 20.0, lambda b: (200, b""), pins_dir=tmp_path
    ).build_payload()
    assert "local_record" not in payload


EXPORT_YAML = """version: "0.2"
name: eng
pods:
  - id: dev
    members:
      - id: coder
      - id: reviewer
    edges:
      - {kind: delegates_to, from: coder, to: reviewer}
      - {kind: escalates_to, from: reviewer, to: coder}
      - {kind: delegates_to, from: coder, to: reviewer}
      - {kind: telepathy, from: coder, to: reviewer}
      - {kind: delegates_to, from: coder, to: coder}
      - {kind: delegates_to, from: "bad name", to: coder}
edges: []
Exported to /dev/stdout
"""


def test_parse_rig_edges_ignores_the_trailing_status_line():
    assert bridge_mod.parse_rig_edges(EXPORT_YAML)[:2] == [
        ("coder", "reviewer", "delegates_to"),
        ("reviewer", "coder", "escalates_to"),
    ]


def test_edges_are_reported_per_room_and_the_server_refusable_ones_are_dropped(tmp_path):
    def runner(argv):
        if argv[:2] == ["rig", "export"]:
            return EXPORT_YAML
        if argv[0] == "rig":
            return rig_output()
        raise FileNotFoundError(argv[0])

    payload = make_bridge(tmp_path, runner, lambda b: (200, b"")).build_payload()
    assert payload["edges"] == [
        {"room": "eng", "from": "coder", "to": "reviewer", "kind": "delegates_to"},
        {"room": "eng", "from": "reviewer", "to": "coder", "kind": "escalates_to"},
    ]
