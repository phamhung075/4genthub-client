"""Tests for ``agenthub_client.cli``.

THE TWO FAULTS THIS FILE PINS, both found by skills-dev on 2026-10-09 and both reproduced before
this file was written:

  (1) ``4genteam compact --help`` treated ``--help`` as the RIG NAME. It therefore started a
      supervisor, the child died instantly on ``error: argument --rig: expected one argument``,
      AND the verb still printed ``compact supervisor: pid N`` and returned 0. The half that
      matters most is that A FAILED START READ AS A SUCCESSFUL ONE, on the documented way to
      restart the supervisor.
  (2) there was no per-verb ``--help`` at all for the lifecycle verbs: help was handled only when
      it was the first token, so ``up --help``, ``compact --help``, ``stop --help``,
      ``status --help`` and ``log --help`` all ran the verb with ``--help`` as the rig.
"""

import subprocess

from agenthub_client import cli

LIFECYCLE_VERBS = ("up", "compact", "stop", "status", "log", "ui")
TOP_LEVEL_DOC_MARKER = "one entry point for everything"


def _no_side_effects(monkeypatch):
    """Neutralise every action a lifecycle verb can take.

    TEST 1 MUST BE SAFE TO RUN AGAINST THE UNFIXED CODE, because it is the test FOR the unfixed
    behaviour: with the faults in place `4genteam up --help` reached ensure_daemon(),
    start_supervisor(), watch and os.execvp("herdr") with `--help` as the rig, and `log --help`
    reached os.execvp("tail") on a log path that does not exist. A test whose failure can start a
    stack or hang is not a test, so every action is replaced here.
    """
    monkeypatch.setattr(cli, "ensure_daemon", lambda: None)
    monkeypatch.setattr(cli, "ensure_rig", lambda rig: None)
    monkeypatch.setattr(cli, "ensure_forcecompact", lambda: None)
    monkeypatch.setattr(cli, "start_bridge", lambda: True)
    monkeypatch.setattr(cli, "argv_tool", lambda *a: 0)
    monkeypatch.setattr(cli, "open_ui", lambda: None)
    monkeypatch.setattr(cli, "start_supervisor", lambda rig: True)
    monkeypatch.setattr(cli, "stop_supervisor", lambda rig, quiet=False: None)
    monkeypatch.setattr(cli, "supervisor_status", lambda rig: None)
    monkeypatch.setattr(cli.os, "execvp", lambda *a: None)


def test_help_is_help_for_every_lifecycle_verb(monkeypatch, capsys):
    """(2) A help flag after ANY verb prints that verb's usage and does nothing else."""
    _no_side_effects(monkeypatch)
    for verb in LIFECYCLE_VERBS:
        for flag in ("-h", "--help"):
            code = cli.main([verb, flag])
            out, err = capsys.readouterr()
            assert code == 0, f"{verb} {flag}: exit {code}, want 0 (stderr: {err!r})"
            assert f"4genteam {verb}" in out, (
                f"{verb} {flag}: the help did not name that verb: {out!r}"
            )
            assert TOP_LEVEL_DOC_MARKER not in out, (
                f"{verb} {flag}: printed the top-level document instead of the verb's own help"
            )
            assert "pid" not in out, f"{verb} {flag}: printed a pid while asked for help"


def test_help_is_help_for_the_feedback_verb(capsys):
    """(2) ``feedback --help`` used to hand the flag to the packaged shell client, which refused it
    as an unknown option and exited 2, instead of printing usage and exiting 0. The script is
    side-effect free about the flag, so this calls the verb for real and reads what comes back."""
    code = cli.main(["feedback", "--help"])
    out, err = capsys.readouterr()
    assert code == 0, f"feedback --help exited {code}: {err!r}"
    assert "4genteam feedback" in out, (
        f"the help did not name the verb: {out!r} (stderr: {err!r})"
    )


def test_help_after_a_verb_never_runs_a_lifecycle_action(monkeypatch):
    """(1) the exact mechanism: ``--help`` must not reach start_supervisor as a rig name."""
    ran = []
    monkeypatch.setattr(cli, "start_supervisor", lambda rig: ran.append(("start", rig)) or True)
    monkeypatch.setattr(cli, "stop_supervisor", lambda rig, quiet=False: ran.append(("stop", rig)))
    monkeypatch.setattr(cli, "open_ui", lambda: ran.append(("ui", None)))
    monkeypatch.setattr(cli, "ensure_daemon", lambda: ran.append(("daemon", None)))

    assert cli.main(["compact", "--help"]) == 0
    assert cli.main(["up", "--help"]) == 0
    assert ran == [], f"asking for help ran a lifecycle action: {ran}"


def test_up_initialises_every_service_in_order(monkeypatch):
    """`up` starts the daemon, restores the rig, checks forcecompact, then the supervisor, watch view and UI."""
    ran = []
    for name, args in (("ensure_daemon", ()), ("ensure_rig", ("rig",)), ("ensure_forcecompact", ()), ("start_bridge", ()), ("open_ui", ())):
        monkeypatch.setattr(cli, name, lambda *a, n=name: ran.append(n))
    monkeypatch.setattr(cli, "start_supervisor", lambda rig: ran.append("supervisor") or True)
    monkeypatch.setattr(cli, "argv_tool", lambda *a: ran.append("watch") or 0)
    monkeypatch.setattr(cli.os, "execvp", lambda *a: ran.append("herdr"))
    monkeypatch.delenv("HERDR_ENV", raising=False)

    assert cli.main(["up"]) == 0
    assert ran == ["ensure_daemon", "ensure_rig", "ensure_forcecompact", "start_bridge", "supervisor", "watch", "open_ui", "herdr"]


class _DiedImmediately:
    """A child that is already dead when the start check runs - what ``--rig --help`` did."""

    pid = 4242
    returncode = 2

    def wait(self, timeout=None):
        return self.returncode


class _StillRunning:
    """A child that outlives the start check, which is what a supervisor does."""

    pid = 4243
    returncode = None

    def wait(self, timeout=None):
        raise subprocess.TimeoutExpired(cmd="compact", timeout=timeout)


def _spawn_returns(monkeypatch, proc):
    monkeypatch.setattr(cli.subprocess, "Popen", lambda *a, **k: proc)
    monkeypatch.setattr(cli, "stop_supervisor", lambda rig, quiet=False: None)
    monkeypatch.setattr(cli.paths, "LOG_DIR", _writable_tmp(monkeypatch))


def _writable_tmp(monkeypatch):
    import tempfile
    from pathlib import Path

    root = Path(tempfile.mkdtemp(prefix="cli-test-"))
    return root


def test_a_start_that_dies_immediately_prints_no_pid_and_does_not_return_zero(
    monkeypatch, capsys
):
    """(1) THE HALF THAT MATTERS: a failed start must not read as a successful one."""
    _spawn_returns(monkeypatch, _DiedImmediately())

    code = cli.main(["compact", "some-rig"])
    out, err = capsys.readouterr()
    assert code != 0, f"a start whose child died immediately returned {code}"
    assert "pid" not in out, f"a failed start printed a pid: {out!r}"
    assert "exited immediately" in err, f"the failure was not named on stderr: {err!r}"


def test_a_start_that_survives_still_reports_its_pid(monkeypatch, capsys):
    """The control: the fix must not break the successful path the verb exists for."""
    _spawn_returns(monkeypatch, _StillRunning())

    code = cli.main(["compact", "some-rig"])
    out, err = capsys.readouterr()
    assert code == 0, f"a healthy start returned {code} (stderr: {err!r})"
    assert "pid 4243" in out, f"a healthy start did not report its pid: {out!r}"
