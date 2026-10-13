// The compaction supervisor: the loop, one pass of it, and the start/stop/status of a supervisor
// process, ported from compact.py and cli.py.
package clientlifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
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
}

// Supervisor is one `compact-run`: what it watches, its limits, and the seams the suite replaces.
// Everything external - the seat list, its log, the rig tool, the clock - is a field, so one pass is
// testable with no rig, no tmux and no waiting.
type Supervisor struct {
	Rig          string
	Quiet        int
	StateRoot    string
	Forcecompact string
	Out          io.Writer

	Now        func() float64
	Seats      func(rig string) []string
	SeatLog    func(rig, seat string) (string, string)
	Idle       func(rig, seat string) bool
	Say        func(rig, seat, text string, raw bool)
	RPCCompact func(rig, seat string) string
	Abort      func(rig, seat string)
	Interrupt  func(rig, seat string)
	Log        func(text string)
	// Nodes is the room's node records - the reading the off-peak gate decides over, the same one the
	// gated verbs take. Getenv is the environment the gate resolves its setting and its clock from.
	Nodes  func(rig string) []map[string]any
	Getenv func(string) string

	State map[string]*seatState
}

// New is a supervisor wired to the real world: tmux for the seat list, OpenRig for idleness and
// messages, and the forcecompact helper for an omp compact.
func New(rig string, quiet int, stateRoot, forcecompact string, out io.Writer) *Supervisor {
	supervisor := &Supervisor{
		Rig:          rig,
		Quiet:        quiet,
		StateRoot:    stateRoot,
		Forcecompact: forcecompact,
		Out:          out,
		State:        map[string]*seatState{},
	}
	supervisor.Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	supervisor.Seats = seatlog.RigSeats
	supervisor.SeatLog = func(r, seat string) (string, string) { return seatlog.SeatLog(stateRoot, r, seat) }
	supervisor.Idle = rigIdle
	supervisor.Say = rigSay
	supervisor.RPCCompact = func(r, seat string) string { return supervisor.rpcCompact(r, seat) }
	supervisor.Abort = rigAbort
	supervisor.Interrupt = tmuxInterrupt
	// The gate's reading is the room's `rig ps --nodes` records, the ONE reading this package already
	// takes for its other gate (rigNodes), reached through the platform matrix in one place.
	supervisor.Nodes = func(rig string) []map[string]any {
		tool, err := clientcmd.RequireRig()
		if err != nil {
			return nil
		}
		return rigNodes(tool, rig)
	}
	supervisor.Getenv = os.Getenv
	supervisor.Log = func(text string) {
		fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text)
	}
	return supervisor
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
			done := compactions(path) > state.count || float64(usedValue) < float64(state.before)*0.6
			if done || now-state.sent > witnessWindow {
				s.Log(fmt.Sprintf("%s: /compact %s (%s -> %s)", seat, witnessed(done),
					seatlog.Kilo(state.before), seatlog.Kilo(usedValue)))
				state.hasBefore = false
				if !done {
					state.sent = 0
				}
				if done {
					// The resume is an AUTOMATIC wake, so it passes the same gate every other automatic
					// wake passes. --allow-peak is never automatic on this path: allowPeak is false, so
					// the gate is ON unless the setting itself turns it off. A refusal (or a schedule the
					// gate cannot read) holds the resume back and says why in this log, so a held-back
					// resume is observable rather than silent.
					if proceed, message, err := clientoffpeak.Check(false, s.Nodes(s.Rig), s.Getenv); err != nil {
						s.Log(fmt.Sprintf("%s: resume held, off-peak gate: %v", seat, err))
					} else if !proceed {
						s.Log(fmt.Sprintf("%s: resume held, %s", seat, message))
					} else {
						s.Say(s.Rig, seat, resumeText, false)
						s.Log(seat + ": told to resume")
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
	if err := clientservices.WriteHeartbeat(clientservices.Compact, s.Rig); err != nil {
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

func rigIdle(rig, seat string) bool {
	path, err := exec.LookPath("rig")
	if err != nil {
		return false
	}
	out, err := exec.Command(path, "ps", "--nodes", "--rig", rig, "--json").Output()
	if err != nil {
		return false
	}
	var nodes []map[string]any
	if json.Unmarshal(out, &nodes) != nil {
		return false
	}
	for _, node := range nodes {
		if node["canonicalSessionName"] != rig+"-"+seat+"@"+rig {
			continue
		}
		activity, _ := node["agentActivity"].(map[string]any)
		return activity["state"] == "idle"
	}
	return false
}

// rigSay sends a seat a message through the rig tool. A plain message waits for the seat to go idle,
// which must not hold up the other seats, so it is started and released rather than waited on; a raw
// one is the exact text (without it the tool wraps the message in a From/To envelope and a /compact
// becomes plain text).
func rigSay(rig, seat, text string, raw bool) {
	path, err := exec.LookPath("rig")
	if err != nil {
		return
	}
	args := []string{"send", rig + "-" + seat + "@" + rig, text, "--wait-for-idle", "120"}
	if raw {
		args = append(args, "--raw")
	}
	cmd := exec.Command(path, args...)
	if raw {
		_ = cmd.Run()
		return
	}
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

// runtimeSendText is the ONE place ABORT types text into a seat's pane - NOT yet the package's only send: rigSay
// ABOVE still builds its own `rig send`, and the remaining sites are converted one by one. A package-level variable
// rather than an interface added for testing, which is the repo's rule, and it is what makes the no-envelope property
// of rigAbort BELOW observable: the test replaces it and asserts the exact barrier text.
var runtimeSendText = func(session, text string) error {
	return clientruntime.NewClient("").SendText(session, text)
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
	_ = runtimeSendText(rig+"-"+seat+"@"+rig, "/abort")
}

func tmuxInterrupt(rig, seat string) {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return
	}
	_ = exec.Command(path, "send-keys", "-t", rig+"-"+seat+"@"+rig, "Escape").Run()
}

// rpcCompact sends an RPC compact straight to the seat's omp, which reads it whatever the seat is doing.
// The helper is not part of this binary: it needs CAP_SYS_PTRACE, so it is named on the command line or
// through 4GENTEAM_FORCECOMPACT, and a machine without it says so in one line rather than pretending.
func (s *Supervisor) rpcCompact(rig, seat string) string {
	if s.Forcecompact == "" {
		return "forcecompact is not configured (set 4GENTEAM_FORCECOMPACT or pass --forcecompact): " +
			"an omp seat past the limit cannot be compacted"
	}
	out, err := exec.Command(s.Forcecompact, rig+"-"+seat+"@"+rig).CombinedOutput()
	if text := strings.TrimSpace(string(out)); text != "" {
		return text
	}
	if err != nil {
		return err.Error()
	}
	return ""
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
// IT IS THE START OF EVERY DETACHED CHILD THIS CLIENT STARTS - rigd itself from `up`, and rigd's own
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
func SupervisorStatus(name string, stdout, stderr io.Writer) {
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
	logPath, err := LogPath(name)
	if err != nil {
		return
	}
	raw, err := os.ReadFile(logPath)
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
