// THE OWNER'S INCIDENT, AS A CASE: a safeguard that is not running, or that is running and has gone
// quiet, must be NAMED - and `doctor` must exit non-zero on it.
//
// WHAT THIS FILE PROVES, in the shape the brief asks for:
//
//	(a) the health report names the safeguard STOPPED or SILENT, with an AGE;
//	(b) `doctor` exits ExitUnavailable (3) and prints `<rig> <safeguard>: <state>, age <n>s` on stderr;
//	(c) THE FALSIFICATION - the SAME fixture with the safeguard alive and beating produces ExitOK and a
//	    `running` row. Without (c) the case would only prove that a check ran, not that it discriminates.
//
// HOW THE PROCESS IS FAKED, and why. A real `kill` is impractical here: these tests must not signal the
// machine's own safeguards (this suite runs on a workstation whose agent may be supervising real rooms), and
// a spawned fake process would make the case depend on scheduling. So the case asserts against THE SEAM
// THE REPORT ACTUALLY READS: clientservices.Live is the process table (pgrep in production, a map here),
// and the heartbeat files are REAL files written through the real reader path. The clock is the other seam,
// so nothing here reads wall time.
package clientlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// healthFixture is one machine's report inputs: a temp log directory holding the heartbeat files, a temp
// agent.json holding the listed room, a frozen clock, and the process table.
type healthFixture struct {
	t *testing.T
	// root is the state root every read of this fixture is pointed at: the logs land under <root>/logs.
	root string
	at   time.Time
	// live maps a safeguard's command-line pattern to the pids that match it. An empty answer is a
	// safeguard that is NOT running, which is what a kill leaves behind.
	live map[string][]int
}

// newHealthFixture wires the seams to a temp machine and restores them afterwards. No test in this file
// touches the real /var-like paths, and none of them starts, restarts or signals anything.
func newHealthFixture(t *testing.T, listedRigs []string) *healthFixture {
	t.Helper()
	root := t.TempDir()
	stateDir := t.TempDir()

	previousStatePath := clientservices.AgentStatePath
	previousNow, previousLive := clientservices.Now, clientservices.Live
	previousOrphans := orphanRowsFor
	previousClone, previousHooks := cloneRowFor, hooksRowFor
	t.Cleanup(func() {
		clientservices.AgentStatePath = previousStatePath
		clientservices.Now, clientservices.Live = previousNow, previousLive
		orphanRowsFor = previousOrphans
		cloneRowFor, hooksRowFor = previousClone, previousHooks
	})
	// The clone and hooks rows read the checkout this binary was built in: a case about the safeguard rows
	// must not go red because the operator's shared checkout has a file open, or because this machine has not
	// installed the guard. The two cases that assert those rows replace these.
	cloneRowFor = func(string) []cloneRow { return nil }
	hooksRowFor = func(string) []hooksRow { return nil }
	// The orphan row reads the queue and the node list through the rig tool, and no test in this file runs
	// a daemon: the default here is a report with NO orphan row at all, so every case below sees exactly
	// the safeguard rows it was written for. A case that wants the row replaces this.
	orphanRowsFor = func(string) []orphanRow { return nil }

	clientservices.AgentStatePath = func() (string, error) { return filepath.Join(stateDir, "agent.json"), nil }

	fixture := &healthFixture{t: t, root: root, at: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), live: map[string][]int{}}
	clientservices.Now = func() time.Time { return fixture.at }
	clientservices.Live = func(pattern string) []int { return fixture.live[pattern] }

	state := clientservices.AgentState{}
	for _, rig := range listedRigs {
		state = clientservices.AddRig(state, rig)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := clientservices.WriteAgentState(state); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// beat writes one safeguard's heartbeat file with a chosen pid and age, through the real file layout the
// report reads (clientservices.HeartbeatPath).
func (f *healthFixture) beat(safeguard, rig string, age time.Duration, pid int) {
	f.t.Helper()
	path := clientservices.HeartbeatPath(f.root, safeguard, rig)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	body, err := json.Marshal(clientservices.Heartbeat{PID: pid, At: f.at.Add(-age)})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// running makes one safeguard healthy: its process is live AND its heartbeat is fresh, from that process.
func (f *healthFixture) running(safeguard, rig string, pid int) {
	f.live[clientservices.Pattern(safeguard, rig)] = []int{pid}
	f.beat(safeguard, rig, time.Second, pid)
}

// everythingRunning is the all-healthy machine: every safeguard the report walks, plus agent itself.
func (f *healthFixture) everythingRunning() {
	f.running(clientservices.Compact, "demo", 4101)
	f.running(clientservices.Watchdog, "demo", 4102)
	f.running(clientservices.Messages, "demo", 4105)
	f.running(clientservices.RoomGuard, "demo", 4106)
	f.running(clientservices.Budget, "demo", 4107)
	f.running(clientservices.Bridge, "", 4103)
	f.running(clientservices.Agent, "", 4104)
}

// row finds one row of the report, so an assertion names the safeguard rather than an index.
func row(t *testing.T, rows []clientservices.Health, safeguard, rig string) clientservices.Health {
	t.Helper()
	for _, candidate := range rows {
		if candidate.Safeguard == safeguard && candidate.Rig == rig {
			return candidate
		}
	}
	t.Fatalf("the report has no row for %s/%s: %+v", safeguard, rig, rows)
	return clientservices.Health{}
}

// doctor runs the verb once, pointed at the fixture's own state root, and returns its exit code and both
// streams.
func doctor(t *testing.T, fixture *healthFixture, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := (doctorCommand{}).Run(context.Background(), append(args, "--state-root", fixture.root), &out, &errOut)
	return code, out.String(), errOut.String()
}

// orphanRowFor is one rig's orphan_handoffs row as a case supplies it, so a case can choose the state
// without producing a queue or a daemon.
func orphanRowFor(t *testing.T, rig string, state string, orphans ...orphanHandoff) {
	t.Helper()
	orphanRowsFor = func(string) []orphanRow {
		return []orphanRow{{
			Row: clientservices.Health{
				Rig:       rig,
				Safeguard: clientservices.OrphanHandoffs,
				State:     state,
				AgeS:      clientservices.UnknownAge,
			},
			Orphans: orphans,
		}}
	}
}

// 7.11: `doctor` reports `orphan_handoffs`, the row is STOPPED, and it NAMES every stranded qitem with the
// destination it was addressed to. The same names go to stderr, because the refusal is only actionable if
// it says which handoff is stranded.
func TestDoctorReportsOrphanHandoffsAndNamesEachQitem(t *testing.T) {
	f := newHealthFixture(t, []string{"demo"})
	f.everythingRunning()
	orphanRowFor(t, "demo", clientservices.Stopped,
		orphanHandoff{Qitem: "qitem-20261009-aaaa", Destination: "demo-writer@demo"},
		orphanHandoff{Qitem: "qitem-20261009-bbbb", Destination: "demo-web-dev@demo"})

	code, out, errOut := doctor(t, f)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d, want %d", code, clientcmd.ExitUnavailable)
	}
	line := "demo orphan_handoffs: stopped, age unknown"
	if !strings.Contains(out, line) {
		t.Errorf("stdout does not report the row:\n%s", out)
	}
	for _, name := range []string{"qitem-20261009-aaaa -> demo-writer@demo: not live", "qitem-20261009-bbbb -> demo-web-dev@demo: not live"} {
		if !strings.Contains(out, name) {
			t.Errorf("stdout does not name %q:\n%s", name, out)
		}
		if !strings.Contains(errOut, name) {
			t.Errorf("stderr does not name %q:\n%s", name, errOut)
		}
	}
	if !strings.Contains(errOut, line) {
		t.Errorf("stderr does not report the row:\n%s", errOut)
	}
}

// THE FALSIFICATION for that row: the same verb, the same fixture, no stranded handoff - the row reads
// RUNNING and doctor answers ExitOK. Without this, the case above would prove only that a row was printed.
func TestAnOrphanFreeReportIsExitOKAndARunningRow(t *testing.T) {
	f := newHealthFixture(t, []string{"demo"})
	f.everythingRunning()
	orphanRowFor(t, "demo", clientservices.Running)

	code, out, errOut := doctor(t, f)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut)
	}
	if !strings.Contains(out, "demo orphan_handoffs: running, age unknown") {
		t.Errorf("the row is missing or not running:\n%s", out)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing when every row is running", errOut)
	}
}

// A read that fails is NOT a clean machine: the row says silent, the reason is named, and doctor refuses.
// This is the reading 7.7 forbids - a missing signal is not evidence of work - applied to the queue read.
func TestAnUnreadableOrphanReadIsNotACleanReport(t *testing.T) {
	f := newHealthFixture(t, []string{"demo"})
	f.everythingRunning()
	orphanRowsFor = func(string) []orphanRow {
		return []orphanRow{{
			Row: clientservices.Health{Rig: "demo", Safeguard: clientservices.OrphanHandoffs, State: clientservices.Silent, AgeS: clientservices.UnknownAge},
			Err: errors.New("rig queue list: exit status 1"),
		}}
	}

	code, _, errOut := doctor(t, f)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if !strings.Contains(errOut, "orphan_handoffs could not be read: rig queue list: exit status 1") {
		t.Errorf("stderr does not name the reason:\n%s", errOut)
	}
	if !strings.Contains(errOut, "demo orphan_handoffs: silent, age unknown") {
		t.Errorf("stderr does not report the row:\n%s", errOut)
	}
}

// The reader over the two runtime reads it makes: a pending qitem to a RUNNING seat is not an orphan, the
// owner's `human@` rows are not handoffs to a seat at all, a dead seat's row is, and a row addressed into
// another room is not this room's orphan.
func TestOrphanHandoffsReadsTheQueueAndTheLiveNodes(t *testing.T) {
	previousQueue, previousLive := queuePending, roomLive
	t.Cleanup(func() { queuePending, roomLive = previousQueue, previousLive })
	queuePending = func(room string) ([]clientruntime.QueueRow, error) {
		if room != "demo" {
			t.Errorf("queue read for %q, want the named room only", room)
		}
		return []clientruntime.QueueRow{
			{QitemID: "q-live", DestinationSession: "demo-lead@demo"},
			{QitemID: "q-human", DestinationSession: "human@demo"},
			{QitemID: "q-dead", DestinationSession: "demo-writer@demo"},
			{QitemID: "q-other", DestinationSession: "other-go-dev@other"},
		}, nil
	}
	roomLive = func(room string) (map[string]bool, error) {
		if room != "demo" {
			t.Errorf("node read for %q, want the named room only", room)
		}
		return map[string]bool{
			"demo-lead@demo":     true,
			"other-go-dev@other": true,
		}, nil
	}

	orphans, err := orphanHandoffs("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Qitem != "q-dead" || orphans[0].Destination != "demo-writer@demo" {
		t.Fatalf("orphans for demo = %+v, want only q-dead to demo-writer@demo", orphans)
	}

	queuePending = func(string) ([]clientruntime.QueueRow, error) { return nil, errors.New("no daemon") }
	if _, err := orphanHandoffs("demo"); err == nil {
		t.Fatal("an unreadable queue must be an error, not a machine with no orphans")
	}
	queuePending = func(string) ([]clientruntime.QueueRow, error) { return nil, nil }
	roomLive = func(string) (map[string]bool, error) { return nil, errors.New("no runtime") }
	if _, err := orphanHandoffs("demo"); err == nil {
		t.Fatal("an unreadable seat listing must be an error, not a machine with no orphans")
	}
}

// TestKilledOrSilentSafeguardIsNamedAndDoctorRefuses is the owner's incident: the compaction supervisor,
// the watchdog and the bridge are checked one by one, each in BOTH shapes a real failure takes - the
// process is gone (stopped), and the process is alive but has stopped beating (silent, the hung case the
// owner named).
func TestKilledOrSilentSafeguardIsNamedAndDoctorRefuses(t *testing.T) {
	cases := []struct {
		name      string
		safeguard string
		rig       string
		// silent keeps the process alive so the HEARTBEAT decides, which is the hung case; otherwise the
		// process is gone and the PROCESS decides.
		silent bool
	}{
		{"compact killed", clientservices.Compact, "demo", false},
		{"compact hung", clientservices.Compact, "demo", true},
		{"watchdog killed", clientservices.Watchdog, "demo", false},
		{"watchdog hung", clientservices.Watchdog, "demo", true},
		{"bridge killed", clientservices.Bridge, "", false},
		{"bridge hung", clientservices.Bridge, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture := newHealthFixture(t, []string{"demo"})
			fixture.everythingRunning()

			// The failure. The last beat is 31 s old: past compact's 30 s threshold, past agent's 75 s? no -
			// 31 s is inside agent's, which is why the target row is the only one that moves.
			fixture.beat(c.safeguard, c.rig, 31*time.Second, 4101)
			if !c.silent {
				// The process is gone: pgrep finds nothing for its command line.
				fixture.live[clientservices.Pattern(c.safeguard, c.rig)] = nil
			}
			wantState := clientservices.Stopped
			if c.silent {
				wantState = clientservices.Silent
			}

			// (a) the report names it, with the age of its last beat.
			rows := rowsFor("demo", fixture.root)
			got := row(t, rows, c.safeguard, c.rig)
			if got.State != wantState {
				t.Errorf("row = %+v, want state %q", got, wantState)
			}
			if got.AgeS != 31 {
				t.Errorf("row = %+v, want age_s 31", got)
			}

			// (b) doctor refuses, and names the safeguard on stderr in 7.5's own words.
			code, stdout, stderr := doctor(t, fixture, "demo")
			if code != clientcmd.ExitUnavailable {
				t.Fatalf("doctor exited %d, want %d\nstdout:\n%s\nstderr:\n%s", code, clientcmd.ExitUnavailable, stdout, stderr)
			}
			named := strings.TrimSpace(c.rig + " " + c.safeguard)
			want := named + ": " + wantState + ", age 31s"
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want the line %q", stderr, want)
			}
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want the same row in the report", stdout)
			}
		})
	}
}

// TestASafeguardThatIsDownIsNamedNoMatterWhichOneItIs checks the machine-scoped rows too: with only the
// bridge or only agent down, both `doctor` and the JSON rows say so.
func TestASafeguardThatIsDownIsNamedNoMatterWhichOneItIs(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo"})
	fixture.everythingRunning()
	fixture.live[clientservices.Pattern(clientservices.Agent, "")] = nil

	code, _, stderr := doctor(t, fixture, "demo")
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("doctor exited %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, stderr)
	}
	if !strings.Contains(stderr, "agent: stopped, age 1s") {
		t.Errorf("stderr = %q, want agent named without a rig", stderr)
	}

	// --json prints the rows themselves, so a caller reads the same decision the lines carry.
	code, stdout, _ := doctor(t, fixture, "demo", "--json")
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("doctor --json exited %d, want %d", code, clientcmd.ExitUnavailable)
	}
	var rows []clientservices.Health
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("doctor --json did not print rows: %v\n%s", err, stdout)
	}
	if got := row(t, rows, clientservices.Agent, ""); got.State != clientservices.Stopped {
		t.Errorf("json row = %+v, want agent stopped", got)
	}
}

// TestAStaleBeatFromAnotherPidIsSilent is the third clause of 7.2's precedence: a live process whose
// heartbeat names a DIFFERENT pid is silent - a restarted child whose old beat is still on disk is not
// evidence that the new one is working.
func TestAStaleBeatFromAnotherPidIsSilent(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo"})
	fixture.everythingRunning()
	// Same safeguard, new process: the beat still names the dead pid, and it is fresh, so only the pid
	// comparison can catch it.
	fixture.live[clientservices.Pattern(clientservices.Compact, "demo")] = []int{7777}
	fixture.beat(clientservices.Compact, "demo", time.Second, 4101)

	got := row(t, rowsFor("demo", fixture.root), clientservices.Compact, "demo")
	if got.State != clientservices.Silent {
		t.Errorf("row = %+v, want silent: the live pid is not the beat's pid", got)
	}

	code, _, stderr := doctor(t, fixture, "demo")
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("doctor exited %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if !strings.Contains(stderr, "demo compact: silent, age 1s") {
		t.Errorf("stderr = %q, want the silent row named", stderr)
	}
}

// TestEverySafeguardRunningIsExitOKAndARunningRow IS THE FALSIFICATION. The same fixture, the same seams,
// the same verb - and every safeguard alive and beating. Without this, the case above would prove only that
// a check ran; with it, the check is shown to DISCRIMINATE between a healthy machine and the incident.
func TestEverySafeguardRunningIsExitOKAndARunningRow(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo"})
	fixture.everythingRunning()

	rows := rowsFor("demo", fixture.root)
	for _, want := range []struct{ safeguard, rig string }{
		{clientservices.Compact, "demo"},
		{clientservices.Watchdog, "demo"},
		{clientservices.Budget, "demo"},
		{clientservices.Bridge, ""},
		{clientservices.Agent, ""},
	} {
		if got := row(t, rows, want.safeguard, want.rig); got.State != clientservices.Running {
			t.Errorf("row = %+v, want running", got)
		}
	}

	code, stdout, stderr := doctor(t, fixture, "demo")
	if code != clientcmd.ExitOK {
		t.Fatalf("doctor exited %d, want %d\nstdout:\n%s\nstderr:\n%s", code, clientcmd.ExitOK, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("a healthy machine wrote %q to stderr", stderr)
	}
	for _, want := range []string{"demo compact: running, age 1s", "demo watchdog: running, age 1s", "demo budget: running, age 1s", "bridge: running, age 1s", "agent: running, age 1s"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}

	// The same machine through --json: the rows carry the same states, so a script reads one shape.
	code, jsonOut, _ := doctor(t, fixture, "demo", "--json")
	if code != clientcmd.ExitOK {
		t.Fatalf("doctor --json exited %d, want %d", code, clientcmd.ExitOK)
	}
	var decoded []clientservices.Health
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("doctor --json did not print rows: %v\n%s", err, jsonOut)
	}
	if got := row(t, decoded, clientservices.Watchdog, "demo"); got.State != clientservices.Running {
		t.Errorf("json row = %+v, want running", got)
	}
}

// TestDoctorWithoutARigReportsEveryListedRoom: no argument means every room in agent.json's list, and a
// room with no agent row at all still gets its safeguard rows - a machine started by hand must be visible.
func TestDoctorWithoutARigReportsEveryListedRoom(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo", "other"})
	fixture.running(clientservices.Agent, "", 4104)

	rows := rowsFor("", fixture.root)
	for _, want := range []struct{ safeguard, rig string }{
		{clientservices.Compact, "demo"},
		{clientservices.Watchdog, "demo"},
		{clientservices.Compact, "other"},
		{clientservices.Watchdog, "other"},
		{clientservices.Bridge, ""},
	} {
		got := row(t, rows, want.safeguard, want.rig)
		if got.State != clientservices.Stopped {
			t.Errorf("%s/%s = %+v, want stopped", want.rig, want.safeguard, got)
		}
	}
	// agent itself is alive in this fixture, and it has no rig: the machine-scoped row is reported without
	// one.
	if got := row(t, rows, clientservices.Agent, ""); got.State != clientservices.Running {
		t.Errorf("agent = %+v, want running", got)
	}

	code, _, stderr := doctor(t, fixture)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("doctor exited %d, want %d", code, clientcmd.ExitUnavailable)
	}
	for _, want := range []string{"demo compact: stopped", "other watchdog: stopped", "bridge: stopped"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if strings.Contains(stderr, "agent: running") {
		t.Errorf("stderr named a row that is running: %q", stderr)
	}
}

// TestDoctorPrintsTheTailOfAlertsLog: the alert log is the local record of every transition, and the human
// output ends with its tail. The JSON output is the rows alone (7.5).
func TestDoctorPrintsTheTailOfAlertsLog(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo"})
	fixture.everythingRunning()
	line := "2026-10-11T11:59:00Z demo compact: stopped, age 300s"
	if err := clientservices.AppendAlert(fixture.root, line); err != nil {
		t.Fatal(err)
	}

	_, stdout, _ := doctor(t, fixture, "demo")
	if !strings.Contains(stdout, line) {
		t.Errorf("stdout = %q, want the alert tail", stdout)
	}

	_, jsonOut, _ := doctor(t, fixture, "demo", "--json")
	if strings.Contains(jsonOut, "age 300s") {
		t.Errorf("--json printed the alert tail: %q", jsonOut)
	}
}

// TestDoctorIsReadOnly is 7.5's strongest sentence: `doctor` never starts, restarts or signals anything.
// The verbs that could are the seams, and every one of them is replaced with a recorder.
func TestDoctorIsReadOnly(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo"})
	fixture.everythingRunning()

	var did []string
	restoreAll(t)
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		did = append(did, "spawn "+strings.Join(args, " "))
		return diedChild{}, nil
	}
	stopPattern = func(pattern string) bool {
		did = append(did, "signal "+pattern)
		return true
	}
	rigNodes = func(string) ([]map[string]any, bool, error) {
		did = append(did, "read nodes")
		return nil, false, nil
	}

	if code, _, _ := doctor(t, fixture, "demo"); code != clientcmd.ExitOK {
		t.Fatalf("doctor exited %d on a healthy machine", code)
	}
	if len(did) != 0 {
		t.Errorf("doctor acted: %v", did)
	}
}
