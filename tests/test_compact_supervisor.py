"""Tests for agenthub_client.compact: tell once past the limit, compact only after the job ends."""

import importlib.util
import json
import os
import sys
import time
from pathlib import Path

from agenthub_client import compact as sup


def seat_log(tmp_path, tokens, age):
    path = tmp_path / "s.jsonl"
    path.write_text(json.dumps({"message": {"usage": {"totalTokens": tokens}}}) + "\n")
    stamp = time.time() - age
    os.utime(path, (stamp, stamp))
    return path


def run_step(monkeypatch, path, state, working=False):
    sent = []
    monkeypatch.setattr(sup.watch, "rig_seats", lambda rig: ["lead"])
    monkeypatch.setattr(sup.watch, "seat_log", lambda rig, seat: (path, "omp"))
    monkeypatch.setattr(sup, "idle", lambda rig, seat: not working)
    monkeypatch.setattr(
        sup,
        "say",
        lambda rig, seat, text, raw=False: sent.append(
            text if not raw else "/compact RAW"
        ),
    )
    sup.step("r", state, quiet=180)
    return sent


def test_a_seat_under_the_limit_is_left_alone(monkeypatch, tmp_path):
    assert run_step(monkeypatch, seat_log(tmp_path, sup.watch.COMPACT_LIMIT - 1, 999), {}) == []


def test_a_seat_past_the_limit_still_working_is_told_once_and_not_compacted(
    monkeypatch, tmp_path
):
    state = {}
    path = seat_log(tmp_path, 200_000, 5)
    first = run_step(monkeypatch, path, state)
    assert len(first) == 1 and "Context limit" in first[0]
    assert "Do not type /compact" in first[0]
    assert run_step(monkeypatch, path, state) == []


def test_a_quiet_seat_past_the_limit_is_compacted_after_the_notice(
    monkeypatch, tmp_path
):
    sent = run_step(monkeypatch, seat_log(tmp_path, 200_000, 600), {})
    assert [t[:5] for t in sent] == ["Conte", "/comp"]


def test_a_seat_that_dropped_below_the_limit_is_told_again_next_time(
    monkeypatch, tmp_path
):
    state = {}
    run_step(monkeypatch, seat_log(tmp_path, 200_000, 5), state)
    run_step(monkeypatch, seat_log(tmp_path, 40_000, 5), state)
    assert state["lead"]["told"] is False


def test_a_seat_at_the_hard_limit_is_compacted_without_waiting_for_quiet(
    monkeypatch, tmp_path
):
    sent = run_step(monkeypatch, seat_log(tmp_path, 320_000, 5), {})
    assert [t[:5] for t in sent] == ["Conte", "VERY ", "/comp"]


def test_a_working_seat_at_the_hard_limit_is_interrupted_not_sent_compact(
    monkeypatch, tmp_path
):
    interrupted = []
    monkeypatch.setattr(sup, "interrupt", lambda rig, seat: interrupted.append(seat))
    state = {}
    path = seat_log(tmp_path, 320_000, 600)
    sent = run_step(monkeypatch, path, state, working=True)
    assert [t[:5] for t in sent] == ["Conte", "VERY "] and interrupted == ["lead"]
    run_step(monkeypatch, path, state, working=True)
    assert interrupted == ["lead"]  # one interrupt per gap


def test_a_seat_past_the_warn_limit_is_warned_once(monkeypatch, tmp_path):
    state = {}
    path = seat_log(tmp_path, 260_000, 5)
    sent = run_step(monkeypatch, path, state)
    assert any("VERY IMPORTANT" in t for t in sent)
    assert not any("VERY IMPORTANT" in t for t in run_step(monkeypatch, path, state))
