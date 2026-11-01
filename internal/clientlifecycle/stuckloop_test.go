package clientlifecycle

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/seatloop"
)

// loopFixture is a room of one looping seat: the clock is the case's, and every pass's log holds repeats made in
// the last few seconds, so a rung is limited by the cooldown and nothing else.
type loopFixture struct {
	deps     *loopDeps
	now      time.Time
	looping  bool
	seats    []string
	acted    []string
	stopErr  error
	raiseErr error
}

func newLoopFixture(t *testing.T) *loopFixture {
	t.Helper()
	f := &loopFixture{now: time.Date(2026, 11, 1, 17, 30, 0, 0, time.UTC), looping: true, seats: []string{"gemini"}}
	f.deps = &loopDeps{
		root:    t.TempDir(),
		seats:   func(string) []string { return f.seats },
		seatLog: func(_, seat string) (string, string) { return "/log/" + seat, "agy" },
		events: func(string) []seatloop.Event {
			if !f.looping {
				return nil
			}
			var events []seatloop.Event
			for i := 0; i < 8; i++ {
				events = append(events, seatloop.Event{Kind: seatloop.Call, Tool: "manage_task", Args: "status", Label: "status task-249",
					At: f.now.Add(-time.Duration(i+1) * time.Second)})
			}
			return events
		},
		steer: func(_, seat, text string) error {
			f.acted = append(f.acted, "steer "+seat+": "+text)
			return nil
		},
		interrupt: func(_, seat, runtime string) { f.acted = append(f.acted, "interrupt "+seat+" "+runtime) },
		stop: func(_, seat string) error {
			f.acted = append(f.acted, "stop "+seat)
			return f.stopErr
		},
		escalate: func(_ string, finding stuckFinding, _ time.Duration) (string, error) {
			f.acted = append(f.acted, "raise "+finding.ID)
			return "q1", f.raiseErr
		},
		now: func() time.Time { return f.now },
		log: func(text string) { f.acted = append(f.acted, "log "+text) },
	}
	return f
}

func (f *loopFixture) pass() { loopPass(f.deps, seatloop.Defaults(), "demo") }

func (f *loopFixture) actions() []string {
	var kept []string
	for _, action := range f.acted {
		if !strings.HasPrefix(action, "log ") {
			kept = append(kept, strings.SplitN(action, ":", 2)[0])
		}
	}
	return kept
}

func TestTheLadderEscalatesOneRungPerCooldownAndStopsAtItsBound(t *testing.T) {
	f := newLoopFixture(t)
	f.pass()
	if got := strings.Join(f.actions(), ","); got != "steer gemini" {
		t.Fatalf("rung 1 took %q", got)
	}
	if text := f.acted[0]; !strings.Contains(text, "status task-249") || !strings.Contains(text, "end your turn") {
		t.Errorf("the steer does not say what was repeated and to stop: %s", text)
	}
	// Inside the cooldown, however many passes run, nothing more is done.
	for i := 0; i < 7; i++ {
		f.now = f.now.Add(15 * time.Second)
		f.pass()
	}
	if got := len(f.actions()); got != 1 {
		t.Fatalf("%d actions inside one cooldown: %v", got, f.actions())
	}
	f.now = f.now.Add(30 * time.Second)
	f.pass()
	if got := strings.Join(f.actions(), ","); got != "steer gemini,interrupt gemini agy,steer gemini" {
		t.Fatalf("rung 2 took %q", got)
	}
	f.now = f.now.Add(seatloop.DefaultCooldown)
	f.pass()
	if got := strings.Join(f.actions()[3:], ","); got != "stop gemini,raise gemini" {
		t.Fatalf("rung 3 took %q", got)
	}
	record := seatloop.Load(f.deps.root, "demo")["gemini"]
	if !record.Stopped || !record.Raised || !strings.Contains(record.Status(), "loop=STOPPED") || !strings.Contains(record.Status(), "status task-249") {
		t.Fatalf("the alert is not in the record: %+v", record)
	}
	// A stopped seat is not live, so the guard has nothing to do, and the record stays for the status verbs.
	f.seats = nil
	before := len(f.actions())
	for i := 0; i < 50; i++ {
		f.now = f.now.Add(seatloop.DefaultCooldown)
		f.pass()
	}
	if got := len(f.actions()); got != before {
		t.Fatalf("the guard went on acting on a stopped seat: %v", f.actions())
	}
	if !seatloop.Load(f.deps.root, "demo")["gemini"].Stopped {
		t.Fatal("the alert did not stay")
	}
}

func TestAtTheTopRungTheStopIsRetriedPerCooldownAndTheOwnerToldOnce(t *testing.T) {
	f := newLoopFixture(t)
	f.stopErr = errors.New("daemon away")
	for i := 0; i < 5; i++ {
		f.pass()
		f.now = f.now.Add(seatloop.DefaultCooldown)
	}
	stops, raises := 0, 0
	for _, action := range f.actions() {
		if action == "stop gemini" {
			stops++
		}
		if action == "raise gemini" {
			raises++
		}
	}
	if stops != 3 || raises != 1 {
		t.Fatalf("stops %d (want 3: passes 3, 4 and 5), raises %d (want 1): %v", stops, raises, f.actions())
	}
	if got := seatloop.Load(f.deps.root, "demo")["gemini"].Status(); !strings.Contains(got, "stop-refused") {
		t.Errorf("a refused stop must read as refused: %s", got)
	}
}

func TestASeatThatStoppedRepeatingStaysQuietAndIsForgottenAfterAWindow(t *testing.T) {
	f := newLoopFixture(t)
	f.pass()
	f.looping = false
	f.now = f.now.Add(seatloop.DefaultCooldown + time.Second)
	f.pass()
	if got := len(f.actions()); got != 1 {
		t.Fatalf("it acted on a seat that stopped: %v", f.actions())
	}
	if len(seatloop.Load(f.deps.root, "demo")) != 1 {
		t.Fatal("the record went before the window closed")
	}
	f.now = f.now.Add(seatloop.DefaultWindow)
	f.pass()
	if len(seatloop.Load(f.deps.root, "demo")) != 0 {
		t.Fatal("a seat clear for a whole window was not forgotten")
	}
	f.looping = true
	f.pass()
	if got := f.actions(); got[len(got)-1] != "steer gemini" {
		t.Fatalf("a later loop must start at rung 1: %v", got)
	}
}

func TestAStuckLoopEscalationReadsAsWhatItIs(t *testing.T) {
	finding := stuckFinding{Kind: stuckLoopKind, ID: "gemini", Reason: "it repeated x"}
	if got := stuckEscalationSubject(finding); !strings.Contains(got, "stopped for looping") {
		t.Errorf("subject %q", got)
	}
	if got := stuckMessage(finding); !strings.Contains(got, "stopped for looping") {
		t.Errorf("message %q", got)
	}
}

func TestTheVerbParsesInTheOrderAgentLaunchesIt(t *testing.T) {
	f := newLoopFixture(t)
	f.looping = false
	saved := loopDepsFor
	defer func() { loopDepsFor = saved }()
	loopDepsFor = func(string, io.Writer) *loopDeps { return f.deps }
	var out, errs bytes.Buffer
	argv := loopGuardArgv("demo", t.TempDir())
	argv = argv[1:] // the verb's own name
	for i, arg := range argv {
		if arg == "--every" {
			argv = append(argv[:i], argv[i+2:]...) // one pass: a case must not sleep
			break
		}
	}
	if code := (stuckCommand{}).Run(nil, argv, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if !strings.HasPrefix(strings.Join(loopGuardArgv("demo", "/r"), " "), "stuck demo --loops --every 15s") {
		t.Errorf("the command line left the pattern's order: %v", loopGuardArgv("demo", "/r"))
	}
	if pattern := clientservices.Pattern(clientservices.LoopGuard, "demo"); !strings.Contains(pattern, "stuck demo --loops") {
		t.Errorf("pattern %q", pattern)
	}
}

func TestADryRunSaysWhatItWouldDoAndDoesNothing(t *testing.T) {
	f := newLoopFixture(t)
	var out bytes.Buffer
	printLoopPlan(f.deps, seatloop.Defaults(), "demo", &out)
	if !strings.Contains(out.String(), "would take rung 1") {
		t.Errorf("plan: %s", out.String())
	}
	if len(f.actions()) != 0 || len(seatloop.Load(f.deps.root, "demo")) != 0 {
		t.Errorf("a dry run acted or wrote: %v", f.actions())
	}
}

// A guard left by `up --room-only` leaves with its room, and one under agent does not.
func TestAGuardWithExitWithRoomLeavesWhenTheRoomIsGone(t *testing.T) {
	f := newLoopFixture(t)
	f.seats = nil
	saved := loopDepsFor
	defer func() { loopDepsFor = saved }()
	loopDepsFor = func(string, io.Writer) *loopDeps { return f.deps }
	savedBeat := clientservices.WriteHeartbeat
	defer func() { clientservices.WriteHeartbeat = savedBeat }()
	beats := 0
	clientservices.WriteHeartbeat = func(string, string, string) error { beats++; return nil }
	var out, errs bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runStuckLoops("demo", f.deps.root, time.Millisecond, true, false, false, nil, &out, &errs)
	}()
	select {
	case code := <-done:
		if code != 0 || beats != loopGonePasses {
			t.Fatalf("exit %d after %d beats, want 0 after %d", code, beats, loopGonePasses)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the guard did not leave with its room")
	}
}

// --room-only starts the guard and nothing else of the safeguards, and says what the room runs without.
func TestRoomOnlyStartsTheGuardAndNotAgent(t *testing.T) {
	root := t.TempDir()
	savedLive, savedSpawn := clientservices.Live, spawnChild
	defer func() { clientservices.Live, spawnChild = savedLive, savedSpawn }()
	clientservices.Live = func(string) ([]int, error) { return nil, nil }
	var spawned [][]string
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		spawned = append(spawned, args)
		return nil, errors.New("not in a case")
	}
	var out, errs bytes.Buffer
	startLoopGuard("demo", root, &out, &errs)
	if len(spawned) != 1 || !strings.HasPrefix(strings.Join(spawned[0], " "), "stuck demo --loops --every 15s --exit-with-room") {
		t.Fatalf("spawned %v", spawned)
	}
	for _, want := range []string{"NO loop guard", "no compaction supervisor", "4genteam up demo"} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("the warning does not say %q: %s", want, errs.String())
		}
	}
	// A guard that is already there is left alone, so running the verb again on a live room starts nothing.
	clientservices.Live = func(string) ([]int, error) { return []int{77}, nil }
	spawned = nil
	out.Reset()
	startLoopGuard("demo", root, &out, &errs)
	if len(spawned) != 0 || !strings.Contains(out.String(), "already running, pid 77") {
		t.Fatalf("spawned %v, said %s", spawned, out.String())
	}
}

// THE ROOMS THAT MUST STAY STOPPED: agent builds children only for the rooms agent.json lists, so a full
// `up aa2-gemini` adds aa2-gemini and starts nothing for aa2-rs or aa2-rs-peak.
func TestAgentStartsNothingForARoomItDoesNotList(t *testing.T) {
	for _, spec := range agentSpecs([]string{"aa2-gemini", "4genthub-min"}, "/r") {
		joined := strings.Join(spec.argv, " ")
		if strings.Contains(joined, "aa2-rs") || spec.room == "aa2-rs" || spec.room == "aa2-rs-peak" {
			t.Errorf("a child for an unlisted room: %s", joined)
		}
	}
	guards := 0
	for _, spec := range agentSpecs([]string{"aa2-gemini"}, "/r") {
		if spec.safeguard == clientservices.LoopGuard {
			guards++
			if strings.Join(spec.argv[:3], " ") != "stuck aa2-gemini --loops" {
				t.Errorf("agent's guard: %v", spec.argv)
			}
		}
	}
	if guards != 1 {
		t.Errorf("%d loop guards for one room", guards)
	}
}
