package clientwatch

import (
	"context"
	"reflect"
	"testing"
)

// The grid and lead panes run `feed` and `input`, which poll forever: closing the room's two workspaces is
// what ends them, and another room's workspaces are not touched.
func TestCloseRoomWorkspacesClosesTheGridAndLeadOfThatRoomOnly(t *testing.T) {
	fake := newFakeExec(t)
	fake.working["herdr"] = true
	fake.on("herdr workspace list", `{"result": {"workspaces": [`+
		`{"workspace_id": "w1", "label": "r grid"}, {"workspace_id": "w2", "label": "r lead"},`+
		`{"workspace_id": "w3", "label": "other grid"}]}}`)
	fake.on("herdr workspace close w1", `{"result": {}}`)
	fake.on("herdr workspace close w2", `{"result": {}}`)

	if err := CloseRoomWorkspaces(context.Background(), "r"); err != nil {
		t.Fatal(err)
	}
	closed := [][]string{}
	for _, call := range fake.callsOf("herdr") {
		if call[1] == "workspace" && call[2] == "close" {
			closed = append(closed, call[3:])
		}
	}
	if want := [][]string{{"w1"}, {"w2"}}; !reflect.DeepEqual(closed, want) {
		t.Errorf("closed %v, want %v", closed, want)
	}
}

func TestCloseRoomWorkspacesWithoutHerdrDoesNothing(t *testing.T) {
	fake := newFakeExec(t)
	if err := CloseRoomWorkspaces(context.Background(), "r"); err != nil {
		t.Fatal(err)
	}
	if calls := fake.callsOf("herdr"); len(calls) != 0 {
		t.Errorf("herdr was called without being installed: %v", calls)
	}
}
