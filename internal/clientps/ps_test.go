package clientps

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func TestCollectReadsANativeRoom(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "rooms", "R", "seats", "S", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(sessions, "x.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seats := collect(root, []session{{name: "agenthub--R--S"}, {name: "plain-session"}, {name: "R-S@R"}, {name: "agenthub--R"}}, "")
	if len(seats) != 1 || seats[0].Rig != "R" || seats[0].Member != "S" || seats[0].Runtime != "omp" || seats[0].LogPath != log {
		t.Fatalf("got %+v", seats)
	}
}

func TestQuotaIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.log")
	if err := os.WriteFile(path, []byte("work\n⚠ Individual quota reached. Please upgrade\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := quotaIn(path); got != "Individual quota reached" {
		t.Fatalf("got %q", got)
	}
	if got := quotaIn(""); got != "" {
		t.Fatalf("no transcript must not report quota, got %q", got)
	}
}

func TestRollupCountsAttention(t *testing.T) {
	rows := rollup([]Seat{{Rig: "a", Status: "running"}, {Rig: "a", Status: "quota"}})
	if len(rows) != 1 || rows[0].Seats != 2 || rows[0].Attn != 1 || rows[0].Status != "attention" {
		t.Fatalf("got %+v", rows)
	}
}

// A documented list is never `null`. A room this verb cannot see - one started under its own state dir, on its
// own tmux server - used to come back as `null`, which reads as a bug rather than as an empty answer.
func TestAnEmptyInventoryIsAnEmptyListAndNotNull(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := writeJSON(&stdout, &stderr, "nodes", nil); code != clientcmd.ExitOK {
		t.Fatalf("writeJSON = %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "[]" {
		t.Errorf("an empty inventory printed %q, want []", got)
	}
}

func TestNodeRecordKeys(t *testing.T) {
	seats := collect("", []session{{name: "agenthub--r--a"}}, "r")
	if len(seats) != 1 || seats[0].LogicalID != "r.a" || seats[0].CanonicalName != "agenthub--r--a" || seats[0].SessionStatus != "running" {
		t.Fatalf("got %+v", seats)
	}
}

// An empty list and a verb that cannot see the room read identically on stdout, so a NAMED room that
// matched nothing has to say so on stderr - that is the difference F-A was reported over, a live room
// whose inventory came back empty with nothing to distinguish it from a blind verb. The silence cases are
// pinned for the same reason: the reason belongs to the one case where the reader cannot tell, and a
// second message on top of Run's own would be noise.
func TestANamedRoomThatMatchedNothingSaysSo(t *testing.T) {
	for _, tc := range []struct {
		what            string
		rig             string
		sessions, seats int
		wantNamed       string
	}{
		{what: "a named room with no live seats", rig: "leg-solo", sessions: 3, wantNamed: "leg-solo"},
		{what: "no room named", sessions: 3},
		{what: "no sessions at all", rig: "leg-solo"},
		{what: "the named room's seats are right there", rig: "leg-solo", sessions: 1, seats: 1},
	} {
		t.Run(tc.what, func(t *testing.T) {
			reason := emptyReason(tc.rig, tc.sessions, tc.seats)
			if tc.wantNamed == "" {
				if reason != "" {
					t.Fatalf("got %q, want silence", reason)
				}
				return
			}
			if !strings.Contains(reason, tc.wantNamed) {
				t.Fatalf("reason %q does not name the room asked for, %q", reason, tc.wantNamed)
			}
		})
	}
}
