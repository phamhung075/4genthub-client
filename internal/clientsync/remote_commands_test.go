package clientsync

import (
	"context"
	"errors"
	"testing"
)

func TestRemoteCommandsRefusalGuardrails(t *testing.T) {
	// Case 1: Disabled by default
	sock := &fakeSocket{}
	daemon := NewRigDaemon(RigDaemonOptions{})
	daemon.sock = sock

	cmdFrame := map[string]any{
		"type":        "command",
		"command_id":  "cmd-1",
		"issuer_kind": "human",
		"token_scope": "sessions:command",
		"action":      "send_to_seat",
		"room":        "4genthub-min",
		"seat":        "lead",
		"args":        map[string]any{"text": "hello"},
	}

	err := daemon.handle(cmdFrame)
	if err != nil {
		t.Fatalf("unexpected handle error: %v", err)
	}
	if len(sock.written) != 1 {
		t.Fatalf("expected 1 response frame, got %d", len(sock.written))
	}
	res := sock.written[0]
	if res["success"] != false || res["refusal_code"] != "REMOTE_COMMANDS_DISABLED" {
		t.Errorf("expected REMOTE_COMMANDS_DISABLED, got: %+v", res)
	}

	// Case 2: Non-human issuer refused
	sock.written = nil
	daemon.opts.AllowRemoteCommands = true
	nonHumanFrame := map[string]any{
		"type":        "command",
		"command_id":  "cmd-2",
		"issuer_kind": "agent",
		"token_scope": "sessions:command",
		"action":      "send_to_seat",
		"room":        "4genthub-min",
		"seat":        "lead",
	}
	_ = daemon.handle(nonHumanFrame)
	if len(sock.written) != 1 || sock.written[0]["refusal_code"] != "UNAUTHORIZED_ISSUER" {
		t.Errorf("expected UNAUTHORIZED_ISSUER, got: %+v", sock.written[0])
	}

	// Case 3: Missing token scope refused
	sock.written = nil
	badScopeFrame := map[string]any{
		"type":        "command",
		"command_id":  "cmd-3",
		"issuer_kind": "human",
		"token_scope": "sessions:read",
		"action":      "send_to_seat",
		"room":        "4genthub-min",
		"seat":        "lead",
	}
	_ = daemon.handle(badScopeFrame)
	if len(sock.written) != 1 || sock.written[0]["refusal_code"] != "SCOPE_INSUFFICIENT" {
		t.Errorf("expected SCOPE_INSUFFICIENT, got: %+v", sock.written[0])
	}

	// Case 4: Disallowed command action refused
	sock.written = nil
	badActionFrame := map[string]any{
		"type":        "command",
		"command_id":  "cmd-4",
		"issuer_kind": "human",
		"token_scope": "sessions:command",
		"action":      "rm_rf",
		"room":        "4genthub-min",
	}
	_ = daemon.handle(badActionFrame)
	if len(sock.written) != 1 || sock.written[0]["refusal_code"] != "DISALLOWED_COMMAND" {
		t.Errorf("expected DISALLOWED_COMMAND, got: %+v", sock.written[0])
	}

	// Case 5: Room not in whitelist refused
	sock.written = nil
	daemon.opts.AllowedRooms = []string{"prod-room"}
	badRoomFrame := map[string]any{
		"type":        "command",
		"command_id":  "cmd-5",
		"issuer_kind": "human",
		"token_scope": "sessions:command",
		"action":      "up",
		"room":        "other-room",
	}
	_ = daemon.handle(badRoomFrame)
	if len(sock.written) != 1 || sock.written[0]["refusal_code"] != "UNRECOGNIZED_ROOM" {
		t.Errorf("expected UNRECOGNIZED_ROOM, got: %+v", sock.written[0])
	}
}

func TestRemoteCommandsExecutionSuccess(t *testing.T) {
	sock := &fakeSocket{}
	daemon := NewRigDaemon(RigDaemonOptions{
		AllowRemoteCommands: true,
		AllowedRooms:        []string{"4genthub-min"},
	})
	daemon.sock = sock

	executed := ""
	daemon.opts.OnCommand = func(ctx context.Context, action, room, seat string, args map[string]any) (string, error) {
		executed = action + ":" + room + "/" + seat
		return "ok", nil
	}

	validFrame := map[string]any{
		"type":        "command",
		"command_id":  "cmd-ok",
		"issuer_kind": "human",
		"token_scope": "sessions:command",
		"action":      "send_to_seat",
		"room":        "4genthub-min",
		"seat":        "lead",
		"args":        map[string]any{"text": "wake up"},
	}

	err := daemon.handle(validFrame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if executed != "send_to_seat:4genthub-min/lead" {
		t.Fatalf("expected executed send_to_seat:4genthub-min/lead, got %s", executed)
	}

	if len(sock.written) != 1 {
		t.Fatalf("expected 1 written frame, got %d", len(sock.written))
	}
	res := sock.written[0]
	if res["success"] != true || res["output"] != "ok" || res["command_id"] != "cmd-ok" {
		t.Errorf("expected successful response, got: %+v", res)
	}
}

func TestRemoteCommandsExecutionFailureReportsError(t *testing.T) {
	sock := &fakeSocket{}
	daemon := NewRigDaemon(RigDaemonOptions{
		AllowRemoteCommands: true,
		AllowedRooms:        []string{"4genthub-min"},
	})
	daemon.sock = sock

	daemon.opts.OnCommand = func(ctx context.Context, action, room, seat string, args map[string]any) (string, error) {
		return "", errors.New("seat process unavailable")
	}

	validFrame := map[string]any{
		"type":        "command",
		"command_id":  "cmd-fail",
		"issuer_kind": "human",
		"token_scope": "sessions:command",
		"action":      "send_to_seat",
		"room":        "4genthub-min",
		"seat":        "lead",
	}

	_ = daemon.handle(validFrame)
	if len(sock.written) != 1 {
		t.Fatalf("expected 1 response frame, got %d", len(sock.written))
	}
	res := sock.written[0]
	if res["success"] != false || res["message"] != "seat process unavailable" {
		t.Errorf("expected failure message, got: %+v", res)
	}
}
