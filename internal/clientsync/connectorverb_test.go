// The candidate extraction, which is the part of the connector verb that decides WHICH session a verb
// addresses and what seat that session belongs to. It is proven here from the JSON alone: `rig ps
// --json` as the rig really reports it is pinned by internal/clientbridge/testdata/rig_ps.json, whose
// entries carry canonicalSessionName, rigName and logicalId and NO plain "name".
package clientsync

import "testing"

const rigPsJSON = `[
 {"rigId":"01M431AAP55S5KEAPB56QP0K2E","rigName":"room-b","logicalId":"room-b.supervisor","canonicalSessionName":"room-b-supervisor@room-b"},
 {"rigId":"01M3T2NY8X64C8HZX5DEJNXZ1F","rigName":"room-c","logicalId":"dev.owner","canonicalSessionName":"dev-owner@room-c"},
 {"rigId":"01M3T2NY8X64C8HZX5DEJNXZ1G","rigName":"room-d","logicalId":"nodot"}
]`

func TestRigSessionCandidatesCarryTheSeatIdentity(t *testing.T) {
	candidates, err := parseRigPsSessions(rigPsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d, want 3", len(candidates))
	}

	// CANONICAL FIRST, and that order is a fix rather than a preference: the real output has no plain
	// "name", so the previous key order fell through to logicalId and addressed the session by an
	// identifier the UI never shows.
	if candidates[0].name != "room-b-supervisor@room-b" {
		t.Errorf("name = %q, want the canonical session name", candidates[0].name)
	}
	if candidates[0].rigName != "room-b" || candidates[0].member != "supervisor" {
		t.Errorf("seat = %q/%q, want room-b/supervisor", candidates[0].rigName, candidates[0].member)
	}

	// THE RIG IS THE ROOM, NOT logicalId's FIRST SEGMENT: here the pod is "dev" and the room is
	// "room-c", which is the case that would fail if the room were read from the logicalId.
	if candidates[1].rigName != "room-c" || candidates[1].member != "owner" {
		t.Errorf("second seat = %q/%q, want room-c/owner", candidates[1].rigName, candidates[1].member)
	}

	// A logicalId carrying no dot names no member, so no seat is reported at all rather than one named
	// after its own pod.
	if candidates[2].member != "" {
		t.Errorf("member = %q for a logicalId with no dot, want empty", candidates[2].member)
	}

	// --session may name the entry by any identifier it answers to, and by nothing else.
	if !candidates[0].answersTo("room-b.supervisor") || !candidates[0].answersTo("room-b-supervisor@room-b") {
		t.Errorf("answersTo did not match the entry's own identifiers")
	}
	if candidates[0].answersTo("room-b-owner@room-b") {
		t.Errorf("answersTo matched another session's name")
	}

	// A shape the rig does not produce today still names its sessions rather than being dropped.
	if names := candidateNames(candidates[2:]); len(names) != 1 || names[0] != "nodot" {
		t.Errorf("candidateNames = %v, want [nodot]", names)
	}
}
