package clientservices

import (
	"path/filepath"
	"strings"
	"testing"
)

// RoomDir JOINS and never resolves (hub task 54a942c5): the verb that took --state-root passes the store
// it resolved, and a missing root is refused rather than turned into a path relative to the working
// directory - which is how a verb could derive worktrees under one store while reading another.
func TestRoomDirJoinsTheGivenRootAndRefusesAnEmptyOne(t *testing.T) {
	dir, err := RoomDir("/tmp/x", "demo")
	if err != nil {
		t.Fatalf("RoomDir(/tmp/x, demo): %v", err)
	}
	if want := filepath.Join("/tmp/x", "rooms", "demo"); dir != want {
		t.Fatalf("RoomDir = %q, want %q", dir, want)
	}
	if _, err := RoomDir("", "demo"); err == nil {
		t.Fatal("an empty root was accepted")
	} else if !strings.Contains(err.Error(), "state root") {
		t.Fatalf("the refusal does not name the state root: %v", err)
	}
	if _, err := RoomDir("/tmp/x", ""); err == nil {
		t.Fatal("an empty room name was accepted")
	}
}
