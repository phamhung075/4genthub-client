package clientdecide

// AN ESCALATION TO THE OWNER WAS PRINTED AND NEVER RAISED. Measured 2026-10-30: jev_decisions.log held 52
// entries with outcome escalated-to-owner across rooms (e.g. 2026-10-28T17:39Z, lead, "landing channel for two
// gated commits", aa2-rs), and the rails held one owner-decision row in all: `decide` printed escalate_to
// "owner" and exit 5, and raising the ask was left to the caller's next command, which a seat skips, so the
// dashboard's Decision Requests panel said "No open decision requests". This verb now raises it itself, once
// per fresh escalation, and the log entry names the room so an escalation can be traced to a room.
//
// In a room with a director office the question goes to the director instead (owner, 2026-10-30): one QUESTION
// line in <room dir>/director/ready.md. Only the director raises an owner ask there.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
	"github.com/phamhung075/4genthub-client/internal/clientdirector"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientguard"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// Seams, vars so a test raises an ask with no daemon, no real state root and a fixed clock.
var (
	stateRoot = clientenv.DefaultStateRoot
	stamp     = func() string { return clientoffpeak.ShortNow(os.Getenv) }
	createAsk = func(room string, in clientruntime.QueueCreateInput) (string, error) {
		row, err := clientruntime.NewClient("").QueueCreate(room, in)
		if err != nil {
			return "", err
		}
		return row.QitemID, nil
	}
)

// route is where an escalation of this ask goes: the room, the asking session, and whether the room has a
// director office.
type route struct {
	room, session, seat, root, roomDir string
	director                           bool
}

// resolveRoute names the room (--room, else the seat session's room, else $AGENTHUB_ROOM) and reads the
// director declaration the git guard reads.
func resolveRoute(flagRoom string, getenv func(string) string) route {
	r := route{room: strings.TrimSpace(flagRoom), session: strings.TrimSpace(getenv(clientguard.CanonicalSessionEnv))}
	if session, ok := clientguard.SessionFromEnv(getenv); ok {
		r.seat = session.Seat
		if r.room == "" {
			r.room = session.Room
		}
		if r.session == "" {
			r.session = clientruntime.SeatSessionName(session.Room, session.Seat)
		}
	}
	if r.room == "" {
		r.room = strings.TrimSpace(getenv("AGENTHUB_ROOM"))
	}
	if r.room == "" {
		return r
	}
	r.root = stateRoot()
	r.roomDir = clientdirector.RoomDir(r.root, r.room, getenv)
	r.director = clientdirector.Has(r.roomDir)
	return r
}

// raise sends a fresh escalation where its room routes it. A failure is reported on stderr and leaves the
// outcome as printed: the log already holds the escalation, and exit 5 still tells the caller it is open.
func raise(r route, tier, summary, detail string, questions []clientdecisions.Question, result *outcome, stderr io.Writer) {
	if r.room == "" {
		fmt.Fprintf(stderr, "4genteam decide: escalated to the %s, but no room could be named (--room, $AGENTHUB_SESSION_NAME or $AGENTHUB_ROOM), so nothing was raised: raise it with `4genteam room queue create <room> --to human@<room> --tag owner-decision --questions-file ...`\n", result.EscalateTo)
		return
	}
	if r.director {
		asker := r.seat
		if asker == "" {
			asker = tier + " tier"
		}
		line := clientdirector.QuestionLine(stamp(), asker, summary, questions)
		path, err := clientdirector.AppendQuestion(r.roomDir, line)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam decide: room %s has a director, but the QUESTION could not be appended to %s (%v): add this line by hand:\n%s\n", r.room, path, err, line)
			return
		}
		result.Ready = path
		fmt.Fprintf(stderr, "4genteam decide: room %s has a director: the question is in %s and the director answers there (not the dashboard). Wait for the ANSWER line; work only on what does not depend on it.\n", r.room, path)
		return
	}
	source := r.session
	if source == "" {
		source = clientruntime.SeatSessionName(r.room, "lead")
	}
	priority := "normal"
	in := clientruntime.QueueCreateInput{
		SourceSession:      source,
		DestinationSession: clientdecisions.Human(r.room),
		Summary:            clip(summary, 120),
		Body:               askBody(tier, result.Reason, detail, questions),
		Priority:           &priority,
		Tags:               []string{clientdecisions.Tag},
	}
	qitem, err := createAsk(r.room, in)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam decide: escalated to the owner, but the owner ask could not be created on room %s's rail (%v): raise it with `4genteam room queue create %s --to %s --tag %s --questions-file ...`\n",
			r.room, err, r.room, clientdecisions.Human(r.room), clientdecisions.Tag)
		return
	}
	result.OwnerAsk = qitem
	if err := clientdecisions.Write(r.root, clientdecisions.Record{QitemID: qitem, Room: r.room, Questions: questions}); err != nil {
		fmt.Fprintf(stderr, "4genteam decide: owner ask %s was created, but its questions were not recorded (%v), so the dashboard's Decision Requests panel cannot offer it: add a --note to the row and tell the owner\n", qitem, err)
		return
	}
	fmt.Fprintf(stderr, "4genteam decide: owner ask %s raised on room %s (to %s, tag %s): the bridge mirrors it to the dashboard's Decision Requests panel. Do not raise it again; wait for the answer row.\n",
		qitem, r.room, clientdecisions.Human(r.room), clientdecisions.Tag)
}

// askBody is the owner ask's body in the rail's four-line shape, each line inside the daemon's caps
// (facts 280, rule 110, decision 100, next 78).
func askBody(tier, reason, detail string, questions []clientdecisions.Question) string {
	var asked, picks []string
	for _, q := range questions {
		asked = append(asked, q.Question)
		pick := q.Recommended()
		if pick == "" {
			pick = "none"
		}
		picks = append(picks, q.ID+"="+pick)
	}
	facts := "Jev at " + tier + " tier escalated (" + reason + "): " + strings.Join(asked, " / ")
	if d := strings.Join(strings.Fields(detail), " "); d != "" {
		facts += " - " + d
	}
	return strings.Join([]string{
		"facts: " + clip(oneLine(facts), 272),
		"rule: " + clip("only the owner's answer settles an escalated decision", 104),
		"decision: " + clip("answer in Decision Requests; asker's pick "+strings.Join(picks, ", "), 90),
		"next: " + clip("the asking seat waits for the answer row", 70),
	}, "\n")
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

func clip(text string, max int) string {
	runes := []rune(oneLine(text))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max-3]) + "..."
}
