"""Tests for agenthub_client.seat_client. No network, no rig, no seat: the cloud, the pins and
the relaunch are replaced with fakes."""

import argparse
import importlib.util
import subprocess
from pathlib import Path

import pytest

# Self-contained; must not spin up the test database.
pytestmark = pytest.mark.unit

MODULE_PATH = Path(__file__).resolve().parents[1] / "src" / "agenthub_client" / "seat_client.py"


def load_module():
    return importlib.import_module("agenthub_client.seat_client")


client = load_module()


def test_a_seat_is_behind_when_its_pin_differs_or_is_missing():
    cloud = {"lead": "h2", "go-dev": "h1", "writer": "h3"}
    pinned = {"lead": "h1", "go-dev": "h1", "writer": None}
    assert client.seats_behind(cloud, pinned) == ["lead", "writer"]


def test_wait_until_quiet_returns_true_once_the_seat_is_idle_and_still():
    assert client.wait_until_quiet(
        "rig",
        "s",
        activity=lambda *_: "idle",
        since=lambda *_: 99,
        sleep=lambda _: None,
    )


def test_wait_until_quiet_gives_up_on_a_seat_that_never_stops_running():
    now = [0.0]

    def sleep(seconds):
        now[0] += seconds

    assert not client.wait_until_quiet(
        "rig",
        "s",
        activity=lambda *_: "running",
        since=lambda *_: 99,
        sleep=sleep,
        clock=lambda: now[0],
    )


def test_wait_until_quiet_does_not_count_a_seat_that_just_wrote():
    now = [0.0]

    def sleep(seconds):
        now[0] += seconds

    assert not client.wait_until_quiet(
        "rig",
        "s",
        activity=lambda *_: "idle",
        since=lambda *_: 2,
        sleep=sleep,
        clock=lambda: now[0],
    )


class FakeSync:
    """Stands in for agenthub_client.seat_sync: a cloud of fixed hashes and pins held in a dict."""

    def __init__(self, cloud, pins):
        self.cloud, self.pins = cloud, pins

    def require_env(self, name):
        return "x"

    def fetch_rigspec(self, url, token, room):
        return {"seats": [{"seat": s, "hash": h} for s, h in self.cloud.items()]}

    def read_lock(self, path):
        hash_ = self.pins.get(path.parent.name)
        return {"hash": hash_, "path": "p"} if hash_ else None


def args(**over):
    base = dict(room="room", out="/nonexistent", rig=None, seat=None, relaunch="none")
    base.update(over)
    return argparse.Namespace(**base)


def record_runs(monkeypatch, on_run=None):
    runs = []

    def fake_run(command, **kwargs):
        runs.append(command)
        if on_run:
            on_run()
        return subprocess.CompletedProcess(command, 0, "", "")

    monkeypatch.setattr(client.subprocess, "run", fake_run)
    return runs


def test_sync_does_nothing_when_every_seat_is_in_sync(monkeypatch):
    runs = record_runs(monkeypatch)
    sync = FakeSync({"lead": "h1"}, {"lead": "h1"})
    assert client.sync_once(args(), sync) == client.EXIT_OK
    assert runs == []


def test_sync_adopts_through_the_sync_script_and_leaves_a_running_seat_alone(
    monkeypatch,
):
    sync = FakeSync({"lead": "h2"}, {"lead": "h1"})
    runs = record_runs(monkeypatch, on_run=lambda: sync.pins.update(lead="h2"))
    relaunched = []
    monkeypatch.setattr(client, "relaunch", lambda rig, seat: relaunched.append(seat))
    assert client.sync_once(args(), sync) == client.EXIT_OK
    assert len(runs) == 1 and "rig" in runs[0] and "--update" in runs[0]
    assert relaunched == []


def test_sync_fails_loudly_when_the_pin_did_not_move_to_the_cloud_hash(monkeypatch):
    sync = FakeSync({"lead": "h2"}, {"lead": "h1"})
    record_runs(monkeypatch)
    with pytest.raises(client.ClientError, match="pin did not move"):
        client.sync_once(args(), sync)


def test_relaunch_quiet_restarts_only_the_seats_that_changed_and_are_quiet(monkeypatch):
    sync = FakeSync(
        {"lead": "h2", "go-dev": "h1", "writer": "h9"},
        {"lead": "h1", "go-dev": "h1", "writer": "h1"},
    )
    record_runs(monkeypatch, on_run=lambda: sync.pins.update(lead="h2", writer="h9"))
    monkeypatch.setattr(client, "wait_until_quiet", lambda rig, seat: seat == "lead")
    relaunched = []
    monkeypatch.setattr(client, "relaunch", lambda rig, seat: relaunched.append(seat))
    assert client.sync_once(args(relaunch="quiet"), sync) == client.EXIT_FAILED
    assert relaunched == ["lead"]


def test_the_seat_filter_limits_the_work(monkeypatch):
    sync = FakeSync({"lead": "h2", "writer": "h9"}, {"lead": "h1", "writer": "h1"})
    record_runs(monkeypatch, on_run=lambda: sync.pins.update(lead="h2"))
    assert client.sync_once(args(seat=["lead"]), sync) == client.EXIT_OK
