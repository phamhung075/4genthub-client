package clientps

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func TestSplitSession(t *testing.T) {
	rig, member, ok := splitSession("4genthub-min-go-dev@4genthub-min")
	if !ok || rig != "4genthub-min" || member != "go-dev" {
		t.Fatalf("got %q %q %v", rig, member, ok)
	}
	if _, _, ok := splitSession("plain-session"); ok {
		t.Fatal("a session without @rig is not a seat")
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
	seats := collect(t.Context(), []session{{name: "r-a@r"}}, "r")
	if len(seats) != 1 || seats[0].LogicalID != "r.a" || seats[0].CanonicalName != "r-a@r" || seats[0].SessionStatus != "running" {
		t.Fatalf("got %+v", seats)
	}
}
