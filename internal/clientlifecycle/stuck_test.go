package clientlifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// stuckFixture is one room the detector reads from a table, so a case says what the queue, the board and
// each seat's session log contain without a room, a queue, a board, a terminal or a clock.
type stuckFixture struct {
	t         *testing.T
	deps      *stuckDeps
	sent      []string
	escalated []string
	rows      []stuckRow
	board     []boardTask
	notes     []string
	seats     []string
	logs      map[string]string
	runtimes  map[string]string
	state     string
	dir       string
	room      string
	now       time.Time
}

const stuckFixtureRoom = "demo"

func stuckFixtureFor(t *testing.T) *stuckFixture {
	t.Helper()
	fixture := &stuckFixture{
		t:        t,
		room:     stuckFixtureRoom,
		now:      time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
		state:    filepath.Join(t.TempDir(), "stuck.json"),
		dir:      t.TempDir(),
		logs:     map[string]string{},
		runtimes: map[string]string{},
	}
	fixture.deps = &stuckDeps{
		rows:  func(string) ([]stuckRow, []string) { return fixture.rows, fixture.notes },
		board: func() ([]boardTask, []string) { return fixture.board, nil },
		seats: func(string) []string { return fixture.seats },
		seatLog: func(_, seat string) (string, string) {
			runtime, known := fixture.runtimes[seat]
			if !known {
				runtime = "omp"
			}
			return fixture.logs[seat], runtime
		},
		writeAt: stuckWriteAt,
		say: func(room, seat, text string) {
			fixture.sent = append(fixture.sent, room+"/"+seat+": "+text)
		},
		escalate: func(room string, finding stuckFinding, open time.Duration) (string, error) {
			fixture.escalated = append(fixture.escalated, room+": "+stuckEscalationText(room, finding, open))
			return "qitem-escalated-1", nil
		},
		now:   func() time.Time { return fixture.now },
		state: func(string) (string, error) { return fixture.state, nil },
	}
	return fixture
}

// seat gives a seat a live session and a session log as old as written.
func (f *stuckFixture) seat(name string, written time.Time) {
	f.t.Helper()
	path := filepath.Join(f.dir, name+".jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"user\"}\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(path, written, written); err != nil {
		f.t.Fatal(err)
	}
	f.seats = append(f.seats, name)
	f.logs[name] = path
	f.runtimes[name] = "omp"
}

// seatWithoutALog is a live session whose log this machine cannot read: a runtime that keeps none.
func (f *stuckFixture) seatWithoutALog(name, runtime string) {
	f.seats = append(f.seats, name)
	f.runtimes[name] = runtime
}

func (f *stuckFixture) scan() stuckReport {
	f.t.Helper()
	options := stuckOptions{Room: f.room, Now: f.now, IdleAfter: stuckIdleAfter, EscalateAfter: stuckEscalateAfter}
	report := stuckScanFor(f.deps, options)
	stuckNotify(f.deps, options, &report)
	return report
}

// at moves the fixture's clock, which is how the hour-long escalation is reachable without an hour. The
// registered seats' logs are re-written to the new time, so a case about a stuck ROW is not also a case
// about seats that went idle while the clock moved.
func (f *stuckFixture) at(offset time.Duration) {
	f.now = f.now.Add(offset)
	for _, path := range f.logs {
		if err := os.Chtimes(path, f.now, f.now); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *stuckFixture) sendsTo(seat string) []string {
	prefix := f.room + "/" + seat + ": "
	var out []string
	for _, line := range f.sent {
		if strings.HasPrefix(line, prefix) {
			out = append(out, strings.TrimPrefix(line, prefix))
		}
	}
	return out
}

func (f *stuckFixture) queueRow(id string, destination string) stuckRow {
	row := stuckRow{
		QitemID: id, Destination: f.room + "-" + destination + "@" + f.room, State: stuckInProgress,
		Summary: "the row's own one line", Updated: f.now.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
	}
	f.rows = append(f.rows, row)
	return row
}

// THE RAIL'S ONE STUCK FACT IS A GONE OWNER. A row in progress whose seat has a live session is not stuck,
// however old it is: the closure deadline the predecessor read is not a field the daemon's row carries, and
// an age threshold was measured false (a working seat records progress only when it has something to write).
// The same room with that session gone does alert - the case below.
func TestStuckLeavesAnInProgressRowWhoseOwnerIsLiveAlone(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seat("go-dev", fixture.now)
	fixture.seat(overlapLeadSeat, fixture.now)
	fixture.queueRow("qitem-old", "go-dev")

	report := fixture.scan()

	if len(report.Findings) != 0 || len(fixture.sent) != 0 {
		t.Fatalf("a row with a live owner produced %+v and %v, want nothing", report.Findings, fixture.sent)
	}
}

// A row whose owning seat has no live session is the plan's second queue condition. It names the seat it
// could not tell, and it goes to the lead ALONE: an alert addressed to a seat with no session is a message
// nothing reads.
func TestStuckAlertsARowWhoseOwnerIsGoneToTheLeadAlone(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seat(overlapLeadSeat, fixture.now)
	fixture.queueRow("qitem-orphan", "gone")

	report := fixture.scan()

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %+v, want the orphaned row", report.Findings)
	}
	if got := fixture.sendsTo(overlapLeadSeat); len(got) != 1 {
		t.Fatalf("the lead got %d message(s), want 1: %v", len(got), got)
	} else if !strings.Contains(got[0], "has no live session") || !strings.Contains(got[0], "gone") {
		t.Errorf("the lead's message does not name the condition and the seat: %s", got[0])
	}
	if got := fixture.sent; len(got) != 1 {
		t.Fatalf("sent %v, want only the lead's message", got)
	}
}

// The escalation route is seat/lead, then owner, and the ladder does not repeat: the first scan alerts
// once, a scan nine minutes later says nothing, the scan after the window writes ONE escalation row to the
// room's human, and every scan after that is silent. A finding that clears is forgotten, so a row that
// comes back is news again.
func TestStuckEscalatesAStandingFindingOnceAndForgetsWhatClears(t *testing.T) {
	fixture := stuckFixtureFor(t)
	// The row's owner is GONE, which is the rail's stuck condition: a live owner would make the row not
	// stuck at all. A gone owner's finding goes to the lead alone (an alert to a seat with no session is a
	// message nothing reads), and the board case covers the seat-and-lead rung.
	fixture.seat(overlapLeadSeat, fixture.now)
	fixture.queueRow("qitem-stuck", "go-dev")

	first := fixture.scan()
	if len(first.Sent) != 1 || len(first.Escalated) != 0 {
		t.Fatalf("the first scan sent %v and escalated %v, want one message and no escalation", first.Sent, first.Escalated)
	}
	if len(fixture.sent) != 1 {
		t.Fatalf("the first scan sent %d messages, want 1 (the lead, the owner being gone)", len(fixture.sent))
	}

	fixture.at(9 * time.Minute)
	fixture.sent = nil
	if report := fixture.scan(); len(fixture.sent) != 0 || len(report.Escalated) != 0 {
		t.Fatalf("a scan inside the window sent %v and escalated %v, want neither", fixture.sent, report.Escalated)
	}

	fixture.at(stuckEscalateAfter - 9*time.Minute)
	fixture.sent = nil
	escalated := fixture.scan()
	if len(escalated.Escalated) != 1 || len(fixture.escalated) != 1 {
		t.Fatalf("the scan after the window escalated %v (%d rows), want exactly one", escalated.Escalated, len(fixture.escalated))
	}
	if len(fixture.sent) != 0 {
		t.Errorf("the escalation also sent %v, want the owner's row alone", fixture.sent)
	}
	for _, want := range []string{"Stuck escalation in demo", "qitem-stuck", "still there"} {
		if !strings.Contains(fixture.escalated[0], want) {
			t.Errorf("the escalation row does not carry %q: %s", want, fixture.escalated[0])
		}
	}

	fixture.at(2 * stuckEscalateAfter)
	fixture.sent = nil
	if report := fixture.scan(); len(fixture.sent) != 0 || len(report.Escalated) != 0 || len(fixture.escalated) != 1 {
		t.Fatalf("a scan long after the escalation sent %v and escalated %v, want silence", fixture.sent, report.Escalated)
	}

	fixture.rows = nil
	fixture.scan()
	fixture.queueRow("qitem-stuck", "go-dev")
	fixture.sent = nil
	if report := fixture.scan(); len(fixture.sent) != 1 {
		t.Fatalf("a row that came back after clearing sent %d message(s), want 1 - a cleared finding must be "+
			"forgotten, or it is never news again", len(fixture.sent))
	} else if len(report.Sent) != 1 {
		t.Errorf("report.Sent = %v, want one re-sent finding", report.Sent)
	}
}

// An idle seat is a live session whose log nobody has written for the window. The nudge goes to the LEAD
// and never to the idle seat, a seat whose log was written inside the window is not idle, and a seat whose
// log cannot be read is SKIPPED AND NAMED rather than called idle.
func TestStuckNudgesTheLeadAboutAnIdleSeatAndNeverTheSeat(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seat("go-dev", fixture.now.Add(-stuckIdleAfter-time.Minute))
	fixture.seat("fe-dev", fixture.now.Add(-time.Minute))
	fixture.seatWithoutALog("architect", "codex")

	report := fixture.scan()

	if len(report.Idle) != 1 || report.Idle[0].Seat != "go-dev" {
		t.Fatalf("idle = %+v, want go-dev alone", report.Idle)
	}
	messages := fixture.sendsTo(overlapLeadSeat)
	if len(messages) != 1 {
		t.Fatalf("the lead got %d message(s), want 1: %v", len(messages), messages)
	}
	for _, want := range []string{"go-dev", "idle", "Nothing is blocked", "assigns nothing"} {
		if !strings.Contains(messages[0], want) {
			t.Errorf("the nudge does not carry %q: %s", want, messages[0])
		}
	}
	if got := fixture.sendsTo("go-dev"); len(got) != 0 {
		t.Errorf("the idle seat was told it is idle: %v", got)
	}
	if len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0], "architect") ||
		!strings.Contains(report.Skipped[0], "codex") {
		t.Errorf("skipped = %v, want the unreadable log named with its runtime", report.Skipped)
	}
}

// The owner node runs no model, so it is never idle however old its log is: 4.6's predicate decides it.
func TestStuckNeverCallsTheOwnerNodeIdle(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seatWithoutALog("owner", "terminal")
	ownerLog := filepath.Join(fixture.dir, "owner.jsonl")
	if err := os.WriteFile(ownerLog, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := fixture.now.Add(-10 * time.Hour)
	if err := os.Chtimes(ownerLog, old, old); err != nil {
		t.Fatal(err)
	}
	fixture.logs["owner"] = ownerLog

	report := fixture.scan()

	if len(report.Idle) != 0 {
		t.Fatalf("idle = %+v, want the owner node excluded", report.Idle)
	}
	if len(report.Skipped) != 0 {
		t.Errorf("skipped = %v, want nothing: the owner node is decided, not skipped", report.Skipped)
	}
	if len(fixture.sent) != 0 {
		t.Errorf("sent %v, want nothing", fixture.sent)
	}
}

// A board task past its due date is reported by the room whose live seat one of its assignees names, and a
// task no live seat of this room names is COUNTED in the notes rather than alerted about, so several rooms
// on one machine do not each repeat it.
func TestStuckReportsABoardTaskPastItsDueDateForTheRoomThatOwnsIt(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seat("go-dev", fixture.now)
	fixture.seat(overlapLeadSeat, fixture.now)
	fixture.board = []boardTask{
		{ID: "task-late", Title: "the board's own one line", Status: stuckInProgress,
			Due: fixture.now.Add(-time.Hour).UTC().Format(time.RFC3339), Assignees: []string{"go-dev"}},
		{ID: "task-elsewhere", Title: "another room's", Status: stuckInProgress,
			Due: fixture.now.Add(-time.Hour).UTC().Format(time.RFC3339), Assignees: []string{"someone-else"}},
		{ID: "task-on-time", Title: "still ahead", Status: stuckInProgress,
			Due: fixture.now.Add(time.Hour).UTC().Format(time.RFC3339), Assignees: []string{"go-dev"}},
		{ID: "task-no-due", Title: "no due date at all", Status: stuckInProgress, Assignees: []string{"go-dev"}},
	}

	report := fixture.scan()

	if len(report.Findings) != 1 || report.Findings[0].ID != "task-late" {
		t.Fatalf("findings = %+v, want the late board task alone (notes=%v)", report.Findings, report.Notes)
	}
	if got := fixture.sendsTo("go-dev"); len(got) != 1 || !strings.Contains(got[0], "board task") {
		t.Fatalf("go-dev got %v, want one board alert", got)
	}
	found := false
	for _, note := range report.Notes {
		if strings.Contains(note, "1 board task") && strings.Contains(note, "no live seat") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want the unplaceable task counted and named", report.Notes)
	}
}

// A detector that could not read its sources has NO answer to give: an unreadable queue listing is reported
// as a note, never as "nothing is stuck".
func TestStuckReportsWhatItCouldNotRead(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seat(overlapLeadSeat, fixture.now)
	fixture.notes = []string{"the queue listing could not be read: the runtime socket did not answer"}
	fixture.queueRow("qitem-unreadable", "lead")

	report := fixture.scan()

	if len(report.Findings) != 0 {
		t.Fatalf("findings = %+v, want none: an unreadable listing is not a finding", report.Findings)
	}
	joined := strings.Join(report.Notes, " | ")
	for _, want := range []string{"could not be read", "runtime socket"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes do not carry %q: %s", want, joined)
		}
	}
	if len(fixture.sent) != 0 {
		t.Errorf("sent %v, want nothing", fixture.sent)
	}
}

// The board's list route writes an assignee as `@<seat>`, and this reader is the ONE place that strips it:
// the scan compares the field against live session names, so a reader that forgot would silently stop every
// board alert - a detector that looks like it works and never fires.
func TestStuckAssigneesAreTheBareSeatKeys(t *testing.T) {
	got := stuckAssignees([]any{"@go-dev", "lead", "", 7})
	want := []string{"go-dev", "lead"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("stuckAssignees = %v, want %v", got, want)
	}
	if got := stuckAssignees("not a list"); got != nil {
		t.Errorf("stuckAssignees of a value that is not a list = %v, want nil", got)
	}
	if got := stuckAssignees([]any{}); len(got) != 0 {
		t.Errorf("stuckAssignees of an empty list = %v, want nothing", got)
	}
}

// The documented order is `stuck ROOM [flags]`, and flag.Parse stops at the first non-flag argument. This
// case exists because the SMOKE RUN of the built binary showed `stuck ROOM --dry-run` taking the report
// branch instead: a dry run that is not dry, which for a verb that alerts means it SENDS.
func TestStuckReadsItsRoomBeforeOrAfterItsFlags(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seat(overlapLeadSeat, fixture.now)
	fixture.queueRow("qitem-late", "go-dev")

	restoreDeps, restoreScan := stuckDepsFor, stuckScanFor
	t.Cleanup(func() { stuckDepsFor, stuckScanFor = restoreDeps, restoreScan })
	stuckDepsFor = func(string) *stuckDeps { return fixture.deps }
	stuckScanFor = func(deps *stuckDeps, options stuckOptions) stuckReport { return stuckScan(deps, options) }

	stateRoot := t.TempDir()
	for _, args := range [][]string{
		{"demo", "--dry-run", "--state-root", stateRoot},
		{"--dry-run", "--state-root", stateRoot, "demo"},
	} {
		var stdout, stderr strings.Builder
		if code := (stuckCommand{}).Run(context.Background(), args, &stdout, &stderr); code != clientcmd.ExitOK {
			t.Fatalf("%v: exit %d, stderr %s", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "DRY RUN, nothing sent") {
			t.Errorf("%v: a dry run did not say so:\n%s", args, stdout.String())
		}
		if !strings.Contains(stdout.String(), "qitem-late") {
			t.Errorf("%v: the dry run did not say what it would send:\n%s", args, stdout.String())
		}
		if len(fixture.sent) != 0 {
			t.Fatalf("%v: a DRY RUN sent %v", args, fixture.sent)
		}
	}

	var stdout, stderr strings.Builder
	if code := (stuckCommand{}).Run(context.Background(), []string{"--dry-run"}, &stdout, &stderr); code != clientcmd.ExitUsage {
		t.Errorf("no room name: exit %d, want a usage error", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := (stuckCommand{}).Run(context.Background(), []string{"demo", "other", "--dry-run"}, &stdout, &stderr); code != clientcmd.ExitUsage {
		t.Errorf("two room names: exit %d, want a usage error", code)
	}
}

// The scan rides agent's own loop on a ten-minute timer, and its first pass runs: a room `up` adds while
// agent runs is looked at on the next pass rather than ten minutes later.
func TestStuckDueRunsTheFirstPassAndThenEveryTenMinutes(t *testing.T) {
	manager := &agentManager{}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	if !manager.stuckDue(now) {
		t.Fatal("the first pass did not run the scan")
	}
	if manager.stuckDue(now.Add(9 * time.Minute)) {
		t.Error("a pass nine minutes later ran the scan again")
	}
	if !manager.stuckDue(now.Add(stuckEvery)) {
		t.Error("the pass at ten minutes did not run the scan")
	}
}

// THE FORGET PASS RUNS ON EVERY PASS, INCLUDING ONE THAT SENT OR ESCALATED SOMETHING ELSE. The shipped case
// clears its finding on a pass where nothing was sent, which is why `changed || stuckForget(memory, report)`
// read as correct: `||` SHORT-CIRCUITS, so the moment a pass alerts, the forget pass never runs - a finding
// that cleared on that pass keeps its record, and its return inside the window reads as already-alerted and is
// silently NOT re-alerted. The pair below is the defect (a clear on a pass that sends) and the control (a
// clear on a pass that sends nothing); both must end with the row re-alerting.
func TestStuckForgetsAClearedFindingOnAPassThatSends(t *testing.T) {
	fixture := stuckFixtureFor(t)
	fixture.seat(overlapLeadSeat, fixture.now)
	fixture.queueRow("qitem-standing", "go-dev")
	fixture.queueRow("qitem-clearing", "go-dev")

	// Pass 1: both rows have a gone owner, so both alert and both are recorded.
	if first := fixture.scan(); len(first.Sent) != 2 {
		t.Fatalf("the first pass sent %v, want both rows", first.Sent)
	}

	// Pass 2: qitem-clearing is gone and a third row appears late, so this pass SENDS - the pass that used to
	// skip the forget pass while qitem-clearing's record survived it.
	fixture.at(9 * time.Minute)
	fixture.rows = fixture.rows[:1]
	fixture.queueRow("qitem-new", "go-dev")
	fixture.sent = nil
	if second := fixture.scan(); len(second.Sent) != 1 {
		t.Fatalf("the second pass sent %v, want the new row alone", second.Sent)
	}

	// Pass 3: qitem-clearing returns INSIDE the window, so it is news again - one message to the lead (its
	// owner is gone). With its record left behind it read as already-alerted and this pass was silent.
	fixture.queueRow("qitem-clearing", "go-dev")
	fixture.sent = nil
	back := fixture.scan()
	if len(fixture.sent) != 1 {
		t.Fatalf("a row that cleared on a pass that sent returned to %d message(s), want 1 - the forget pass did "+
			"not run on the pass that sent, so its old record read as already-alerted", len(fixture.sent))
	}
	if len(back.Sent) != 1 || !strings.HasPrefix(back.Sent[0], stuckQueueKind+" qitem-clearing") {
		t.Errorf("report.Sent = %v, want the clearing row re-sent", back.Sent)
	}

	// The control, the shipped path: the same clear on a pass that sends NOTHING is forgotten too.
	fixture.rows = nil
	fixture.sent = nil
	if report := fixture.scan(); len(fixture.sent) != 0 || len(report.Sent) != 0 {
		t.Fatalf("a pass with nothing late sent %v, want silence", fixture.sent)
	}
	fixture.queueRow("qitem-clearing", "go-dev")
	fixture.sent = nil
	if report := fixture.scan(); len(fixture.sent) != 1 || len(report.Sent) != 1 {
		t.Errorf("a row that cleared on a quiet pass returned to %d message(s) / %v, want 1 and one re-sent finding "+
			"(the control leg)", len(fixture.sent), report.Sent)
	}
}

// THE BOARD URL CARRIES ITS BOUND ONCE. `stuckBoardPath` already ended in `limit=` and `stuckBoardTasks`
// appended `stuckBoardLimit` to it, so every request went out as `limit=200200` - a bound nothing asked for, on
// a route that answers it. The case reads what the request actually carries, because a URL assembled by
// concatenation cannot be checked by reading the constants that built it.
func TestStuckBoardURLCarriesItsLimitOnce(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"tasks":[],"count":0}`))
	}))
	defer server.Close()
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "a-token-this-case-never-checks")

	tasks, notes := stuckBoardTasks()
	if len(notes) != 0 {
		t.Fatalf("the board read reported %v, want no notes", notes)
	}
	if len(tasks) != 0 {
		t.Fatalf("the board read answered %d task(s), want none", len(tasks))
	}
	if want := "task_status=in-progress&limit=" + strconv.Itoa(stuckBoardLimit); gotQuery != want {
		t.Errorf("the board request carried %q, want %q - the bound is built from stuckBoardLimit once", gotQuery, want)
	}
	if gotPath != "/api/v2/tasks/" {
		t.Errorf("the board request path = %q, want /api/v2/tasks/", gotPath)
	}
}
