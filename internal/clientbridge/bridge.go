package clientbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// The wire contract, in the Python bridge's own numbers.
const (
	statusPath      = "/api/v2/openrig/seat-status"
	notifyPath      = "/api/v2/broadcast/notify"
	HeartbeatSecs   = 60.0
	MaxBackoffSecs  = 120.0
	SendTimeout     = 15 * time.Second
	ToolTimeout     = 15 * time.Second
	MaxResponseSize = 1 << 20
	DefaultInterval = 20.0
)

// Sender posts a report and returns the status and the (bounded) body. The body matters on an error
// status too: it is the only channel that says what the cloud expects per seat.
//
// A transport that never answered comes back as an error, NOT as a status: the status is the cloud's
// answer, and there was none. The Python modelled it this way too - its sender raised and the loop named
// the reason - so a failure can never be reported as `server answered HTTP 0`, a sentence about an
// answer that did not exist.
type Sender func(body []byte) (int, []byte, error)

// Runner runs a LOCAL tool and returns its stdout. The rig itself is reached through clientcmd.Rig,
// which is the one place an external command starts; herdr is not rig, so it gets this seam - argv,
// never a shell string, the same rule.
type Runner func(ctx context.Context, argv ...string) (string, error)

// Bridge builds payloads and decides when to send them.
type Bridge struct {
	MachineID string
	Interval  time.Duration
	Send      Sender
	// Notify posts one dashboard notification frame per human-addressed OpenRig row (D3). Nil
	// disables the producer: 4genteam bridge once --print and the parity fixtures build payloads
	// without talking to anything, and a bridge that cannot notify must still report.
	Notify Sender
	// Runner runs a LOCAL tool that is not rig; rig itself arrives through clientcmd.Rig, the one
	// place an external command starts. Named Runner rather than Run so it cannot collide with the
	// loop's own Run method.
	Runner    Runner
	Known     map[string]string
	PinsDir   string
	StatePath string
	// Note receives one line per newly-changed status message, so a caller decides where the
	// operator reads it. The Python printed to stderr, and the dispatcher passes stderr through.
	Note func(string)
	// clock is a seam, because the heartbeat decision is otherwise untestable without sleeping.
	clock func() time.Time

	state       map[string]string
	notified    map[string]string
	notes       map[string]string
	stripDetail bool
	backoff     time.Duration
	lastKey     string
	lastSent    time.Time
}

// NewBridge builds a bridge with the defaults the Python had: the pins directory the client writes,
// the sync record beside it, and a runner that shells out to argv. notify may be nil - a bridge that
// only builds payloads has nowhere to post notifications, and says so by not sending any.
func NewBridge(machineID string, interval time.Duration, send Sender, notify Sender, known map[string]string, pinsDir, statePath string, note func(string)) *Bridge {
	if interval <= 0 {
		interval = time.Duration(DefaultInterval * float64(time.Second))
	}
	return &Bridge{
		MachineID: machineID, Interval: interval, Send: send, Notify: notify, Runner: execRunner,
		Known: known, PinsDir: pinsDir, StatePath: statePath, Note: note,
		clock: time.Now, state: ReadSyncState(statePath), notified: ReadNotifiedState(statePath),
		notes: map[string]string{},
	}
}

// execRunner is the default runner: argv, no shell, bounded by ToolTimeout.
func execRunner(ctx context.Context, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ToolTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", argv[0], err)
	}
	return string(out), nil
}

// note prints a status line once per distinct message, which is what keeps a 20-second loop from
// repeating the same sentence forever.
func (b *Bridge) note(key, message string) {
	if b.notes[key] == message {
		return
	}
	b.notes[key] = message
	if b.Note != nil {
		b.Note(message)
	}
}

// read runs a local tool through the runner and parses it, noting either outcome and reading an
// absent or broken tool as EMPTY rather than failing the whole report: a machine without herdr still
// reports its seats.
func (b *Bridge) read(key string, argv []string, parse func(string) ([]any, error)) []any {
	output, err := b.Runner(context.Background(), argv...)
	if err != nil {
		b.note(key, fmt.Sprintf("%s unavailable: %v", argv[0], err))
		return nil
	}
	items, err := parse(output)
	if err != nil {
		b.note(key, fmt.Sprintf("%s unavailable: %v", argv[0], err))
		return nil
	}
	b.note(key, fmt.Sprintf("%s ok", argv[0]))
	return items
}

// BuildPayload reads the local state and builds what would be sent. Every read goes through a seam
// that cannot start a shell: the rig tool arrives through the clientcmd.Rig contract (the one place
// an external command starts, and the Windows route with it), and herdr through the Runner.
func (b *Bridge) BuildPayload(rig *clientcmd.Rig) Payload {
	nodes := b.readRig(rig)
	agents := b.read("herdr", []string{"herdr", "api", "snapshot"}, ParseHerdrAgents)

	seats, invalid, duplicates := BuildSeats(nodes, b.Known, b.PinsDir, b.stripDetail)
	if invalid > 0 {
		b.note("invalid", fmt.Sprintf("skipped %d seat(s) with invalid names", invalid))
	}
	for _, message := range duplicates {
		b.note("duplicate "+message, message)
	}
	return Payload{
		MachineID:  b.MachineID,
		ReportedAt: UTCNow(),
		Seats:      seats,
		Agents:     BuildAgents(agents),
		Edges:      BuildEdges(RigRooms(nodes), func(rigID string) []rigEdge { return b.readEdges(rig, rigID) }),
	}
}

// readEdges reads `rig export <rigId> -o /dev/stdout` the same two ways readRig reads the nodes,
// and reads a rig it cannot export as having no edges.
func (b *Bridge) readEdges(rig *clientcmd.Rig, rigID string) []rigEdge {
	key := "edges " + rigID
	argv := []string{"rig", "export", rigID, "-o", "/dev/stdout"}
	var output string
	var err error
	if rig != nil {
		output, err = rig.Run(context.Background(), argv[1:]...)
	} else {
		output, err = b.Runner(context.Background(), argv...)
	}
	var edges []rigEdge
	if err == nil {
		edges, err = ParseRigEdges(output)
	}
	if err != nil {
		b.note(key, fmt.Sprintf("rig unavailable: %v", err))
		return nil
	}
	b.note(key, "rig ok")
	return edges
}

// readRig reads `rig ps --json --nodes -A` through the contract when the dispatcher resolved the rig
// tool, and through the Runner when a caller supplied one instead (the tests, and any environment
// where rig is reached another way).
func (b *Bridge) readRig(rig *clientcmd.Rig) []any {
	if rig != nil {
		output, err := rig.Run(context.Background(), "ps", "--json", "--nodes", "-A")
		if err != nil {
			b.note("rig", fmt.Sprintf("rig unavailable: %v", err))
			return nil
		}
		items, err := ParseRigNodes(output)
		if err != nil {
			b.note("rig", fmt.Sprintf("rig unavailable: %v", err))
			return nil
		}
		b.note("rig", "rig ok")
		return items
	}
	return b.read("rig", []string{"rig", "ps", "--json", "--nodes", "-A"}, ParseRigNodes)
}

// LocalRecord is what this machine can say about each seat from its own record - unchanged, changed
// or unknown - and never the cloud's verdict, which only the answer to a report knows.
func (b *Bridge) LocalRecord(seats []SeatStatus) map[string]SeatNote {
	out := make(map[string]SeatNote, len(seats))
	for _, seat := range seats {
		confirmed := b.state[b.stateKey(seat.Room, seat.Seat)]
		since := "unknown"
		switch {
		case confirmed == "" || seat.Hash == "":
			since = "unknown"
		case seat.Hash == confirmed:
			since = "unchanged"
		default:
			since = "changed"
		}
		out[seat.Room+"/"+seat.Seat] = SeatNote{
			Running: seat.Hash, LastInSync: confirmed, SinceLastInSync: since,
		}
	}
	return out
}

func (b *Bridge) stateKey(room, seat string) string {
	return b.MachineID + "/" + room + "/" + seat
}

// ApplyVerdicts takes the cloud's answer: the `in_sync` verdicts are recorded, and every other one is
// NAMED, because this answer is the only channel that knows the cloud's current expectation. An
// answer that carries no verdicts leaves the record as it was rather than clearing it.
func (b *Bridge) ApplyVerdicts(body []byte) {
	verdicts := verdictsOf(body)
	if verdicts == nil {
		return
	}
	changed := false
	for _, raw := range verdicts {
		verdict, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		room, seat := str(verdict["room"]), str(verdict["seat"])
		if room == "" || seat == "" {
			continue
		}
		where := room + "/" + seat
		expected, sync := str(verdict["expected_hash"]), str(verdict["sync"])
		if sync == "in_sync" {
			if expected == "" {
				continue
			}
			if key := b.stateKey(room, seat); b.state[key] != expected {
				b.state[key] = expected
				changed = true
			}
			continue
		}
		message := fmt.Sprintf("the cloud reports %s %s", where, sync)
		if sync == "" {
			message = fmt.Sprintf("the cloud reports %s without a verdict", where)
		}
		if expected != "" {
			message += fmt.Sprintf(" (it expects %s)", expected)
		}
		b.note("cloud:"+where, message)
	}
	if changed {
		WriteSyncState(b.StatePath, b.MachineID, b.state, b.notified, b.Note)
	}
}

// Cycle runs one cycle and returns how long to wait before the next.
//
// The three outcomes are the Python's, kept because each one is a decision rather than a default:
// a 2xx records the verdicts and clears any degradation; a 422 means the cloud refused the text as
// secret-bearing, so the NEXT cycle drops detail entirely rather than scrubbing harder; and a 401
// fails loud and actionable, because a rejected credential never recovers by retrying and a bridge
// that cannot report must say so instead of looping quietly.
func (b *Bridge) Cycle(ctx context.Context, rig *clientcmd.Rig) time.Duration {
	payload := b.BuildPayload(rig)
	for where, record := range b.LocalRecord(payload.Seats) {
		if record.SinceLastInSync == "changed" {
			b.note("changed:"+where, fmt.Sprintf(
				"%s has changed since the cloud last confirmed it in sync (running %s, confirmed %s)",
				where, record.Running, record.LastInSync))
		}
	}
	// The human-addressed rows are forwarded whether or not a report goes out this cycle. The early
	// return below skips the REPORT when nothing changed, and a row addressed to the human is news on
	// its own, so this must not sit behind that decision.
	b.notifyHumans(rig)

	body, err := json.Marshal(payload)
	if err != nil {
		return b.fail(fmt.Sprintf("cycle failed: %v", err))
	}
	key := string(body)
	now := b.clock()
	due := b.lastSent.IsZero() || now.Sub(b.lastSent) >= time.Duration(HeartbeatSecs*float64(time.Second))
	if key == b.lastKey && !due {
		return b.Interval
	}
	status, answer, err := b.send(body)
	if err != nil {
		return b.fail(fmt.Sprintf("cycle failed: %v", err))
	}
	switch {
	case status >= 200 && status < 300:
		b.ApplyVerdicts(answer)
		b.lastKey, b.lastSent = key, now
		b.stripDetail = false
		b.backoff = 0
		b.note("send", "sent")
		return b.Interval
	case status == 422:
		b.stripDetail = true
		b.note("send", "server rejected text as secret-bearing (422); dropping detail next cycle")
		return b.Interval
	case status == 401:
		return b.fail("AGENTHUB_TOKEN rejected (HTTP 401): check the token and AGENTHUB_URL")
	default:
		return b.fail(fmt.Sprintf("server answered HTTP %d", status))
	}
}

// send hands the report to the transport. A transport that never answered is a cycle failure carrying
// its own reason - not a status - and the body is bounded here whatever a Sender hands back.
func (b *Bridge) send(body []byte) (int, []byte, error) {
	if b.Send == nil {
		return 0, nil, fmt.Errorf("no sender configured")
	}
	status, answer, err := b.Send(body)
	if err != nil {
		return 0, nil, err
	}
	if len(answer) > MaxResponseSize {
		answer = answer[:MaxResponseSize]
	}
	return status, answer, nil
}

// fail notes the message and doubles the wait, up to MaxBackoffSecs.
func (b *Bridge) fail(message string) time.Duration {
	b.note("send", message)
	if b.backoff == 0 {
		b.backoff = b.Interval
	} else {
		b.backoff *= 2
	}
	max := time.Duration(MaxBackoffSecs * float64(time.Second))
	if b.backoff > max {
		b.backoff = max
	}
	return b.backoff
}

// Run loops until ctx is done. With once it runs a single cycle and reports whether anything was
// accepted: EXIT_OK only when nothing is degraded and no backoff is held, clientcmd.ExitRemote otherwise - so a
// one-shot run can be used as a check rather than only as a report.
func (b *Bridge) Run(ctx context.Context, rig *clientcmd.Rig, once bool) int {
	for {
		delay := b.Cycle(ctx, rig)
		if once {
			if b.backoff == 0 && !b.stripDetail {
				return clientcmd.ExitOK
			}
			return clientcmd.ExitRemote
		}
		select {
		case <-ctx.Done():
			return clientcmd.ExitOK
		case <-time.After(delay):
		}
	}
}

// verdictsOf returns the per-seat verdicts in a report answer, or nil when the answer carries none.
// nil means the record is left alone: an answer that is absent, not JSON or carries no verdicts is
// not evidence that anything moved.
func verdictsOf(body []byte) []any {
	var answer any
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil
	}
	object, ok := answer.(map[string]any)
	if !ok {
		return nil
	}
	verdicts, ok := object["verdicts"].([]any)
	if !ok {
		return nil
	}
	return verdicts
}

// ReadNotifiedState reads the ids of the human-addressed rows this bridge has already posted, or empty.
// It shares the sync record's file on purpose: one record for one bridge, so a restart cannot lose one
// half while keeping the other. A missing, unreadable or malformed record reads as empty, which costs
// at most one duplicate notification (the dashboard deduplicates on entity_id) and never a wrong one.
func ReadNotifiedState(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var record struct {
		Notified map[string]string `json:"notified"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return out
	}
	for key, value := range record.Notified {
		if value != "" {
			out[key] = value
		}
	}
	return out
}

// ReadSyncState reads the recorded last-in-sync hash per seat key, or empty. A missing, unreadable or
// malformed record reads as empty rather than failing: a bridge whose own state file is gone must
// still report, and it never invents a hash.
func ReadSyncState(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var record struct {
		Seats map[string]string `json:"seats"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return out
	}
	for key, value := range record.Seats {
		if value != "" {
			out[key] = value
		}
	}
	return out
}

// WriteSyncState records the expected hash per seat, and the human rows already notified, through a
// temporary file and a rename, so a reader sees the previous whole record or the new one and never a
// half-written file. BOTH halves are written together: they share one file, and a writer that carried
// only its own half would erase the other one. A write that fails is loud but not fatal: a bridge that
// cannot record must still report.
func WriteSyncState(path, machineID string, seats, notified map[string]string, note func(string)) {
	record := map[string]any{"machine_id": machineID, "seats": seats}
	if len(notified) > 0 {
		record["notified"] = notified
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		report(note, fmt.Sprintf("cannot record the sync state in %s: %v", path, err))
		return
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(payload, '\n'), 0o644); err != nil {
		report(note, fmt.Sprintf("cannot record the sync state in %s: %v", path, err))
		return
	}
	if err := os.Rename(temporary, path); err != nil {
		report(note, fmt.Sprintf("cannot record the sync state in %s: %v", path, err))
	}
}

func report(note func(string), message string) {
	if note != nil {
		note(message)
	}
}

// HasPinnedSeat reports whether any seat in the payload carries a hash. A parity fixture without one
// exercises the hash field as always-empty; this exists so a test can say that out loud.
func HasPinnedSeat(seats []SeatStatus) bool {
	for _, seat := range seats {
		if seat.Hash != "" {
			return true
		}
	}
	return false
}

// TrimForReport is the bounded body a caller logs, so a hostile answer cannot be unbounded.
func TrimForReport(body []byte) string {
	if len(body) > MaxResponseSize {
		body = body[:MaxResponseSize]
	}
	return strings.TrimSpace(string(body))
}
