// The report's own rules, tested where they live: 7.4's thresholds, 7.2's precedence, the heartbeat file
// layout, rigd's state file and the alert log.
//
// EVERY TEST HERE FROZEN-CLOCKS THE SEAM and answers the process table through the seam: nothing in this
// file reads wall time or pgrep, and nothing writes outside a temp directory.
package clientservices

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reportFixture wires the three seams (the clock, the process table and the log directory) to a temp
// machine, and answers the state file from a temp store too.
type reportFixture struct {
	t    *testing.T
	at   time.Time
	dir  string
	live map[string][]int
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	dir := t.TempDir()
	stateDir := t.TempDir()
	fixture := &reportFixture{t: t, at: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), dir: dir, live: map[string][]int{}}

	previousLogDir, previousNow, previousLive := LogDir, Now, Live
	previousStatePath := RigdStatePath
	t.Cleanup(func() {
		LogDir, Now, Live = previousLogDir, previousNow, previousLive
		RigdStatePath = previousStatePath
	})
	LogDir = func() (string, error) { return dir, nil }
	Now = func() time.Time { return fixture.at }
	Live = func(pattern string) []int { return fixture.live[pattern] }
	RigdStatePath = func() (string, error) { return filepath.Join(stateDir, "rigd.json"), nil }
	return fixture
}

// beat writes one heartbeat file with a chosen pid and age.
func (f *reportFixture) beat(safeguard, rig string, age time.Duration, pid int) {
	f.t.Helper()
	path, err := HeartbeatPath(safeguard, rig)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	body, err := json.Marshal(Heartbeat{PID: pid, At: f.at.Add(-age)})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reportFixture) row(safeguard, rig string, env Env) Health {
	f.t.Helper()
	for _, row := range Verify(context.Background(), env) {
		if row.Safeguard == safeguard && row.Rig == rig {
			return row
		}
	}
	f.t.Fatalf("no row for %s/%s", safeguard, rig)
	return Health{}
}

// TestTheSilenceThresholdsAreSevenFour pins the table: 30 s, 180 s, 360 s and 75 s - three times each
// child's longest legitimate sleep - and rejects any environment knob, because a threshold nobody owns is
// how the silence went unnoticed.
func TestTheSilenceThresholdsAreSevenFour(t *testing.T) {
	want := map[string]time.Duration{
		Compact:  30 * time.Second,
		Watchdog: 180 * time.Second,
		Bridge:   360 * time.Second,
		Rigd:     75 * time.Second,
	}
	for _, safeguard := range Safeguards() {
		if got := DefaultSilentAfter(safeguard); got != want[safeguard] {
			t.Errorf("DefaultSilentAfter(%s) = %s, want %s", safeguard, got, want[safeguard])
		}
	}
	if got := SilentAfter(10 * time.Second); got != 30*time.Second {
		t.Errorf("SilentAfter(10s) = %s, want three times the sleep", got)
	}
}

// TestTheProcessDecidesRunningAgainstStopped is the FIRST clause of 7.2, and the second: a dead process is
// stopped whatever its heartbeat says, and a live process whose beat is missing or stale or names another
// pid is silent.
func TestTheProcessDecidesRunningAgainstStopped(t *testing.T) {
	env := Env{Rigs: []string{"demo"}}

	t.Run("live with a fresh beat", func(t *testing.T) {
		f := newReportFixture(t)
		f.live[Pattern(Compact, "demo")] = []int{77}
		f.beat(Compact, "demo", time.Second, 77)
		got := f.row(Compact, "demo", env)
		if got.State != Running || got.PID != 77 || got.AgeS != 1 {
			t.Errorf("row = %+v, want running with pid 77 and age 1", got)
		}
	})

	t.Run("dead whatever the beat says", func(t *testing.T) {
		f := newReportFixture(t)
		f.beat(Compact, "demo", time.Second, 77)
		got := f.row(Compact, "demo", env)
		if got.State != Stopped {
			t.Errorf("row = %+v, want stopped: the process is gone", got)
		}
		if got.AgeS != 1 {
			t.Errorf("row = %+v, want the last beat's age reported with it", got)
		}
	})

	t.Run("live but the beat names another pid", func(t *testing.T) {
		f := newReportFixture(t)
		f.live[Pattern(Compact, "demo")] = []int{88}
		f.beat(Compact, "demo", time.Second, 77)
		got := f.row(Compact, "demo", env)
		if got.State != Silent {
			t.Errorf("row = %+v, want silent: a restarted child's old beat is not evidence", got)
		}
	})

	t.Run("live and never beat", func(t *testing.T) {
		f := newReportFixture(t)
		f.live[Pattern(Compact, "demo")] = []int{77}
		got := f.row(Compact, "demo", env)
		if got.State != Silent {
			t.Errorf("row = %+v, want silent: there is no signal at all", got)
		}
		if got.AgeS != UnknownAge || got.LastBeatAt != "" {
			t.Errorf("row = %+v, want an unknown age rather than a zero one", got)
		}
	})

	t.Run("no process and no beat", func(t *testing.T) {
		f := newReportFixture(t)
		got := f.row(Compact, "demo", env)
		if got.State != Stopped || got.AgeS != UnknownAge || got.PID != 0 {
			t.Errorf("row = %+v, want stopped with no beat", got)
		}
	})
}

// TestSilentIsStrictlyPastTheThreshold: the boundary is `age > threshold`, so a beat exactly at the
// threshold is still running and a moment past it is not.
func TestSilentIsStrictlyPastTheThreshold(t *testing.T) {
	env := Env{Rigs: []string{"demo"}}

	f := newReportFixture(t)
	f.live[Pattern(Compact, "demo")] = []int{77}
	f.beat(Compact, "demo", 30*time.Second, 77)
	if got := f.row(Compact, "demo", env); got.State != Running {
		t.Errorf("row = %+v, want running at exactly the threshold", got)
	}

	f.beat(Compact, "demo", 30*time.Second+time.Millisecond, 77)
	if got := f.row(Compact, "demo", env); got.State != Silent {
		t.Errorf("row = %+v, want silent past the threshold", got)
	}
}

// TestRigdComputesTheThresholdFromTheFlagsItLaunchedWith: 7.4's owner is rigd, and the thresholds it
// supplies override the defaults - the same child with a 60 s `--every` must be silent only past 180 s.
func TestRigdComputesTheThresholdFromTheFlagsItLaunchedWith(t *testing.T) {
	f := newReportFixture(t)
	f.live[Pattern(Compact, "demo")] = []int{77}
	f.beat(Compact, "demo", 60*time.Second, 77)

	slow := Env{Rigs: []string{"demo"}, Thresholds: map[string]time.Duration{Compact: SilentAfter(60 * time.Second)}}
	if got := f.row(Compact, "demo", slow); got.State != Running {
		t.Errorf("row = %+v, want running: rigd launched it with a 60 s pass", got)
	}
	if got := f.row(Compact, "demo", Env{Rigs: []string{"demo"}}); got.State != Silent {
		t.Errorf("row = %+v, want silent under the 30 s default", got)
	}
}

// TestFailingIsDecidableOnlyByRigd: pgrep cannot count deaths, so the state comes from rigd's own record -
// and `running` rows take their restarts and started_at from it without their state being overridden.
func TestFailingIsDecidableOnlyByRigd(t *testing.T) {
	f := newReportFixture(t)
	env := Env{
		Rigs: []string{"demo"},
		Supervised: []Health{{
			Rig: "demo", Safeguard: Compact, State: Failing, Restarts: 3,
			StartedAt: "2026-10-11T11:00:00Z",
		}},
	}
	got := f.row(Compact, "demo", env)
	if got.State != Failing || got.Restarts != 3 || got.StartedAt != "2026-10-11T11:00:00Z" {
		t.Errorf("row = %+v, want the failing state and rigd's own fields", got)
	}

	// A live, beating child is RUNNING even while rigd records it as having failed before: the process and
	// the heartbeat are judged fresh, and `failing` is what a dead-and-retrying child is.
	f.live[Pattern(Compact, "demo")] = []int{77}
	f.beat(Compact, "demo", time.Second, 77)
	got = f.row(Compact, "demo", env)
	if got.State != Running {
		t.Errorf("row = %+v, want running: it is alive and beating now", got)
	}
	if got.Restarts != 3 {
		t.Errorf("row = %+v, want the restart count kept", got)
	}
}

// TestTheHeartbeatFileLayoutIsSevenTwo: <LogDir>/heartbeat/<safeguard>-<rig>.json, and `rigd.json` for the
// machine-scoped writer, holding {pid, at} and nothing else.
func TestTheHeartbeatFileLayoutIsSevenTwo(t *testing.T) {
	f := newReportFixture(t)
	if err := WriteHeartbeat(Compact, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHeartbeat(Rigd, ""); err != nil {
		t.Fatal(err)
	}

	rigScoped := filepath.Join(f.dir, "heartbeat", "compact-demo.json")
	if _, err := os.Stat(rigScoped); err != nil {
		t.Errorf("the rig-scoped beat is not at %s: %v", rigScoped, err)
	}
	machine := filepath.Join(f.dir, "heartbeat", "rigd.json")
	if _, err := os.Stat(machine); err != nil {
		t.Errorf("rigd's own beat is not at %s: %v", machine, err)
	}
	if err := WriteHeartbeat(Bridge, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "heartbeat", "bridge.json")); err != nil {
		t.Errorf("the bridge is one per machine, so its beat is bridge.json: %v", err)
	}

	raw, err := os.ReadFile(rigScoped)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Errorf("the heartbeat holds %v, want exactly {pid, at}", fields)
	}
	if fields["pid"] != float64(os.Getpid()) {
		t.Errorf("pid = %v, want this process", fields["pid"])
	}

	beat, ok := ReadHeartbeat(Compact, "demo")
	if !ok || beat.PID != os.Getpid() || !beat.At.Equal(f.at) {
		t.Errorf("ReadHeartbeat = %+v, %v; want this process at the seam's clock", beat, ok)
	}

	// An unreadable file is NOT a fresh beat: it is the absence of one.
	if err := os.WriteFile(filepath.Join(f.dir, "heartbeat", "watchdog-demo.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadHeartbeat(Watchdog, "demo"); ok {
		t.Error("a malformed heartbeat read as a live one")
	}
}

// TestRigdStateIsPrivateAndAddsARigOnce is 7.1's `rigs` list and 3.2's mode: the file is a consent file in a
// later phase, so it is 0600, and `up` may run twice.
func TestRigdStateIsPrivateAndAddsARigOnce(t *testing.T) {
	newReportFixture(t)
	state := AddRig(AddRig(RigdState{}, "demo"), "demo")
	state = AddRig(state, "other")
	if strings.Join(state.Rigs, ",") != "demo,other" {
		t.Fatalf("rigs = %v, want the name added once", state.Rigs)
	}
	if err := WriteRigdState(state); err != nil {
		t.Fatal(err)
	}
	path, err := RigdStatePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	read := ReadRigdState()
	if strings.Join(read.Rigs, ",") != "demo,other" {
		t.Errorf("ReadRigdState = %+v, want the two rigs", read)
	}
	if got := RemoveRig(read, "demo"); strings.Join(got.Rigs, ",") != "other" {
		t.Errorf("RemoveRig = %+v, want demo gone", got.Rigs)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := ReadRigdState(); len(got.Rigs) != 0 {
		t.Errorf("a missing state file answered %+v, want the empty state", got)
	}
}

// TestAlertsLogKeepsTheTransitionsAndTheTail: every transition is appended with its UTC stamp, and the
// reader gets the last lines.
func TestAlertsLogKeepsTheTransitionsAndTheTail(t *testing.T) {
	newReportFixture(t)
	if err := AppendAlert("2026-10-11T11:00:00Z demo compact: stopped, age 31s"); err != nil {
		t.Fatal(err)
	}
	if err := AppendAlert("2026-10-11T11:01:00Z demo watchdog: silent, age 400s"); err != nil {
		t.Fatal(err)
	}
	tail := TailAlerts()
	if len(tail) != 2 || !strings.Contains(tail[1], "watchdog: silent") {
		t.Errorf("tail = %v, want both transitions in order", tail)
	}
	if AlertsTail <= 0 {
		t.Fatalf("AlertsTail = %d", AlertsTail)
	}
}

// TestStateLineIsTheLineSevenFiveRequires: `<rig> <safeguard>: <state>, age <n>s`, with the machine-scoped
// rows carrying no rig and a row with no beat saying so rather than claiming an age of zero.
func TestStateLineIsTheLineSevenFiveRequires(t *testing.T) {
	cases := []struct {
		row  Health
		want string
	}{
		{Health{Rig: "demo", Safeguard: Compact, State: Stopped, AgeS: 31}, "demo compact: stopped, age 31s"},
		{Health{Rig: "demo", Safeguard: Watchdog, State: Silent, AgeS: 400}, "demo watchdog: silent, age 400s"},
		{Health{Rig: "", Safeguard: Bridge, State: Stopped, AgeS: 0}, "bridge: stopped, age 0s"},
		{Health{Rig: "", Safeguard: Rigd, State: Running, AgeS: 1}, "rigd: running, age 1s"},
		{Health{Rig: "demo", Safeguard: Compact, State: Stopped, AgeS: UnknownAge}, "demo compact: stopped, age unknown"},
	}
	for _, c := range cases {
		if got := StateLine(c.row); got != c.want {
			t.Errorf("StateLine(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}

// TestTheReportWalksTheListedRigsAndTheTwoMachineRows: one supervisor and one watchdog per listed rig, and
// exactly one bridge and one rigd, in a stable order.
func TestTheReportWalksTheListedRigsAndTheTwoMachineRows(t *testing.T) {
	newReportFixture(t)
	rows := Verify(context.Background(), Env{Rigs: []string{"demo", "other"}})
	var order []string
	for _, row := range rows {
		order = append(order, strings.TrimSpace(row.Rig+" "+row.Safeguard))
	}
	want := "demo compact,demo watchdog,other compact,other watchdog,bridge,rigd"
	if strings.Join(order, ",") != want {
		t.Errorf("order = %v, want %v", order, want)
	}
	if AllRunning(rows) {
		t.Error("AllRunning said a machine with nothing running is well")
	}
	if len(NotRunning(rows)) != len(rows) {
		t.Errorf("NotRunning = %d rows, want all %d", len(NotRunning(rows)), len(rows))
	}
}
