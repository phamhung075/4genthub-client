//! The Claude Code dialect of the runner's agent channel.
//!
//! The runner speaks one protocol to its agent: omp's newline-JSON RPC (`prompt`, `abort`, `compact`,
//! `get_state` in; `ready`, `response`, `turn_start`, `turn_end`, `message_update` out). Claude Code has no
//! such mode, but `claude -p --input-format stream-json --output-format stream-json` is a long-lived
//! newline-JSON channel of its own. This module is the translation between the two, so the runner's
//! handshake, retry ladder, state file and mailbox stay the single implementation for every runtime.
//!
//! Measured against `claude` 2.1.300 (`--verbose --include-partial-messages`):
//!
//! * nothing is printed until the first turn: `system/init` arrives with the first user message, so there
//!   is no startup frame to wait for. `ready` is therefore synthesized when the process is spawned, and
//!   `get_state` is answered locally. That proves the pipe is open, not that the model is reachable; the
//!   first turn proves the rest.
//! * a user turn is `{"type":"user","message":{"role":"user","content":<text>}}`; its end is one
//!   `{"type":"result"}` frame, `subtype:"success"` or an error subtype with `is_error`.
//! * `/compact` sent as a user message compacts the session and ends with a `result` of its own.
//! * an interrupt is `{"type":"control_request","request_id":..,"request":{"subtype":"interrupt"}}` and
//!   ends the running turn with `result/error_during_execution`, which is the abort working, not a failure.

use parking_lot::Mutex;
use serde_json::{json, Value};
use std::collections::VecDeque;

/// What one runner frame becomes: lines for the agent's stdin, and frames the runner's own reader should
/// see (answers that only this side can give, because Claude Code has no such request).
#[derive(Debug, Default, PartialEq)]
pub struct Outbound {
    pub to_agent: Vec<Value>,
    pub to_reader: Vec<Value>,
}

#[derive(Default)]
struct Turns {
    /// Ids of the requests whose `result` is still owed, oldest first. Claude Code answers turns in order.
    awaiting: VecDeque<String>,
    turn_open: bool,
    interrupt_sent: bool,
}

#[derive(Default)]
pub struct Bridge {
    turns: Mutex<Turns>,
}

impl Bridge {
    /// The runner's frame, in the agent's dialect.
    pub fn from_runner(&self, frame: &Value) -> Outbound {
        let id = frame.get("id").and_then(|v| v.as_str()).unwrap_or("?");
        let mut out = Outbound::default();
        match frame.get("type").and_then(|v| v.as_str()) {
            Some("get_state") => out.to_reader.push(json!({"type": "response", "id": id, "success": true, "data": {}})),
            Some("prompt") => {
                let text = frame.get("message").and_then(|v| v.as_str()).unwrap_or_default();
                self.turns.lock().awaiting.push_back(id.to_string());
                out.to_agent.push(user_message(text));
            }
            Some("compact") => {
                self.turns.lock().awaiting.push_back(id.to_string());
                out.to_agent.push(user_message("/compact"));
            }
            Some("abort") => {
                let mut turns = self.turns.lock();
                if turns.turn_open {
                    turns.interrupt_sent = true;
                    out.to_agent.push(json!({
                        "type": "control_request",
                        "request_id": id,
                        "request": {"subtype": "interrupt"}
                    }));
                }
            }
            // A dialog answer: Claude Code asks no dialogs on this channel, so there is nothing to answer.
            Some("extension_ui_response") => {}
            other => out.to_reader.push(json!({
                "type": "response",
                "id": id,
                "success": false,
                "error": format!("the claude-code channel has no {} request", other.unwrap_or("untyped"))
            })),
        }
        out
    }

    /// One frame from the agent, as the runner's reader expects it. Frames the runner has no use for
    /// (hooks, status, rate-limit notices, tool traffic) produce nothing.
    pub fn from_agent(&self, record: &Value) -> Vec<Value> {
        let mut frames = Vec::new();
        let mut turns = self.turns.lock();
        match record.get("type").and_then(|v| v.as_str()) {
            Some("stream_event") | Some("assistant") => {
                if !turns.turn_open {
                    turns.turn_open = true;
                    frames.push(json!({"type": "turn_start"}));
                }
                let delta = record
                    .get("event")
                    .filter(|e| e.get("type").and_then(|t| t.as_str()) == Some("content_block_delta"))
                    .and_then(|e| e.get("delta"))
                    .filter(|d| d.get("type").and_then(|t| t.as_str()) == Some("text_delta"))
                    .and_then(|d| d.get("text"))
                    .and_then(|t| t.as_str());
                if let Some(text) = delta {
                    frames.push(json!({"type": "message_update", "assistantMessageEvent": {"delta": text}}));
                }
            }
            Some("result") => {
                turns.turn_open = false;
                let interrupted = std::mem::take(&mut turns.interrupt_sent);
                frames.push(json!({"type": "turn_end"}));
                frames.push(json!({"type": "agent_end"}));
                if let Some(id) = turns.awaiting.pop_front() {
                    frames.push(response_for(record, &id, interrupted));
                }
            }
            _ => {}
        }
        frames
    }
}

fn user_message(text: &str) -> Value {
    json!({"type": "user", "message": {"role": "user", "content": text}})
}

/// The `response` the runner's reader matches to a pending prompt. An interrupted turn answers success: the
/// operator asked for it to stop, and a failure would start the retry ladder on a turn that was cancelled.
fn response_for(result: &Value, id: &str, interrupted: bool) -> Value {
    let subtype = result.get("subtype").and_then(|v| v.as_str()).unwrap_or("");
    let is_error = result.get("is_error").and_then(|v| v.as_bool()).unwrap_or(false);
    if interrupted && subtype == "error_during_execution" {
        return json!({"type": "response", "id": id, "success": true});
    }
    if is_error || subtype != "success" {
        let text = result
            .get("result")
            .and_then(|v| v.as_str())
            .filter(|s| !s.is_empty())
            .map(str::to_string)
            .or_else(|| result.get("errors").map(|e| e.to_string()))
            .unwrap_or_else(|| format!("the turn ended with {subtype}"));
        return json!({"type": "response", "id": id, "success": false, "error": text});
    }
    json!({"type": "response", "id": id, "success": true})
}

#[cfg(test)]
mod tests {
    use super::*;

    fn prompt(id: &str, text: &str) -> Value {
        json!({"id": id, "type": "prompt", "message": text})
    }

    #[test]
    fn a_prompt_becomes_one_user_message_and_is_owed_a_result() {
        let bridge = Bridge::default();
        let out = bridge.from_runner(&prompt("p1", "hello"));
        assert_eq!(out.to_agent, vec![json!({"type": "user", "message": {"role": "user", "content": "hello"}})]);
        assert!(out.to_reader.is_empty());
    }

    #[test]
    fn get_state_is_answered_locally_because_claude_has_no_such_request() {
        let out = Bridge::default().from_runner(&json!({"id": "runner-get-state", "type": "get_state"}));
        assert!(out.to_agent.is_empty());
        assert_eq!(out.to_reader[0]["id"], "runner-get-state");
        assert_eq!(out.to_reader[0]["success"], true);
    }

    #[test]
    fn compact_is_the_slash_command_and_its_result_answers_the_compact_id() {
        let bridge = Bridge::default();
        let out = bridge.from_runner(&json!({"id": "c1", "type": "compact"}));
        assert_eq!(out.to_agent[0]["message"]["content"], "/compact");
        let frames = bridge.from_agent(&json!({"type": "result", "subtype": "success", "is_error": false}));
        assert_eq!(frames.last().unwrap(), &json!({"type": "response", "id": "c1", "success": true}));
    }

    #[test]
    fn a_turn_streams_start_text_end_and_a_matching_response() {
        let bridge = Bridge::default();
        bridge.from_runner(&prompt("p1", "x"));
        let delta = json!({"type": "stream_event", "event": {"type": "content_block_delta",
            "delta": {"type": "text_delta", "text": "PONG"}}});
        let first = bridge.from_agent(&delta);
        assert_eq!(first[0]["type"], "turn_start");
        assert_eq!(first[1]["assistantMessageEvent"]["delta"], "PONG");
        // The turn is already open: no second turn_start.
        assert_eq!(bridge.from_agent(&delta).len(), 1);
        let end = bridge.from_agent(&json!({"type": "result", "subtype": "success", "is_error": false}));
        let kinds: Vec<_> = end.iter().map(|f| f["type"].as_str().unwrap()).collect();
        assert_eq!(kinds, ["turn_end", "agent_end", "response"]);
        assert_eq!(end[2]["id"], "p1");
        assert_eq!(end[2]["success"], true);
    }

    #[test]
    fn an_error_result_is_a_failed_response_carrying_the_agents_own_words() {
        let bridge = Bridge::default();
        bridge.from_runner(&prompt("p1", "x"));
        let end = bridge.from_agent(&json!({"type": "result", "subtype": "error_during_execution",
            "is_error": true, "result": "API Error: 529 overloaded"}));
        assert_eq!(end[2]["success"], false);
        assert_eq!(end[2]["error"], "API Error: 529 overloaded");
    }

    #[test]
    fn an_interrupted_turn_answers_success_so_a_cancel_never_starts_the_retry_ladder() {
        let bridge = Bridge::default();
        bridge.from_runner(&prompt("p1", "x"));
        bridge.from_agent(&json!({"type": "assistant", "message": {}}));
        let abort = bridge.from_runner(&json!({"id": "a1", "type": "abort"}));
        assert_eq!(abort.to_agent[0]["request"]["subtype"], "interrupt");
        let end = bridge.from_agent(&json!({"type": "result", "subtype": "error_during_execution", "is_error": true}));
        assert_eq!(end[2]["success"], true);
    }

    #[test]
    fn an_abort_with_no_turn_running_sends_nothing() {
        let out = Bridge::default().from_runner(&json!({"id": "a1", "type": "abort"}));
        assert_eq!(out, Outbound::default());
    }

    #[test]
    fn a_request_the_channel_cannot_serve_is_refused_by_name() {
        let out = Bridge::default().from_runner(&json!({"id": "x", "type": "set_model"}));
        assert_eq!(out.to_reader[0]["success"], false);
        assert!(out.to_reader[0]["error"].as_str().unwrap().contains("set_model"));
    }

    #[test]
    fn frames_the_runner_has_no_use_for_produce_nothing() {
        let bridge = Bridge::default();
        for t in ["system", "rate_limit_event", "control_response", "user"] {
            assert!(bridge.from_agent(&json!({"type": t})).is_empty(), "{t}");
        }
    }
}
