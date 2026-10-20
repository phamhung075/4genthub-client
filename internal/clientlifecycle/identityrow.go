// The `identity` row of `doctor`: the sender identity of each node of a room, and the nodes the daemon
// cannot name at all.
//
// The architect's ruling `e2948011` (the owner node) is why the row exists: a node whose identity is UNSET
// keeps sending as `<unknown sender>` forever, and the owner's own message to the lead arrived exactly that
// way. A seat that is named is attributed; a node that is not is a fact the report must state, because no
// reader of the messages can tell the two apart from the text.
//
// WHAT THIS ROW REPORTS IS ATTRIBUTION, NEVER AUTHENTICATION. A sender's identity is self-declared - the
// daemon stamps the node a request came from, and a plain terminal sends whatever its own environment says -
// so this row says what a message CLAIMS and cannot say who sent it. The distinction is in the row's own
// lines so a reader cannot take it for proof: the same reason 4.6 refuses to treat a name as a proof of
// authorship.
//
// A NODE IS ATTRIBUTED WHEN THE DAEMON CAN NAME IT: `canonicalSessionName` is the name a message is stamped
// with, and a `logicalId` without one still resolves to a node. Only a node with NEITHER is unset, which is
// what the ruling measured. The node list is the same reading the peak gate and the restore make (roomNodes),
// so the three cannot disagree about which nodes exist.
//
// AN INPUT THAT COULD NOT BE READ IS A NOTE, never a failure, like every other check row.
package clientlifecycle

import (
	"fmt"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// The node record's fields this row reads.
const (
	nodeSessionField = "canonicalSessionName"
	nodeLogicalField = "logicalId"
	nodeRuntimeField = "runtime"
)

// identityFinding is ONE node's line: the node, the code, and the detail naming what was read.
type identityFinding struct {
	Node   string
	Code   string
	Detail string
}

// line keeps the report's own shape - `<thing> <name>: <code> <detail>` - so a reader of any row reads the
// same way.
func (f identityFinding) line() string {
	return fmt.Sprintf("node %s: %s %s", f.Node, f.Code, f.Detail)
}

// identityRow is the row plus what produced it.
type identityRow struct {
	Row      clientservices.Health
	Failures []identityFinding
	// Sent are the identities the daemon CAN name, one per node, in the row's own reading order: the detail
	// a reader needs to see that a room's senders are attributed.
	Sent []string
	// Note names an input this client could not read. Never a failure.
	Note string
}

// identityReasonLines are the lines a refusal must carry: the nodes whose messages cannot be attributed.
func identityReasonLines(row identityRow) []string {
	lines := make([]string, 0, len(row.Failures))
	for _, failure := range row.Failures {
		lines = append(lines, failure.line())
	}
	return lines
}

// identityDetailLines are what the row reports when it has nothing to refuse: the identities the daemon can
// name, and the standing fact that an identity is a CLAIM. The second line is never omitted, because a row
// read as proof of authorship is worse than no row at all.
func identityDetailLines(row identityRow) []string {
	lines := make([]string, 0, len(row.Sent)+1)
	for _, sent := range row.Sent {
		lines = append(lines, clientservices.Identity+": "+sent)
	}
	lines = append(lines, clientservices.Identity+": a sender's identity is self-declared and stamped by the "+
		"daemon, so this row reports what a message claims, never who sent it")
	return lines
}

// identityLines is one identity row as the report prints it: the reasons first, then the details, then the
// note about an input that could not be read. The human report prints these; `--json` carries the same lines
// as the row's reasons, from this ONE function.
func identityLines(row identityRow) []string {
	lines := identityReasonLines(row)
	lines = append(lines, identityDetailLines(row)...)
	if row.Note != "" {
		lines = append(lines, row.Note)
	}
	return lines
}

// identityRowFor is the identity-row computation as a seam, beside orphanRowsFor: a case drives attribution,
// an unset node and an unreadable node list without a daemon.
var identityRowFor = identityRows

// identityRows computes the identity row of every room the report covers. The room list is the one the
// safeguard rows use, so the two surfaces cannot disagree about which rooms exist.
func identityRows(room string) []identityRow {
	state, _ := clientservices.ReadAgentState()
	rooms := reportedRooms(state.Rooms, room)
	rows := make([]identityRow, 0, len(rooms))
	for _, name := range rooms {
		rows = append(rows, identityRowOf(name))
	}
	return rows
}

// identityRowOf is one room's row. The node list is read once and every node is judged against it, so the
// row's failures and its list of attributed senders can never disagree about which nodes exist.
func identityRowOf(name string) identityRow {
	row := identityRow{Row: clientservices.Health{
		Room:      name,
		Safeguard: clientservices.Identity,
		State:     clientservices.Running,
		AgeS:      clientservices.UnknownAge,
	}}
	nodes := identityNodes(name)
	if len(nodes) == 0 {
		row.Note = clientservices.Identity + ": not observed: the room's node list could not be read, or the " +
			"room has no nodes"
		return row
	}
	for _, node := range nodes {
		session := nodeField(node, nodeSessionField)
		logical := nodeField(node, nodeLogicalField)
		if session == "" && logical == "" {
			row.Failures = append(row.Failures, identityFinding{
				Node:   nodeName(node),
				Code:   clientservices.IdentityUnset,
				Detail: "the node record names no sender identity, so its messages arrive as <unknown sender>",
			})
			continue
		}
		row.Sent = append(row.Sent, identitySentence(node, session, logical))
	}
	if len(row.Failures) > 0 {
		row.Row.State = clientservices.IdentityUnset
	}
	return row
}

// identitySentence is one attributed node as a line's tail: the name it sends under, and the runtime whose
// messages carry it. The runtime is what tells a reader whether the name was stamped by the daemon or
// declared by the node's own environment.
func identitySentence(node map[string]any, session, logical string) string {
	name := session
	if name == "" {
		name = logical
	}
	runtime := nodeField(node, nodeRuntimeField)
	if runtime == "" {
		runtime = "unknown runtime"
	}
	return name + " sends as that identity (runtime " + runtime + ")"
}

// nodeName is the node as a failure names it: the session when there is one, else the logical id, else the
// fact that the record carried neither.
func nodeName(node map[string]any) string {
	if session := nodeField(node, nodeSessionField); session != "" {
		return session
	}
	if logical := nodeField(node, nodeLogicalField); logical != "" {
		return logical
	}
	if room := strings.TrimSpace(nodeField(node, "roomName")); room != "" {
		return "(unnamed node of " + room + ")"
	}
	return "(unnamed node)"
}

// nodeField reads one string field of a node record. A missing key and a non-string value are both the empty
// string: the row reports what the record says, and a record this client cannot read the field from is
// exactly a node whose identity it cannot name.
func nodeField(node map[string]any, key string) string {
	value, _ := node[key].(string)
	return value
}

// identityNodes is the room's node list, through the ONE read the peak gate and the restore make. A read that
// FAILED is the empty list here, because this row reports rather than refuses: the note says the row could not
// observe the room, and the failure itself is named by the verbs that act.
var identityNodes = func(name string) []map[string]any {
	nodes, _, err := roomNodes(name)
	if err != nil {
		return nil
	}
	return nodes
}
