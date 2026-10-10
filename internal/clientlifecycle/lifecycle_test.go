package clientlifecycle

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// helpOf runs one verb with --help and returns what it printed.
func helpOf(t *testing.T, name, flag string) (int, string, string) {
	t.Helper()
	for _, command := range Commands() {
		if command.Name() != name {
			continue
		}
		var out, errOut bytes.Buffer
		code := command.Run(context.Background(), nil, []string{flag}, &out, &errOut)
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

// restoreAll puts every seam back after a test that replaces it.
func restoreAll(t *testing.T) {
	t.Helper()
	daemon, rigStep := ensureDaemon, ensureRig
	force, bridge := ensureForcecompact, startBridge
	ui, watch, herdr := openUI, runWatchView, runHerdr
	spawn, grace := spawnChild, SupervisorStartGrace
	stop := stopPattern
	t.Cleanup(func() {
		ensureDaemon, ensureRig = daemon, rigStep
		ensureForcecompact, startBridge = force, bridge
		openUI, runWatchView, runHerdr = ui, watch, herdr
		spawnChild, SupervisorStartGrace = spawn, grace
		stopPattern = stop
	})
}

// TestHelpAfterAVerbNeverRunsALifecycleAction is the other half of the same fault, asserted as the
// mechanism: nothing the verb would do may happen while it is answering help.
func TestHelpAfterAVerbNeverRunsALifecycleAction(t *testing.T) {
	var ran []string
	restoreAll(t)
	ensureDaemon = func(*clientcmd.Rig, io.Writer) error { return nil }
	spawnChild = func([]string, string) (supervisorChild, error) {
		ran = append(ran, "spawn")
		return diedChild{}, nil
	}
	startBridge = func(io.Writer, io.Writer) {}
	openUI = func(*clientcmd.Rig, io.Writer) {}
	runWatchView = func(string, io.Writer) { ran = append(ran, "watch") }
	runHerdr = func(io.Writer, io.Writer) int { ran = append(ran, "herdr"); return 0 }

	var out, errOut bytes.Buffer
	if code := (compactCommand{}).Run(context.Background(), nil, []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("compact --help exited %d", code)
	}
	if code := (upCommand{}).Run(context.Background(), nil, []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("up --help exited %d", code)
	}
	if len(ran) != 0 {
		t.Fatalf("asking for help ran a lifecycle action: %v", ran)
	}
}

// TestUpInitialisesEveryServiceInOrder is the Python test of the same name: the daemon, the rig, the
// forcecompact check, the bridge, the supervisor, the watch view, the UI, then herdr.
func TestUpInitialisesEveryServiceInOrder(t *testing.T) {
	var ran []string
	restoreAll(t)
	ensureDaemon = func(*clientcmd.Rig, io.Writer) error { ran = append(ran, "ensureDaemon"); return nil }
	ensureRig = func(*clientcmd.Rig, string, io.Writer) { ran = append(ran, "ensureRig") }
	ensureForcecompact = func(io.Writer) { ran = append(ran, "ensureForcecompact") }
	startBridge = func(io.Writer, io.Writer) { ran = append(ran, "startBridge") }
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	spawnChild = func([]string, string) (supervisorChild, error) {
		ran = append(ran, "startSupervisor")
		return survivedChild{release}, nil
	}
	runWatchView = func(string, io.Writer) { ran = append(ran, "watch") }
	openUI = func(*clientcmd.Rig, io.Writer) { ran = append(ran, "openUI") }
	runHerdr = func(io.Writer, io.Writer) int { ran = append(ran, "herdr"); return 0 }
	SupervisorStartGrace = time.Millisecond
	t.Setenv("HERDR_ENV", "")

	var out, errOut bytes.Buffer
	if code := (upCommand{}).Run(context.Background(), &clientcmd.Rig{}, []string{"my-rig"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("up returned %d (stderr: %q)", code, errOut.String())
	}
	want := []string{"ensureDaemon", "ensureRig", "ensureForcecompact", "startBridge", "startSupervisor", "watch", "openUI", "herdr"}
	if strings.Join(ran, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", ran, want)
	}
}

// TestStopSignalsTheSupervisorAndTheBridgeForTheNamedRig: `stop` is the other half of `up`'s contract,
// and it has to name what it signals. Asserted as the PATTERNS rather than by signalling them - a test
// that ran the real thing would stop this machine's services.
func TestStopSignalsTheSupervisorAndTheBridgeForTheNamedRig(t *testing.T) {
	var patterns []string
	restoreAll(t)
	stopPattern = func(pattern string) bool {
		patterns = append(patterns, pattern)
		return true
	}

	var out, errOut bytes.Buffer
	if code := (stopCommand{}).Run(context.Background(), &clientcmd.Rig{}, []string{"my-rig"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("stop returned %d (stderr %q)", code, errOut.String())
	}
	want := []string{SupervisorPattern("my-rig"), BridgePattern}
	if strings.Join(patterns, "|") != strings.Join(want, "|") {
		t.Errorf("signalled %v, want %v", patterns, want)
	}
	if !strings.Contains(out.String(), "compact supervisor: stopped") {
		t.Errorf("stdout = %q, want it to report the supervisor it stopped", out.String())
	}

	// No rig means no guess, and nothing may be signalled while the verb decides that.
	patterns = nil
	out.Reset()
	if code := (stopCommand{}).Run(context.Background(), &clientcmd.Rig{}, nil, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("stop with no rig returned %d, want %d", code, clientcmd.ExitUsage)
	}
	// Help is the other case where a lifecycle action must not run.
	if code := (stopCommand{}).Run(context.Background(), &clientcmd.Rig{}, []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Errorf("stop --help returned %d, want %d", code, clientcmd.ExitOK)
	}
	if len(patterns) != 0 {
		t.Errorf("stop signalled %v without a rig to name", patterns)
	}
}

// diedChild is a supervisor that was already dead when the start check ran - what `--rig --help` did.
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

// TestAStartThatDiesImmediatelyPrintsNoPidAndDoesNotReturnZero is the half that matters: a failed start
// must not read as a successful one.
func TestAStartThatDiesImmediatelyPrintsNoPidAndDoesNotReturnZero(t *testing.T) {
	restoreAll(t)
	spawnChild = func([]string, string) (supervisorChild, error) { return diedChild{}, nil }
	SupervisorStartGrace = time.Millisecond

	var out, errOut bytes.Buffer
	code := (compactCommand{}).Run(context.Background(), nil, []string{"some-rig"}, &out, &errOut)
	if code == clientcmd.ExitOK {
		t.Fatalf("a start whose child died immediately returned %d", code)
	}
	if strings.Contains(out.String(), "pid") {
		t.Errorf("a failed start printed a pid: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "exited immediately") {
		t.Errorf("the failure was not named on stderr: %q", errOut.String())
	}
}

// TestAStartThatSurvivesStillReportsItsPid is the control: the fix must not break the successful path.
func TestAStartThatSurvivesStillReportsItsPid(t *testing.T) {
	restoreAll(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	spawnChild = func([]string, string) (supervisorChild, error) { return survivedChild{release}, nil }
	SupervisorStartGrace = time.Millisecond

	var out, errOut bytes.Buffer
	code := (compactCommand{}).Run(context.Background(), nil, []string{"some-rig"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("a healthy start returned %d (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "pid 4243") {
		t.Errorf("a healthy start did not report its pid: %q", out.String())
	}
}

// TestEveryLifecycleVerbRequiresARig: the Python defaulted to a rig of the owner's own team. There is no
// such default now, so an omitted name is a usage error that names the verb and the argument.
func TestEveryLifecycleVerbRequiresARig(t *testing.T) {
	for _, name := range []string{"up", "compact", "stop", "status", "log"} {
		for _, command := range Commands() {
			if command.Name() != name {
				continue
			}
			var out, errOut bytes.Buffer
			code := command.Run(context.Background(), &clientcmd.Rig{}, nil, &out, &errOut)
			if code != clientcmd.ExitUsage {
				t.Errorf("%s with no rig: exit %d, want %d", name, code, clientcmd.ExitUsage)
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
	options, ok := ParseCompactRunOptions([]string{"--rig", "my-rig", "--once"}, &errOut)
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
	if _, ok := ParseCompactRunOptions([]string{"--rig", "my-rig", "--once"}, &errOut); ok {
		t.Fatal("a non-numeric COMPACT_QUIET_SECONDS was accepted")
	}
	if !strings.Contains(errOut.String(), "COMPACT_QUIET_SECONDS") {
		t.Errorf("the refusal did not name the variable: %q", errOut.String())
	}
}

// test_compact_run_reads_the_documented_state_root_variable: the flag used to default straight to the
// machine's own root, so whoever set $OPENRIG_STATE_ROOT got the machine tree while the refusal in these
// verbs told them to set exactly that variable. The variable is the second clause for compact-run too.
func TestCompactRunReadsTheStateRootVariable(t *testing.T) {
	var errOut bytes.Buffer
	chosen := t.TempDir()
	t.Setenv(clientenv.StateRootEnv, chosen)

	options, ok := ParseCompactRunOptions([]string{"--rig", "my-rig", "--once"}, &errOut)
	if !ok {
		t.Fatalf("a set variable was refused: %s", errOut.String())
	}
	if options.StateRoot != chosen {
		t.Errorf("StateRoot = %q, want the variable's %q", options.StateRoot, chosen)
	}

	// The flag still wins over the variable.
	options, ok = ParseCompactRunOptions([]string{"--rig", "my-rig", "--once", "--state-root", "/from/flag"}, &errOut)
	if !ok {
		t.Fatalf("the flag was refused: %s", errOut.String())
	}
	if options.StateRoot != "/from/flag" {
		t.Errorf("StateRoot = %q, want /from/flag", options.StateRoot)
	}
}

// TestCompactRunRequiresARig: the Python's --rig defaulted to the owner's rig.
// test_compact_run_requires_a_rig.
func TestCompactRunRequiresARig(t *testing.T) {
	var errOut bytes.Buffer
	if _, ok := ParseCompactRunOptions([]string{"--once"}, &errOut); ok {
		t.Fatal("compact-run accepted no --rig")
	}
	if !strings.Contains(errOut.String(), "no default rig") {
		t.Errorf("the refusal did not state the rule: %q", errOut.String())
	}
}
