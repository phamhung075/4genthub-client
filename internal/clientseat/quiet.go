// The wait and the restart a reseat needs: the quiet wait over a seat's own activity, the activity
// readers it uses, and the relaunch. It was `sync.go`, and the adopting half (`seat sync`, `seat watch`)
// went with the rigspec route it adopted from - see the package doc for the sentence an operator gets.
package clientseat

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

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

// NewQuietWaiter is the real one: the rig orchestrator for the activity, the seat's session files for the stillness.
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
// rig daemon's node table today, the runtime's producer at cutover.
//
// WHY THE PROGRAM NAME IS GONE FROM HERE. This used to exec `rig ps --nodes --json`, resolved with
// exec.LookPath("rig"). `rig`, where it exists at all, is the compat shim to 4genteam, whose own `ps` carries
// `activity` (a log mtime) and NO `agentActivity` at all - and on this host the lookup is DEAD outright
// (measured 2026-10-15: `command -v rig` prints nothing, and ~/.local/bin/rig does not exist), so neither the name
// nor the shim could ever have answered. The word therefore never arrived, quietly: a matched node
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
	read := clientruntime.ReadSeatActivity(rig, seat)
	return read.Word, read.Read
}

// secondsSinceLastWrite is seconds_since_last_write: how long the seat's session files have been still.
// No session at all is infinite, which the wait reads as "not quiet yet" - the Python's float("inf").
func secondsSinceLastWrite(stateRoot, rig, seat string) float64 {
	files, _ := filepath.Glob(filepath.Join(clientruntime.SeatSessionsDir(stateRoot, rig, seat), "*.jsonl"))
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
// (~/.config/4genthub/seats), which is resolved from the passwd entry rather than $HOME - so a test that ran the real
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

// logLine writes a timed line. It is a variable so a test can silence it.
var logLine = func(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
