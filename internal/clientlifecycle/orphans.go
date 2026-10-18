// orphan_handoffs: 7.11's doctor row - every pending qitem addressed to a seat that is not running.
//
// THE ROW IS THE REPORT'S ONE ROW ABOUT WORK RATHER THAN ABOUT A CHILD. A handoff can outlive its
// destination: a qitem addressed to a seat the room no longer runs waits for a reader that will never
// come, and no other row in the report would say so. The predicate is 7.11's own - a destination that is
// not in the live set is an orphan, and the live set is the room's node records with
// `sessionStatus: running`, read from the local runtime.
//
// WHAT IS NOT AN ORPHAN. A destination that is not a seat session - `human@<rig>`, the form the bridge
// addresses the owner with - is not a handoff to a seat at all, and such rows are the COMMON case rather
// than the exception: on 2026-10-11, 14 of 4genthub-min's 30 pending qitems were `human@`. So a
// destination is checked only when it names a seat session (`<pod>-<member>@<rig>`).
package clientlifecycle

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// orphanHandoff is one orphan as the report prints it: the qitem, and the session it was addressed to.
type orphanHandoff struct {
	Qitem       string
	Destination string
}

// orphanRow is 7.11's row for ONE rig: the report row, the orphans it names, and the reason the row could
// not be read when that is what happened. The names are held here rather than in the row because a Health
// row's field set is 7.2's exactly and has no place for them - the row line says the state, the lines after
// it say which qitems.
type orphanRow struct {
	Row     clientservices.Health
	Orphans []orphanHandoff
	Err     error
}

// queuePending and roomLive are the two readings this row is computed from, each a seam: a test answers
// from memory instead of starting a daemon. `doctor` stays read-only in the sense 7.5 gives it - it reads
// the room's rail and its node records, and it starts, restarts and signals nothing.
var (
	queuePending = func(room string) ([]clientruntime.QueueRow, error) {
		limit := int64(1000)
		return clientruntime.NewClient("").QueueList(room, clientruntime.QueueListFilter{
			State: []string{"pending"},
			Limit: &limit,
		})
	}
	roomLive = func(room string) (map[string]bool, error) {
		nodes, _, err := clientruntime.RoomNodes(room)
		if err != nil {
			return nil, err
		}
		live := make(map[string]bool, len(nodes))
		for _, node := range nodes {
			name, _ := node["canonicalSessionName"].(string)
			status, _ := node["sessionStatus"].(string)
			if name != "" && status == clientservices.Running {
				live[name] = true
			}
		}
		return live, nil
	}
)

// orphanRowsFor is the orphan computation as a seam, beside rowsFor: doctor's wiring is driven without a
// daemon, and the reader's own filtering is driven by replacing queuePending and roomLive.
var orphanRowsFor = orphanRows

// orphanRows computes the row for every rig the report covers.
func orphanRows(rig string) []orphanRow {
	// The same list the safeguard rows cover: agent.json's rigs, or the named rig alone. A failed read
	// leaves it empty exactly as a machine whose agent never ran does, and the row below still prints.
	state, _ := clientservices.ReadAgentState()
	rigs := reportedRigs(state.Rigs, rig)
	if len(rigs) == 0 {
		// A machine whose agent has never run lists no rooms, and still gets this row: a queue read needs a
		// scope rather than a room, and the row's absence would then be indistinguishable from a clean
		// machine. It is reported for no rig at all, like the bridge and agent rows.
		rigs = []string{""}
	}
	rows := make([]orphanRow, 0, len(rigs))
	for _, name := range rigs {
		row := orphanRow{Row: clientservices.Health{
			Rig:       name,
			Safeguard: clientservices.OrphanHandoffs,
			State:     clientservices.Running,
			AgeS:      clientservices.UnknownAge,
		}}
		if name == "" {
			// No room is meant to run, so there is no rail to read: the row is still printed, like the two
			// machine-scoped rows, rather than dropped as a machine with nothing to say.
			rows = append(rows, row)
			continue
		}
		found, err := orphanHandoffs(name)
		switch {
		case err != nil:
			// An unreadable listing is NOT a clean report: 7.7's rule for a missing signal applies to a
			// missing reading too, so the row says silent and carries the reason out.
			row.Row.State, row.Err = clientservices.Silent, err
		case len(found) > 0:
			row.Row.State, row.Orphans = clientservices.Stopped, found
		}
		rows = append(rows, row)
	}
	return rows
}

// orphanLine is one orphan as the operator reads it: under the row that reports it.
func orphanLine(orphan orphanHandoff) string {
	return fmt.Sprintf("  %s -> %s: not live", orphan.Qitem, orphan.Destination)
}

// orphanHandoffs lists the room's pending qitems whose destination is a seat session that is not running,
// sorted by qitem so the report is stable from one run to the next.
//
// BOTH READS ARE ERRORS WHEN THEY FAIL, and neither is ever read as an empty result: a handoff list built
// from a broken reading would report a clean machine, which is the failure mode this row exists to remove.
// The rail and the node records are the runtime's own (QueueList, RoomNodes), so a room's row is read from
// the one writer that owns it; a handoff addressed into another room appears in THAT room's row.
func orphanHandoffs(rig string) ([]orphanHandoff, error) {
	items, err := queuePending(rig)
	if err != nil {
		return nil, fmt.Errorf("room %s queue: %w", rig, err)
	}
	live, err := roomLive(rig)
	if err != nil {
		return nil, fmt.Errorf("room %s seats: %w", rig, err)
	}
	orphans := []orphanHandoff{}
	for _, item := range items {
		if !seatSession(item.DestinationSession) || live[item.DestinationSession] {
			continue
		}
		if rigOfSession(item.DestinationSession) != rig {
			continue
		}
		orphans = append(orphans, orphanHandoff{Qitem: item.QitemID, Destination: item.DestinationSession})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Qitem < orphans[j].Qitem })
	return orphans, nil
}

// seatSession reports whether a queue destination names a seat session (`<pod>-<member>@<rig>`) rather than
// some other reader. `human@<rig>` is the bridge's owner form and is deliberately not one.
func seatSession(destination string) bool {
	local, rig, found := strings.Cut(destination, "@")
	return found && local != "" && rig != "" && local != "human"
}

// rigOfSession is the room a session name belongs to: the part after the '@', which is the form `rig send`
// addresses a seat with.
func rigOfSession(session string) string {
	_, rig, _ := strings.Cut(session, "@")
	return rig
}
