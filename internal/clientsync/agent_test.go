// The agent socket daemon's own tests, driven entirely through the seams agent.go exposes: a scripted
// ConnectorSocket, the injected local reads (Sessions, Transcript, Capture, Now), the `dialConnector`
// and `sleep` package variables, and the daemon's own unexported methods. NO test in this file touches
// the machine: nothing shells out, and the one test that withholds an event points `seatStore` at a
// temp dir. Every clock read goes through the injected `Now` field, so no wall-clock call appears here.
package clientsync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// fakeTimeout is the "nothing waiting" reading of the scripted socket. drain and expect both treat a
// net.Error whose Timeout is true as an empty queue, so the fake returns exactly that rather than
// io.EOF, which would read as a dead socket.
type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "fake socket: no frame waiting" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

var _ net.Error = fakeTimeout{}

// fakeSocket is ONE ConnectorSocket for every case: it records every frame the daemon writes in order,
// answers a write with a queued response (so a session_ack or events_ack is not eaten by an earlier
// drain), and returns unsolicited frames from a scripted read queue. An empty read queue is a timeout.
type fakeSocket struct {
	written []map[string]any
	reads   []any            // scripted read results: frames (map[string]any) or errors
	answers map[string][]any // responses to enqueue when a frame of this type is written
	closed  bool
	lastDL  time.Time
}

func (f *fakeSocket) WriteFrame(v any) error {
	frame, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("fake socket: the daemon wrote a value that is not a frame: %T", v)
	}
	f.written = append(f.written, frame)
	kind, _ := frame["type"].(string)
	if queue := f.answers[kind]; len(queue) > 0 {
		f.reads = append(f.reads, queue[0])
		f.answers[kind] = queue[1:]
	}
	return nil
}

func (f *fakeSocket) ReadFrame() (map[string]any, error) {
	if len(f.reads) == 0 {
		return nil, fakeTimeout{}
	}
	item := f.reads[0]
	f.reads = f.reads[1:]
	switch value := item.(type) {
	case map[string]any:
		return value, nil
	case error:
		return nil, value
	default:
		return nil, fmt.Errorf("fake socket: scripted read is neither a frame nor an error: %T", item)
	}
}

func (f *fakeSocket) SetDeadline(t time.Time) error { f.lastDL = t; return nil }
func (f *fakeSocket) Close() error                  { f.closed = true; return nil }

// answer queues a response the next write of `kind` will deliver; script queues an unsolicited read.
func (f *fakeSocket) answer(kind string, items ...any) {
	if f.answers == nil {
		f.answers = map[string][]any{}
	}
	f.answers[kind] = append(f.answers[kind], items...)
}

func (f *fakeSocket) script(items ...any) { f.reads = append(f.reads, items...) }

func (f *fakeSocket) framesOfType(kind string) []map[string]any {
	var out []map[string]any
	for _, frame := range f.written {
		if frame["type"] == kind {
			out = append(out, frame)
		}
	}
	return out
}

func (f *fakeSocket) countOfType(kind string) int { return len(f.framesOfType(kind)) }

// fakeClock is the moving time the daemon reads through its exported Now field.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// harness wires one daemon to a fake socket, a fake clock, a captured log and no local reads.
type harness struct {
	t     *testing.T
	d     *AgentDaemon
	sock  *fakeSocket
	clock *fakeClock
	logs  []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		sock:  &fakeSocket{},
		clock: &fakeClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)},
	}
	d := NewAgentDaemon(AgentDaemonOptions{
		ConnectorID: "connector-1",
		Log:         func(line string) { h.logs = append(h.logs, line) },
	})
	d.Now = h.clock.now
	d.Sessions = func(context.Context) ([]sessionCandidate, error) { return nil, nil }
	d.Transcript = func(context.Context, string) (string, error) { return "", nil }
	d.Capture = func(context.Context, string) (string, error) { return "", nil }
	d.sock = h.sock
	now := h.clock.now()
	d.lastFrame, d.lastPong, d.lastPing, d.connected = now, now, now, now
	h.d = d
	return h
}

func (h *harness) logsContaining(sub string) int {
	count := 0
	for _, line := range h.logs {
		if strings.Contains(line, sub) {
			count++
		}
	}
	return count
}

func readyFrame(connectorID string, caps ...string) map[string]any {
	capabilities := make([]any, len(caps))
	for i, c := range caps {
		capabilities[i] = c
	}
	return map[string]any{
		"type": "ready", "connector_id": connectorID,
		"protocol": float64(ClientProtocol), "heartbeat_s": float64(1), "capabilities": capabilities,
	}
}

func sessionAck(sessionID, cursor string) map[string]any {
	return map[string]any{"type": "session_ack", "session_id": sessionID, "last_seq": float64(0), "cursor": cursor}
}

func eventsAck(sessionID, cursor string) map[string]any {
	return map[string]any{"type": "events_ack", "session_id": sessionID, "last_seq": float64(0), "cursor": cursor}
}

func newCandidate(name, room, member, state string) sessionCandidate {
	return sessionCandidate{name: name, roomName: room, member: member, state: state}
}

func keysOf(frame map[string]any) []string {
	out := make([]string, 0, len(frame))
	for key := range frame {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Case 1: hello is 2.2's first frame, field by field, and the daemon's ready follows what the server
// echoed for the safeguards capability.
func TestHelloCarriesProtocolVersionAndCapabilities(t *testing.T) {
	for _, withSafeguards := range []bool{true, false} {
		t.Run(fmt.Sprintf("safeguards_echoed_%t", withSafeguards), func(t *testing.T) {
			h := newHarness(t)
			caps := []string{CapabilityIngest}
			if withSafeguards {
				caps = append(caps, CapabilitySafeguards)
			}
			h.sock.answer("hello", readyFrame("connector-1", caps...))

			ready, err := h.d.hello()
			if err != nil {
				t.Fatalf("hello: %v", err)
			}
			if len(h.sock.written) != 1 {
				t.Fatalf("hello wrote %d frames, want 1", len(h.sock.written))
			}
			wrote := h.sock.written[0]
			if wrote["type"] != "hello" {
				t.Errorf("type = %v, want hello", wrote["type"])
			}
			if wrote["connector_id"] != "connector-1" {
				t.Errorf("connector_id = %v, want connector-1", wrote["connector_id"])
			}
			if wrote["protocol"] != ClientProtocol {
				t.Errorf("protocol = %v, want %d", wrote["protocol"], ClientProtocol)
			}
			if wrote["client_version"] != ClientVersion {
				t.Errorf("client_version = %v, want %q", wrote["client_version"], ClientVersion)
			}
			got, _ := wrote["capabilities"].([]string)
			want := []string{CapabilityIngest, CapabilitySafeguards}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("capabilities = %v, want %v", got, want)
			}
			if ready.Has(CapabilitySafeguards) != withSafeguards {
				t.Errorf("ready.Has(safeguards) = %t, want %t (echoed capability)", ready.Has(CapabilitySafeguards), withSafeguards)
			}
		})
	}
}

// Case 2 (7.6): a safeguard frame is sent ONLY once ready echoed the capability, and its field set is
// the ruled one, with pid present only when the row names a live pid.
func TestSafeguardFramesOnlyAfterReadyEchoesTheCapability(t *testing.T) {
	rows := []clientservices.Health{
		{Room: "room-a", Safeguard: "compact", State: "running", PID: 4321, Restarts: 2, AgeS: 1},
		{Room: "room-a", Safeguard: "bridge", State: "stopped", Restarts: 5, AgeS: 900},
	}

	h := newHarness(t)
	h.d.ready = readyOf(readyFrame("connector-1", CapabilityIngest))
	if err := h.d.publishRows(rows); err != nil {
		t.Fatalf("publishRows without the capability: %v", err)
	}
	if n := h.sock.countOfType("safeguard"); n != 0 {
		t.Fatalf("safeguard frames written without the echoed capability = %d, want 0", n)
	}

	h.d.ready = readyOf(readyFrame("connector-1", CapabilityIngest, CapabilitySafeguards))
	if err := h.d.publishRows(rows); err != nil {
		t.Fatalf("publishRows with the capability: %v", err)
	}
	frames := h.sock.framesOfType("safeguard")
	if len(frames) != 2 {
		t.Fatalf("safeguard frames = %d, want 2", len(frames))
	}
	withPID := []string{"age_s", "at", "pid", "restarts", "room", "safeguard", "state", "type"}
	withoutPID := []string{"age_s", "at", "restarts", "room", "safeguard", "state", "type"}
	if got := keysOf(frames[0]); !reflect.DeepEqual(got, withPID) {
		t.Errorf("frame[0] keys = %v, want %v", got, withPID)
	}
	if got := keysOf(frames[1]); !reflect.DeepEqual(got, withoutPID) {
		t.Errorf("frame[1] keys = %v, want %v", got, withoutPID)
	}
	if frames[0]["room"] != "room-a" || frames[0]["safeguard"] != "compact" || frames[0]["state"] != "running" {
		t.Errorf("frame[0] = %v, want the row's room, safeguard and state", frames[0])
	}
	if frames[0]["restarts"] != 2 || frames[0]["age_s"] != 1 || frames[0]["pid"] != 4321 {
		t.Errorf("frame[0] = %v, want restarts 2, age_s 1, pid 4321", frames[0])
	}
	if _, present := frames[1]["pid"]; present {
		t.Errorf("frame[1] carries pid for a row whose pid is zero: %v", frames[1])
	}
}

// Case 3: stop-and-wait, the cursor and the gap. After hello + resendAll the stored cursor is 5, one
// pass writes EXACTLY one events frame carrying the lines after 5 with the absolute cursor, and a
// second pass with no ack queued writes no second frame and does not fail.
func TestStopAndWaitCursorAndGap(t *testing.T) {
	h := newHarness(t)
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i+1)
	}
	h.d.Sessions = func(context.Context) ([]sessionCandidate, error) {
		return []sessionCandidate{newCandidate("dev-alice@room-a", "room-a", "alice", SessionRunning)}, nil
	}
	h.d.Transcript = func(context.Context, string) (string, error) {
		return strings.Join(lines, "\n") + "\n", nil
	}
	h.sock.answer("hello", readyFrame("connector-1", CapabilityIngest, CapabilitySafeguards))
	h.sock.answer("session", sessionAck("s1", "5"))
	h.sock.answer("events", eventsAck("s1", "12"))

	if _, err := h.d.hello(); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := h.d.resendAll(context.Background()); err != nil {
		t.Fatalf("resendAll: %v", err)
	}
	if got := h.d.sessions["dev-alice@room-a"].acked; got != 5 {
		t.Fatalf("stored cursor after session_ack = %d, want 5", got)
	}
	if err := h.d.pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}

	events := h.sock.framesOfType("events")
	if len(events) != 1 {
		t.Fatalf("events frames after one pass = %d, want exactly 1", len(events))
	}
	frame := events[0]
	if frame["cursor"] != "12" {
		t.Errorf("cursor = %v, want 12 (the absolute line count reached)", frame["cursor"])
	}
	batch, _ := frame["events"].([]map[string]any)
	if len(batch) != 7 {
		t.Fatalf("the batch has %d events, want the 7 lines after the cursor", len(batch))
	}
	for i, event := range batch {
		payload, _ := event["payload"].(map[string]any)
		if payload["text"] != lines[5+i] {
			t.Errorf("event %d text = %v, want %q", i, payload["text"], lines[5+i])
		}
	}

	// Nothing new to send and no ack in the queue: the second pass must add no frame and must not error.
	if err := h.d.pass(context.Background()); err != nil {
		t.Fatalf("second pass with no ack queued: %v", err)
	}
	if n := h.sock.countOfType("events"); n != 1 {
		t.Errorf("events frames after a second pass = %d, want 1 (stop-and-wait)", n)
	}
}

// driveOnePass registers one session with an empty stored cursor, tails `lines`, and returns the single
// events frame the pass wrote.
func driveOnePass(t *testing.T, h *harness, lines []string) map[string]any {
	t.Helper()
	h.d.Sessions = func(context.Context) ([]sessionCandidate, error) {
		return []sessionCandidate{newCandidate("dev-alice@room-a", "room-a", "alice", SessionRunning)}, nil
	}
	h.d.Transcript = func(context.Context, string) (string, error) {
		return strings.Join(lines, "\n") + "\n", nil
	}
	h.sock.answer("hello", readyFrame("connector-1", CapabilityIngest, CapabilitySafeguards))
	h.sock.answer("session", sessionAck("s1", ""))
	h.sock.answer("events", eventsAck("s1", strconv.Itoa(len(lines))))
	if _, err := h.d.hello(); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := h.d.resendAll(context.Background()); err != nil {
		t.Fatalf("resendAll: %v", err)
	}
	if err := h.d.pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	frames := h.sock.framesOfType("events")
	if len(frames) != 1 {
		t.Fatalf("events frames = %d, want 1", len(frames))
	}
	return frames[0]
}

// Case 4 (4.4): a withheld event uploads only the ruled placeholder, its reason is from the ruled set,
// and the batch's cursor still counts the withheld line - the placeholder records the gap.
func TestWithheldEventsStillAdvanceTheCursor(t *testing.T) {
	store := t.TempDir()
	original := seatStore
	seatStore = func(string) (string, error) { return store, nil }
	t.Cleanup(func() { seatStore = original })

	envLine := `{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"/srv/app/.env"}}`
	unsureLine := "the session token is Qw9_xK2mP7vB4nR8tY6uI3oL5aZ1sD0fH"

	ruled := map[string]bool{
		reasonRedactorError: true, reasonNotUTF8: true, reasonBadShape: true,
		reasonTooLarge: true, reasonEnvFile: true, reasonUnsure: true,
	}

	for _, tc := range []struct {
		name     string
		lines    []string
		wantIdx  int
		wantByte int
		want     []string
	}{
		{"env_file", []string{"a plain line", envLine}, 1, len(envLine), []string{reasonEnvFile}},
		{"unsure", []string{"a plain line", unsureLine}, 1, len(unsureLine), []string{reasonUnsure}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			frame := driveOnePass(t, h, tc.lines)

			batch, _ := frame["events"].([]map[string]any)
			if len(batch) != len(tc.lines) {
				t.Fatalf("the batch has %d events, want %d (the withheld line still counts)", len(batch), len(tc.lines))
			}
			if frame["cursor"] != strconv.Itoa(len(tc.lines)) {
				t.Errorf("cursor = %v, want %d (the withheld line still advances it)", frame["cursor"], len(tc.lines))
			}
			event := batch[tc.wantIdx]
			if event["type"] != withheldEventType {
				t.Fatalf("withheld event type = %v, want %s", event["type"], withheldEventType)
			}
			payload, _ := event["payload"].(map[string]any)
			reason, _ := payload["reason"].(string)
			if !ruled[reason] {
				t.Errorf("reason = %q, want one of the ruled set", reason)
			}
			if tc.name == "env_file" && reason != reasonEnvFile {
				t.Errorf("reason = %q, want %q for a .env tool call", reason, reasonEnvFile)
			}
			if tc.name == "unsure" && reason != reasonUnsure {
				t.Errorf("reason = %q, want %q for a high-entropy token", reason, reasonUnsure)
			}
			if payload["bytes"] != tc.wantByte {
				t.Errorf("payload bytes = %v, want %d", payload["bytes"], tc.wantByte)
			}
			for _, forbidden := range []string{"text", "content", "hash"} {
				if _, present := payload[forbidden]; present {
					t.Errorf("the placeholder carries %q; it must carry no content and no hash", forbidden)
				}
			}
		})
	}

	// The raw event is surfaced locally, under the temp store, and nowhere else.
	dir := filepath.Join(store, "agent-withheld", safeFileName("dev-alice@room-a"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("the withheld raw copy directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no withheld raw copy under %s", dir)
	}
}

// The withheld copy's two promises, which nothing held before this test (the agent gate's coverage gap,
// qitem-20261010234730). 4.4 says the raw event may exist in exactly one place once it is withheld -
// <seat store>/agent-withheld/<session>/<ts>.json, mode 0600, under a directory only its owner may enter -
// and that it is kept seven days. Both belong to the write path, and neither was asserted: the case above
// checks that the directory is not empty and stops there. The mode is the promise that the raw bytes are
// not readable by anyone else on the machine; the window is the promise that a redactor which has started
// withholding cannot fill the disk forever. A test that let either drift would let the protection fall
// over without a word.
func TestTheWithheldCopyIsPrivateAndKeptSevenDays(t *testing.T) {
	store := t.TempDir()
	original := seatStore
	seatStore = func(string) (string, error) { return store, nil }
	t.Cleanup(func() { seatStore = original })

	h := newHarness(t)
	stream := &sessionStream{key: "dev-alice@room-a"}
	dir := filepath.Join(store, "agent-withheld", safeFileName(stream.key))

	// A withheld event writes the copy, in a directory this user alone can enter.
	h.d.writeWithheld(stream, `{"type":"tool_result","id":"tu1"}`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("the withheld copy directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the withheld copy holds %d files, want 1", len(entries))
	}
	written := filepath.Join(dir, entries[0].Name())
	info, err := os.Stat(written)
	if err != nil {
		t.Fatalf("the withheld copy: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the withheld copy is mode %04o, want 0600: the raw event must not be readable by anyone else", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the withheld copy directory: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("the withheld directory is mode %04o, want 0700", perm)
	}

	// The prune runs on the write path, because writing is the only moment this process touches the
	// directory. Two copies an hour either side of the window, named in the writer's own timestamp shape so
	// that age is what decides them rather than their names.
	copyAged := func(name string, age time.Duration) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"type":"tool_result"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		at := h.d.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	inside := copyAged("20261010T120000.000000000Z.json", withheldKeepDays*24*time.Hour-time.Hour)
	past := copyAged("20261003T120000.000000000Z.json", withheldKeepDays*24*time.Hour+time.Hour)

	h.d.writeWithheld(stream, `{"type":"tool_result","id":"tu2"}`)

	if _, err := os.Stat(past); !os.IsNotExist(err) {
		t.Errorf("a copy an hour past %d days survived the write path (stat err = %v): the window is what keeps a withholding redactor from filling the disk", withheldKeepDays, err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("a copy an hour inside %d days was pruned: %v", withheldKeepDays, err)
	}

	// The cutoff instant itself is deliberately not asserted: it is computed from d.Now(), so pinning it to
	// the nanosecond would fail on a clock change rather than on a behaviour change. An hour on each side is
	// what a reader of 4.4 can check, and it fails the same way if the comparison is ever reversed.
}

// Case 4b (4.4's last paragraph): the five-minute backstop is about a session whose EVERY event is withheld.
// One bad line among many is the ordinary case the per-event rule exists for, and it must not take a
// healthy session's stream down; a batch that could be uploaded again clears the window.
func TestTheBackstopNeedsEveryEventWithheld(t *testing.T) {
	h := newHarness(t)

	// One withheld line in each batch, for well past five minutes: NOT a broken redactor.
	mixed := &sessionStream{key: "dev-alice@room-a"}
	h.d.noteWithheld(mixed, 1, 3)
	h.clock.advance(4 * time.Minute)
	h.d.noteWithheld(mixed, 1, 3)
	h.clock.advance(4 * time.Minute)
	h.d.noteWithheld(mixed, 2, 3)
	if mixed.suspended {
		t.Fatal("a session with one withheld line among many was suspended, and the ruling says EVERY event")
	}

	// EVERY event withheld and still every event five minutes later: the ruled backstop.
	broken := &sessionStream{key: "dev-bob@room-a"}
	h.d.noteWithheld(broken, 3, 3)
	h.clock.advance(4 * time.Minute)
	h.d.noteWithheld(broken, 3, 3)
	if broken.suspended {
		t.Fatal("the backstop fired before its five minutes")
	}
	h.clock.advance(2 * time.Minute)
	h.d.noteWithheld(broken, 3, 3)
	if !broken.suspended {
		t.Fatal("a session whose every event was withheld for five minutes was not stopped")
	}
	if got := h.logsContaining("every event has been withheld"); got != 1 {
		t.Errorf("agent reported the stopped session %d times, want exactly once: %v", got, h.logs)
	}

	// A batch that could be uploaded again clears the window: the clock restarts for the next stretch.
	recovering := &sessionStream{key: "dev-carol@room-a"}
	h.d.noteWithheld(recovering, 1, 1)
	h.clock.advance(4 * time.Minute)
	h.d.noteWithheld(recovering, 0, 1)
	h.clock.advance(4 * time.Minute)
	h.d.noteWithheld(recovering, 1, 1)
	if recovering.suspended {
		t.Error("a good batch did not clear the withheld window")
	}
}

// Case 5 (2.2): a close with 1008 or 4401 is NOT retried - one dial and ExitUnavailable - while a plain
// transport error reconnects with the doubling backoff, the sleeps recorded through the sleep seam.
func TestRefusedCredentialsAreNotRetried(t *testing.T) {
	for _, code := range []int{CloseCodePolicyViolation, CloseCodeAuthFailed} {
		t.Run(fmt.Sprintf("close_%d", code), func(t *testing.T) {
			originalDial, originalSleep := dialConnector, sleep
			t.Cleanup(func() { dialConnector, sleep = originalDial, originalSleep })

			dials := 0
			dialConnector = func(context.Context, string, string) (ConnectorSocket, error) {
				dials++
				return &fakeSocket{reads: []any{
					readyFrame("connector-1", CapabilityIngest),
					&ConnectorClosed{Code: code, Reason: "refused"},
				}}, nil
			}
			slept := []time.Duration{}
			sleep = func(d time.Duration) { slept = append(slept, d) }

			h := newHarness(t)
			if exit := h.d.Run(context.Background()); exit != clientcmd.ExitUnavailable {
				t.Errorf("Run = %d, want ExitUnavailable (%d)", exit, clientcmd.ExitUnavailable)
			}
			if dials != 1 {
				t.Errorf("dial count = %d, want exactly 1 (a refused credential is not retried)", dials)
			}
			if len(slept) != 0 {
				t.Errorf("slept %v; a refused credential is not waited on", slept)
			}
		})
	}
}

func TestAPlainTransportErrorReconnectsWithBackoff(t *testing.T) {
	originalDial, originalSleep := dialConnector, sleep
	t.Cleanup(func() { dialConnector, sleep = originalDial, originalSleep })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dials := 0
	slept := []time.Duration{}
	dialConnector = func(context.Context, string, string) (ConnectorSocket, error) {
		dials++
		if dials > 2 {
			cancel()
			return nil, errors.New("dial refused after the test's two failures")
		}
		return &fakeSocket{reads: []any{
			readyFrame("connector-1", CapabilityIngest),
			errors.New("the network dropped"),
		}}, nil
	}
	sleep = func(d time.Duration) { slept = append(slept, d) }

	h := newHarness(t)
	if exit := h.d.Run(ctx); exit != clientcmd.ExitOK {
		t.Fatalf("Run = %d, want ExitOK (%d)", exit, clientcmd.ExitOK)
	}
	if dials != 3 {
		t.Fatalf("dial count = %d, want 3 (a plain error reconnects)", dials)
	}
	if len(slept) != 2 {
		t.Fatalf("sleeps = %v, want 2 recorded waits", slept)
	}
	// 2.2's 1 s, +/-20 %: the first wait is roughly a second...
	if slept[0] < 800*time.Millisecond || slept[0] > 1200*time.Millisecond {
		t.Errorf("first wait = %s, want ~1 s (within +/-20 %%)", slept[0])
	}
	// ...and the doubling has started: the second is roughly two seconds and strictly larger.
	if slept[1] <= slept[0] || slept[1] < 1600*time.Millisecond || slept[1] > 2400*time.Millisecond {
		t.Errorf("second wait = %s, want ~2 s and larger than %s (the doubling started)", slept[1], slept[0])
	}
}

// Case 6 (2.3): an unknown server frame kind is ignored and logged ONCE per kind, not twice, and the
// socket is not closed.
func TestUnknownServerFrameKindIsIgnoredAndLoggedOnce(t *testing.T) {
	h := newHarness(t)
	h.sock.script(
		map[string]any{"type": "brand_new_kind", "n": 1},
		map[string]any{"type": "brand_new_kind", "n": 2},
	)
	if err := h.d.pass(context.Background()); err != nil {
		t.Fatalf("pass over an unknown frame kind: %v", err)
	}
	if h.sock.closed {
		t.Error("the socket was closed on an unknown frame kind; 2.3 says it never is")
	}
	if n := h.logsContaining("brand_new_kind"); n != 1 {
		t.Errorf("log lines for the unknown kind = %d, want exactly 1", n)
	}
}

// Case 7 (2.2): a session frame is re-sent after EVERY ready, and a seat that leaves `rig ps` is sent
// once with state stopped.
func TestSessionIsResentAfterEveryReady(t *testing.T) {
	h := newHarness(t)
	cand := newCandidate("dev-alice@room-a", "room-a", "alice", SessionRunning)
	h.d.Sessions = func(context.Context) ([]sessionCandidate, error) {
		return []sessionCandidate{cand}, nil
	}
	h.sock.answer("hello",
		readyFrame("connector-1", CapabilityIngest),
		readyFrame("connector-1", CapabilityIngest),
	)
	h.sock.answer("session", sessionAck("s1", ""), sessionAck("s1", ""))

	for i := range 2 {
		if _, err := h.d.hello(); err != nil {
			t.Fatalf("hello %d: %v", i, err)
		}
		if err := h.d.resendAll(context.Background()); err != nil {
			t.Fatalf("resendAll %d: %v", i, err)
		}
	}
	frames := h.sock.framesOfType("session")
	if len(frames) != 2 {
		t.Fatalf("session frames after two readys = %d, want 2 (re-sent after every ready)", len(frames))
	}
	for _, frame := range frames {
		if frame["state"] != SessionRunning {
			t.Errorf("session frame = %v, want state running", frame)
		}
	}
}

func TestASeatLeavingRoomPsIsSentStoppedOnce(t *testing.T) {
	h := newHarness(t)
	candidates := []sessionCandidate{newCandidate("dev-alice@room-a", "room-a", "alice", SessionRunning)}
	h.d.Sessions = func(context.Context) ([]sessionCandidate, error) { return candidates, nil }
	h.sock.answer("hello", readyFrame("connector-1", CapabilityIngest))
	h.sock.answer("session", sessionAck("s1", ""), sessionAck("s1", ""))

	if _, err := h.d.hello(); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := h.d.resendAll(context.Background()); err != nil {
		t.Fatalf("resendAll: %v", err)
	}

	candidates = nil
	if err := h.d.pass(context.Background()); err != nil {
		t.Fatalf("pass after the seat left: %v", err)
	}
	if err := h.d.pass(context.Background()); err != nil {
		t.Fatalf("second pass after the seat left: %v", err)
	}

	frames := h.sock.framesOfType("session")
	if len(frames) != 2 {
		t.Fatalf("session frames = %d, want 2 (running then one stopped)", len(frames))
	}
	stopped := 0
	for _, frame := range frames {
		if frame["state"] == SessionStopped {
			stopped++
		}
	}
	if stopped != 1 {
		t.Errorf("stopped session frames = %d, want exactly 1", stopped)
	}
}

// Case 8 (2.2): the heartbeat closes the socket when three heartbeats pass with no frame, and a pong
// arriving within the window keeps it up. The clock is the injected, frozen-then-moved one.
func TestKeepAliveClosesAfterThreeHeartbeatsSilent(t *testing.T) {
	h := newHarness(t)
	h.sock.answer("hello", readyFrame("connector-1", CapabilityIngest))
	ready, err := h.d.hello()
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	// connectAndServe applies ready.heartbeat_s; driving hello directly does the same here.
	h.d.heartbeatS = ready.HeartbeatS
	if h.d.heartbeatS != 1 {
		t.Fatalf("heartbeat_s from ready = %d, want 1", h.d.heartbeatS)
	}
	h.d.lastFrame = h.clock.now()
	h.clock.advance(4 * time.Second)

	err = h.d.keepAlive()
	if err == nil {
		t.Fatal("keepAlive returned nil four heartbeats after the last frame, want a dead-socket error")
	}
	if !strings.Contains(err.Error(), "three heartbeats") {
		t.Errorf("keepAlive error = %q, want it to name the three heartbeats", err)
	}
}

func TestKeepAliveWithAPongStaysUp(t *testing.T) {
	h := newHarness(t)
	h.sock.answer("hello", readyFrame("connector-1", CapabilityIngest))
	ready, err := h.d.hello()
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	h.d.heartbeatS = ready.HeartbeatS
	h.d.lastFrame = h.clock.now()
	h.clock.advance(4 * time.Second)

	h.sock.script(map[string]any{"type": "pong", "t": float64(0)})
	if err := h.d.drain(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := h.d.keepAlive(); err != nil {
		t.Errorf("keepAlive after a pong = %v, want nil", err)
	}
}
