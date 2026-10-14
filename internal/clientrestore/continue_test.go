package clientrestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
)

// These tests need no network, no rig, no seat, no tmux and no real home: every one of those is a seam
// above. What they pin is the pair of behaviours the shell pair was written for - restore a rig whose
// seats are gone, never re-restore one whose seats are there - plus the rules this client added on top
// (the peak gate before a restore, an argument it does not know refused by name).

// deepSeekNodes is a room whose seat runs a DeepSeek model: the case the gate exists for, since resuming
// it bills the peak rate. The keys are the ones clientoffpeak reads off `rig ps --nodes --json`.
func deepSeekNodes() string {
	return `[{"logicalId":"rig-a.dev","canonicalSessionName":"rig-a-dev@rig-a","rigName":"rig-a",` +
		`"model":"deepseek/deepseek-flash","sessionStatus":"stopped"}]`
}

// plainNodes is a room with no DeepSeek seat anywhere: the gate never has an opinion about it.
func plainNodes() string {
	return `[{"logicalId":"rig-a.dev","canonicalSessionName":"rig-a-dev@rig-a","rigName":"rig-a",` +
		`"model":"anthropic/claude-sonnet-4-5","sessionStatus":"stopped"}]`
}

// world is one test's machine: a stub `rig` on the path the client resolves, a tmux read it controls, a
// home under the test's temp dir, and the records of what happened.
type world struct {
	t     *testing.T
	home  string
	stub  string
	sends []string
	walls []string
	tmux  func() []string
}

// newWorld wires every seam and returns the machine. tmux answers the session names it is given - a
// function rather than a slice so a test can have the seats appear only once the restore has run.
func newWorld(t *testing.T, tmux func() []string, nodes string) *world {
	t.Helper()
	w := &world{t: t, tmux: tmux}
	w.home = t.TempDir()
	for _, dir := range []string{"briefs", "logs", "state"} {
		if err := os.MkdirAll(filepath.Join(w.home, ".openrig", dir), 0o755); err != nil {
			t.Fatalf("preparing %s: %v", dir, err)
		}
	}
	previousHome, previousTmux, previousNow := realHome, tmuxRead, now
	realHome = func() (string, error) { return w.home, nil }
	tmuxRead = func() (string, error) { return strings.Join(w.tmux(), "\n"), nil }
	now = func() time.Time { return time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { realHome, tmuxRead, now = previousHome, previousTmux, previousNow })

	// The stub IS the daemon: the client reaches rig only through Rig.Run, which resolves the tool
	// through clientcmd.LookPath, so pointing that at a script is pointing the whole client at a fake.
	w.stub = filepath.Join(t.TempDir(), "rig")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(filepath.Join(w.home, "calls")) + "\n" +
		"if [ \"$1\" = ps ]; then printf '%s' " + shellQuote(nodes) + "; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(w.stub, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stub rig: %v", err)
	}
	previousLookPath := clientcmd.LookPath
	clientcmd.LookPath = func(string) (string, error) { return w.stub, nil }
	t.Cleanup(func() { clientcmd.LookPath = previousLookPath })

	// THE DAEMON THE NODE READ TALKS TO. The world's node JSON is served over the daemon's OWN route (GET
	// /api/ps, then GET /api/rigs/<id>/nodes), because the gate and the restore now read through
	// clientruntime.RoomNodes rather than through a `rig ps` exec: a stub beside the seam would let this
	// suite pass while the real read was broken. The rig names come from the nodes the test handed in.
	daemon := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/api/ps":
			var rows []map[string]any
			_ = json.Unmarshal([]byte(nodes), &rows)
			seen := map[string]bool{}
			list := []map[string]string{}
			for _, row := range rows {
				name, _ := row["rigName"].(string)
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				list = append(list, map[string]string{"rigId": "01TESTRIG" + name, "name": name, "rigName": name})
			}
			out.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(out).Encode(list)
		case strings.HasPrefix(req.URL.Path, "/api/rigs/") && strings.HasSuffix(req.URL.Path, "/nodes"):
			_, _ = io.WriteString(out, nodes)
		default:
			out.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(daemon.Close)
	t.Setenv("OPENRIG_URL", daemon.URL)
	t.Setenv("OPENRIG_PORT", "")
	// And NO runtime room: the socket names a path nothing listens on, so the runtime branch of the read
	// fails fast and the daemon is the transport that answers - the same order a seat's box takes.
	t.Setenv("OPENRIG_RUNTIME_SOCKET", filepath.Join(t.TempDir(), "no-runtime.sock"))

	// The watch wall is skipped unless a test asks for it: herdr is not on this machine, which is the
	// case the shell logged as a skip rather than a failure.
	previousLook, previousSelf, previousCommand := execLookPath, execSelfPath, execCommand
	execLookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { execLookPath, execSelfPath, execCommand = previousLook, previousSelf, previousCommand })

	// The two steps that reach past the daemon are stubbed for the whole world, and put back at its end,
	// so a suite that runs after this one is never handed a stubbed restorer.
	previousSteps := restoreSteps
	restoreSteps = func(r *restorer) {
		r.send = func(_ context.Context, session, text string) error {
			w.sends = append(w.sends, session+"\t"+text)
			return nil
		}
		r.wall = func(rig string) (bool, string) {
			w.walls = append(w.walls, rig)
			return true, ""
		}
	}
	t.Cleanup(func() { restoreSteps = previousSteps })
	return w
}

// calls is every `rig` invocation the stub recorded, in order.
func (w *world) calls() []string {
	raw, err := os.ReadFile(filepath.Join(w.home, "calls"))
	if err != nil {
		return nil
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

// brief writes a brief file the way the owner does.
func (w *world) brief(name, body string) string {
	w.t.Helper()
	path := filepath.Join(w.home, ".openrig", "briefs", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatalf("brief dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		w.t.Fatalf("brief %s: %v", name, err)
	}
	return path
}

// log is what the verb wrote to its own log file - the record a reader would find after the fact.
func (w *world) log() string {
	raw, _ := os.ReadFile(filepath.Join(w.home, ".openrig", "logs", continueLogName))
	return string(raw)
}

// run drives the verb the way the dispatcher does, with the stub rig on the path. The dispatcher hands a
// verb the arguments AFTER its own name, so the name is dropped here - a test that named the verb and
// passed it through would be testing a different verb.
func (w *world) run(args ...string) (int, string, string) {
	w.t.Helper()
	if len(args) > 0 && args[0] == "continue" {
		args = args[1:]
	}
	var out, errOut bytes.Buffer
	code := (continueCommand{}).Run(context.Background(), nil, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// shellQuote wraps a string for the stub script's sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// staticSessions is a rig whose seats are up: the state a resume must NOT restore.
func staticSessions(names ...string) func() []string {
	return func() []string { return names }
}

// TestALiveRigIsNotRestoredAndItsSeatsAreBriefed is the first half of the rule the shell existed for: a
// second `rig up --existing` on a live rig is what the verb must avoid, and the brief still goes out, in
// the seats' own order, with the shared text.
func TestALiveRigIsNotRestoredAndItsSeatsAreBriefed(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-beta@rig-a", "rig-a-alpha@rig-a", "other-rig-alpha@other-rig"), plainNodes())
	brief := w.brief("rig-a.txt", "Take the next board row.\n")

	code, _, errOut := w.run("continue", "rig-a", brief)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if calls := strings.Join(w.calls(), "|"); strings.Contains(calls, "up rig-a") {
		t.Errorf("the restore ran on a live rig: %v", w.calls())
	}
	want := []string{
		"rig-a-alpha@rig-a\tYour seat: alpha. Take the next board row.",
		"rig-a-beta@rig-a\tYour seat: beta. Take the next board row.",
	}
	if strings.Join(w.sends, "\n") != strings.Join(want, "\n") {
		t.Errorf("briefs sent:\n%q\nwant:\n%q", w.sends, want)
	}
	for _, line := range []string{
		"rig-a: already live, skipping restore",
		"rig-a: seats live: 2",
		"rig-a: briefed alpha",
		"rig-a: briefed beta",
		"rig-a: watch wall opened",
		"rig-a: done — verify with: rig ps --nodes --rig rig-a",
	} {
		if !strings.Contains(w.log(), line) {
			t.Errorf("the log is missing %q:\n%s", line, w.log())
		}
	}
}

// TestADeadRigIsRestoredFromItsSnapshot is the other half: no sessions means `rig up --existing`, and the
// seat count is read AFTER it, which is why the stub's sessions appear only once the restore has run.
func TestADeadRigIsRestoredFromItsSnapshot(t *testing.T) {
	var w *world
	w = newWorld(t, func() []string {
		if strings.Contains(strings.Join(w.calls(), "|"), "up rig-a") {
			return []string{"rig-a-alpha@rig-a"}
		}
		return nil
	}, plainNodes())
	brief := w.brief("rig-a.txt", "Resume the port.\n")

	code, _, errOut := w.run("continue", "rig-a", brief)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(strings.Join(w.calls(), "|"), "up rig-a --existing") {
		t.Errorf("the snapshot restore did not run: %v", w.calls())
	}
	if len(w.sends) != 1 || !strings.HasPrefix(w.sends[0], "rig-a-alpha@rig-a\tYour seat: alpha. Resume the port.") {
		t.Errorf("briefs sent = %q, want one to alpha with the shared text", w.sends)
	}
	if !strings.Contains(w.log(), "rig-a: restoring from latest snapshot") {
		t.Errorf("the log does not say it restored:\n%s", w.log())
	}
}

// TestASeatWithItsOwnBriefGetsThatInsteadOfTheSharedOne: briefs/<rig>/<seat>.txt replaces the shared text
// for that seat, so no generic paragraph is pushed onto a seat that has its own.
func TestASeatWithItsOwnBriefGetsThatInsteadOfTheSharedOne(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a", "rig-a-beta@rig-a"), plainNodes())
	shared := w.brief("rig-a.txt", "Shared text\n")
	w.brief(filepath.Join("rig-a", "beta.txt"), "Beta only\n")

	if code, _, errOut := w.run("continue", "rig-a", shared); code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if len(w.sends) != 2 {
		t.Fatalf("briefs = %q, want two", w.sends)
	}
	if !strings.HasSuffix(w.sends[0], "Shared text") {
		t.Errorf("alpha = %q, want the shared text", w.sends[0])
	}
	if !strings.HasSuffix(w.sends[1], "Beta only") {
		t.Errorf("beta = %q, want its own text", w.sends[1])
	}
}

// TestNoSeatsUpAbortsForThatRig: a restore that leaves nothing up cannot brief anyone, so the verb fails
// rather than reporting a success it did not get. This is the state a dead tmux server leaves behind.
func TestNoSeatsUpAbortsForThatRig(t *testing.T) {
	w := newWorld(t, staticSessions(), plainNodes())
	brief := w.brief("rig-a.txt", "Text\n")

	code, _, _ := w.run("continue", "rig-a", brief)
	if code != ExitRestoreFailed {
		t.Fatalf("exit = %d, want %d", code, ExitRestoreFailed)
	}
	if len(w.sends) != 0 {
		t.Errorf("briefs went out to no seats: %q", w.sends)
	}
	if !strings.Contains(w.log(), "rig-a: no seats up, aborting for this rig") {
		t.Errorf("the log does not say why it stopped:\n%s", w.log())
	}
}

// TestAMissingBriefFileIsReportedAndFailsTheRun: the shell restored and then said it had nothing to send.
// The restore happened - that work is not undone - but the run's answer is a failure, because the caller
// asked for a briefing and did not get one.
func TestAMissingBriefFileIsReportedAndFailsTheRun(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())

	code, _, _ := w.run("continue", "rig-a", filepath.Join(w.home, "nope.txt"))
	if code != ExitRestoreFailed {
		t.Fatalf("exit = %d, want %d", code, ExitRestoreFailed)
	}
	if !strings.Contains(w.log(), "brief file missing") {
		t.Errorf("the log does not name the missing brief:\n%s", w.log())
	}
}

// TestNoBriefRequestedSendsNothing: --no-brief is a restore with no words, and it must not fall back to
// the default brief file - the caller said not to brief.
func TestNoBriefRequestedSendsNothing(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())
	w.brief("rig-a.txt", "Default text that must NOT be sent\n")

	code, _, _ := w.run("continue", "rig-a", "--no-brief")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if len(w.sends) != 0 {
		t.Errorf("--no-brief sent %q", w.sends)
	}
	if !strings.Contains(w.log(), "rig-a: no brief requested") {
		t.Errorf("the log does not say the brief was skipped:\n%s", w.log())
	}
}

// gateEnv pins the clock and the setting, and empties the holiday file, so a case is decided by the
// schedule alone unless it says otherwise.
func gateEnv(t *testing.T, pin string) {
	t.Helper()
	t.Setenv(clientoffpeak.HolidaysEnv, "")
	t.Setenv(clientoffpeak.ModeEnv, "")
	t.Setenv(clientoffpeak.PinEnv, pin)
}

// TestTheGateRefusesBeforeTheRestoreAtPeak is the rule this client added: the shell restored a room
// without asking what model its seats run, so a resume at the peak rate started DeepSeek work at the peak
// price. The refusal must come BEFORE the restore, and it must reach the log, not only the caller.
func TestTheGateRefusesBeforeTheRestoreAtPeak(t *testing.T) {
	w := newWorld(t, staticSessions(), deepSeekNodes())
	gateEnv(t, "2026-10-08 07:00") // Thursday 07:00Z: inside the 06:00-10:00 peak block
	brief := w.brief("rig-a.txt", "Text\n")

	code, _, errOut := w.run("continue", "rig-a", brief)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut)
	}
	if calls := strings.Join(w.calls(), "|"); strings.Contains(calls, "up rig-a") {
		t.Errorf("the seats were resumed despite the refusal: %v", w.calls())
	}
	if !strings.Contains(errOut, clientoffpeak.FlagAllowPeak) {
		t.Errorf("the refusal does not name the override: %q", errOut)
	}
	if !strings.Contains(w.log(), "deepseek/") {
		t.Errorf("the refusal is missing from the log:\n%s", w.log())
	}
}

// TestAllowPeakRestoresTheSameRoom: the override is one run's decision, and with it the restore happens.
func TestAllowPeakRestoresTheSameRoom(t *testing.T) {
	w := newWorld(t, staticSessions(), deepSeekNodes())
	gateEnv(t, "2026-10-08 07:00")

	code, _, errOut := w.run("continue", "rig-a", "--allow-peak", "--no-brief")
	if code != ExitRestoreFailed { // the stub restore leaves no seats up, which is the point of the next case
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitRestoreFailed, errOut)
	}
	if !strings.Contains(strings.Join(w.calls(), "|"), "up rig-a --existing") {
		t.Errorf("--allow-peak did not restore: %v", w.calls())
	}
}

// TestAnAlreadyLiveRigIsNotGated: the gate exists to stop seats STARTING. A rig whose seats are already
// there has nothing to start, and its brief is text to seats that are running - so a peak reading must not
// refuse it, which is also what a caller watching a live room expects.
func TestAnAlreadyLiveRigIsNotGated(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), deepSeekNodes())
	gateEnv(t, "2026-10-08 07:00")
	brief := w.brief("rig-a.txt", "Text\n")

	code, _, errOut := w.run("continue", "rig-a", brief)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0: a live rig is not gated (stderr %q)", code, errOut)
	}
	if len(w.sends) != 1 {
		t.Errorf("the live rig was not briefed: %q", w.sends)
	}
}

// TestTheDefaultBriefIsTheRigsOwnFile: no second argument means <home>/.openrig/briefs/<rig>.txt, which is
// the path the help, the listing and this default must agree on.
func TestTheDefaultBriefIsTheRigsOwnFile(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())
	w.brief("rig-a.txt", "Default text\n")

	if code, _, errOut := w.run("continue", "rig-a"); code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if len(w.sends) != 1 || !strings.HasSuffix(w.sends[0], "Default text") {
		t.Errorf("briefs = %q, want the default file's text", w.sends)
	}
}

// TestAllContinuesEveryRigThatHasABrief, one at a time, and does not stop at the first rig that fails.
func TestAllContinuesEveryRigThatHasABrief(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a", "rig-b-alpha@rig-b"), plainNodes())
	w.brief("rig-b.txt", "B text\n")
	w.brief("rig-a.txt", "A text\n") // sorted, so rig-a is briefed first

	code, _, _ := w.run("continue", "--all")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if len(w.sends) != 2 {
		t.Fatalf("briefs = %q, want both rigs", w.sends)
	}
	if !strings.Contains(w.sends[0], "Your seat: alpha. A text") || !strings.Contains(w.sends[1], "Your seat: alpha. B text") {
		t.Errorf("briefs = %q, want rig-a then rig-b", w.sends)
	}
}

// TestAllWithNoBriefsSaysSo: the shell logged that there was nothing to do rather than printing nothing.
func TestAllWithNoBriefsSaysSo(t *testing.T) {
	w := newWorld(t, staticSessions(), plainNodes())
	code, _, _ := w.run("continue", "--all")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(w.log(), "--all: no brief files in") {
		t.Errorf("the log does not say there was nothing to continue:\n%s", w.log())
	}
}

// TestAllReportsARigItCouldNotBringUp: the shell's `|| true` inside the loop is kept - the rigs after a
// failure are still visited - but the run no longer ends 0 whatever happened, so a caller reading only the
// exit code can tell a run that resumed every room from one that did not. The rig that could not come up
// is named in the log, which is the record a reader has after the fact.
func TestAllReportsARigItCouldNotBringUp(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())
	w.brief("rig-a.txt", "A text\n")
	w.brief("rig-b.txt", "B text\n") // no seat of rig-b is up, so its restore cannot end with a live room

	code, _, _ := w.run("continue", "--all")
	if code != ExitRestoreFailed {
		t.Fatalf("exit = %d, want %d", code, ExitRestoreFailed)
	}
	if !strings.Contains(w.log(), "rig-b: no seats up, aborting for this rig") {
		t.Errorf("the log does not say rig-b could not be brought up:\n%s", w.log())
	}
	if !strings.Contains(w.log(), "--all: 1 of 2 rigs are not up: rig-b") {
		t.Errorf("the summary does not name rig-b:\n%s", w.log())
	}
	if !strings.Contains(w.log(), "rig-a: already live, skipping restore") {
		t.Errorf("the first rig was not visited before the failing one:\n%s", w.log())
	}
	if len(w.sends) != 1 || !strings.Contains(w.sends[0], "A text") {
		t.Errorf("briefs = %q, want the live rig's own", w.sends)
	}
}

// TestAllCountsARefusedRig: a peak refusal is deliberate, not a failure - but the run visited that room and
// left it stopped, so it is not a run that brought everything up either. Exit non-zero, the room named, and
// nothing started for it: --allow-peak is how a caller asks for the peak-hour restore.
func TestAllCountsARefusedRig(t *testing.T) {
	bothDeepSeek := `[{"logicalId":"rig-a.dev","canonicalSessionName":"rig-a-dev@rig-a","rigName":"rig-a",` +
		`"model":"deepseek/deepseek-flash","sessionStatus":"stopped"},` +
		`{"logicalId":"rig-b.dev","canonicalSessionName":"rig-b-dev@rig-b","rigName":"rig-b",` +
		`"model":"deepseek/deepseek-flash","sessionStatus":"stopped"}]`
	w := newWorld(t, staticSessions(), bothDeepSeek)
	gateEnv(t, "2026-10-08 07:00") // Thursday 07:00Z: inside the 06:00-10:00 peak block
	w.brief("rig-a.txt", "A text\n")
	w.brief("rig-b.txt", "B text\n")

	code, _, _ := w.run("continue", "--all")
	if code != ExitRestoreFailed {
		t.Fatalf("exit = %d, want %d", code, ExitRestoreFailed)
	}
	if !strings.Contains(w.log(), "--all: 2 of 2 rigs are not up: rig-a, rig-b") {
		t.Errorf("the summary does not name both refused rigs:\n%s", w.log())
	}
	for _, call := range w.calls() {
		if strings.Contains(call, "up ") {
			t.Errorf("a refused rig was restored anyway: %q", call)
		}
	}
}

// TestAnArgumentTheVerbDoesNotKnowIsRefusedByName: the client's rule. The shell silently read an unknown
// second argument as a brief PATH and ignored everything after it, so a caller who mistyped got a restore
// with a briefing that never happened.
func TestAnArgumentTheVerbDoesNotKnowIsRefusedByName(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())
	for _, args := range [][]string{
		{"continue", "--wat", "rig-a"},
		{"continue", "-x"},
		{"continue", "rig-a", "--wat"},
		{"continue", "rig-a", "--no-brief", "extra"},
		{"continue", "rig-a", "brief.txt", "extra"},
		{"continue", "--all", "rig-a"},
	} {
		code, _, errOut := w.run(args...)
		if code != clientcmd.ExitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, clientcmd.ExitUsage)
		}
		if errOut == "" {
			t.Errorf("%v: refused with nothing on stderr", args)
		}
	}
	if len(w.calls()) != 0 {
		t.Errorf("a refused invocation still ran rig: %v", w.calls())
	}
}

// TestTheWallIsSkippedWithoutATerminalHost: herdr hosts the wall, and the shell logged a skip rather than
// a failure on a machine that has no wall. RIG_CONTINUE_WATCH=0 is the other way out, for a headless run.
func TestTheWallIsSkippedWithoutATerminalHost(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())
	w.run("continue", "rig-a", "--no-brief")
	if !strings.Contains(w.log(), "rig-a: watch wall opened") {
		t.Errorf("the stubbed wall did not report opening:\n%s", w.log())
	}

	// Now the real wall step with no herdr on the machine: a skip with its reason.
	opened, why := watchWall("rig-a")
	if opened || why != wallNoTerminal {
		t.Errorf("watchWall with no herdr = (%v, %q), want (false, %q)", opened, why, wallNoTerminal)
	}

	w2 := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())
	t.Setenv(watchEnv, "0")
	w2.run("continue", "rig-a", "--no-brief")
	if len(w2.walls) != 0 {
		t.Errorf("RIG_CONTINUE_WATCH=0 still asked for the wall: %v", w2.walls)
	}
	if strings.Contains(w2.log(), "watch wall") {
		t.Errorf("a skipped wall should log nothing about the wall:\n%s", w2.log())
	}
}

// TestTheWallIsThisSameBinaryWithTheWatchArguments pins the wall's own contract: herdr must be there, and
// the wall is `4genteam watch --rig RIG` - the shell ran `4genteam watch watch --rig RIG`, whose group and
// sub-verb are one verb here.
func TestTheWallIsThisSameBinaryWithTheWatchArguments(t *testing.T) {
	var started []string
	previousLook, previousSelf, previousCommand := execLookPath, execSelfPath, execCommand
	execLookPath = func(string) (string, error) { return "/stub/herdr", nil }
	execSelfPath = func() (string, error) { return "/stub/4genteam", nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		started = append(started, strings.Join(append([]string{name}, args...), " "))
		return exec.Command("/bin/true")
	}
	t.Cleanup(func() { execLookPath, execSelfPath, execCommand = previousLook, previousSelf, previousCommand })

	opened, why := watchWall("rig-a")
	if !opened {
		t.Fatalf("watchWall = (false, %q), want it opened", why)
	}
	if len(started) != 1 || started[0] != "/stub/4genteam watch --rig rig-a" {
		t.Errorf("started %v, want `/stub/4genteam watch --rig rig-a`", started)
	}
}

// TestTheListingNeedsNoRigTool: the briefs on disk are the one thing a reader wants on a machine that has
// no daemon, which is why this verb resolves the rig tool itself instead of asking the dispatcher to.
func TestTheListingNeedsNoRigTool(t *testing.T) {
	w := newWorld(t, staticSessions(), plainNodes())
	w.brief("rig-a.txt", "Text\n")
	previousLookPath := clientcmd.LookPath
	clientcmd.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { clientcmd.LookPath = previousLookPath })

	code, out, _ := w.run("continue")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"Rigs that can be continued:", "rig is not on PATH", "briefs on disk:", "  rig-a", continueListingUsage} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing is missing %q:\n%s", want, out)
		}
	}
	// The usage line is printed once: the shell's own, not continueUsage behind a "Usage: " prefix, which
	// already carries one of its own.
	if strings.Contains(out, "Usage: usage:") {
		t.Errorf("the listing repeats the prefix:\n%s", out)
	}
}

// TestTheRestoreFormsRefuseWithNoRigTool: the forms that need the daemon give the same one message every
// other verb gives, rather than a nil-pointer panic or a half-restore. `continue <rig>` needs the rig tool
// for the restore; `--all` needs it only once there IS a brief to continue - with none, there is nothing to
// do and nothing to refuse, which is what the shell answered too.
func TestTheRestoreFormsRefuseWithNoRigTool(t *testing.T) {
	w := newWorld(t, staticSessions(), plainNodes())
	previousLookPath := clientcmd.LookPath
	clientcmd.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { clientcmd.LookPath = previousLookPath })

	if code, _, _ := w.run("continue", "--all"); code != clientcmd.ExitOK {
		t.Errorf("--all with no briefs: exit = %d, want 0: there is nothing to do, so nothing to refuse", code)
	}
	w.brief("rig-a.txt", "Text\n")
	for _, args := range [][]string{{"continue", "rig-a", "--no-brief"}, {"continue", "--all"}} {
		code, _, errOut := w.run(args...)
		if code != clientcmd.ExitUnavailable {
			t.Errorf("%v: exit = %d, want %d", args, code, clientcmd.ExitUnavailable)
		}
		if !strings.Contains(errOut, "rig is not on PATH") {
			t.Errorf("%v: stderr = %q, want the platform matrix's reason", args, errOut)
		}
	}
}

// TestHelpIsAnsweredBeforeAnythingElse: help must not open a log, read tmux or touch a brief.
func TestHelpIsAnsweredBeforeAnythingElse(t *testing.T) {
	w := newWorld(t, func() []string { t.Error("help read tmux"); return nil }, plainNodes())
	code, out, _ := w.run("continue", "--help")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{continueUsage, clientoffpeak.ModeEnv, clientoffpeak.FlagAllowPeak, "--all"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help is missing %q:\n%s", want, out)
		}
	}
}

// TestRigSessionsReadsOnlyThisRigsSeats: a name that merely contains the rig's name is another rig's seat,
// and counting it would make the watchdog think a dead rig is up - the one mistake that silences it.
func TestRigSessionsReadsOnlyThisRigsSeats(t *testing.T) {
	names := []string{
		"rig-a-beta@rig-a",
		"rig-a-alpha@rig-a",
		"other-rig-a-alpha@other-rig-a",
		"rig-a@rig-a",
		"rig-aa-alpha@rig-aa",
		"rig-a-late@rig-a-x",
		"",
	}
	if got := strings.Join(rigSeats("rig-a", names), ","); got != "alpha,beta" {
		t.Errorf("rigSeats = %q, want alpha,beta (only <rig>-<seat>@<rig>)", got)
	}
	if got := len(rigSessions("rig-a", names)); got != 2 {
		t.Errorf("rigSessions = %d, want 2", got)
	}
}

// TestAMissingTmuxServerReadsAsNoSessions: a machine whose tmux server is gone is exactly the crash this
// pair exists for, so the read must answer "none" rather than fail.
func TestAMissingTmuxServerReadsAsNoSessions(t *testing.T) {
	previous := tmuxRead
	tmuxRead = func() (string, error) { return "", errors.New("no server running on /tmp/tmux-1000/default") }
	t.Cleanup(func() { tmuxRead = previous })
	if got := sessionNames(); len(got) != 0 {
		t.Errorf("sessionNames = %q, want none", got)
	}
}

// TestTheLogLineKeepsTheShellsShape: `date -Is` then the message, so a log the client writes and the log
// the script wrote read as one file rather than two dialects.
func TestTheLogLineKeepsTheShellsShape(t *testing.T) {
	w := newWorld(t, staticSessions("rig-a-alpha@rig-a"), plainNodes())
	w.run("continue", "rig-a", "--no-brief")
	first := strings.SplitN(w.log(), "\n", 2)[0]
	if !strings.HasPrefix(first, "2026-10-10T15:00:00+00:00 ") {
		t.Errorf("log line = %q, want the pinned RFC3339 stamp first", first)
	}
}

// TestTheBriefIsRefusedInWordsWhenNoTransportCanDeliverIt drives the REAL delivery seam rather than the
// stubbed one, because this is the seam whose native branch was unreachable: it looked for the Python-era
// agenthub--<room>--<seat> session shape, which the sessions this rig actually has do not match, then
// exec'd `rig send` and SWALLOWED its failure - so a brief for 4genthub-min-reviewer@4genthub-min was
// reported as briefed and dropped. The seal is the pair of facts below: the delivery is refused for the
// real session shape, and the refusal says what could not deliver it.
func TestTheBriefIsRefusedInWordsWhenNoTransportCanDeliverIt(t *testing.T) {
	home := t.TempDir()
	previousHome := realHome
	realHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { realHome = previousHome })

	// No transport at all: no runtime socket, no OpenRig daemon, no seat environment.
	for _, name := range []string{"OPENRIG_URL", "OPENRIG_PORT", "OPENRIG_SESSION_NAME", "RIGGED_SESSION_NAME",
		"AGENTHUB_ROOM", "AGENTHUB_SEAT", "AGENTHUB_SESSION_NAME"} {
		t.Setenv(name, "")
	}
	t.Setenv("OPENRIG_RUNTIME_SOCKET", filepath.Join(t.TempDir(), "no-runtime.sock"))

	var out bytes.Buffer
	r, err := newRestorer(nil, &teeLog{out: &out})
	if err != nil {
		t.Fatalf("newRestorer: %v", err)
	}
	if err := r.send(context.Background(), "4genthub-min-reviewer@4genthub-min", "Your seat: reviewer. hello"); err == nil {
		t.Fatal("a brief no transport delivered was reported as delivered")
	} else if !strings.Contains(err.Error(), "no runtime is answering") {
		t.Errorf("the refusal does not name the transport that did not answer: %v", err)
	}
}
