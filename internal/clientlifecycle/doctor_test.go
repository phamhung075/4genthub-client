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
// machine's own safeguards (this suite runs on a workstation whose rigd may be supervising real rooms), and
// a spawned fake process would make the case depend on scheduling. So the case asserts against THE SEAM
// THE REPORT ACTUALLY READS: clientservices.Live is the process table (pgrep in production, a map here),
// and the heartbeat files are REAL files written through the real reader path. The clock is the other seam,
// so nothing here reads wall time.
package clientlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// healthFixture is one machine's report inputs: a temp log directory holding the heartbeat files, a temp
// rigd.json holding the listed room, a frozen clock, and the process table.
type healthFixture struct {
	t  *testing.T
	at time.Time
	// live maps a safeguard's command-line pattern to the pids that match it. An empty answer is a
	// safeguard that is NOT running, which is what a kill leaves behind.
	live map[string][]int
}

// newHealthFixture wires the seams to a temp machine and restores them afterwards. No test in this file
// touches the real /var-like paths, and none of them starts, restarts or signals anything.
func newHealthFixture(t *testing.T, listedRigs []string) *healthFixture {
	t.Helper()
	logDir := t.TempDir()
	stateDir := t.TempDir()

	previousLogDir, previousStatePath := clientservices.LogDir, clientservices.RigdStatePath
	previousNow, previousLive := clientservices.Now, clientservices.Live
	t.Cleanup(func() {
		clientservices.LogDir, clientservices.RigdStatePath = previousLogDir, previousStatePath
		clientservices.Now, clientservices.Live = previousNow, previousLive
	})

	clientservices.LogDir = func() (string, error) { return logDir, nil }
	clientservices.RigdStatePath = func() (string, error) { return filepath.Join(stateDir, "rigd.json"), nil }

	fixture := &healthFixture{t: t, at: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), live: map[string][]int{}}
	clientservices.Now = func() time.Time { return fixture.at }
	clientservices.Live = func(pattern string) []int { return fixture.live[pattern] }

	state := clientservices.RigdState{}
	for _, rig := range listedRigs {
		state = clientservices.AddRig(state, rig)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := clientservices.WriteRigdState(state); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// beat writes one safeguard's heartbeat file with a chosen pid and age, through the real file layout the
// report reads (clientservices.HeartbeatPath).
func (f *healthFixture) beat(safeguard, rig string, age time.Duration, pid int) {
	f.t.Helper()
	path, err := clientservices.HeartbeatPath(safeguard, rig)
	if err != nil {
		f.t.Fatal(err)
	}
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

// everythingRunning is the all-healthy machine: every safeguard the report walks, plus rigd itself.
func (f *healthFixture) everythingRunning() {
	f.running(clientservices.Compact, "demo", 4101)
	f.running(clientservices.Watchdog, "demo", 4102)
	f.running(clientservices.Messages, "demo", 4105)
	f.running(clientservices.RoomGuard, "demo", 4106)
	f.running(clientservices.Bridge, "", 4103)
	f.running(clientservices.Rigd, "", 4104)
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

// doctor runs the verb once and returns its exit code and both streams.
func doctor(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := (doctorCommand{}).Run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
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

			// The failure. The last beat is 31 s old: past compact's 30 s threshold, past rigd's 75 s? no -
			// 31 s is inside rigd's, which is why the target row is the only one that moves.
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
			rows := rowsFor("demo")
			got := row(t, rows, c.safeguard, c.rig)
			if got.State != wantState {
				t.Errorf("row = %+v, want state %q", got, wantState)
			}
			if got.AgeS != 31 {
				t.Errorf("row = %+v, want age_s 31", got)
			}

			// (b) doctor refuses, and names the safeguard on stderr in 7.5's own words.
			code, stdout, stderr := doctor(t, "demo")
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
// bridge or only rigd down, both `doctor` and the JSON rows say so.
func TestASafeguardThatIsDownIsNamedNoMatterWhichOneItIs(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo"})
	fixture.everythingRunning()
	fixture.live[clientservices.Pattern(clientservices.Rigd, "")] = nil

	code, _, stderr := doctor(t, "demo")
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("doctor exited %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, stderr)
	}
	if !strings.Contains(stderr, "rigd: stopped, age 1s") {
		t.Errorf("stderr = %q, want rigd named without a rig", stderr)
	}

	// --json prints the rows themselves, so a caller reads the same decision the lines carry.
	code, stdout, _ := doctor(t, "demo", "--json")
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("doctor --json exited %d, want %d", code, clientcmd.ExitUnavailable)
	}
	var rows []clientservices.Health
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("doctor --json did not print rows: %v\n%s", err, stdout)
	}
	if got := row(t, rows, clientservices.Rigd, ""); got.State != clientservices.Stopped {
		t.Errorf("json row = %+v, want rigd stopped", got)
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

	got := row(t, rowsFor("demo"), clientservices.Compact, "demo")
	if got.State != clientservices.Silent {
		t.Errorf("row = %+v, want silent: the live pid is not the beat's pid", got)
	}

	code, _, stderr := doctor(t, "demo")
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

	rows := rowsFor("demo")
	for _, want := range []struct{ safeguard, rig string }{
		{clientservices.Compact, "demo"},
		{clientservices.Watchdog, "demo"},
		{clientservices.Bridge, ""},
		{clientservices.Rigd, ""},
	} {
		if got := row(t, rows, want.safeguard, want.rig); got.State != clientservices.Running {
			t.Errorf("row = %+v, want running", got)
		}
	}

	code, stdout, stderr := doctor(t, "demo")
	if code != clientcmd.ExitOK {
		t.Fatalf("doctor exited %d, want %d\nstdout:\n%s\nstderr:\n%s", code, clientcmd.ExitOK, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("a healthy machine wrote %q to stderr", stderr)
	}
	for _, want := range []string{"demo compact: running, age 1s", "demo watchdog: running, age 1s", "bridge: running, age 1s", "rigd: running, age 1s"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}

	// The same machine through --json: the rows carry the same states, so a script reads one shape.
	code, jsonOut, _ := doctor(t, "demo", "--json")
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

// TestDoctorWithoutARigReportsEveryListedRoom: no argument means every room in rigd.json's list, and a
// room with no rigd row at all still gets its safeguard rows - a machine started by hand must be visible.
func TestDoctorWithoutARigReportsEveryListedRoom(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo", "other"})
	fixture.running(clientservices.Rigd, "", 4104)

	rows := rowsFor("")
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
	// rigd itself is alive in this fixture, and it has no rig: the machine-scoped row is reported without
	// one.
	if got := row(t, rows, clientservices.Rigd, ""); got.State != clientservices.Running {
		t.Errorf("rigd = %+v, want running", got)
	}

	code, _, stderr := doctor(t)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("doctor exited %d, want %d", code, clientcmd.ExitUnavailable)
	}
	for _, want := range []string{"demo compact: stopped", "other watchdog: stopped", "bridge: stopped"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if strings.Contains(stderr, "rigd: running") {
		t.Errorf("stderr named a row that is running: %q", stderr)
	}
}

// TestDoctorPrintsTheTailOfAlertsLog: the alert log is the local record of every transition, and the human
// output ends with its tail. The JSON output is the rows alone (7.5).
func TestDoctorPrintsTheTailOfAlertsLog(t *testing.T) {
	fixture := newHealthFixture(t, []string{"demo"})
	fixture.everythingRunning()
	line := "2026-10-11T11:59:00Z demo compact: stopped, age 300s"
	if err := clientservices.AppendAlert(line); err != nil {
		t.Fatal(err)
	}

	_, stdout, _ := doctor(t, "demo")
	if !strings.Contains(stdout, line) {
		t.Errorf("stdout = %q, want the alert tail", stdout)
	}

	_, jsonOut, _ := doctor(t, "demo", "--json")
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

	if code, _, _ := doctor(t, "demo"); code != clientcmd.ExitOK {
		t.Fatalf("doctor exited %d on a healthy machine", code)
	}
	if len(did) != 0 {
		t.Errorf("doctor acted: %v", did)
	}
}
