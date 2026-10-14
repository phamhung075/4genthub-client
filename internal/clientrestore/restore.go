package clientrestore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientlifecycle"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// briefSendTimeout is the shell's `timeout 40 rig send`. A brief is text typed into a seat's terminal
// through the rig tool, and a seat whose terminal is wedged must not hold the whole restore open: that
// seat gets "send failed" in the log and the rest of the room is briefed.
const briefSendTimeout = 40 * time.Second

// restorer is one rig's restore-and-brief: the shell's continue_rig(), with the seams a test needs. It is
// used by `continue` for the rig the caller named and by `watchdog` when it decides a rig is dead - one
// implementation, so the two verbs cannot restore differently.
type restorer struct {
	log    *teeLog
	briefs string
	// ensureDaemon and revive are the two steps that reach the runtime: the daemon up, and the room's
	// stopped seats started again from the spec it holds. They are fields so a test restores a whole room
	// with no daemon.
	ensureDaemon func(ctx context.Context) error
	revive       func(ctx context.Context, room string) error
	// send and wall are the two steps that reach past the daemon - the brief, and the watch wall - and
	// they are struct fields so a test drives a whole restore with no seat, no terminal and no wall.
	send func(ctx context.Context, session, text string) error
	wall func(rig string) (opened bool, why string)
}

// restoreSteps, when a test has set it, is handed the restorer newRestorer just wired, so the brief and
// the watch wall - the two steps that reach past the daemon - can be replaced without a seat, a terminal
// or a wall. A package-level variable rather than an interface added for testing, which is the repo's
// rule; it is nil everywhere but the suite.
var restoreSteps func(*restorer)

// newRestorer wires the real world: the rig tool the caller resolved, the brief directory the shell used,
// `rig send` for the brief, and this same binary for the watch wall.
func newRestorer(log *teeLog) (*restorer, error) {
	briefs, err := briefDir()
	if err != nil {
		return nil, err
	}
	r := &restorer{
		log:          log,
		briefs:       briefs,
		ensureDaemon: func(ctx context.Context) error { return clientruntime.NewClient("").EnsureDaemon(ctx) },
		revive:       reviveRoom,
		// send is the brief's delivery through the runtime socket. DeliverToSession takes the session name
		// as given, and a refusal in words is what a caller gets when the runtime does not answer.
		send: func(_ context.Context, session, text string) error {
			var refusal strings.Builder
			if code := clientruntime.DeliverToSession(session, text, io.Discard, &refusal); code != clientcmd.ExitOK {
				why := strings.TrimSpace(refusal.String())
				if why == "" {
					why = "the runtime did not answer and said nothing"
				}
				return errors.New(why)
			}
			return nil
		},
		wall: watchWall,
	}
	if restoreSteps != nil {
		restoreSteps(r)
	}
	return r, nil
}

// restore is the shell's continue_rig() body, and it answers whether the rig ended up usable: false when
// the restore left no seats up, or when a brief the caller asked for is not there to send.
//
// The order is the shell's, and each step is load-bearing:
//
//  1. a rig whose sessions are ALREADY there is not restored: a second `rig up --existing` on a live rig
//     is what this verb exists to avoid;
//  2. seats are counted from tmux AFTER the restore, because that reading - not the command's status - is
//     what says whether the restore worked;
//  3. no seats means the brief would go to nobody, so this aborts for that rig instead of reporting a
//     success it did not get.
func (r *restorer) restore(rigName, brief string) bool {
	if len(rigSessions(rigName, sessionNames())) > 0 {
		r.log.printf("%s: already live, skipping restore", rigName)
	} else {
		r.log.printf("%s: restoring from latest snapshot (every seat resumes its own session)", rigName)
		if err := r.revive(context.Background(), rigName); err != nil {
			// The shell sent this command's output to the log and did not stop on its status; the seat
			// count is what decides, so a failed restore has to be reported here instead of silently
			// becoming "no seats up".
			r.log.printf("%s: restore command failed: %v", rigName, err)
		}
	}
	seats := rigSeats(rigName, sessionNames())
	r.log.printf("%s: seats live: %d", rigName, len(seats))
	if len(seats) == 0 {
		r.log.printf("%s: no seats up, aborting for this rig", rigName)
		return false
	}
	if brief == "" {
		r.log.printf("%s: no brief requested", rigName)
	} else {
		body, err := os.ReadFile(brief)
		if err != nil {
			r.log.printf("%s: brief file missing (%s) — restored but nothing sent", rigName, brief)
			return false
		}
		if undelivered := r.brief(rigName, seats, string(body)); len(undelivered) > 0 {
			// R3: a delivery is never reported as delivered. The seats are up but the brief the caller asked
			// for did not arrive, so this restore did NOT complete and answers false - which is what the doc
			// above already promised ("false when a brief the caller asked for is not there to send") and what
			// the caller's own exit code and summary hang off. The seats that were missed are named here.
			r.log.printf("%s: brief did not reach %s", rigName, strings.Join(undelivered, ", "))
			return false
		}
	}
	r.openWatch(rigName)
	// The pointer names the CLIENT's own read - `4genteam ps <RIG> --nodes`, which reads the node table through
	// the runtime (RoomNodes) - and not `rig ps --nodes --rig RIG`, which is the tool this cutover removes and
	// which an operator following the old line would find missing (the gate's nit at bb4f412).
	r.log.printf("%s: done — verify with: 4genteam ps %s --nodes", rigName, rigName)
	return true
}

// brief sends each seat its text: `Your seat: <seat>. <brief>`, with the seat's OWN brief
// (briefs/<rig>/<seat>.txt) replacing the shared one, so no generic text is pushed onto a seat that has
// its own. Both texts keep the shell's `$(cat …)` shape: trailing newlines are dropped, because the rig
// tool presses Enter after what it sends and a trailing newline would submit an empty prompt.
//
// IT RETURNS THE SEATS ITS TEXT DID NOT REACH, because a brief that was asked for and did not arrive is not a
// success. The log line alone left the caller answering true - "done" over a seat nobody briefed - which is the
// failure the restorer's own doc says its `false` is for: a brief the caller asked for is not there to send.
func (r *restorer) brief(rigName string, seats []string, body string) []string {
	body = strings.TrimRight(body, "\n")
	var undelivered []string
	for _, seat := range seats {
		text := "Your seat: " + seat + ". " + body
		if own, err := os.ReadFile(filepath.Join(r.briefs, rigName, seat+".txt")); err == nil {
			text = "Your seat: " + seat + ". " + strings.TrimRight(string(own), "\n")
		}
		ctx, cancel := context.WithTimeout(context.Background(), briefSendTimeout)
		err := r.send(ctx, rigName+"-"+seat+"@"+rigName, text)
		cancel()
		if err != nil {
			r.log.printf("%s: send failed for %s", rigName, seat)
			undelivered = append(undelivered, seat)
			continue
		}
		r.log.printf("%s: briefed %s", rigName, seat)
	}
	return undelivered
}

// watchEnv is the shell's RIG_CONTINUE_WATCH: 0 skips the wall, which is what keeps a headless resume -
// a seat, a cron-adjacent watchdog, an ssh session - from trying to build a terminal it does not have.
const watchEnv = "RIG_CONTINUE_WATCH"

// openWatch opens the owner's watch wall for this rig, in the one fixed style the watch verb defines: the
// wall replaces any earlier wall of the same rig, so a resume or a runtime switch never stacks them - and
// that rule lives in the watch verb, not here. The shell logged a skip or a failure and carried on, and so
// does this: the wall is the window onto the room, not part of the restore.
func (r *restorer) openWatch(rigName string) {
	if os.Getenv(watchEnv) == "0" {
		return
	}
	opened, why := r.wall(rigName)
	switch {
	case opened:
		r.log.printf("%s: watch wall opened", rigName)
	case why == wallNoTerminal:
		r.log.printf("%s: watch wall skipped (herdr or 4genteam missing)", rigName)
	default:
		r.log.printf("%s: watch wall failed (%s)", rigName, why)
	}
}

// wallNoTerminal is the ONE reason the wall is SKIPPED rather than failed, because the shell drew that
// distinction too: a machine without herdr cannot host a wall at all, while a wall that was asked for and
// did not open is a failure a reader should see.
const wallNoTerminal = "no terminal host"

// watchWall starts this same binary's watch view, which is what the shell ran (`4genteam watch watch
// --rig RIG`: the Python's group and its `watch` sub-verb are one verb here). herdr hosts the wall, so a
// machine without it is a skip with a reason; the wall's own output goes to this verb's log, where the
// shell redirected it too.
func watchWall(rigName string) (bool, string) {
	if _, err := execLookPath("herdr"); err != nil {
		return false, wallNoTerminal
	}
	self, err := execSelfPath()
	if err != nil {
		return false, "see log"
	}
	dir, err := logDir()
	if err != nil {
		return false, "see log"
	}
	out, err := os.OpenFile(filepath.Join(dir, continueLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, "see log"
	}
	defer func() { _ = out.Close() }()
	cmd := execCommand(self, "watch", "--rig", rigName)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
	if err := cmd.Run(); err != nil {
		return false, "see log"
	}
	return true, ""
}

// daemonUp is the shell's daemon_up(): the runtime daemon must be up before a rig is restored - and before the
// peak gate, which reads the room's node records from it. The start's failure is logged rather than
// returned, the way the shell carried on: the step that needs the daemon reports what it could not do.
func (r *restorer) daemonUp() {
	if err := r.ensureDaemon(context.Background()); err != nil {
		r.log.printf("daemon did not start: %v", err)
	}
}

// reviveRoom starts the stopped seats of a room the runtime holds a spec for. Each seat resumes its own saved
// session, which is what the rig orchestrator snapshot restore did. A seat that already runs is left alone: a restart
// would cycle live agents, and `continue` on a live room must change nothing. A room the runtime does not
// know is the daemon's refusal, returned as it came.
func reviveRoom(_ context.Context, room string) error {
	client := clientruntime.NewClient("")
	seats, err := client.GetRoomStatus(room)
	if err != nil {
		return err
	}
	var failed []string
	for _, seat := range seats {
		if strings.EqualFold(strings.TrimSpace(seat.State), "running") {
			continue
		}
		if _, err := client.RestartSeat(room, seat.Seat); err != nil {
			failed = append(failed, seat.Seat+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return errors.New("seats that did not start: " + strings.Join(failed, "; "))
	}
	return nil
}

// gate is the DeepSeek peak gate for a restore, through the same exported gate `up` and `compact` obey -
// one home for the decision, so a restore here and a restore there cannot disagree. Its refusal is written
// to the caller's stderr AND to this verb's log: a room left stopped for billing reasons says so in the
// record, which is the whole point of a watchdog whose restore refuses.
func (r *restorer) gate(rigName, verb string, allowPeak bool, stderr io.Writer) int {
	var refusal strings.Builder
	code := clientlifecycle.PeakGate(rigName, verb, allowPeak, io.MultiWriter(stderr, &refusal))
	if code != clientcmd.ExitOK {
		if text := strings.TrimRight(refusal.String(), "\n"); text != "" {
			r.log.printf("%s", text)
		}
	}
	return code
}
