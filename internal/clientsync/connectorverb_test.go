// The candidate extraction, which is the part of the connector verb that decides WHICH session a verb
// addresses and what seat that session belongs to. It is proven from the RUNTIME'S OWN ANSWER: the fixture
// below is that document as the runtime serves it (`rust/agenthub-runtime/src/types.rs`, `SessionEntry`,
// whose serde renames are the wire spelling). It is decoded through the client's own type and then through
// the one candidate builder, so a rename on either side of the wire fails THIS test rather than silently
// emptying the connector's session list.
package clientsync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const runtimeSessionsJSON = `[
 {"canonicalSessionName":"room-b-supervisor@room-b","sessionName":"agenthub--room-b--supervisor","roomName":"room-b","logicalId":"room-b.supervisor","sessionStatus":"running","agentActivity":{"state":"idle","source":"idle"},"runtime":"omp","model":"deepseek/deepseek-flash","transcriptPath":"/tmp/room-b/logs/supervisor.jsonl"},
 {"canonicalSessionName":"dev-owner@room-c","sessionName":"agenthub--room-c--owner","roomName":"room-c","logicalId":"dev.owner","sessionStatus":"stopped","agentActivity":{"state":"unknown"},"runtime":"omp"},
 {"logicalId":"nodot","roomName":"room-d","sessionStatus":"running"},
 {"canonicalSessionName":"room-b-supervisor@room-b","sessionName":"agenthub--room-b--supervisor","roomName":"room-b","logicalId":"room-b.supervisor","sessionStatus":"running"}
]`

func TestRoomSessionCandidatesCarryTheSeatIdentity(t *testing.T) {
	var entries []clientruntime.SessionEntry
	if err := json.Unmarshal([]byte(runtimeSessionsJSON), &entries); err != nil {
		t.Fatalf("the runtime's own answer did not decode: %v", err)
	}
	if entries[0].AgentActivity.State != "idle" {
		t.Errorf("agentActivity.state = %q, want the word nested where the node table nests it", entries[0].AgentActivity.State)
	}

	candidates := candidatesFrom(entries)
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d, want 3: the entry naming a session already listed is dropped", len(candidates))
	}

	// CANONICAL FIRST, and that order is a fix rather than a preference: the canonical name is the one the
	// UI shows and the one a person passes to --session, so printing the logicalId instead addressed
	// sessions by an identifier nobody types.
	if candidates[0].name != "room-b-supervisor@room-b" {
		t.Errorf("name = %q, want the canonical session name", candidates[0].name)
	}
	if candidates[0].roomName != "room-b" || candidates[0].member != "supervisor" {
		t.Errorf("seat = %q/%q, want room-b/supervisor", candidates[0].roomName, candidates[0].member)
	}
	if candidates[0].state != SessionRunning {
		t.Errorf("state = %q, want the runtime's own running", candidates[0].state)
	}

	// THE ROOM IS THE ROOM, NOT logicalId's FIRST SEGMENT: here the pod is "dev" and the room is "room-c",
	// which is the case that would fail if the room were read from the logicalId.
	if candidates[1].roomName != "room-c" || candidates[1].member != "owner" {
		t.Errorf("second seat = %q/%q, want room-c/owner", candidates[1].roomName, candidates[1].member)
	}
	// A session the runtime reports as anything but running is STOPPED, never a running seat that may not
	// exist; the Sessions page distinguishes a reported stop from no report at all.
	if got := candidateState(candidates[1]); got != SessionStopped {
		t.Errorf("state = %q for a stopped session, want stopped", got)
	}

	// A logicalId carrying no dot names no member, so no seat is reported at all rather than one named after
	// its own pod - and the entry still names its session, so it is not dropped.
	if candidates[2].member != "" {
		t.Errorf("member = %q for a logicalId with no dot, want empty", candidates[2].member)
	}
	if candidates[2].name != "nodot" {
		t.Errorf("candidate name = %q, want nodot", candidates[2].name)
	}

	// --session may name the entry by any identifier it answers to, and by nothing else.
	if !candidates[0].answersTo("room-b.supervisor") || !candidates[0].answersTo("room-b-supervisor@room-b") {
		t.Errorf("answersTo did not match the entry's own identifiers")
	}
	if !candidates[0].answersTo("agenthub--room-b--supervisor") {
		t.Errorf("answersTo did not match the runtime's own session name")
	}
	if candidates[0].answersTo("room-b-owner@room-b") {
		t.Errorf("answersTo matched another session's name")
	}
}

// THE SESSION IS REQUIRED AND THE PROOF IS THE EMPTY PATH. This verb used to take the FIRST session `rig ps --json`
// reported; with no `rig` anywhere that fallback cannot produce the contract's own words, so the old behaviour fails
// here and the new refusal passes without running anything - before any socket is opened.
func TestConnectorVerbRequiresASessionWithoutShellingOut(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTHUB_CONNECTOR_TOKEN", "token")
	t.Setenv("PATH", t.TempDir())

	var stdout, stderr strings.Builder
	code := RunConnectorVerb(context.Background(), nil, &stdout, &stderr)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit = %d, want ExitUsage: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--session <name>") {
		t.Errorf("stderr = %q, want it to name --session <name> and what it takes", stderr.String())
	}
}
