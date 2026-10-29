package seatwait

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

var t0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func dest(seat string) string { return clientruntime.SeatSessionName("demo", seat) }

func testDeps(rows []clientruntime.QueueRow) deps {
	return deps{
		now:     func() time.Time { return t0 },
		getenv:  func(string) string { return "" },
		getwd:   func() (string, error) { return "/repo", nil },
		rows:    func(string) ([]clientruntime.QueueRow, error) { return rows, nil },
		repo:    func(string) string { return "/repo" },
		sha:     func(_, target string) (string, error) { return "f85361d0000000000000000000000000000000000", nil },
		peakEnd: func(time.Time) (time.Time, bool) { return t0.Add(90 * time.Minute), true },
	}
}

func runWait(t *testing.T, d deps, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(d, append([]string{"--room", "demo", "--seat", "rust-dev", "--state-root", root}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestParseOnRefusesAWaitOnNothing(t *testing.T) {
	for _, on := range []string{"merge", "merge:", "review: ", "nap:later", "peak:noon", ""} {
		if _, _, err := ParseOn(on); err == nil {
			t.Errorf("ParseOn(%q) accepted", on)
		}
	}
	if kind, target, err := ParseOn("merge:step6@f85361d"); err != nil || kind != "merge" || target != "step6@f85361d" {
		t.Errorf("ParseOn = %q %q %v", kind, target, err)
	}
}

func TestWaitStartsWritesTheRecordAndLogsIt(t *testing.T) {
	root := t.TempDir()
	rows := []clientruntime.QueueRow{{QitemID: "q-own", State: "in-progress", DestinationSession: dest("rust-dev")}}
	code, out, errs := runWait(t, testDeps(rows), root, "--on", "merge:step6@f85361d", "--note", "fit math")
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, errs)
	}
	if !strings.Contains(out, "waiting on merge:step6@f85361d until 13:00Z (max 1h00m)") {
		t.Errorf("out = %q", out)
	}
	rec, ok := Live(root, "demo", "rust-dev", t0.Add(time.Minute))
	if !ok || rec.SHA == "" || rec.Repo != "/repo" || !rec.Snapshotted || !rec.Covers("q-own") {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
	log, _ := os.ReadFile(LogPath(root, "demo"))
	if !strings.Contains(string(log), `"event":"start"`) || !strings.Contains(string(log), `"max_s":3600`) {
		t.Errorf("log = %s", log)
	}
}

func TestWaitWithoutATargetIsRefused(t *testing.T) {
	root := t.TempDir()
	code, _, errs := runWait(t, testDeps(nil), root, "--on", "review")
	if code != 2 || !strings.Contains(errs, "waiting on nothing is idle") {
		t.Errorf("code %d stderr %q", code, errs)
	}
	if _, ok := Read(root, "demo", "rust-dev"); ok {
		t.Error("a refused wait wrote a record")
	}
}

func TestWaitIsRefusedWhileClaimableWorkIsAddressedToTheSeat(t *testing.T) {
	root := t.TempDir()
	rows := []clientruntime.QueueRow{{QitemID: "q-next", State: "pending", DestinationSession: dest("rust-dev")}}
	code, _, errs := runWait(t, testDeps(rows), root, "--on", "review:q-mine")
	if code != 5 || !strings.Contains(errs, "q-next") {
		t.Errorf("code %d stderr %q", code, errs)
	}
	// The awaited row itself being pending is not claimable work.
	rows[0].QitemID = "q-mine"
	if code, _, errs = runWait(t, testDeps(rows), root, "--on", "review:q-mine"); code != 0 {
		t.Errorf("waiting on its own pending target refused: %d %s", code, errs)
	}
}

func TestMaxIsCappedAndPeakDefaultsToTheWindowEnd(t *testing.T) {
	root := t.TempDir()
	if code, _, errs := runWait(t, testDeps(nil), root, "--on", "seat:reviewer", "--max", "9h"); code != 0 {
		t.Fatal(errs)
	}
	rec, _ := Read(root, "demo", "rust-dev")
	if rec.Max() != MaxCap {
		t.Errorf("max = %s, want the 4h cap", rec.Max())
	}
	if code, _, errs := runWait(t, testDeps(nil), root, "--on", "peak:offpeak"); code != 0 {
		t.Fatal(errs)
	}
	rec, _ = Read(root, "demo", "rust-dev")
	if rec.Max() != 91*time.Minute {
		t.Errorf("peak wait max = %s, want the window end plus a minute", rec.Max())
	}
	log, _ := os.ReadFile(LogPath(root, "demo"))
	if !strings.Contains(string(log), `"how":"replaced"`) {
		t.Errorf("the replaced wait was not logged: %s", log)
	}
	d := testDeps(nil)
	d.peakEnd = func(time.Time) (time.Time, bool) { return time.Time{}, false }
	if code, _, _ := runWait(t, d, root, "--on", "peak:offpeak"); code != 5 {
		t.Errorf("a peak wait off-peak answered %d, want refused", code)
	}
}

func TestClearEndsTheWait(t *testing.T) {
	root := t.TempDir()
	runWait(t, testDeps(nil), root, "--on", "slot:cargo")
	if code, out, _ := runWait(t, testDeps(nil), root, "--clear"); code != 0 || !strings.Contains(out, "cleared") {
		t.Errorf("clear: %d %q", code, out)
	}
	if _, ok := Read(root, "demo", "rust-dev"); ok {
		t.Error("record left after --clear")
	}
}

func waiting(kind, target string) Record {
	return Record{Room: "demo", Seat: "rust-dev", Kind: kind, Target: target, Started: t0, Deadline: t0.Add(time.Hour)}
}

func TestEvaluateExpiresAtMaxWithOneWakeLine(t *testing.T) {
	r := waiting("merge", "step6@f85361d")
	out := Evaluate(&r, Observation{Now: t0.Add(time.Hour), Idle: true}, Probes{})
	if !out.End || out.How != EndExpired ||
		out.Wake != "wait expired after 1h00m: merge:step6@f85361d still open - pick a backup item or ask" {
		t.Errorf("outcome = %+v", out)
	}
}

func TestEvaluateEndsSilentlyOnANewTurn(t *testing.T) {
	r := waiting("seat", "reviewer")
	out := Evaluate(&r, Observation{Now: t0.Add(time.Minute), Idle: true, LogMtime: 100}, Probes{})
	if out.End || !r.Settled || !out.Dirty {
		t.Fatalf("first idle pass: %+v settled=%v", out, r.Settled)
	}
	out = Evaluate(&r, Observation{Now: t0.Add(2 * time.Minute), Idle: false, LogMtime: 100}, Probes{})
	if out.End {
		t.Fatalf("no new log line yet, ended: %+v", out)
	}
	out = Evaluate(&r, Observation{Now: t0.Add(3 * time.Minute), Idle: false, LogMtime: 160}, Probes{})
	if !out.End || out.How != EndMessage || out.Wake != "" {
		t.Errorf("a new turn: %+v", out)
	}
}

func TestEvaluateEndsSilentlyOnANewAssignedItem(t *testing.T) {
	r := waiting("review", "q-mine")
	rows := []clientruntime.QueueRow{{QitemID: "q-mine", State: "pending", DestinationSession: dest("reviewer")}}
	if err := Snapshot(&r, rows); err != nil {
		t.Fatal(err)
	}
	if out := Evaluate(&r, Observation{Now: t0, RowsOK: true, Rows: rows}, Probes{}); out.End {
		t.Fatalf("unchanged rail ended the wait: %+v", out)
	}
	rows = append(rows, clientruntime.QueueRow{QitemID: "q-new", State: "pending", DestinationSession: dest("rust-dev")})
	out := Evaluate(&r, Observation{Now: t0, RowsOK: true, Rows: rows}, Probes{})
	if !out.End || out.How != EndNewItem || out.Wake != "" || !strings.Contains(out.Reason, "q-new") {
		t.Errorf("new row: %+v", out)
	}
}

func TestEvaluateEndsOnTheAwaitedEvent(t *testing.T) {
	r := waiting("review", "q-mine")
	open := []clientruntime.QueueRow{{QitemID: "q-mine", State: "in-progress", DestinationSession: dest("reviewer")}}
	_ = Snapshot(&r, open)
	if r.WatchQitem != "q-mine" {
		t.Fatalf("the active target is not watched: %+v", r)
	}
	out := Evaluate(&r, Observation{Now: t0, RowsOK: true, Rows: nil}, Probes{})
	if !out.End || out.How != EndEvent || out.Wake != "wait ended: review:q-mine closed on the rail" {
		t.Errorf("row closed: %+v", out)
	}

	m := waiting("merge", "step6@f85361d")
	m.Repo, m.SHA = "/repo", "f85361d"
	merged := Probes{MergedAs: func(repo, sha string) (string, bool) { return "abc1234", repo == "/repo" && sha == "f85361d" }}
	out = Evaluate(&m, Observation{Now: t0}, merged)
	if !out.End || out.Wake != "wait ended: merge:step6@f85361d merged as abc1234" {
		t.Errorf("merge: %+v", out)
	}

	s := waiting("slot", "cargo")
	s.SlotDir = "/slots"
	out = Evaluate(&s, Observation{Now: t0}, Probes{SlotFree: func(dir string) bool { return dir == "/slots" }})
	if !out.End || out.How != EndEvent {
		t.Errorf("slot: %+v", out)
	}

	p := waiting("peak", "offpeak")
	out = Evaluate(&p, Observation{Now: t0}, Probes{OffPeak: func(time.Time) bool { return true }})
	if !out.End || !strings.Contains(out.Wake, "off-peak window is open") {
		t.Errorf("peak: %+v", out)
	}
}

func TestTheGuardsFirstSnapshotEndsAWaitThatHidesWork(t *testing.T) {
	r := waiting("seat", "reviewer") // started while the rail could not be read
	rows := []clientruntime.QueueRow{{QitemID: "q-next", State: "pending", DestinationSession: dest("rust-dev")}}
	out := Evaluate(&r, Observation{Now: t0, RowsOK: true, Rows: rows}, Probes{})
	if !out.End || out.How != EndClaimable || !strings.Contains(out.Wake, "q-next") {
		t.Errorf("outcome = %+v", out)
	}
	var claim *ClaimableError
	if !errors.As(Snapshot(&r, rows), &claim) {
		t.Error("Snapshot did not answer a ClaimableError")
	}
}

func TestStatusAndCovers(t *testing.T) {
	r := waiting("merge", "step6@f85361d")
	r.Covered = []string{"q-own:in-progress"}
	if got := r.Status(t0.Add(12 * time.Minute)); got != "waiting=merge:step6@f85361d 12m/1h00m" {
		t.Errorf("status = %q", got)
	}
	if !r.Covers("q-own") || r.Covers("q-other") {
		t.Error("Covers")
	}
}
