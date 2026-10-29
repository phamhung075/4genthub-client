// Package seatwait is a seat's declared wait: "I am blocked on THIS, do not call me idle".
//
// WHY IT EXISTS. A seat that waits for a reviewer verdict, a director merge, an answer, a build slot or the end
// of a peak window looked exactly like a seat that stopped: the idle guard nudged it ("You went quiet in the
// middle of a job"), the stuck scan told the lead and then the owner ("rust-dev idle 11h13m"), heal reported
// seat-quiet. Each nudge cost a turn and made the seat do busywork (room aa2-rs, 2026-10-29).
//
// WHAT A WAIT IS. A record in the seat's own directory (`<root>/rooms/<room>/seats/<seat>/wait.json`) naming
// one kind and one target. The verb writes it and returns: nothing sleeps and nothing polls on the seat's side,
// the seat ends its turn and burns no tokens. The idle guard's pass ends it, first of:
//
//	(a) the seat took a new turn (a message reached it): no extra message, the message was the wake;
//	(b) a rail row addressed to the seat appeared or changed and is not the awaited target: no extra message;
//	(c) the awaited event, when it is cheap to see: the rail row closed, the commit is in main, a build slot
//	    is free, the off-peak window is open - the seat is woken with one line naming it;
//	(d) the --max expiry - the seat is woken with one line saying so.
//
// A WAIT NEVER HIDES CLAIMABLE WORK: a wait is refused while a pending row other than its target is addressed
// to the seat, and (b) ends it the moment one arrives.
//
// Every start and end is one JSON line in `<root>/rooms/<room>/waits.jsonl`, which team-benchmark reads.
package seatwait

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const (
	// DefaultMax is a wait's life when --max is not given.
	DefaultMax = 60 * time.Minute
	// MaxCap is the longest wait accepted: past it a seat is asked to look again, whatever it waits on.
	MaxCap = 4 * time.Hour

	// How a wait ended, as the log records it.
	EndMessage   = "message"   // (a) the seat took a new turn
	EndNewItem   = "new-item"  // (b) a rail row for the seat appeared or changed
	EndEvent     = "event"     // (c) the awaited event happened
	EndExpired   = "expired"   // (d) --max ran out
	EndCleared   = "cleared"   // the seat cleared it itself
	EndReplaced  = "replaced"  // a new wait replaced it
	EndClaimable = "claimable" // claimable work was found when the guard first looked
)

// Kinds are the things a seat may wait on, with what each target names.
var Kinds = map[string]string{
	"review": "a rail row id or branch@sha awaiting a reviewer verdict",
	"merge":  "branch@sha (or branch) awaiting a merge into main",
	"answer": "a rail row id or a ready.md QUESTION ref awaiting an answer",
	"slot":   "a build slot name, e.g. cargo",
	"peak":   "offpeak: the end of the DeepSeek peak window",
	"seat":   "another seat's key",
}

// directorKinds are the waits whose target only the director or the owner can release.
var directorKinds = map[string]bool{"merge": true, "answer": true}

// Record is one seat's live wait.
type Record struct {
	Room     string    `json:"room"`
	Seat     string    `json:"seat"`
	Kind     string    `json:"kind"`
	Target   string    `json:"target"`
	Note     string    `json:"note,omitempty"`
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline"`
	// Repo and SHA make a merge wait detectable: the commit is reachable from the repo's main.
	Repo string `json:"repo,omitempty"`
	SHA  string `json:"sha,omitempty"`
	// SlotDir makes a slot wait detectable: a free slot file in it.
	SlotDir string `json:"slot_dir,omitempty"`
	// WatchQitem is a rail row whose closing ends the wait (the target, when it is an active row).
	WatchQitem string `json:"watch_qitem,omitempty"`
	// Covered are the "id:state" rows addressed to the seat when the wait began; Snapshotted says they were read.
	Covered     []string `json:"covered,omitempty"`
	Snapshotted bool     `json:"snapshotted"`
	// Settled is set once the declaring turn ended; SettledLog is the seat log's mtime then. A log written
	// after it is a new turn: rule (a).
	Settled    bool    `json:"settled"`
	SettledLog float64 `json:"settled_log,omitempty"`
}

// On is "kind:target", the form the verb takes and every report prints.
func (r Record) On() string { return r.Kind + ":" + r.Target }

// Max is the wait's whole life.
func (r Record) Max() time.Duration { return r.Deadline.Sub(r.Started) }

// Live says the wait still holds at now.
func (r Record) Live(now time.Time) bool { return r.Kind != "" && now.Before(r.Deadline) }

// OnDirector says only the director or the owner can release this wait.
func (r Record) OnDirector() bool { return directorKinds[r.Kind] }

// Status is the segment `room status` prints: `waiting=<kind>:<target> <elapsed>/<max>`.
func (r Record) Status(now time.Time) string {
	return fmt.Sprintf("waiting=%s %s/%s", r.On(), Short(now.Sub(r.Started)), Short(r.Max()))
}

// Covers says a rail row belongs to the wait: the awaited target, or a row the seat already held when it began.
// A runtime alert naming only covered rows is the wait, not a stuck seat.
func (r Record) Covers(qitemID string) bool {
	if qitemID == r.Target || qitemID == r.WatchQitem {
		return true
	}
	for _, pair := range r.Covered {
		if id, _, _ := strings.Cut(pair, ":"); id == qitemID {
			return true
		}
	}
	return false
}

// Short renders a duration the way a reader counts it: 45s, 12m, 1h05m.
func Short(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
}

// ParseOn splits "kind:target" and refuses an unknown kind or an empty target: waiting on nothing is idle.
func ParseOn(on string) (kind, target string, err error) {
	kind, target, found := strings.Cut(strings.TrimSpace(on), ":")
	kind, target = strings.TrimSpace(kind), strings.TrimSpace(target)
	if _, known := Kinds[kind]; !known {
		return "", "", fmt.Errorf("unknown wait kind %q in %q: use one of %s", kind, on, kindList())
	}
	if !found || target == "" {
		return "", "", fmt.Errorf("--on %q names no target: waiting on nothing is idle, take a backup item instead", on)
	}
	if kind == "peak" && target != "offpeak" {
		return "", "", fmt.Errorf("--on peak:%s: the only peak target is offpeak", target)
	}
	return kind, target, nil
}

func kindList() string {
	names := make([]string, 0, len(Kinds))
	for name := range Kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ClampMax applies the default and the cap.
func ClampMax(max time.Duration) time.Duration {
	if max <= 0 {
		return DefaultMax
	}
	if max > MaxCap {
		return MaxCap
	}
	return max
}

// ---- the rail ----------------------------------------------------------------

// SeatOf is the seat key a rail destination names in this room, or "". Both destination forms are read: the
// runtime's `agenthub--<room>--<seat>` and the older `<room>-<seat>@<room>`.
func SeatOf(room, destination string) string {
	if sessionRoom, seat, ok := clientruntime.ParseSeatSessionName(destination); ok {
		if sessionRoom != "" && sessionRoom != room {
			return ""
		}
		return seat
	}
	local, destRoom, found := strings.Cut(destination, "@")
	if !found {
		// A bare seat key, the form the idle guard also reads.
		return destination
	}
	if destRoom != room || !strings.HasPrefix(local, room+"-") {
		return ""
	}
	return strings.TrimPrefix(local, room+"-")
}

// seatRows are the active rows addressed to the record's seat.
func seatRows(r Record, rows []clientruntime.QueueRow) []clientruntime.QueueRow {
	var out []clientruntime.QueueRow
	for _, row := range rows {
		if SeatOf(r.Room, row.DestinationSession) == r.Seat {
			out = append(out, row)
		}
	}
	return out
}

// ClaimableError is the refusal of a wait while claimable work is addressed to the seat.
type ClaimableError struct{ IDs []string }

func (e *ClaimableError) Error() string {
	return fmt.Sprintf("claimable work is addressed to this seat (%s): take it before waiting - a wait never hides work",
		strings.Join(e.IDs, ", "))
}

// Snapshot records the rows the seat holds as the wait begins, and refuses when one of them is a pending row
// that is not the awaited target. The target itself, when it is an active row, becomes the row to watch.
func Snapshot(r *Record, rows []clientruntime.QueueRow) error {
	var claimable []string
	r.Covered = nil
	for _, row := range rows {
		if row.QitemID == r.Target {
			r.WatchQitem = row.QitemID
		}
	}
	for _, row := range seatRows(*r, rows) {
		if row.QitemID != r.Target && row.State == "pending" {
			claimable = append(claimable, row.QitemID)
		}
		r.Covered = append(r.Covered, row.QitemID+":"+row.State)
	}
	sort.Strings(r.Covered)
	r.Snapshotted = true
	if len(claimable) > 0 {
		sort.Strings(claimable)
		return &ClaimableError{IDs: claimable}
	}
	return nil
}

// newItem is the first row addressed to the seat that was not there, or not in that state, when the wait
// began - and is not the awaited target.
func newItem(r Record, rows []clientruntime.QueueRow) (string, bool) {
	held := map[string]bool{}
	for _, pair := range r.Covered {
		held[pair] = true
	}
	for _, row := range seatRows(r, rows) {
		if row.QitemID == r.Target || row.QitemID == r.WatchQitem {
			continue
		}
		if !held[row.QitemID+":"+row.State] {
			return row.QitemID + " (" + row.State + ")", true
		}
	}
	return "", false
}

// ---- files -------------------------------------------------------------------

// Path is the seat's wait record.
func Path(root, room, seat string) string {
	return filepath.Join(clientruntime.SeatDir(root, room, seat), "wait.json")
}

// LogPath is the room's wait log, one JSON line per start and end.
func LogPath(root, room string) string {
	return filepath.Join(root, "rooms", room, "waits.jsonl")
}

// Read answers the seat's wait record, live or expired; false when there is none.
func Read(root, room, seat string) (Record, bool) {
	if root == "" {
		return Record{}, false
	}
	raw, err := os.ReadFile(Path(root, room, seat))
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(raw, &r) != nil || r.Kind == "" {
		return Record{}, false
	}
	return r, true
}

// Live answers the seat's wait when one holds at now.
func Live(root, room, seat string, now time.Time) (Record, bool) {
	r, ok := Read(root, room, seat)
	if !ok || !r.Live(now) {
		return Record{}, false
	}
	return r, true
}

// Write stores the record atomically, so a reader never sees half of one.
func Write(root string, r Record) error {
	path := Path(root, r.Room, r.Seat)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Event is one line of the wait log.
type Event struct {
	Ts       string  `json:"ts"`
	Event    string  `json:"event"` // start | end | escalate
	Room     string  `json:"room"`
	Seat     string  `json:"seat,omitempty"`
	Kind     string  `json:"kind,omitempty"`
	Target   string  `json:"target,omitempty"`
	MaxS     int64   `json:"max_s,omitempty"`
	Note     string  `json:"note,omitempty"`
	How      string  `json:"how,omitempty"`
	Reason   string  `json:"reason,omitempty"`
	Duration float64 `json:"duration_s,omitempty"`
}

// AppendLog adds one line to the room's wait log.
func AppendLog(root string, e Event) error {
	path := LogPath(root, e.Room)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.Write(append(raw, '\n'))
	return err
}

// Start stores a new wait and logs it; a wait already there is ended as replaced.
func Start(root string, r Record) error {
	if old, ok := Read(root, r.Room, r.Seat); ok && old.Live(r.Started) {
		_ = End(root, old, r.Started, EndReplaced, "replaced by "+r.On())
	}
	if err := Write(root, r); err != nil {
		return err
	}
	return AppendLog(root, Event{Ts: stamp(r.Started), Event: "start", Room: r.Room, Seat: r.Seat, Kind: r.Kind,
		Target: r.Target, MaxS: int64(r.Max() / time.Second), Note: r.Note})
}

// End removes the record and logs how the wait ended.
func End(root string, r Record, now time.Time, how, reason string) error {
	if err := os.Remove(Path(root, r.Room, r.Seat)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return AppendLog(root, Event{Ts: stamp(now), Event: "end", Room: r.Room, Seat: r.Seat, Kind: r.Kind, Target: r.Target,
		How: how, Reason: reason, Duration: now.Sub(r.Started).Seconds()})
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---- one pass ----------------------------------------------------------------

// Probes are the cheap readings of an awaited event. A nil probe sees nothing.
type Probes struct {
	// MergedAs answers the commit that brought sha into main (a merge commit, else main's tip).
	MergedAs func(repo, sha string) (string, bool)
	// SlotFree answers whether a slot in dir can be taken now.
	SlotFree func(dir string) bool
	// OffPeak answers whether the off-peak window is open at now.
	OffPeak func(now time.Time) bool
}

// Observation is what the guard read about one seat this pass.
type Observation struct {
	Now      time.Time
	Idle     bool    // the seat is at its prompt
	LogMtime float64 // the seat log's mtime, unix seconds; 0 when unknown
	Rows     []clientruntime.QueueRow
	RowsOK   bool // the rail was read; without it neither (b) nor a row's closing is decided
}

// Outcome is what a pass decided for one wait.
type Outcome struct {
	End    bool
	How    string
	Reason string
	// Wake is the one line sent to the seat; empty when the ending was itself a wake.
	Wake string
	// Dirty says the record changed and must be written back (settled, snapshotted).
	Dirty bool
}

// Evaluate decides one pass for one wait. It changes r in place when the wait settles or is snapshotted.
func Evaluate(r *Record, obs Observation, probes Probes) Outcome {
	var out Outcome
	if obs.RowsOK && !r.Snapshotted {
		out.Dirty = true
		if err := Snapshot(r, obs.Rows); err != nil {
			return Outcome{End: true, How: EndClaimable, Reason: err.Error(),
				Wake: fmt.Sprintf("wait ended: %s - %s", r.On(), err.Error())}
		}
	}
	if obs.RowsOK && r.Snapshotted {
		if id, found := newItem(*r, obs.Rows); found {
			return Outcome{End: true, How: EndNewItem, Reason: "rail row " + id + " is addressed to the seat"}
		}
	}
	if !r.Settled {
		if obs.Idle {
			r.Settled, r.SettledLog, out.Dirty = true, obs.LogMtime, true
		}
	} else if obs.LogMtime > r.SettledLog+1 {
		return Outcome{End: true, How: EndMessage, Reason: "the seat took a new turn"}
	}
	if reason, seen := event(*r, obs, probes); seen {
		return Outcome{End: true, How: EndEvent, Reason: reason, Wake: fmt.Sprintf("wait ended: %s %s", r.On(), reason)}
	}
	if !obs.Now.Before(r.Deadline) {
		return Outcome{End: true, How: EndExpired, Reason: "max " + Short(r.Max()) + " reached",
			Wake: fmt.Sprintf("wait expired after %s: %s still open - pick a backup item or ask", Short(r.Max()), r.On())}
	}
	return out
}

// event is the awaited event when it can be seen cheaply.
func event(r Record, obs Observation, probes Probes) (string, bool) {
	if r.WatchQitem != "" && obs.RowsOK {
		open := false
		for _, row := range obs.Rows {
			if row.QitemID == r.WatchQitem {
				open = true
			}
		}
		if !open {
			return "closed on the rail", true
		}
	}
	switch r.Kind {
	case "merge":
		if r.Repo != "" && r.SHA != "" && probes.MergedAs != nil {
			if as, ok := probes.MergedAs(r.Repo, r.SHA); ok {
				return "merged as " + as, true
			}
		}
	case "slot":
		if r.SlotDir != "" && probes.SlotFree != nil && probes.SlotFree(r.SlotDir) {
			return "a " + r.Target + " slot is free", true
		}
	case "peak":
		if probes.OffPeak != nil && probes.OffPeak(obs.Now) {
			return "the off-peak window is open", true
		}
	}
	return "", false
}
