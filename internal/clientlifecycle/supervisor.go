// The compaction supervisor: the loop, one pass of it, and the start/stop/status of a supervisor
// process, ported from compact.py and cli.py.
package clientlifecycle

import (
	"errors"
	"fmt"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

const (
	// tailBytes is how much of a seat's log the newest reading is taken from: it is a LAST reading, so
	// the tail is enough and the file may be gigabytes.
	tailBytes = 2_000_000
	// interruptGap is the least time between two interrupts of the same seat.
	interruptGap = 60.0
	// cooldown is how long after a /compact the same seat is left alone.
	cooldown = 900.0
	// witnessWindow is how long an unwitnessed compaction is given before the supervisor says so and
	// tries again.
	witnessWindow = 180.0
)

// resumeText is what a compacted seat is told, so it carries on without the owner typing continue.
const resumeText = "Context was compacted. Continue the job you were on: re-read your role file and the board item " +
	"you held, check the working tree for what is already done, then carry on without waiting for me."

// seatState is what one seat's pass remembers between passes.
type seatState struct {
	told        bool
	warned      bool
	interrupted float64
	sent        float64
	before      int
	hasBefore   bool
	count       int
	// held records a resume the off-peak gate refused: the witness stays in place, so the resume is OWED
	// rather than lost, and this flag keeps the refusal (and the witness line beside it) from being logged
	// on every pass between the refusal and the window opening.
	held bool
	// reported is the compaction count the witness line has already been written for. The line is NEWS,
	// not a heartbeat: a hold suppresses writing the SAME witness again, and must not suppress a new one -
	// a compaction that lands while the resume is held is a second event, and an operator reading the log
	// over the window cannot otherwise see it.
	reported int
}

// Supervisor is one `compact-run`: what it watches, its limits, and the seams the suite replaces.
// Everything external - the seat list, its log, the rig tool, the clock - is a field, so one pass is
// testable with no rig, no tmux and no waiting.
type Supervisor struct {
	Rig       string
	Quiet     int
	StateRoot string
	Out       io.Writer

	Now     func() float64
	Seats   func(rig string) []string
	SeatLog func(rig, seat string) (string, string)
	Idle    func(rig, seat string) bool
	// Say sends one message to a seat and returns the transport's own outcome - nil when it accepted it, the
	// failure otherwise. The send is SYNCHRONOUS and refuses a mid-turn seat rather than waiting for it:
	// waiting was the `rig send --wait-for-idle` dance, and the runtime's own SendText has no such wait to
	// escape, so each caller's latch (or its compaction witness) is what makes a late arrival harmless.
	Say        func(rig, seat, text string, raw bool) error
	RPCCompact func(rig, seat string) string
	Abort      func(rig, seat string)
	Interrupt  func(rig, seat string)
	Log        func(text string)
	// IdleWhy carries the sentence behind an Idle=false, so the pass that ACTS on the false can say it rather than
	// letting "the seat is working" and "nothing ever answered" print the same way - as nothing. Empty means the
	// refusal was a deliberate one and there is nothing to say. nil means the wiring says nothing, as in tests.
	IdleWhy func(rig, seat string) string
	// Nodes is the room's node records - the reading the off-peak gate decides over, the same one the gated
	// verbs take - and it carries the three answers the reading itself has, because the resume below is an
	// AUTOMATIC seat action: a read that FAILED must hold the resume back rather than arrive at the gate as an
	// empty fleet (no DeepSeek seats, nothing to refuse, the seat told to resume with the gate never having
	// seen a seat). Answered-with-no-records still proceeds - a room with no seats has nothing to bill - so
	// the two must stay distinguishable here. Getenv is the environment the gate resolves its setting and its
	// clock from.
	Nodes  func(rig string) ([]map[string]any, bool, error)
	Getenv func(string) string

	State map[string]*seatState
}

// New is a supervisor wired to the real world: tmux for the seat list, the runtime for idleness and
// messages, and the runtime daemon for a compact.
func New(rig string, quiet int, stateRoot string, out io.Writer) *Supervisor {
	supervisor := &Supervisor{
		Rig:       rig,
		Quiet:     quiet,
		StateRoot: stateRoot,
		Out:       out,
		State:     map[string]*seatState{},
	}
	supervisor.Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	supervisor.Seats = func(rig string) []string { return seatlog.RigSeats(stateRoot, rig) }
	// The same resolver the idle guard binds: an agy seat's context is read from its live transcript,
	// not from the omp log it left behind (measured 2026-10-17: ps said 53k while the stale file said 14h).
	supervisor.SeatLog = func(r, seat string) (string, string) { return seatlog.SeatLog(stateRoot, r, seat) }
	supervisor.Idle = rigIdle
	supervisor.IdleWhy = func(rig, seat string) string { _, why := rigIdleReason(rig, seat); return why }
	supervisor.Say = rigSay
	supervisor.RPCCompact = func(r, seat string) string { return supervisor.rpcCompact(r, seat) }
	supervisor.Abort = rigAbort
	supervisor.Interrupt = func(rig, seat string) { tmuxInterrupt(stateRoot, rig, seat) }
	// The gate's reading is the room's node records, the ONE reading this package already takes for its other
	// gate (rigNodes), reached here without resolving a rig tool: the reading is the client's own. The seam
	// carries all three of that reading's answers, so the resume path below can tell a read that FAILED from a
	// room with no seats; the peaked decision that must refuse is still PeakGate's above, which sees its own.
	supervisor.Nodes = func(rig string) ([]map[string]any, bool, error) {
		// rigNodes is read at CALL time rather than captured at wiring time, so a test that replaces the
		// reading after the supervisor is built still reaches the gate.
		return rigNodes(rig)
	}
	supervisor.Getenv = os.Getenv
	supervisor.Log = func(text string) {
		fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text)
	}
	return supervisor
}

// holdResume records a resume the off-peak gate refused. The witness stays where it is (the caller does not
// clear it), so the resume is owed and the next pass asks again; this only makes the refusal, and the
// witness line beside it, once-per-hold instead of once-per-pass.
func (s *Supervisor) holdResume(seat string, state *seatState, reason string) {
	if state.held {
		return
	}
	state.held = true
	s.Log(fmt.Sprintf("%s: resume held, %s", seat, reason))
}

// releaseResume sends the resume when the gate is open, and clears the witness ONLY when the transport
// accepted it: a refusal means the seat never saw the resume, and clearing the witness then would make
// that loss final - the next pass could never re-ask. The runtime's SendText carries its own deadline
// (clientruntime.CallDeadline, applied to the socket), so this call cannot hang a pass the way a `rig
// send` subprocess could, and no second timer is invented here.
func (s *Supervisor) releaseResume(seat string, state *seatState) {
	if state.held {
		s.Log(seat + ": resume released, the off-peak gate is open")
	}
	if err := s.Say(s.Rig, seat, resumeText, false); err != nil {
		s.Log(fmt.Sprintf("%s: resume not delivered (%v), the witness stays and the gate is asked again", seat, err))
		return
	}
	state.hasBefore, state.held = false, false
	s.Log(seat + ": told to resume")
}

// Step is one pass over the rig's live seats. The order is compact.py's step(), and the three states it
// remembers per seat are what keep a notice from being repeated and a compaction from being demanded
// again inside the cooldown.
//
// EVERY PASS BEATS, at the end, whatever the pass did. That is the whole point of the heartbeat (7.2): this
// loop logs only when it ACTS, so on a quiet rig a healthy supervisor writes nothing and its log age
// cannot tell a dead supervisor from an idle one - which is the incident the report exists for.
func (s *Supervisor) Step() {
	now := s.Now()
	for _, seat := range s.Seats(s.Rig) {
		path, runtime := s.SeatLog(s.Rig, seat)
		if path == "" {
			continue
		}
		used, hasUsed := lastContext(path)
		usedValue := 0
		if hasUsed {
			usedValue = used
		}
		state := s.State[seat]
		if state == nil {
			state = &seatState{}
			s.State[seat] = state
		}
		if state.hasBefore {
			count := compactions(path)
			done := count > state.count || float64(usedValue) < float64(state.before)*0.6
			if done || now-state.sent > witnessWindow {
				// The witness line is news, not a heartbeat, and the hold is what makes that matter: the
				// same witness is written ONCE per hold instead of once per pass, and a witness that MOVED
				// - a second compaction landing while the resume is held - is written again, so an
				// operator reading the log over the window sees every compaction and not only the first.
				if !state.held || count != state.reported {
					s.Log(fmt.Sprintf("%s: /compact %s (%s -> %s)", seat, witnessed(done),
						seatlog.Kilo(state.before), seatlog.Kilo(usedValue)))
					state.reported = count
				}
				// A HELD WITNESS IS NEVER DROPPED. The window is what stops the supervisor waiting for a
				// compaction that never came - but while a resume is OWED the refusal deferred it, and 7.9
				// sends it at the first open minute, so expiring the window must not abandon the witness.
				// Without a hold the old behaviour stands: an unwitnessed compaction is reported and the
				// next one is demanded instead.
				if !done && !state.held {
					state.hasBefore = false
					state.sent = 0
				}
				if done || state.held {
					// The resume is an AUTOMATIC wake, so it passes the same gate every other automatic
					// wake passes. --allow-peak is never automatic on this path: allowPeak is false, so
					// the gate is ON unless the setting itself turns it off.
					//
					// A REFUSAL DEFERS THE RESUME, IT DOES NOT DROP IT. 7.9: at peak "the message is
					// held, and it is sent at the first open minute". The witness stays in place, so the
					// next pass asks the gate again and the resume goes out the moment the window opens.
					// `done` cannot regress while it waits when the COUNT clause witnessed it - that
					// record is in the log, which is read WHOLE, and the log is append-only. When the
					// DROP clause witnessed it, `done` CAN regress if the seat works again, which is why
					// `state.held` keeps such a witness and why the gate is re-asked even with done
					// false. The hold itself is logged ONCE: this loop logs when it ACTS, and a held
					// resume is one decision, not one per pass.
					nodes, ok, err := s.Nodes(s.Rig)
					if err != nil {
						// FAILING OPEN WOULD BE THE BUG HERE: an unreadable reading is not an empty room, and a
						// resume that proceeded on one would be the gate deciding over no seats at all.
						s.holdResume(seat, state, "the room's node records could not be read: "+err.Error())
					} else if ok {
						if proceed, message, checkErr := clientoffpeak.Check(false, nodes, s.Getenv); checkErr != nil {
							s.holdResume(seat, state, "off-peak gate: "+checkErr.Error())
						} else if !proceed {
							s.holdResume(seat, state, message)
						} else {
							s.releaseResume(seat, state)
						}
					} else {
						// NO RECORDS is not a failure: there are no seats here to bill, so the gate has
						// nothing to hold back and the resume proceeds - said plainly rather than implied
						// by silence.
						s.releaseResume(seat, state)
						s.Log(seat + ": told to resume (the room's records list no seats)")
					}
				}
			}
		}
		if !hasUsed || used < seatlog.CompactLimit {
			state.told, state.warned = false, false
			continue
		}
		if !state.told {
			s.Say(s.Rig, seat, s.notice(), false)
			state.told = true
			s.Log(fmt.Sprintf("%s: told, %s of %s (%s)", seat, seatlog.Kilo(used),
				seatlog.Kilo(seatlog.CompactLimit), runtime))
		}
		if used >= seatlog.WarnLimit && !state.warned {
			s.Say(s.Rig, seat, warningText(), false)
			state.warned = true
			s.Log(fmt.Sprintf("%s: warned, %s of %s", seat, seatlog.Kilo(used), seatlog.Kilo(seatlog.HardLimit)))
		}
		silent := now - modTime(path)
		urgent, hard := used >= seatlog.WarnLimit, used >= seatlog.HardLimit
		if now-state.sent <= cooldown {
			continue
		}
		if runtime == "omp" && urgent {
			if hard {
				s.Abort(s.Rig, seat)
			}
			state.sent, state.before, state.hasBefore, state.count = now, used, true, compactions(path)
			limit, aborted := "warn", ""
			if hard {
				limit, aborted = "hard", ", aborted"
			}
			s.Log(fmt.Sprintf("%s: %s past the %s limit%s: %s", seat, seatlog.Kilo(used), limit, aborted,
				s.RPCCompact(s.Rig, seat)))
			continue
		}
		if hard && !s.Idle(s.Rig, seat) {
			if now-state.interrupted > interruptGap {
				s.Interrupt(s.Rig, seat)
				state.interrupted = now
				s.Log(fmt.Sprintf("%s: interrupted at %s (hard limit)", seat, seatlog.Kilo(used)))
				// AND WHY NOTHING WAS COMPACTED, on this line, where the false is ACTED ON: this branch returns every
				// pass, so the quiet-triggered compact below never fires, and a read that answered nothing must not
				// look the same as a seat that is simply working. Rate-limited with the interrupt above; a deliberate
				// refusal (a busy seat) carries no sentence and says nothing here.
				if s.IdleWhy != nil {
					if why := s.IdleWhy(s.Rig, seat); why != "" {
						s.Log(fmt.Sprintf("%s: not compacting at %s: %s", seat, seatlog.Kilo(used), why))
					}
				}
			}
			continue
		}
		if (urgent || silent >= float64(s.Quiet)) && s.Idle(s.Rig, seat) {
			state.sent, state.before, state.hasBefore, state.count = now, used, true, compactions(path)
			s.Say(s.Rig, seat, "/compact", true)
			note := ""
			if urgent {
				note = " (past the warn limit)"
			}
			s.Log(fmt.Sprintf("%s: quiet %.0fs at %s, sent /compact%s", seat, silent, seatlog.Kilo(used), note))
		}
	}
	// The heartbeat, through clientservices: one write per pass, so a live-but-hung supervisor reads
	// `silent` and a supervisor that died reads `stopped` (7.2). A failure to write is reported here and
	// nowhere else - the report will say what it says.
	if err := clientservices.WriteHeartbeat(s.StateRoot, clientservices.Compact, s.Rig); err != nil {
		s.Log("cannot write the heartbeat: " + err.Error())
	}
}

func witnessed(done bool) string {
	if done {
		return "WITNESSED"
	}
	return "NOT witnessed"
}

// notice is the soft-limit message. It states the REAL quiet window: the Python hard-coded "15 seconds"
// while COMPACT_QUIET_SECONDS is configurable, so an operator who moved that window was told the default.
func (s *Supervisor) notice() string {
	return fmt.Sprintf(
		"Context limit: your session is past %s tokens. "+
			"Find the next safe point yourself: finish or checkpoint the step you are on, note in your board item "+
			"what is done and what comes next, then stop and send nothing. "+
			"Do not type /compact, a /compact sent while you work is only queued as text. "+
			"The supervisor compacts you once you are idle for %d seconds, then tells you to resume and you carry on.",
		seatlog.Kilo(seatlog.CompactLimit), s.Quiet)
}

func warningText() string {
	return fmt.Sprintf(
		"VERY IMPORTANT: your session is past %s tokens. "+
			"Reach a safe point and stop NOW. "+
			"A compaction is being requested now and runs at your next turn boundary. "+
			"If you are still running at %s your turn is aborted and /compact is forced, "+
			"and whatever you had not written down is lost. Write down the job, what is done, what is next and the files you hold.",
		seatlog.Kilo(seatlog.WarnLimit), seatlog.Kilo(seatlog.HardLimit))
}

// lastContext is last_context: the tokens of the NEWEST reading in the log, taken from its tail because
// it is a last reading.
func lastContext(path string) (int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, false
	}
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, false
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if tokens, ok := seatlog.ContextTokens(lines[i]); ok && tokens > 0 {
			return tokens, true
		}
	}
	return 0, false
}

// compactions counts the compaction records in a log. Unlike the token reading this is over the WHOLE
// file: a compaction that has scrolled out of the tail still happened.
func compactions(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), `"type":"compaction"`)
}

func modTime(path string) float64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(info.ModTime().UnixNano()) / 1e9
}

// rigIdle reads a seat's idleness through clientruntime.ReadSeatActivity, the runtime's own session list.
// Every failure answers false, and false means "not idle" to both callers: a seat that cannot be proved
// idle WAITS rather than racing a working turn, because a check that failed open would send /compact into a
// running seat, which is the one outcome the gate exists to prevent.
func rigIdle(rig, seat string) bool {
	idle, _ := rigIdleReason(rig, seat)
	return idle
}

// rigIdleReason is rigIdle with the sentence that says WHY a false came back, and it exists because the two kinds of
// false are not the same fact: "the seat is working" is a healthy answer, while "nothing ever answered" is this box's
// actual state - on the compaction path the not-idle branch is taken EVERY pass (`:196`, rate-limited, interrupt and
// continue), so the quiet-triggered compact below it never fires and a supervisor that never compacts looks exactly
// like a supervisor with nothing to do. The bool the Idle hook trades in stays a bool; the sentence is said where the
// false is ACTED ON, and only when the read could not answer at all.
func rigIdleReason(rig, seat string) (idle bool, why string) {
	read := clientruntime.ReadSeatActivity(rig, seat)
	if !read.Read {
		// THE SENTENCE IS THE READ'S, not this caller's: it names what could not be asked - no daemon named in the
		// environment, the rig absent from the list, the route's HTTP status, no record for this seat. One read, one
		// diagnosis; the two readers used to word their own failures, and the wording drifted with the program they
		// exec'd.
		return false, read.Why
	}
	return idleFromActivityWord(read.Word, read.Present)
}

// idleFromActivityWord is the pure half of the read: it turns the word the node table carried into the answer the
// hook returns AND the sentence a caller can say. A recognized, non-quiet word is a DELIBERATE false and has nothing
// to report - the seat is simply busy; an absent or unrecognized one is the failure this box has been living in
// silently, and it is named rather than left to be discovered.
func idleFromActivityWord(word string, present bool) (idle bool, why string) {
	released, unrecognized := clientruntime.ActivityReleasesTheWait(word)
	if released {
		return true, ""
	}
	if unrecognized != "" {
		return false, fmt.Sprintf("the activity word %q is not one this client recognizes", unrecognized)
	}
	if !present {
		return false, "the node table carries no agentActivity for this seat, so nothing can say whether it is idle"
	}
	// UNKNOWN IS NAMED, because silence here is indistinguishable from a busy seat: `unknown` is a word this client
	// RECOGNIZES, so the branch above does not catch it, and it is the daemon's own never-evidence value - a report
	// that cannot tell. Measured on the live rig: architect and fe-dev both answer `unknown` and both used to give an
	// empty why, so two of five seats read exactly like a seat that is mid-turn when the truth was that the daemon
	// could not say. The definite words stay silent on purpose: `running`/`working`/`needs_input` are ANSWERS ("not
	// idle"), and a sentence is owed for a mystery, not for an answer.
	if word == "unknown" {
		return false, "the activity word for this seat is unknown, which the daemon's own taxonomy defines as not " +
			"evidence - a report that cannot tell is not a seat that is at its prompt"
	}
	if word == "" {
		return false, "the activity read answered, and the word it carried was empty"
	}
	return false, ""
}

// saySender is the "From" prefix a plain message carries, and it is REPRODUCED rather than invented: the client's own
// seat send writes these words when the caller names no sender (`clientseat`'s send verb, defaultSender = "owner
// session"), and the supervisor's messages are the same class - the client speaking to a seat it supervises. The
// constant lives here instead of being exported from the verb package because a supervisor importing a verb package
// for one string inverts the dependency; if a third writer appears, that is the moment to move it to the runtime
// client, not before.
const saySender = "owner session"

// rigSay sends a seat one message through the RUNTIME, not through the rig tool. Two differences from the shell-out
// it replaces, and both are deliberate:
//
//   - THE ENVELOPE is written HERE, because the runtime's send has none: it types the exact bytes it is given. A plain
//     message is prefixed "From <sender>: " so the seat reads who spoke, which is what the CLI's envelope did; a raw
//     message is the exact text, or a /compact arrives as prose the runner cannot act on.
//
//   - THE WAIT IS GONE, and its removal is declared rather than silent. The shell-out passed --wait-for-idle 120, and
//     WHAT THAT WAIT WAS WORTH DEPENDS ON WHICH SEND IT WAS, because the two shapes did not observe it equally:
//
//     FOR THE THREE PLAIN SENDS the wait was a PRETENCE. They were non-raw, so they ran `cmd.Start` + `Process.Release`
//     with no `Wait`: a detached CLI that timed out failed where nobody was listening, and this package never observed
//     its outcome at all - the pretence of a guarantee rather than one.
//
//     FOR /compact the wait was REDUNDANT, because raw was `cmd.Run`: that one DID observe its wait, and the call site
//     already gates the send on s.Idle. So the removal costs that caller nothing it was actually relying on - which is
//     a stronger reason than the pretence alone, and the reason the gate is named below rather than assumed.
//
//     The runtime's send has no idle wait to escape (the same reason rigAbort's start-and-release dance was
//     unnecessary), and BOTH idle readings on this machine are the same shell-out this unit removes: this file's
//     rigIdle, and QuietWaiter's Activity - clientseat's seatActivity, which also runs `rig ps --nodes --json` and reads
//     agentActivity.state. So honouring the wait would re-add the dependency being cut, in a second package.
//
//     EVERY CALLER THAT LOSES THE WAIT, and why each is safe. /compact (raw, on the urgent path) is gated by s.Idle at
//     its own call site - the one place a race with a working seat matters - so the wait was redundant with a gate that
//     was already there. The resume is sent after a WITNESSED compaction, which is a turn boundary, and text typed at a
//     boundary is the next prompt, which is the resume's purpose. The notice and the warning are each latched once by
//     state.told/state.warned and read whenever the seat next looks at its pane, so arriving later changes nothing
//     except that they are no longer held behind a busy seat.
func rigSay(rig, seat, text string, raw bool) error {
	session := clientruntime.SeatSessionName(rig, seat)
	if raw {
		return runtimeSendText(session, text, "say")
	}
	return runtimeSendText(session, "From "+saySender+": "+text, "say")
}

// runtimeSendText is the ONE place this package types text into a seat's pane. It is a package-level variable rather
// than an interface added for testing, which is the repo's rule, and it is what makes both sends' text observable:
// rigSay ABOVE rebuilds the CLI's "From <sender>: " envelope for a plain message and sends a raw one bare, and
// rigAbort BELOW sends the bare "/abort" keystroke. The one thing in this file that still reaches the rig tool is
// rigIdle, which READS and types nothing (declared as an exception above).
//
// The VERB is the third argument and it is not decoration: it rides the frame, and a text a mid-turn seat cannot
// take comes back to this session naming the way it arrived. The two carriers here name themselves.
var runtimeSendText = func(session, text, verb string) error {
	return clientruntime.NewClient("").SendText(session, text, verb)
}

// rigAbort sends /abort, the one pane text the omp runner turns into an abort of the running turn, and it goes
// out WITHOUT the From/To envelope the send verb adds: a sender prefix would arrive as a MESSAGE and the runner
// would see text where it needs a keystroke, so the bare "/abort" is the contract, not a detail. It is sent
// without waiting for idle, because the seat is working by definition - and the runtime's send has no idle wait
// to escape, so the old start-and-release dance is unnecessary rather than dropped.
func rigAbort(rig, seat string) {
	// The error is DELIBERATELY discarded, and it is the deliberate kind rather than an oversight: the caller is
	// aborting a turn that is already running, so a refusal has nothing useful to do here - the seat is being
	// interrupted either way, and the supervisor's next step is its own interrupt/escalation ladder, not a retry of
	// this keystroke. The old code discarded it too (Start + Release), so this is not a new silence. The alternative
	// is a supervisor log line, which this function cannot reach without a logger seam of its own: if a reader needs
	// the refusal visible, THAT seam is the change to make - not a silent retry here.
	_ = runtimeSendText(clientruntime.SeatSessionName(rig, seat), "/abort", "abort")
}

func tmuxInterrupt(root, rig, seat string) {
	cmd, err := clientenv.TmuxCommandAt(root, "send-keys", "-t", clientruntime.SeatSessionName(rig, seat), "Escape")
	if err != nil {
		return
	}
	_ = cmd.Run()
}

// rpcCompact asks the runtime daemon to compact the seat through its runner, which reads the request
// whatever the seat is doing.
func (s *Supervisor) rpcCompact(rig, seat string) string {
	answer, err := clientruntime.NewClient("").ForceCompact(clientruntime.SeatSessionName(rig, seat))
	if err != nil {
		return err.Error()
	}
	return answer
}

// supervisorChild is a started supervisor or bridge from the caller's point of view: its pid, and a
// Wait that returns when it exits.
type supervisorChild interface {
	Pid() int
	Wait() error
}

// spawnChild is a variable because the suite replaces it: a start that must be told apart from a child
// that died is not testable against a real process without waiting for one.
var spawnChild = spawnChildImpl

func spawnChildImpl(args []string, logPath string) (supervisorChild, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("this binary's own path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = log, log, nil
	cmd.Env = os.Environ()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	_ = log.Close()
	return &execChild{cmd: cmd}, nil
}

type execChild struct{ cmd *exec.Cmd }

func (c *execChild) Pid() int { return c.cmd.Process.Pid }

func (c *execChild) Wait() error {
	if err := c.cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &ChildError{Status: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

// awaitStart waits out the start grace: true when the child is still running, false with the child's
// own error when it exited inside the window.
//
// IT IS THE START OF EVERY DETACHED CHILD THIS CLIENT STARTS - agent itself from `up`, and agent's own
// children from the supervision loop - which is why it takes any supervisorChild rather than a specific
// caller's child.
func awaitStart(child supervisorChild) (bool, error) {
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		return false, err
	case <-time.After(SupervisorStartGrace):
		return true, nil
	}
}

// SupervisorStatus prints the supervisor's process and the last five lines of its log. The Python
// filtered pgrep's own line out of the output, and so does this.
func SupervisorStatus(name, root string, stdout, stderr io.Writer) {
	pgrep, err := exec.LookPath("pgrep")
	if err != nil {
		fmt.Fprintln(stderr, "4genteam status: pgrep is not on PATH, and this verb looks for the supervisor with it")
		return
	}
	out, _ := exec.Command(pgrep, "-af", SupervisorPattern(name)).Output()
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" && !strings.Contains(line, "pgrep") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		fmt.Fprintln(stdout, "compact supervisor: not running")
	} else {
		fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	}
	raw, err := os.ReadFile(LogPath(name, root))
	if err != nil {
		return
	}
	tail := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(tail) == 1 && tail[0] == "" {
		return
	}
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}
	fmt.Fprintln(stdout, strings.Join(tail, "\n"))
}

// stopPattern is pkill -f in one place. False means nothing matched OR pkill is not here; both are
// "not running" from the caller's point of view. A var so a test asserts WHAT would be signalled
// without signalling it - the difference between testing stop and stopping the machine's services.
var stopPattern = func(pattern string) bool {
	path, err := exec.LookPath("pkill")
	if err != nil {
		return false
	}
	return exec.Command(path, "-f", pattern).Run() == nil
}
