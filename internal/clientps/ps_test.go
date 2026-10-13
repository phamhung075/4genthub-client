package clientps

import (
	"os"
	"path/filepath"
	"testing"
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

func TestNodeRecordKeys(t *testing.T) {
	seats := collect(t.Context(), []session{{name: "r-a@r"}}, "r")
	if len(seats) != 1 || seats[0].LogicalID != "r.a" || seats[0].CanonicalName != "r-a@r" || seats[0].SessionStatus != "running" {
		t.Fatalf("got %+v", seats)
	}
}
