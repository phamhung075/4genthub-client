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
	if why := openRigGetJSON(ctx, "activity", "/api/rigs/"+rigID+"/nodes", &nodes); why != "" {
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

// ReadRigNodes reads one rig's NODE ROWS over the daemon the client already talks to: GET /api/ps to resolve
// the rig's id, then GET /api/rigs/<rigId>/nodes - the route the activity read above uses - decoded into the
// CLI's own node shape, which is what every caller of this read already parses
// (`rig ps --nodes --json`).
//
// IT IS NARROWER THAN `rig ps --json --nodes` IN EXACTLY ONE KEY: pid. The daemon's node table does not carry
// it, and nothing that consumes this read asks for it - the peak gate reads a seat's name and model
// (clientoffpeak.SeatsFromNodes), and the restore, switch and reseat paths read logicalId,
// canonicalSessionName, sessionStatus and model. If pid ever gains a consumer, that is a NEW DECISION to grow
// this read deliberately rather than a gap to discover at a call site.
//
// The three answers are told apart by the pair, because a caller that must refuse needs to know which one it
// has:
//
//   - rows != nil: the daemon answered with this rig's node table, which may be empty (a rig with no seats).
//   - rows == nil && why == "": NO RECORDS, which is not a failure - either this shell names no OpenRig
//     daemon at all, or the daemon answered that it knows no rig by that name. A caller for which "no
//     records" means "nothing to decide" proceeds; one for which it means "I cannot see the room I was asked
//     about" refuses on its own judgement, which is why this read does not decide that for it.
//   - rows == nil && why != "": the read was ATTEMPTED and failed - nothing answered, or the answer was not
//     the table. A caller that needs the reading must refuse in these words rather than proceed as if the
//     room had no seats.
func ReadRigNodes(ctx context.Context, rig string) ([]map[string]any, string) {
	if !openRigDaemonConfigured() {
		return nil, ""
	}

	var listed []openRigRigRow
	if why := openRigGetJSON(ctx, "node", "/api/ps", &listed); why != "" {
		return nil, why
	}
	rigID := ""
	for _, row := range listed {
		if row.Name == rig || row.RigName == rig {
			rigID = row.RigID
			break
		}
	}
	if rigID == "" {
		return nil, ""
	}

	var rows []map[string]any
	if why := openRigGetJSON(ctx, "node", "/api/rigs/"+rigID+"/nodes", &rows); why != "" {
		return nil, why
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	for _, row := range rows {
		// The seat half is derived from the logical id, which is the pair "<rig>.<seat>" everywhere this
		// client reads one - the CLI's own node rows carry it as its own key, and the daemon's do not.
		if _, ok := row["seat"]; !ok {
			if logical, ok := row["logicalId"].(string); ok {
				if at := strings.LastIndex(logical, "."); at >= 0 && at < len(logical)-1 {
					row["seat"] = logical[at+1:]
				}
			}
		}
	}
	return rows, ""
}

// openRigRigID resolves a rig's name to the id the node route is keyed on, over GET /api/ps.
func openRigRigID(ctx context.Context, rig string) (string, string) {
	var rows []openRigRigRow
	if why := openRigGetJSON(ctx, "activity", "/api/ps", &rows); why != "" {
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
// this read has to say why nothing could be read, and an error carried up two layers would be re-worded twice. The
// label names WHICH read in those sentences, because two reads now share this helper - the activity read and the node
// read - and a diagnostic that blames the wrong one sends a reader to the wrong place.
func openRigGetJSON(ctx context.Context, label, path string, out any) string {
	raw, status, err := openRigCall(ctx, "GET", path, nil)
	if err != nil {
		return "the " + label + " read could not ask " + path + ": " + err.Error()
	}
	if status != 200 {
		return fmt.Sprintf("the %s read asked %s and got HTTP %d: %s", label, path, status, firstLine(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Sprintf("the %s read asked %s and the answer is not the document it expects: %v", label, path, err)
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
