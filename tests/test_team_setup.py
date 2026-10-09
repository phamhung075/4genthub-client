"""Tests for agenthub_client.team_setup.

The script runs against a local HTTP server that records every request, so no
real 4genthub server is needed.
"""

import hashlib
import importlib.util
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import pytest

# These tests are self-contained and must not spin up the test database.
pytestmark = pytest.mark.unit

REPO_ROOT = Path(__file__).resolve().parents[2]
MODULE_PATH = REPO_ROOT / "agenthub_client" / "src" / "agenthub_client" / "team_setup.py"
TEAM_DIR = REPO_ROOT / "scripts" / "team" / "4genthub"

TOKEN = "tok-secret-1234567890"
SEAT_TYPES_PATH = "/api/v2/openrig/seat-types"
SEAT_TYPES = {
    "lead",
    "planner",
    "architect",
    "developer",
    "reviewer",
    "tester",
    "debugger",
    "researcher",
    "writer",
}
WORD_LIMITS = {
    "project-4genthub": (350, 500),
    "delegate-deepseek": (100, 230),
    "area-go-backend": (120, 250),
    "area-web-frontend": (100, 200),
    "area-quality": (100, 200),
    "area-docs": (80, 150),
    "mission-4genthub": (350, 520),
}
MODULE_FILES = {"mission-4genthub": "mission.md"}


def _load_module():
    return importlib.import_module("agenthub_client.team_setup")


team_setup = _load_module()


class _Handler(BaseHTTPRequestHandler):
    def _handle(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(length) or b"null")
        server = self.server
        # apply's read of the stored seat types is counted apart, so the write-sequence
        # assertions below stay about writes
        record = (
            server.seat_type_reads
            if (self.command, self.path) == ("GET", SEAT_TYPES_PATH)
            else server.requests
        )
        record.append(
            {
                "method": self.command,
                "path": self.path,
                "body": body,
                "auth": self.headers.get("Authorization"),
            }
        )
        status, text = server.respond(self.command, self.path, body)
        payload = text.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    do_PUT = _handle
    do_POST = _handle
    do_GET = _handle

    def log_message(self, *args):
        pass


class TeamServer:
    def __init__(self):
        self.requests = []
        self.seat_type_reads = []
        self.overrides = {}  # (method, path) -> (status, text)
        self.httpd = HTTPServer(("127.0.0.1", 0), _Handler)
        self.httpd.requests = self.requests
        self.httpd.seat_type_reads = self.seat_type_reads
        self.httpd.respond = self.respond
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return f"http://127.0.0.1:{self.httpd.server_port}"

    def respond(self, method, path, body):
        if (method, path) in self.overrides:
            return self.overrides[(method, path)]
        if (method, path) == ("GET", SEAT_TYPES_PATH):
            return 200, '{"seat_types": []}'  # nothing stored: the seed runs
        if method == "GET":
            return 404, '{"detail": "not found"}'
        return (201 if method == "POST" else 200), '{"success": true}'

    def close(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.thread.join(timeout=5)


@pytest.fixture
def server():
    team_server = TeamServer()
    yield team_server
    team_server.close()


@pytest.fixture
def env(server, monkeypatch):
    monkeypatch.setenv("AGENTHUB_URL", server.url)
    monkeypatch.setenv("AGENTHUB_TOKEN", TOKEN)


def _run(capsys, *extra):
    code = team_setup.main(["apply", *extra])
    out = capsys.readouterr()
    return code, out.out, out.err


def _context_file(slug):
    return TEAM_DIR / MODULE_FILES.get(slug, f"{slug}.txt")


def _team():
    return team_setup.load_team(TEAM_DIR)


def test_calls_follow_the_documented_order(server, env, capsys):
    code, out, _ = _run(capsys)
    assert code == 0
    kinds = []
    for req in server.requests:
        path = req["path"]
        if path.endswith("/seat-types/seed"):
            kind = "seed"
        elif "/modules/" in path:
            kind = "module"
        elif path.endswith("/rooms"):
            kind = "room"
        elif path.endswith("/links"):
            kind = "link"
        elif path.endswith("/overlay") and "/seats/" not in path:
            kind = "company-overlay"
        elif path.endswith("/overlay"):
            kind = "seat-overlay"
        else:
            kind = "seat"
        if not kinds or kinds[-1] != kind:
            kinds.append(kind)
    assert kinds == [
        "seed",
        "module",
        "room",
        "seat",
        "link",
        "company-overlay",
        "seat-overlay",
    ]
    assert len(out.strip().splitlines()) == len(server.requests)
    assert all(r["auth"] == f"Bearer {TOKEN}" for r in server.requests)


def test_module_content_comes_from_the_files(server, env, capsys):
    assert _run(capsys)[0] == 0
    puts = {
        r["path"].split("/")[5]: r["body"]
        for r in server.requests
        if "/modules/" in r["path"]
    }
    assert set(puts) == set(WORD_LIMITS)
    for slug, body in puts.items():
        assert body["kind"] == "instruction"
        assert body["content"] == _context_file(slug).read_text(encoding="utf-8")


def test_overlays_send_full_op_lists(server, env, capsys):
    assert _run(capsys)[0] == 0
    overlays = {
        r["path"]: r["body"] for r in server.requests if r["path"].endswith("/overlay")
    }
    company = overlays["/api/v2/openrig/overlay"]
    assert company["ops"] == [
        {"kind": "add", "slug": "project-4genthub", "version": "1.0.0", "content": ""},
        {"kind": "add", "slug": "delegate-deepseek", "version": "1.1.0", "content": ""},
    ]
    room = "/api/v2/openrig/rooms/4genthub-dev/seats"

    def slugs(seat):
        return [o["slug"] for o in overlays[f"{room}/{seat}/overlay"]["ops"]]

    assert slugs("go-dev") == ["area-go-backend"]
    assert slugs("web-dev") == ["area-web-frontend"]
    assert slugs("planner") == [
        "area-go-backend",
        "area-web-frontend",
        "mission-4genthub",
    ]
    assert slugs("architect") == ["area-go-backend", "area-web-frontend"]
    for seat in ("tester", "reviewer", "debugger"):
        assert slugs(seat) == ["area-quality"]
    assert slugs("writer") == ["area-docs"]
    assert slugs("lead") == ["mission-4genthub"]
    mission_seats = {
        seat
        for seat in team_setup.load_team(TEAM_DIR)["seat_overlays"]
        if "mission-4genthub" in slugs(seat)
    }
    assert mission_seats == {"lead", "planner"}


def test_links_match_the_team_topology(server, env, capsys):
    assert _run(capsys)[0] == 0
    prefix = "/api/v2/openrig/rooms/4genthub-dev/seats/"
    links = {
        (
            r["path"][len(prefix) :].split("/")[0],
            r["body"]["kind"],
            r["body"]["to_seat"],
        )
        for r in server.requests
        if r["path"].endswith("/links")
    }
    others = {
        "planner",
        "architect",
        "go-dev",
        "web-dev",
        "reviewer",
        "tester",
        "debugger",
        "writer",
    }
    expected = {("lead", "delegates_to", s) for s in others}
    expected |= {(s, "escalates_to", "lead") for s in others}
    expected |= {
        (d, "collaborates_with", t)
        for d in ("go-dev", "web-dev")
        for t in ("reviewer", "tester")
    }
    expected.add(("reviewer", "collaborates_with", "tester"))
    assert links == expected
    assert all(
        r["body"]["allow"] is True
        for r in server.requests
        if r["path"].endswith("/links")
    )


def test_rerun_tolerates_existing_room_and_seats(server, env, capsys):
    server.overrides[("POST", "/api/v2/openrig/rooms")] = (
        409,
        '{"detail":"room already exists"}',
    )
    for seat in _team()["seats"]:
        path = "/api/v2/openrig/rooms/4genthub-dev/seats"
        server.overrides[("POST", path)] = (409, '{"detail":"seat already exists"}')
    code, out, _ = _run(capsys)
    assert code == 0
    assert "room 4genthub-dev: exists" in out
    assert "seat lead: exists" in out
    assert "link lead delegates_to planner: applied" in out
    assert len(server.requests) == len(team_setup.build_plan(_team()))


def test_409_on_a_module_is_an_error(server, env, capsys):
    path = "/api/v2/openrig/modules/project-4genthub/versions/1.0.0"
    server.overrides[("PUT", path)] = (
        409,
        '{"detail":"already exists with different content"}',
    )
    code, _, err = _run(capsys)
    assert code == 1
    assert "project-4genthub" in err
    assert len(server.requests) == 2  # the seed, then the failing module


def test_seed_failure_stops_before_any_other_call(server, env, capsys):
    server.overrides[("POST", "/api/v2/openrig/seat-types/seed")] = (
        500,
        '{"detail":"AGENTHUB_PUBLIC_URL is not set"}',
    )
    code, _, err = _run(capsys)
    assert code == 1
    assert "seat types (seed)" in err and "AGENTHUB_PUBLIC_URL" in err
    assert len(server.requests) == 1


def test_the_seed_is_skipped_when_every_needed_seat_type_is_stored(server, env, capsys):
    stored = [{"slug": slug} for slug in SEAT_TYPES]
    server.overrides[("GET", SEAT_TYPES_PATH)] = (
        200,
        json.dumps({"seat_types": stored}),
    )
    code, out, _ = _run(capsys)
    assert code == 0
    assert not any(r["path"].endswith("/seat-types/seed") for r in server.requests)
    assert "seat types (seed)" not in out
    assert len(server.requests) == len(team_setup.build_plan(_team(), SEAT_TYPES))


def test_the_seed_runs_when_a_needed_seat_type_is_missing(server, env, capsys):
    stored = [{"slug": slug} for slug in SEAT_TYPES - {"writer"}]
    server.overrides[("GET", SEAT_TYPES_PATH)] = (
        200,
        json.dumps({"seat_types": stored}),
    )
    code, out, _ = _run(capsys)
    assert code == 0
    assert server.requests[0]["path"] == "/api/v2/openrig/seat-types/seed"


def test_an_empty_company_overlay_sends_no_company_overlay(server, env, capsys):
    team = _team()
    team[
        "company_overlay"
    ] = []  # the company overlay is account-wide: a PUT would replace it
    labels = [step[0] for step in team_setup.build_plan(team, SEAT_TYPES)]
    assert "overlay company" not in labels
    assert any(label.startswith("overlay seat ") for label in labels)


def test_an_unreadable_seat_type_list_stops_before_any_write(server, env, capsys):
    server.overrides[("GET", SEAT_TYPES_PATH)] = (500, '{"detail":"boom"}')
    code, _, err = _run(capsys)
    assert code == 1
    assert "GET /api/v2/openrig/seat-types failed: HTTP 500" in err
    assert server.requests == []


def test_409_without_already_exists_is_an_error(server, env, capsys):
    server.overrides[("POST", "/api/v2/openrig/rooms")] = (409, '{"detail":"conflict"}')
    code, _, _ = _run(capsys)
    assert code == 1


def test_other_4xx_stops_the_run(server, env, capsys):
    server.overrides[("POST", "/api/v2/openrig/rooms")] = (422, '{"detail":"bad slug"}')
    code, _, err = _run(capsys)
    assert code == 1
    assert "422" in err
    assert server.requests[-1]["path"] == "/api/v2/openrig/rooms"


def test_dry_run_makes_no_requests_and_needs_no_env(server, monkeypatch, capsys):
    monkeypatch.delenv("AGENTHUB_URL", raising=False)
    monkeypatch.delenv("AGENTHUB_TOKEN", raising=False)
    code, out, _ = _run(capsys, "--dry-run")
    assert code == 0
    assert server.requests == []
    assert len(out.strip().splitlines()) == len(team_setup.build_plan(_team()))


def test_token_is_never_printed(server, env, capsys):
    server.overrides[("POST", "/api/v2/openrig/rooms")] = (500, "boom")
    _, fail_out, fail_err = _run(capsys)
    assert TOKEN not in fail_out + fail_err
    server.overrides.clear()
    ok_code, ok_out, ok_err = _run(capsys)
    assert ok_code == 0
    assert TOKEN not in ok_out + ok_err
    _, dry_out, dry_err = _run(capsys, "--dry-run")
    assert TOKEN not in dry_out + dry_err


def test_missing_config_is_a_usage_error(monkeypatch, capsys):
    monkeypatch.delenv("AGENTHUB_URL", raising=False)
    monkeypatch.setenv("AGENTHUB_TOKEN", TOKEN)
    code, _, err = _run(capsys)
    assert code == 2
    assert "AGENTHUB_URL" in err


def test_unreachable_server_is_exit_1(monkeypatch, capsys):
    monkeypatch.setenv("AGENTHUB_URL", "http://127.0.0.1:1")
    monkeypatch.setenv("AGENTHUB_TOKEN", TOKEN)
    code, _, err = _run(capsys)
    assert code == 1
    assert TOKEN not in err


def test_every_overlay_module_is_created_by_the_script():
    team = _team()
    created = {m["slug"] for m in team["modules"]}
    referenced = set(team["company_overlay"])
    for slugs in team["seat_overlays"].values():
        referenced.update(slugs)
    assert referenced <= created
    assert created <= referenced, "a module no overlay uses is dead weight"
    seat_keys = {s["seat_key"] for s in team["seats"]}
    assert set(team["seat_overlays"]) <= seat_keys
    for link in team["links"]:
        assert {link["from"], link["to"]} <= seat_keys


def test_seat_types_are_among_the_nine():
    for seat in _team()["seats"]:
        assert seat["seat_type"] in SEAT_TYPES
        assert seat["runtime"] == "claude-code"


@pytest.mark.parametrize("slug", sorted(WORD_LIMITS))
def test_context_files_respect_word_limits(slug):
    low, high = WORD_LIMITS[slug]
    words = len(_context_file(slug).read_text(encoding="utf-8").split())
    assert low <= words <= high, f"{slug}: {words} words, expected {low}-{high}"


def test_delegate_module_carries_the_chef_and_worker_wording():
    text = (TEAM_DIR / "delegate-deepseek.txt").read_text(encoding="utf-8")
    assert "Each seat's session is the chef" in text
    assert "accountable for the result" in text
    assert "never forwarded unreviewed" in text
    versions = {m["slug"]: m["version"] for m in _team()["modules"]}
    assert versions["delegate-deepseek"] == "1.1.0"


def test_project_brief_starts_with_the_safety_rule():
    text = (TEAM_DIR / "project-4genthub.txt").read_text(encoding="utf-8")
    assert text.startswith("SAFETY.")
    assert "Never git push" in text.split("\n")[0]


# --- import-project: local .mcp.json and .claude/skills -> module versions ---------------

HOOKS_DIR = REPO_ROOT / ".claude" / "hooks"
MCP_CONFIG = {
    "mcpServers": {
        "weather": {
            "type": "http",
            "url": "https://mcp.example.com/mcp",
            "headers": {"Authorization": "Bearer ${WEATHER_TOKEN}"},
        },
        "filesystem": {
            "command": "npx",
            "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
            "env": {"FS_ROOT": "${FS_ROOT}"},
        },
    }
}
SKILLS = {"alpha-skill": "# Alpha\nbody\n", "beta-skill": "# Beta\nbody\n"}


@pytest.fixture
def project_root(tmp_path):
    (tmp_path / ".mcp.json").write_text(json.dumps(MCP_CONFIG), encoding="utf-8")
    for slug, text in SKILLS.items():
        skill_dir = tmp_path / ".claude" / "skills" / slug
        skill_dir.mkdir(parents=True)
        (skill_dir / "SKILL.md").write_text(text, encoding="utf-8")
    return tmp_path


def _import(monkeypatch, capsys, project_root, *extra):
    monkeypatch.setattr(team_setup, "get_project_root", lambda: project_root)
    code = team_setup.main(["import-project", *extra])
    out = capsys.readouterr()
    return code, out.out, out.err


def test_import_project_pushes_mcp_blocks_and_skills(
    server, env, monkeypatch, capsys, project_root
):
    code, out, _ = _import(monkeypatch, capsys, project_root)
    assert code == 0
    # every module is first resolved with a GET; the pushes themselves are all PUT
    puts = {
        r["path"].split("/")[5]: r["body"]
        for r in server.requests
        if r["method"] == "PUT"
    }
    assert set(puts) == {"weather", "filesystem", "alpha-skill", "beta-skill"}
    assert all(
        r["path"].startswith("/api/v2/openrig/modules/")
        for r in server.requests
        if r["method"] == "GET"
    )
    # an http server keeps its ${ENV_VAR} header reference verbatim
    assert json.loads(puts["weather"]["content"]) == {
        "name": "weather",
        "type": "http",
        "url": "https://mcp.example.com/mcp",
        "headers": {"Authorization": "Bearer ${WEATHER_TOKEN}"},
    }
    assert puts["weather"]["kind"] == "mcp"
    # a command entry with no explicit type is a stdio server, env references kept
    assert json.loads(puts["filesystem"]["content"]) == {
        "name": "filesystem",
        "type": "stdio",
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
        "env": {"FS_ROOT": "${FS_ROOT}"},
    }
    assert puts["filesystem"]["kind"] == "mcp"
    assert puts["alpha-skill"]["kind"] == "skill"
    alpha = json.loads(puts["alpha-skill"]["content"])
    assert alpha == {
        "content": SKILLS["alpha-skill"],
        "source_path": ".claude/skills/alpha-skill/SKILL.md",
        "sha256": _sha256(SKILLS["alpha-skill"]),
    }
    assert puts["beta-skill"]["kind"] == "skill"
    beta = json.loads(puts["beta-skill"]["content"])
    assert beta["content"] == SKILLS["beta-skill"]
    assert beta["source_path"] == ".claude/skills/beta-skill/SKILL.md"
    assert beta["sha256"] == _sha256(SKILLS["beta-skill"])
    for slug in puts:
        assert f"/api/v2/openrig/modules/{slug}/versions/1.0.0" in {
            r["path"] for r in server.requests
        }
    assert "module weather@1.0.0: applied" in out


def test_import_project_refuses_a_secret_literal(
    server, monkeypatch, capsys, project_root
):
    leaky = {
        "mcpServers": {
            "leaky": {
                "type": "http",
                "url": "https://mcp.example.com/mcp",
                "headers": {
                    "Authorization": "Bearer sk-abcdefghijklmnopqrstuvwxyz012345"
                },
            }
        }
    }
    (project_root / ".mcp.json").write_text(json.dumps(leaky), encoding="utf-8")
    code, _, err = _import(monkeypatch, capsys, project_root)
    assert code == 2
    assert "leaky" in err
    assert "ENV_VAR" in err
    assert server.requests == []


def test_import_project_dry_run_makes_no_requests(
    server, monkeypatch, capsys, project_root
):
    monkeypatch.delenv("AGENTHUB_URL", raising=False)
    monkeypatch.delenv("AGENTHUB_TOKEN", raising=False)
    code, out, _ = _import(monkeypatch, capsys, project_root, "--dry-run")
    assert code == 0
    assert server.requests == []
    assert (
        "plan: module weather@1.0.0: PUSH unless stored content is identical (then SKIP); "
        "if stored content differs, NEW-VERSION 1.0.1"
    ) in out


def test_import_project_uses_the_hooks_project_root_derivation():
    """ONE derivation, and it is the hooks' - but bound WHERE IT IS USED.

    This asserted identity (`team_setup.get_project_root is hooks_get_project_root`), which held
    only because the hook import ran at MODULE import time. That is precisely what made a wheel
    install die with `ModuleNotFoundError: No module named 'utils'` before the CLI could print
    `--help`. The binding moved into the function, so the property worth pinning is the one that
    survives the move: the module still derives nothing of its own, it returns the hooks' answer,
    and the identity being FALSE is what proves the import is no longer at module level.
    """
    if str(HOOKS_DIR) not in sys.path:
        sys.path.insert(0, str(HOOKS_DIR))
    from utils.env_loader import get_project_root as hooks_get_project_root

    assert team_setup.get_project_root() == hooks_get_project_root()
    assert team_setup.get_project_root is not hooks_get_project_root, (
        "the hook import is bound at module level again, which is what made a wheel install die "
        "at import time instead of naming AGENTHUB_REPO_ROOT"
    )


def _store_pushes(server):
    """Serve every recorded module PUT back as a stored GET, the way the backend would."""
    for req in list(server.requests):
        if req["method"] != "PUT":
            continue
        slug, version = req["path"].split("/")[5], req["path"].split("/")[7]
        module = {
            "slug": slug,
            "version": version,
            "kind": req["body"]["kind"],
            "content": req["body"]["content"],
        }
        server.overrides[("GET", req["path"])] = (
            200,
            json.dumps({"success": True, "module": module}),
        )


def test_import_project_rerun_skips_identical_modules(
    server, env, monkeypatch, capsys, project_root
):
    assert _import(monkeypatch, capsys, project_root)[0] == 0
    _store_pushes(server)
    server.requests.clear()

    code, out, _ = _import(monkeypatch, capsys, project_root)

    assert code == 0
    # the second import leaves the store untouched: every module was already stored
    assert [r for r in server.requests if r["method"] == "PUT"] == []
    assert out.count(": skipped (identical content already stored)") == 4
    assert "import summary: 0 pushed, 0 new version(s), 4 skipped" in out


def test_import_project_changed_content_pushes_the_next_patch(
    server, env, monkeypatch, capsys, project_root
):
    assert _import(monkeypatch, capsys, project_root)[0] == 0
    _store_pushes(server)
    (project_root / ".claude" / "skills" / "alpha-skill" / "SKILL.md").write_text(
        "# Alpha v2\nbody\n", encoding="utf-8"
    )
    server.requests.clear()

    code, out, _ = _import(monkeypatch, capsys, project_root)

    assert code == 0
    puts = [r for r in server.requests if r["method"] == "PUT"]
    # the stored 1.0.0 differs, so the change lands as 1.0.1 rather than an overwrite
    assert [r["path"] for r in puts] == [
        "/api/v2/openrig/modules/alpha-skill/versions/1.0.1"
    ]
    changed = json.loads(puts[0]["body"]["content"])
    assert changed["content"] == "# Alpha v2\nbody\n"
    assert changed["sha256"] == _sha256("# Alpha v2\nbody\n")
    assert (
        "module alpha-skill@1.0.1 (new version; 1.0.0 stored with different content): applied"
        in out
    )
    assert "import summary: 0 pushed, 1 new version(s), 3 skipped" in out


def test_import_project_dry_run_classifies_against_stored_versions(
    server, env, monkeypatch, capsys, project_root
):
    assert _import(monkeypatch, capsys, project_root)[0] == 0
    _store_pushes(server)
    (project_root / ".claude" / "skills" / "alpha-skill" / "SKILL.md").write_text(
        "# Alpha v2\nbody\n", encoding="utf-8"
    )
    new_skill = project_root / ".claude" / "skills" / "gamma-skill"
    new_skill.mkdir(parents=True)
    (new_skill / "SKILL.md").write_text("# Gamma\nbody\n", encoding="utf-8")
    server.requests.clear()

    code, out, _ = _import(monkeypatch, capsys, project_root, "--dry-run")

    assert code == 0
    assert [r for r in server.requests if r["method"] == "PUT"] == []
    assert (
        "plan: module alpha-skill@1.0.1: NEW-VERSION (1.0.0 stored with different content)"
        in out
    )
    assert "plan: module weather@1.0.0: SKIP (identical content already stored)" in out
    assert "plan: module gamma-skill@1.0.0: PUSH" in out


# --- drift-check: stored skill provenance vs the files on disk ---------------------------


def _sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _stored_skill(slug, version, source_path=None, sha256=None, content=None):
    """One stored skill module body as the backend returns it; raw content = no provenance."""
    if content is None:
        content = json.dumps({"source_path": source_path, "sha256": sha256})
    return {"slug": slug, "kind": "skill", "version": version, "content": content}


def _store_blocks(server, modules):
    """Serve GET /modules (latest summaries) plus each module's content GET, the way the backend would."""
    summaries = []
    for module in modules:
        summaries.append(
            {
                "slug": module["slug"],
                "kind": module["kind"],
                "version": module["version"],
                "sha256": hashlib.sha256(module["content"].encode("utf-8")).hexdigest(),
            }
        )
        server.overrides[
            (
                "GET",
                f"/api/v2/openrig/modules/{module['slug']}/versions/{module['version']}",
            )
        ] = (200, json.dumps({"success": True, "module": module}))
    server.overrides[("GET", "/api/v2/openrig/modules")] = (
        200,
        json.dumps({"success": True, "modules": summaries}),
    )


def _drift(capsys, server, project_root, *extra, library_root=None):
    # hermetic by default: the fallback root points at a directory that holds nothing
    if library_root is None:
        library_root = project_root / "library-root"
    code = team_setup.main(
        [
            "drift-check",
            "--root",
            str(project_root),
            "--library-root",
            str(library_root),
            *extra,
        ]
    )
    out = capsys.readouterr()
    return code, out.out, out.err


def test_drift_check_clean_tree_reports_nothing(server, env, capsys, project_root):
    alpha, beta = SKILLS["alpha-skill"], SKILLS["beta-skill"]
    _store_blocks(
        server,
        [
            _stored_skill(
                "alpha-skill",
                "1.0.0",
                ".claude/skills/alpha-skill/SKILL.md",
                _sha256(alpha),
            ),
            # a canonical/plugin overlap: both the source and the mirror pair match disk
            {
                "slug": "beta-skill",
                "kind": "skill",
                "version": "1.0.0",
                "content": json.dumps(
                    {
                        "content": beta,
                        "source_path": ".claude/skills/beta-skill/SKILL.md",
                        "sha256": _sha256(beta),
                        "mirror_path": ".claude/skills/alpha-skill/SKILL.md",
                        "mirror_sha256": _sha256(alpha),
                    }
                ),
            },
            # an mcp block is never a skill block, even without provenance, so it is not a finding
            {"slug": "weather", "kind": "mcp", "version": "1.0.0", "content": "{}"},
        ],
    )

    code, out, err = _drift(capsys, server, project_root)

    assert code == 0
    assert out == "" and err == ""
    # read-only: the listing plus one content GET per skill block, never a write
    assert {r["method"] for r in server.requests} == {"GET"}


def test_drift_check_names_the_skill_and_both_hashes(server, env, capsys, project_root):
    stored = _sha256("some other bytes\n")
    _store_blocks(
        server,
        [
            _stored_skill(
                "alpha-skill", "1.0.0", ".claude/skills/alpha-skill/SKILL.md", stored
            )
        ],
    )

    code, out, err = _drift(capsys, server, project_root)

    assert code == 1
    assert out == ""
    assert err.splitlines() == [
        "stored skill provenance drifted from disk; nothing was written.",
        f"  alpha-skill: .claude/skills/alpha-skill/SKILL.md (digest @ {project_root}) "
        f"stored {stored} computed {_sha256(SKILLS['alpha-skill'])}",
    ]
    assert [r for r in server.requests if r["method"] != "GET"] == []


def test_drift_check_reports_a_missing_source_file(server, env, capsys, project_root):
    _store_blocks(
        server,
        [
            _stored_skill(
                "alpha-skill", "1.0.0", ".claude/skills/gone/SKILL.md", _sha256("x")
            )
        ],
    )

    code, _, err = _drift(capsys, server, project_root)

    assert code == 1
    assert (
        f"  alpha-skill: .claude/skills/gone/SKILL.md (missing-source) "
        f"no file under {project_root} or {project_root / 'library-root'}"
    ) in err


def test_drift_check_reports_a_block_without_provenance(
    server, env, capsys, project_root
):
    # an older block published before provenance: its content is the raw skill text
    _store_blocks(
        server, [_stored_skill("alpha-skill", "1.0.0", content=SKILLS["alpha-skill"])]
    )

    code, _, err = _drift(capsys, server, project_root)

    assert code == 1
    assert (
        "  alpha-skill: stored 1.0.0 (no-provenance) carries no source_path/sha256"
        in err
    )


def test_drift_check_reports_a_divergent_mirror_pair(server, env, capsys, project_root):
    # the canonical source matches disk, the mirrored copy does not: one mirror finding, no digest one
    (project_root / ".claude" / "skills" / "mirror-skill").mkdir(parents=True)
    (project_root / ".claude" / "skills" / "mirror-skill" / "SKILL.md").write_text(
        "# Mirror\nbody\n", encoding="utf-8"
    )
    mirror_stored = _sha256("a different mirror\n")
    _store_blocks(
        server,
        [
            {
                "slug": "alpha-skill",
                "kind": "skill",
                "version": "1.0.0",
                "content": json.dumps(
                    {
                        "content": SKILLS["alpha-skill"],
                        "source_path": ".claude/skills/alpha-skill/SKILL.md",
                        "sha256": _sha256(SKILLS["alpha-skill"]),
                        "mirror_path": ".claude/skills/mirror-skill/SKILL.md",
                        "mirror_sha256": mirror_stored,
                    }
                ),
            }
        ],
    )

    code, _, err = _drift(capsys, server, project_root)

    assert code == 1
    assert err.splitlines() == [
        "stored skill provenance drifted from disk; nothing was written.",
        f"  alpha-skill: .claude/skills/mirror-skill/SKILL.md (mirror @ {project_root}) "
        f"stored {mirror_stored} computed {_sha256('# Mirror\nbody\n')}",
    ]


def test_drift_check_resolves_a_library_path_under_library_root(
    server, env, capsys, project_root, tmp_path
):
    # publish-skills' paths belong to the OpenRig checkout, not to this project
    library = tmp_path / "openrig"
    target = library / "skills" / "_canonical" / "core" / "alpha-skill" / "SKILL.md"
    target.parent.mkdir(parents=True)
    target.write_text("# Library\nbody\n", encoding="utf-8")
    stored = _sha256("stale library bytes\n")
    _store_blocks(
        server,
        [
            _stored_skill(
                "alpha-skill",
                "1.0.0",
                "skills/_canonical/core/alpha-skill/SKILL.md",
                stored,
            )
        ],
    )

    code, _, err = _drift(capsys, server, project_root, library_root=library)

    assert code == 1
    assert err.splitlines() == [
        "stored skill provenance drifted from disk; nothing was written.",
        f"  alpha-skill: skills/_canonical/core/alpha-skill/SKILL.md (digest @ {library}) "
        f"stored {stored} computed {_sha256('# Library\nbody\n')}",
    ]


def test_drift_check_library_root_defaults_to_openrig_skills_root(
    server, env, monkeypatch, capsys, project_root, tmp_path
):
    library = tmp_path / "openrig"
    target = library / "skills" / "_canonical" / "core" / "beta-skill" / "SKILL.md"
    target.parent.mkdir(parents=True)
    target.write_text("# Library\nbody\n", encoding="utf-8")
    monkeypatch.setenv("OPENRIG_SKILLS_ROOT", str(library))
    _store_blocks(
        server,
        [
            _stored_skill(
                "beta-skill",
                "1.0.0",
                "skills/_canonical/core/beta-skill/SKILL.md",
                _sha256("# Library\nbody\n"),
            )
        ],
    )

    # no --library-root: the env default must resolve the path cleanly
    code = team_setup.main(["drift-check", "--root", str(project_root)])
    out = capsys.readouterr()

    assert code == 0
    assert out.out == "" and out.err == ""


def test_drift_check_separates_mismatch_from_no_provenance(
    server, env, capsys, project_root
):
    _store_blocks(
        server,
        [
            _stored_skill(
                "alpha-skill",
                "1.0.0",
                ".claude/skills/alpha-skill/SKILL.md",
                _sha256("z"),
            ),
            _stored_skill("beta-skill", "1.0.0", content=SKILLS["beta-skill"]),
        ],
    )

    code, _, err = _drift(capsys, server, project_root)

    assert code == 1
    lines = err.splitlines()
    assert len([line for line in lines if "(digest @ " in line]) == 1
    assert len([line for line in lines if "(no-provenance)" in line]) == 1
    assert "  alpha-skill: " in err and "  beta-skill: " in err


# --- publish-skills: the skill library inventory -> one block per skill --------------------

CANONICAL_PATHS = {
    "alpha-skill": "skills/_canonical/process/alpha-skill",
    "gamma-skill": "skills/_canonical/core/gamma-skill",
}
PLUGIN_PATHS = {
    "beta-skill": "packages/daemon/assets/plugins/openrig-core/skills/beta-skill",
    "gamma-skill": "packages/daemon/assets/plugins/openrig-core/skills/gamma-skill",
}
LIBRARY_TEXTS = {
    "alpha-skill": "# Alpha\nlibrary body\n",
    "beta-skill": "# Beta\nlibrary body\n",
    "gamma-skill": "# Gamma\nlibrary body\n",
}


@pytest.fixture
def library(tmp_path):
    """A fixture skill library: a canonical-only skill, a plugin-only skill and an overlap."""
    skills = []
    for name, text in LIBRARY_TEXTS.items():
        row = {"name": name}
        if name in CANONICAL_PATHS:
            path = CANONICAL_PATHS[name]
            (tmp_path / path).mkdir(parents=True)
            (tmp_path / path / "SKILL.md").write_text(text, encoding="utf-8")
            row["canonical"] = {"path": path, "group": "core", "sha256": _sha256(text)}
        if name in PLUGIN_PATHS:
            path = PLUGIN_PATHS[name]
            (tmp_path / path).mkdir(parents=True)
            (tmp_path / path / "SKILL.md").write_text(text, encoding="utf-8")
            row["plugin"] = {"path": path, "sha256": _sha256(text)}
        skills.append(row)
    (tmp_path / "inventory.json").write_text(
        json.dumps(
            {
                "version": 1,
                "count": len(skills),
                "skills": skills,
                "seat_curation": {},
                "unused_by_default": [],
            }
        ),
        encoding="utf-8",
    )
    return tmp_path


def _publish(capsys, library, *extra):
    code = team_setup.main(
        [
            "publish-skills",
            "--inventory",
            str(library / "inventory.json"),
            "--source-root",
            str(library),
            *extra,
        ]
    )
    out = capsys.readouterr()
    return code, out.out, out.err


def test_publish_skills_emits_one_block_per_skill_with_provenance(
    server, env, capsys, library
):
    code, out, _ = _publish(capsys, library)

    assert code == 0
    puts = {
        r["path"].split("/")[5]: r["body"]
        for r in server.requests
        if r["method"] == "PUT"
    }
    assert set(puts) == set(LIBRARY_TEXTS)
    assert all(body["kind"] == "skill" for body in puts.values())

    alpha = json.loads(puts["alpha-skill"]["content"])
    assert alpha == {
        "content": LIBRARY_TEXTS["alpha-skill"],
        "source_path": CANONICAL_PATHS["alpha-skill"] + "/SKILL.md",
        "sha256": _sha256(LIBRARY_TEXTS["alpha-skill"]),
    }
    # a plugin-only skill has the plugin path as its source, and no mirror
    beta = json.loads(puts["beta-skill"]["content"])
    assert beta["source_path"] == PLUGIN_PATHS["beta-skill"] + "/SKILL.md"
    assert "mirror_path" not in beta and "mirror_sha256" not in beta
    # the overlap is ONE module: canonical source plus the plugin mirror, both digests
    gamma = json.loads(puts["gamma-skill"]["content"])
    assert gamma["source_path"] == CANONICAL_PATHS["gamma-skill"] + "/SKILL.md"
    assert gamma["mirror_path"] == PLUGIN_PATHS["gamma-skill"] + "/SKILL.md"
    assert gamma["mirror_sha256"] == _sha256(LIBRARY_TEXTS["gamma-skill"])
    assert len([r for r in server.requests if r["method"] == "PUT"]) == 3
    assert "module alpha-skill@1.0.0: applied" in out
    assert "publish summary: 3 pushed, 0 new version(s), 0 skipped" in out


def test_publish_skills_attempts_every_entry_and_names_every_failure(
    server, env, capsys, library
):
    """A refused block must not hide the rest: every entry is attempted, every failure is named
    with its reason, and the exit is non-zero - never a silent partial publish."""
    # the way the server refuses an oversized block (measured: openrig-user/SKILL.md, 67595 bytes)
    server.overrides[("PUT", "/api/v2/openrig/modules/beta-skill/versions/1.0.0")] = (
        400,
        '{"detail": "content must be 1 to 65536 bytes"}',
    )

    code, out, err = _publish(capsys, library)

    assert code == 1
    attempted = [
        r["path"].split("/")[5] for r in server.requests if r["method"] == "PUT"
    ]
    # every entry was attempted, including the ones ordered after the failure
    assert sorted(attempted) == sorted(LIBRARY_TEXTS)
    assert "module alpha-skill@1.0.0: applied" in out
    assert "module beta-skill@1.0.0: FAILED" in out
    assert "module gamma-skill@1.0.0: applied" in out
    # the reason travels with the failure, on stdout and in the exit error
    assert "content must be 1 to 65536 bytes" in out
    assert "beta-skill" in err and "content must be 1 to 65536 bytes" in err
    # and the summary reads as partial rather than as a short inventory
    assert "publish summary: 2 of 3 block(s) pushed, 1 FAILED, 0 skipped" in out


def test_publish_skills_rerun_skips_identical_blocks(server, env, capsys, library):
    assert _publish(capsys, library)[0] == 0
    _store_pushes(server)
    server.requests.clear()

    code, out, _ = _publish(capsys, library)

    assert code == 0
    # the second publish leaves the store untouched: every block was already stored
    assert [r for r in server.requests if r["method"] == "PUT"] == []
    assert out.count(": skipped (identical content already stored)") == 3
    assert "publish summary: 0 pushed, 0 new version(s), 3 skipped" in out


def test_publish_skills_changed_content_pushes_the_next_patch(
    server, env, capsys, library
):
    assert _publish(capsys, library)[0] == 0
    _store_pushes(server)
    changed = "# Alpha v2\nlibrary body\n"
    (library / CANONICAL_PATHS["alpha-skill"] / "SKILL.md").write_text(
        changed, encoding="utf-8"
    )
    inventory = json.loads((library / "inventory.json").read_text(encoding="utf-8"))
    for row in inventory["skills"]:
        if row["name"] == "alpha-skill":
            row["canonical"]["sha256"] = _sha256(changed)
    (library / "inventory.json").write_text(json.dumps(inventory), encoding="utf-8")
    server.requests.clear()

    code, out, _ = _publish(capsys, library)

    assert code == 0
    puts = [r for r in server.requests if r["method"] == "PUT"]
    # the stored 1.0.0 differs, so the change lands as 1.0.1 rather than an overwrite
    assert [r["path"] for r in puts] == [
        "/api/v2/openrig/modules/alpha-skill/versions/1.0.1"
    ]
    assert (
        "module alpha-skill@1.0.1 (new version; 1.0.0 stored with different content): applied"
        in out
    )


def test_publish_skills_refuses_a_secret(server, capsys, library):
    secret = "token Bearer sk-abcdefghijklmnopqrstuvwxyz012345"
    (library / CANONICAL_PATHS["alpha-skill"] / "SKILL.md").write_text(
        secret, encoding="utf-8"
    )
    inventory = json.loads((library / "inventory.json").read_text(encoding="utf-8"))
    for row in inventory["skills"]:
        if row["name"] == "alpha-skill":
            row["canonical"]["sha256"] = _sha256(secret)
    (library / "inventory.json").write_text(json.dumps(inventory), encoding="utf-8")

    code, _, err = _publish(capsys, library)

    assert code == 2
    assert "alpha-skill" in err and "ENV_VAR" in err
    assert server.requests == []


def test_publish_skills_refuses_a_stale_inventory(server, capsys, library):
    (library / CANONICAL_PATHS["alpha-skill"] / "SKILL.md").write_text(
        "# changed without regenerating the inventory\n", encoding="utf-8"
    )

    code, _, err = _publish(capsys, library)

    assert code == 2
    assert "alpha-skill" in err and "stale" in err
    assert server.requests == []


def test_publish_skills_needs_a_source_root(capsys, monkeypatch, library):
    monkeypatch.delenv("OPENRIG_SKILLS_ROOT", raising=False)

    code = team_setup.main(
        [
            "publish-skills",
            "--inventory",
            str(library / "inventory.json"),
            "--dry-run",
        ]
    )
    out = capsys.readouterr()

    assert code == 2
    assert "source-root" in out.err


# --- the shipped inventory's digests vs the committed OpenRig checkout (the guard) ---------
#
# ai_docs/agent-system/skill-library.json records, per skill, the committed OpenRig path and the
# sha256 of that skill's SKILL.md. Until this test, the only thing that verified those digests was
# `publish-skills` - a networked action that runs when someone decides to publish. So when OpenRig
# 31fe301b changed openrig-user's SKILL.md, the stored digest went stale and NOTHING SAID SO: it was
# found by re-running the rule by hand, a day later, while this seat was idle.
#
# It lives here rather than as a CLI verb because a guard that runs when a human remembers is a
# guard that does not run, and this one rides the suite every seat already runs. It recomputes
# THROUGH skill_library_modules(), the publish path's own function, so the rule has exactly one
# implementation and there is no second copy of it to drift from the first.


def _openrig_root_or_skip() -> Path:
    """The real OpenRig checkout, or a skip that says so LOUDLY.

    A skip that reads as a pass is the same silence this guard exists to end, one level down, so
    the reason names the variable, the value it holds and what to set instead.
    """
    root = os.environ.get(team_setup.LIBRARY_ROOT_ENV)
    if root and Path(root).is_dir():
        return Path(root)
    state = (
        f"{team_setup.LIBRARY_ROOT_ENV}={root!r}, which is not a directory"
        if root
        else f"{team_setup.LIBRARY_ROOT_ENV} is not set"
    )
    pytest.skip(
        f"SKIPPED, NOT PASSED ({state}): the inventory's digests can only be verified against a real "
        f"OpenRig checkout. Set {team_setup.LIBRARY_ROOT_ENV}=/path/to/openrig - the variable "
        "publish-skills --source-root and drift-check --library-root already read - to run it."
    )


def _inventory_guard(inventory_path: Path, source_root: Path) -> list:
    """The check itself: the publish path's own verifier, fed the inventory it publishes from."""
    return team_setup.skill_library_modules(
        team_setup.load_skill_inventory(inventory_path), source_root
    )


def test_the_shipped_inventory_digests_match_the_committed_openrig_checkout():
    """Every recorded digest still describes the file at its committed path."""
    source_root = _openrig_root_or_skip()
    inventory_path = team_setup.DEFAULT_SKILL_INVENTORY

    modules = _inventory_guard(inventory_path, source_root)

    inventory = team_setup.load_skill_inventory(inventory_path)
    assert inventory["count"] == len(inventory["skills"]), "the count disagrees with the rows"
    assert len(modules) == inventory["count"], "one block per skill, so a short list is a lost skill"


def test_the_inventory_guard_names_the_skill_and_both_digests(tmp_path):
    """A perturbed digest must fail, naming WHICH side moved - the case that must not be vacuous.

    Hermetic: it perturbs a COPY of the inventory under tmp_path and leaves the shipped file alone,
    so the guard is seen to fail without the shared tree being touched.
    """
    source_root = _openrig_root_or_skip()
    inventory = json.loads(team_setup.DEFAULT_SKILL_INVENTORY.read_text(encoding="utf-8"))
    row = next(entry for entry in inventory["skills"] if "canonical" in entry)
    recomputed = row["canonical"]["sha256"]
    row["canonical"]["sha256"] = "0" * 64
    perturbed = tmp_path / "skill-library.json"
    perturbed.write_text(json.dumps(inventory), encoding="utf-8")

    with pytest.raises(team_setup.SetupError) as refused:
        _inventory_guard(perturbed, source_root)

    message = str(refused.value)
    assert row["name"] in message, "the failing skill must be named"
    assert "0" * 64 in message, "the RECORDED digest must be named"
    assert recomputed in message, (
        "the RECOMPUTED digest must be named too, or the reader cannot tell which side moved"
    )
