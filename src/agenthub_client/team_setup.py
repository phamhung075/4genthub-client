#!/usr/bin/env python3
"""Create the 4genthub development team as an OpenRig room through the 4genthub API.

The team definition (room, seats, links, context modules, overlays) is data in
``scripts/team/4genthub/team.json``; the module texts are the files next to it.
``apply`` pushes that definition in a fixed order and is idempotent::

    1. seat types        POST /api/v2/openrig/seat-types/seed        (the seats need them;
                         idempotent, the server needs AGENTHUB_PUBLIC_URL; sent only when
                         GET /seat-types lacks one the team uses, or when no credentials
                         are set so the stored set is unknown)
    2. context modules   PUT  /api/v2/openrig/modules/{slug}/versions/{version}
    3. room              POST /api/v2/openrig/rooms                  (409 exists: ok)
    4. seats             POST /api/v2/openrig/rooms/{room}/seats     (409 exists: ok)
    5. links             PUT  /api/v2/openrig/rooms/{room}/seats/{seat}/links
    6. company overlay   PUT  /api/v2/openrig/overlay
    7. seat overlays     PUT  /api/v2/openrig/rooms/{room}/seats/{seat}/overlay

Overlay PUTs replace the previous overlay of their scope, so the full op list is
sent every time. Any other HTTP error stops the run.

``import-project`` reads the current project's local Claude configuration and pushes
it as module versions through the same ``PUT`` call ``apply`` uses::

  1. .mcp.json         one ``mcp`` module per server entry (name, type http|stdio,
                       url, or command + args, headers/env)
  2. .claude/skills/*  one ``skill`` block per directory: the SKILL.md text plus its
                       source path and sha256

The project root is the one the Claude-code hooks derive, imported from
``utils/env_loader.get_project_root`` (never re-derived here). A block never carries a
secret value: a credential-shaped literal is refused with a clear message, while a value
that names ``${ENV_VAR}`` is stored verbatim for the client runtime to expand.

Because a module version is an immutable row keyed by a concrete semver, ``import-project``
resolves each module against what the server already stores before it sends anything
(``GET /api/v2/openrig/modules/{slug}/versions/{version}``): the stored version is
published unchanged when its content is identical (SKIP), a version that does not exist is
pushed as given (PUSH), and a stored version whose content differs moves to the next patch
(NEW-VERSION, mirroring the backend's ``NextPatchVersion``) so a changed file lands as a
new version instead of overwriting an immutable one or failing.

``publish-skills`` pushes the skill library as catalog blocks: it reads the inventory
``ai_docs/agent-system/skill-library.json`` (52 skills, each with the committed path and
sha256 of its SKILL.md) and sends ONE ``skill`` block per skill through the same ``PUT``.
A skill committed on two edges (canonical and plugin) is ONE block, with the canonical copy
as ``source_path``/``sha256`` and the plugin copy as ``mirror_path``/``mirror_sha256``. The
inventory carries paths, not text, so the OpenRig checkout they are relative to is named by
``--source-root`` (or ``OPENRIG_SKILLS_ROOT``); the file read is verified against the
inventory's digest, so a stale inventory fails loudly instead of publishing a block whose
provenance is already wrong. It shares ``import-project``'s resolve-then-push idempotence.

A published skill block records where it came from: its content JSON carries ``source_path``
(the repo-relative path of the committed file) and ``sha256`` (the digest of that file's bytes
as committed). The two canonical/plugin overlap blocks also carry ``mirror_path`` and
``mirror_sha256`` for the copy they mirror. ``drift-check`` is the read-only counterpart of that
provenance: it lists the stored modules (``GET /api/v2/openrig/modules``), reads every stored
``skill`` block, and compares each recorded digest with the sha256 of the file at its path under
the root that path is relative to.

Two publishers write provenance with different path bases, so two roots are resolved in order.
``--root`` (default: the hooks-derived project root) holds ``import-project``'s paths, relative
to THIS project (``.claude/skills/<dir>/SKILL.md``). ``--library-root`` (default:
``$OPENRIG_SKILLS_ROOT``) holds ``publish-skills``' paths, relative to the OpenRig checkout
(``skills/_canonical/...``, ``packages/daemon/assets/plugins/...``). A path is tried under
``--root`` first and falls back to ``--library-root``; the root a finding resolved against (or
both roots, when the file is missing from either) is named in the line.

It reports only: it sends no write, republishes nothing and touches no file. Each finding is one
indented line; a stale digest naming the skill and both hashes, a source path whose file is gone,
a divergent canonical/mirror pair, and a block that carries no provenance (an older skill) are
separate categories, and any finding exits 1.

Environment:
  AGENTHUB_URL    base URL of the 4genthub server, e.g. https://api.4genthub.com
  AGENTHUB_TOKEN  bearer token; sent as an Authorization header, never printed.

Usage:
  4genteam team apply [--dry-run] [--team DIR]
  4genteam team import-project [--dry-run] [--version X.Y.Z]
  4genteam team publish-skills [--dry-run] [--inventory FILE] [--source-root DIR] [--version X.Y.Z]
  4genteam team drift-check [--root DIR] [--library-root DIR]

``apply --dry-run`` prints the plan without calling the API and needs no environment.
``import-project --dry-run`` reports each module's SKIP / PUSH / NEW-VERSION outcome: it
reads the stored versions (a read-only ``GET``) when ``AGENTHUB_URL`` and
``AGENTHUB_TOKEN`` are set, and otherwise prints each module's conditional outcome
without any request.
``drift-check`` needs ``AGENTHUB_URL`` and ``AGENTHUB_TOKEN`` and only reads; findings go
to stderr, one indented line each. ``--root`` is where ``import-project``'s paths live and
``--library-root`` where ``publish-skills``' paths live (see Environment).

Exit codes:
  0  success, and no drift found
  1  network or HTTP error, or drift-check found at least one finding
  2  usage or configuration error
"""

import argparse
import hashlib
import json
import os
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

# Sibling client-side helpers live next to this script; the Claude-code hooks live under the
# repository's .claude. The hooks' env_loader owns the one project-root derivation, so this
# script imports it instead of computing a second "project root" of its own.
from . import paths
from .scrub import scrub

if str(paths.HOOKS_DIR) not in sys.path:
    sys.path.insert(0, str(paths.HOOKS_DIR))
from utils.env_loader import get_project_root  # noqa: E402

DEFAULT_TEAM_DIR = paths.TEAM_DIR
# The skill library inventory (machine-readable twin of skill-library.md): 52 skills, each
# with its committed path and the sha256 of its SKILL.md, plus the per-seat curation.
DEFAULT_SKILL_INVENTORY = paths.SKILL_INVENTORY
API = "/api/v2/openrig"

EXIT_OK = 0
EXIT_REMOTE = 1
EXIT_USAGE = 2
# drift-check mirrors the digest check in the OpenRig repo's scripts/skill-edge-digests.generated.json
# family (mirror-skills.mjs --check): a header, one indented "<key>: <path> (<reason>)" line per
# finding, and a non-zero exit. A drifted tree is its own exit condition, separate from a network error.
EXIT_DRIFT = 1

IMPORT_VERSION = "1.0.0"
# The version publish-skills pushes. A published version is immutable, so a changed block that
# re-runs against what is stored lands as the next patch (NEW-VERSION), never an overwrite.
PUBLISH_VERSION = "1.0.0"
MCP_CONFIG = ".mcp.json"
SKILLS_DIR = Path(".claude") / "skills"
SKILL_FILE = "SKILL.md"
SKILL_KIND = "skill"

# A published skill block's provenance, carried in its content JSON (no schema change).
PROVENANCE_SOURCE = "source_path"
PROVENANCE_SHA256 = "sha256"
# The two canonical/plugin overlaps also record the mirror copy's path and digest, checked as a pair.
PROVENANCE_MIRROR_SOURCE = "mirror_path"
PROVENANCE_MIRROR_SHA256 = "mirror_sha256"
# The one reason token per finding, in the digest-check style: a stale digest, a source path
# with no file under --root, an older stored block with no provenance, and a canonical/mirror
# pair that diverged (a finding about the two copies, not about the stored-vs-source pair).
DRIFT_DIGEST = "digest"
DRIFT_MISSING = "missing-source"
DRIFT_NO_PROVENANCE = "no-provenance"
DRIFT_MIRROR = "mirror"
DRIFT_HEADER = "stored skill provenance drifted from disk; nothing was written."
# drift-check resolves each recorded path under the root its publisher wrote it against, in this
# order: --root (default: the hooks' project root) holds import-project's paths, relative to THIS
# project (``.claude/skills/<dir>/SKILL.md``); --library-root (default: ``$OPENRIG_SKILLS_ROOT``)
# holds publish-skills' paths, relative to the OpenRig checkout (``skills/_canonical/...``,
# ``packages/daemon/assets/plugins/...``). The root a finding resolved against is named in its line.
LIBRARY_ROOT_ENV = "OPENRIG_SKILLS_ROOT"

# A ${ENV_VAR} reference is expanded by the client runtime and is never a stored secret.
_ENV_REFERENCE = re.compile(r"\$\{[A-Za-z_][A-Za-z0-9_]*\}")
_MODULE_SLUG = re.compile(r"[a-z][a-z0-9-]*")
# The concrete-version rule the backend enforces (repositories.ValidateConcreteVersion).
_SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")

# The three outcomes import-project reports per module. SKIP is also the pseudo-method a
# resolved skip step carries into run_steps, so a skip never becomes an HTTP request.
SKIP = "SKIP"
PUSH = "PUSH"
NEW_VERSION = "NEW-VERSION"


class SetupError(Exception):
    """A failure that maps to one of the documented process exit codes."""

    def __init__(self, message: str, code: int):
        super().__init__(message)
        self.code = code


def load_team(team_dir: Path) -> dict:
    """Read team.json and attach each module's text from its file."""
    try:
        team = json.loads((team_dir / "team.json").read_text(encoding="utf-8"))
        for module in team["modules"]:
            module["content"] = (team_dir / module["file"]).read_text(encoding="utf-8")
    except (OSError, ValueError, KeyError) as err:
        raise SetupError(f"cannot load team definition from {team_dir}: {err}", EXIT_USAGE)
    return team


def module_versions(team: dict) -> dict:
    return {m["slug"]: m["version"] for m in team["modules"]}


def overlay_body(team: dict, slugs: list) -> dict:
    versions = module_versions(team)
    try:
        ops = [{"kind": "add", "slug": s, "version": versions[s], "content": ""} for s in slugs]
    except KeyError as err:
        raise SetupError(f"overlay references unknown module {err}", EXIT_USAGE)
    return {"ops": ops}


def module_step(slug: str, kind: str, version: str, content: str) -> tuple:
    """One module-version PUT: the single shape both apply and import-project push."""
    return (
        f"module {slug}@{version}", "PUT",
        f"{API}/modules/{slug}/versions/{version}",
        {"kind": kind, "content": content}, False,
    )


def next_patch(version: str) -> str:
    """``version`` with its patch incremented; mirrors the backend's ``NextPatchVersion``."""
    if not _SEMVER.fullmatch(version):
        raise SetupError(
            f"version {version!r} must be a concrete semver x.y.z to advance to the next patch",
            EXIT_USAGE,
        )
    major, minor, patch = version.split(".")
    return f"{major}.{minor}.{int(patch) + 1}"


def _credentials() -> tuple:
    """The API credentials from the environment; a missing one is a usage error."""
    base_url = os.environ.get("AGENTHUB_URL", "")
    token = os.environ.get("AGENTHUB_TOKEN", "")
    for name, value in (("AGENTHUB_URL", base_url), ("AGENTHUB_TOKEN", token)):
        if not value:
            raise SetupError(f"{name} is not set", EXIT_USAGE)
    return base_url, token


def _optional_credentials() -> tuple:
    """The API credentials when both are present; ("", "") otherwise, never an error."""
    base_url = os.environ.get("AGENTHUB_URL", "")
    token = os.environ.get("AGENTHUB_TOKEN", "")
    return (base_url, token) if base_url and token else ("", "")


def _get_json(base_url: str, token: str, path: str) -> tuple:
    """GET ``path`` and return (status, decoded JSON body); raise SetupError on transport failure."""
    request = urllib.request.Request(
        base_url.rstrip("/") + path,
        method="GET",
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            return response.status, json.loads(response.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as err:
        return err.code, None
    except (urllib.error.URLError, OSError) as err:
        reason = getattr(err, "reason", err)
        raise SetupError(f"GET {path} failed: {reason}", EXIT_REMOTE)


def get_module_version(base_url: str, token: str, slug: str, version: str):
    """Return the stored module-version body, or None when the version does not exist."""
    path = f"{API}/modules/{slug}/versions/{version}"
    status, payload = _get_json(base_url, token, path)
    if status == 404:
        return None
    if status != 200:
        raise SetupError(f"GET {path} failed: HTTP {status}", EXIT_REMOTE)
    module = payload.get("module") if isinstance(payload, dict) else None
    if not isinstance(module, dict):
        raise SetupError(f"GET {path} returned an unexpected body", EXIT_REMOTE)
    return module


def list_module_versions(base_url: str, token: str) -> list:
    """Return the newest stored version of every module (``GET /modules``)."""
    path = f"{API}/modules"
    status, payload = _get_json(base_url, token, path)
    if status != 200:
        raise SetupError(f"GET {path} failed: HTTP {status}", EXIT_REMOTE)
    modules = payload.get("modules") if isinstance(payload, dict) else None
    if not isinstance(modules, list) or not all(isinstance(m, dict) for m in modules):
        raise SetupError(f"GET {path} returned an unexpected body", EXIT_REMOTE)
    return modules



def resolve_modules(modules: list, version: str, base_url: str, token: str) -> list:
    """Classify each module against the stored versions before anything is pushed.

    A version that does not exist is PUSH; one whose stored content is identical is SKIP
    (no request is ever sent for it); one whose stored content differs moves to the next
    patch and is NEW-VERSION, so the change lands as a new immutable version.
    """
    resolved = []
    for module in modules:
        target = version
        while True:
            stored = get_module_version(base_url, token, module["slug"], target)
            if stored is None:
                action = NEW_VERSION if target != version else PUSH
                resolved.append({"module": module, "action": action, "version": target, "requested": version})
                break
            if stored.get("content") == module["content"]:
                resolved.append({"module": module, "action": SKIP, "version": target, "requested": version})
                break
            target = next_patch(target)
    return resolved


def resolved_steps(resolved: list) -> list:
    """The ordered steps for resolved modules: a PUT for each push, a SKIP marker for each skip."""
    steps = []
    for entry in resolved:
        module = entry["module"]
        if entry["action"] == SKIP:
            steps.append((f"module {module['slug']}@{entry['version']}", SKIP, "", {}, False))
            continue
        step = module_step(module["slug"], module["kind"], entry["version"], module["content"])
        if entry["action"] == NEW_VERSION:
            step = (
                f"{step[0]} (new version; {entry['requested']} stored with different content)",
            ) + step[1:]
        steps.append(step)
    return steps


def _plan_line(entry: dict) -> str:
    module = entry["module"]
    label = f"plan: module {module['slug']}@{entry['version']}"
    if entry["action"] == SKIP:
        return f"{label}: SKIP (identical content already stored)"
    if entry["action"] == NEW_VERSION:
        return f"{label}: NEW-VERSION ({entry['requested']} stored with different content)"
    return f"{label}: PUSH"


def _unresolved_plan_line(module: dict, version: str) -> str:
    """The per-module dry-run line when no server is configured to classify against."""
    label = f"plan: module {module['slug']}@{version}"
    if not _SEMVER.fullmatch(version):
        return f"{label}: PUSH"
    return (
        f"{label}: PUSH unless stored content is identical (then SKIP); "
        f"if stored content differs, NEW-VERSION {next_patch(version)}"
    )


def _refuse_secret(label: str, value: str) -> None:
    """Fail on a credential-shaped literal; ${ENV_VAR} references are allowed through."""
    if scrub(_ENV_REFERENCE.sub("", value), {})[1]:
        raise SetupError(
            f"{label}: carries a credential-shaped value; reference the secret as "
            "${ENV_VAR} instead of writing it into the module",
            EXIT_USAGE,
        )


def _module_slug(name: str, what: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-")
    if not _MODULE_SLUG.fullmatch(slug):
        raise SetupError(
            f"{what} {name!r} does not yield a module slug matching ^[a-z][a-z0-9-]*$ "
            f"(got {slug!r})",
            EXIT_USAGE,
        )
    return slug


def _refuse_block_secrets(name: str, block: dict) -> None:
    def check(field: str, value: str) -> None:
        _refuse_secret(f"mcp server {name!r} {field}", value)

    for field in ("url", "command"):
        if field in block:
            check(field, block[field])
    for index, arg in enumerate(block.get("args", [])):
        check(f"args[{index}]", arg)
    for field in ("headers", "env"):
        for key, value in block.get(field, {}).items():
            check(f"{field}.{key}", value)


def _mcp_block(name: str, entry) -> dict:
    """One .mcp.json server entry as the backend's mcp block payload."""
    if not isinstance(entry, dict):
        raise SetupError(f"mcp server {name!r} must be an object", EXIT_USAGE)
    server_type = entry.get("type")
    if server_type not in ("http", "stdio"):
        if "url" in entry and "command" not in entry:
            server_type = "http"
        elif "command" in entry and "url" not in entry:
            server_type = "stdio"
        else:
            raise SetupError(
                f"mcp server {name!r}: type must be \"http\" or \"stdio\", or be "
                "inferable from url / command",
                EXIT_USAGE,
            )
    block = {"name": name, "type": server_type}
    if server_type == "http":
        url = entry.get("url")
        if not isinstance(url, str) or not url:
            raise SetupError(f"mcp server {name!r}: an http server needs a url", EXIT_USAGE)
        block["url"] = url
    else:
        command = entry.get("command")
        if not isinstance(command, str) or not command:
            raise SetupError(f"mcp server {name!r}: a stdio server needs a command", EXIT_USAGE)
        block["command"] = command
        args = entry.get("args")
        if args:
            if not isinstance(args, list) or not all(isinstance(a, str) for a in args):
                raise SetupError(f"mcp server {name!r}: args must be an array of strings", EXIT_USAGE)
            block["args"] = args
    for field in ("headers", "env"):
        values = entry.get(field)
        if not values:
            continue
        if not isinstance(values, dict) or not all(isinstance(v, str) for v in values.values()):
            raise SetupError(f"mcp server {name!r}: {field} must be an object of strings", EXIT_USAGE)
        if server_type == "http" and field == "env":
            raise SetupError(f"mcp server {name!r}: an http server takes headers, not env", EXIT_USAGE)
        if server_type == "stdio" and field == "headers":
            raise SetupError(f"mcp server {name!r}: a stdio server takes env, not headers", EXIT_USAGE)
        block[field] = values
    _refuse_block_secrets(name, block)
    return block


def mcp_modules(project_root: Path) -> list:
    """One mcp module per .mcp.json server entry; [] when there is no config."""
    config_path = project_root / MCP_CONFIG
    if not config_path.exists():
        return []
    try:
        config = json.loads(config_path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        raise SetupError(f"cannot read {config_path}: {err}", EXIT_USAGE)
    servers = config.get("mcpServers") if isinstance(config, dict) else None
    if servers is None:
        return []
    if not isinstance(servers, dict):
        raise SetupError(f"{config_path}: mcpServers must be an object", EXIT_USAGE)
    modules = []
    for name in sorted(servers):
        block = _mcp_block(name, servers[name])
        modules.append({
            "slug": _module_slug(name, "mcp server"),
            "kind": "mcp",
            "content": json.dumps(block, indent=2),
        })
    return modules


def skill_block_content(content: str, source_path: str, sha256: str,
                        mirror_path: str = None, mirror_sha256: str = None) -> str:
    """One skill block's content JSON: the SKILL.md text plus its source provenance.

    A skill module's content is a block, never raw markdown. The Go renderer parses it and
    writes only ``content`` to skills/<slug>/SKILL.md, so a module that is not a valid block
    fails visibly at render instead of writing JSON or untraceable text into a seat.
    ``source_path`` is relative to the root its publisher used, and ``mirror_path`` /
    ``mirror_sha256`` record the second committed copy of a skill that lives on two edges.
    """
    block = {"content": content, PROVENANCE_SOURCE: source_path, PROVENANCE_SHA256: sha256}
    if mirror_path and mirror_sha256:
        block[PROVENANCE_MIRROR_SOURCE] = mirror_path
        block[PROVENANCE_MIRROR_SHA256] = mirror_sha256
    return json.dumps(block)


def skill_modules(project_root: Path) -> list:
    """One skill block per .claude/skills directory: the SKILL.md text plus its provenance.

    import-project's paths are project-root-relative (the source is this project's own tree),
    unlike publish-skills' library paths, which are relative to the OpenRig repository root.
    """
    skills_dir = project_root / SKILLS_DIR
    if not skills_dir.is_dir():
        return []
    modules = []
    for entry in sorted(skills_dir.iterdir()):
        if not entry.is_dir():
            continue
        skill_file = entry / SKILL_FILE
        if not skill_file.is_file():
            raise SetupError(f"skill {entry.name!r}: {skill_file} is missing", EXIT_USAGE)
        try:
            raw = skill_file.read_bytes()
        except OSError as err:
            raise SetupError(f"cannot read {skill_file}: {err}", EXIT_USAGE)
        content = raw.decode("utf-8", "replace")
        _refuse_secret(f"skill {entry.name!r}", content)
        modules.append({
            "slug": _module_slug(entry.name, "skill"),
            "kind": SKILL_KIND,
            "content": skill_block_content(
                content,
                skill_file.relative_to(project_root).as_posix(),
                hashlib.sha256(raw).hexdigest(),
            ),
        })
    return modules


def project_modules(project_root: Path) -> list:
    """Every module the project's local Claude configuration imports."""
    modules = mcp_modules(project_root) + skill_modules(project_root)
    slugs = [m["slug"] for m in modules]
    duplicates = sorted({slug for slug in slugs if slugs.count(slug) > 1})
    if duplicates:
        raise SetupError(
            f"module slug(s) {', '.join(duplicates)} are produced by more than one import entry",
            EXIT_USAGE,
        )
    return modules


def stored_seat_type_slugs(base_url: str, token: str) -> set:
    """Return the slug of every seat type the server already stores (``GET /seat-types``)."""
    path = f"{API}/seat-types"
    status, payload = _get_json(base_url, token, path)
    if status != 200:
        raise SetupError(f"GET {path} failed: HTTP {status}", EXIT_REMOTE)
    seat_types = payload.get("seat_types") if isinstance(payload, dict) else None
    if not isinstance(seat_types, list) or not all(isinstance(t, dict) for t in seat_types):
        raise SetupError(f"GET {path} returned an unexpected body", EXIT_REMOTE)
    return {t.get("slug") for t in seat_types}


def build_plan(team: dict, stored_seat_types: set = None) -> list:
    """Return the ordered steps: (label, method, path, body, tolerate_exists).

    The seat-types seed runs when a seat type the team uses is not stored, or when the stored set
    is unknown (``None``, a plan built without credentials). The seed publishes against the curated
    catalog, so seeding a server whose seat types already exist would fail on refs it never needed.
    """
    room = team["room"]["slug"]
    seats_path = f"{API}/rooms/{room}/seats"
    needed = {seat["seat_type"] for seat in team["seats"]}
    plan = []
    if stored_seat_types is None or not needed <= stored_seat_types:
        plan.append(("seat types (seed)", "POST", f"{API}/seat-types/seed", {}, False))
    for m in team["modules"]:
        plan.append(module_step(m["slug"], m["kind"], m["version"], m["content"]))
    plan.append((f"room {room}", "POST", f"{API}/rooms", dict(team["room"]), True))
    for seat in team["seats"]:
        plan.append((f"seat {seat['seat_key']}", "POST", seats_path, dict(seat), True))
    for link in team["links"]:
        plan.append((
            f"link {link['from']} {link['kind']} {link['to']}", "PUT",
            f"{seats_path}/{link['from']}/links",
            {"to_seat": link["to"], "kind": link["kind"], "allow": True}, False,
        ))
    if team["company_overlay"]:  # the company overlay is account-wide and a PUT replaces it
        plan.append((
            "overlay company", "PUT", f"{API}/overlay",
            overlay_body(team, team["company_overlay"]), False,
        ))
    for seat, slugs in team["seat_overlays"].items():
        plan.append((
            f"overlay seat {seat}", "PUT", f"{seats_path}/{seat}/overlay",
            overlay_body(team, slugs), False,
        ))
    return plan


def send(base_url: str, token: str, method: str, path: str, body: dict) -> tuple:
    """Return (status, response text); raise SetupError on network failure."""
    request = urllib.request.Request(
        base_url.rstrip("/") + path,
        data=json.dumps(body).encode("utf-8"),
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            return response.status, response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode("utf-8", "replace")
    except urllib.error.URLError as err:
        raise SetupError(f"{method} {path} failed: {err.reason}", EXIT_REMOTE)
    except OSError as err:
        raise SetupError(f"{method} {path} failed: {err}", EXIT_REMOTE)


def outcome(method: str, status: int, text: str, tolerate_exists: bool) -> str:
    if status in (200, 201):
        return "created" if method == "POST" else "applied"
    if status == 409 and tolerate_exists and "already exists" in text.lower():
        return "exists"
    raise SetupError(f"HTTP {status} {text[:300]}", EXIT_REMOTE)


def run_steps(steps: list, dry_run: bool, collect_failures: bool = False) -> list:
    """Print a dry-run plan, or send every non-skip step in order and report each outcome.

    With ``collect_failures``, a step that fails does not abort the run: every remaining step is
    attempted and the failures come back as ``(label, reason)`` pairs. That is for callers whose
    steps are independent, where stopping at the first error would leave the rest unattempted and
    unnamed — a 52-block publish that dies at block 30 looks like a 30-block inventory.
    """
    if dry_run:
        for label, method, path, _, _ in steps:
            print(f"plan: {label} ({method} {path})")
        return []
    base_url, token = _credentials()
    failures = []
    for label, method, path, body, tolerate_exists in steps:
        if method == SKIP:
            print(f"{label}: skipped (identical content already stored)")
            continue
        try:
            status, text = send(base_url, token, method, path, body)
            result = outcome(method, status, text, tolerate_exists)
        except SetupError as err:
            if not collect_failures:
                raise SetupError(f"{label}: {method} {path}: {err}", err.code)
            failures.append((label, f"{method} {path}: {err}"))
            print(f"{label}: FAILED {err}")
            continue
        print(f"{label}: {result}")
    return failures


def cmd_apply(args) -> None:
    base_url, token = _optional_credentials()
    stored = stored_seat_type_slugs(base_url, token) if base_url else None
    run_steps(build_plan(load_team(args.team), stored), args.dry_run)


def cmd_import_project(args) -> None:
    root = get_project_root()  # the Claude-code hooks' derivation, not a second one
    modules = project_modules(root)
    base_url, token = _optional_credentials()
    if args.dry_run:
        print(f"plan: import-project from {root} ({len(modules)} module(s))")
        if base_url:
            for entry in resolve_modules(modules, args.version, base_url, token):
                print(_plan_line(entry))
        else:
            for module in modules:
                print(_unresolved_plan_line(module, args.version))
        return
    base_url, token = _credentials()
    resolved = resolve_modules(modules, args.version, base_url, token)
    counts = {action: sum(e["action"] == action for e in resolved) for action in (PUSH, NEW_VERSION, SKIP)}
    print(f"importing {len(modules)} module(s) from {root}")
    run_steps(resolved_steps(resolved), args.dry_run)
    print(
        f"import summary: {counts[PUSH]} pushed, {counts[NEW_VERSION]} new version(s), "
        f"{counts[SKIP]} skipped"
    )


def load_skill_inventory(path: Path) -> dict:
    """Read the skill library inventory the publish consumes.

    The inventory is the machine-readable twin of skill-library.md: the skill rows (each with
    the committed path and sha256 of its SKILL.md) plus the per-seat curation. It is content
    owned elsewhere; this publish path only consumes it and never edits it.
    """
    try:
        inventory = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        raise SetupError(f"cannot read skill inventory from {path}: {err}", EXIT_USAGE)
    if not isinstance(inventory, dict) or not isinstance(inventory.get("skills"), list):
        raise SetupError(f"{path}: inventory must be an object with a skills array", EXIT_USAGE)
    return inventory


def _inventory_edge(edge: str, row: dict, name: str) -> dict:
    source = row.get(edge)
    if not isinstance(source, dict) or not source.get("path") or not source.get("sha256"):
        raise SetupError(
            f"skill {name!r}: inventory entry {edge!r} needs a path and a sha256", EXIT_USAGE
        )
    return source


def skill_library_modules(inventory: dict, source_root: Path) -> list:
    """One skill block per inventory skill: the SKILL.md text plus its source provenance.

    A skill committed on both edges is ONE module: the canonical copy is the source and the
    plugin copy is its mirror, so the block records both digests and the drift check can see
    either. Library paths are relative to the OpenRig repository root (``source_root``), not to
    this project. The recorded sha256 is the inventory's, but the file read is verified against
    it: publishing bytes the inventory does not describe would write a block whose provenance
    is stale the moment it lands, which is exactly what the drift check exists to catch.
    """
    rows = {}
    for row in inventory["skills"]:
        if not isinstance(row, dict) or not row.get("name"):
            raise SetupError("every skill row in the inventory needs a name", EXIT_USAGE)
        name = row["name"]
        merged = rows.setdefault(name, {})
        for edge in ("canonical", "plugin"):
            if edge in row:
                merged[edge] = _inventory_edge(edge, row, name)
    modules = []
    for name in sorted(rows):
        row = rows[name]
        primary_edge = "canonical" if "canonical" in row else "plugin"
        primary = row[primary_edge]
        skill_file = source_root / primary["path"] / SKILL_FILE
        try:
            raw = skill_file.read_bytes()
        except OSError as err:
            raise SetupError(
                f"skill {name!r}: cannot read {skill_file} under --source-root: {err}", EXIT_USAGE
            )
        digest = hashlib.sha256(raw).hexdigest()
        if digest != primary["sha256"]:
            raise SetupError(
                f"skill {name!r}: {skill_file} digests {digest}, but the inventory records "
                f"{primary['sha256']}; the inventory is stale - regenerate it before publishing",
                EXIT_USAGE,
            )
        content = raw.decode("utf-8", "replace")
        _refuse_secret(f"skill {name!r}", content)
        mirror = row.get("plugin") if primary_edge == "canonical" else None
        modules.append({
            "slug": _module_slug(name, "skill"),
            "kind": SKILL_KIND,
            "content": skill_block_content(
                content,
                primary["path"].rstrip("/") + "/" + SKILL_FILE,
                primary["sha256"],
                (mirror["path"].rstrip("/") + "/" + SKILL_FILE) if mirror else None,
                mirror["sha256"] if mirror else None,
            ),
        })
    return modules


def cmd_publish_skills(args) -> None:
    inventory = load_skill_inventory(args.inventory)
    source_root = args.source_root
    if source_root is None and os.environ.get("OPENRIG_SKILLS_ROOT"):
        source_root = Path(os.environ["OPENRIG_SKILLS_ROOT"])
    if source_root is None:
        raise SetupError(
            "publish-skills needs the OpenRig checkout the inventory paths are relative to: "
            "pass --source-root DIR or set OPENRIG_SKILLS_ROOT",
            EXIT_USAGE,
        )
    modules = skill_library_modules(inventory, source_root)
    base_url, token = _optional_credentials()
    if args.dry_run:
        print(f"plan: publish-skills {len(modules)} skill block(s) from {args.inventory}")
        if base_url:
            for entry in resolve_modules(modules, args.version, base_url, token):
                print(_plan_line(entry))
        else:
            for module in modules:
                print(_unresolved_plan_line(module, args.version))
        return
    base_url, token = _credentials()
    resolved = resolve_modules(modules, args.version, base_url, token)
    counts = {action: sum(e["action"] == action for e in resolved) for action in (PUSH, NEW_VERSION, SKIP)}
    print(f"publishing {len(modules)} skill block(s) from {args.inventory}")
    failures = run_steps(resolved_steps(resolved), args.dry_run, collect_failures=True)
    if failures:
        # Attempt every entry rather than stopping at the first refusal: a publish that dies at
        # block 30 of 52 leaves 22 unattempted and reads as a 30-block inventory. The failures are
        # named here and carried into the exit error, so a partial publish is visible as partial.
        pushed = counts[PUSH] + counts[NEW_VERSION] - len(failures)
        print(
            f"publish summary: {pushed} of {counts[PUSH] + counts[NEW_VERSION]} block(s) pushed, "
            f"{len(failures)} FAILED, {counts[SKIP]} skipped"
        )
        raise SetupError(
            f"{len(failures)} skill block(s) not published: "
            + "; ".join(f"{label}: {reason}" for label, reason in failures),
            EXIT_REMOTE,
        )
    print(
        f"publish summary: {counts[PUSH]} pushed, {counts[NEW_VERSION]} new version(s), "
        f"{counts[SKIP]} skipped"
    )


def _sha256_file(path: Path) -> str:
    """The lowercase hex sha256 of the file's bytes."""
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _provenance_path(value) -> str:
    """A recorded path/digest is provenance only when it is a non-empty string."""
    return value if isinstance(value, str) and value else ""


def stored_provenance(module: dict):
    """The provenance a stored skill block records, or None when it carries none.

    An older block published before provenance carries raw skill text, or JSON without both
    ``source_path`` and ``sha256``; that is the no-provenance category, reported apart from a
    digest mismatch. The canonical/plugin overlaps additionally record ``mirror_path`` and
    ``mirror_sha256``; that pair is checked too only when both keys are present.
    """
    content = module.get("content")
    if not isinstance(content, str):
        return None
    try:
        parsed = json.loads(content)
    except ValueError:
        return None
    if not isinstance(parsed, dict):
        return None
    source_path = _provenance_path(parsed.get(PROVENANCE_SOURCE))
    stored = _provenance_path(parsed.get(PROVENANCE_SHA256))
    if not source_path or not stored:
        return None
    mirror_path = _provenance_path(parsed.get(PROVENANCE_MIRROR_SOURCE))
    mirror_stored = _provenance_path(parsed.get(PROVENANCE_MIRROR_SHA256))
    mirror = (mirror_path, mirror_stored) if mirror_path and mirror_stored else None
    return {"source_path": source_path, "sha256": stored, "mirror": mirror}


def stored_skill_blocks(base_url: str, token: str, summaries: list) -> list:
    """The full body of every stored skill module in a listing; the listing itself has no content."""
    blocks = []
    for summary in summaries:
        if summary.get("kind") != SKILL_KIND:
            continue
        slug, version = summary.get("slug", ""), summary.get("version", "")
        module = get_module_version(base_url, token, slug, version)
        if module is None:
            raise SetupError(
                f"stored module {slug}@{version} disappeared between listing and read", EXIT_REMOTE
            )
        blocks.append(module)
    return blocks


def _pair_finding(slug: str, path: str, stored: str, roots: list, mirror: bool):
    """The finding for one recorded path/digest pair, or None when it matches a file under a root.

    The path is tried under each root in order and the first file found decides the answer, so
    with no match it is the missing-source finding for every root that was tried.
    """
    for root in roots:
        target = root / path
        if not target.is_file():
            continue
        computed = _sha256_file(target)
        if computed == stored:
            return None
        return {
            "slug": slug, "reason": DRIFT_MIRROR if mirror else DRIFT_DIGEST,
            "source_path": path, "stored": stored, "computed": computed, "root": str(root),
        }
    return {"slug": slug, "reason": DRIFT_MISSING, "source_path": path, "stored": stored,
            "roots": [str(root) for root in roots]}


def drift_findings(blocks: list, roots: list) -> list:
    """One finding per stored digest pair that drifted or lost its file, plus no-provenance blocks.

    Read-only: it hashes each recorded path under the roots it may belong to and compares. The
    primary ``source_path``/``sha256`` pair is always checked; a block that also records a
    ``mirror_path``/``mirror_sha256`` pair has that pair checked too, independently, and a
    divergent mirror is its own ``mirror`` finding rather than a stored-vs-source mismatch.
    """
    findings = []
    for module in blocks:
        slug = module.get("slug", "")
        provenance = stored_provenance(module)
        if provenance is None:
            findings.append({
                "slug": slug, "reason": DRIFT_NO_PROVENANCE, "version": module.get("version", ""),
            })
            continue
        primary = _pair_finding(slug, provenance["source_path"], provenance["sha256"], roots, False)
        if primary is not None:
            findings.append(primary)
        if provenance["mirror"] is not None:
            mirror_path, mirror_stored = provenance["mirror"]
            mirror_finding = _pair_finding(slug, mirror_path, mirror_stored, roots, True)
            if mirror_finding is not None:
                findings.append(mirror_finding)
    return findings


def _drift_line(finding: dict) -> str:
    """One finding as the digest check prints them: indented ``<key>: <path> (<reason>)`` + detail.

    A resolved path names the root it matched under; a missing one names every root tried.
    """
    reason, slug = finding["reason"], finding["slug"]
    if reason == DRIFT_NO_PROVENANCE:
        return f"  {slug}: stored {finding['version']} ({reason}) carries no source_path/sha256"
    path = finding["source_path"]
    if reason == DRIFT_MISSING:
        roots = " or ".join(finding["roots"])
        return f"  {slug}: {path} ({reason}) no file under {roots}"
    return (
        f"  {slug}: {path} ({reason} @ {finding['root']}) "
        f"stored {finding['stored']} computed {finding['computed']}"
    )


def _drift_roots(args) -> list:
    """The roots a recorded path may be relative to, in resolution order (see LIBRARY_ROOT_ENV)."""
    roots = [Path(args.root) if args.root is not None else Path(get_project_root())]
    library = args.library_root
    if library is None and os.environ.get(LIBRARY_ROOT_ENV):
        library = Path(os.environ[LIBRARY_ROOT_ENV])
    if library is not None and Path(library) not in roots:
        roots.append(Path(library))
    return roots


def cmd_drift_check(args) -> int:
    """Report stored skill provenance against disk; it reads only and writes nothing.

    Returns EXIT_DRIFT when there is at least one finding, EXIT_OK on a clean tree.
    """
    roots = _drift_roots(args)
    base_url, token = _credentials()
    blocks = stored_skill_blocks(base_url, token, list_module_versions(base_url, token))
    findings = drift_findings(blocks, roots)
    if not findings:
        return EXIT_OK
    print(DRIFT_HEADER, file=sys.stderr)
    for finding in findings:
        print(_drift_line(finding), file=sys.stderr)
    return EXIT_DRIFT


def main(argv: list = None) -> int:
    parser = argparse.ArgumentParser(
        prog="4genteam team",
        description="Create the 4genthub development team as an OpenRig room.",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    apply = subparsers.add_parser("apply", help="create modules, room, seats, links, overlays")
    apply.add_argument("--dry-run", action="store_true", help="print the plan, call nothing")
    apply.add_argument("--team", type=Path, default=DEFAULT_TEAM_DIR, help="team definition directory")
    apply.set_defaults(func=cmd_apply)
    import_project = subparsers.add_parser(
        "import-project",
        help="push .mcp.json servers and .claude/skills as module versions",
    )
    import_project.add_argument("--dry-run", action="store_true", help="print the plan, call nothing")
    import_project.add_argument(
        "--version", default=IMPORT_VERSION, help="module version to publish (default 1.0.0)"
    )
    import_project.set_defaults(func=cmd_import_project)
    publish = subparsers.add_parser(
        "publish-skills",
        help="push the skill library inventory as kind=skill blocks with provenance",
    )
    publish.add_argument("--dry-run", action="store_true", help="print the plan, call nothing")
    publish.add_argument(
        "--inventory", type=Path, default=DEFAULT_SKILL_INVENTORY,
        help="skill library inventory JSON (default: ai_docs/agent-system/skill-library.json)",
    )
    publish.add_argument(
        "--source-root", type=Path, default=None,
        help="OpenRig checkout the inventory paths are relative to "
             "(default: $OPENRIG_SKILLS_ROOT)",
    )
    publish.add_argument(
        "--version", default=PUBLISH_VERSION, help="module version to publish (default 1.0.0)"
    )
    publish.set_defaults(func=cmd_publish_skills)
    drift = subparsers.add_parser(
        "drift-check",
        help="report stored skill provenance that no longer matches the files on disk",
    )
    drift.add_argument(
        "--root", type=Path, default=None,
        help="project root import-project's paths are relative to "
             "(default: the hooks' project root)",
    )
    drift.add_argument(
        "--library-root", type=Path, default=None,
        help="OpenRig checkout publish-skills' paths are relative to "
             "(default: $OPENRIG_SKILLS_ROOT)",
    )
    drift.set_defaults(func=cmd_drift_check)

    args = parser.parse_args(argv)
    try:
        result = args.func(args)
    except SetupError as err:
        print(f"error: {err}", file=sys.stderr)
        return err.code
    return EXIT_OK if result is None else result


if __name__ == "__main__":
    sys.exit(main())
