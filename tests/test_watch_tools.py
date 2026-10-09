"""Tests for agenthub_client.watch: how a session log line becomes a feed line."""

import importlib.util
import json
from pathlib import Path

import pytest

# Self-contained; must not spin up the test database.
pytestmark = pytest.mark.unit

MODULE_PATH = Path(__file__).resolve().parents[1] / "src" / "agenthub_client" / "watch.py"


def load_module():
    return importlib.import_module("agenthub_client.watch")


watch = load_module()


def line(role: str, content, **extra) -> str:
    return json.dumps({"message": {"role": role, "content": content, **extra}})


def test_a_tool_call_shows_its_name_and_the_main_argument():
    call = [
        {
            "type": "toolCall",
            "name": "bash",
            "arguments": {"command": "git status", "cwd": "/x"},
        }
    ]
    (text,) = watch.events(line("assistant", call), 200)
    assert "bash" in text and "command=git status" in text and "+cwd" in text


def test_only_a_real_policy_refusal_is_marked_blocked():
    refusal = 'Tool "bash" is blocked by tool policy. Reason: Blocked by bash pattern: git push*'
    quoted = "POLICY=BLOCKED (Blocked by bash pattern: git push*)"
    (blocked,) = watch.events(
        line("toolResult", [{"type": "text", "text": refusal}]), 200
    )
    (plain,) = watch.events(line("toolResult", [{"type": "text", "text": quoted}]), 200)
    assert "BLOCKED" in blocked
    assert "BLOCKED" not in plain.split("POLICY=")[0]


def test_lines_that_are_not_tool_events_or_not_json_produce_nothing():
    assert list(watch.events("not json", 200)) == []
    assert (
        list(watch.events(line("assistant", [{"type": "text", "text": "hi"}]), 200))
        == []
    )


def test_mcp_tools_get_their_own_colour():
    call = [
        {
            "type": "toolCall",
            "name": "mcp__deepseek_agent",
            "arguments": {"prompt": "x"},
        }
    ]
    (text,) = watch.events(line("assistant", call), 200)
    assert watch.fg(watch.MCP_COLOR) in text


def test_detail_adds_reasoning_what_the_agent_says_and_what_it_is_told_and_nothing_else_does():
    thinking = line(
        "assistant", [{"type": "thinking", "thinking": "weigh the two options"}]
    )
    said = line("assistant", [{"type": "text", "text": "I will run the tests now"}])
    told = line("user", [{"type": "text", "text": "please review packet 5"}])
    for entry, marker in ((thinking, "think"), (said, "say"), (told, "in")):
        assert list(watch.events(entry, 200)) == []
        (text,) = watch.events(entry, 200, detail=True)
        assert marker in text


def test_a_single_character_reply_is_not_shown_as_something_said():
    dot = line("assistant", [{"type": "text", "text": "."}])
    assert list(watch.events(dot, 200, detail=True)) == []


def result(text: str) -> str:
    (out,) = watch.events(
        line("toolResult", [{"type": "text", "text": text}]), 200, lines=5
    )
    return out


def test_a_multi_line_result_is_shown_as_lines_not_joined_with_a_marker():
    out = result("first\nsecond\nthird")
    assert "⏎" not in out
    assert out.count("\n") == 2


def test_lines_beyond_the_cap_are_counted_not_dropped_silently():
    out = result("\n".join(f"row {n}" for n in range(12)))
    assert "row 4" in out and "row 5" not in out
    assert "+7 more lines" in out


def test_a_compact_json_result_is_shown_indented():
    out = result('{"success":true,"data":{"task":{"id":"abc"}}}')
    assert '  "success": true' in out
    assert out.count("\n") >= 4


def test_a_long_line_is_cut_at_the_width_but_the_other_lines_stay():
    (out,) = watch.events(
        line("toolResult", [{"type": "text", "text": "x" * 500 + "\nsecond"}]), 50
    )
    assert "x" * 50 in out and "x" * 51 not in out and "second" in out


GRID = "w25"


def fake_herdr(monkeypatch, panes):
    """Replace the herdr call: record every command and answer list/split like the real one."""
    calls = []

    def run(*args):
        calls.append(args)
        if args[:2] == ("workspace", "list"):
            return {"workspaces": [{"workspace_id": GRID, "label": "r grid"}]}
        if args[:2] == ("pane", "list"):
            return {"panes": panes}
        if args[:2] == ("pane", "split"):
            return {"pane": {"pane_id": f"{GRID}:new{len(calls)}"}}
        if args[:2] == ("workspace", "create"):
            return {"root_pane": {"pane_id": f"{GRID}:p1"}}
        return {}

    monkeypatch.setattr(watch, "herdr", run)
    monkeypatch.setattr(watch, "rig_seats", lambda rig: ["lead", "go-dev"])
    return calls


def pane(label):
    return {"pane_id": f"{GRID}:{label}", "workspace_id": GRID, "label": label}


def test_inputs_open_splits_a_small_input_pane_under_each_seat_pane(monkeypatch):
    calls = fake_herdr(monkeypatch, [pane("lead"), pane("go-dev")])
    watch.inputs(type("A", (), {"rig": "r", "action": "open", "seat": None}))
    assert sum(c[:2] == ("pane", "split") for c in calls) == 2
    renamed = [c[3] for c in calls if c[:2] == ("pane", "rename")]
    assert renamed == ["lead > input", "go-dev > input"]


def test_inputs_open_twice_adds_nothing_the_second_time(monkeypatch):
    calls = fake_herdr(
        monkeypatch,
        [pane("lead"), pane("lead > input"), pane("go-dev"), pane("go-dev > input")],
    )
    watch.inputs(type("A", (), {"rig": "r", "action": "open", "seat": None}))
    assert not any(c[:2] == ("pane", "split") for c in calls)


def test_inputs_hide_closes_only_the_input_panes(monkeypatch):
    calls = fake_herdr(monkeypatch, [pane("lead"), pane("lead > input")])
    watch.inputs(type("A", (), {"rig": "r", "action": "hide", "seat": None}))
    closed = [c[2] for c in calls if c[:2] == ("pane", "close")]
    assert closed == [f"{GRID}:lead > input"]


def test_a_relaunched_grid_replaces_the_old_one_and_keeps_the_same_layout(monkeypatch):
    calls = fake_herdr(monkeypatch, [])
    args = type(
        "A",
        (),
        {"rig": "r", "cols": 2, "back": 40, "width": 200, "lines": 60, "detail": True},
    )
    watch.grid(args)
    closed = [c[2] for c in calls if c[:2] == ("workspace", "close")]
    assert closed == [GRID]
    assert calls.index(("workspace", "close", GRID)) < next(
        i for i, c in enumerate(calls) if c[:2] == ("workspace", "create")
    )
    sent = [c[3] for c in calls if c[:2] == ("pane", "send-text")]
    assert all("--back 40 --width 200 --lines 60 --detail" in text for text in sent)


def test_a_typed_line_is_sent_to_its_seat_and_hide_leaves_the_loop(monkeypatch):
    typed = iter(["hello lead", "", "/hide", "never read"])
    sent = []
    monkeypatch.setattr("builtins.input", lambda prompt="": next(typed))
    monkeypatch.setattr(
        watch.subprocess,
        "run",
        lambda cmd, **kw: sent.append(cmd)
        or type("R", (), {"returncode": 0, "stderr": ""})(),
    )
    watch.input_loop(type("A", (), {"rig": "r", "seat": "lead"}))
    assert sent == [["rig", "send", "r-lead@r", "hello lead"]]


def test_inputs_open_for_one_seat_leaves_the_others_without_one(monkeypatch):
    calls = fake_herdr(monkeypatch, [pane("lead"), pane("go-dev")])
    watch.inputs(type("A", (), {"rig": "r", "action": "open", "seat": ["lead"]}))
    renamed = [c[3] for c in calls if c[:2] == ("pane", "rename")]
    assert renamed == ["lead > input"]


def test_pretty_indents_json_followed_by_a_trailer():
    shown = watch.pretty('{"a":[1,{"b":2}]}\n\nWall time: 0.1 seconds')
    assert shown.startswith('{\n  "a": [\n    1,')
    assert shown.endswith("}\n\nWall time: 0.1 seconds")


def test_pretty_indents_json_cut_off_by_the_log():
    shown = watch.pretty('{"a":[1,{"b":"x, {y}"},{"k":"cut\n\n[Some lines truncated]')
    assert '"b":"x, {y}"' in shown
    assert shown.count("\n") > 6
    assert shown.endswith("[Some lines truncated]")


def test_pretty_leaves_prose_alone():
    assert watch.pretty('plain {"a":1}') == 'plain {"a":1}'


def test_watch_opens_the_grid_and_a_lead_window_with_its_input(monkeypatch):
    calls = fake_herdr(monkeypatch, [])
    watch.watch(
        type(
            "A",
            (),
            {
                "rig": "r",
                "cols": 2,
                "back": 40,
                "width": 200,
                "lines": 25,
                "detail": True,
            },
        )
    )
    labels = [c for c in calls if c[:3] == ("workspace", "create", "--cwd")]
    assert [c[4] for c in labels] == ["--label", "--label"] and labels[-1][
        5
    ] == "r lead"
    sent = [c[3] for c in calls if c[:2] == ("pane", "send-text")]
    assert any("feed --rig r --seat lead" in t and "--detail" in t for t in sent)
    assert any(t.endswith("input --rig r --seat lead") for t in sent)


def test_seats_are_the_live_tmux_sessions(monkeypatch):
    sessions = "r-lead@r\nr-architect@r\nother-x@other\n"
    monkeypatch.setattr(
        watch.subprocess,
        "run",
        lambda *a, **k: type("R", (), {"stdout": sessions})(),
    )
    assert watch.rig_seats("r") == ["architect", "lead"]
    assert "feed --rig r --seat architect --back 40" in watch.seat_command(
        "r", "architect", "--back 40"
    )


def test_a_claude_code_call_and_its_result_show_like_an_omp_one():
    call = json.dumps(
        {
            "message": {
                "role": "assistant",
                "content": [
                    {"type": "tool_use", "name": "Bash", "input": {"command": "ls"}}
                ],
            }
        }
    )
    result = json.dumps(
        {
            "message": {
                "role": "user",
                "content": [{"type": "tool_result", "content": "a.txt\nb.txt"}],
            }
        }
    )
    shown = list(watch.events(call, 80)) + list(watch.events(result, 80))
    assert "→ Bash" in shown[0] and "command=ls" in shown[0]
    assert "← " in shown[1] and "b.txt" in shown[1]


def test_context_tokens_reads_both_runtimes_and_ignores_records_without_usage():
    omp = json.dumps({"message": {"usage": {"totalTokens": 422248, "input": 705}}})
    claude = json.dumps(
        {
            "message": {
                "usage": {
                    "input_tokens": 2,
                    "cache_creation_input_tokens": 873,
                    "cache_read_input_tokens": 148956,
                    "output_tokens": 1273,
                }
            }
        }
    )
    assert watch.context_tokens(omp) == 422248
    assert watch.context_tokens(claude) == 151104
    assert watch.context_tokens(json.dumps({"message": {"role": "user"}})) is None


def test_the_token_bar_shows_percent_and_tokens_of_the_compaction_point():
    bar = watch.token_bar(425_000, 850_000)
    assert "50%" in bar and "425k/850k" in bar and bar.count("█") == 8
    assert "…" in watch.token_bar(None, 850_000)


def test_a_compaction_record_shows_as_a_line_and_resets_the_context_reading():
    line = json.dumps(
        {"type": "compaction", "tokensBefore": 234283, "tokensAfter": 42545}
    )
    assert watch.context_tokens(line) == 42545
    text = "".join(watch.events(line, 100))
    assert "COMPACTED" in text and "234k -> 43k" in text


def test_seat_model_is_the_last_model_in_the_log_tail(tmp_path):
    log = tmp_path / "s.jsonl"
    log.write_text(
        '{"message":{"model":"old"}}\n{"message":{"model":"claude-opus-5-5"}}\n'
    )
    assert watch.seat_model(log) == "claude-opus-5-5"
    assert watch.seat_model(None) == ""
