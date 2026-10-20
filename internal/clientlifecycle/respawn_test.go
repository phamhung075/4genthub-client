package clientlifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// stubDaemons replaces the two process-reading seams --respawn uses and returns the recorded steps. Live
// answers from answer, and a stopping pattern drains it, so the exchange - signalled, then gone - is what
// the test sees rather than a process table the test cannot produce.
func stubDaemons(t *testing.T, live []int) *[]string {
	t.Helper()
	var steps []string
	previousLive, previousStop := clientservices.Live, stopPattern
	t.Cleanup(func() { clientservices.Live, stopPattern = previousLive, previousStop })
	clientservices.Live = func(pattern string) ([]int, error) {
		steps = append(steps, "live")
		if len(live) == 0 {
			return nil, nil
		}
		out := append([]int(nil), live...)
		return out, nil
	}
	stopPattern = func(pattern string) (bool, error) {
		if strings.Contains(pattern, "agenthub-runtime") {
			steps = append(steps, "stop-daemon")
			live = nil
		} else {
			steps = append(steps, "stop-agent")
		}
		return true, nil
	}
	return &steps
}

// TestTheDaemonPatternNamesOnlyTheRootItServes: the match is the argv runtimeDaemonCommand builds, so the
// daemon of ANOTHER state tree is not this run's to stop.
func TestTheDaemonPatternNamesOnlyTheRootItServes(t *testing.T) {
	pattern := daemonPattern("/home/u/.config/4genthub")
	if _, err := regexp.Compile(pattern); err != nil {
		t.Fatalf("the pattern is not valid for pgrep -f: %v", err)
	}
	for _, c := range []struct {
		cmdline string
		want    bool
	}{
		{"/usr/local/bin/agenthub-runtime --state-dir /home/u/.config/4genthub --socket /run/a.sock daemon", true},
		{"agenthub-runtime --state-dir /home/u/.config/4genthub --socket /run/a.sock daemon", true},
		{"/usr/local/bin/agenthub-runtime --state-dir /home/u/.config/4genthub-2 --socket /run/a.sock daemon", false},
		{"/usr/local/bin/agenthub-runtime --state-dir /home/u/.config/4genthub2 --socket /run/a.sock daemon", false},
		{"/usr/local/bin/4genteam up 4genthub-min --state-root /home/u/.config/4genthub", false},
	} {
		if got := regexp.MustCompile(pattern).MatchString(c.cmdline); got != c.want {
			t.Errorf("pattern match for %q = %v, want %v", c.cmdline, got, c.want)
		}
	}
}

// TestUpRespawnStopsTheSupervisionThenTheDaemonBeforeTheRoomStarts is the flag's whole contract: agent goes
// first (a watchdog left running would restore the seats being cycled), then the daemon serving THIS root,
// and only then does the run reach ensureDaemon - which starts the installed binary.
func TestUpRespawnStopsTheSupervisionThenTheDaemonBeforeTheRoomStarts(t *testing.T) {
	restoreAll(t)
	steps := stubDaemons(t, []int{4242})
	// This is the DETACHED run: the calling process has already re-exec'd itself (respawn.go), so this
	// side is the one that stops agent and the daemon.
	t.Setenv(respawnDetachedEnv, "1")
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTHUB_TOKEN", "not-a-real-token")
	t.Setenv("AGENTHUB_CONNECTOR_TOKEN", "not-a-real-token")
	ensureDaemon = func(string, io.Writer) (string, error) {
		*steps = append(*steps, "ensureDaemon")
		return "/tmp/daemon.log", nil
	}
	ensureRoom = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		*steps = append(*steps, "ensureRoom")
		return clientcmd.ExitOK
	}
	ensureWorktrees = func(string, clientruntime.RoomSpec, io.Writer, io.Writer) int {
		*steps = append(*steps, "ensureWorktrees")
		return clientcmd.ExitOK
	}

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(),
		[]string{"my-room", "--team", writeRoomDefinition(t, "my-room"), "--room-only", "--respawn"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stdout %q, stderr %q)", code, out.String(), errOut.String())
	}
	want := []string{"stop-agent", "live", "stop-daemon", "live", "ensureDaemon", "ensureWorktrees", "ensureRoom"}
	if strings.Join(*steps, ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, want %v", *steps, want)
	}
	if !strings.Contains(out.String(), "4242") {
		t.Errorf("the daemon's process id was not named: %q", out.String())
	}
}

// TestUpRespawnRefusesToStartASecondDaemon: a daemon that did not accept the signal, and one that did not
// exit, are both a refusal - never a second daemon beside the first.
func TestUpRespawnRefusesToStartASecondDaemon(t *testing.T) {
	for _, c := range []struct {
		name    string
		signals bool
	}{
		{"the signal was not accepted", false},
		{"the daemon did not exit", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			restoreAll(t)
			t.Setenv(respawnDetachedEnv, "1")
			steps := stubDaemons(t, []int{4242})
			if c.signals {
				// Signalled, but still there when the window runs out.
				clientservices.Live = func(string) ([]int, error) { *steps = append(*steps, "live"); return []int{4242}, nil }
			} else {
				stopPattern = func(string) (bool, error) { return false, nil }
			}
			previousWindow := respawnWindow
			respawnWindow = 0
			t.Cleanup(func() { respawnWindow = previousWindow })
			ensureDaemon = func(string, io.Writer) (string, error) {
				t.Error("a second daemon was started")
				return "/tmp/daemon.log", nil
			}

			var out, errOut bytes.Buffer
			code := (upCommand{}).Run(context.Background(),
				[]string{"my-room", "--team", writeRoomDefinition(t, "my-room"), "--room-only", "--respawn"}, &out, &errOut)
			if code != clientcmd.ExitUnavailable {
				t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
			}
			if !strings.Contains(errOut.String(), "4242") {
				t.Errorf("the refusal did not name the daemon: %q", errOut.String())
			}
		})
	}
}

// TestUpWithoutRespawnLeavesTheDaemonAlone is the other direction: the flag is opt-in, and a plain `up`
// must not stop anything.
func TestUpWithoutRespawnLeavesTheDaemonAlone(t *testing.T) {
	restoreAll(t)
	steps := stubDaemons(t, []int{4242})
	ensureDaemon = func(string, io.Writer) (string, error) {
		*steps = append(*steps, "ensureDaemon")
		return "/tmp/daemon.log", nil
	}
	ensureRoom = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		*steps = append(*steps, "ensureRoom")
		return clientcmd.ExitOK
	}

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(),
		[]string{"my-room", "--team", writeRoomDefinition(t, "my-room"), "--room-only"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stdout %q, stderr %q)", code, out.String(), errOut.String())
	}
	for _, step := range *steps {
		if strings.HasPrefix(step, "stop") || step == "live" {
			t.Errorf("a plain up touched the daemon: steps %v", *steps)
			break
		}
	}
}

// TestUpRespawnReExecsItselfBeforeItStopsAnything is the 2026-10-20 defect: a seat ran the verb, the agent
// stop took the seat with it, and the seats and agent the run was about to start never were. The calling
// process must detach FIRST - before it signals agent or the daemon - name the detached run's log, and
// return with nothing started or stopped by itself.
func TestUpRespawnReExecsItselfBeforeItStopsAnything(t *testing.T) {
	restoreAll(t)
	steps := stubDaemons(t, []int{4242})
	t.Setenv(respawnDetachedEnv, "")
	stateDir := filepath.Join(t.TempDir(), "state")
	teamDir := writeRoomDefinition(t, "my-room")
	var detachedRoot string
	var argv []string
	detachRespawn = func(root string, a []string) (string, error) {
		detachedRoot, argv = root, a
		return respawnLogPath(root), nil
	}
	ensureDaemon = func(string, io.Writer) (string, error) {
		t.Error("the calling process started the daemon")
		return "", nil
	}
	ensureRoom = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		t.Error("the calling process started a seat")
		return clientcmd.ExitOK
	}
	ensureWorktrees = func(string, clientruntime.RoomSpec, io.Writer, io.Writer) int {
		t.Error("the calling process ran the worktrees step")
		return clientcmd.ExitOK
	}

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(),
		[]string{"my-room", "--team", teamDir, "--state-root", stateDir, "--room-only", "--respawn"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stdout %q, stderr %q)", code, out.String(), errOut.String())
	}
	want := []string{"up", "my-room", "--team", teamDir, "--state-root", stateDir, "--room-only", "--respawn"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("the detached run's argv = %q, want %q", argv, want)
	}
	if detachedRoot != stateDir {
		t.Errorf("the detached run got state dir %q, want %q", detachedRoot, stateDir)
	}
	if len(*steps) != 0 {
		t.Errorf("the calling process signalled something before detaching: %v", *steps)
	}
	if !strings.Contains(out.String(), respawnLogPath(stateDir)) {
		t.Errorf("the caller was not given the log path: %q", out.String())
	}
}

// TestTheDetachedRespawnIsItsOwnSession runs the real detach against a shell: the restart must be in a NEW
// session (so the caller's death, or a signal to its group, does not take it), its output must land on the
// log the caller was given, and the call must not wait for it.
func TestTheDetachedRespawnIsItsOwnSession(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not on this machine")
	}
	ps, err := exec.LookPath("ps")
	if err != nil {
		t.Skip("ps is not on this machine")
	}
	restoreAll(t)
	respawnExecutable = func() (string, error) { return sh, nil }
	sid := func(pid string) string {
		out, err := exec.Command(ps, "-o", "sid=", "-p", pid).Output()
		if err != nil {
			t.Fatalf("ps -o sid= -p %s: %v", pid, err)
		}
		return strings.TrimSpace(string(out))
	}
	caller := sid(strconv.Itoa(os.Getpid()))
	stateDir := t.TempDir()

	started := time.Now()
	logPath, err := detachRespawnImpl(stateDir, []string{"-c", "echo \"sid=$(ps -o sid= -p $$)\"; sleep 3; echo done"})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("detach refused: %v", err)
	}
	if !strings.HasSuffix(logPath, "up-respawn.log") {
		t.Errorf("log path = %q, want the state root's up-respawn.log", logPath)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the detach waited on its child: %s", elapsed)
	}
	deadline := time.Now().Add(5 * time.Second)
	var log string
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), "sid=") {
			log = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if log == "" {
		t.Fatalf("the detached run wrote nothing to %s", logPath)
	}
	got := strings.TrimPrefix(strings.SplitN(strings.TrimSpace(log), "\n", 2)[0], "sid=")
	if got == "" || got == caller {
		t.Errorf("the restart's session id = %q, want its own session rather than the caller's %q (log %q)", got, caller, log)
	}
	if strings.Contains(log, "done") {
		t.Errorf("the detach waited for the child to finish: %q", log)
	}
}

// daemonStandInEnv makes this test binary the stand-in runtime daemon: it never parses a flag, so it can
// be spawned with the daemon's own argv, and the pattern under test sees exactly that command line.
const daemonStandInEnv = "CLIENTLIFECYCLE_DAEMON_STANDIN"

func TestMain(m *testing.M) {
	if os.Getenv(daemonStandInEnv) == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

// TestRespawnDaemonReplacesARealProcessForTheRoot runs the step against a real process table: a stand-in
// spawned with the argv `up` spawns is stopped by the pattern, a daemon of ANOTHER state root is left
// alone. pgrep/pkill are the mechanism, so a machine without them skips.
func TestRespawnDaemonReplacesARealProcessForTheRoot(t *testing.T) {
	for _, tool := range []string{"pgrep", "pkill"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on this machine; the step has no mechanism to test", tool)
		}
	}
	start := func(t *testing.T, stateDir string) {
		t.Helper()
		directory := t.TempDir()
		standin := filepath.Join(directory, "agenthub-runtime")
		cmd := exec.Command(os.Args[0], "--state-dir", stateDir, "--socket", filepath.Join(directory, "s.sock"), "daemon")
		cmd.Args[0] = standin
		cmd.Env = append(os.Environ(), daemonStandInEnv+"=1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	}
	waitLive := func(t *testing.T, stateDir string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if live, err := clientservices.Live(daemonPattern(stateDir)); err == nil && len(live) > 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("the stand-in daemon for %s never showed up in the process table", stateDir)
	}

	mine := filepath.Join(t.TempDir(), "state")
	other := filepath.Join(t.TempDir(), "state")
	start(t, mine)
	start(t, other)
	waitLive(t, mine)
	waitLive(t, other)

	var out, errOut bytes.Buffer
	if !respawnDaemon(mine, &out, &errOut) {
		t.Fatalf("respawnDaemon refused: %s", errOut.String())
	}
	if live, err := clientservices.Live(daemonPattern(mine)); err != nil || len(live) != 0 {
		t.Errorf("the daemon for this root is still running: %v", live)
	}
	if live, err := clientservices.Live(daemonPattern(other)); err != nil || len(live) == 0 {
		t.Error("the daemon for another state root was stopped as well")
	}
}

// TestRespawnRefusesWhenTheProbeCannotRun: the empty process table a missing pgrep used to answer is what
// reached `return true` here, and `up --respawn` then started a SECOND daemon for a state root that already
// had one. The refusal is on stderr, nothing is signalled, and nothing is started (7.12).
func TestRespawnRefusesWhenTheProbeCannotRun(t *testing.T) {
	restoreAll(t)
	clientservices.Live = func(string) ([]int, error) { return nil, errors.New("pgrep: not found") }
	signalled := false
	stopPattern = func(string) (bool, error) { signalled = true; return true, nil }
	started := false
	ensureDaemon = func(string, io.Writer) (string, error) { started = true; return "/tmp/daemon.log", nil }

	var out, errOut bytes.Buffer
	if respawnDaemon(t.TempDir(), &out, &errOut) {
		t.Fatalf("respawnDaemon reported the daemon replaced from a probe that could not run")
	}
	if signalled || started {
		t.Errorf("a refused respawn signalled the daemon (%v) or started one (%v)", signalled, started)
	}
	if !strings.Contains(errOut.String(), "pgrep") || out.Len() != 0 {
		t.Errorf("stdout = %q stderr = %q, want the refusal naming the probe and no daemon line", out.String(), errOut.String())
	}
}
