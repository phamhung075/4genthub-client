// The loop half of `stuck`: a seat making the same call over and over, which spends a model turn each time and
// never ends on its own (room aa2-gemini, seat gemini, 2026-11-01: `manage_task status task-249` until the founder
// saw it on the grid). The queue, board and idle findings of stuck.go stay on agent's ten-minute timer, because
// they only inform; a loop costs tokens every few seconds and the guard acts on it, so this half has its own pass,
// `stuck ROOM --loops --every 15s`, run for every room by agent and by `up --room-only`.
//
// THE LADDER, one rung per cooldown, each rung earned by repeats made AFTER the rung before it:
//
//  1. a steer through the seat's normal input path, naming what it repeated;
//  2. an interrupt of the turn, and a second steer;
//  3. the seat is stopped, the record shows in `room status`, `ps` and the pane title, and one row goes to the
//     room's human through the same escalation stuck.go writes.
//
// It is code and uses no model. The off-peak gate is not asked: a looping seat is already spending, and these
// messages are what makes it stop.
package clientlifecycle

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientwatch"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
	"github.com/phamhung075/4genthub-client/internal/seatloop"
)

const (
	stuckLoopKind = "loop"
	// loopSender signs the guard's messages, so a seat can tell it from a person.
	loopSender = "loop-guard"
	// loopGonePasses is how many passes in a row may find the room without a live seat before a guard started with
	// --exit-with-room leaves. It is what stops `up --room-only` leaving a process behind a stopped room.
	loopGonePasses = 20
)

// loopDeps is what a pass touches outside its arguments, so a case drives the ladder with no room and no clock.
type loopDeps struct {
	root      string
	seats     func(room string) []string
	seatLog   func(room, seat string) (string, string)
	events    func(path string) []seatloop.Event
	steer     func(room, seat, text string) error
	interrupt func(room, seat, runtime string)
	stop      func(room, seat string) error
	escalate  func(room string, finding stuckFinding, open time.Duration) (string, error)
	now       func() time.Time
	log       func(text string)
}

func defaultLoopDeps(root string, out io.Writer) *loopDeps {
	return &loopDeps{
		root:    root,
		seats:   func(room string) []string { return seatlog.RoomSeats(root, room) },
		seatLog: func(room, seat string) (string, string) { return seatlog.SeatLog(root, room, seat) },
		events:  clientwatch.SeatEvents,
		steer: func(room, seat, text string) error {
			_, err := clientruntime.RuntimeSendText(clientruntime.SeatSessionName(room, seat), text,
				clientruntime.SendOptions{Sender: loopSender})
			return err
		},
		interrupt: func(room, seat, runtime string) {
			if runtime == "omp" {
				roomAbort(room, seat)
				return
			}
			tmuxInterrupt(root, room, seat)
		},
		stop:     func(room, seat string) error { return clientruntime.NewClient("").StopSeat(room, seat) },
		escalate: stuckEscalateRow,
		now:      clientservices.Now,
		log:      func(text string) { fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text) },
	}
}

// loopPass is one pass over a room's live seats, and it answers how many there were. Nothing is written unless
// something changed.
func loopPass(deps *loopDeps, config seatloop.Config, room string) int {
	now := deps.now()
	records := seatloop.Load(deps.root, room)
	changed := false
	seats := deps.seats(room)
	for _, seat := range seats {
		if seat == ownerNodeSeat {
			continue
		}
		path, runtime := deps.seatLog(room, seat)
		if path == "" {
			continue
		}
		record, known := records[seat]
		var since time.Time
		if known {
			since = record.At
		}
		finding, looping := seatloop.Detect(deps.events(path), config, now, since)
		if !looping {
			// A seat that stayed clear for a whole window has recovered, and that seat's next loop starts at rung 1.
			if known && now.Sub(record.At) >= config.Window {
				delete(records, seat)
				changed = true
				deps.log(fmt.Sprintf("%s: no repeat for %s since the %s; the record is cleared", seat,
					stuckDuration(now.Sub(record.At)), record.Status()))
			}
			continue
		}
		if known && now.Sub(record.At) < config.Cooldown {
			continue
		}
		records[seat] = loopAct(deps, room, seat, runtime, record, finding, now)
		changed = true
	}
	if changed {
		if err := seatloop.Save(deps.root, room, records); err != nil {
			deps.log("the loop record could not be written: " + err.Error())
		}
	}
	return len(seats)
}

// loopAct takes the next rung. The top rung is where it stays: a stop the runtime refused is asked again one
// cooldown later, and the owner's row is written once.
func loopAct(deps *loopDeps, room, seat, runtime string, record seatloop.Record, finding seatloop.Finding, now time.Time) seatloop.Record {
	rung := record.Rung + 1
	if rung > seatloop.Halted {
		rung = seatloop.Halted
	}
	next := seatloop.Record{Rung: rung, At: now, Call: finding.Line(), Count: finding.Count, Raised: record.Raised}
	what := fmt.Sprintf("%s x%d", finding.Line(), finding.Count)
	switch rung {
	case seatloop.Steered:
		err := deps.steer(room, seat, loopSteer(finding))
		deps.log(fmt.Sprintf("%s: looping (%s), rung 1, steered%s", seat, what, loopFailure(err)))
	case seatloop.Interrupted:
		deps.interrupt(room, seat, runtime)
		err := deps.steer(room, seat, loopSteerAgain(finding))
		deps.log(fmt.Sprintf("%s: still looping (%s), rung 2, interrupted and steered%s", seat, what, loopFailure(err)))
	default:
		err := deps.stop(room, seat)
		next.Stopped = err == nil
		deps.log(fmt.Sprintf("%s: still looping (%s), rung 3, stopped%s", seat, what, loopFailure(err)))
		if !next.Raised {
			alert := stuckFinding{Kind: stuckLoopKind, ID: seat, Seat: seat, Title: seat, Reason: loopReason(finding)}
			if _, raiseErr := deps.escalate(room, alert, now.Sub(record.At)); raiseErr != nil {
				deps.log(fmt.Sprintf("%s: the owner's row could not be written: %v", seat, raiseErr))
			} else {
				next.Raised = true
			}
		}
	}
	return next
}

func loopFailure(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(" - NOT delivered: %v", err)
}

// loopReason is the finding's fact, in the voice of the other stuck findings.
func loopReason(finding seatloop.Finding) string {
	return fmt.Sprintf("it repeated %s %d times with no file written, and went on after two messages and an interrupt; "+
		"it is stopped, and `4genteam room reseat` brings it back", finding.Line(), finding.Count)
}

func loopSteer(finding seatloop.Finding) string {
	return fmt.Sprintf("You have repeated this %d times with no file written and nothing changed: %s. Repeating it will "+
		"not change the answer, and each repeat costs a turn. Stop now and end your turn. A background task wakes you "+
		"when it finishes, so do not poll it and do not schedule another check. If something blocks you, say what in "+
		"one line and stop.", finding.Count, finding.Line())
}

func loopSteerAgain(finding seatloop.Finding) string {
	return fmt.Sprintf("Your turn was interrupted because you repeated this %d more times after being told to stop: %s. "+
		"End your turn now and make no further call. If you repeat it again this seat is stopped and the owner is told.",
		finding.Count, finding.Line())
}

// runStuckLoops is `stuck ROOM --loops`: one pass, or one every `every`, with the heartbeat agent reads.
func runStuckLoops(room, root string, every time.Duration, exitWithRoom, dryRun, clear bool, interrupted <-chan struct{}, stdout, stderr io.Writer) int {
	if clear {
		if err := seatloop.Save(root, room, nil); err != nil {
			fmt.Fprintf(stderr, "4genteam stuck: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "stuck %s: the loop records are cleared\n", room)
		return clientcmd.ExitOK
	}
	config, err := seatloop.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam stuck: %v\n", err)
		return clientcmd.ExitUsage
	}
	deps := loopDepsFor(root, stdout)
	if dryRun {
		printLoopPlan(deps, config, room, stdout)
		return clientcmd.ExitOK
	}
	deps.log(fmt.Sprintf("guarding %s against seat loops: %d repeats in %s, %s between actions (on: %t)", room,
		config.Repeat, config.Window, config.Cooldown, config.On()))
	gone := 0
	for {
		seats := 0
		if config.On() {
			seats = loopPass(deps, config, room)
		} else {
			seats = len(deps.seats(room))
		}
		if every <= 0 {
			return clientcmd.ExitOK
		}
		if err := clientservices.WriteHeartbeat(root, clientservices.LoopGuard, room); err != nil {
			deps.log("cannot write the heartbeat: " + err.Error())
		}
		if seats == 0 {
			gone++
		} else {
			gone = 0
		}
		if exitWithRoom && gone >= loopGonePasses {
			deps.log(fmt.Sprintf("%s has had no live seat for %d passes; this guard leaves with it", room, gone))
			return clientcmd.ExitOK
		}
		select {
		case <-interrupted:
			return clientcmd.ExitOK
		case <-time.After(every):
		}
	}
}

// loopDepsFor is the production wiring, and a seam so a case drives the verb without a room.
var loopDepsFor = defaultLoopDeps

// printLoopPlan reads and says what it would do, and neither acts nor writes.
func printLoopPlan(deps *loopDeps, config seatloop.Config, room string, out io.Writer) {
	now := deps.now()
	records := seatloop.Load(deps.root, room)
	fmt.Fprintf(out, "stuck %s --loops: %d repeats in %s, %s between actions - DRY RUN, nothing sent\n", room, config.Repeat,
		config.Window, config.Cooldown)
	for _, seat := range deps.seats(room) {
		path, runtime := deps.seatLog(room, seat)
		if path == "" {
			fmt.Fprintf(out, "  %s: skipped, no session log this machine can read\n", seat)
			continue
		}
		record, known := records[seat]
		var since time.Time
		if known {
			since = record.At
		}
		finding, looping := seatloop.Detect(deps.events(path), config, now, since)
		switch {
		case looping:
			fmt.Fprintf(out, "  %s (%s): LOOPING, would take rung %d: %s x%d\n", seat, runtime, record.Rung+1, finding.Line(), finding.Count)
		case known:
			fmt.Fprintf(out, "  %s (%s): clear since %s\n", seat, runtime, record.Status())
		default:
			fmt.Fprintf(out, "  %s (%s): clear\n", seat, runtime)
		}
	}
}

// LoopLogPath is the guard's log for one room.
func LoopLogPath(room, root string) string {
	return filepath.Join(LogDir(root), "loop-guard-"+room+".log")
}

// loopGuardArgv is the one command line of the guard, for agent's child and for `up --room-only`, in the order
// clientservices.Pattern matches.
func loopGuardArgv(room, root string, extra ...string) []string {
	argv := []string{"stuck", room, "--loops", "--every", clientservices.LoopEvery.String()}
	argv = append(argv, extra...)
	return append(argv, "--state-root", root)
}

// startLoopGuard starts the room's own guard for `up --room-only`, and says what the room runs without. A running
// guard is left alone, and agent adopts this process when the room is later brought up in full. A guard that did
// not start is a warning and not this verb's verdict, which stays the room's own.
var startLoopGuard = func(room, root string, stdout, stderr io.Writer) {
	unprotected := fmt.Sprintf("4genteam up: WARNING: room %s runs with NO loop guard: ", room)
	live, err := clientservices.Live(clientservices.Pattern(clientservices.LoopGuard, room))
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "%scannot tell whether one is running: %v\n", unprotected, err)
	case len(live) > 0:
		fmt.Fprintf(stdout, "loop guard: already running, pid %d\n", live[0])
	default:
		logPath := LoopLogPath(room, root)
		child, spawnErr := spawnChild(loopGuardArgv(room, root, "--exit-with-room"), logPath)
		if spawnErr != nil {
			fmt.Fprintf(stderr, "%s%v\n", unprotected, spawnErr)
			break
		}
		if survived, waitErr := awaitStart(child); !survived {
			fmt.Fprintf(stderr, "%sit exited at once (status %d); see %s\n", unprotected, exitStatus(waitErr), logPath)
			break
		}
		fmt.Fprintf(stdout, "loop guard: pid %d, log %s\n", child.Pid(), logPath)
	}
	fmt.Fprintf(stderr, "4genteam up: --room-only: no compaction supervisor, watchdog or idle guard runs for %s; "+
		"`4genteam up %s` starts them.\n", room, room)
}
