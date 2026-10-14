// The long-running half of rigd: ONE outbound /ws/connector socket that registers every local session,
// uploads its redacted events stop-and-wait, publishes safeguard transitions, and reconnects with
// backoff. It extends the connector in connector.go - it is not a second wire.
//
// THE RULES THIS FILE EXISTS TO KEEP, each from ai_docs/core-architecture/rigd-boundaries.md:
//
//   - 2.2 STOP-AND-WAIT. Exactly one `events` frame per session is outstanding at a time, and its
//     `events_ack` is read before the next one is built. That ordering is what makes the cursor exact:
//     the cursor and the events commit in ONE transaction on the server, so the acked value is the only
//     position this client may resume from.
//   - 2.2 RECONNECT. Exponential backoff from 1 s, doubling, capped at 60 s, +/-20 % jitter, reset after
//     60 s connected. A close with 1008 (scope missing) or 4401 (authentication) is NOT retried: the
//     caller reports it and exits non-zero, because retrying a refused credential is load, not recovery.
//   - 2.2 HEARTBEAT. A `ping` every ready.heartbeat_s seconds (25 by default); three heartbeats with no
//     `pong`, or with no frame at all, close the socket and reconnect.
//   - 2.3 FORWARD COMPATIBILITY. An unknown server frame kind is logged once per kind and otherwise
//     ignored: it never closes the socket, never retries and never causes an action.
//   - 7.3/7.6 SAFEGUARD FRAMES. A `safeguard` frame is sent on every state change and every row is
//     re-sent after each `ready` - but ONLY once ready echoed the `safeguards` capability.
//   - 4.4 FAIL CLOSED. Every event is redacted before it is wrapped in a frame, and an event that cannot
//     be proven clean is withheld as the ruled placeholder. If EVERY event of a session is withheld for
//     five minutes, that session's upload stops and it is reported locally.
package clientsync

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

const (
	// rigdTick is one pass of the socket loop: publish what changed, register what appeared, send what is
	// new. It is small because the tail's cost is one transcript read per session.
	rigdTick = time.Second
	// rigdAckTimeout is how long one stop-and-wait acknowledgement may take before the socket is declared
	// dead. It is not a retry: a socket that cannot answer one frame inside this window is one the
	// heartbeat's 3x rule would close in the same breath.
	rigdAckTimeout = 30 * time.Second
	// rigdBackoffStart, rigdBackoffCap and rigdConnectedReset are 2.2's reconnect curve.
	rigdBackoffStart   = time.Second
	rigdBackoffCap     = 60 * time.Second
	rigdConnectedReset = 60 * time.Second
	// rigdJitter is the +/-20 % of 2.2.
	rigdJitter = 0.2

	// transcriptTail is the client's own backfill window: the lines a tail read returns. It is the source
	// of the cursor's coordinate, so it is one constant and not a per-call number.
	transcriptTail = 5000
	// backfillLimit bounds what a FIRST registration uploads: a cloud that has no cursor for a session
	// gets the newest lines, not the whole history (2.2: "bounded by the client's own backfill limit").
	backfillLimit = 500
	// captureLines is the window a capture read takes for a session whose transcript is empty (a seat
	// whose runtime renders to an alternate screen has no scrollback to read). The runtime's own default
	// is the same number, so a caller that asks for nothing gets the same answer as this caller.
	captureLines = 200
	// withheldBackstop is 4.4's last resort: every event withheld for this long means a broken redactor,
	// not one bad line, so the session's upload stops.
	withheldBackstop = 5 * time.Minute
	// withheldKeepDays is how long a withheld event's raw copy is kept locally (4.4).
	withheldKeepDays = 7
)

// sleep is a variable so the reconnect curve is testable without waiting it out, the same seam
// clientrestore uses for its poll interval.
var sleep = time.Sleep

// RigDaemonOptions is one rigd run, as the process's other half supplies it.
type RigDaemonOptions struct {
	BaseURL     string
	Token       string
	ConnectorID string
	// Log is where rigd's own lines go. It is one line per event, never a secret: redaction runs before
	// anything is logged from an event.
	Log func(string)
	// Rows is every row the supervision half currently reports, for the re-send after each `ready` (7.3).
	Rows func() []clientservices.Health
	// Pending TAKES the state changes since the last call: latest wins per (safeguard, rig), which is the
	// frame's own contract - no seq, no cursor, no ack.
	Pending func() []clientservices.Health

	// AllowRemoteCommands enables opt-in execution of commands from /ws/connector (D4/F5)
	AllowRemoteCommands bool
	AllowedRooms        []string
	OnCommand           func(ctx context.Context, action, room, seat string, args map[string]any) (string, error)
}

// sessionStream is one local session's position in its source.
//
// acked is the CURSOR THE SERVER CONFIRMED, in lines of the transcript read window: it is set from
// session_ack on every connect and from events_ack after every batch, so a lost ack costs nothing - the
// next session_ack carries the stored value and the client resumes from that.
type sessionStream struct {
	key    string
	name   string
	room   string
	seat   string
	source string

	registered bool
	lastState  string
	acked      int
	suspended  bool

	withheldSince time.Time
}

// RigDaemon is the socket half of rigd. Every external read is a field so a test drives the whole loop
// with a scripted socket and no rig, no cloud and no waiting.
type RigDaemon struct {
	opts RigDaemonOptions

	// The local reads, injected so a test never shells out.
	Sessions   func(ctx context.Context) ([]sessionCandidate, error)
	Transcript func(ctx context.Context, session string) (string, error)
	Capture    func(ctx context.Context, session string) (string, error)
	Now        func() time.Time

	redactor *EventRedactor

	sessions map[string]*sessionStream
	unknown  map[string]bool

	sock       ConnectorSocket
	ready      Ready
	heartbeatS int
	lastFrame  time.Time
	lastPong   time.Time
	lastPing   time.Time
	connected  time.Time
}

// NewRigDaemon wires the real world: the daemon reads the rig tool and the clock, and holds one redactor
// for the whole stream, because the .env rule pairs a tool call with its result ACROSS events.
func NewRigDaemon(opts RigDaemonOptions) *RigDaemon {
	daemon := &RigDaemon{
		opts:       opts,
		Now:        time.Now,
		redactor:   NewEventRedactor(),
		sessions:   map[string]*sessionStream{},
		unknown:    map[string]bool{},
		heartbeatS: defaultHeartbeatS,
	}
	if daemon.opts.Log == nil {
		daemon.opts.Log = func(string) {}
	}
	if daemon.opts.Rows == nil {
		daemon.opts.Rows = func() []clientservices.Health { return nil }
	}
	if daemon.opts.Pending == nil {
		daemon.opts.Pending = func() []clientservices.Health { return nil }
	}
	// THE THREE LOCAL READS GO THROUGH THE RUNTIME'S OWN SOCKET. They were execs of `rig ps --json`,
	// `rig transcript` and `rig capture`; the last two are not verbs the installed client has at all
	// ("4genteam: unknown command transcript"), so wherever `rig` resolved to that client rigd could not
	// read a transcript or a pane. The session list is the runtime's, which answers one entry per seat of
	// every room it hosts - the same set `--nodes --all-rigs` was asked for, from the daemon that owns it.
	daemon.Sessions = rigSessionCandidates
	daemon.Transcript = func(ctx context.Context, session string) (string, error) {
		answer, err := clientruntime.NewClient("").Transcript(session, transcriptTail)
		if err != nil {
			return "", err
		}
		return answer.Content, nil
	}
	daemon.Capture = func(ctx context.Context, session string) (string, error) {
		answer, err := clientruntime.NewClient("").Capture(session, captureLines)
		if err != nil {
			return "", err
		}
		return answer.Content, nil
	}
	return daemon
}

// Run keeps the socket up until the context ends, and returns the process's exit code. A refused
// credential is ExitUnavailable: the verb ran, and this machine cannot do it with this token.
func (d *RigDaemon) Run(ctx context.Context) int {
	backoff := rigdBackoffStart
	for {
		if ctx.Err() != nil {
			return clientcmd.ExitOK
		}
		err := d.connectAndServe(ctx)
		if ctx.Err() != nil {
			return clientcmd.ExitOK
		}
		var closed *ConnectorClosed
		if errors.As(err, &closed) && closed.Refused() {
			d.opts.Log(fmt.Sprintf("the connector refused this credential (%s): rigd is NOT retrying. Check the token's scope and value, then run `4genteam up` again", closed.Error()))
			return clientcmd.ExitUnavailable
		}
		d.opts.Log(fmt.Sprintf("the connector socket ended: %v", err))

		// 2.2: the backoff resets after 60 s connected. A socket that stayed up is a working credential
		// and a working network, so the next failure starts the curve over rather than at its cap.
		if !d.connected.IsZero() && d.Now().Sub(d.connected) >= rigdConnectedReset {
			backoff = rigdBackoffStart
		}
		wait := jitter(backoff)
		d.opts.Log(fmt.Sprintf("reconnecting in %s", wait.Round(time.Millisecond)))
		sleep(wait)
		backoff *= 2
		if backoff > rigdBackoffCap {
			backoff = rigdBackoffCap
		}
	}
}

// jitter is 2.2's +/-20 %, so a fleet returning after an outage does not arrive in one wave.
func jitter(d time.Duration) time.Duration {
	spread := float64(d) * rigdJitter
	return time.Duration(float64(d) - spread + rand.Float64()*2*spread)
}

// connectAndServe opens one socket and serves it until it fails or the context ends.
func (d *RigDaemon) connectAndServe(ctx context.Context) error {
	sock, err := dialConnector(ctx, d.opts.BaseURL, d.opts.Token)
	if err != nil {
		return err
	}
	d.sock = sock
	defer func() { _ = sock.Close() }()

	now := d.Now()
	d.connected, d.lastFrame, d.lastPong, d.lastPing = now, now, now, now
	// A reconnect re-registers every session: the server marks a connector's sessions offline when the
	// socket closes, and the registration is what puts them back (2.2).
	for _, stream := range d.sessions {
		stream.registered = false
	}

	ready, err := d.hello()
	if err != nil {
		return err
	}
	d.ready = ready
	d.heartbeatS = ready.HeartbeatS
	d.opts.Log(fmt.Sprintf("connected as %s: protocol %d, heartbeat %ds, capabilities %v",
		d.opts.ConnectorID, ready.Protocol, ready.HeartbeatS, ready.Capabilities))

	if err := d.resendAll(ctx); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := d.pass(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(rigdTick):
		}
	}
}

// hello is the first exchange: 2.2's protocol, version and capabilities, and the server's ready. It goes
// through the socket interface rather than the concrete type, so a scripted socket exercises the same
// frame the real one writes.
func (d *RigDaemon) hello() (Ready, error) {
	caps := []string{CapabilityIngest, CapabilitySafeguards}
	if d.opts.AllowRemoteCommands {
		caps = append(caps, CapabilityCommands)
	}
	if err := d.sock.WriteFrame(helloFrame(d.opts.ConnectorID, ClientVersion, caps)); err != nil {
		return Ready{}, err
	}
	frame, err := d.expect("ready")
	if err != nil {
		return Ready{}, err
	}
	if frame["connector_id"] != d.opts.ConnectorID {
		return Ready{}, fmt.Errorf("hello was answered %v", frame)
	}
	return readyOf(frame), nil
}

// resendAll is what happens after EVERY ready: the sessions are registered again and every safeguard row
// is sent, because a transition that happened while the socket was down still has to arrive (7.3).
func (d *RigDaemon) resendAll(ctx context.Context) error {
	candidates, err := d.Sessions(ctx)
	if err != nil {
		// A rig that cannot be read is not a dead socket: the socket is what the credential wants, and
		// the next pass reads the rigs again.
		d.opts.Log(fmt.Sprintf("cannot read the local sessions: %v", err))
	}
	for _, candidate := range candidates {
		stream := d.streamFor(candidate)
		if err := d.register(stream, SessionRunning); err != nil {
			return err
		}
	}
	return d.publishRows(d.opts.Rows())
}

// pass is one tick: drain what the server sent, keep the heartbeat, publish what changed, then register
// and tail the local sessions.
func (d *RigDaemon) pass(ctx context.Context) error {
	if err := d.drain(); err != nil {
		return err
	}
	if err := d.keepAlive(); err != nil {
		return err
	}
	if err := d.publishRows(d.opts.Pending()); err != nil {
		return err
	}
	candidates, err := d.Sessions(ctx)
	if err != nil {
		d.opts.Log(fmt.Sprintf("cannot read the local sessions: %v", err))
		return nil
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.name] = true
		stream := d.streamFor(candidate)
		state := candidateState(candidate)
		if !stream.registered || stream.lastState != state {
			if err := d.register(stream, state); err != nil {
				return err
			}
		}
		if state != SessionRunning {
			continue
		}
		if err := d.ingest(ctx, stream); err != nil {
			return err
		}
	}
	// A seat that leaves `rig ps` is sent ONCE with state stopped (2.2), and then it is not tailed again.
	for key, stream := range d.sessions {
		if seen[key] || stream.lastState == SessionStopped {
			continue
		}
		if err := d.register(stream, SessionStopped); err != nil {
			return err
		}
	}
	return nil
}

// streamFor is the per-session state, created on first sight. Its source starts as the transcript: the
// capture fallback is decided by what the transcript read returns, per session.
func (d *RigDaemon) streamFor(candidate sessionCandidate) *sessionStream {
	stream := d.sessions[candidate.name]
	if stream == nil {
		stream = &sessionStream{
			key: candidate.name, name: candidate.name,
			room: candidate.rigName, seat: candidate.member,
			source: SourceTranscript,
		}
		d.sessions[candidate.name] = stream
	}
	return stream
}

// register sends one `session` frame and takes the server's stored cursor, which is the ONLY position
// this client may resume from (2.1).
func (d *RigDaemon) register(stream *sessionStream, state string) error {
	frame := sessionFrame(SessionRegistration{
		Key: stream.key, Name: stream.name, Room: stream.room, Seat: stream.seat,
		State: state, Source: stream.source,
	})
	if err := d.sock.WriteFrame(frame); err != nil {
		return err
	}
	answer, err := d.expect("session_ack")
	if err != nil {
		return err
	}
	stream.registered, stream.lastState = true, state
	if cursor, ok := answer["cursor"].(string); ok && cursor != "" {
		if value, err := strconv.Atoi(cursor); err == nil && value >= 0 {
			// The stored cursor is authoritative: it was committed with the events it describes.
			if value != stream.acked {
				stream.acked = value
			}
		}
	}
	return nil
}

// ingest reads one session's source and uploads what is new, ONE batch per call.
//
// The cursor is a line count in the transcript read window. A window that cannot be aligned with the
// stored cursor - the transcript grew past the tail between two reads, so the window slid - is reported
// and restarted from the window's start: an unanswered resume is a gap, and a gap this client CHOOSES to
// re-send is visible in the stream, while one it hides is not.
func (d *RigDaemon) ingest(ctx context.Context, stream *sessionStream) error {
	if stream.suspended {
		return nil
	}
	text, err := d.Transcript(ctx, stream.key)
	if err != nil || strings.TrimSpace(text) == "" {
		text, err = d.Capture(ctx, stream.key)
		if err != nil {
			return nil
		}
		stream.source = SourceCapture
	}
	lines := transcriptLines(text)
	if stream.acked > len(lines) {
		d.opts.Log(fmt.Sprintf("%s: the transcript window slid past the stored cursor (%d > %d lines); resuming from the window's start, so up to %d lines may arrive twice",
			stream.key, stream.acked, len(lines), len(lines)))
		stream.acked = 0
	}
	if stream.acked == 0 && len(lines) > backfillLimit {
		// The FIRST upload is bounded by the client's own backfill limit (2.2); the reported cursor is
		// then the position of the line after the ones actually sent.
		stream.acked = len(lines) - backfillLimit
	}
	pending := lines[stream.acked:]
	if len(pending) == 0 {
		return nil
	}
	batch := pending
	if len(batch) > MaxEventsPerBatch {
		batch = batch[:MaxEventsPerBatch]
	}
	events, withheld := d.buildEvents(stream, batch)
	cursor := strconv.Itoa(stream.acked + len(batch))
	if err := d.sock.WriteFrame(map[string]any{
		"type": "events", "session_key": stream.key, "events": events, "cursor": cursor,
	}); err != nil {
		return err
	}
	ack, err := d.expect("events_ack")
	if err != nil {
		return err
	}
	// The ack's cursor is the value the server STORED, so it is what the next batch continues from even
	// if our own count and the server's disagreed.
	stream.acked = stream.acked + len(batch)
	if echoed, ok := ack["cursor"].(string); ok && echoed != "" {
		if value, err := strconv.Atoi(echoed); err == nil && value >= 0 {
			stream.acked = value
		}
	}
	d.noteWithheld(stream, withheld, len(batch))
	return nil
}

// buildEvents redacts every line of one batch. It returns the events and how many of them were withheld,
// and it is the ONLY place an event is built, so no path can put an unredacted byte into a frame (4.1).
func (d *RigDaemon) buildEvents(stream *sessionStream, batch []string) ([]map[string]any, int) {
	events := make([]map[string]any, 0, len(batch))
	withheldCount := 0
	for _, line := range batch {
		event, reason := d.redactor.Event(line)
		if reason != "" {
			withheldCount++
			d.writeWithheld(stream, line)
		}
		if event == nil {
			// The redactor is the one path that could hand back nothing; a nil event is a defect in it,
			// and the placeholder is still what goes out.
			event = withheldEvent(reasonRedactorError, len(line))
			withheldCount++
		}
		events = append(events, event)
	}
	return events, withheldCount
}

// noteWithheld is 4.4's backstop: a session whose EVERY event is withheld for five minutes is a broken
// redactor rather than one bad line, so its upload stops and rigd says so locally.
//
// THE COMPARISON IS THE RULE. A batch with ONE withheld line among many is the ordinary case the per-event
// rule exists for - the gap is uploaded as a placeholder and the stream continues - so the window only
// starts, and only keeps running, while NOTHING in the batch could be uploaded. A single bad line repeated
// for five minutes must not take a healthy session's stream down.
func (d *RigDaemon) noteWithheld(stream *sessionStream, withheld, total int) {
	if total == 0 || withheld < total {
		stream.withheldSince = time.Time{}
		return
	}
	now := d.Now()
	if stream.withheldSince.IsZero() {
		stream.withheldSince = now
		return
	}
	if now.Sub(stream.withheldSince) >= withheldBackstop {
		stream.suspended = true
		d.opts.Log(fmt.Sprintf("%s: every event has been withheld for %s; stopping this session's upload - the redactor is broken, not one line",
			stream.key, withheldBackstop))
	}
}

// writeWithheld keeps the raw event locally, which is the ONLY place it may exist once it is withheld:
// <seat store>/rigd-withheld/<session>/<ts>.json, mode 0600, kept seven days (4.4). The store is the
// package's existing `seatStore` seam (statusverb.go), so a test withholds into a scratch tree rather than
// into the machine's own store.
func (d *RigDaemon) writeWithheld(stream *sessionStream, line string) {
	store, err := seatStore("")
	if err != nil {
		return
	}
	dir := filepath.Join(store, "rigd-withheld", safeFileName(stream.key))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	d.pruneWithheld(dir)
	stamp := d.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.WriteFile(filepath.Join(dir, stamp+".json"), []byte(line), 0o600); err != nil {
		return
	}
}

// pruneWithheld drops the raw copies past their seven days. It runs on the write path, because that is
// the only moment this process touches the directory.
func (d *RigDaemon) pruneWithheld(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := d.Now().Add(-withheldKeepDays * 24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// safeFileName is a session name as one path component: a session key is already a rig-qualified name,
// and everything that is not one of those characters is replaced rather than escaped, so no key can name
// a path outside the store.
func safeFileName(key string) string {
	var out strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	if out.Len() == 0 {
		return "session"
	}
	return out.String()
}

// publishRows sends one `safeguard` frame per row, and ONLY after ready echoed the capability (7.6): a
// server without it would answer `unknown_type` and keep the socket open, so sending would be noise at
// best and, on a later server, a frame with a meaning this client cannot know.
func (d *RigDaemon) publishRows(rows []clientservices.Health) error {
	if len(rows) == 0 {
		return nil
	}
	if !d.ready.Has(CapabilitySafeguards) {
		return nil
	}
	for _, row := range rows {
		frame := map[string]any{
			"type":      "safeguard",
			"rig":       row.Rig,
			"safeguard": row.Safeguard,
			"state":     row.State,
			"restarts":  row.Restarts,
			"age_s":     row.AgeS,
			"at":        d.Now().UTC().Format(time.RFC3339),
		}
		// pid is optional in the frame (7.3): a row whose process is gone has no pid to name, and a zero
		// would read as a process at pid 0.
		if row.PID > 0 {
			frame["pid"] = row.PID
		}
		if err := d.sock.WriteFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

// keepAlive is 2.2's heartbeat in both directions: ping every heartbeat_s, and close when three
// heartbeats pass with no frame at all.
func (d *RigDaemon) keepAlive() error {
	now := d.Now()
	interval := time.Duration(d.heartbeatS) * time.Second
	if now.Sub(d.lastFrame) > 3*interval {
		return fmt.Errorf("no frame from the server in %s (three heartbeats); reconnecting", 3*interval)
	}
	if now.Sub(d.lastPing) < interval {
		return nil
	}
	d.lastPing = now
	// The ping's pong is also the server's dead-man on rigd (7.3): three of these without an answer, or
	// any frame at all, and this socket is replaced.
	return d.sock.WriteFrame(map[string]any{"type": "ping", "t": now.UnixMilli()})
}

// drain reads every frame already waiting, with a deadline so it never blocks the tick. It handles the
// three kinds a client must: `pong` (the heartbeat), `error` (which may be fatal), and an unknown kind,
// which is logged once and otherwise ignored (2.3).
func (d *RigDaemon) drain() error {
	for {
		_ = d.sock.SetDeadline(d.Now().Add(time.Millisecond))
		frame, err := d.sock.ReadFrame()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return nil
			}
			return err
		}
		d.lastFrame = d.Now()
		if err := d.handle(frame); err != nil {
			return err
		}
	}
}

// expect reads frames until the wanted kind arrives, tolerating pongs and unknown kinds in between. One
// `events` frame is outstanding at a time, so the frame this returns is the acks one - and the deadline
// is what stops a silent server from freezing the loop.
func (d *RigDaemon) expect(kind string) (map[string]any, error) {
	deadline := d.Now().Add(rigdAckTimeout)
	for {
		_ = d.sock.SetDeadline(deadline)
		frame, err := d.sock.ReadFrame()
		if err != nil {
			if kind == "ready" {
				// The ready is answered by hello, whose caller reports the failure in its own words.
			}
			return nil, err
		}
		d.lastFrame = d.Now()
		if frame["type"] == kind {
			return frame, nil
		}
		if err := d.handle(frame); err != nil {
			return nil, err
		}
	}
}

// handle is what a frame that is NOT the one being waited for means.
func (d *RigDaemon) handle(frame map[string]any) error {
	kind, _ := frame["type"].(string)
	switch kind {
	case "pong":
		d.lastPong = d.Now()
		return nil
	case "ping":
		// A JSON ping is not this protocol's; the wire-level ping is answered in the frame reader. Ignore
		// it rather than inventing a json pong.
		return nil
	case "error":
		message, _ := frame["message"].(string)
		code, _ := frame["code"].(string)
		if fatal, _ := frame["fatal"].(bool); fatal {
			return fmt.Errorf("the server refused the frame (code %q): %s", code, message)
		}
		d.opts.Log(fmt.Sprintf("the server reported an error (code %q): %s", code, message))
		return nil
	case "command":
		return d.handleRemoteCommand(frame)
	case "":
		d.opts.Log("the server sent a frame with no type; ignoring it")
		return nil
	default:
		if !d.unknown[kind] {
			d.unknown[kind] = true
			d.opts.Log(fmt.Sprintf("the server sent an unknown frame type %q; ignoring it (this is logged once)", kind))
		}
		return nil
	}
}

// handleRemoteCommand evaluates security guardrails and executes opt-in remote commands (D4/F5).
func (d *RigDaemon) handleRemoteCommand(frame map[string]any) error {
	cmdID, _ := frame["command_id"].(string)
	issuerKind, _ := frame["issuer_kind"].(string)
	scope, _ := frame["token_scope"].(string)
	action, _ := frame["action"].(string)
	room, _ := frame["room"].(string)
	seat, _ := frame["seat"].(string)
	args, _ := frame["args"].(map[string]any)

	refusalCode := ""
	refusalMsg := ""

	if !d.opts.AllowRemoteCommands {
		refusalCode = "REMOTE_COMMANDS_DISABLED"
		refusalMsg = "Remote execution via connector is disabled by local configuration"
	} else if issuerKind != "human" {
		refusalCode = "UNAUTHORIZED_ISSUER"
		refusalMsg = fmt.Sprintf("Issuer %q not permitted; only human operators may issue commands", issuerKind)
	} else if !strings.Contains(scope, "sessions:command") {
		refusalCode = "SCOPE_INSUFFICIENT"
		refusalMsg = "Token scope must include 'sessions:command'"
	} else if action != "send_to_seat" && action != "up" && action != "down" {
		refusalCode = "DISALLOWED_COMMAND"
		refusalMsg = fmt.Sprintf("Action %q is not in the allowed command whitelist", action)
	} else if len(d.opts.AllowedRooms) > 0 {
		allowed := false
		for _, r := range d.opts.AllowedRooms {
			if r == room {
				allowed = true
				break
			}
		}
		if !allowed {
			refusalCode = "UNRECOGNIZED_ROOM"
			refusalMsg = fmt.Sprintf("Room %q is not in the allowed rooms whitelist", room)
		}
	}

	resultFrame := map[string]any{
		"type":       "command_result",
		"command_id": cmdID,
	}

	if refusalCode != "" {
		resultFrame["success"] = false
		resultFrame["refusal_code"] = refusalCode
		resultFrame["message"] = refusalMsg
		if d.opts.Log != nil {
			d.opts.Log(fmt.Sprintf("[remote-command:refused] id=%s issuer=%s action=%s room=%s/%s code=%s msg=%s",
				cmdID, issuerKind, action, room, seat, refusalCode, refusalMsg))
		}
		return d.sock.WriteFrame(resultFrame)
	}

	// Execute command via OnCommand callback
	var out string
	var execErr error
	if d.opts.OnCommand != nil {
		out, execErr = d.opts.OnCommand(context.Background(), action, room, seat, args)
	} else {
		refusalCode = "NO_EXECUTOR"
		refusalMsg = "No local command executor configured"
		resultFrame["success"] = false
		resultFrame["refusal_code"] = refusalCode
		resultFrame["message"] = refusalMsg
		return d.sock.WriteFrame(resultFrame)
	}

	if execErr != nil {
		resultFrame["success"] = false
		resultFrame["message"] = execErr.Error()
		if d.opts.Log != nil {
			d.opts.Log(fmt.Sprintf("[remote-command:error] id=%s action=%s room=%s/%s err=%v",
				cmdID, action, room, seat, execErr))
		}
	} else {
		resultFrame["success"] = true
		resultFrame["output"] = out
		if d.opts.Log != nil {
			d.opts.Log(fmt.Sprintf("[remote-command:success] id=%s action=%s room=%s/%s",
				cmdID, action, room, seat))
		}
	}

	return d.sock.WriteFrame(resultFrame)
}

// transcriptLines is the source's text as lines. A trailing newline is not a line.
func transcriptLines(text string) []string {
	if strings.TrimRight(text, "\n") == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}

// candidateState maps a node record's status onto 2.2's two values. The vocabulary is deliberately
// reduced: `session.state` is running or stopped, and a status this client does not know is reported as
// stopped rather than as a running seat that may not exist.
func candidateState(candidate sessionCandidate) string {
	if candidate.state == SessionRunning {
		return SessionRunning
	}
	return SessionStopped
}
