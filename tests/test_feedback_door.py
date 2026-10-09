"""Tests for the `4genteam feedback` door: the submission path for a runtime with no MCP.

The script itself is exercised for real against a routed server by the Go guard in
``agenthub_go/fastmcp/server/httpapp/seat_feedback_script_test.go``, which compares the row it
produces with the row the MCP tool produces. What this file holds is the wiring that guard cannot
see, in the two ways it fails silently:

* the verb must reach the script rather than fall through to the lifecycle path, because that
  fall-through runs ``up`` - a seat asking for the friction channel would start a whole rig instead;
* the script must still sit next to this module, because ``[tool.setuptools.package-data]`` names it
  by filename and a rename would otherwise drop it from the wheel with no error anywhere.
"""

import importlib
from pathlib import Path

import pytest

pytestmark = pytest.mark.unit

cli = importlib.import_module("agenthub_client.cli")


def test_feedback_runs_the_script_and_never_falls_through_to_up(monkeypatch):
    """`feedback` is a verb of its own; an unknown layer is refused by the script before it dials."""
    def forbidden(*args, **kwargs):
        pytest.fail("`feedback` fell through to the lifecycle path, so it would have started a rig")

    monkeypatch.setattr(cli, "lifecycle", forbidden)
    monkeypatch.setenv("AGENTHUB_TOKEN", "unit-test-token")

    # The layer vocabulary is the script's own closed set, checked before any request is made, so
    # this needs no server: a wrong layer is the cheapest proof that the script is what ran.
    assert cli.main(["feedback", "--layer", "harness", "--text", "t", "--url", "http://127.0.0.1:1"]) == 2


def test_the_script_the_wheel_must_carry_is_still_next_to_the_module():
    """`seat_feedback.sh` is the door's whole body: it travels with the module or the verb dies."""
    assert cli.FEEDBACK_SCRIPT == Path(cli.__file__).with_name("seat_feedback.sh")
    assert cli.FEEDBACK_SCRIPT.is_file()
