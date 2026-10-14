package clientruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// THE ONE ACTIVITY READ. Both idle readers - clientseat's QuietWaiter (through seatActivity) and clientlifecycle's
// rigIdleReason - ask this function and nothing else, so the question "is this seat at its prompt?" has ONE
// implementation and ONE place to re-point.
//
// WHERE THE WORD COMES FROM, and why it is routed here rather than exec'd. The authority is the agent's own activity
// report, which the OpenRig daemon already stores and serves: GET /api/ps names the rigs and their ids, and
// GET /api/rigs/<rigId>/nodes carries the node table, each row with `agentActivity.state`. The readers used to run
// `rig ps --nodes --json` through exec.LookPath("rig"), and on this box `rig` on PATH is the compat shim to
// `4genteam`, whose own `ps` carries `activity` (a log mtime) and NO `agentActivity` at all - so the word never
// arrived and the quiet gate could never open. Asking the daemon through the client's own transport removes the
// program-name question entirely: nothing here resolves a binary.
//
// THE CUTOVER RE-POINT: the runtime will grow its own producer for this word (its own unit), and then the BODY of
// this function is re-pointed at it - the callers do not move. That is what makes this seam worth having: the
// declaration comments on the two readers name this function, and the re-point deletes them rather than annotating
// them.
//
// SeatActivity is one seat's reading, in the three shapes the callers must tell apart.
//
//   - Read && Word != "": the report answered with a word. Whether it releases a destroyer is the predicate's
//     decision (ActivityReleasesTheWait), never this read's.
//   - Read && Word == "" && !Present: the report answered and this seat has NO activity field at all. That is an
//     explicit diagnostic rather than an empty word, because a missing producer and a seat that said nothing are
//     different facts and only one of them is a bug in the daemon.
//   - !Read: nothing could be asked, and Why names it. AN UNREADABLE ACTIVITY IS NOT THE ANSWER "not running" - it is
//     the absence of an answer, and both callers keep waiting on it rather than reading it as permission to destroy.
type SeatActivity struct {
	Word    string
	Present bool
	Read    bool
	Why     string
}

// openRigRigRow is one row of GET /api/ps: the rigs, each with the id the node route is keyed on. The route does NOT
// accept the rig's name - measured, it answers `Rig "<name>" not found` - so the id is resolved here rather than
// guessed at the node read.
type openRigRigRow struct {
	RigID   string `json:"rigId"`
	Name    string `json:"name"`
	RigName string `json:"rigName"`
}

// openRigNodeRow is one row of GET /api/rigs/<rigId>/nodes. Only the keys this read needs are named: the node's two
// addresses (the logical id the readers already key on, and the canonical session name a record carries) and the
// activity report. Everything else on a node row is the daemon's business.
type openRigNodeRow struct {
	LogicalID            string `json:"logicalId"`
	CanonicalSessionName string `json:"canonicalSessionName"`
	AgentActivity        *struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"agentActivity"`
}

// ReadSeatActivity answers one seat's activity word, over the daemon the client already talks to.
//
// The seat is addressed by the ONE record-derived translation (SeatSessionName), not by a name composed at a call
// site: the node table's canonical session name is exactly that string, and the logical id is the same pair the
// readers were already comparing against. A rig whose rows carry neither refuses rather than answering for the wrong
// seat - a read that cannot say WHICH seat it read is not a reading.
func ReadSeatActivity(ctx context.Context, rig, seat string) SeatActivity {
	if !openRigDaemonConfigured() {
		return SeatActivity{Read: false, Why: "the activity read could not run: this shell names no OpenRig daemon " +
			"(set " + openRigURLEnv + " or " + openRigPortEnv + ")"}
	}

	rigID, why := openRigRigID(ctx, rig)
	if why != "" {
		return SeatActivity{Read: false, Why: why}
	}

	var nodes []openRigNodeRow
	if why := openRigGetJSON(ctx, "/api/rigs/"+rigID+"/nodes", &nodes); why != "" {
		return SeatActivity{Read: false, Why: why}
	}

	canonical := SeatSessionName(rig, seat)
	for _, node := range nodes {
		if node.LogicalID != rig+"."+seat && node.CanonicalSessionName != canonical {
			continue
		}
		if node.AgentActivity == nil {
			// ASKED, AND NO FIELD: this is the shape that made the shim-shaped read look like a live gate - a matched
			// node carrying no activity at all. It is reported as its own answer so the supervisor can say so.
			return SeatActivity{Read: true, Present: false}
		}
		return SeatActivity{Word: node.AgentActivity.State, Present: true, Read: true}
	}
	return SeatActivity{Read: false, Why: "the activity read answered no record for " + rig + "/" + seat}
}

// openRigRigID resolves a rig's name to the id the node route is keyed on, over GET /api/ps.
func openRigRigID(ctx context.Context, rig string) (string, string) {
	var rows []openRigRigRow
	if why := openRigGetJSON(ctx, "/api/ps", &rows); why != "" {
		return "", why
	}
	for _, row := range rows {
		if row.Name == rig || row.RigName == rig {
			if row.RigID == "" {
				return "", "the activity read found " + rig + " in the rig list with no id, so its node table cannot be asked"
			}
			return row.RigID, ""
		}
	}
	return "", "the activity read answered no rig named " + rig
}

// openRigGetJSON performs one daemon read and decodes it, returning a SENTENCE rather than an error: every caller of
// this read has to say why nothing could be read, and an error carried up two layers would be re-worded twice.
func openRigGetJSON(ctx context.Context, path string, out any) string {
	raw, status, err := openRigCall(ctx, "GET", path, nil)
	if err != nil {
		return "the activity read could not ask " + path + ": " + err.Error()
	}
	if status != 200 {
		return fmt.Sprintf("the activity read asked %s and got HTTP %d: %s", path, status, firstLine(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Sprintf("the activity read asked %s and the answer is not the document it expects: %v", path, err)
	}
	return ""
}

// firstLine keeps a daemon sentence on one line: a diagnostic that wraps into three lines is read as three facts.
func firstLine(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if at := strings.IndexByte(text, '\n'); at >= 0 {
		text = text[:at]
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}
