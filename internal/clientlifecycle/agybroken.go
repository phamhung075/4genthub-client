// An agy seat whose conversation broke on the executor error is restarted in a fresh conversation and
// handed its last instruction again.
//
// MEASURED (room aa2-rs-peak, 2026-10-29, agy log cli-20261029_081024.log, conversation d14d9779): the turn
// ended at 07:12:09Z; at 07:12:26Z a background /tasks job finished and agy appended its SYSTEM_MESSAGE, and
// the run it started died at once with
//
//	agent executor error: generating and executing: serializing trajectory: maxMessageTokens must be positive, got 0
//
// Every later input (the idle guard's nudges at 07:14:35 and 07:16:37, the director's at 07:17:20) failed the
// same way within half a second, until `4genteam seat reseat --force` started a new conversation. It is NOT
// the context limit: the conversation was 41 records, 43 KB, its last model call 2.3k input tokens (16k at its
// largest), against agy's ~270k compaction point. The same error is in two older agy logs (2026-10-26 and
// 2026-10-28), both on the first message to a conversation that had just been loaded. A /compact typed into
// such a pane is one more input into the same broken conversation, so the recovery is a fresh conversation.
//
// The pane is the only place the error shows: agy writes no transcript record for the failed run, so the
// transcript's newest record is the unanswered input and the seat reads as mid-turn - neither idle (so never
// compacted) nor progressing.
package clientlifecycle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// agyBrokenSignature is the error that leaves an agy conversation unusable. Only this one is acted on: a
// different executor error (a quota refusal, a network failure) is not cured by a fresh conversation, and a
// restart would only lose the seat's context.
const agyBrokenSignature = "maxMessageTokens must be positive"

// agyBrokenTailLines is how much of the bottom of the pane the error is looked for in: the error sits right
// above the input box when the seat is stuck on it, and scrolls up out of this window once the seat works again.
const agyBrokenTailLines = 20

// agyBrokenConfirm is how long the error must stay at the bottom of the pane before the seat is restarted:
// one pass sees it, a pass this much later acts on it.
const agyBrokenConfirm = 30.0

// agyRecoverCooldown is the least time between two restarts of the same seat. A seat that breaks again
// inside it is left for the lead, with one log line.
const agyRecoverCooldown = 1800.0

// agyBriefLimit caps the instruction carried into the fresh conversation.
const agyBriefLimit = 4000

// agyStartGrace is how long a restarted agy seat is given before its brief is typed: agy takes up to ~6s to
// log in and resolve its model (agy log 2026-10-29 06:52: "CLI startup completed (took 5.68s)"), and a text
// typed before that is queued rather than run.
var agyStartGrace = 15 * time.Second

// PaneShowsAgyBroken says whether the bottom of an agy pane shows the conversation-breaking executor error.
func PaneShowsAgyBroken(pane string) bool {
	lines := strings.Split(pane, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < agyBrokenTailLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if strings.Contains(line, agyBrokenSignature) {
			return true
		}
	}
	return false
}

// agyLastInstruction is the newest USER_INPUT of an agy transcript that did not come from this client's own
// supervisors (their messages start "From owner session: "), with agy's wrapper tags removed, or "".
func agyLastInstruction(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	last := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		var record struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Type != "USER_INPUT" {
			continue
		}
		text := record.Content
		if start := strings.Index(text, "<USER_REQUEST>"); start >= 0 {
			text = text[start+len("<USER_REQUEST>"):]
			if end := strings.Index(text, "</USER_REQUEST>"); end >= 0 {
				text = text[:end]
			}
		}
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "From "+saySender+": ") {
			continue
		}
		last = text
	}
	if len(last) > agyBriefLimit {
		last = last[:agyBriefLimit] + "\n[... cut]"
	}
	return last
}

// agyRecoveryText is what the fresh conversation is told.
func agyRecoveryText(instruction string) string {
	text := "Your previous agy conversation stopped on an executor error (\"" + agyBrokenSignature + ", got 0\") " +
		"and every message after it failed the same way, so the compaction supervisor restarted this seat in a " +
		"fresh conversation. Re-read your role file and the board item you held, check the working tree and " +
		"/tasks for what is already done, then carry on without waiting for me."
	if instruction != "" {
		text += "\nThe last instruction you were given:\n---\n" + instruction + "\n---"
	}
	return text
}

// recoverBrokenAgy handles an agy seat stuck on the executor error, and says whether it did anything this
// pass (the rest of the pass is skipped for the seat then: its transcript belongs to the broken conversation).
func (s *Supervisor) recoverBrokenAgy(seat, path string, state *seatState, now float64) bool {
	if s.Pane == nil || s.Recover == nil {
		return false
	}
	pane, ok := s.Pane(s.Room, seat)
	if !ok || !PaneShowsAgyBroken(pane) {
		state.brokenSeen, state.brokenLogged = 0, false
		return false
	}
	if state.brokenSeen == 0 {
		state.brokenSeen = now
		s.Log(seat + ": agy executor error at the bottom of the pane (" + agyBrokenSignature + "); restarting the seat if it is still there in " + fmt.Sprintf("%.0fs", agyBrokenConfirm))
		return true
	}
	if now-state.brokenSeen < agyBrokenConfirm {
		return true
	}
	if state.recovered > 0 && now-state.recovered < agyRecoverCooldown {
		if !state.brokenLogged {
			state.brokenLogged = true
			s.Log(fmt.Sprintf("%s: agy executor error again %.0fs after the last restart; left for the lead (no second restart inside %.0fs)",
				seat, now-state.recovered, agyRecoverCooldown))
		}
		return true
	}
	instruction := agyLastInstruction(path)
	if err := s.Recover(s.Room, seat); err != nil {
		s.Log(fmt.Sprintf("%s: agy executor error, the restart failed: %v", seat, err))
		return true
	}
	*state = seatState{recovered: now}
	s.Log(fmt.Sprintf("%s: agy executor error, seat restarted in a fresh conversation", seat))
	if s.Brief == nil {
		return true
	}
	if err := s.Brief(s.Room, seat, agyRecoveryText(instruction)); err != nil {
		s.Log(fmt.Sprintf("%s: the brief after the restart was not delivered: %v", seat, err))
		return true
	}
	carried := "no earlier instruction found"
	if instruction != "" {
		carried = "its last instruction carried over"
	}
	s.Log(fmt.Sprintf("%s: briefed after the restart (%s)", seat, carried))
	return true
}

// agyPane reads the bottom of a seat's pane through the runtime's own terminal manager.
func agyPane(room, seat string) (string, bool) {
	answer, err := clientruntime.NewClient("").Capture(clientruntime.SeatSessionName(room, seat), 1)
	if err != nil {
		return "", false
	}
	return answer.Content, true
}

// agyRestart restarts the seat through the runtime, the same restart `seat reseat --force` makes: an agy seat
// is launched as plain `agy`, which opens a new conversation (measured: peak-review came back in conversation
// a06b2a80 after its reseat at 08:17).
func agyRestart(room, seat string) error {
	_, err := clientruntime.NewClient("").RestartSeat(room, seat, nil)
	return err
}

// agyBrief types the brief into the restarted seat once agy has had time to start, then settles it if agy
// queued it anyway.
func agyBrief(room, seat, text string) error {
	time.Sleep(agyStartGrace)
	if err := roomSay(room, seat, text, false); err != nil {
		return err
	}
	clientruntime.SubmitQueuedMessage("", clientruntime.SeatSessionName(room, seat))
	return nil
}
