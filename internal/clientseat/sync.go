// The adopting half of `seat`: sync, watch, the quiet wait and the relaunch.
package clientseat

import (
	"context"
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
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
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
//
// Activity's TWO answers are not one: the word the seat is doing, and whether the read happened at all. They are
// separate because "" is a word a broken read and a wordless node both produce, and an unknown activity must NOT be
// read as "not running" - see quietOnce, which is where that rule is written.
type QuietWaiter struct {
	Activity func(rig, seat string) (string, bool)
	Since    func(rig, seat string) float64
	Sleep    func(seconds float64)
	Clock    func() float64

	// Report says, ONCE, what a wait that timed out could not make sense of. It exists because the failure this wait
	// has on this box is silent: with no word arriving at all, a genuinely idle seat and a dead gate look identical -
	// no quiet, forever, nothing said. nil means say nothing.
	Report func(message string)

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
		Report:       func(message string) { fmt.Fprintln(os.Stderr, message) },
	}
}

func (w QuietWaiter) Wait(rig, seat string) bool {
	deadline := w.Clock() + w.WaitSeconds
	unrecognized := ""
	note := func(word string) {
		if word != "" && unrecognized == "" {
			unrecognized = word
		}
	}
	for w.Clock() < deadline {
		quiet, strange := w.quietOnce(rig, seat)
		note(strange)
		if quiet {
			w.Sleep(w.PollSeconds)
			quiet, strange = w.quietOnce(rig, seat)
			note(strange)
			if quiet {
				return true
			}
		}
		w.Sleep(w.PollSeconds)
	}
	// ONCE, at the end, and only when a word arrived that this client has never seen: a vocabulary growth has to
	// surface as a sentence rather than as a wait that never opens.
	if unrecognized != "" && w.Report != nil {
		w.Report(fmt.Sprintf("no quiet for %s/%s: the activity word %q is not one this client recognizes, so the "+
			"wait cannot tell an idle seat from a dead gate", rig, seat, unrecognized))
	}
	return false
}

// quietOnce is ONE reading of the two clauses - the seat is not running AND its session has been still for the quiet
// window - and the activity comparison is NOT written here: it is clientruntime's one allow-set, shared with rigIdle,
// so a vocabulary change is decided once instead of being found twice.
//
// AN UNREADABLE ACTIVITY IS NOT "NOT RUNNING", and the seam says which is which: Activity's second answer is false
// when no activity could be read at all (no `rig` on PATH, the ps call failed, the seat missing from the node table),
// and that false KEEPS THE WAIT GOING rather than releasing it. The word itself is then tested against the allow-set
// instead of against one forbidden value: `!= "running"` ACCEPTED every word this client had never seen - `working`,
// `needs_input`, and any word the daemon adds later - so the stillness clause decided by itself and a reseat could
// proceed past a seat that was mid-turn. Stillness may DELAY a destroyer; only the allow-set's word releases one.
func (w QuietWaiter) quietOnce(rig, seat string) (quiet bool, unrecognized string) {
	activity, read := w.Activity(rig, seat)
	if !read {
		return false, ""
	}
	released, unrecognized := clientruntime.ActivityReleasesTheWait(activity)
	if !released {
		return false, unrecognized
	}
	return w.Since(rig, seat) >= w.QuietSeconds, ""
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// seatActivity is seat_activity: the activity word for one seat, and it is the SECOND reader of the same answer
// rigIdle reads - so it is a DECLARED EXCEPTION with the same retirement. Both readers now call ONE function,
// clientruntime.ReadSeatActivity, which asks the agent's own activity report over the client's OWN transport: the
// OpenRig daemon's node table today, the runtime's producer at cutover.
//
// WHY THE PROGRAM NAME IS GONE FROM HERE. This used to exec `rig ps --nodes --json`, resolved with
// exec.LookPath("rig") - and on this box `rig` on PATH is the compat shim to 4genteam, whose own `ps` carries
// `activity` (a log mtime) and NO `agentActivity` at all. The word therefore never arrived, quietly: a matched node
// with no field, an empty word, a gate that can never open. Asking the daemon through the client's own transport
// removes the question of WHICH program answered, and with it the whole class.
//
// THE RE-POINT IS THE CUTOVER PRECONDITION, and it is now one function rather than two readers: when the runtime
// grows its own producer, ReadSeatActivity's body moves and this declaration is DELETED rather than annotated.
//
// ITS CONSUMER IS QuietWaiter, and the direction of failure here is the SAME as rigIdle's: THIS ONE FAILS CLOSED. The
// second value is FALSE whenever nothing could be read at all, and QuietWaiter keeps waiting on that answer instead
// of reading it as "not running". With no daemon named in the environment and no producer yet on the runtime, every
// call answers false and a reseat waits out its deadline and does NOT proceed - far better than proceeding past a
// seat whose agent is mid-turn.
func seatActivity(rig, seat string) (string, bool) {
	read := clientruntime.ReadSeatActivity(context.Background(), rig, seat)
	return read.Word, read.Read
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

// restartSeat is the ONE place this package asks the runtime to start a seat again. It is a variable for the same
// reason clientlifecycle's runtimeSendText is: a test replaces it to assert WHICH room and seat were asked for, and to
// prove that no shell-out can hide behind it.
var restartSeat = func(rig, seat string) error {
	_, err := clientruntime.NewClient("").RestartSeat(rig, seat)
	return err
}

// deliverSeatSkills is a variable because the function it stands for writes into the REAL account's seat store
// (~/.openrig/agenthub-seats), which is resolved from the passwd entry rather than $HOME - so a test that ran the real
// one would write into the owner's tree. Everything else about it is deliverSkillsDefault's.
var deliverSeatSkills = deliverSkillsDefault

// relaunch stops the seat's session and asks the runtime to start it again, so it loads the snapshot it is
// now pinned to.
//
// THE RUNTIME TAKES THE RESTART OVER, and four pieces of the old code are DELETED rather than kept around the new call:
//
//   - `rig launch <rigId> <rig.seat>` becomes the runtime's RestartSeat, which is stop_seat followed by start_seat
//     from the room's own spec - the same two steps, taken by the party that owns the session.
//   - THE CLIENT'S OWN `tmux kill-session` AND THE 4s SLEEP are gone: the runtime's restart stops the session itself,
//     so the client reaching into tmux was the client doing the runtime's job, and the sleep existed to let a session
//     the client had just killed go away before asking for the launch.
//   - `rigID` (a `rig ps --json` call that existed only to find the id argument) and `sessionComesUp` (a three-minute
//     tmux poll) are gone with it: neither had another caller.
//   - THE "did not respond in time" TOLERANCE IS GONE, and this is the one behaviour change worth naming: it existed
//     because the CLI gave up after 5s while the launch landed anyway, so the code polled tmux to find out. A socket
//     call has no such failure mode - the reply IS the outcome, or the call reports why - so a failure is now the
//     runtime's own reason instead of being inferred from a session appearing, which is the shape that let a no-op
//     look like a success elsewhere this evening.
func relaunch(rig, seat string) error {
	if err := deliverSeatSkills(rig, seat); err != nil {
		return err
	}
	if err := restartSeat(rig, seat); err != nil {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("could not relaunch %s/%s: %v", rig, seat, err),
			Code:    ExitFailed,
		}
	}
	return nil
}

// logf writes a timed line. It is a variable so a test can silence it.
var logLine = func(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
