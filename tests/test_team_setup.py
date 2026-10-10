"""Tests for agenthub_client.team_setup.

The script runs against a local HTTP server that records every request, so no
real 4genthub server is needed.
"""


import hashlib


import importlib.util


import json






import threading


from http.server import BaseHTTPRequestHandler, HTTPServer




import pytest


# These tests are self-contained and must not spin up the test database.
pytestmark = pytest.mark.unit


TOKEN = "tok-secret-1234567890"


SEAT_TYPES_PATH = "/api/v2/openrig/seat-types"


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


def test_apply_names_its_team_directory_and_has_no_default(capsys):
    """The client ships no team: ``apply`` without ``--team`` is a usage error, not a guess."""
    with pytest.raises(SystemExit) as stop:
        team_setup.main(["apply"])
    assert stop.value.code == 2
    assert "--team" in capsys.readouterr().err


def test_publish_skills_names_its_inventory_and_has_no_default(capsys):
    with pytest.raises(SystemExit) as stop:
        team_setup.main(["publish-skills"])
    assert stop.value.code == 2
    assert "--inventory" in capsys.readouterr().err


def test_the_project_root_is_the_directory_the_command_runs_from(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    assert team_setup.get_project_root() == str(tmp_path.resolve())
