// The wait and the restart a reseat needs: the quiet wait over a seat's own activity, the activity
// readers it uses, and the relaunch. It was `sync.go`, and the adopting half (`seat sync`, `seat watch`)
// went with the roomspec route it adopted from - see the package doc for the sentence an operator gets.
package clientseat

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

// QuietWaiter is wait_until_quiet: true once the seat is not running and its session has been still for
// QuietSeconds, twice in a row. Every read is a field, so the suite needs no room and no clock.
//
// Activity's TWO answers are not one: the word the seat is doing, and whether the read happened at all. They are
// separate because "" is a word a broken read and a wordless node both produce, and an unknown activity must NOT be
// read as "not running" - see quietOnce, which is where that rule is written.
type QuietWaiter struct {
	Activity func(room, seat string) (string, bool)
	Since    func(room, seat string) float64
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

// NewQuietWaiter is the real one: the room orchestrator for the activity, the seat's session files for the stillness.
func NewQuietWaiter(stateRoot string) QuietWaiter {
	return QuietWaiter{
		Activity:     seatActivity,
		Since:        func(room, seat string) float64 { return secondsSinceLastWrite(stateRoot, room, seat) },
		Sleep:        func(seconds float64) { time.Sleep(time.Duration(seconds * float64(time.Second))) },
		Clock:        nowSeconds,
		QuietSeconds: quietSeconds,
		WaitSeconds:  quietWaitSeconds,
		PollSeconds:  pollSeconds,
		Report:       func(message string) { fmt.Fprintln(os.Stderr, message) },
	}
}

func (w QuietWaiter) Wait(room, seat string) bool {
	deadline := w.Clock() + w.WaitSeconds
	unrecognized := ""
	blind := false
	// THE FIRST READING THAT CANNOT OPEN IS SAID OUT LOUD, THE MOMENT IT HAPPENS - and this is the whole of the
	// change here. There are two ways to a wait that never opens: an activity word nobody recognizes, and no
	// activity read at all. Both used to be discovered only by the deadline - the word was reported once at the
	// end, and the unreadable case was reported NOWHERE - inside a budget of quietWaitSeconds. So the verb sat
	// silent for its full budget with nothing to read, and the caller's only evidence was silence.
	//
	// ONCE PER REASON: a wait that polls every few seconds must not repeat itself on every poll, so each reason is
	// said when it is first seen and never again, whichever of the two arrives first.
	reading := func() (quiet bool) {
		q, strange, unreadable := w.quietOnce(room, seat)
		if strange != "" && unrecognized == "" {
			unrecognized = strange
			w.reportf("no quiet for %s/%s: the activity word %q is not one this client recognizes, so the wait "+
				"cannot tell an idle seat from a dead gate; it will run its full %s and leave the seat running",
				room, seat, strange, quietBudget())
		}
		if unreadable && !blind {
			blind = true
			w.reportf("%s/%s: no activity could be read at all, so this wait cannot open; it will run its full "+
				"%s and leave the seat running", room, seat, quietBudget())
		}
		return q
	}
	for w.Clock() < deadline {
		if reading() {
			w.Sleep(w.PollSeconds)
			if reading() {
				return true
			}
		}
		w.Sleep(w.PollSeconds)
	}
	return false
}

// reportf says one sentence through Report, and nothing when there is no Report: the seam stays a field a test can
// leave nil, and the call sites stay free of nil checks.
func (w QuietWaiter) reportf(format string, args ...any) {
	if w.Report == nil {
		return
	}
	w.Report(fmt.Sprintf(format, args...))
}

// quietBudget is the wait's budget as a DURATION rather than as a count of minutes: the sentence it replaces
// divided by 60, so a budget that is not a whole number of minutes printed as a floor and named a window that was
// not the window.
func quietBudget() string { return (time.Duration(quietWaitSeconds) * time.Second).String() }

// quietOnce is ONE reading of the two clauses - the seat is not running AND its session has been still for the quiet
// window - and the activity comparison is NOT written here: it is clientruntime's one allow-set, shared with roomIdle,
// so a vocabulary change is decided once instead of being found twice.
//
// AN UNREADABLE ACTIVITY IS NOT "NOT RUNNING", and the seam says which is which: Activity's second answer is false
// when no activity could be read at all (no `rig` on PATH, the ps call failed, the seat missing from the node table),
// and that false KEEPS THE WAIT GOING rather than releasing it. The word itself is then tested against the allow-set
// instead of against one forbidden value: `!= "running"` ACCEPTED every word this client had never seen - `working`,
// `needs_input`, and any word the daemon adds later - so the stillness clause decided by itself and a reseat could
// proceed past a seat that was mid-turn. Stillness may DELAY a destroyer; only the allow-set's word releases one.
// The THIRD answer is the second half of the same seam: `unreadable` is true when no activity could be read at all,
// which is the one case the word cannot report because there is no word - and it is the case this box hits, so the
// wait must be able to name it instead of only counting the polls.
func (w QuietWaiter) quietOnce(room, seat string) (quiet bool, unrecognized string, unreadable bool) {
	activity, read := w.Activity(room, seat)
	if !read {
		return false, "", true
	}
	released, unrecognized := clientruntime.ActivityReleasesTheWait(activity)
	if !released {
		return false, unrecognized, false
	}
	return w.Since(room, seat) >= w.QuietSeconds, "", false
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// seatActivity is seat_activity: the activity word for one seat, and it is the SECOND reader of the same answer
// roomIdle reads - so it is a DECLARED EXCEPTION with the same retirement. Both readers now call ONE function,
// clientruntime.ReadSeatActivity, which asks the agent's own activity report over the client's OWN transport: the
// room daemon's node table today, the runtime's producer at cutover.
//
// WHY THE PROGRAM NAME IS GONE FROM HERE. This used to exec `rig ps --nodes --json`, resolved with
// exec.LookPath("rig"). `rig` is not this client: where an `rig` exists at all it is a different program, and the
// `ps` such a program serves carries `activity` (a log mtime) and NO `agentActivity` at all - and on this host the
// lookup is DEAD outright (measured 2026-10-15, re-measured 2026-10-17: `command -v room` prints nothing, and
// ~/.local/bin/room does not exist), so neither the name nor the program could ever have answered. The word
// therefore never arrived, quietly: a matched node
// with no field, an empty word, a gate that can never open. Asking the daemon through the client's own transport
// removes the question of WHICH program answered, and with it the whole class.
//
// THE RE-POINT IS THE CUTOVER PRECONDITION, and it is now one function rather than two readers: when the runtime
// grows its own producer, ReadSeatActivity's body moves and this declaration is DELETED rather than annotated.
//
// ITS CONSUMER IS QuietWaiter, and the direction of failure here is the SAME as roomIdle's: THIS ONE FAILS CLOSED. The
// second value is FALSE whenever nothing could be read at all, and QuietWaiter keeps waiting on that answer instead
// of reading it as "not running". With no daemon named in the environment and no producer yet on the runtime, every
// call answers false and a reseat waits out its deadline and does NOT proceed - far better than proceeding past a
// seat whose agent is mid-turn.
func seatActivity(room, seat string) (string, bool) {
	read := clientruntime.ReadSeatActivity(room, seat)
	return read.Word, read.Read
}

// secondsSinceLastWrite is seconds_since_last_write: how long the seat's session files have been still.
// No session at all is infinite, which the wait reads as "not quiet yet" - the Python's float("inf").
func secondsSinceLastWrite(stateRoot, room, seat string) float64 {
	files, _ := filepath.Glob(filepath.Join(clientruntime.SeatSessionsDir(stateRoot, room, seat), "*.jsonl"))
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
//
// THE DOCUMENT COMES FROM THE HUB, per restart. A reseat is the act that applies a policy change - it is what an
// operator runs after the hub's render changes - so the render is fetched here and travels on the frame, and the
// runtime installs it before the seat's runner starts. A render that cannot be read does NOT stop the restart: the
// seat comes back on the room's own record, which is what it was already running under, and the reason is SAID
// rather than swallowed (clientsync.SeatDocumentFor).
var restartSeat = func(room, seat string) error {
	document := clientsync.SeatDocumentFor(context.Background(), room, seat, policyNotes{})
	_, err := clientruntime.NewClient("").RestartSeat(room, seat, document)
	return err
}

// policyNotes carries clientpolicy's own lines into this package's timed log, so a note about the seat's policy
// does not become a second output channel - and a test that silences logLine silences this with it.
type policyNotes struct{}

func (policyNotes) Write(p []byte) (int, error) {
	logLine("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// relaunch stops the seat's session and asks the runtime to start it again, so it loads the snapshot it is
// now pinned to.
//
// THE RUNTIME TAKES THE RESTART OVER, and five pieces of the old code are DELETED rather than kept around the new call:
//
//   - `rig launch <roomId> <rig.seat>` becomes the runtime's RestartSeat, which is stop_seat followed by start_seat
//     from the room's own spec - the same two steps, taken by the party that owns the session.
//   - THE CLIENT'S OWN `tmux kill-session` AND THE 4s SLEEP are gone: the runtime's restart stops the session itself,
//     so the client reaching into tmux was the client doing the runtime's job, and the sleep existed to let a session
//     the client had just killed go away before asking for the launch.
//   - `roomID` (a `rig ps --json` call that existed only to find the id argument) and `sessionComesUp` (a three-minute
//     tmux poll) are gone with it: neither had another caller.
//   - THE "did not respond in time" TOLERANCE IS GONE, and this is the one behaviour change worth naming: it existed
//     because the CLI gave up after 5s while the launch landed anyway, so the code polled tmux to find out. A socket
//     call has no such failure mode - the reply IS the outcome, or the call reports why - so a failure is now the
//     runtime's own reason instead of being inferred from a session appearing, which is the shape that let a no-op
//     look like a success elsewhere this evening.
//   - THE CLIENT'S SKILL DELIVERY IS GONE (`clientseat/deliverSkills`). It copied `<store>/<room>/room/agents/<seat>/skills`
//     into the seat's own directory, a store only the removed `sync room` verb ever wrote, and it treated a missing source
//     as a refusal - so `4genteam seat reseat`, the surviving verb that reaches relaunch, failed at that step and never
//     restarted anything. THE RUNTIME INSTALLS THE SEAT'S SKILLS ITSELF at every start (`install_seat_skills`,
//     `rust/agenthub-runtime/src/supervisor/mod.rs:258`, from the tree `AGENTHUB_CLIENT_SKILLS` names), so the client has
//     nothing left to deliver and no store to read.
func relaunch(room, seat string) error {
	if err := restartSeat(room, seat); err != nil {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("could not relaunch %s/%s: %v", room, seat, err),
			Code:    ExitFailed,
		}
	}
	return nil
}

// logLine writes a timed line. It is a variable so a test can silence it.
var logLine = func(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
