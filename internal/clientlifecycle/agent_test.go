// agent's supervision of its children, and the two refusals `up` owes the operator.
//
// THE CLOCK AND THE PROCESS TABLE ARE SEAMS, so every case here advances time by hand and answers pgrep
// with a map: no case waits a backoff out, and none may start, signal or read anything of this machine's.
package clientlifecycle

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// childrenFixture is a machine with one listed room, a fake clock, a fake process table and a recorder for
// every spawn.
type childrenFixture struct {
	*healthFixture
	spawned  [][]string
	children []chan struct{}
	manager  *agentManager
	logged   []string
}

func newChildrenFixture(t *testing.T, rigs ...string) *childrenFixture {
	t.Helper()
	fixture := &childrenFixture{healthFixture: newHealthFixture(t, rigs)}
	fixture.manager = newAgentManager(func(line string) { fixture.logged = append(fixture.logged, line) })
	// The inherited lifecycle test seams: the spawn is a recorder, the start grace is short, and nothing
	// signals anything.
	restoreAll(t)
	SupervisorStartGrace = time.Millisecond
	return fixture
}

// diesAlways makes every started child exit inside the start grace: the crash loop, without a process.
func (f *childrenFixture) diesAlways() {
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		f.spawned = append(f.spawned, args)
		return diedChild{}, nil
	}
}

// survives makes every started child outlive the grace, and remembers its release so the test can end.
func (f *childrenFixture) survives() {
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		f.spawned = append(f.spawned, args)
		release := make(chan struct{})
		f.children = append(f.children, release)
		return survivedChild{release: release}, nil
	}
	f.t.Cleanup(func() {
		for _, release := range f.children {
			close(release)
		}
	})
}

func (f *childrenFixture) advance(d time.Duration) {
	f.at = f.at.Add(d)
}

// startsOf counts the starts of one child, because one pass starts every child of every listed rig - the
// assertion that matters is about the child whose behaviour the case is driving.
func (f *childrenFixture) startsOf(needle string) int {
	count := 0
	for _, argv := range f.spawned {
		if strings.Join(argv, " ") == needle {
			count++
		}
	}
	return count
}

// compactStarts is the compaction supervisor of the fixture's one listed room.
func (f *childrenFixture) compactStarts() int {
	return f.startsOf("compact-run --room demo")
}

// TestEverySupervisionPassWritesAgentOwnHeartbeat: agent's own row is what a machine with no systemd has
// instead of a supervisor, and it must stay fresh while the cloud is unreachable - so the beat is written by
// the supervision pass and not by the socket.
func TestEverySupervisionPassWritesAgentOwnHeartbeat(t *testing.T) {
	// The lifecycle tests replace the heartbeat writer with a recorder; THIS case is about the real write,
	// so the writer is put back while the clock, the process table and the log directory stay faked.
	writer := clientservices.WriteHeartbeat
	f := newChildrenFixture(t, "demo")
	clientservices.WriteHeartbeat = writer
	f.survives()
	// The process table answers for agent itself: this process, at the pid the real writer stamped.
	f.live[clientservices.Pattern(clientservices.Agent, "")] = []int{os.Getpid()}
	f.manager.pass()
	f.manager.pass()

	beat, ok := clientservices.ReadHeartbeat(clientservices.Agent, "")
	if !ok {
		t.Fatal("agent wrote no heartbeat for itself")
	}
	if beat.PID != os.Getpid() {
		t.Errorf("beat = %+v, want this process", beat)
	}
	if got := row(t, f.manager.rows(), clientservices.Agent, ""); got.State != clientservices.Running {
		t.Errorf("row = %+v, want agent running in its own report", got)
	}
}

// TestTheChildListIsSevenOne: a compaction supervisor, a watchdog and a seat-message deliverer per
// listed rig, and ONE bridge per machine, each with the arguments and the derived threshold 7.4 gives it.
func TestTheChildListIsSevenOne(t *testing.T) {
	specs, err := agentSpecs([]string{"demo", "other"})
	if err != nil {
		t.Fatal(err)
	}
	var described []string
	for _, spec := range specs {
		described = append(described, strings.Join(spec.argv, " ")+" @"+spec.threshold.String())
	}
	want := []string{
		"compact-run --room demo @30s",
		"watchdog demo --watch @3m0s",
		"sync messages --room demo --watch @30s",
		"idle-run --room demo @1m30s",
		"compact-run --room other @30s",
		"watchdog other --watch @3m0s",
		"sync messages --room other --watch @30s",
		"idle-run --room other @1m30s",
		"bridge run @6m0s",
	}
	if strings.Join(described, "|") != strings.Join(want, "|") {
		t.Errorf("children = %v\nwant %v", described, want)
	}
}

// TestAChildThatDiesIsRestartedWithBackoffFromOneSecond: 1 s, doubling to 120 s - asserted as the INSTANTS
// of the starts, because "it restarted" and "it restarted with backoff" are different claims.
func TestAChildThatDiesIsRestartedWithBackoffFromOneSecond(t *testing.T) {
	f := newChildrenFixture(t, "demo")
	f.diesAlways()

	f.manager.pass() // t0: the first start, which dies inside the grace
	if got := f.compactStarts(); got != 1 {
		t.Fatalf("compact starts = %d, want the first start (%v)", got, f.spawned)
	}
	f.manager.pass() // still inside the 1 s wait
	if got := f.compactStarts(); got != 1 {
		t.Errorf("compact starts = %d, want no retry before the first backoff elapses", got)
	}

	f.advance(time.Second)
	f.manager.pass()
	if got := f.compactStarts(); got != 2 {
		t.Fatalf("compact starts = %d, want the retry after 1 s", got)
	}
	f.advance(time.Second) // only 1 s since the retry, whose wait doubled to 2 s
	f.manager.pass()
	if got := f.compactStarts(); got != 2 {
		t.Errorf("compact starts = %d, want the SECOND wait to be 2 s, not 1 s", got)
	}
	f.advance(time.Second)
	f.manager.pass()
	if got := f.compactStarts(); got != 3 {
		t.Errorf("compact starts = %d, want the third start after the doubled wait", got)
	}

	// Only the children of the ONE listed rig, plus the single bridge: nothing else is ever started.
	for _, argv := range f.spawned {
		joined := strings.Join(argv, " ")
		if !strings.HasSuffix(joined, " demo") && joined != "bridge run" && joined != "watchdog demo --watch" && joined != "sync messages --room demo --watch" {
			t.Errorf("spawned %v, which belongs to no listed rig", argv)
		}
	}
}

// TestThreeDeathsInsideTenMinutesIsFailingAndRetriesAtTheCap is 7.1's last sentence, in both directions:
// the state becomes failing, and agent KEEPS retrying - at 120 s.
func TestThreeDeathsInsideTenMinutesIsFailingAndRetriesAtTheCap(t *testing.T) {
	f := newChildrenFixture(t, "demo")
	f.diesAlways()

	f.manager.pass() // death 1 at t0 (backoff 1 s)
	f.advance(time.Second)
	f.manager.pass() // death 2 (backoff 2 s)
	f.advance(2 * time.Second)
	f.manager.pass() // death 3: three inside ten minutes
	if got := f.compactStarts(); got != 3 {
		t.Fatalf("compact starts = %d, want three deaths (%v)", got, f.spawned)
	}

	row := row(t, f.manager.recorded(), clientservices.Compact, "demo")
	if row.State != clientservices.Failing {
		t.Errorf("recorded row = %+v, want failing", row)
	}
	if row.Restarts != 2 {
		t.Errorf("recorded row = %+v, want two restarts after the first start", row)
	}
	// And the published row carries the same state, so the server's alert sees it as the failure it is.
	var published bool
	for _, candidate := range f.manager.take() {
		if candidate.Safeguard == clientservices.Compact && candidate.State == clientservices.Failing {
			published = true
		}
	}
	if !published {
		t.Error("the failing state was not published to the socket")
	}

	// It keeps retrying, but at the cap: nothing until 120 s have passed.
	f.advance(time.Minute)
	f.manager.pass()
	if got := f.compactStarts(); got != 3 {
		t.Errorf("compact starts = %d, want no retry before the 120 s cap", got)
	}
	f.advance(agentRestartCap)
	f.manager.pass()
	if got := f.compactStarts(); got != 4 {
		t.Errorf("compact starts = %d, want the retry at the cap", got)
	}
}

// TestALiveChildIsAdoptedNotDuplicated: a detached child can outlive an agent that was killed, and two
// supervisors on one rig would be worse than none. Liveness is read from the same pgrep the report uses, so
// agent and `doctor` cannot disagree.
func TestALiveChildIsAdoptedNotDuplicated(t *testing.T) {
	f := newChildrenFixture(t, "demo")
	f.survives()
	f.running(clientservices.Compact, "demo", 5150)

	f.manager.pass()
	for _, argv := range f.spawned {
		if strings.Contains(strings.Join(argv, " "), "compact-run") {
			t.Errorf("agent spawned a second compaction supervisor: %v", argv)
		}
	}
	if got := row(t, f.manager.rows(), clientservices.Compact, "demo"); got.State != clientservices.Running || got.PID != 5150 {
		t.Errorf("row = %+v, want the adopted process running", got)
	}
}

// TestATransitionIsPublishedToTheSocketAndTheAlertLog: one row per change, latest wins, and the local
// record says the same thing - with the shape 7.9 gives `alerts.log`'s siblings.
func TestATransitionIsPublishedToTheSocketAndTheAlertLog(t *testing.T) {
	f := newChildrenFixture(t, "demo")
	f.survives()
	f.running(clientservices.Compact, "demo", 5150)

	f.manager.pass()
	published := f.manager.take()
	if len(published) == 0 {
		t.Fatal("the first pass published no rows")
	}
	if len(f.manager.take()) != 0 {
		t.Error("a published row was handed out twice")
	}

	// The supervisor dies: the transition is published again, and appended to alerts.log.
	f.live[clientservices.Pattern(clientservices.Compact, "demo")] = nil
	f.manager.pass()
	changed := f.manager.take()
	if len(changed) == 0 {
		t.Fatal("the change was not published")
	}
	var stopped bool
	for _, row := range changed {
		if row.Safeguard == clientservices.Compact && row.State == clientservices.Stopped {
			stopped = true
		}
	}
	if !stopped {
		t.Errorf("published = %+v, want the compaction supervisor stopped", changed)
	}

	tail := clientservices.TailAlerts()
	var lines []string
	for _, line := range tail {
		lines = append(lines, line)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "demo compact: stopped") {
		t.Errorf("alerts.log tail = %q, want the transition", joined)
	}
	if !strings.Contains(joined, "2026-10-11T") {
		t.Errorf("alerts.log tail = %q, want the UTC stamp", joined)
	}
}

// TestTheSupervisionFactsArePersistedForDoctor: restarts and started_at cannot be read from a heartbeat or
// from pgrep, so agent records them in agent.json - while the process and the heartbeat stay the source for
// the state.
func TestTheSupervisionFactsArePersistedForDoctor(t *testing.T) {
	f := newChildrenFixture(t, "demo")
	f.survives()

	f.manager.pass()
	stored, err := clientservices.ReadAgentState()
	if err != nil {
		t.Fatalf("ReadAgentState: %v", err)
	}
	if len(stored.Rigs) != 1 || stored.Rigs[0] != "demo" {
		t.Fatalf("rigs = %v, want the listed room kept", stored.Rigs)
	}
	var found bool
	for _, row := range stored.Safeguards {
		if row.Safeguard == clientservices.Compact && row.Rig == "demo" {
			found = true
			if row.StartedAt == "" {
				t.Errorf("row = %+v, want started_at recorded", row)
			}
			if row.State != clientservices.Stopped && row.State != clientservices.Silent && row.State != clientservices.Running {
				t.Errorf("row = %+v, want a state from the ruled four", row)
			}
		}
	}
	if !found {
		t.Errorf("agent.json carries no compact row: %+v", stored.Safeguards)
	}
	// The heartbeat of a child agent spawned is the CHILD's to write, so agent's own state must not pretend
	// to have one: no beat was written by the fake child.
	if _, ok := clientservices.ReadHeartbeat(clientservices.Compact, "demo"); ok {
		t.Error("a heartbeat appeared that no child wrote")
	}
}

// TestEveryStepWritesTheCompactHeartbeat is 7.2's "the supervisor writes it per Step": the pass that ACTS
// and the pass that does nothing both beat, because a beat only when something happens cannot tell a dead
// supervisor from a quiet one - the incident itself.
func TestEveryStepWritesTheCompactHeartbeat(t *testing.T) {
	beats := stubHeartbeat(t)
	path := seatLogFile(t, 10, 0)
	sent, interrupted := []string{}, []string{}
	supervisor := supervisorFor(path, "omp", false, &sent, &interrupted)

	supervisor.Step()
	supervisor.Step()
	if len(*beats) != 2 {
		t.Fatalf("beats = %v, want one per pass", *beats)
	}
	for _, beat := range *beats {
		if beat != clientservices.Compact+"/r" {
			t.Errorf("beat = %q, want the compaction supervisor of the supervised rig", beat)
		}
	}
}

// TestUpRefusesWhenACredentialIsMissingWithoutPrintingItsValue is 7.5's "up fails loudly": the variable is
// NAMED, its value never, and the refusal comes before the views.
func TestUpRefusesWhenACredentialIsMissingWithoutPrintingItsValue(t *testing.T) {
	restoreAll(t)
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "super-secret-token-value")

	var ran []string
	ensureDaemon = func(string, io.Writer) (string, error) {
		ran = append(ran, "ensureDaemon")
		return "/tmp/daemon.log", nil
	}
	ensureRig = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		ran = append(ran, "ensureRig")
		return clientcmd.ExitOK
	}
	recordRigFor = func(string, io.Writer) bool { ran = append(ran, "record"); return true }
	startAuthority = func(io.Writer, io.Writer) bool { ran = append(ran, "agent"); return true }
	runWatchView = func(string, io.Writer) { ran = append(ran, "watch") }
	openUI = func(io.Writer) { ran = append(ran, "ui") }
	runHerdr = func(io.Writer, io.Writer) int { ran = append(ran, "herdr"); return 0 }

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"demo", "--team", writeRoomDefinition(t, "demo")}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("up returned %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if !strings.Contains(errOut.String(), "AGENTHUB_URL") {
		t.Errorf("stderr = %q, want the missing variable named", errOut.String())
	}
	if strings.Contains(errOut.String(), "super-secret-token-value") || strings.Contains(out.String(), "super-secret-token-value") {
		t.Errorf("the credential was printed: stdout %q stderr %q", out.String(), errOut.String())
	}
	for _, step := range ran {
		if step == "record" || step == "agent" || step == "watch" || step == "ui" || step == "herdr" {
			t.Errorf("up ran %q after a refused credential: %v", step, ran)
		}
	}
}

// TestUpRefusesAndNamesTheSafeguardsThatDidNotComeUp is the verify-before-success step: agent starts, a
// safeguard does not come up, and `up` exits 3 naming it BEFORE the watch view, the UI and herdr.
func TestUpRefusesAndNamesTheSafeguardsThatDidNotComeUp(t *testing.T) {
	restoreAll(t)
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTHUB_TOKEN", "not-a-real-token")

	var ran []string
	ensureDaemon = func(string, io.Writer) (string, error) {
		ran = append(ran, "ensureDaemon")
		return "/tmp/daemon.log", nil
	}
	ensureRig = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		ran = append(ran, "ensureRig")
		return clientcmd.ExitOK
	}
	recordRigFor = func(string, io.Writer) bool { ran = append(ran, "record"); return true }
	startAuthority = func(io.Writer, io.Writer) bool { ran = append(ran, "agent"); return true }
	waitSafeguards = func(string) ([]clientservices.Health, bool) {
		ran = append(ran, "wait")
		return []clientservices.Health{
			{Rig: "demo", Safeguard: clientservices.Compact, State: clientservices.Stopped, AgeS: 31},
			{Rig: "demo", Safeguard: clientservices.Agent, State: clientservices.Running, AgeS: 1},
		}, false
	}
	runWatchView = func(string, io.Writer) { ran = append(ran, "watch") }
	openUI = func(io.Writer) { ran = append(ran, "ui") }
	runHerdr = func(io.Writer, io.Writer) int { ran = append(ran, "herdr"); return 0 }

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"demo", "--team", writeRoomDefinition(t, "demo")}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("up returned %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if !strings.Contains(errOut.String(), "demo compact: stopped, age 31s") {
		t.Errorf("stderr = %q, want the down row named in doctor's own words", errOut.String())
	}
	if strings.Contains(errOut.String(), "agent: running") {
		t.Errorf("stderr named the agent row, which is running: %q", errOut.String())
	}
	for _, step := range ran {
		if step == "watch" || step == "ui" || step == "herdr" {
			t.Errorf("up reached %q before verifying: %v", step, ran)
		}
	}
	if !strings.Contains(strings.Join(ran, ","), "wait") {
		t.Errorf("up never ran the health check: %v", ran)
	}
}

// TestStartAgentLeavesARunningAuthorityAlone: `up` is the idempotent repair verb, and two agents would be two
// restart authorities.
func TestStartAgentLeavesARunningAuthorityAlone(t *testing.T) {
	restoreAll(t)
	var spawned int
	spawnChild = func([]string, string) (supervisorChild, error) {
		spawned++
		return diedChild{}, nil
	}
	agentRunning = func() int { return 4242 }

	var out, errOut bytes.Buffer
	if !startAgent(&out, &errOut) {
		t.Fatalf("startAgent refused a running agent: %q", errOut.String())
	}
	if spawned != 0 {
		t.Errorf("a running agent was started again (%d spawns)", spawned)
	}
	if !strings.Contains(out.String(), "already running") {
		t.Errorf("stdout = %q, want it to say agent was already running", out.String())
	}
	if !strings.Contains(out.String(), "4242") {
		t.Errorf("stdout = %q, want the pid it found", out.String())
	}
}
