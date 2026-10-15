package clientrestore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
)

// These tests need no network, no rig, no seat, no tmux and no real home: every one of those is a seam
// above. What they pin is the pair of behaviours the shell pair was written for - restore a rig whose
// seats are gone, never re-restore one whose seats are there - plus the rules this client added on top
// (the peak gate before a restore, an argument it does not know refused by name).

// deepSeekNodes is a room whose seat runs a DeepSeek model: the case the gate exists for, since resuming
// it bills the peak rate. The keys are the ones clientoffpeak reads off `rig ps --nodes --json`.
func deepSeekNodes() string {
	return `[{"logicalId":"rig-a.dev","canonicalSessionName":"agenthub--rig-a--dev","rigName":"rig-a",` +
		`"model":"deepseek/deepseek-flash","sessionStatus":"stopped"}]`
}

// plainNodes is a room with no DeepSeek seat anywhere: the gate never has an opinion about it.
func plainNodes() string {
	return `[{"logicalId":"rig-a.dev","canonicalSessionName":"agenthub--rig-a--dev","rigName":"rig-a",` +
		`"model":"anthropic/claude-sonnet-4-5","sessionStatus":"stopped"}]`
}

// seatStatuses is the runtime's GetRoomStatus answer for one room, built from the world's node rows: the seat
// half of the logical id, the canonical session name, the model and the state, in the keys the runtime sends.
func seatStatuses(nodes, room string) []map[string]any {
	var rows []map[string]any
	_ = json.Unmarshal([]byte(nodes), &rows)
	statuses := []map[string]any{}
	for _, row := range rows {
		if name, _ := row["rigName"].(string); name != room {
			continue
		}
		logical, _ := row["logicalId"].(string)
		statuses = append(statuses, map[string]any{
			"room": room, "seat": strings.TrimPrefix(logical, room+"."), "session_name": row["canonicalSessionName"],
			"runtime": "omp", "model": row["model"], "state": row["sessionStatus"],
		})
	}
	return statuses
}

// roomsOf is the rooms the world's node rows name, in first-seen order.
func roomsOf(nodes string) []string {
	var rows []map[string]any
	_ = json.Unmarshal([]byte(nodes), &rows)
	rooms := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		if name, _ := row["rigName"].(string); name != "" && !seen[name] {
			seen[name] = true
			rooms = append(rooms, name)
		}
	}
	return rooms
}

// serveRuntime answers the runtime socket for the world: ListAllRooms with the rooms the nodes name and
// GetRoomStatus with that room's seats. The unix socket is the transport the reads really use, so a stub beside
// the seam could not let this suite pass while the real read was broken. The path is a short temp dir: a unix
// socket path is capped near 108 bytes.
func serveRuntime(t *testing.T, answer func(action string, payload json.RawMessage) string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "crst")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return // the reachability probe: dialled and closed with no line
				}
				var frame struct {
					Action  string          `json:"action"`
					Payload json.RawMessage `json:"payload"`
				}
				_ = json.Unmarshal(line, &frame)
				_, _ = io.WriteString(conn, answer(frame.Action, frame.Payload))
			}(conn)
		}
	}()
	t.Setenv("AGENTHUB_RUNTIME_SOCKET", sock)
}

// runtimeData wraps a document in the runtime's ok answer.
func runtimeData(v any) string {
	raw, _ := json.Marshal(map[string]any{"ok": true, "data": v})
	return string(raw) + "\n"
}

// world is one test's machine: stubbed runtime steps, a tmux read it controls, a
// home under the test's temp dir, and the records of what happened.
type world struct {
	t     *testing.T
	home  string
	sends []string
	walls []string
	tmux  func() []string
	// listFails makes the runtime refuse the room list: the read that failed.
	listFails bool
}

// newWorld wires every seam and returns the machine. tmux answers the session names it is given - a
// function rather than a slice so a test can have the seats appear only once the restore has run.
func newWorld(t *testing.T, tmux func() []string, nodes string) *world {
	t.Helper()
	w := &world{t: t, tmux: tmux}
	w.home = t.TempDir()
	for _, dir := range []string{"briefs", "logs", "state"} {
		if err := os.MkdirAll(filepath.Join(w.home, ".config", "4genthub", dir), 0o755); err != nil {
			t.Fatalf("preparing %s: %v", dir, err)
		}
	}
	previousHome, previousTmux, previousNow := realHome, tmuxRead, now
	realHome = func() (string, error) { return w.home, nil }
	tmuxRead = func() (string, error) { return strings.Join(w.tmux(), "\n"), nil }
	now = func() time.Time { return time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { realHome, tmuxRead, now = previousHome, previousTmux, previousNow })

	// THE RUNTIME THE READS TALK TO: the room list and each room's seats, over the runtime's own socket.
	serveRuntime(t, func(action string, payload json.RawMessage) string {
		switch action {
		case "ListAllRooms":
			if w.listFails {
				return `{"ok":false,"error":"the room table is not here"}` + "\n"
			}
			return runtimeData(roomsOf(nodes))
		case "GetRoomStatus":
			var asked struct {
				Room string `json:"room"`
			}
			_ = json.Unmarshal(payload, &asked)
			return runtimeData(seatStatuses(nodes, asked.Room))
		}
		return `{"ok":false,"error":"unexpected action ` + action + `"}` + "\n"
	})

	// The watch wall is skipped unless a test asks for it: herdr is not on this machine, which is the
	// case the shell logged as a skip rather than a failure.
	previousLook, previousSelf, previousCommand := execLookPath, execSelfPath, execCommand
	execLookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { execLookPath, execSelfPath, execCommand = previousLook, previousSelf, previousCommand })

	// The two steps that reach past the runtime are stubbed for the whole world, and put back at its end,
	// so a suite that runs after this one is never handed a stubbed restorer.
	previousSteps := restoreSteps
	restoreSteps = func(r *restorer) {
		r.ensureDaemon = func(context.Context) error { w.record("daemon up"); return nil }
		r.revive = func(_ context.Context, room string) error { w.record("up " + room + " --existing"); return nil }
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

// record notes one runtime step the restorer took, in order.
func (w *world) record(call string) {
	handle, err := os.OpenFile(filepath.Join(w.home, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		w.t.Fatalf("recording %q: %v", call, err)
	}
	defer handle.Close()
	_, _ = handle.WriteString(call + "\n")
}

// calls is every runtime step the restorer took, in order.
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
	path := filepath.Join(w.home, ".config", "4genthub", "briefs", name)
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
	raw, _ := os.ReadFile(filepath.Join(w.home, ".config", "4genthub", "logs", continueLogName))
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
	code := (continueCommand{}).Run(context.Background(), args, &out, &errOut)
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
	w := newWorld(t, staticSessions("agenthub--rig-a--beta", "agenthub--rig-a--alpha", "agenthub--other-rig--alpha"), plainNodes())
	brief := w.brief("rig-a.txt", "Take the next board row.\n")

	code, _, errOut := w.run("continue", "rig-a", brief)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if calls := strings.Join(w.calls(), "|"); strings.Contains(calls, "up rig-a") {
		t.Errorf("the restore ran on a live rig: %v", w.calls())
	}
	want := []string{
		"agenthub--rig-a--alpha\tYour seat: alpha. Take the next board row.",
		"agenthub--rig-a--beta\tYour seat: beta. Take the next board row.",
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
		"rig-a: done — verify with: 4genteam ps rig-a --nodes",
	} {
		if !strings.Contains(w.log(), line) {
			t.Errorf("the log is missing %q:\n%s", line, w.log())
		}
	}
}

// TestAnUndeliveredBriefIsAFailedRestore: the seats are up but the brief the caller asked for did not arrive,
// and that is not a success and must not be reported as one. The restore answers false - which is what the
// restorer's own doc promises ("a brief the caller asked for is not there to send") - the seats that were
// missed are named, and the verb leaves non-zero. Before this, each seat was logged as failed and the verb
// still ended saying done, which is the shape R3 forbids: a delivery reported that never happened.
func TestAnUndeliveredBriefIsAFailedRestore(t *testing.T) {
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
	brief := w.brief("rig-a.txt", "Take the next board row.\n")

	previousSteps := restoreSteps
	restoreSteps = func(r *restorer) {
		r.ensureDaemon = func(context.Context) error { w.record("daemon up"); return nil }
		r.revive = func(_ context.Context, room string) error { w.record("up " + room + " --existing"); return nil }
		r.send = func(context.Context, string, string) error { return errors.New("no transport answered") }
		r.wall = func(string) (bool, string) { return true, "" }
	}
	t.Cleanup(func() { restoreSteps = previousSteps })

	code, _, _ := w.run("continue", "rig-a", brief)
	if code != ExitRestoreFailed {
		t.Fatalf("exit = %d, want %d: a brief that did not arrive is not a completed restore", code, ExitRestoreFailed)
	}
	for _, line := range []string{"rig-a: send failed for alpha", "rig-a: brief did not reach alpha"} {
		if !strings.Contains(w.log(), line) {
			t.Errorf("the log is missing %q:\n%s", line, w.log())
		}
	}
	if strings.Contains(w.log(), "rig-a: done") {
		t.Errorf("an undelivered brief was reported as done:\n%s", w.log())
	}
}

// TestADeadRigIsRestoredFromItsSnapshot is the other half: no sessions means `rig up --existing`, and the
// seat count is read AFTER it, which is why the stub's sessions appear only once the restore has run.
func TestADeadRigIsRestoredFromItsSnapshot(t *testing.T) {
	var w *world
	w = newWorld(t, func() []string {
		if strings.Contains(strings.Join(w.calls(), "|"), "up rig-a") {
			return []string{"agenthub--rig-a--alpha"}
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
	if len(w.sends) != 1 || !strings.HasPrefix(w.sends[0], "agenthub--rig-a--alpha\tYour seat: alpha. Resume the port.") {
		t.Errorf("briefs sent = %q, want one to alpha with the shared text", w.sends)
	}
	if !strings.Contains(w.log(), "rig-a: restoring from latest snapshot") {
		t.Errorf("the log does not say it restored:\n%s", w.log())
	}
}

// TestASeatWithItsOwnBriefGetsThatInsteadOfTheSharedOne: briefs/<rig>/<seat>.txt replaces the shared text
// for that seat, so no generic paragraph is pushed onto a seat that has its own.
func TestASeatWithItsOwnBriefGetsThatInsteadOfTheSharedOne(t *testing.T) {
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha", "agenthub--rig-a--beta"), plainNodes())
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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())

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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), deepSeekNodes())
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

// TestTheDefaultBriefIsTheRigsOwnFile: no second argument means <home>/.config/4genthub/briefs/<rig>.txt, which is
// the path the help, the listing and this default must agree on.
func TestTheDefaultBriefIsTheRigsOwnFile(t *testing.T) {
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha", "agenthub--rig-b--alpha"), plainNodes())
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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
	w.brief("rig-a.txt", "A text\n")
	w.brief("rig-b.txt", "B text\n") // no seat of rig-b is up, so its restore cannot end with a live room

	code, _, _ := w.run("continue", "--all")
	if code != ExitRestoreFailed {
		t.Fatalf("exit = %d, want %d", code, ExitRestoreFailed)
	}
	if !strings.Contains(w.log(), "rig-b: no seats up, aborting for this rig") {
		t.Errorf("the log does not say rig-b could not be brought up:\n%s", w.log())
	}
	if !strings.Contains(w.log(), "--all: 1 of 2 rigs did not complete: rig-b") {
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
	bothDeepSeek := `[{"logicalId":"rig-a.dev","canonicalSessionName":"agenthub--rig-a--dev","rigName":"rig-a",` +
		`"model":"deepseek/deepseek-flash","sessionStatus":"stopped"},` +
		`{"logicalId":"rig-b.dev","canonicalSessionName":"agenthub--rig-b--dev","rigName":"rig-b",` +
		`"model":"deepseek/deepseek-flash","sessionStatus":"stopped"}]`
	w := newWorld(t, staticSessions(), bothDeepSeek)
	gateEnv(t, "2026-10-08 07:00") // Thursday 07:00Z: inside the 06:00-10:00 peak block
	w.brief("rig-a.txt", "A text\n")
	w.brief("rig-b.txt", "B text\n")

	code, _, _ := w.run("continue", "--all")
	if code != ExitRestoreFailed {
		t.Fatalf("exit = %d, want %d", code, ExitRestoreFailed)
	}
	if !strings.Contains(w.log(), "--all: 2 of 2 rigs did not complete: rig-a, rig-b") {
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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
	w.run("continue", "rig-a", "--no-brief")
	if !strings.Contains(w.log(), "rig-a: watch wall opened") {
		t.Errorf("the stubbed wall did not report opening:\n%s", w.log())
	}

	// Now the real wall step with no herdr on the machine: a skip with its reason.
	opened, why := watchWall("rig-a")
	if opened || why != wallNoTerminal {
		t.Errorf("watchWall with no herdr = (%v, %q), want (false, %q)", opened, why, wallNoTerminal)
	}

	w2 := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
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
// the wall is `4genteam watch --room RIG` - the shell ran `4genteam watch watch --room RIG`, whose group and
// sub-verb are one verb here.
func TestTheWallIsThisSameBinaryWithTheWatchArguments(t *testing.T) {
	var started []string
	home := t.TempDir()
	previousHome := realHome
	realHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { realHome = previousHome })
	if err := os.MkdirAll(clientenv.StateSubdir(home, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	if len(started) != 1 || started[0] != "/stub/4genteam watch --room rig-a" {
		t.Errorf("started %v, want `/stub/4genteam watch --room rig-a`", started)
	}
}

// TestTheListingNeedsNoRigTool: the room lines come from the runtime and the briefs from the disk. The
// output is pinned whole, surroundings included, so the section's shape cannot drift; the rooms are served
// out of order, so name order and each room's one-word state (running, partial, stopped) are asserted.
func TestTheListingNeedsNoRigTool(t *testing.T) {
	nodes := `[` +
		`{"logicalId":"rig-b.one","canonicalSessionName":"agenthub--rig-b--one","rigName":"rig-b","model":"m","sessionStatus":"running"},` +
		`{"logicalId":"rig-b.two","canonicalSessionName":"agenthub--rig-b--two","rigName":"rig-b","model":"m","sessionStatus":"stopped"},` +
		`{"logicalId":"rig-a.dev","canonicalSessionName":"agenthub--rig-a--dev","rigName":"rig-a","model":"m","sessionStatus":"running"},` +
		`{"logicalId":"rig-z.dev","canonicalSessionName":"agenthub--rig-z--dev","rigName":"rig-z","model":"m","sessionStatus":"stopped"}]`
	w := newWorld(t, staticSessions(), nodes)
	w.brief("rig-a.txt", "Text\n")

	code, out, _ := w.run("continue")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := "Rigs that can be continued:\n" +
		"  rig-a  running\n" +
		"  rig-b  partial\n" +
		"  rig-z  stopped\n" +
		"\n" +
		"briefs on disk:\n" +
		"  rig-a\n" +
		"\n" +
		continueListingUsage + "\n"
	if out != want {
		t.Errorf("the listing printed:\n%s\nwant:\n%s", out, want)
	}
	// The usage line is printed once: the shell's own, not continueUsage behind a "Usage: " prefix, which
	// already carries one of its own.
	if strings.Contains(out, "Usage: usage:") {
		t.Errorf("the listing repeats the prefix:\n%s", out)
	}
	if calls := w.calls(); len(calls) != 0 {
		t.Errorf("the listing ran rig: %v", calls)
	}
}

// TestTheListingSaysSoWhenTheRoomListCannotBeRead: the room section is a runtime read, so THAT is what can
// fail - and it fails in its own one line while the briefs below it still print and the run still ends 0.
func TestTheListingSaysSoWhenTheRoomListCannotBeRead(t *testing.T) {
	w := newWorld(t, staticSessions(), plainNodes())
	w.brief("rig-a.txt", "Text\n")
	w.listFails = true

	code, out, _ := w.run("continue")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0: the briefs are still listed", code)
	}
	for _, want := range []string{
		"Rigs that can be continued:",
		"  (the room list could not be read from the runtime: ",
		"the room table is not here",
		"briefs on disk:",
		"  rig-a\n",
		continueListingUsage,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "  rig-a  ") {
		t.Errorf("a read that failed printed a room line:\n%s", out)
	}
}

// TestTheListingSaysWhenNoRuntimeAnswers: with no runtime there is no room list to read, and that is not a
// failure of the verb - the exit stays 0 and the briefs below print as usual. The line is printed rather than
// the section left empty, because an empty section reads as "no rooms here".
func TestTheListingSaysWhenNoRuntimeAnswers(t *testing.T) {
	w := newWorld(t, staticSessions(), plainNodes())
	w.brief("rig-a.txt", "Text\n")
	t.Setenv("AGENTHUB_RUNTIME_SOCKET", filepath.Join(t.TempDir(), "no-runtime.sock"))

	code, out, _ := w.run("continue")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "  (the room list could not be read from the runtime: ") || !strings.Contains(out, "  rig-a\n") {
		t.Errorf("the listing printed:\n%s", out)
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
		"agenthub--rig-a--beta",
		"agenthub--rig-a--alpha",
		"agenthub--other-rig-a--alpha",
		"rig-a@rig-a",
		"agenthub--rig-aa--alpha",
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
	w := newWorld(t, staticSessions("agenthub--rig-a--alpha"), plainNodes())
	w.run("continue", "rig-a", "--no-brief")
	first := strings.SplitN(w.log(), "\n", 2)[0]
	if !strings.HasPrefix(first, "2026-10-10T15:00:00+00:00 ") {
		t.Errorf("log line = %q, want the pinned RFC3339 stamp first", first)
	}
}

// TestTheBriefIsRefusedInWordsWhenNoTransportCanDeliverIt drives the REAL delivery seam rather than the
// stubbed one, because this is the seam whose native branch was unreachable: it looked for the Python-era
// agenthub--<room>--<seat> session shape, which the sessions this rig actually has do not match, then
// exec'd `rig send` and SWALLOWED its failure - so a brief for agenthub--4genthub-min--reviewer was
// reported as briefed and dropped. The seal is the pair of facts below: the delivery is refused for the
// real session shape, and the refusal says what could not deliver it.
func TestTheBriefIsRefusedInWordsWhenNoTransportCanDeliverIt(t *testing.T) {
	home := t.TempDir()
	previousHome := realHome
	realHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { realHome = previousHome })

	// No transport at all: no runtime socket, no seat environment.
	for _, name := range []string{"AGENTHUB_ROOM", "AGENTHUB_SEAT", "AGENTHUB_SESSION_NAME"} {
		t.Setenv(name, "")
	}
	t.Setenv("AGENTHUB_RUNTIME_SOCKET", filepath.Join(t.TempDir(), "no-runtime.sock"))

	var out bytes.Buffer
	r, err := newRestorer(&teeLog{out: &out})
	if err != nil {
		t.Fatalf("newRestorer: %v", err)
	}
	if err := r.send(context.Background(), "agenthub--4genthub-min--reviewer", "Your seat: reviewer. hello"); err == nil {
		t.Fatal("a brief no transport delivered was reported as delivered")
	} else if !strings.Contains(err.Error(), "no runtime is answering") {
		t.Errorf("the refusal does not name the transport that did not answer: %v", err)
	}
}
