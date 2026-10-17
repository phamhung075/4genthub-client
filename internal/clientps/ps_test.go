package clientps

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
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

// listSessions reaches the tmux server the runtime owns whatever the caller's environment names: a tmux that
// answers only for the runtime's TMUX_TMPDIR stands in for the two servers.
func TestListSessionsReadsTheRuntimesServerWhateverTheEnvironmentNames(t *testing.T) {
	want := clientenv.TmuxRoot(clientenv.DefaultStateRoot())
	bin := t.TempDir()
	script := "#!/bin/sh\n[ \"$TMUX_TMPDIR\" = \"" + want + "\" ] && [ -z \"$TMUX\" ] || { echo 'no server running on /tmp/x' >&2; exit 1; }\nprintf 'agenthub--r--lead\\t1\\t0\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TMPDIR", "/not/the/runtime")
	t.Setenv("TMUX", "/tmp/default,1,0")

	sessions, err := listSessions(context.Background(), clientenv.DefaultStateRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].name != "agenthub--r--lead" {
		t.Fatalf("sessions = %+v, want the runtime's one seat", sessions)
	}
}

// TestListSessionsReadsAnAbsentServerAsNoSessions: a root where no tmux server ever started is a fact - no
// sessions - and not a failed read. tmux spells it "error connecting to <socket> (No such file or
// directory)" when the socket directory is missing, which is every freshly created state root; the same
// reading clientrestore takes for the same command is the one pinned here.
func TestListSessionsReadsAnAbsentServerAsNoSessions(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'error connecting to /tmp/none/tmux-1000/default (No such file or directory)' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	sessions, err := listSessions(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("an absent server is not a failed read: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %+v, want none", sessions)
	}
}
