"""Tests for agenthub_client.seat_policy.

They render and apply to a temporary state root; no seat, daemon or omp process is needed.
"""

import importlib.util
import inspect
import os
import pwd
from pathlib import Path

import pytest
import yaml

# These tests are self-contained and must not spin up the test database.
pytestmark = pytest.mark.unit

MODULE_PATH = Path(__file__).resolve().parents[1] / "src" / "agenthub_client" / "seat_policy.py"
RIG = "4genthub-min"


def load_module():
    return importlib.import_module("agenthub_client.seat_policy")


policy = load_module()


def parsed(seat: str) -> dict:
    return yaml.safe_load(policy.render_config(seat, policy.SEAT_ROLES[RIG][seat], RIG))


def deny_patterns(config: dict) -> list[str]:
    """The bash patterns that DENY. The startup exemption is `allow` and is asserted on its own."""
    return [
        rule["match"]
        for rule in config["bash"]["patterns"]
        if rule["approval"] == "deny"
    ]


def test_every_seat_exempts_the_startup_rig_whoami_and_only_that():
    """The startup file orders `rig whoami` first and ask.timeout is 0 live, so on an always-ask seat
    an unanswered prompt blocks the seat on its first call. The exemption is one entry, on every seat,
    and nothing else may be allowed by this file."""
    for seat in policy.SEAT_ROLES[RIG]:
        patterns = parsed(seat)["bash"]["patterns"]
        allows = [rule["match"] for rule in patterns if rule["approval"] == "allow"]
        assert allows == ["rig whoami*"], f"{seat}: allowed patterns are {allows}"
        assert "rig whoami*" not in deny_patterns(parsed(seat))


def tools_denied(config: dict) -> list[str]:
    return sorted(config.get("tools", {}).get("approval", {}))


def test_every_seat_waits_for_mcp_and_checks_compound_commands():
    for seat in policy.SEAT_ROLES[RIG]:
        config = parsed(seat)
        assert config["mcp"]["startupTimeoutMs"] == 0
        assert config["bash"]["allowCompoundCommands"] is True


def test_every_seat_including_the_lead_is_denied_push_amend_and_production_access():
    for seat in policy.SEAT_ROLES[RIG]:
        patterns = deny_patterns(parsed(seat))
        for rule in (
            "git push*",
            "git commit*--amend*",
            "git add -A*",
            "git reset --hard*",
            "ssh *",
            "tmux kill-*",
        ):
            assert rule in patterns, f"{seat} is not denied {rule}"


def test_only_the_lead_keeps_rig_launch_and_the_agent_seat_connection_tools():
    lead = parsed("lead")
    assert "rig launch*" not in deny_patterns(lead)
    assert tools_denied(lead) == []
    for seat in policy.SEAT_ROLES[RIG]:
        if seat == "lead":
            continue
        config = parsed(seat)
        assert "rig launch*" in deny_patterns(config)
        assert {
            "mcp__agenthub_http_manage_agent",
            "mcp__agenthub_http_manage_connection",
            "mcp__agenthub_http_manage_seat",
        } <= set(tools_denied(config))


def test_the_context_sync_tools_are_never_denied():
    for seat in policy.SEAT_ROLES[RIG]:
        denied = tools_denied(parsed(seat))
        for tool in (
            "manage_task",
            "manage_subtask",
            "manage_context",
            "manage_project",
            "manage_git_branch",
            "call_seat",
        ):
            assert f"mcp__agenthub_http_{tool}" not in denied
        assert not any("deepseek" in name for name in denied)


def test_only_the_reviewer_loses_the_edit_tools():
    for seat in policy.SEAT_ROLES[RIG]:
        denied = tools_denied(parsed(seat))
        assert (("edit" in denied) and ("ast_edit" in denied)) == (seat == "reviewer")


def test_an_unlisted_rig_or_seat_has_no_permissive_default(tmp_path):
    with pytest.raises(SystemExit):
        policy.seat_roles("unknown-rig")
    assert policy.main(["show", "nobody", "--rig", RIG]) == policy.EXIT_USAGE


def seed_state(root: Path) -> None:
    for seat in policy.SEAT_ROLES[RIG]:
        policy.config_path(root, RIG, seat).parent.mkdir(parents=True)


def test_apply_writes_every_seat_is_idempotent_and_check_reports_drift(tmp_path):
    seed_state(tmp_path)
    args = ["--rig", RIG, "--state-root", str(tmp_path)]
    assert policy.main(["apply", *args, "--check"]) == policy.EXIT_DRIFT
    assert not policy.config_path(tmp_path, RIG, "lead").exists()

    assert policy.main(["apply", *args]) == policy.EXIT_OK
    for seat, role in policy.SEAT_ROLES[RIG].items():
        assert policy.config_path(
            tmp_path, RIG, seat
        ).read_text() == policy.render_config(seat, role, RIG)

    assert policy.main(["apply", *args, "--check"]) == policy.EXIT_OK
    policy.config_path(tmp_path, RIG, "writer").write_text("tools: {}\n")
    assert policy.main(["apply", *args, "--check"]) == policy.EXIT_DRIFT


def test_apply_refuses_a_seat_that_was_never_launched(tmp_path):
    assert (
        policy.main(["apply", "--rig", RIG, "--state-root", str(tmp_path)])
        == policy.EXIT_USAGE
    )


def test_check_tolerates_keys_the_policy_does_not_define_and_still_catches_a_broken_rule(
    tmp_path,
):
    """The pipeline MERGES this document into the runtime's file, so `--check` compares the policy's
    own keys rather than the whole file - and still reports a rule that is actually wrong."""
    seed_state(tmp_path)
    args = ["--rig", RIG, "--state-root", str(tmp_path)]
    assert policy.main(["apply", *args]) == policy.EXIT_OK
    path = policy.config_path(tmp_path, RIG, "go-dev")

    path.write_text(path.read_text() + "\nmodel: something-else\n")
    assert policy.main(["apply", *args, "--check"]) == policy.EXIT_OK

    path.write_text(path.read_text().replace('"git push*"', '"git pull*"'))
    assert policy.main(["apply", *args, "--check"]) == policy.EXIT_DRIFT


def machine_state_root() -> Path:
    """The state root the DEFAULT must resolve to: the real user's home, from the passwd
    database, which is the one source that is the same inside and outside a seat."""
    return Path(pwd.getpwuid(os.getuid()).pw_dir) / ".openrig" / "state" / "omp"


def test_the_default_state_root_survives_a_home_that_points_at_a_seat(monkeypatch):
    """INSIDE A SEAT, HOME *IS* THE SEAT'S STATE DIRECTORY.

    OpenRig launches a seat with HOME=/home/<user>/.openrig/state/omp/<rig>-<seat>@<rig>, so a
    default built on ``Path.home()`` appended the state root to itself and the documented
    ``apply --rig RIG --check`` reported every seat "no agent directory at ...<seat state>/
    .openrig/state/omp/<rig>-<seat>@<rig>/agent" and exited 2. The assertion is the RESOLVED
    PATH rather than a message: with HOME at a seat's own state directory the default must
    still be the machine-level state root under the REAL user's home, and the seat's own
    directory must not appear in it."""
    seat_state = machine_state_root() / f"{RIG}-web-dev@{RIG}"
    monkeypatch.setenv("HOME", str(seat_state))

    reloaded = load_module()

    assert reloaded.DEFAULT_STATE_ROOT == machine_state_root()
    # THE DOUBLING, as a path assertion: the seat's own state dir must not be a prefix.
    assert str(seat_state) not in str(reloaded.DEFAULT_STATE_ROOT)
    # And the path the tool actually uses for a seat is single-nested under that root.
    assert reloaded.config_path(reloaded.DEFAULT_STATE_ROOT, RIG, "web-dev") == (
        machine_state_root() / f"{RIG}-web-dev@{RIG}" / "agent" / "config.yml"
    )


def test_the_default_state_root_is_the_same_under_any_HOME(monkeypatch, tmp_path):
    """Independence stated as an equality: two different HOMEs, one resolved default."""
    monkeypatch.setenv("HOME", str(tmp_path / "elsewhere"))
    outside_a_seat = load_module().DEFAULT_STATE_ROOT
    monkeypatch.setenv("HOME", str(machine_state_root() / f"{RIG}-lead@{RIG}"))
    inside_a_seat = load_module().DEFAULT_STATE_ROOT

    assert outside_a_seat == inside_a_seat == machine_state_root()


def test_every_seat_keeps_only_the_recent_tail_verbatim_at_a_compaction():
    for seat in policy.SEAT_ROLES[RIG]:
        assert parsed(seat)["compaction"] == {
            "keepRecentTokens": policy.KEEP_RECENT_TOKENS
        }


def test_only_the_trial_seat_runs_a_lower_thinking_level():
    def level(seat):
        text = policy.render_config(seat, policy.SEAT_ROLES[RIG][seat], RIG)
        return yaml.safe_load(text).get("defaultThinkingLevel")

    assert level("writer") == "medium"
    assert {level(s) for s in policy.SEAT_ROLES[RIG] if s != "writer"} == {None}


def test_render_config_refuses_a_call_that_omits_the_rig():
    """The rig is REQUIRED, and this is the defect the requirement closes.

    With a default of None, a caller that omitted the rig rendered a document whose per-rig thinking
    level was simply ABSENT - no exception, no log line, no test - so the seat came up on the wrong
    level and only a reader of the rendered file could notice. Both halves are asserted here: the
    call fails where the argument is missing, and the signature carries no default for a future
    caller to lean on, so a reintroduced default fails in this file rather than in a seat.
    """
    with pytest.raises(TypeError):
        policy.render_config("writer", "writer")

    assert (
        inspect.signature(policy.render_config).parameters["rig"].default
        is inspect.Parameter.empty
    )
