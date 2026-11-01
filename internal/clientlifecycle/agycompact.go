// The compact an agy seat arranges for itself, at the tool call that crossed the limit.
//
// agy has no runner, so the client cannot compact it mid-turn: a /compact typed while a turn runs is only queued. The
// supervisor can only wait for a quiet prompt, or interrupt. agy's own hooks give the client the two moments it needs,
// and both are measured on agy 1.4.12 (scratch workspace, `-p` and interactive):
//
//   - PreInvocation runs before every model call, so after every batch of tool calls, built-in (run_command, read_file,
//     write_file) and MCP alike. A PostToolUse answer can carry nothing to the model, and no token reading can change
//     between a tool and the next model call, so PreInvocation alone is the trigger: it reads the context, and past
//     COMPACT_LIMIT_TOKENS it arranges the compact and tells the model to end the turn. A model that calls a tool anyway
//     is stopped by the next PreInvocation answering "terminate", which costs no model call.
//   - Stop runs when the execution loop ends, so it is the one signal that says the seat is at its prompt: a quiet log
//     cannot say that during a long run_command. It starts the detached sender, which types /compact once, witnesses
//     the CHECKPOINT and sends the resume, so a room with no supervisor (`up --room-only`) is served the same way.
//
// THE CLAIM IS WHAT KEEPS THIS AND THE SUPERVISOR FROM BOTH COMPACTING: one file per seat, taken under a lock, fresh for
// the supervisor's own witness window. Whoever holds a fresh claim is the one who types /compact.
package clientlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

const (
	// compactClaimFile and compactHookMarkFile live in the seat's own directory.
	compactClaimFile    = "compact-claim.json"
	compactHookMarkFile = "compact-hook-conversation"
	compactTriggerLog   = "compact-trigger.log"
	// claimLockStale is the age past which a lock file is a dead process's and is taken over.
	claimLockStale = 5 * time.Second
	// claimLockWait bounds the wait for a lock: a hook blocks the agent loop, so it gives up rather than queue.
	claimLockWait = 2 * time.Second

	claimByHook       = "hook"
	claimBySupervisor = "supervisor"

	// compactSettle is the pause between Stop and the typing: the hook runs before agy has redrawn its prompt.
	compactSettle = 1500 * time.Millisecond
	// compactPoll is how often the sender looks for the CHECKPOINT.
	compactPoll = 2 * time.Second
	// resumeGatePoll and resumeGateLimit bound the wait for an off-peak gate that refuses the resume.
	resumeGatePoll  = time.Minute
	resumeGateLimit = 12 * time.Hour
)

// compactClaim is the file: who holds this seat's next /compact, since when, and how far it has got.
type compactClaim struct {
	By     string  `json:"by"`
	At     float64 `json:"at"`
	Used   int     `json:"used"`
	Count  int     `json:"count"`
	Told   int     `json:"told"`
	SentAt float64 `json:"sentAt,omitempty"`
	Path   string  `json:"path,omitempty"`
}

func (c compactClaim) fresh(now float64) bool {
	latest := c.At
	if c.SentAt > latest {
		latest = c.SentAt
	}
	return now-latest <= witnessWindow
}

// withClaimLock runs fn holding the seat's claim lock. A lock that cannot be had in claimLockWait is an error, and the
// caller then does nothing, because acting without the lock is how both sides would type /compact.
func withClaimLock(seatDir string, fn func() error) error {
	lock := filepath.Join(seatDir, compactClaimFile+".lock")
	deadline := time.Now().Add(claimLockWait)
	for {
		file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = file.Close()
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > claimLockStale {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the claim lock %s is held", lock)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() { _ = os.Remove(lock) }()
	return fn()
}

func readClaim(seatDir string) (compactClaim, bool) {
	raw, err := os.ReadFile(filepath.Join(seatDir, compactClaimFile))
	if err != nil {
		return compactClaim{}, false
	}
	var claim compactClaim
	if json.Unmarshal(raw, &claim) != nil {
		return compactClaim{}, false
	}
	return claim, true
}

func writeClaim(seatDir string, claim compactClaim) error {
	raw, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	path := filepath.Join(seatDir, compactClaimFile)
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// supervisorClaim takes the claim for a /compact the supervisor is about to type, and answers whether it may.
func supervisorClaim(seatDir string, used, count int, now float64) bool {
	taken := false
	err := withClaimLock(seatDir, func() error {
		if held, ok := readClaim(seatDir); ok && held.By == claimByHook && held.fresh(now) {
			return nil
		}
		taken = true
		return writeClaim(seatDir, compactClaim{By: claimBySupervisor, At: now, SentAt: now, Used: used, Count: count})
	})
	return taken && err == nil
}

// hookHoldsClaim says the hook path owns this seat's compact right now, so the supervisor leaves the seat alone.
func hookHoldsClaim(seatDir string, now float64) bool {
	held, ok := readClaim(seatDir)
	return ok && held.By == claimByHook && held.fresh(now)
}

// compactHookLive says this seat's current conversation runs the hook. A notice typed mid-turn is queued, starts a turn
// when the running one ends and would catch the /compact behind it mid-turn, so a seat with a live hook gets no notice.
func compactHookLive(seatDir string) bool {
	mark, err := os.ReadFile(filepath.Join(seatDir, compactHookMarkFile))
	if err != nil {
		return false
	}
	current, err := os.ReadFile(filepath.Join(seatDir, "agy-conversation"))
	if err != nil {
		return false
	}
	id := strings.TrimSpace(string(current))
	return id != "" && strings.TrimSpace(string(mark)) == id
}

// compactTrigger is one seat, as the hook and the MCP server see it from inside the seat's environment.
type compactTrigger struct {
	Root, Room, Seat string
	Now              func() float64
	// Spawn starts the detached sender; a seam because a test must not start a process.
	Spawn func(root, room, seat string) error
}

func (t *compactTrigger) seatDir() string { return clientruntime.SeatDir(t.Root, t.Room, t.Seat) }

// compactEndTurnText is what the model is told, by the hook's injected step or in the MCP tool's result.
func compactEndTurnText(used int) string {
	return fmt.Sprintf("Context limit: this session holds %s tokens (limit %s). End your turn NOW: call no more tools, "+
		"and answer with one short line saying what is done and what comes next. The client then compacts this session "+
		"and tells you to resume. A further tool call stops the turn.", seatlog.Kilo(used), seatlog.Kilo(seatlog.CompactLimit))
}

// arrange is THE ONE FUNCTION behind every trigger: the hook, compact_me and every other MCP tool. It reads the seat's
// context with the supervisor's reader and answers what the model must be told, "" meaning nothing. force is compact_me,
// which a seat may call under the limit. A claim someone already holds is honoured, whoever holds it.
func (t *compactTrigger) arrange(path string, force bool) string {
	dir, now := t.seatDir(), t.Now()
	// A claim is answered before the size is compared: compact_me arranges under the limit too, and its next model
	// call must still be held to it.
	if held, ok := readClaim(dir); ok && held.fresh(now) {
		if held.By == claimByHook && held.SentAt == 0 {
			return compactEndTurnText(held.Used)
		}
		return ""
	}
	used, has := lastContext(path)
	if !force && (!has || used < seatlog.CompactLimit) {
		// Under the limit this is the whole cost: one missing file, one tail read, nothing written.
		return ""
	}
	tell := ""
	err := withClaimLock(dir, func() error {
		held, ok := readClaim(dir)
		if ok && held.fresh(now) {
			return nil // taken since the read above: the holder acts, this call says nothing
		}
		tell = compactEndTurnText(used)
		return writeClaim(dir, compactClaim{By: claimByHook, At: now, Used: used, Count: compactions(path), Path: path})
	})
	if err != nil {
		return ""
	}
	return tell
}

// agyHookPayload is the part of agy's hook document this reads (hooks.md, camelCase protojson).
type agyHookPayload struct {
	ConversationID    string `json:"conversationId"`
	TranscriptPath    string `json:"transcriptPath"`
	TerminationReason string `json:"terminationReason"`
	FullyIdle         *bool  `json:"fullyIdle"`
}

// preInvocation answers agy's PreInvocation: nothing under the limit, the instruction the first time past it, and a
// terminate from then on.
func (t *compactTrigger) preInvocation(payload agyHookPayload) string {
	t.markHook(payload.ConversationID)
	if t.arrange(payload.TranscriptPath, false) == "" {
		return "{}"
	}
	return t.answerArranged()
}

// answerArranged counts the answers given under the claim and renders the next one.
func (t *compactTrigger) answerArranged() string {
	dir, answer := t.seatDir(), "{}"
	_ = withClaimLock(dir, func() error {
		held, ok := readClaim(dir)
		if !ok || held.By != claimByHook || held.SentAt != 0 {
			return nil
		}
		first := held.Told == 0
		held.Told++
		if err := writeClaim(dir, held); err != nil {
			return err
		}
		if first {
			raw, err := json.Marshal(map[string]any{
				"injectSteps": []any{map[string]any{"ephemeralMessage": compactEndTurnText(held.Used)}},
			})
			if err != nil {
				return err
			}
			answer = string(raw)
			return nil
		}
		answer = `{"terminationBehavior":"terminate"}`
		return nil
	})
	return answer
}

// stop handles agy's Stop: the seat is at its prompt, so an arranged compact goes out now. A seat past the limit whose
// last turn called no tool arranges it here. Its answer is always {}, which lets the seat stop.
func (t *compactTrigger) stop(payload agyHookPayload) string {
	t.markHook(payload.ConversationID)
	// A broken conversation fails every input, /compact among them; the supervisor's recovery owns that seat. A loop
	// with background work still running can come back, so the next Stop is the moment.
	if payload.TerminationReason == "ERROR" || (payload.FullyIdle != nil && !*payload.FullyIdle) {
		return "{}"
	}
	t.arrange(payload.TranscriptPath, false)
	dir, send, now := t.seatDir(), false, t.Now()
	_ = withClaimLock(dir, func() error {
		held, ok := readClaim(dir)
		if !ok || held.By != claimByHook || held.SentAt != 0 || !held.fresh(now) {
			return nil
		}
		held.SentAt = now
		send = true
		return writeClaim(dir, held)
	})
	if send {
		if err := t.Spawn(t.Root, t.Room, t.Seat); err != nil {
			appendTriggerLog(dir, "cannot start the sender: "+err.Error())
		}
	}
	return "{}"
}

// markHook records that this conversation runs the hook, once per conversation.
func (t *compactTrigger) markHook(conversation string) {
	if conversation == "" {
		return
	}
	path := filepath.Join(t.seatDir(), compactHookMarkFile)
	if raw, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(raw)) == conversation {
		return
	}
	_ = os.WriteFile(path, []byte(conversation), 0o600)
}

func appendTriggerLog(seatDir, text string) {
	file, err := os.OpenFile(filepath.Join(seatDir, compactTriggerLog), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	fmt.Fprintf(file, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text)
}

// spawnCompactSender starts `compact-hook send` detached: a hook blocks the agent loop, so it cannot wait in the hook.
func spawnCompactSender(root, room, seat string) error {
	logPath := filepath.Join(clientruntime.SeatDir(root, room, seat), compactTriggerLog)
	child, err := spawnChild([]string{"compact-hook", "send", "--room", room, "--seat", seat, "--state-root", root}, logPath)
	if err != nil {
		return err
	}
	go func() { _ = child.Wait() }()
	return nil
}

// compactSender is the detached half: it types /compact once, witnesses it, and sends the resume. Its seams let the
// suite run it without a pane or a wait.
type compactSender struct {
	Root, Room, Seat string
	Sleep            func(time.Duration)
	Now              func() float64
	Say              func(room, seat, text string, raw bool) error
	Nodes            func(room string) ([]map[string]any, bool, error)
	Getenv           func(string) string
}

func (s *compactSender) run() {
	dir := clientruntime.SeatDir(s.Root, s.Room, s.Seat)
	s.Sleep(compactSettle)
	claim, ok := readClaim(dir)
	if !ok || claim.By != claimByHook || claim.SentAt == 0 {
		appendTriggerLog(dir, "no arranged compact to send")
		return
	}
	if err := s.Say(s.Room, s.Seat, "/compact", true); err != nil {
		appendTriggerLog(dir, "/compact not delivered: "+err.Error())
		return
	}
	appendTriggerLog(dir, fmt.Sprintf("%s: sent /compact at %s", s.Seat, seatlog.Kilo(claim.Used)))
	start := s.Now()
	for compactions(claim.Path) <= claim.Count {
		if s.Now()-start > witnessWindow {
			appendTriggerLog(dir, fmt.Sprintf("%s: /compact NOT witnessed in %.0fs", s.Seat, witnessWindow))
			return
		}
		s.Sleep(compactPoll)
	}
	after, _ := lastContext(claim.Path)
	appendTriggerLog(dir, fmt.Sprintf("%s: /compact WITNESSED in %.0fs (%s -> %s)", s.Seat, s.Now()-start,
		seatlog.Kilo(claim.Used), seatlog.Kilo(after)))
	// The compact is done, so the claim goes with it: lastContext reads no size until the next model call writes one,
	// which is what stops the resume's first hook from reading the old size and arranging a second compact.
	_ = withClaimLock(dir, func() error { return os.Remove(filepath.Join(dir, compactClaimFile)) })
	s.resume(dir)
}

// resume passes the off-peak gate every automatic wake passes (supervisor.go). A refusal defers the resume, a failed
// read is not an empty room, and no records means no seat to bill: the three answers the supervisor gives.
func (s *compactSender) resume(dir string) {
	start := s.Now()
	for {
		nodes, ok, err := s.Nodes(s.Room)
		reason := ""
		switch {
		case err != nil:
			reason = "the room's node records could not be read: " + err.Error()
		case ok:
			proceed, message, checkErr := clientoffpeak.Check(false, nodes, s.Getenv)
			if checkErr != nil {
				reason = "off-peak gate: " + checkErr.Error()
			} else if !proceed {
				reason = message
			}
		}
		if reason == "" {
			if err := s.Say(s.Room, s.Seat, resumeText, false); err != nil {
				appendTriggerLog(dir, "resume not delivered: "+err.Error())
				return
			}
			appendTriggerLog(dir, s.Seat+": told to resume")
			return
		}
		if s.Now()-start > resumeGateLimit.Seconds() {
			appendTriggerLog(dir, s.Seat+": resume dropped after 12h held: "+reason)
			return
		}
		appendTriggerLog(dir, s.Seat+": resume held, "+reason)
		s.Sleep(resumeGatePoll)
	}
}

type compactHookCommand struct{}

func (compactHookCommand) Name() string { return "compact-hook" }
func (compactHookCommand) Summary() string {
	return "agy's hook into the client: PreInvocation and Stop arrange and send a seat's /compact"
}

const compactHookUsage = "usage: 4genteam compact-hook pre-invocation|stop | compact-hook send --room ROOM --seat SEAT [--state-root DIR]"

func (compactHookCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) || len(args) == 0 {
		printHelp(stdout, compactHookUsage,
			"What agy's global hooks.json runs in a seat, on the payload agy writes to stdin: pre-invocation before every\n"+
				"model call (past COMPACT_LIMIT_TOKENS it tells the model to end the turn, then stops the turn), and stop when\n"+
				"the loop ends (it starts the detached sender of /compact). Both always answer agy a JSON object and exit 0, so\n"+
				"a fault here never costs the seat its turn. `send` is the detached sender. See docs/WATCH.md.")
		return clientcmd.ExitOK
	}
	if args[0] == "send" {
		return runCompactSend(args[1:], stderr)
	}
	fmt.Fprintln(stdout, compactHookAnswer(args[0], os.Stdin, stderr))
	return clientcmd.ExitOK
}

// compactHookAnswer never fails: agy reads a bad answer from a hook as a fault of the turn.
func compactHookAnswer(phase string, stdin io.Reader, stderr io.Writer) string {
	room, seat := os.Getenv("AGENTHUB_ROOM"), os.Getenv("AGENTHUB_SEAT")
	if room == "" || seat == "" {
		return "{}"
	}
	root, err := clientenv.ResolveStateRoot("")
	if err == nil {
		err = seatlog.ApplyLimits()
	}
	if err != nil {
		fmt.Fprintf(stderr, "4genteam compact-hook: %v\n", err)
		return "{}"
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "{}"
	}
	var payload agyHookPayload
	if json.Unmarshal(raw, &payload) != nil {
		return "{}"
	}
	trigger := &compactTrigger{Root: root, Room: room, Seat: seat, Now: wallClock, Spawn: spawnCompactSender}
	if payload.TranscriptPath == "" {
		payload.TranscriptPath, _ = seatlog.SeatLog(root, room, seat)
	}
	switch phase {
	case "pre-invocation":
		return trigger.preInvocation(payload)
	case "stop":
		return trigger.stop(payload)
	}
	return "{}"
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func runCompactSend(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("compact-hook send", flag.ContinueOnError)
	flags.SetOutput(stderr)
	room := flags.String("room", "", "the room")
	seat := flags.String("seat", "", "the seat")
	rootFlag := flags.String("state-root", "", "the state root")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(*rootFlag)
	if err != nil || *room == "" || *seat == "" {
		fmt.Fprintln(stderr, compactHookUsage)
		return clientcmd.ExitUsage
	}
	// This process speaks for the client, as the supervisor does, not for the seat whose environment it inherited.
	_ = os.Unsetenv("AGENTHUB_SESSION_NAME")
	sender := &compactSender{Root: root, Room: *room, Seat: *seat, Sleep: time.Sleep, Now: wallClock, Say: roomSay,
		Nodes:  func(room string) ([]map[string]any, bool, error) { return roomNodes("", room) },
		Getenv: os.Getenv}
	sender.run()
	return clientcmd.ExitOK
}
