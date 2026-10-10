// The adopting half of `seat`: sync, watch, the quiet wait and the relaunch.
package clientseat

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// SyncRigArgs is the adopt step's command: THIS binary's own `sync rig`, so the pinning rules (never
// fall back to another snapshot; a missing pin is an error) stay in one place. The Python called
// `python -m agenthub_client.seat_sync rig ROOM --out OUT --update`.
func SyncRigArgs(self, room, out string) []string {
	return []string{self, "sync", "rig", room, "--out", out, "--update"}
}

// syncDeps is everything sync_once touches outside the two reads: the adopt step, the quiet wait, the
// relaunch, and the log. Each is a field so the suite drives sync with no rig, no cloud and no waiting.
type syncDeps struct {
	src        source
	runSyncRig func(room, out string) error
	waitQuiet  func(rig, seat string) bool
	relaunch   func(rig, seat string) error
	logf       func(format string, args ...any)
}

func defaultSyncDeps(stateRoot string) *syncDeps {
	return &syncDeps{
		src:        cloudSource{},
		runSyncRig: runSyncRigCommand,
		waitQuiet:  func(rig, seat string) bool { return NewQuietWaiter(stateRoot).Wait(rig, seat) },
		relaunch:   relaunch,
		logf:       logLine,
	}
}

func runSync(args []string, stdout, stderr io.Writer) int {
	parsed, code, ok := parseOptions("sync", args, true, false, stdout, stderr)
	if !ok {
		return code
	}
	stateRoot, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return report(stderr, err)
	}
	exitCode, err := syncOnce(parsed, defaultSyncDeps(stateRoot))
	if err != nil {
		return report(stderr, err)
	}
	return exitCode
}

func runWatch(args []string, stdout, stderr io.Writer) int {
	parsed, code, ok := parseOptions("watch", args, true, true, stdout, stderr)
	if !ok {
		return code
	}
	stateRoot, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return report(stderr, err)
	}
	deps := defaultSyncDeps(stateRoot)
	logLine("watching %s every %ds; Ctrl-C to stop", parsed.room, parsed.interval)
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	for {
		if _, err := syncOnce(parsed, deps); err != nil {
			// Keep watching: the next pass retries from a clean read.
			logLine("FAILED: %v", err)
		}
		select {
		case <-interrupted:
			return clientcmd.ExitOK
		case <-time.After(time.Duration(parsed.interval) * time.Second):
		}
	}
}

// syncOnce adopts every behind seat and, when asked, restarts the ones that changed. It returns the
// exit code and, for a failure the caller should print, an error - the Python's ClientError, whose code
// decides the exit.
func syncOnce(parsed options, deps *syncDeps) (int, error) {
	out := expand(parsed.out)
	rig := parsed.rigName()
	cloud, err := deps.src.CloudHashes(parsed.room)
	if err != nil {
		return 0, err
	}
	cloud = seatsFiltered(cloud, parsed.seats)
	pins, err := pinnedHashes(deps.src, out, parsed.room, cloud)
	if err != nil {
		return 0, err
	}
	behind := SeatsBehind(cloud, pins)
	if len(behind) == 0 {
		deps.logf("%s: all %d seats in sync", parsed.room, len(cloud))
		return clientcmd.ExitOK, nil
	}
	deps.logf("%s: behind: %s", parsed.room, strings.Join(behind, ", "))
	if err := deps.runSyncRig(parsed.room, out); err != nil {
		return 0, &clientcmd.CodedError{
			Message: fmt.Sprintf("4genteam sync rig failed: %v", err),
			Code:    ExitFailed,
		}
	}
	after, err := pinnedHashes(deps.src, out, parsed.room, cloud)
	if err != nil {
		return 0, err
	}
	if still := SeatsBehind(cloud, after); len(still) > 0 {
		return 0, &clientcmd.CodedError{
			Message: fmt.Sprintf("adopted nothing for: %s (pin did not move to the cloud hash)", strings.Join(still, ", ")),
			Code:    ExitFailed,
		}
	}
	deps.logf("adopted the newer snapshot for: %s", strings.Join(behind, ", "))
	if parsed.relaunch == "none" {
		deps.logf("running seats keep what they loaded; the change applies at each seat's next launch")
		return clientcmd.ExitOK, nil
	}
	failed := 0
	for _, seat := range behind {
		if !deps.waitQuiet(rig, seat) {
			deps.logf("%s: not quiet within %d minutes, left running with the old snapshot",
				seat, int(quietWaitSeconds/60))
			failed++
			continue
		}
		if err := deps.relaunch(rig, seat); err != nil {
			return 0, err
		}
		deps.logf("%s: relaunched on the new snapshot", seat)
	}
	if failed > 0 {
		return ExitFailed, nil
	}
	return clientcmd.ExitOK, nil
}

func runSyncRigCommand(room, out string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := SyncRigArgs(self, room, out)
	done, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", truncate(strings.TrimSpace(string(done)), 300))
	}
	return nil
}

func truncate(text string, limit int) string {
	if len(text) > limit {
		return text[:limit]
	}
	return text
}

// QuietWaiter is wait_until_quiet: true once the seat is not running and its session has been still for
// QuietSeconds, twice in a row. Every read is a field, so the suite needs no rig and no clock.
type QuietWaiter struct {
	Activity func(rig, seat string) string
	Since    func(rig, seat string) float64
	Sleep    func(seconds float64)
	Clock    func() float64

	QuietSeconds float64
	WaitSeconds  float64
	PollSeconds  float64
}

// NewQuietWaiter is the real one: OpenRig for the activity, the seat's session files for the stillness.
func NewQuietWaiter(stateRoot string) QuietWaiter {
	return QuietWaiter{
		Activity:     seatActivity,
		Since:        func(rig, seat string) float64 { return secondsSinceLastWrite(stateRoot, rig, seat) },
		Sleep:        func(seconds float64) { time.Sleep(time.Duration(seconds * float64(time.Second))) },
		Clock:        nowSeconds,
		QuietSeconds: quietSeconds,
		WaitSeconds:  quietWaitSeconds,
		PollSeconds:  pollSeconds,
	}
}

func (w QuietWaiter) Wait(rig, seat string) bool {
	deadline := w.Clock() + w.WaitSeconds
	for w.Clock() < deadline {
		if w.Activity(rig, seat) != "running" && w.Since(rig, seat) >= w.QuietSeconds {
			w.Sleep(w.PollSeconds)
			if w.Activity(rig, seat) != "running" && w.Since(rig, seat) >= w.QuietSeconds {
				return true
			}
		}
		w.Sleep(w.PollSeconds)
	}
	return false
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// seatActivity is seat_activity: the runtime's own word on whether the seat is running.
func seatActivity(rig, seat string) string {
	path, err := exec.LookPath("rig")
	if err != nil {
		return ""
	}
	out, err := exec.Command(path, "ps", "--nodes", "--rig", rig, "--json").Output()
	if err != nil {
		return ""
	}
	var nodes []map[string]any
	if json.Unmarshal(out, &nodes) != nil {
		return ""
	}
	for _, node := range nodes {
		if node["logicalId"] != rig+"."+seat {
			continue
		}
		activity, _ := node["agentActivity"].(map[string]any)
		state, _ := activity["state"].(string)
		return state
	}
	return ""
}

// secondsSinceLastWrite is seconds_since_last_write: how long the seat's session files have been still.
// No session at all is infinite, which the wait reads as "not quiet yet" - the Python's float("inf").
func secondsSinceLastWrite(stateRoot, rig, seat string) float64 {
	files, _ := filepath.Glob(filepath.Join(stateRoot, rig+"-"+seat+"@"+rig, "sessions", "*.jsonl"))
	if len(files) == 0 {
		return math.Inf(1)
	}
	newest := 0.0
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		if written := float64(info.ModTime().UnixNano()) / 1e9; written > newest {
			newest = written
		}
	}
	return nowSeconds() - newest
}

// relaunch stops the seat's session and asks OpenRig to launch it again, so it loads the snapshot it is
// now pinned to.
func relaunch(rig, seat string) error {
	session := rig + "-" + seat + "@" + rig
	if tmux, err := exec.LookPath("tmux"); err == nil {
		_ = exec.Command(tmux, "kill-session", "-t", session).Run()
	}
	time.Sleep(4 * time.Second)
	path, err := exec.LookPath("rig")
	if err != nil {
		return &clientcmd.CodedError{
			Message: "rig is not on PATH, and relaunching a seat needs it",
			Code:    clientcmd.ExitUnavailable,
		}
	}
	id, err := rigID(path, rig)
	if err != nil {
		return err
	}
	if done, err := exec.Command(path, "launch", id, rig+"."+seat).CombinedOutput(); err != nil {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("rig launch %s.%s failed: %s", rig, seat, truncate(strings.TrimSpace(string(done)), 200)),
			Code:    ExitFailed,
		}
	}
	return nil
}

func rigID(rigPath, rig string) (string, error) {
	out, err := exec.Command(rigPath, "ps", "--json").Output()
	if err != nil {
		return "", &clientcmd.CodedError{Message: fmt.Sprintf("rig ps --json failed: %v", err), Code: ExitFailed}
	}
	var entries []map[string]any
	if json.Unmarshal(out, &entries) != nil {
		var wrapper struct {
			Rigs []map[string]any `json:"rigs"`
		}
		if json.Unmarshal(out, &wrapper) != nil {
			return "", &clientcmd.CodedError{Message: "rig ps --json did not answer with a rig list", Code: ExitFailed}
		}
		entries = wrapper.Rigs
	}
	for _, entry := range entries {
		if entry["name"] != rig {
			continue
		}
		if id, ok := entry["rigId"].(string); ok && id != "" {
			return id, nil
		}
	}
	return "", &clientcmd.CodedError{Message: fmt.Sprintf("no running rig named %q", rig), Code: ExitFailed}
}

// logf writes a timed line. It is a variable so a test can silence it.
var logLine = func(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
