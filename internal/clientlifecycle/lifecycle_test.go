package clientlifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// helpOf runs one verb with --help and returns what it printed.
func helpOf(t *testing.T, name, flag string) (int, string, string) {
	t.Helper()
	for _, command := range Commands() {
		if command.Name() != name {
			continue
		}
		var out, errOut bytes.Buffer
		code := command.Run(context.Background(), []string{flag}, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	t.Fatalf("no command named %q is registered", name)
	return 0, "", ""
}

// TestHelpIsHelpForEveryLifecycleVerb is the Python test of the same name: a help flag after ANY verb
// prints that verb's own usage, exits 0, and does nothing else. The fault it pins is that `--help` used
// to be read as the RIG of a lifecycle verb, so `compact --help` started a supervisor whose child died
// instantly while the verb still printed a pid and returned 0.
func TestHelpIsHelpForEveryLifecycleVerb(t *testing.T) {
	for _, command := range Commands() {
		for _, flag := range []string{"-h", "--help"} {
			code, out, errOut := helpOf(t, command.Name(), flag)
			if code != clientcmd.ExitOK {
				t.Errorf("%s %s: exit %d, want 0 (stderr: %q)", command.Name(), flag, code, errOut)
			}
			if !strings.Contains(out, "4genteam "+command.Name()) {
				t.Errorf("%s %s: the help did not name the verb: %q", command.Name(), flag, out)
			}
			if strings.Contains(out, "pid") {
				t.Errorf("%s %s: printed a pid while asked for help: %q", command.Name(), flag, out)
			}
		}
	}
}

// stubHeartbeat replaces the heartbeat write for the test and restores it afterwards, so a Step that
// beats cannot write into the machine's own log directory. The recorder is returned for a test that wants
// to assert the beat happened.
func stubHeartbeat(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	previous := clientservices.WriteHeartbeat
	clientservices.WriteHeartbeat = func(root, safeguard, rig string) error {
		calls = append(calls, safeguard+"/"+rig)
		return nil
	}
	t.Cleanup(func() { clientservices.WriteHeartbeat = previous })
	return &calls
}

// restoreAll puts every seam back after a test that replaces it.
func restoreAll(t *testing.T) {
	t.Helper()
	stubHeartbeat(t)
	// The worktrees step touches git and the machine's checkouts. Every case that is not ABOUT it starts
	// from a stub, so a lifecycle assertion never depends on the repository the test binary was built
	// in; the two cases that assert the step replace it with their own.
	ensureWorktrees = func(string, clientruntime.RoomSpec, io.Writer, io.Writer) int { return clientcmd.ExitOK }
	daemon, worktrees, rigStep := ensureDaemon, ensureWorktrees, ensureRig
	record := recordRigFor
	authority, wait := startAuthority, waitSafeguards
	ui, watch, herdr := openUI, runWatchView, runHerdr
	spawn, grace := spawnChild, SupervisorStartGrace
	stop, nodes := stopPattern, rigNodes
	rows, authorityUp := rowsFor, agentRunning
	clone, hooks := cloneRowFor, hooksRowFor
	detach, executable := detachRespawn, respawnExecutable
	t.Cleanup(func() {
		ensureDaemon, ensureWorktrees, ensureRig = daemon, worktrees, rigStep
		recordRigFor = record
		startAuthority, waitSafeguards = authority, wait
		openUI, runWatchView, runHerdr = ui, watch, herdr
		spawnChild, SupervisorStartGrace = spawn, grace
		stopPattern, rigNodes = stop, nodes
		rowsFor, agentRunning = rows, authorityUp
		cloneRowFor, hooksRowFor = clone, hooks
		detachRespawn, respawnExecutable = detach, executable
	})
	// The clone and hooks rows read the checkout this binary was built in, so every case that is not ABOUT
	// them starts from a clean checkout, like the worktrees step above: a lifecycle assertion must not depend
	// on whether the operator's shared checkout has a file open.
	cloneRowFor = func(string) []cloneRow { return nil }
	hooksRowFor = func(string) []hooksRow { return nil }
	// A test in this package runs with the peak gate reading NO nodes: the gate then has nothing to
	// decide, and no test here reaches the machine's own daemon, rig or clock. A test about the gate
	// replaces rigNodes with the reading it wants to be judged on.
	rigNodes = func(string) ([]map[string]any, bool, error) { return nil, false, nil }
	// NO TEST WRITES agent.json. The seams that would - recording the room and starting the authority -
	// are replaced with recorders here, so a `up` test can assert the ORDER without touching the machine's
	// own state file.
	recordRigFor = func(string, io.Writer) bool { return true }
	startAuthority = func(string, io.Writer, io.Writer) bool { return true }
	waitSafeguards = func(string, string) ([]clientservices.Health, bool, error) {
		return []clientservices.Health{{Safeguard: clientservices.Agent, State: clientservices.Running}}, true, nil
	}
}

// TestEveryLogPathIsUnderTheResolvedRoot: the supervisor's log, the bridge's log, the message deliverer's
// log and the two guards' logs are all <root>/logs/<name>, for the root the verb resolved - so a verb
// pointed at another tree writes its logs there and nothing under this machine's.
func TestEveryLogPathIsUnderTheResolvedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "another-root")
	want := filepath.Join(root, "logs")
	if got := LogDir(root); got != want {
		t.Errorf("LogDir(%q) = %q, want %q", root, got, want)
	}
	for _, c := range []struct {
		name string
		path string
	}{
		{"supervisor", LogPath("demo", root)},
		{"messages", MessagesLogPath("demo", root)},
		{"bridge", BridgeLogPath(root)},
		{"idle guard", IdleLogPath("demo", root)},
		{"budget guard", BudgetLogPath("demo", root)},
		{"agent", AgentLogPath(root)},
		{"watchdog child", WatchdogLogPath("demo", root)},
	} {
		if filepath.Dir(c.path) != want {
			t.Errorf("%s log = %q, want it under %q", c.name, c.path, want)
		}
	}
}

// TestHelpAfterAVerbNeverRunsALifecycleAction is the other half of the same fault, asserted as the
// mechanism: nothing the verb would do may happen while it is answering help.
func TestHelpAfterAVerbNeverRunsALifecycleAction(t *testing.T) {
	var ran []string
	restoreAll(t)
	ensureDaemon = func(string, io.Writer) (string, error) { return "", nil }
	spawnChild = func([]string, string) (supervisorChild, error) {
		ran = append(ran, "spawn")
		return diedChild{}, nil
	}
	recordRigFor = func(string, io.Writer) bool { ran = append(ran, "record"); return true }
	startAuthority = func(string, io.Writer, io.Writer) bool { ran = append(ran, "agent"); return true }
	openUI = func(io.Writer) {}
	runWatchView = func(string, io.Writer) { ran = append(ran, "watch") }
	runHerdr = func(io.Writer, io.Writer) int { ran = append(ran, "herdr"); return 0 }

	var out, errOut bytes.Buffer
	if code := (compactCommand{}).Run(context.Background(), []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("compact --help exited %d", code)
	}
	if code := (upCommand{}).Run(context.Background(), []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("up --help exited %d", code)
	}
	if len(ran) != 0 {
		t.Fatalf("asking for help ran a lifecycle action: %v", ran)
	}
}

// TestUpInitialisesEveryServiceInOrder is the Python test of the same name, restated for 7.1: the daemon,
// the rig, the room recorded as meant-to-run, agent (the single restart authority),
// the same health check `doctor` makes, the watch view, the UI, then herdr.
//
// THE BRIDGE AND THE SUPERVISOR ARE NOT IN THIS LIST ANY MORE, and that is the ruling: `up` starts agent,
// and agent starts them. A start path here would be a second mechanism for one safeguard.
func TestUpInitialisesEveryServiceInOrder(t *testing.T) {
	var ran []string
	restoreAll(t)
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTHUB_TOKEN", "not-a-real-token")
	t.Setenv("AGENTHUB_CONNECTOR_TOKEN", "not-a-real-token")
	ensureDaemon = func(string, io.Writer) (string, error) {
		ran = append(ran, "ensureDaemon")
		return "/tmp/daemon.log", nil
	}
	ensureWorktrees = func(string, clientruntime.RoomSpec, io.Writer, io.Writer) int {
		ran = append(ran, "ensureWorktrees")
		return clientcmd.ExitOK
	}
	ensureRig = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		ran = append(ran, "ensureRig")
		return clientcmd.ExitOK
	}
	recordRigFor = func(string, io.Writer) bool { ran = append(ran, "recordRig"); return true }
	startAuthority = func(string, io.Writer, io.Writer) bool { ran = append(ran, "startAgent"); return true }
	waitSafeguards = func(string, string) ([]clientservices.Health, bool, error) {
		ran = append(ran, "waitSafeguards")
		return []clientservices.Health{{Safeguard: clientservices.Agent, State: clientservices.Running}}, true, nil
	}
	runWatchView = func(string, io.Writer) { ran = append(ran, "watch") }
	openUI = func(io.Writer) { ran = append(ran, "openUI") }
	runHerdr = func(io.Writer, io.Writer) int { ran = append(ran, "herdr"); return 0 }
	t.Setenv("HERDR_ENV", "")

	var out, errOut bytes.Buffer
	if code := (upCommand{}).Run(context.Background(), []string{"my-rig", "--team", writeRoomDefinition(t, "my-rig")}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("up returned %d (stderr: %q)", code, errOut.String())
	}
	want := []string{"ensureDaemon", "ensureWorktrees", "ensureRig", "recordRig", "startAgent", "waitSafeguards", "watch", "openUI", "herdr"}
	if strings.Join(ran, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", ran, want)
	}
}

// writeRoomDefinition writes the smallest team.json that can start a room: the room, one seat, and the two
// empty collections the definition may carry. `up` must read its seats from here and nowhere else.
func writeRoomDefinition(t *testing.T, room string) string {
	t.Helper()
	directory := t.TempDir()
	body := `{"room":{"slug":"` + room + `"},` +
		`"seats":[{"seat_key":"lead","seat_type":"developer","runtime":"omp"}],` +
		`"links":[],"seat_overlays":{}}`
	if err := os.WriteFile(filepath.Join(directory, "team.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return directory
}

// A definition that names a different room is a caller error, and it is reported BEFORE the machine is
// reached: `up other --team DIR` must not start a daemon or touch a seat on the way to saying so.
//
// Its code is 3, not the CLI-wide ExitUsage (2), because 2 is a PARTIAL START for this verb (design §2.5).
func TestUpRefusesADefinitionThatNamesAnotherRoom(t *testing.T) {
	restoreAll(t)
	var started []string
	ensureDaemon = func(string, io.Writer) (string, error) { started = append(started, "daemon"); return "", nil }
	ensureRig = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		started = append(started, "room")
		return clientcmd.ExitOK
	}

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"other-rig", "--team", writeRoomDefinition(t, "my-rig")}, &out, &errOut)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, errOut.String())
	}
	if len(started) != 0 {
		t.Errorf("a wrong room name reached the machine: %v", started)
	}
	if !strings.Contains(errOut.String(), "my-rig") {
		t.Errorf("stderr did not name the room the definition defines: %q", errOut.String())
	}
}

// --room-only is an INSTRUMENT, and this is the property that makes it one: the room's own verdict is the
// exit code, and nothing after the room runs - no agent, no bridge wait, no watch
// view, no UI, no herdr. A run whose red could come from any of those is not evidence about the room.
func TestUpRoomOnlyReturnsTheRoomsVerdictAndStopsThere(t *testing.T) {
	for _, c := range []struct {
		name string
		room int
		want int
	}{
		{"a room that came up", clientcmd.ExitOK, clientcmd.ExitOK},
		{"a room that did not", clientcmd.ExitUnavailable, clientcmd.ExitUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			var ran []string
			restoreAll(t)
			ensureDaemon = func(string, io.Writer) (string, error) {
				ran = append(ran, "ensureDaemon")
				return "/tmp/daemon.log", nil
			}
			ensureRig = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
				ran = append(ran, "ensureRig")
				return c.room
			}
			recordRigFor = func(string, io.Writer) bool { ran = append(ran, "recordRig"); return true }
			startAuthority = func(string, io.Writer, io.Writer) bool { ran = append(ran, "startAgent"); return true }
			waitSafeguards = func(string, string) ([]clientservices.Health, bool, error) {
				ran = append(ran, "waitSafeguards")
				return nil, true, nil
			}
			runWatchView = func(string, io.Writer) { ran = append(ran, "watch") }
			openUI = func(io.Writer) { ran = append(ran, "openUI") }
			runHerdr = func(io.Writer, io.Writer) int { ran = append(ran, "herdr"); return 0 }

			var out, errOut bytes.Buffer
			code := (upCommand{}).Run(context.Background(), []string{"my-rig", "--team", writeRoomDefinition(t, "my-rig"), "--room-only"}, &out, &errOut)
			if code != c.want {
				t.Fatalf("exit %d, want %d (stderr %q)", code, c.want, errOut.String())
			}
			if strings.Join(ran, ",") != "ensureDaemon,ensureRig" {
				t.Errorf("--room-only ran %v, want only the daemon and the room", ran)
			}
		})
	}
}

// TestUpKeepsEachSeatsCwdFromTheRoomsOwnSpec pins the defect measured on 2026-10-17: the live room spec
// carried five distinct per-seat cwds, and `up --team` - which starts the room from the definition and
// lets the runtime WRITE spec.json - replaced them with what the definition says (nothing), moving every
// seat to one directory. The definition wins where it declares a cwd: that is an edit, not a default.
func TestUpKeepsEachSeatsCwdFromTheRoomsOwnSpec(t *testing.T) {
	restoreAll(t)
	root := t.TempDir()
	directory := filepath.Join(root, "rooms", "my-rig")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	live := `{"room":"my-rig","seats":[` +
		`{"seat_key":"lead","cwd":"/work/lead"},` +
		`{"seat_key":"go-dev","cwd":"/work/go-dev"},` +
		`{"seat_key":"reviewer","cwd":"   "}]}`
	if err := os.WriteFile(filepath.Join(directory, "spec.json"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	team := `{"room":{"slug":"my-rig"},` +
		`"seats":[{"seat_key":"lead","seat_type":"developer","runtime":"omp"},` +
		`{"seat_key":"go-dev","seat_type":"developer","runtime":"omp","cwd":"/declared/go-dev"},` +
		`{"seat_key":"fe-dev","seat_type":"developer","runtime":"omp"}],` +
		`"links":[{"from":"lead","kind":"delegates_to","to":"go-dev"},` +
		`{"from":"lead","kind":"delegates_to","to":"fe-dev"}],"seat_overlays":{}}`
	teamDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(teamDir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureDaemon = func(string, io.Writer) (string, error) { return "/tmp/daemon.log", nil }
	var sent clientruntime.RoomSpec
	ensureRig = func(_ string, spec clientruntime.RoomSpec, _ string, _ io.Writer, _ io.Writer) int {
		sent = spec
		return clientcmd.ExitOK
	}

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(),
		[]string{"my-rig", "--team", teamDir, "--state-root", root, "--room-only"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stderr %q)", code, errOut.String())
	}
	got := map[string]string{}
	for _, seat := range sent.Seats {
		value := "<nil>"
		if seat.Cwd != nil {
			value = *seat.Cwd
		}
		got[seat.SeatKey] = value
	}
	want := map[string]string{
		"lead":   "/work/lead",
		"go-dev": "/declared/go-dev",
		"fe-dev": "<nil>",
	}
	for seat, value := range want {
		if got[seat] != value {
			t.Errorf("%s cwd = %q, want %q (all: %v)", seat, got[seat], value, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("seats = %v, want exactly %d", got, len(want))
	}
}

// TestUpStartsARoomFromItsOwnSpecWithNoTeamDefinition: `up ROOM` with no --team is exactly what heal's
// safeguard repair runs (clientheal's Up seam execs `up ROOM`), so a room that already has its record must
// start from it. Before this, the usage gate refused the invocation every time and the safeguard finding was
// reported unfixed - the room's own spec.json is the one way a room names its seats once it exists.
func TestUpStartsARoomFromItsOwnSpecWithNoTeamDefinition(t *testing.T) {
	restoreAll(t)
	root := t.TempDir()
	directory := filepath.Join(root, "rooms", "my-rig")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	live := `{"room":"my-rig","seats":[` +
		`{"seat_key":"lead","seat_type":"developer","runtime":"omp","cwd":"/work/lead"},` +
		`{"seat_key":"go-dev","seat_type":"developer","runtime":"omp","cwd":"/work/go-dev"}],` +
		`"links":[{"from":"lead","kind":"delegates_to","to":"go-dev"}]}`
	if err := os.WriteFile(filepath.Join(directory, "spec.json"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureDaemon = func(string, io.Writer) (string, error) { return "/tmp/daemon.log", nil }
	started := false
	var sent clientruntime.RoomSpec
	ensureRig = func(_ string, spec clientruntime.RoomSpec, _ string, _ io.Writer, _ io.Writer) int {
		started = true
		sent = spec
		return clientcmd.ExitOK
	}

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(),
		[]string{"my-rig", "--state-root", root, "--room-only"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stderr %q)", code, errOut.String())
	}
	if !started {
		t.Fatal("the room was never started")
	}
	got := map[string]string{}
	for _, seat := range sent.Seats {
		value := "<nil>"
		if seat.Cwd != nil {
			value = *seat.Cwd
		}
		got[seat.SeatKey] = value
	}
	want := map[string]string{"lead": "/work/lead", "go-dev": "/work/go-dev"}
	for seat, value := range want {
		if got[seat] != value {
			t.Errorf("%s cwd = %q, want %q (all: %v)", seat, got[seat], value, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("seats = %v, want exactly %d", got, len(want))
	}
	if len(sent.Links) != 1 || sent.Links[0].To != "go-dev" {
		t.Errorf("links = %v, want the one the room's own spec records", sent.Links)
	}
}

// TestStopSignalsAgentAndSaysTheStopIsMachineWide: `stop` is the other half of `up`'s contract, and 7.1
// makes it stop agent - which stops its children - rather than picking safeguards out one by one. Asserted
// as the PATTERN rather than by signalling it: a test that ran the real thing would stop this machine's
// services.
func TestStopSignalsAgentAndSaysTheStopIsMachineWide(t *testing.T) {
	var patterns []string
	restoreAll(t)
	stopPattern = func(pattern string) (bool, error) {
		patterns = append(patterns, pattern)
		return true, nil
	}

	var out, errOut bytes.Buffer
	if code := (stopCommand{}).Run(context.Background(), []string{"my-rig"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("stop returned %d (stderr %q)", code, errOut.String())
	}
	want := []string{clientservices.Pattern(clientservices.Agent, "")}
	if strings.Join(patterns, "|") != strings.Join(want, "|") {
		t.Errorf("signalled %v, want %v", patterns, want)
	}
	if !strings.Contains(out.String(), "agent: stopped") {
		t.Errorf("stdout = %q, want it to report agent stopped", out.String())
	}

	// No rig means no guess, and nothing may be signalled while the verb decides that.
	patterns = nil
	out.Reset()
	if code := (stopCommand{}).Run(context.Background(), nil, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("stop with no rig returned %d, want %d", code, clientcmd.ExitUsage)
	}
	// Help is the other case where a lifecycle action must not run.
	if code := (stopCommand{}).Run(context.Background(), []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Errorf("stop --help returned %d, want %d", code, clientcmd.ExitOK)
	}
	if len(patterns) != 0 {
		t.Errorf("stop signalled %v without a rig to name", patterns)
	}
}

// diedChild is a supervisor that was already dead when the start check ran - what `--room --help` did.
type diedChild struct{}

func (diedChild) Pid() int    { return 4242 }
func (diedChild) Wait() error { return &ChildError{Status: 2} }

// survivedChild outlives the start check, which is what a supervisor does: its Wait blocks until the
// test releases it, so the start grace is what decides.
type survivedChild struct{ release chan struct{} }

func (survivedChild) Pid() int { return 4243 }

func (c survivedChild) Wait() error {
	<-c.release
	return nil
}

// TestCompactSpawnsNothingAndRefusesWithoutTheAuthority is 7.1's removed path, asserted from both sides:
// `compact` must not start a supervisor (agent does), and with no agent running it must SAY so instead of
// returning 0 - which is exactly the silence the owner's incident was made of.
func TestCompactSpawnsNothingAndRefusesWithoutTheAuthority(t *testing.T) {
	var spawned []string
	restoreAll(t)
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		spawned = append(spawned, strings.Join(args, " "))
		return diedChild{}, nil
	}
	agentRunning = func() (int, error) { return 0, nil }

	var out, errOut bytes.Buffer
	code := (compactCommand{}).Run(context.Background(), []string{"some-rig"}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("compact with no agent returned %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if len(spawned) != 0 {
		t.Errorf("compact spawned %v, and agent is the only restart authority", spawned)
	}
	if !strings.Contains(errOut.String(), "agent is not running") || !strings.Contains(errOut.String(), "4genteam up some-rig") {
		t.Errorf("the refusal did not name agent and the repair verb: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "4genteam room compact some-rig") {
		t.Errorf("the refusal did not name the runtime's own compact verb: %q", errOut.String())
	}
}

// TestCompactReportsTheRowWhenTheAuthorityRuns is the control: with agent alive, `compact` is a read that
// reports the supervisor's row rather than a start.
func TestCompactReportsTheRowWhenTheAuthorityRuns(t *testing.T) {
	var spawned []string
	restoreAll(t)
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		spawned = append(spawned, strings.Join(args, " "))
		return diedChild{}, nil
	}
	agentRunning = func() (int, error) { return 4242, nil }
	rowsFor = func(string, string) ([]clientservices.Health, error) {
		return []clientservices.Health{
			{Rig: "some-rig", Safeguard: clientservices.Compact, State: clientservices.Running, PID: 99, AgeS: 3},
			{Rig: "some-rig", Safeguard: clientservices.Watchdog, State: clientservices.Silent, AgeS: 400},
		}, nil
	}

	var out, errOut bytes.Buffer
	code := (compactCommand{}).Run(context.Background(), []string{"some-rig"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("compact with a running supervisor returned %d (stderr: %q)", code, errOut.String())
	}
	if len(spawned) != 0 {
		t.Errorf("compact spawned %v, and agent is the only restart authority", spawned)
	}
	if !strings.Contains(out.String(), "compact supervisor: running") {
		t.Errorf("stdout = %q, want the supervisor reported as running", out.String())
	}
}

// TestCompactNamesTheRowWhenTheSupervisorIsNotRunning: agent alive, its child down - the row is named on
// stderr in the same words `doctor` uses.
func TestCompactNamesTheRowWhenTheSupervisorIsNotRunning(t *testing.T) {
	restoreAll(t)
	agentRunning = func() (int, error) { return 4242, nil }
	rowsFor = func(string, string) ([]clientservices.Health, error) {
		return []clientservices.Health{
			{Rig: "some-rig", Safeguard: clientservices.Compact, State: clientservices.Stopped, AgeS: 31},
		}, nil
	}

	var out, errOut bytes.Buffer
	code := (compactCommand{}).Run(context.Background(), []string{"some-rig"}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("compact with a stopped supervisor returned %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if !strings.Contains(errOut.String(), "some-rig compact: stopped, age 31s") {
		t.Errorf("stderr = %q, want the row named in doctor's own words", errOut.String())
	}
}

// TestEveryLifecycleVerbRequiresARig: the Python defaulted to a rig of the owner's own team. There is no
// such default now, so an omitted name is a usage error that names the verb and the argument.
//
// `up` is the ONE exception to the code, not to the rule: its usage failures are 3, because 2 is a PARTIAL
// START for that verb (design §2.5) and one number cannot mean both.
func TestEveryLifecycleVerbRequiresARig(t *testing.T) {
	for _, name := range []string{"up", "compact", "stop", "status", "log"} {
		for _, command := range Commands() {
			if command.Name() != name {
				continue
			}
			want := clientcmd.ExitUsage
			if name == "up" {
				want = clientcmd.ExitRoomRefused
			}
			var out, errOut bytes.Buffer
			code := command.Run(context.Background(), nil, &out, &errOut)
			if code != want {
				t.Errorf("%s with no rig: exit %d, want %d", name, code, want)
			}
			if !strings.Contains(errOut.String(), "no default rig") {
				t.Errorf("%s with no rig: stderr = %q, want the required-rig rule", name, errOut.String())
			}
		}
	}
}

// TestABlankQuietValueMeansTheDefaultNotAFailedParse is the Python test of the same name: a
// `COMPACT_QUIET_SECONDS=` line in a copied .env is the default, not a crash while the arguments are
// read - which used to take `compact-run` down before it supervised anything.
func TestABlankQuietValueMeansTheDefaultNotAFailedParse(t *testing.T) {
	t.Setenv("COMPACT_QUIET_SECONDS", "")
	var errOut bytes.Buffer
	options, ok := ParseCompactRunOptions([]string{"--room", "my-rig", "--once"}, &errOut)
	if !ok {
		t.Fatalf("a blank COMPACT_QUIET_SECONDS was a parse failure: %q", errOut.String())
	}
	if options.Quiet != 15 {
		t.Errorf("quiet = %d, want the documented default 15", options.Quiet)
	}
}

// TestANonNumericQuietValueRefusesAndNamesIt: blank is forgiven, a typo is not.
func TestANonNumericQuietValueRefusesAndNamesIt(t *testing.T) {
	t.Setenv("COMPACT_QUIET_SECONDS", "soon")
	var errOut bytes.Buffer
	if _, ok := ParseCompactRunOptions([]string{"--room", "my-rig", "--once"}, &errOut); ok {
		t.Fatal("a non-numeric COMPACT_QUIET_SECONDS was accepted")
	}
	if !strings.Contains(errOut.String(), "COMPACT_QUIET_SECONDS") {
		t.Errorf("the refusal did not name the variable: %q", errOut.String())
	}
}

// TestCompactRunStateRootFlag: --state-root names the root; without it the machine's own default is used.
func TestCompactRunStateRootFlag(t *testing.T) {
	var errOut bytes.Buffer
	options, ok := ParseCompactRunOptions([]string{"--room", "my-rig", "--once"}, &errOut)
	if !ok {
		t.Fatalf("the default was refused: %s", errOut.String())
	}
	if want := clientenv.DefaultStateRoot(); options.StateRoot != want {
		t.Errorf("StateRoot = %q, want the machine default %q", options.StateRoot, want)
	}

	options, ok = ParseCompactRunOptions([]string{"--room", "my-rig", "--once", "--state-root", "/from/flag"}, &errOut)
	if !ok {
		t.Fatalf("the flag was refused: %s", errOut.String())
	}
	if options.StateRoot != "/from/flag" {
		t.Errorf("StateRoot = %q, want /from/flag", options.StateRoot)
	}
}

// TestCompactRunRequiresARig: the Python's --room defaulted to the owner's rig.
// test_compact_run_requires_a_rig.
func TestCompactRunRequiresARig(t *testing.T) {
	var errOut bytes.Buffer
	if _, ok := ParseCompactRunOptions([]string{"--once"}, &errOut); ok {
		t.Fatal("compact-run accepted no --room")
	}
	if !strings.Contains(errOut.String(), "no default rig") {
		t.Errorf("the refusal did not state the rule: %q", errOut.String())
	}
}

// daemonRefusal is the runtime's OWN refusal - it answered on the socket and will not serve this request. For a
// status read that means a room it does not know, and the definition is then the only source of its seats (U1).
func daemonRefusal() error {
	return &clientruntime.DaemonRefused{Reason: "cannot read room spec: No such file or directory (os error 2)"}
}

// unreadableRoom is a room reading NOBODY COULD READ - a frame that never parsed, say. The daemon may know the room
// perfectly well, so this process has learned nothing about what is running and must not start anything from the
// definition: StartRoom kills and re-creates a session it finds running, so that start would cycle live agents.
type unreadableRoom struct{}

func (unreadableRoom) Error() string { return "decode error: unexpected end of JSON input" }

// TestSeatStartPlan pins the check-then-start decision, and the stake is the runtime's own semantics:
// StartRoom KILLS AND RE-CREATES a session it finds running (pty::launch_session), so "send the whole spec"
// and "start this one seat" differ by whether a second `up` is a no-op or a room of live agents is cycled.
// The design's observable - a second `up` does NOT move started_at or a pane pid - is this table.
func TestSeatStartPlan(t *testing.T) {
	running := func(seat string) clientruntime.SeatStatus {
		return clientruntime.SeatStatus{Seat: seat, State: "running"}
	}
	stopped := func(seat string) clientruntime.SeatStatus {
		return clientruntime.SeatStatus{Seat: seat, State: "stopped"}
	}

	for _, tc := range []struct {
		name      string
		reported  []clientruntime.SeatStatus
		readErr   error
		wantStart []string
		wantSend  bool
	}{
		{
			name:     "a room the daemon does not know: the definition is the only source of its seats",
			readErr:  daemonRefusal(),
			wantSend: true,
		},
		{
			name:     "an answer nobody could read: nothing is learned, so nothing is sent",
			readErr:  unreadableRoom{},
			wantSend: false,
		},
		{
			name:     "a reading with no seats at all: same, the definition starts the room",
			reported: nil,
			wantSend: true,
		},
		{
			name:     "every seat stopped: nothing is running, so nothing can be cycled",
			reported: []clientruntime.SeatStatus{stopped("a"), stopped("b"), stopped("c")},
			wantSend: true,
		},
		{
			name:      "every seat already running: nothing is started and nothing is sent",
			reported:  []clientruntime.SeatStatus{running("a"), running("b"), running("c")},
			wantStart: nil,
			wantSend:  false,
		},
		{
			name:      "one seat down: that seat only, so its running siblings are left alone",
			reported:  []clientruntime.SeatStatus{running("a"), stopped("b"), running("c")},
			wantStart: []string{"b"},
			wantSend:  false,
		},
		{
			name:      "the runtime spells the state its own way: Running still counts as up",
			reported:  []clientruntime.SeatStatus{{Seat: "a", State: "Running"}, stopped("b")},
			wantStart: []string{"b"},
			wantSend:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, send, _ := seatStartPlan(tc.reported, tc.readErr)
			if send != tc.wantSend {
				t.Errorf("sendSpec = %v, want %v", send, tc.wantSend)
			}
			if strings.Join(start, ",") != strings.Join(tc.wantStart, ",") {
				t.Errorf("restart = %v, want %v", start, tc.wantStart)
			}
		})
	}
}

// AND THE BLIND START IS REFUSED, which is what the table's first case used to permit. "The daemon does not know the
// room" and "nobody could read the answer" both arrive as errors, and collapsing them meant every unreadable status
// sent the whole definition - StartRoom on the wire against a room that may be running perfectly well. The two are
// told apart by TYPE now, because the code they share (the room contract's exit 3) cannot tell them apart.
func TestAnUnreadableStatusRefusesInsteadOfStartingTheRoomFromTheDefinition(t *testing.T) {
	unreadable := &clientruntime.UnreadableAnswer{
		Action: "GetRoomStatus",
		CodedError: &clientcmd.CodedError{
			Message: "the runtime's GetRoomStatus answer could not be read (the document it sent is not the shape " +
				"this verb asks for): unexpected end of JSON input",
			Code: clientcmd.ExitRoomRefused,
		},
	}
	restart, send, fail := seatStartPlan(nil, unreadable)
	if send || len(restart) != 0 {
		t.Errorf("an unreadable status planned a start: restart=%v sendSpec=%v", restart, send)
	}
	if fail == nil {
		t.Fatal("an unreadable status was not refused: the caller would go on to StartRoom")
	}
	if code := clientcmd.CodeOf(fail, clientcmd.ExitOK); code != clientcmd.ExitRoomRefused {
		t.Errorf("exit code = %d, want %d (the room contract's refusal)", code, clientcmd.ExitRoomRefused)
	}
}

// AND ONLY THE DAEMON'S OWN REFUSAL STILL STARTS THE ROOM, so the split is not over-tightened into "any error
// refuses" either: the daemon not knowing the room is exactly when the definition is the only source of its seats
// (U1). This is the half U1 rests on, and the wire measurement for it is a daemon that answers ok=false for a room
// whose spec.json does not exist yet.
func TestADaemonRefusalStillStartsFromTheDefinition(t *testing.T) {
	restart, send, fail := seatStartPlan(nil, daemonRefusal())
	if !send {
		t.Error("the daemon's own refusal did not send the definition: U1's unknown room would no longer start")
	}
	if len(restart) != 0 {
		t.Errorf("restart = %v, want none", restart)
	}
	if fail != nil {
		t.Errorf("the daemon's own refusal was turned into a refusal of ours: %v", fail)
	}
}

// AND A FAILURE THAT IS NOT THE DAEMON'S OWN REFUSAL REFUSES. A transport that never answered tells this process
// nothing about what is running, so the definition must not be sent - this is the arm the reviewer's measurement
// failed on: a status whose line was cut short reached StartRoom, because that error was a bare one and every bare
// error was read as "the room is unknown".
func TestATransportFailureRefusesInsteadOfStartingTheRoom(t *testing.T) {
	restart, send, fail := seatStartPlan(nil, errors.New(
		"cannot connect to agenthub-runtime at /tmp/undec-fake/rt.sock: connection refused"))
	if send || len(restart) != 0 {
		t.Errorf("a transport failure planned a start: restart=%v sendSpec=%v", restart, send)
	}
	if fail == nil {
		t.Fatal("a transport failure was not refused: the caller would go on to StartRoom")
	}
}

// THE THIRD CASE IS A START THE GATE DID NOT DECIDE, AND IT IS SAID OUT LOUD. NO RECORDS - the client's
// transports know no such room - still proceeds, because there are no seats in those records to bill and
// blocking a start on a room this client knows nothing about would be wrong. But the gate has NOT run for
// that start, and this branch used to `return clientcmd.ExitOK` with nothing printed: silence is how a
// DeepSeek seat starts on the peak rate with no line anywhere saying the check never happened. So the
// decision stays ExitOK and the fact is printed to the writer the gate already holds, naming the verb, the
// room, and that the gate did NOT run. Both verbs that call the gate here are pinned, so neither can drop
// the line on its own.
func TestPeakGateSaysWhenItDidNotRunForARoomItHasNoRecordsOf(t *testing.T) {
	restoreAll(t)
	rigNodes = func(string) ([]map[string]any, bool, error) { return nil, false, nil }
	// The clock is pinned inside the peak window on a DeepSeek-heavy Friday, so the case cannot pass
	// merely because the schedule had nothing to refuse.
	gateEnv(t, "2026-10-09 02:00")

	for _, verb := range []string{"up", "compact"} {
		var errOut bytes.Buffer
		if code := PeakGate("room-alpha", verb, false, &errOut); code != clientcmd.ExitOK {
			t.Fatalf("%s: exit %d, want %d - a room with no records has no seats to bill, so the start proceeds",
				verb, code, clientcmd.ExitOK)
		}
		for _, want := range []string{"4genteam " + verb + ":", "room-alpha", "no node records", "did NOT run"} {
			if !strings.Contains(errOut.String(), want) {
				t.Errorf("%s: the gate's silence is not ended - stderr %q does not name %q", verb, errOut.String(), want)
			}
		}
	}
}

// TestUpRefusesBeforeTheDaemonWhenTheProbeCannotRun: every step of `up` reads or signals processes, so a
// machine whose pgrep/pkill cannot run is refused BEFORE the daemon, the agent or a seat (7.12). The check
// is the real one - PATH holds no pgrep - and the assertion is that nothing ran at all.
func TestUpRefusesBeforeTheDaemonWhenTheProbeCannotRun(t *testing.T) {
	restoreAll(t)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	// The seams below are recorders: if the preflight ever moved behind them, the test names the step that
	// ran instead of starting the machine's own daemon.
	var ran []string
	ensureDaemon = func(string, io.Writer) (string, error) {
		ran = append(ran, "ensureDaemon")
		return "/tmp/daemon.log", nil
	}
	recordRigFor = func(string, io.Writer) bool { ran = append(ran, "recordRig"); return true }
	startAuthority = func(string, io.Writer, io.Writer) bool { ran = append(ran, "startAgent"); return true }

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"demo", "--team", writeRoomDefinition(t, "demo")}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("up exited %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if len(ran) != 0 {
		t.Errorf("up ran %v on a probe it could not read, want nothing started", ran)
	}
	if !strings.Contains(errOut.String(), "nothing was started or stopped") || !strings.Contains(errOut.String(), "pgrep") {
		t.Errorf("stderr = %q, want the refusal naming the probe", errOut.String())
	}
}

// TestStopRefusesWhenTheSignalProbeCannotRun: a pkill that could not run is not "nothing matched". `stop`
// exits 3 and says the agent was NOT stopped, and it never prints the `was not running` line that used to
// make the verb exit 0 on a stop it never attempted (7.12).
func TestStopRefusesWhenTheSignalProbeCannotRun(t *testing.T) {
	restoreAll(t)
	stopPattern = func(string) (bool, error) { return false, errors.New("pkill: not found") }
	closed := false
	previousClose := closeWatch
	closeWatch = func(context.Context, string) error { closed = true; return nil }
	t.Cleanup(func() { closeWatch = previousClose })

	var out, errOut bytes.Buffer
	code := (stopCommand{}).Run(context.Background(), []string{"my-rig"}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("stop exited %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if strings.Contains(out.String(), "was not running") {
		t.Errorf("stdout = %q, want no `was not running`: nothing was signalled", out.String())
	}
	if !strings.Contains(errOut.String(), "agent was NOT stopped") || !strings.Contains(errOut.String(), "pkill") {
		t.Errorf("stderr = %q, want the refusal naming the probe", errOut.String())
	}
	if closed {
		t.Error("stop closed the watch workspaces on a stop it never carried out")
	}
}
