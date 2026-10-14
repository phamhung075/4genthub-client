package clientrestore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// beats records every heartbeat the watchdog seam wrote in the current test; isolate installs the
// recorder and clears it, so a test asserts the beats without touching the machine's log directory.
var beats []string

// isolate points the package's home at a temporary directory and stubs every seam the watchdog can reach:
// no real home, no tmux, no rig. It restores them when the test ends. Everything a test sets AFTER calling
// it (a per-test tmuxRead, watchdogNodesJSON) is restored by the same cleanup.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldHome, oldTmux, oldIdent := realHome, tmuxRead, tmuxServerIdentity
	oldNodes, oldSleep, oldLimit := watchdogNodesJSON, watchSleep, watchPollLimit
	// Every poll of the watch loop now beats. The seam is replaced here, so no test writes into the
	// machine's own log directory; the beat is recorded for the focused test that asserts it.
	oldBeat := clientservices.WriteHeartbeat
	beats = nil
	clientservices.WriteHeartbeat = func(safeguard, rig string) error {
		beats = append(beats, safeguard+"/"+rig)
		return nil
	}
	realHome = func() (string, error) { return home, nil }
	tmuxRead = func() (string, error) { return "", nil }
	tmuxServerIdentity = func() string { return "1 2" }
	watchdogNodesJSON = func(string) (string, error) { return "[]", nil }
	watchSleep = func(time.Duration) {}
	watchPollLimit = 0
	t.Cleanup(func() {
		realHome, tmuxRead, tmuxServerIdentity = oldHome, oldTmux, oldIdent
		watchdogNodesJSON, watchSleep, watchPollLimit = oldNodes, oldSleep, oldLimit
		clientservices.WriteHeartbeat = oldBeat
	})
	return home
}

// writeBrief drops <home>/.config/4genthub/briefs/<rig>.txt, which is what "a rig that has a brief" means.
func writeBrief(t *testing.T, home, rigName string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "4genthub", "briefs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rigName+".txt"), []byte("brief"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// drivePolls makes each poll answer with the next state in states ("dead" or "alive"): expected seats come
// from the rig-JSON seam (which advances the index once per poll) and live seats from the tmux seam. A
// call past the end repeats the last state, so a test need only name the polls it cares about.
func drivePolls(states []string) {
	idx := -1
	tmuxRead = func() (string, error) {
		if idx >= 0 && states[idx] == "alive" {
			return "agenthub--demo--seat\n", nil
		}
		return "", nil
	}
	watchdogNodesJSON = func(string) (string, error) {
		idx++
		if idx >= len(states) {
			idx = len(states) - 1
		}
		return `[{"lifecycleState":"running"}]`, nil
	}
}

func testWatcher(buf *bytes.Buffer) *watcher {
	return &watcher{
		log:      &teeLog{out: buf},
		stderr:   io.Discard,
		logPath:  "/tmp/rig-watchdog.log",
		daemonUp: func() {},
		gate:     func(string, string, bool, io.Writer) int { return clientcmd.ExitOK },
		restore:  func(string, string) bool { return true },
	}
}

// TestVerdictRule: the decision rule as a pure function, the six cases --self-test prints.
func TestVerdictRule(t *testing.T) {
	cases := []struct {
		expected, live int
		want           string
	}{
		{10, 10, verdictHealthy},
		{10, 0, verdictDead},
		{1, 0, verdictDead},
		{3, 2, verdictHealthy},
		{0, 0, verdictStopped},
		{0, 10, verdictStopped},
	}
	for _, c := range cases {
		if got := verdict(c.expected, c.live); got != c.want {
			t.Errorf("verdict(%d,%d) = %s, want %s", c.expected, c.live, got, c.want)
		}
	}
}

// TestSelfTestTable: the shell's table, six passes, exit 0.
func TestSelfTestTable(t *testing.T) {
	var buf bytes.Buffer
	if code := selfTest(&buf); code != clientcmd.ExitOK {
		t.Fatalf("selfTest exit %d, want %d", code, clientcmd.ExitOK)
	}
	for _, want := range []string{
		"decision rule:",
		"ok   verdict(10,10) = healthy",
		"ok   verdict(10,0) = dead",
		"ok   verdict(1,0) = dead",
		"ok   verdict(3,2) = healthy",
		"ok   verdict(0,0) = stopped",
		"ok   verdict(0,10) = stopped",
		"result: 6 passed, 0 failed",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("self-test output missing %q:\n%s", want, buf.String())
		}
	}
}

// TestCountRunningSeats: the python's job - running rows counted, everything unparseable is 0.
func TestCountRunningSeats(t *testing.T) {
	cases := []struct {
		name, data string
		want       int
	}{
		{"all running", `[{"lifecycleState":"running"},{"lifecycleState":"running"}]`, 2},
		{"mixed states", `[{"lifecycleState":"running"},{"lifecycleState":"stopped"},{"lifecycleState":"archived"}]`, 1},
		{"none running", `[{"lifecycleState":"stopped"}]`, 0},
		{"empty array", `[]`, 0},
		{"malformed", `{not json`, 0},
		{"not an array", `{"lifecycleState":"running"}`, 0},
		{"empty input", ``, 0},
		{"row that is not an object", `[{"lifecycleState":"running"}, "x"]`, 0},
	}
	for _, c := range cases {
		if got := countRunningSeats(c.data); got != c.want {
			t.Errorf("%s: countRunningSeats = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestExpectedSeatsUsesTheNodeSeam: the count flows through the node seam, a malformed answer is 0, and a
// reading that FAILED comes back as an error rather than as a zero the caller would print as a fact.
func TestExpectedSeatsUsesTheNodeSeam(t *testing.T) {
	isolate(t)
	watchdogNodesJSON = func(string) (string, error) {
		return `[{"lifecycleState":"running"},{"lifecycleState":"stopped"}]`, nil
	}
	if got, err := expectedSeats("demo"); got != 1 || err != nil {
		t.Errorf("expectedSeats = %d, %v, want 1 and no error", got, err)
	}
	watchdogNodesJSON = func(string) (string, error) { return "garbage", nil }
	if got, err := expectedSeats("demo"); got != 0 || err != nil {
		t.Errorf("expectedSeats on malformed input = %d, %v, want 0 and no error", got, err)
	}
	watchdogNodesJSON = func(string) (string, error) { return "", errors.New("HTTP 500 from /api/ps") }
	if got, err := expectedSeats("demo"); err == nil {
		t.Errorf("expectedSeats = %d, nil: a reading that failed was reported as an answer", got)
	}
}

// TestAnUnreadableRoomIsNotReportedAsStopped: the sentence "daemon reports no running seats" is a fact about
// the room, and a read that FAILED does not have it. The pass says what happened and touches nothing - the
// same inaction the shell's zero produced, without the false sentence.
func TestAnUnreadableRoomIsNotReportedAsStopped(t *testing.T) {
	isolate(t)
	watchdogNodesJSON = func(string) (string, error) { return "", errors.New("HTTP 500 from /api/ps") }
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	if code := w.check("demo", "report"); code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d: an unreadable room is not a dead one", code, clientcmd.ExitOK)
	}
	out := buf.String()
	if !strings.Contains(out, "the room's node records could not be read; no action") {
		t.Errorf("the failed read was not named:\n%s", out)
	}
	if strings.Contains(out, "daemon reports no running seats") || strings.Contains(out, "DEAD —") {
		t.Errorf("a reading that failed was reported as a fact about the room:\n%s", out)
	}
}

// TestWatchDoesNotCountAnUnreadableRoomAsAMiss: the miss counter is for seats that vanished. Two unreadable
// polls must not add up to a restore of a room this watcher never read.
func TestWatchDoesNotCountAnUnreadableRoomAsAMiss(t *testing.T) {
	isolate(t)
	watchdogNodesJSON = func(string) (string, error) { return "", errors.New("HTTP 500 from /api/ps") }
	watchPollLimit = 2
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	restores := 0
	w.restore = func(string, string) bool { restores++; return true }
	w.watch("demo", time.Minute, defaultWatchBeat)

	if restores != 0 {
		t.Errorf("restores = %d, want 0: an unreadable reading is not a miss", restores)
	}
	out := buf.String()
	if strings.Contains(out, "MISS ") {
		t.Errorf("an unreadable reading was counted as a miss:\n%s", out)
	}
	if !strings.Contains(out, "the room's node records could not be read; not counted as a miss") {
		t.Errorf("the failed read was not named:\n%s", out)
	}
}

// TestWatchOneMissDoesNotRestore: the shell required two consecutive misses; one is a report and nothing else.
func TestWatchOneMissDoesNotRestore(t *testing.T) {
	isolate(t)
	drivePolls([]string{"dead"})
	watchPollLimit = 1
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	restores := 0
	w.restore = func(string, string) bool { restores++; return true }
	w.watch("demo", time.Minute, defaultWatchBeat)

	if restores != 0 {
		t.Errorf("restores = %d, want 0 after a single miss", restores)
	}
	out := buf.String()
	if !strings.Contains(out, "demo: MISS 1/2 (daemon expects 1, tmux has 0)") {
		t.Errorf("missing MISS line:\n%s", out)
	}
	if strings.Contains(out, "DEAD —") {
		t.Errorf("a single miss acted:\n%s", out)
	}
}

// TestWatchTwoMissesRestoreOnce: the second consecutive miss runs exactly one restore.
func TestWatchTwoMissesRestoreOnce(t *testing.T) {
	isolate(t)
	drivePolls([]string{"dead", "dead"})
	watchPollLimit = 2
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	restores := 0
	w.restore = func(string, string) bool { restores++; return true }
	w.watch("demo", time.Minute, defaultWatchBeat)

	if restores != 1 {
		t.Errorf("restores = %d, want exactly 1 after two misses", restores)
	}
	out := buf.String()
	for _, want := range []string{
		"demo: MISS 1/2 (daemon expects 1, tmux has 0)",
		"demo: MISS 2/2 (daemon expects 1, tmux has 0)",
		"demo: DEAD — daemon expects 1 running seats, tmux has 0. Restoring via continue's restore.",
		"demo: restore invoked; tmux now has 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// TestWatchCounterResetWhenALiveRigRecovers: a rig that comes back before acting resets the counter and says so.
func TestWatchCounterResetWhenALiveRigRecovers(t *testing.T) {
	isolate(t)
	drivePolls([]string{"dead", "alive"})
	watchPollLimit = 2
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	restores := 0
	w.restore = func(string, string) bool { restores++; return true }
	w.watch("demo", time.Minute, defaultWatchBeat)

	out := buf.String()
	if !strings.Contains(out, "demo: live again before acting (live=1); counter reset") {
		t.Errorf("missing counter-reset line:\n%s", out)
	}
	if restores != 0 {
		t.Errorf("restores = %d, want 0", restores)
	}
}

// TestWatchHeartbeatOnlyEveryNPolls: the heartbeat fires on polls that are multiples of the beat and not
// before, so a healthy rig is not silent and not chatty.
func TestWatchHeartbeatOnlyEveryNPolls(t *testing.T) {
	isolate(t)
	drivePolls([]string{"alive"})
	watchPollLimit = 4
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	w.watch("demo", time.Minute, 2)

	out := buf.String()
	if got := strings.Count(out, "heartbeat —"); got != 2 {
		t.Errorf("heartbeats = %d, want 2 (polls 2 and 4):\n%s", got, out)
	}
	if strings.Contains(out, "MISS") {
		t.Errorf("a healthy rig reported a miss:\n%s", out)
	}
	if !strings.Contains(out, "watching every 60s (two consecutive misses required before acting; heartbeat every 120s)") {
		t.Errorf("missing the watching banner:\n%s", out)
	}
}

// TestWatchWritesOneHeartbeatPerPoll: every poll beats, not only the polls the operator's log line names,
// so a stopped watchdog and a quiet rig cannot look the same.
func TestWatchWritesOneHeartbeatPerPoll(t *testing.T) {
	isolate(t)
	drivePolls([]string{"alive"})
	watchPollLimit = 3
	w := testWatcher(&bytes.Buffer{})
	w.watch("demo", time.Minute, defaultWatchBeat)

	if len(beats) != 3 {
		t.Fatalf("heartbeats = %d (%v), want one per poll (3)", len(beats), beats)
	}
	for _, beat := range beats {
		if beat != "watchdog/demo" {
			t.Errorf("heartbeat = %q, want watchdog/demo (the rig-scoped heartbeat file)", beat)
		}
	}
}

// TestServerIdentityFirstWriteAndChange: the identity file is written once, a change is named with both
// servers, and an unchanged poll is silent.
func TestServerIdentityFirstWriteAndChange(t *testing.T) {
	home := isolate(t)
	tmuxServerIdentity = func() string { return "100 2026-01-01" }
	buf := &bytes.Buffer{}
	log := &teeLog{out: buf}

	noteServerIdentity(log)
	if got := buf.String(); !strings.Contains(got, "tmux server identity: 100 2026-01-01 (no change recorded before this poll)") {
		t.Errorf("first write did not log the identity:\n%s", got)
	}
	path := filepath.Join(home, ".config", "4genthub", "state", watchdogServerIDName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("identity file: %v", err)
	}
	if string(data) != "100 2026-01-01" {
		t.Errorf("identity file = %q, want %q", data, "100 2026-01-01")
	}

	buf.Reset()
	tmuxServerIdentity = func() string { return "200 2026-01-02" }
	noteServerIdentity(log)
	if got := buf.String(); !strings.Contains(got, "tmux SERVER CHANGED: 100 2026-01-01 -> 200 2026-01-02 (the previous server is gone)") {
		t.Errorf("change did not log the transition:\n%s", got)
	}

	buf.Reset()
	noteServerIdentity(log)
	if buf.Len() != 0 {
		t.Errorf("an unchanged identity logged: %s", buf.String())
	}
}

// TestCheckReport: a dead rig reports and exits exitDead; a healthy one is healthy and exits 0.
func TestCheckReport(t *testing.T) {
	isolate(t)
	drivePolls([]string{"dead"})
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	if code := w.check("demo", "report"); code != exitDead {
		t.Fatalf("dead report exit %d, want %d (%d is ExitUsage's number, answering a different question)",
			code, exitDead, clientcmd.ExitUsage)
	}
	if !strings.Contains(buf.String(), "demo: DEAD — daemon expects 1 running seats, tmux has 0. Re-run with --watch to restore.") {
		t.Errorf("missing dead report:\n%s", buf.String())
	}

	drivePolls([]string{"alive"})
	buf.Reset()
	w = testWatcher(buf)
	if code := w.check("demo", "report"); code != clientcmd.ExitOK {
		t.Fatalf("healthy report exit %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "demo: healthy (daemon expects 1, tmux has 1)") {
		t.Errorf("missing healthy report:\n%s", buf.String())
	}
}

// TestRestoreRunsTheGateBeforeRestoring: the peak gate is called after the daemon is up and before the
// restore, and a refusal means no restore at all.
func TestRestoreRunsTheGateBeforeRestoring(t *testing.T) {
	isolate(t)
	drivePolls([]string{"dead"})
	buf := &bytes.Buffer{}
	w := testWatcher(buf)
	var order []string
	w.daemonUp = func() { order = append(order, "daemon") }
	w.gate = func(string, string, bool, io.Writer) int { order = append(order, "gate"); return clientcmd.ExitOK }
	w.restore = func(string, string) bool { order = append(order, "restore"); return true }
	if code := w.check("demo", "restore"); code != clientcmd.ExitOK {
		t.Fatalf("restore exit %d, want 0", code)
	}
	if got := strings.Join(order, ","); got != "daemon,gate,restore" {
		t.Errorf("order = %s, want daemon,gate,restore", got)
	}

	order = nil
	w.gate = func(string, string, bool, io.Writer) int {
		order = append(order, "gate")
		return clientcmd.ExitUnavailable
	}
	w.restore = func(string, string) bool { order = append(order, "restore"); return true }
	w.check("demo", "restore")
	if got := strings.Join(order, ","); got != "daemon,gate" {
		t.Errorf("a refused gate still restored: order = %s", got)
	}
}

// TestAllWithoutWatchIsUsage: --all needs --watch, and exits 2 when it has none.
func TestAllWithoutWatchIsUsage(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := watchdogCommand{}.Run(context.Background(), []string{"--all"}, &out, &errBuf)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errBuf.String(), "--all requires --watch") {
		t.Errorf("stderr = %q, want it to name the requirement", errBuf.String())
	}
}

// TestCheckReportsAnyDeadRig: the shell's `for … done` exited with the LAST rig's status, so a dead rig
// that was not last left the exit code at 0 - the one thing a caller that reads only the exit code acts on.
// The aggregate makes that deterministic: any dead rig in the pass is exitDead, wherever it sits. The two
// rigs are polled in the order the briefs sort (alpha before zulu), so the dead one is deliberately NOT
// last.
func TestCheckReportsAnyDeadRig(t *testing.T) {
	home := isolate(t)
	writeBrief(t, home, "alpha")
	writeBrief(t, home, "zulu")
	// The daemon expects a running seat in both, and tmux has one for zulu only: alpha is dead, and it is
	// the FIRST rig in the pass, which is the position the shell's last-status exit code got wrong.
	watchdogNodesJSON = func(string) (string, error) {
		return `[{"lifecycleState":"running"}]`, nil
	}
	tmuxRead = func() (string, error) { return "agenthub--zulu--seat\n", nil }

	var out, errBuf bytes.Buffer
	code := watchdogCommand{}.Run(context.Background(), []string{"--check"}, &out, &errBuf)
	if code != exitDead {
		t.Fatalf("exit %d, want %d: a dead rig that is not last must still count (stderr %q)", code, exitDead, errBuf.String())
	}
	for _, want := range []string{"alpha: DEAD", "zulu: healthy (daemon expects 1, tmux has 1)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report is missing %q:\n%s", want, out.String())
		}
	}
}

// TestListingWorksWithoutRigTool: the no-argument form lists the briefs on disk and needs no rig tool.
func TestListingWorksWithoutRigTool(t *testing.T) {
	home := isolate(t)
	writeBrief(t, home, "alpha")
	var out, errBuf bytes.Buffer
	code := watchdogCommand{}.Run(context.Background(), nil, &out, &errBuf)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want 0 (stderr %q)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "rig with a brief: alpha") {
		t.Errorf("listing did not name the brief:\n%s", out.String())
	}
}
