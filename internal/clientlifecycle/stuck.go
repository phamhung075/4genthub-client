// The stuck and idle alerts of `agent` (plan item 5 of
// ai_docs/operations/client-features-for-the-dev-team.md). It rides the SAME agent loop the overlap detector
// rides, on its own ten-minute timer, and it reports three things:
//
//  1. A STUCK QUEUE ROW: a row of this room still `in-progress` past its OWN deadline, or one whose owning
//     seat has no live session at all. Both are the queue's own facts; neither is a number this file invented.
//  2. A STUCK BOARD TASK: a task of this room still `in-progress` past its own due date.
//  3. AN IDLE SEAT: a seat with a live session whose session log has not been written for the idle window.
//     Only omp and claude-code keep a log this client can read (`internal/seatlog`), so a seat whose log
//     cannot be read is SKIPPED AND NAMED, never called idle - the same rule the overlap detector follows.
//
// WHY A BARE "NO CHANGE FOR N MINUTES" IS NOT A STUCK ROW. It was measured against this room's live queue on
// 2026-10-11 before it was dropped: a row whose owner was actively working read four hours old, because the
// seat records progress on the row only when it has something to write. A threshold that fires on a working
// seat is the false alarm the plan's own risk 1 names, so the row's deadline and a gone owner are the two
// conditions used instead.
//
// THE ESCALATION ROUTE IS THE PLAN'S, SEAT THEN LEAD THEN OWNER, and it is the same timer with a SECOND,
// LONGER threshold: the first alert goes to the seat the finding names (when it names one) and to the lead,
// and a finding still there `stuckEscalateAfter` later is written to the room's human as a queue row. That
// destination is `human@<room>`, NOT the owner node: ruling 4.6 says a row left on the owner node's queue
// waits unseen, and `human@<room>` is the form the bridge already delivers to the cloud as a notification, so
// it is a surface that reaches the owner rather than one that only looks like one.
//
// AND IT DOES NOT REPEAT ITSELF: one finding is sent once at each rung, and a rung that has been sent is
// never sent again while the finding stands, so a room told the same thing every ten minutes stops reading
// it. The memory is a file under the state root, so a restarted agent does not start telling the room the
// same thing over, and a finding that clears is forgotten - so a row that comes back IS news again.
package clientlifecycle

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

const (
	stuckUsage = "usage: 4genteam stuck ROOM [--once|--every DURATION] [--idle-after DURATION] " +
		"[--escalate-after DURATION] [--state-root DIR] [--json] [--dry-run]"

	// stuckEvery is how often agent runs the scan: the SAME cadence as the overlap detector beside it (4.5's
	// ten minutes). The plan's 5.3c leaves the loop's cadence to one number; two timers in one loop that
	// disagreed would be a second schedule nobody could reason about.
	stuckEvery = 10 * time.Minute
	// stuckEscalateAfter is the SECOND, LONGER threshold the plan names for the escalation route: a finding
	// the seat and the lead were told about this long ago, and which is still there, is the owner's to see.
	stuckEscalateAfter = time.Hour
	// stuckIdleAfter is the K of "no log activity for K minutes". The plan names the condition and not the
	// number, exactly as it names the stale-branch condition and not 4.5's four hours. Thirty minutes is
	// longer than any single tool call this client has seen and shorter than the hour the room works in.
	stuckIdleAfter = 30 * time.Minute
	// stuckQueueLimit and stuckBoardLimit bound one read, so a room with a long queue or the board of a busy
	// user cannot make a pass unbounded.
	stuckQueueLimit = 1000
	stuckBoardLimit = 200
	// stuckBoardTimeout is the client's HTTP convention, the same 60s internal/clientevidence and
	// internal/clientsync use.
	stuckBoardTimeout = 60 * time.Second
	// stuckBoardPath is the board's list route, which the server serves as GET /api/v2/tasks/ and answers
	// `{"success":true,"tasks":[…],"count":N,"user":…}` (fastmcp/server/routes/task_user_routes.go,
	// ListUserTasks). The status filter is the route's own `task_status` query parameter, and the bound is the
	// route's own `limit`, built ONCE from stuckBoardLimit by stuckBoardTasks: the path used to carry
	// `limit=200` of its own, and appending the bound to it sent `limit=200200`.
	stuckBoardPath = "/api/v2/tasks/?task_status=in-progress&"
	// stuckOwnerPrefix addresses a room's human on the queue. It is the same literal the bridge notifies
	// (`clientbridge.HumanDestination`), kept here rather than exported from there because the two readers
	// must not be able to disagree about it.
	stuckOwnerPrefix = "human@"
	// ownerNodeSeat is the room member that runs no model: ruling 4.6 fixes the name `<room>-owner@<room>`.
	// main's client has no owner-node predicate of its own (the room layout that adds one is not in this
	// tree), so the idle scan uses this name to leave the node alone; a node keeps no session log, and the
	// owning terminal's runtime is terminal rather than a model.
	ownerNodeSeat = "owner"
	// stuckEscalationTag marks the rows this detector writes. It is `handoff` rather than a name of its own
	// because a rail create accepts only the daemon's own tag vocabulary (clientruntime.QueueTags): the row
	// hands the finding to the owner, and the summary's "Stuck escalation in <room>:" prefix is what makes it
	// legible as one of these.
	stuckEscalationTag = "handoff"
	// The three kinds and the two rungs, as constants so the report, the memory and the tests name the same
	// things.
	stuckQueueKind = "queue"
	stuckBoardKind = "board"
	stuckIdleKind  = "idle"
	stuckAlerted   = 1
	stuckEscalated = 2
	// stuckInProgress is the queue's and the board's own state name for "claimed and being worked on".
	stuckInProgress = "in-progress"
)

// ---- what one scan reads, and from where -----------------------------------

// stuckDeps is what a scan touches outside its arguments, as fields so a case drives the whole detector
// without a room, a queue, a board, a terminal or a clock.
type stuckDeps struct {
	rows     func(room string) ([]stuckRow, []string)
	board    func() ([]boardTask, []string)
	seats    func(room string) []string
	seatLog  func(room, seat string) (string, string)
	writeAt  func(path string) (time.Time, error)
	say      func(room, seat, text string)
	escalate func(room string, finding stuckFinding, open time.Duration) (string, error)
	now      func() time.Time
	state    func(room string) (string, error)
}

// stuckDepsFor is the scan's production wiring, and a seam so a case drives agent's pass without a room.
var stuckDepsFor = defaultStuckDeps

func defaultStuckDeps(stateRoot string) *stuckDeps {
	return &stuckDeps{
		rows:     stuckQueueRows,
		board:    stuckBoardTasks,
		seats:    func(room string) []string { return seatlog.RoomSeats(stateRoot, room) },
		seatLog:  func(room, seat string) (string, string) { return seatlog.SeatLog(stateRoot, room, seat) },
		writeAt:  stuckWriteAt,
		say:      func(room, seat, text string) { roomSay(room, seat, text, false) },
		escalate: stuckEscalateRow,
		now:      clientservices.Now,
		state:    func(room string) (string, error) { return filepath.Join(stateRoot, "stuck-"+room+".json"), nil },
	}
}

// stuckWriteAt is when a session log was last written - the whole of "log activity" that is needed here.
func stuckWriteAt(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// ---- what one scan reads, and what it says ---------------------------------

// stuckRow is the part of a queue row this detector reads. The body and the summary are read because the
// alert has to say WHICH row, and a reader cannot tell one `qitem-…` from another. The rail carries no
// deadline and no claim time: the daemon's row has a state, an owner and its two timestamps, so the one stuck
// fact a row can prove is an owner with no live session (see the head of this file for why an age threshold
// is not used instead).
type stuckRow struct {
	QitemID     string
	Destination string
	State       string
	Priority    string
	Summary     string
	Body        string
	Updated     string
}

// boardTask is the part of a board row this detector reads. `due_date` is the board's own deadline, and a
// task that carries none is never called late - the rule the queue rows are held to as well.
//
// Assignees are BARE seat keys: the board's list route writes them as `@<seat>`, and stuckAssignees strips
// that at the wire boundary, so the field holds what a live session is named by and nothing downstream has
// to strip anything.
type boardTask struct {
	ID        string
	Title     string
	Status    string
	Due       string
	Assignees []string
}

// stuckOptions is one run's inputs.
type stuckOptions struct {
	Room          string
	Now           time.Time
	IdleAfter     time.Duration
	EscalateAfter time.Duration
}

// stuckFinding is one stuck row or task: what it is, who owns it, and the fact that makes it stuck.
// Seat is the seat the finding NAMES, which for a row whose owner is gone is exactly the seat that cannot
// be told; Live says whether there is a session to tell, so an alert is never addressed to a dead seat.
type stuckFinding struct {
	Kind   string
	ID     string
	Seat   string
	Live   bool
	Title  string
	Reason string
	Since  time.Time
}

// idleSeat is one seat with a live session and a session log nobody has written for the idle window.
type idleSeat struct {
	Seat    string
	Runtime string
	Silent  time.Duration
	At      time.Time
}

// stuckReport is what one scan found, and what it sent.
type stuckReport struct {
	Room          string            `json:"room"`
	IdleAfter     time.Duration     `json:"idle_after"`
	EscalateAfter time.Duration     `json:"escalate_after"`
	Findings      []stuckFinding    `json:"findings"`
	Idle          []idleSeat        `json:"idle"`
	Skipped       []string          `json:"skipped,omitempty"`
	Notes         []string          `json:"not_observed,omitempty"`
	Sent          []string          `json:"sent"`
	Escalated     []string          `json:"escalated,omitempty"`
	Deadlines     map[string]string `json:"-"`
}

// stuckScanFor is the scan as a seam: agent's timer, the `stuck` verb and every case go through it.
var stuckScanFor = stuckScan

// stuckScan reads one room's queue rows, the board's tasks and every live seat's log, and answers what is
// stuck and what is idle. Every read that fails produces a NOTE rather than a state: a detector that could
// not look has no answer to give, and an empty report must not read as "nothing is stuck".
func stuckScan(deps *stuckDeps, opts stuckOptions) stuckReport {
	report := stuckReport{Room: opts.Room, IdleAfter: opts.IdleAfter, EscalateAfter: opts.EscalateAfter}
	liveSeats := deps.seats(opts.Room)
	live := map[string]bool{}
	for _, seat := range liveSeats {
		live[seat] = true
	}

	rows, rowNotes := deps.rows(opts.Room)
	report.Notes = append(report.Notes, rowNotes...)
	for _, row := range rows {
		if row.State != stuckInProgress {
			continue
		}
		seat := stuckSeat(opts.Room, row.Destination)
		// THE ONE STUCK FACT THE RAIL PROVES is an owner with no live session. A deadline is not among the
		// daemon's row fields (stuckRow says why), and an age threshold was measured false before it was
		// dropped: a row whose owner is working reads hours old.
		if seat != "" && !live[seat] {
			report.Findings = append(report.Findings, stuckFinding{
				Kind: stuckQueueKind, ID: row.QitemID, Seat: seat, Live: false,
				Title:  stuckSubject(row.Summary, row.Body),
				Reason: "the seat that owns it (" + seat + ") has no live session", Since: stuckSince(row, opts.Now),
			})
		}
	}

	// THE BOARD IS THE USER'S, NOT A ROOM'S, so a task is reported by the room whose live seats one of its
	// assignees names. A task no live seat of this room names is counted, not alerted: the row would
	// otherwise be repeated by every room agent supervises, and a repeated alert is one the reader stops
	// reading.
	tasks, boardNotes := deps.board()
	report.Notes = append(report.Notes, boardNotes...)
	unplaced := 0
	for _, task := range tasks {
		if task.Status != stuckInProgress {
			continue
		}
		due, hasDue, parsed := stuckMoment(task.Due)
		if !parsed {
			report.Notes = append(report.Notes, fmt.Sprintf("board task %s: its due date reads %q, which is not a time, so it is not called late", task.ID, task.Due))
			continue
		}
		if !hasDue || !due.Before(opts.Now) {
			continue
		}
		seat := ""
		for _, assignee := range task.Assignees {
			if live[assignee] {
				seat = assignee
				break
			}
		}
		if seat == "" {
			unplaced++
			continue
		}
		report.Findings = append(report.Findings, stuckFinding{
			Kind: stuckBoardKind, ID: task.ID, Seat: seat, Live: true, Title: task.Title,
			Reason: "its own due date passed at " + due.UTC().Format(time.RFC3339), Since: due,
		})
	}
	if unplaced > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("%d board task(s) in progress are past their due date and name no live seat of %s", unplaced, opts.Room))
	}

	// THE IDLE SCAN COVERS ONLY SEATS WITH A LIVE SESSION. A seat whose session is gone is not idle, and it
	// is not invisible either: a queue row it owns is the first finding above.
	for _, seat := range liveSeats {
		// 4.6: the owner node runs no model, so it must never be counted idle - and it keeps no session log,
		// so left here it would be skipped and NAMED every pass. main's client has no owner-node predicate of
		// its own (one arrives with the room's layout), so the name the ruling fixes is the signal this scan
		// uses; the node's own runtime is terminal and therefore has no log to read.
		if seat == ownerNodeSeat {
			continue
		}
		path, runtime := deps.seatLog(opts.Room, seat)
		if path == "" {
			report.Skipped = append(report.Skipped, fmt.Sprintf("%s (no session log this machine can read: runtime %s)", seat, stuckRuntime(runtime)))
			continue
		}
		written, err := deps.writeAt(path)
		if err != nil {
			report.Notes = append(report.Notes, fmt.Sprintf("%s: cannot read when its session log was last written: %v", seat, err))
			continue
		}
		if silent := opts.Now.Sub(written); silent >= opts.IdleAfter {
			report.Idle = append(report.Idle, idleSeat{Seat: seat, Runtime: runtime, Silent: silent, At: written})
		}
	}

	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Kind != report.Findings[j].Kind {
			return report.Findings[i].Kind < report.Findings[j].Kind
		}
		return report.Findings[i].ID < report.Findings[j].ID
	})
	sort.Slice(report.Idle, func(i, j int) bool { return report.Idle[i].Seat < report.Idle[j].Seat })
	sort.Strings(report.Skipped)
	sort.Strings(report.Notes)
	return report
}

// stuckSeat is the seat key a queue destination names, or nothing when it names no seat of this room.
// `human@<room>` is the owner's destination and deliberately answers nothing: it is a reader, not a worker.
// The rail's destinations are the runtime's own session names (`agenthub--<room>--<seat>`) and, on older
// rows, the canonical `<room>-<seat>@<room>`; both forms are read here, because a detector that only knew
// one would call every row ownerless.
func stuckSeat(room, destination string) string {
	if sessionRoom, seat, ok := clientruntime.ParseSeatSessionName(destination); ok {
		if sessionRoom != "" && sessionRoom != room {
			return ""
		}
		return seat
	}
	local, destRoom, found := strings.Cut(destination, "@")
	if !found || local == "" || destRoom == "" || local == "human" {
		return ""
	}
	if destRoom != "" && (destRoom != room || !strings.HasPrefix(local, destRoom+"-")) {
		return ""
	}
	return strings.TrimPrefix(local, destRoom+"-")
}

// stuckDestinationRoom is the room a destination belongs to, in either of the two forms stuckSeat reads.
func stuckDestinationRoom(destination string) string {
	if room, _, ok := clientruntime.ParseSeatSessionName(destination); ok {
		return room
	}
	return roomOfSession(destination)
}

// stuckMoment parses one timestamp field. It answers (time, present, parsed): a field the row does not carry
// is present=false and parsed=true - "no deadline" is a fact, not a reading failure - and a field that is
// there and unreadable is present=false and parsed=false, which the caller NAMES and treats as no deadline.
//
// AN UNREADABLE FIELD IS NEVER A DEADLINE: answering present=true with the zero time made every row with a
// malformed timestamp read as "past its deadline since year 1", which is an alert about a fact nobody read.
func stuckMoment(value string) (time.Time, bool, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false, false
	}
	return parsed.UTC(), true, true
}

// stuckSince is when the row last moved, which is the age a "no live owner" finding reports. A row whose
// timestamp cannot be read falls back to now, so the report never claims an age it did not read.
func stuckSince(row stuckRow, now time.Time) time.Time {
	if updated, present, parsed := stuckMoment(row.Updated); present && parsed {
		return updated
	}
	return now
}

// stuckSubject is the row's one line as a reader sees it: the summary when the row has one, otherwise the
// body's first non-empty line, clipped. A row with neither is named by its id alone.
func stuckSubject(summary, body string) string {
	text := strings.TrimSpace(summary)
	if text == "" {
		for _, line := range strings.Split(body, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				text = line
				break
			}
		}
	}
	return stuckClip(strings.Join(strings.Fields(text), " "), 160)
}

// stuckClip shortens a line for a message, and says so when it did, so a truncated reason is never read as
// the whole one.
func stuckClip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.TrimRight(text[:limit], " ") + "..."
}

// stuckRuntime renders a runtime for a note: the session log carries none for a runtime this client cannot
// read, and "unknown" is what the rest of the client prints for that.
func stuckRuntime(runtime string) string {
	if runtime == "" {
		return "unknown"
	}
	return runtime
}

// stuckDuration is a duration as these messages speak it: minutes and hours, with the trailing seconds a
// minute-rounded value always carries trimmed off.
func stuckDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}

// ---- what it sends, and remembers ------------------------------------------

// stuckRecord is what one finding is remembered as: the rung it was sent at, when, and who was told.
type stuckRecord struct {
	Seats []string  `json:"seats"`
	At    time.Time `json:"at"`
	Stage int       `json:"stage"`
}

// stuckMemory is the file under the state root: the findings already sent and the seats already nudged, so a
// restarted agent does not start the window over.
type stuckMemory struct {
	Findings map[string]stuckRecord `json:"findings"`
	Idle     map[string]stuckRecord `json:"idle"`
}

func newStuckMemory() *stuckMemory {
	return &stuckMemory{Findings: map[string]stuckRecord{}, Idle: map[string]stuckRecord{}}
}

// stuckNotify sends what the plan says to send and remembers it. It is separate from the scan so the reading
// and the sending can be driven apart, which is how the hour-long escalation is testable without an hour.
func stuckNotify(deps *stuckDeps, opts stuckOptions, report *stuckReport) {
	memory, path := stuckLoad(deps, opts.Room)
	changed := false
	for _, finding := range report.Findings {
		key := finding.Kind + " " + finding.ID
		record, seen := memory.Findings[key]
		switch {
		case seen && record.Stage >= stuckEscalated:
			// Already the owner's. Nothing more is said about it while it stands, and a cleared finding is
			// forgotten below, so a row that comes back is news again.
			continue
		case seen && record.Stage == stuckAlerted && opts.Now.Sub(record.At) >= opts.EscalateAfter:
			written, err := deps.escalate(opts.Room, finding, opts.Now.Sub(record.At))
			if err != nil {
				report.Notes = append(report.Notes, "the escalation row could not be written: "+err.Error())
				continue
			}
			memory.Findings[key] = stuckRecord{At: opts.Now, Stage: stuckEscalated}
			report.Escalated = append(report.Escalated, fmt.Sprintf("%s -> %s: %s", key, stuckOwnerPrefix+opts.Room, stuckClip(written, 120)))
		case seen:
			// Already the seat's and the lead's, and the escalation window has not closed: silent is the
			// correct answer, because a room told the same thing every ten minutes stops reading the alert.
			// The window closing is what moves a finding up a rung, in the case above.
			continue
		default:
			stuckTell(deps, opts.Room, stuckRecipients(finding), stuckMessage(finding))
			memory.Findings[key] = stuckRecord{Seats: stuckRecipients(finding), At: opts.Now, Stage: stuckAlerted}
			report.Sent = append(report.Sent, key+" -> "+overlapNames(stuckRecipients(finding)))
		}
		changed = true
	}
	for _, seat := range report.Idle {
		record, seen := memory.Idle[seat.Seat]
		switch {
		case seen && record.Stage >= stuckEscalated:
			continue
		case seen && record.Stage == stuckAlerted && opts.Now.Sub(record.At) >= opts.EscalateAfter:
			finding := stuckFinding{Kind: stuckIdleKind, ID: seat.Seat, Seat: seat.Seat, Title: seat.Seat, Reason: stuckIdleReason(seat)}
			written, err := deps.escalate(opts.Room, finding, opts.Now.Sub(record.At))
			if err != nil {
				report.Notes = append(report.Notes, "the escalation row could not be written: "+err.Error())
				continue
			}
			memory.Idle[seat.Seat] = stuckRecord{At: opts.Now, Stage: stuckEscalated}
			report.Escalated = append(report.Escalated, fmt.Sprintf("idle %s -> %s: %s", seat.Seat, stuckOwnerPrefix+opts.Room, stuckClip(written, 120)))
		case seen:
			// Already the seat's and the lead's, and the escalation window has not closed: silent is the
			// correct answer, because a room told the same thing every ten minutes stops reading the alert.
			// The window closing is what moves a finding up a rung, in the case above.
			continue
		default:
			stuckTell(deps, opts.Room, []string{overlapLeadSeat}, stuckIdleMessage(seat))
			memory.Idle[seat.Seat] = stuckRecord{Seats: []string{overlapLeadSeat}, At: opts.Now, Stage: stuckAlerted}
			report.Sent = append(report.Sent, "idle "+seat.Seat+" -> the "+overlapLeadSeat)
		}
		changed = true
	}
	// The forget pass runs on EVERY pass, which is why its answer is taken first: `changed || stuckForget(…)`
	// would SHORT-CIRCUIT the call away on any pass that alerted or escalated, so a finding that cleared on
	// that pass would keep its record and its return inside the window would read as already-alerted.
	dropped := stuckForget(memory, report)
	if changed || dropped {
		stuckSave(path, memory)
	}
}

// stuckRecipients is who a finding is sent to: the seat it names when there is a session to tell, and the
// lead. A finding whose seat is GONE goes to the lead alone - addressing it to a seat with no session would
// be a message nothing reads, and the lead is who reassigns it. An idle seat is nudged to the lead alone
// too: the plan's idle nudge is "give a task to an idle seat", which is the lead's decision to make.
//
// overlapLeadSeat is the same literal the overlap detector reads, because the team file has one lead key.
func stuckRecipients(finding stuckFinding) []string {
	if finding.Seat == "" || !finding.Live || finding.Seat == overlapLeadSeat {
		return []string{overlapLeadSeat}
	}
	return []string{finding.Seat, overlapLeadSeat}
}

// stuckTell sends one message to each named recipient. It is the same shape as overlapTell, and it names the
// recipients the same way, so the two detectors' messages read alike.
func stuckTell(deps *stuckDeps, room string, seats []string, text string) {
	for _, seat := range seats {
		deps.say(room, seat, text)
	}
}

// stuckForget drops records nothing refers to any more, so the file does not grow with every row that was
// ever late. It answers whether it dropped anything, because that is also worth writing down.
func stuckForget(memory *stuckMemory, report *stuckReport) bool {
	dropped := false
	live := map[string]bool{}
	for _, finding := range report.Findings {
		live[finding.Kind+" "+finding.ID] = true
	}
	for key := range memory.Findings {
		if !live[key] {
			delete(memory.Findings, key)
			dropped = true
		}
	}
	idle := map[string]bool{}
	for _, seat := range report.Idle {
		idle[seat.Seat] = true
	}
	for key := range memory.Idle {
		if !idle[key] {
			delete(memory.Idle, key)
			dropped = true
		}
	}
	return dropped
}

// stuckLoad reads the memory. A file that cannot be read is started over rather than refused: the cost of
// forgetting is one repeated alert, and the cost of refusing is a detector that stops detecting.
func stuckLoad(deps *stuckDeps, room string) (*stuckMemory, string) {
	memory := newStuckMemory()
	path, err := deps.state(room)
	if err != nil {
		return memory, ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return memory, path
	}
	if json.Unmarshal(raw, memory) != nil || memory.Findings == nil || memory.Idle == nil {
		return newStuckMemory(), path
	}
	return memory, path
}

// stuckSave writes the memory. A write that fails is not an error the report carries: the scan still found
// what it found, and the worst outcome is a repeated alert after a restart.
func stuckSave(path string, memory *stuckMemory) {
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(raw, '\n'), 0o600)
}

// stuckMessage is one finding in its own words: what it is, which one, and the fact that makes it stuck. Every
// message says that NOTHING IS BLOCKED, because an alert that reads like a stop is one a seat acts on; the
// lead decides what happens to the row.
func stuckMessage(finding stuckFinding) string {
	name := finding.ID
	if finding.Title != "" {
		name = fmt.Sprintf("%s (%s)", finding.ID, finding.Title)
	}
	switch finding.Kind {
	case stuckBoardKind:
		return fmt.Sprintf("A board task is past its due date. Task %s: %s. Nothing is blocked. The lead decides "+
			"whether it is re-scoped, reassigned or dropped.", name, finding.Reason)
	case stuckIdleKind:
		return fmt.Sprintf("A seat is idle. Seat %s: %s. Nothing is blocked. The lead decides whether it takes "+
			"work; this alert assigns nothing.", name, finding.Reason)
	default:
		return fmt.Sprintf("A queue row is stuck. Row %s: %s. Nothing is blocked. The row is not moved by this "+
			"alert; the lead decides whether it is re-scoped or handed on.", name, finding.Reason)
	}
}

// stuckIdleReason is the idle finding's fact, in the same voice as the others.
func stuckIdleReason(seat idleSeat) string {
	return fmt.Sprintf("its session log (%s) has not been written for %s", stuckRuntime(seat.Runtime), stuckDuration(seat.Silent))
}

// stuckIdleMessage is the idle nudge: the lead reads it, and it is deliberately not sent to the idle seat.
func stuckIdleMessage(seat idleSeat) string {
	return fmt.Sprintf("A seat is idle. Seat %s: %s. Nothing is blocked. The lead decides whether it takes work; "+
		"this alert assigns nothing.", seat.Seat, stuckIdleReason(seat))
}

// stuckEscalationText is the row written to the room's human: the finding, how long it has stood, and where it
// is in the plan's route. Its first line is the one line a queue listing shows, so it says what the row is
// about rather than repeating the id.
func stuckEscalationText(room string, finding stuckFinding, open time.Duration) string {
	return fmt.Sprintf("Stuck escalation in %s: %s.\n\n%s\n\nThe seat and the lead were told %s ago and the "+
		"finding is still there. Nothing is blocked. This is the third rung of the escalation route (seat, then "+
		"lead, then owner), and it is here for the owner to decide.",
		room, stuckEscalationSubject(finding), stuckMessage(finding), stuckDuration(open))
}

// stuckEscalationSubject is the escalation row's one line.
func stuckEscalationSubject(finding stuckFinding) string {
	switch finding.Kind {
	case stuckBoardKind:
		return "board task " + finding.ID + " is past its due date"
	case stuckIdleKind:
		return "seat " + finding.ID + " has been idle past the window"
	default:
		return "queue row " + finding.ID + " is stuck"
	}
}

// ---- the production readers ------------------------------------------------

// stuckQueueRows reads this room's in-progress rows. `--all-rooms` because agent serves every room on the
// machine, and an unreadable listing is an ERROR rather than an empty result: a detector that could not look
// must not answer "nothing is stuck" (the same rule the orphan list follows).
//
// THE RAIL IS THE RUNTIME'S OWN (clientruntime.QueueList), the same reader doctor's orphan row uses: the
// predecessor shelled the retired `rig queue list --full`, whose row shape carried a claim time and a closure
// deadline the daemon's row does not have.
func stuckQueueRows(room string) ([]stuckRow, []string) {
	limit := int64(stuckQueueLimit)
	rows, err := clientruntime.NewClient("").QueueList(room, clientruntime.QueueListFilter{
		State: []string{stuckInProgress},
		Limit: &limit,
	})
	if err != nil {
		return nil, []string{"the queue listing could not be read: " + err.Error()}
	}
	out := make([]stuckRow, 0, len(rows))
	for _, row := range rows {
		if row.QitemID == "" {
			continue
		}
		if room != "" && stuckDestinationRoom(row.DestinationSession) != room {
			continue
		}
		out = append(out, stuckRow{
			QitemID:     row.QitemID,
			Destination: row.DestinationSession,
			State:       row.State,
			Priority:    row.Priority,
			Summary:     row.Summary,
			Body:        row.Body,
			Updated:     row.TsUpdated,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QitemID < out[j].QitemID })
	return out, nil
}

// stuckBoardTasks reads the board's in-progress tasks. It needs the same credential the rest of the client
// uses to talk to the cloud (AGENTHUB_URL and AGENTHUB_TOKEN), and a machine without them gets a NOTE rather
// than a silent empty board: agent's own start already requires both, so this is a reading that was asked for
// and could not be made, not a configuration this detector is free to skip.
func stuckBoardTasks() ([]boardTask, []string) {
	baseURL, token := os.Getenv("AGENTHUB_URL"), os.Getenv("AGENTHUB_TOKEN")
	if baseURL == "" || token == "" {
		return nil, []string{"the board was not read: AGENTHUB_URL and AGENTHUB_TOKEN are not both set"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), stuckBoardTimeout)
	defer cancel()
	body, err := clientsync.RequestJSON(ctx, http.MethodGet, baseURL, token,
		fmt.Sprintf("%slimit=%d", stuckBoardPath, stuckBoardLimit), nil)
	if err != nil {
		return nil, []string{"the board could not be read: " + err.Error()}
	}
	if success, ok := body["success"].(bool); !ok || !success {
		return nil, []string{"the board answered an error response"}
	}
	raw, ok := body["tasks"].([]any)
	if !ok {
		return nil, []string{"the board answered without a tasks list"}
	}
	tasks := make([]boardTask, 0, len(raw))
	for _, entry := range raw {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		tasks = append(tasks, boardTask{
			ID:        stuckString(row["id"]),
			Title:     stuckString(row["title"]),
			Status:    stuckString(row["status"]),
			Due:       stuckString(row["due_date"]),
			Assignees: stuckAssignees(row["assignees"]),
		})
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// stuckString is one JSON field as text, with a value that is not text reading as nothing rather than as a
// rendered Go value.
func stuckString(value any) string {
	text, _ := value.(string)
	return text
}

// stuckAssignees is a task's assignee list as seat keys: the board writes them as `@<seat>`, and the seat key
// is what a live session is named by.
func stuckAssignees(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	var seats []string
	for _, entry := range raw {
		seat := strings.TrimPrefix(stuckString(entry), "@")
		if seat != "" {
			seats = append(seats, seat)
		}
	}
	return seats
}

// stuckEscalateRow writes the escalation row through the room's own rail (clientruntime.QueueCreate), so the
// daemon - the rail's only writer - validates and stores it. The destination is the room's human, and the
// body is the rail's four-line grammar, which is what a create now requires: the predecessor wrote a free-form
// body through the retired `rig queue create`, and no such body is accepted today.
func stuckEscalateRow(room string, finding stuckFinding, open time.Duration) (string, error) {
	summary := stuckClip("Stuck escalation in "+room+": "+stuckEscalationSubject(finding), 120)
	body := fmt.Sprintf("facts: %s\nrule: %s\ndecision: %s\nnext: %s",
		stuckClip("the "+finding.Kind+" finding "+finding.ID+" stands: "+finding.Reason, 280),
		stuckClip("the escalation route is seat, then lead, then owner; the first alert went "+stuckDuration(open)+" ago", 110),
		stuckClip("nothing is blocked; the owner decides what happens to "+finding.ID, 100),
		stuckClip("the owner reads this row and decides", 78))
	priority := "normal"
	tag := stuckEscalationTag
	row, err := clientruntime.NewClient("").QueueCreate(room, clientruntime.QueueCreateInput{
		SourceSession:      stuckSourceSession(room),
		DestinationSession: stuckOwnerPrefix + room,
		Summary:            summary,
		Body:               body,
		Priority:           &priority,
		Tags:               []string{tag},
	})
	if err != nil {
		return "", fmt.Errorf("the room's rail: %w", err)
	}
	return row.QitemID, nil
}

// stuckSourceSession is who the escalation row is written by: the machine's own supervision process, named
// the way a room names its sessions so the row's source is readable rather than blank. It is not a seat and
// claims to be none.
func stuckSourceSession(room string) string {
	return room + "-agent@" + room
}

// ---- the verb, and agent's timer --------------------------------------------

type stuckCommand struct{}

func (stuckCommand) Name() string { return "stuck" }
func (stuckCommand) Summary() string {
	return "alert a queue row that is late or has no live owner, a board task past its due date, and an idle seat"
}
func (stuckCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, stuckUsage,
			"Reads one room and reports three things: a queue row of that room still in progress past its own\n"+
				"deadline or whose owning seat has no live session; a board task in progress past its own due date;\n"+
				"and a seat with a live session whose session log has not been written for the idle window. Only omp\n"+
				"and claude-code keep a log this client can read, so a seat whose log cannot be read is skipped and\n"+
				"named, never called idle; the owner node is never idle, because it runs no model. Nothing is\n"+
				"blocked: the first alert goes to the seat the finding names and to the lead, and a finding still\n"+
				"there an hour later is written to the room's human as a queue row. A finding is sent once at each\n"+
				"rung and a rung is never re-sent while it stands. agent runs this every "+stuckEvery.String()+" on its\n"+
				"own; this verb is the run on request, one scan by default.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("stuck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	every := flags.Duration("every", 0, "run a scan every DURATION until stopped (0 = one scan and exit)")
	idle := flags.Duration("idle-after", stuckIdleAfter, "how long a seat's session log may go unwritten before it is idle")
	escalate := flags.Duration("escalate-after", stuckEscalateAfter, "how long a finding may stand after the first alert before the owner is told")
	stateRoot := flags.String("state-root", "", "where OpenRig keeps its per-seat state: the flag, else this machine's own")
	asJSON := flags.Bool("json", false, "print one scan's report as JSON (one scan, never a loop)")
	dryRun := flags.Bool("dry-run", false, "print what would be sent and send nothing, and remember nothing")
	// THE ROOM IS LIFTED OUT OF THE ARGUMENTS BEFORE THE FLAGS ARE PARSED, because flag.Parse STOPS at the
	// first non-flag argument: with the documented order (`stuck ROOM --dry-run`) the flag was left as a
	// second positional, ignored, and the verb SENT what a dry run exists to hold back. The built binary
	// is what showed it. A flag given before the room keeps working.
	room, flagArgs := splitStuckArgs(args)
	if err := flags.Parse(flagArgs); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "4genteam stuck: unexpected argument %s - this verb takes one room name. %s\n", flags.Arg(0), stuckUsage)
		return clientcmd.ExitUsage
	}
	if room == "" {
		fmt.Fprintf(stderr, "4genteam stuck: ROOM is required. %s\n", stuckUsage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam stuck: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	deps := stuckDepsFor(root)
	opts := stuckOptions{Room: room, IdleAfter: *idle, EscalateAfter: *escalate}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	for {
		opts.Now = deps.now()
		report := stuckScanFor(deps, opts)
		if *dryRun {
			// A dry run reads and says what it WOULD send: it does not notify and does not write the memory,
			// so the first run of a detector that messages a room cannot message anyone.
			printStuckPlan(stdout, report)
			if *every <= 0 {
				return clientcmd.ExitOK
			}
		} else {
			stuckNotify(deps, opts, &report)
			if *asJSON {
				printStuckJSON(stdout, report)
			} else {
				printStuckReport(stdout, report)
			}
			if *every <= 0 {
				return clientcmd.ExitOK
			}
		}
		select {
		case <-interrupted:
			return clientcmd.ExitOK
		case <-time.After(*every):
		}
	}
}

// stuckFlagsWithValues are the flags of this verb that take an argument of their own, so the room can be
// picked out of a command line in EITHER order without a flag's value being mistaken for the room name.
var stuckFlagsWithValues = map[string]bool{
	"-every": true, "--every": true,
	"-idle-after": true, "--idle-after": true,
	"-escalate-after": true, "--escalate-after": true,
	"-state-root": true, "--state-root": true,
}

// splitStuckArgs answers the room name and the arguments the flag set should read, from either order.
// The first argument that is not a flag and not a flag's value is the room; anything positional after it is
// kept for the flag set, which refuses it, so a second room name is an error rather than a silent drop.
func splitStuckArgs(args []string) (string, []string) {
	var room string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			rest = append(rest, arg)
			if !strings.Contains(arg, "=") && stuckFlagsWithValues[arg] && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		if room == "" {
			room = arg
			continue
		}
		rest = append(rest, arg)
	}
	return room, rest
}

// printStuckReport is one scan as the operator reads it: what was sent, what was escalated, and what could not
// be read. The messages are shown as they were sent, so the report and the alert cannot drift apart.
func printStuckReport(out io.Writer, report stuckReport) {
	fmt.Fprintf(out, "stuck %s: %d stuck finding(s), %d idle seat(s), %d escalation row(s) written\n",
		report.Room, len(report.Findings), len(report.Idle), len(report.Escalated))
	for _, finding := range report.Findings {
		fmt.Fprintf(out, "  %s\n", stuckMessage(finding))
	}
	for _, seat := range report.Idle {
		fmt.Fprintf(out, "  %s\n", stuckIdleMessage(seat))
	}
	for _, line := range report.Escalated {
		fmt.Fprintf(out, "  escalated: %s\n", line)
	}
	if len(report.Skipped) > 0 {
		fmt.Fprintf(out, "  skipped: %s\n", strings.Join(report.Skipped, ", "))
	}
	if len(report.Notes) > 0 {
		fmt.Fprintf(out, "  not observed: %s\n", strings.Join(report.Notes, "; "))
	}
	fmt.Fprintf(out, "  %d alert(s) sent this scan; a finding already sent inside the window is not repeated\n",
		len(report.Sent))
}

// printStuckPlan is a dry run: the messages exactly as they would be sent, marked as not sent, so the first
// run of a detector that messages a room is safe to watch.
func printStuckPlan(out io.Writer, report stuckReport) {
	fmt.Fprintf(out, "stuck %s: %d stuck finding(s), %d idle seat(s) - DRY RUN, nothing sent\n",
		report.Room, len(report.Findings), len(report.Idle))
	for _, finding := range report.Findings {
		recipients := stuckRecipients(finding)
		fmt.Fprintf(out, "  would send to %s: %s\n", overlapNames(recipients), stuckMessage(finding))
		fmt.Fprintf(out, "  would escalate to %s after %s, if still there: %s\n", stuckOwnerPrefix+report.Room,
			stuckDuration(report.EscalateAfter), stuckEscalationText(report.Room, finding, report.EscalateAfter))
	}
	for _, seat := range report.Idle {
		fmt.Fprintf(out, "  would send to the %s: %s\n", overlapLeadSeat, stuckIdleMessage(seat))
	}
	if len(report.Skipped) > 0 {
		fmt.Fprintf(out, "  skipped: %s\n", strings.Join(report.Skipped, ", "))
	}
	if len(report.Notes) > 0 {
		fmt.Fprintf(out, "  not observed: %s\n", strings.Join(report.Notes, "; "))
	}
}

// printStuckJSON is one scan's report as JSON, for a caller that reads rather than watches.
func printStuckJSON(out io.Writer, report stuckReport) {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(out, "{\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintln(out, string(raw))
}

// stuckRoomScan is agent's own scan of one room: the same function the verb calls, with the production wiring,
// so the operator's run on request and the loop can never do different things.
func stuckRoomScan(room string, now time.Time) stuckReport {
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return stuckReport{Room: room, Notes: []string{"the state root cannot be resolved: " + err.Error()}}
	}
	deps := stuckDepsFor(root)
	opts := stuckOptions{Room: room, IdleAfter: stuckIdleAfter, EscalateAfter: stuckEscalateAfter, Now: now}
	report := stuckScanFor(deps, opts)
	stuckNotify(deps, opts, &report)
	return report
}

// stuckDue is whether the alert scan's ten minutes have passed. It is a method so a case can drive the timer
// without waiting ten minutes.
func (m *agentManager) stuckDue(now time.Time) bool {
	if !m.lastStuck.IsZero() && now.Sub(m.lastStuck) < stuckEvery {
		return false
	}
	m.lastStuck = now
	return true
}

// scanStuck runs the alert scan for every room agent supervises, and logs only what it SENT or ESCALATED: a
// pass that found nothing is not news, and a log line every ten minutes per room would bury the alerts that
// matter.
func (m *agentManager) scanStuck(rooms []string, now time.Time) {
	for _, room := range rooms {
		report := stuckRoomScan(room, now)
		for _, line := range report.Sent {
			m.log("stuck " + room + ": " + line)
		}
		for _, line := range report.Escalated {
			m.log("stuck " + room + ": escalated: " + line)
		}
		for _, note := range report.Notes {
			m.log("stuck " + room + ": not observed: " + note)
		}
	}
}
