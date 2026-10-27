// The report's own rules, tested where they live: 7.4's thresholds, 7.2's precedence, the heartbeat file
// layout, agent's state file and the alert log.
//
// EVERY TEST HERE FROZEN-CLOCKS THE SEAM and answers the process table through the seam: nothing in this
// file reads wall time or pgrep, and nothing writes outside a temp directory.
package clientservices

import (
	"context"
	"encoding/json"
	"errors"
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
	root string // the state root every read of this fixture is pointed at
	dir  string // <root>/logs, where the log-derived paths land
	live map[string][]int
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "logs")
	stateDir := t.TempDir()
	fixture := &reportFixture{t: t, at: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), root: root, dir: dir, live: map[string][]int{}}

	previousNow, previousLive := Now, Live
	previousStatePath := AgentStatePath
	t.Cleanup(func() {
		Now, Live = previousNow, previousLive
		AgentStatePath = previousStatePath
	})
	Now = func() time.Time { return fixture.at }
	Live = func(pattern string) ([]int, error) { return fixture.live[pattern], nil }
	AgentStatePath = func() (string, error) { return filepath.Join(stateDir, "agent.json"), nil }
	return fixture
}

// beat writes one heartbeat file with a chosen pid and age.
func (f *reportFixture) beat(safeguard, room string, age time.Duration, pid int) {
	f.t.Helper()
	path := HeartbeatPath(f.root, safeguard, room)
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

func (f *reportFixture) row(safeguard, room string, env Env) Health {
	f.t.Helper()
	env.Root = f.root
	rows, err := Verify(context.Background(), env)
	if err != nil {
		f.t.Fatalf("Verify refused: %v", err)
	}
	for _, row := range rows {
		if row.Safeguard == safeguard && row.Room == room {
			return row
		}
	}
	f.t.Fatalf("no row for %s/%s", safeguard, room)
	return Health{}
}

// TestTheSilenceThresholdsAreSevenFour pins the table - 30 s, 180 s, 30 s, 360 s and 75 s, three times
// each child's longest legitimate sleep - and rejects any environment knob, because a threshold nobody
// owns is how the silence went unnoticed.
func TestTheSilenceThresholdsAreSevenFour(t *testing.T) {
	want := map[string]time.Duration{
		Compact:   30 * time.Second,
		Watchdog:  180 * time.Second,
		Messages:  30 * time.Second,
		RoomGuard: 90 * time.Second,
		Budget:    45 * time.Second,
		Bridge:    360 * time.Second,
		Agent:     75 * time.Second,
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
	env := Env{Rooms: []string{"demo"}}

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

	t.Run("two live matches and the beat names the second", func(t *testing.T) {
		f := newReportFixture(t)
		f.live[Pattern(Compact, "demo")] = []int{66, 77}
		f.beat(Compact, "demo", time.Second, 77)
		got := f.row(Compact, "demo", env)
		if got.State != Running || got.PID != 77 {
			t.Errorf("row = %+v, want running on the beating pid 77 though 66 matches first", got)
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
	env := Env{Rooms: []string{"demo"}}

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

// TestAgentComputesTheThresholdFromTheFlagsItLaunchedWith: 7.4's owner is agent, and the thresholds it
// supplies override the defaults - the same child with a 60 s `--every` must be silent only past 180 s.
func TestAgentComputesTheThresholdFromTheFlagsItLaunchedWith(t *testing.T) {
	f := newReportFixture(t)
	f.live[Pattern(Compact, "demo")] = []int{77}
	f.beat(Compact, "demo", 60*time.Second, 77)

	slow := Env{Rooms: []string{"demo"}, Thresholds: map[string]time.Duration{Compact: SilentAfter(60 * time.Second)}}
	if got := f.row(Compact, "demo", slow); got.State != Running {
		t.Errorf("row = %+v, want running: agent launched it with a 60 s pass", got)
	}
	if got := f.row(Compact, "demo", Env{Rooms: []string{"demo"}}); got.State != Silent {
		t.Errorf("row = %+v, want silent under the 30 s default", got)
	}
}

// TestFailingIsDecidableOnlyByAgent: pgrep cannot count deaths, so the state comes from agent's own record -
// and `running` rows take their restarts and started_at from it without their state being overridden.
func TestFailingIsDecidableOnlyByAgent(t *testing.T) {
	f := newReportFixture(t)
	env := Env{
		Rooms: []string{"demo"},
		Supervised: []Health{{
			Room: "demo", Safeguard: Compact, State: Failing, Restarts: 3,
			StartedAt: "2026-10-11T11:00:00Z",
		}},
	}
	got := f.row(Compact, "demo", env)
	if got.State != Failing || got.Restarts != 3 || got.StartedAt != "2026-10-11T11:00:00Z" {
		t.Errorf("row = %+v, want the failing state and agent's own fields", got)
	}

	// THE RULING OF 2026-10-11 (architect 3fa2125d, board row 72dac9f9, wired as qitem-20261010234706):
	// `failing` BEATS a live process, so the precedence is stopped -> failing -> silent -> running. A child
	// agent has counted three deaths for inside the window reads failing even while its heartbeat is fresh:
	// it is in a crash loop, and agent is the only seat that can count that. The expectation that stood here
	// before - a live, beating child is RUNNING even while agent records it as having failed - was RETIRED
	// with the ruling that changed it, not re-pinned to keep a green run.
	f.live[Pattern(Compact, "demo")] = []int{77}
	f.beat(Compact, "demo", time.Second, 77)
	got = f.row(Compact, "demo", env)
	if got.State != Failing {
		t.Errorf("row = %+v, want failing: agent counted the deaths, and failing beats a live process", got)
	}
	if got.Restarts != 3 {
		t.Errorf("row = %+v, want the restart count kept", got)
	}
}

// TestTheHeartbeatFileLayoutIsSevenTwo: <LogDir>/heartbeat/<safeguard>-<room>.json, and `agent.json` for the
// machine-scoped writer, holding {pid, at} and nothing else.
func TestTheHeartbeatFileLayoutIsSevenTwo(t *testing.T) {
	f := newReportFixture(t)
	if err := WriteHeartbeat(f.root, Compact, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHeartbeat(f.root, Agent, ""); err != nil {
		t.Fatal(err)
	}

	roomScoped := filepath.Join(f.dir, "heartbeat", "compact-demo.json")
	if _, err := os.Stat(roomScoped); err != nil {
		t.Errorf("the room-scoped beat is not at %s: %v", roomScoped, err)
	}
	// The beat is state under the state root, and an explicit mode is what a process umask cannot clamp: this
	// was 0644 - readable by every local user - until the room's whole tree went private.
	if info, err := os.Stat(roomScoped); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("the heartbeat's mode = %v, want 0600", info.Mode().Perm())
	}
	machine := filepath.Join(f.dir, "heartbeat", "agent.json")
	if _, err := os.Stat(machine); err != nil {
		t.Errorf("agent's own beat is not at %s: %v", machine, err)
	}
	if err := WriteHeartbeat(f.root, Bridge, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "heartbeat", "bridge.json")); err != nil {
		t.Errorf("the bridge is one per machine, so its beat is bridge.json: %v", err)
	}

	raw, err := os.ReadFile(roomScoped)
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

	beat, ok := ReadHeartbeat(f.root, Compact, "demo")
	if !ok || beat.PID != os.Getpid() || !beat.At.Equal(f.at) {
		t.Errorf("ReadHeartbeat = %+v, %v; want this process at the seam's clock", beat, ok)
	}

	// An unreadable file is NOT a fresh beat: it is the absence of one.
	if err := os.WriteFile(filepath.Join(f.dir, "heartbeat", "watchdog-demo.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadHeartbeat(f.root, Watchdog, "demo"); ok {
		t.Error("a malformed heartbeat read as a live one")
	}
}

// TestAgentStateIsPrivateAndAddsARoomOnce is 7.1's `rooms` list and 3.2's mode: the file is a consent file in a
// later phase, so it is 0600, and `up` may run twice.
func TestAgentStateIsPrivateAndAddsARoomOnce(t *testing.T) {
	newReportFixture(t)
	state := AddRoom(AddRoom(AgentState{}, "demo"), "demo")
	state = AddRoom(state, "other")
	if strings.Join(state.Rooms, ",") != "demo,other" {
		t.Fatalf("rooms = %v, want the name added once", state.Rooms)
	}
	if err := WriteAgentState(state); err != nil {
		t.Fatal(err)
	}
	path, err := AgentStatePath()
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

	read, err := ReadAgentState()
	if err != nil {
		t.Fatalf("ReadAgentState: %v", err)
	}
	if strings.Join(read.Rooms, ",") != "demo,other" {
		t.Errorf("ReadAgentState = %+v, want the two rooms", read)
	}
	if got := RemoveRoom(read, "demo"); strings.Join(got.Rooms, ",") != "other" {
		t.Errorf("RemoveRoom = %+v, want demo gone", got.Rooms)
	}
	if err := os.WriteFile(path, []byte("not json{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A FILE THAT IS THERE AND COULD NOT BE READ IS AN ERROR, not the empty state: the difference decides
	// whether the three callers that WRITE this list back may write at all.
	if _, err := ReadAgentState(); err == nil {
		t.Error("an unreadable state file answered no error; a writer would take the empty list for the truth")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A MISSING FILE IS NOT AN ERROR: a machine whose agent has never run has no rooms to name.
	if got, err := ReadAgentState(); err != nil || len(got.Rooms) != 0 {
		t.Errorf("a missing state file answered %+v (err %v), want the empty state and no error", got, err)
	}
}

// TestAlertsLogKeepsTheTransitionsAndTheTail: every transition is appended with its UTC stamp, and the
// reader gets the last lines.
func TestAlertsLogKeepsTheTransitionsAndTheTail(t *testing.T) {
	f := newReportFixture(t)
	if err := AppendAlert(f.root, "2026-10-11T11:00:00Z demo compact: stopped, age 31s"); err != nil {
		t.Fatal(err)
	}
	if err := AppendAlert(f.root, "2026-10-11T11:01:00Z demo watchdog: silent, age 400s"); err != nil {
		t.Fatal(err)
	}
	tail := TailAlerts(f.root)
	if len(tail) != 2 || !strings.Contains(tail[1], "watchdog: silent") {
		t.Errorf("tail = %v, want both transitions in order", tail)
	}
	if AlertsTail <= 0 {
		t.Fatalf("AlertsTail = %d", AlertsTail)
	}
}

// TestStateLineIsTheLineSevenFiveRequires: `<room> <safeguard>: <state>, age <n>s`, with the machine-scoped
// rows carrying no room and a row with no beat saying so rather than claiming an age of zero.
func TestStateLineIsTheLineSevenFiveRequires(t *testing.T) {
	cases := []struct {
		row  Health
		want string
	}{
		{Health{Room: "demo", Safeguard: Compact, State: Stopped, AgeS: 31}, "demo compact: stopped, age 31s"},
		{Health{Room: "demo", Safeguard: Watchdog, State: Silent, AgeS: 400}, "demo watchdog: silent, age 400s"},
		{Health{Room: "", Safeguard: Bridge, State: Stopped, AgeS: 0}, "bridge: stopped, age 0s"},
		{Health{Room: "", Safeguard: Agent, State: Running, AgeS: 1}, "agent: running, age 1s"},
		{Health{Room: "demo", Safeguard: Compact, State: Stopped, AgeS: UnknownAge}, "demo compact: stopped, age unknown"},
	}
	for _, c := range cases {
		if got := StateLine(c.row); got != c.want {
			t.Errorf("StateLine(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}

// TestTheReportWalksTheListedRoomsAndTheTwoMachineRows: one supervisor, one watchdog and one seat
// deliverer per listed room, and exactly one bridge and one agent, in a stable order.
func TestTheReportWalksTheListedRoomsAndTheTwoMachineRows(t *testing.T) {
	f := newReportFixture(t)
	rows, err := Verify(context.Background(), Env{Root: f.root, Rooms: []string{"demo", "other"}})
	if err != nil {
		t.Fatalf("Verify refused: %v", err)
	}
	var order []string
	for _, row := range rows {
		order = append(order, strings.TrimSpace(row.Room+" "+row.Safeguard))
	}
	want := "demo compact,demo watchdog,demo messages,demo roomguard,demo budget,other compact,other watchdog,other messages,other roomguard,other budget,bridge,agent"
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

// TestTheObligatoryTableAgreesWithTheScopeRule holds 7.8's table against the predicate the heartbeat file's
// name and the row's room are both decided by. Two answers to "does this run once per room" is exactly how a
// per-room service ends up writing a machine-wide heartbeat, and then `doctor` reads one seat's beat for
// every room.
func TestTheObligatoryTableAgreesWithTheScopeRule(t *testing.T) {
	seen := map[string]bool{}
	for _, service := range Obligatory {
		if seen[service.Name] {
			t.Errorf("%s is in the table twice", service.Name)
		}
		seen[service.Name] = true
		if service.Check == nil {
			t.Errorf("%s has no Check: a row the report cannot compute is not a row", service.Name)
		}
		if wantRoom := service.Scope == Room; wantRoom != RoomScoped(service.Name) {
			t.Errorf("%s: table says scope %q, RoomScoped says %v", service.Name, service.Scope, RoomScoped(service.Name))
		}
	}
	for _, name := range Safeguards() {
		if !seen[name] {
			t.Errorf("%s is a safeguard the report lists and the table does not carry", name)
		}
	}
	if len(Obligatory) != len(Safeguards()) {
		t.Errorf("table carries %d rows, the report lists %d safeguards", len(Obligatory), len(Safeguards()))
	}
}

// TestVerifyRefusesWhenTheProcessProbeCannotRun: a pgrep that never ran is NOT "no processes". The walk
// answers NO rows and the probe's own error (7.12) - before this it answered every row `stopped` from a
// process table nobody had read, which is what made `doctor` a false report and `respawn` a second daemon.
func TestVerifyRefusesWhenTheProcessProbeCannotRun(t *testing.T) {
	previous := Live
	Live = func(string) ([]int, error) { return nil, errors.New("pgrep: not found") }
	t.Cleanup(func() { Live = previous })

	rows, err := Verify(context.Background(), Env{Root: t.TempDir(), Rooms: []string{"demo"}})
	if err == nil {
		t.Fatalf("Verify decided %d row(s) from a probe that could not run", len(rows))
	}
	if rows != nil {
		t.Errorf("Verify answered %d row(s) beside its error, want none: a partial report reads as complete", len(rows))
	}
	if !strings.Contains(err.Error(), "pgrep") {
		t.Errorf("error %q does not name the probe", err)
	}
}

// TestARestartedChildIsRunningInsideItsStartGrace: right after agent restarts a child, the heartbeat file
// still names the dead predecessor. That row used to read `silent` (age 0s) and gave every restart a second
// alert; inside one threshold of agent's recorded start it is running, and past it the old rule holds.
func TestARestartedChildIsRunningInsideItsStartGrace(t *testing.T) {
	f := newReportFixture(t)
	f.live[Pattern(Budget, "demo")] = []int{88}
	f.beat(Budget, "demo", 2*time.Second, 77) // the dead predecessor's last beat
	started := func(ago time.Duration) Env {
		return Env{Rooms: []string{"demo"}, Supervised: []Health{{
			Room: "demo", Safeguard: Budget, Restarts: 1, StartedAt: f.at.Add(-ago).UTC().Format(time.RFC3339),
		}}}
	}
	if got := f.row(Budget, "demo", started(time.Second)); got.State != Running || got.PID != 88 || got.Restarts != 1 {
		t.Errorf("row = %+v, want running pid 88 with the restart counted", got)
	}
	if got := f.row(Budget, "demo", started(SilentAfter(BudgetEvery)+time.Second)); got.State != Silent {
		t.Errorf("row = %+v, want silent: the new child never beat within its threshold", got)
	}
	// doctor has no record and no resume, so it keeps judging without grace.
	if got := f.row(Budget, "demo", Env{Rooms: []string{"demo"}}); got.State != Silent {
		t.Errorf("row = %+v, want silent without agent's record", got)
	}
}

// TestABeatThatWentStaleDuringAPauseIsNotSilence: after a host suspend every beat is old. Measured from the
// observer's resume (NotBefore) the child is running until one threshold has passed since the resume.
func TestABeatThatWentStaleDuringAPauseIsNotSilence(t *testing.T) {
	f := newReportFixture(t)
	f.live[Pattern(Compact, "demo")] = []int{77}
	f.beat(Compact, "demo", 460*time.Second, 77)
	paused := Env{Rooms: []string{"demo"}, NotBefore: f.at.Add(-time.Second)}
	got := f.row(Compact, "demo", paused)
	if got.State != Running || got.AgeS != 460 {
		t.Errorf("row = %+v, want running with the true age 460", got)
	}
	paused.NotBefore = f.at.Add(-SilentAfter(CompactEvery) - time.Second)
	if got := f.row(Compact, "demo", paused); got.State != Silent {
		t.Errorf("row = %+v, want silent: a full threshold passed since the resume", got)
	}
}
