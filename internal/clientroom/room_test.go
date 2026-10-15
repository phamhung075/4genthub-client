// The room verbs' WIRE and its RENDERED OUTPUT are what these tests pin, because those are the two things a
// reader and a daemon meet: `clientruntime`'s own suite makes the same choice for the same reason - a Go
// struct assertion would pin the encoder's opinion rather than the frame.
//
// The server is a real Unix socket, not a seam over clientruntime: the verbs' flags, the address they build
// and the JSON they write are then all exercised by the test the way a run exercises them.
package clientroom

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

const testRoom = "room-a"

// fakeDaemon is a real Unix socket server standing in for agenthub-runtime: it accepts connections in the
// order the client opens them, records each request LINE exactly as it arrived, and answers with the canned
// response registered for that action.
type fakeDaemon struct {
	socket   string
	stateDir string
	answers  map[string]string

	mu    sync.Mutex
	lines []string
	t     *testing.T
}

func newFakeDaemon(t *testing.T, answers map[string]string) *fakeDaemon {
	t.Helper()

	// The socket lives in a short temp dir rather than in t.TempDir(): a Unix socket path is capped at about
	// 108 bytes, and t.TempDir() spells out the test's own name.
	dir, err := os.MkdirTemp("", "rframe")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	daemon := &fakeDaemon{t: t, socket: socket, stateDir: t.TempDir(), answers: answers}
	go daemon.serve(listener)
	return daemon
}

// serve answers ONE CONNECTION AT A TIME, which is what makes the recorded order the client's own order
// rather than a race between goroutines: the client's Call writes one line and waits for one line back, so a
// sequential server cannot deadlock it.
func (d *fakeDaemon) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return // the listener is closed by the test's own cleanup
		}
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			_ = conn.Close()
			return
		}
		d.record(string(line))
		var request struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(line, &request)
		answer, ok := d.answers[request.Action]
		if !ok {
			// An unregistered request is a test failure, and it is ANSWERED rather than dropped, so the failure
			// is the assertion below instead of a verb hanging on a socket that never replies.
			d.t.Errorf("the verb asked for %s, which this test did not register", request.Action)
			answer = `{"ok":false,"error":"no such action"}` + "\n"
		}
		_, _ = conn.Write([]byte(answer))
		_ = conn.Close()
	}
}

func (d *fakeDaemon) record(line string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, line)
}

// requests is the lines the client wrote, in the order it wrote them.
func (d *fakeDaemon) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.lines...)
}

// roomArgs is the flag surface every test drives: the sub-verb, its positionals, and then the two optional
// flags in the order the usage line writes them. Passing them as real arguments is the point - the pair the
// verb prints is the pair it must have resolved from these.
func (d *fakeDaemon) args(verb string, positionals ...string) []string {
	args := append([]string{verb}, positionals...)
	return append(args, "--state-dir", d.stateDir, "--socket", d.socket)
}

// runRoom drives the verb the way the dispatcher does: the three streams
// captured.
func runRoom(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RoomCommand{}.Run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// assertRequests compares the lines the client wrote, in order, against the frames the contract names. The
// expected lines are spelled out per test rather than composed, so a change to any key fails in the test that
// names it, and the COUNT is asserted, which is how "one ForceCompact per running seat and none for the
// stopped one" is pinned. The terminating newline is pinned once rather than repeated in every expected
// string, because the daemon reads one line per request.
func assertRequests(t *testing.T, daemon *fakeDaemon, want ...string) {
	t.Helper()
	got := daemon.requests()
	if len(got) != len(want) {
		t.Fatalf("the client wrote %d request lines, want %d:\n got %q\nwant %q", len(got), len(want), got, want)
	}
	for i, line := range got {
		if !strings.HasSuffix(line, "\n") {
			t.Fatalf("request line %d = %q, want it terminated by a newline", i, line)
		}
		if frame := strings.TrimSuffix(line, "\n"); frame != want[i] {
			t.Fatalf("request line %d:\n got %q\nwant %q", i, line, want[i])
		}
	}
}

// okData frames one successful response the way the daemon does: the payload under `data`, one JSON line.
func okData(data string) string { return `{"ok":true,"data":` + data + "}\n" }

// daemonError frames the daemon's own failure. It is what a room the daemon does not know arrives as: the
// runtime reads `<room>/spec.json` and reports why it could not.
func daemonError(message string) string {
	return `{"ok":false,"error":` + strconv.Quote(message) + "}\n"
}

// seatJSON is one seat as the daemon renders it, carrying the four fields every line of this group prints.
func seatJSON(seat, runtime, state string) string {
	return `{"room":"` + testRoom + `","seat":"` + seat + `","session_name":"agenthub--` + testRoom + `--` + seat +
		`","runtime":"` + runtime + `","state":"` + state + `"}`
}

// TestRoomStatusPrintsThePairThenOneLinePerSeat pins the two halves of the verb a caller reads: the pair it
// resolved (as <room>@<state-dir>, plus the socket that named the daemon) and then one line per seat in the
// daemon's own order, with the daemon's own state word.
func TestRoomStatusPrintsThePairThenOneLinePerSeat(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("lead", "omp", "running") + "," + seatJSON("dev", "omp", "stopped") + "]"),
	})

	code, stdout, stderr := runRoom(t, daemon.args("status", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := "room: " + testRoom + "@" + daemon.stateDir + " socket: " + daemon.socket + "\n" +
		"lead omp running agenthub--room-a--lead\n" +
		"dev omp stopped agenthub--room-a--dev\n"
	if stdout != want {
		t.Fatalf("stdout:\n got %q\nwant %q (stderr %q)", stdout, want, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing: a status that read the room has nothing to refuse", stderr)
	}
	assertRequests(t, daemon, `{"action":"GetRoomStatus","payload":{"room":"room-a"}}`)
}

// TestRoomStatusRendersTheAlertAndKeepsUnknownQuietTimeUnknown pins the three readings of the OPTIONAL
// `alert` key on one seat's row, and it compares the WHOLE stdout rather than a substring because both
// halves of the contract are exact: a seat the runtime did not flag prints the four fields it always did -
// a pre-existing reader of this verb sees no change - and a seat whose quiet time is UNKNOWN must not print
// the number a seat that acted a moment ago prints.
//
// THE TWO INDISTINGUISHABLE CASES are the point of the third and fourth payloads: `quiet_seconds` absent is
// a seat that published NO stamp (unknown), and `quiet_seconds: 0` is a seat that published one just now.
// Rendering both as "0" would make the alert say "this seat just did something" about the one seat it exists
// to raise, so the unknown case says `unknown` in the number's place.
func TestRoomStatusRendersTheAlertAndKeepsUnknownQuietTimeUnknown(t *testing.T) {
	// The payloads differ in the alert key alone, so a difference in the rendered line can only come from it.
	const (
		fields = `"room":"` + testRoom + `","seat":"lead","session_name":"agenthub--` + testRoom + `--lead","runtime":"omp","state":"running"`
		row    = "lead omp running agenthub--room-a--lead"
	)
	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "an alert is rendered with its kind, its quiet seconds and the ids waiting for the seat",
			payload: "{" + fields + `,"alert":{"kind":"seat_up_with_queued_work","quiet_seconds":90,"waiting_qitem_ids":["qi-1","qi-2"]}}`,
			want:    row + " alert=seat_up_with_queued_work quiet_seconds=90 waiting_qitem_ids=qi-1,qi-2",
		},
		{
			name:    "an ABSENT alert key is no alert, not an alert of zeroes",
			payload: "{" + fields + "}",
			want:    row,
		},
		{
			name:    "an ABSENT quiet_seconds says unknown, never a zero-second alert",
			payload: "{" + fields + `,"alert":{"kind":"seat_up_with_queued_work","waiting_qitem_ids":["qi-3"]}}`,
			want:    row + " alert=seat_up_with_queued_work quiet_seconds=unknown waiting_qitem_ids=qi-3",
		},
		{
			name:    "a stamp published now is a real zero seconds, which reads differently from unknown",
			payload: "{" + fields + `,"alert":{"kind":"seat_up_with_queued_work","quiet_seconds":0,"waiting_qitem_ids":["qi-4"]}}`,
			want:    row + " alert=seat_up_with_queued_work quiet_seconds=0 waiting_qitem_ids=qi-4",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemon := newFakeDaemon(t, map[string]string{
				"GetRoomStatus": okData("[" + tc.payload + "]"),
			})

			code, stdout, stderr := runRoom(t, daemon.args("status", testRoom)...)
			if code != clientcmd.ExitOK {
				t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
			}
			want := "room: " + testRoom + "@" + daemon.stateDir + " socket: " + daemon.socket + "\n" + tc.want + "\n"
			if stdout != want {
				t.Fatalf("stdout:\n got %q\nwant %q (stderr %q)", stdout, want, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing: a status that read the room has nothing to refuse", stderr)
			}
			assertRequests(t, daemon, `{"action":"GetRoomStatus","payload":{"room":"room-a"}}`)
		})
	}
}

// TestRoomStatusReportsTheDaemonsOwnMessage pins the failure half: a room the daemon does not know is the
// daemon's sentence, printed as it arrived, and the design's 3 (§2.5) - not 2, which says the daemon started
// and a seat came up short.
func TestRoomStatusReportsTheDaemonsOwnMessage(t *testing.T) {
	const message = "cannot read room spec: No such file or directory (os error 2)"
	daemon := newFakeDaemon(t, map[string]string{"GetRoomStatus": daemonError(message)})

	code, stdout, stderr := runRoom(t, daemon.args("status", "nope")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	if !strings.Contains(stderr, message) {
		t.Errorf("stderr = %q, want the daemon's own message %q", stderr, message)
	}
	// The pair is printed BEFORE the ask, so a failure is readable against the daemon and the state dir it
	// was asked about.
	if !strings.Contains(stdout, "room: nope@"+daemon.stateDir) {
		t.Errorf("stdout = %q, want the pair for the room that was asked about", stdout)
	}
	assertRequests(t, daemon, `{"action":"GetRoomStatus","payload":{"room":"nope"}}`)
}

// TestRoomReseatWritesStopSeatThenRestartSeat pins the ORDER on the wire: two requests, the stop first, and
// both naming the same room and seat.
func TestRoomReseatWritesStopSeatThenRestartSeat(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{
		"StopSeat":    okData("null"),
		"RestartSeat": okData(seatJSON("lead", "omp", "running")),
	})

	code, stdout, stderr := runRoom(t, daemon.args("reseat", testRoom, "lead")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	assertRequests(t, daemon,
		`{"action":"StopSeat","payload":{"room":"room-a","seat":"lead"}}`,
		`{"action":"RestartSeat","payload":{"room":"room-a","seat":"lead"}}`)
	if !strings.Contains(stdout, "lead omp running agenthub--room-a--lead\n") {
		t.Errorf("stdout = %q, want the returned seat status", stdout)
	}
}

// TestRoomReseatReportsASeatThatDidNotComeBack pins the partial case: the daemon answered, the seat is not
// running, and the verdict is 2 with the seat named - never a bare 0.
func TestRoomReseatReportsASeatThatDidNotComeBack(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{
		"StopSeat":    okData("null"),
		"RestartSeat": okData(seatJSON("lead", "omp", "stopped")),
	})

	code, stdout, stderr := runRoom(t, daemon.args("reseat", testRoom, "lead")...)
	if code != clientcmd.ExitPartialStart {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitPartialStart, stderr)
	}
	if !strings.Contains(stdout, "lead omp stopped agenthub--room-a--lead\n") {
		t.Errorf("stdout = %q, want the seat status the runtime returned", stdout)
	}
	if !strings.Contains(stderr, "lead") || !strings.Contains(stderr, "not running") {
		t.Errorf("stderr = %q, want the seat named as the one that is not running", stderr)
	}
}

// TestRoomCompactAsksOncePerRunningSeatAndRefusesTheStoppedOne pins all three of compact's rules at once: one
// GetRoomStatus, exactly ONE ForceCompact (for the running seat, named by session), NONE for the stopped one,
// the runtime's own answer printed, the stopped seat refused by name, and exit 2.
func TestRoomCompactAsksOncePerRunningSeatAndRefusesTheStoppedOne(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("lead", "omp", "running") + "," + seatJSON("dev", "omp", "stopped") + "]"),
		"ForceCompact":  okData(`{"result":"compacted"}`),
	})

	code, stdout, stderr := runRoom(t, daemon.args("compact", testRoom)...)
	if code != clientcmd.ExitPartialStart {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitPartialStart, stderr)
	}
	assertRequests(t, daemon,
		`{"action":"GetRoomStatus","payload":{"room":"room-a"}}`,
		`{"action":"ForceCompact","payload":{"session":"agenthub--room-a--lead"}}`)
	if !strings.Contains(stdout, "lead agenthub--room-a--lead: compacted\n") {
		t.Errorf("stdout = %q, want the runtime's own answer beside the session it was asked about", stdout)
	}
	if !strings.Contains(stderr, "dev") || !strings.Contains(stderr, "not running") {
		t.Errorf("stderr = %q, want the stopped seat refused by name", stderr)
	}
	if strings.Contains(stdout, "dev") {
		t.Errorf("stdout = %q, want no success line for the seat nothing was asked about", stdout)
	}
}

// TestRoomCompactReportsARuntimeRefusalVerbatim pins the honest refusal of §3's F5: a runtime that will not
// compact is reported in its own words and the verb exits 2 rather than claiming a compact that did not
// happen.
func TestRoomCompactReportsARuntimeRefusalVerbatim(t *testing.T) {
	const refusal = "no runner for session agenthub--room-a--lead (no --mode rpc child)"
	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("lead", "omp", "running") + "]"),
		"ForceCompact":  daemonError(refusal),
	})

	code, stdout, stderr := runRoom(t, daemon.args("compact", testRoom)...)
	if code != clientcmd.ExitPartialStart {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitPartialStart, stderr)
	}
	if !strings.Contains(stderr, refusal) {
		t.Errorf("stderr = %q, want the runtime's refusal verbatim (%q)", stderr, refusal)
	}
	if strings.Contains(stdout, "compacted") {
		t.Errorf("stdout = %q, want no success line over a compaction the runtime refused", stdout)
	}
}

// TestRoomStopWritesStopRoomAndNothingElse pins the must-not list: ONE StopRoom request, and an output that
// names no tmux command and no supervision process - neither of which this verb has any business starting,
// stopping or mentioning.
func TestRoomStopWritesStopRoomAndNothingElse(t *testing.T) {
	useTempRigdState(t, "other")
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": okData("null")})

	code, stdout, stderr := runRoom(t, daemon.args("stop", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	assertRequests(t, daemon, `{"action":"StopRoom","payload":{"room":"room-a"}}`)

	rendered := stdout + stderr
	for _, forbidden := range []string{"tmux", "rigd", "kill-session", "compact-run", "watchdog", "bridge"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the stop reported %q, which names %q: StopRoom is the whole of this verb", rendered, forbidden)
		}
	}
	// The rig orchestrator's state home is NOT in that list, and the omission is deliberate: the pair this verb
	// prints echoes back the --state-dir the caller passed, so a substring test there would fail on the checkout
	// path rather than on the verb. What the verb touched is the
	// wire, and assertRequests above already pins it to one StopRoom and nothing else.
	// The directory is LEFT IN PLACE, and the report says so: spec.json, seats/ and logs/ are what makes the
	// room startable again.
	if !strings.Contains(stdout, "left in place") || !strings.Contains(stdout, "spec.json") {
		t.Errorf("stdout = %q, want the room's directory named as left in place", stdout)
	}
}

// useTempRigdState points the rooms-meant-to-run file at a temp store holding `rooms`, so a stop under test
// never rewrites the machine's own list.
func useTempRigdState(t *testing.T, rooms ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rigd.json")
	previous := clientservices.RigdStatePath
	clientservices.RigdStatePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { clientservices.RigdStatePath = previous })
	if err := clientservices.WriteRigdState(clientservices.RigdState{Rigs: rooms}); err != nil {
		t.Fatal(err)
	}
	return path
}

// rigdRigs is the rooms-meant-to-run list for tests whose subject is not the read itself.
func rigdRigs(t *testing.T) []string {
	t.Helper()
	state, err := clientservices.ReadRigdState()
	if err != nil {
		t.Fatalf("ReadRigdState: %v", err)
	}
	return state.Rigs
}

// TestRoomStopDropsTheRoomFromTheRoomsMeantToRun pins the fix for a stopped room that rigd kept supervising:
// the stop removes exactly that room from the list and leaves the others.
func TestRoomStopDropsTheRoomFromTheRoomsMeantToRun(t *testing.T) {
	useTempRigdState(t, testRoom, "other")
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": okData("null")})

	if code, _, stderr := runRoom(t, daemon.args("stop", testRoom)...); code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stderr %q)", code, stderr)
	}
	if got := strings.Join(rigdRigs(t), ","); got != "other" {
		t.Errorf("rooms meant to run = %q, want only %q", got, "other")
	}
}

// A room the runtime no longer knows is still dropped, so a stale entry can be cleared.
func TestRoomStopDropsTheRoomEvenWhenTheRuntimeRefusesTheStop(t *testing.T) {
	useTempRigdState(t, testRoom)
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": daemonError("no such room")})

	if code, _, _ := runRoom(t, daemon.args("stop", testRoom)...); code == clientcmd.ExitOK {
		t.Fatal("a refused StopRoom must not exit 0")
	}
	if rooms := rigdRigs(t); len(rooms) != 0 {
		t.Errorf("rooms meant to run = %v, want none", rooms)
	}
}

// TestRoomStopRefusesToWriteAListItCouldNotRead pins the destructive half of the failed-read class this pod
// has been closing all day. `stop` rewrites the rooms-meant-to-run list with what it read, so ONE unreadable
// rigd.json turned the machine's list into the empty one and dropped every OTHER room from rigd's view - the
// rooms it is there to keep running. Measured before the fix: with `not json{` written over a two-room list,
// the file came back as the empty state and `other` was gone. A read that failed is not the answer "nothing
// is meant to run", and a verb must not write the answer it could not read.
func TestRoomStopRefusesToWriteAListItCouldNotRead(t *testing.T) {
	path := useTempRigdState(t, testRoom, "other")
	unreadable := "not json{ into the file\n"
	if err := os.WriteFile(path, []byte(unreadable), 0o644); err != nil {
		t.Fatal(err)
	}
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": okData("null")})

	code, stdout, stderr := runRoom(t, daemon.args("stop", testRoom)...)
	if code == clientcmd.ExitOK {
		t.Fatalf("a stop over a list that could not be read exited 0 (stdout %q)", stdout)
	}
	if !strings.Contains(stderr, "could not be read") || !strings.Contains(stderr, "NOT dropped") {
		t.Errorf("stderr = %q, want the failed read named and the room said not dropped", stderr)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != unreadable {
		t.Errorf("the file that could not be read was rewritten to %q (err %v); nothing may be written over it", got, err)
	}
	// The room is not stopped either: with the room still in a list that could not be read, stopping it would
	// have rigd bring it straight back, which is the lie the ordering in `runStop` exists to prevent.
	assertRequests(t, daemon)
}
