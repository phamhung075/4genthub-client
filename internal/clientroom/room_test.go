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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
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
	return append(args, "--state-root", d.stateDir, "--socket", d.socket)
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
	// A HUB THAT CANNOT BE REACHED, deliberately: this test is the fallback case - the room's own record is
	// all there is, so the restart frame carries no document and the seat still comes back. The document's own
	// case is the test below.
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTHUB_TOKEN", "tok")

	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("lead", "omp", "running") + "]"),
		"StopSeat":      okData("null"),
		"RestartSeat":   okData(seatJSON("lead", "omp", "running")),
	})

	code, stdout, stderr := runRoom(t, daemon.args("reseat", testRoom, "lead")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	assertRequests(t, daemon,
		`{"action":"GetRoomStatus","payload":{"room":"room-a"}}`,
		`{"action":"StopSeat","payload":{"room":"room-a","seat":"lead"}}`,
		`{"action":"RestartSeat","payload":{"room":"room-a","seat":"lead"}}`)
	if !strings.Contains(stdout, "lead omp running agenthub--room-a--lead\n") {
		t.Errorf("stdout = %q, want the returned seat status", stdout)
	}
}

// THE RESEAT CARRIES THE HUB'S DOCUMENT: this is the act that installs a policy change, so the render is
// fetched for the seat being restarted and travels on the restart frame - the frame the runtime writes
// `<seat dir>/config.yml` from. The frame's KEY is a contract between two implementations, and the runtime's
// own test parses this exact spelling (`types.rs`), because a rename on one side alone is a document that
// silently never arrives. Measured before this change: the frame carried only the room and the seat, so a
// seat whose policy had changed came back with the file it was launched with.
func TestRoomReseatCarriesTheHubsPolicyDocument(t *testing.T) {
	const document = "bash:\n  allowCompoundCommands: true\ntools:\n  approval:\n    edit: deny\n"
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != clientsync.PathsPath+"/"+testRoom+"/lead" {
			http.Error(w, `{"detail":"seat not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"resolved_seat": map[string]any{
				"room": testRoom, "seat": "lead", "hash": "abc",
				"files":  []any{map[string]any{"path": clientsync.SeatRenderPath, "content": document}},
				"policy": map[string]any{},
			},
		})
	}))
	t.Cleanup(cloud.Close)
	t.Setenv("AGENTHUB_URL", cloud.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok")

	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("lead", "omp", "running") + "]"),
		"StopSeat":      okData("null"),
		"RestartSeat":   okData(seatJSON("lead", "omp", "running")),
	})

	code, _, stderr := runRoom(t, daemon.args("reseat", testRoom, "lead")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	installed, err := json.Marshal(clientsync.InstallDocument(document))
	if err != nil {
		t.Fatalf("marshal the document the seat is installed with: %v", err)
	}
	assertRequests(t, daemon,
		`{"action":"GetRoomStatus","payload":{"room":"room-a"}}`,
		`{"action":"StopSeat","payload":{"room":"room-a","seat":"lead"}}`,
		`{"action":"RestartSeat","payload":{"policy_document":`+string(installed)+`,"room":"room-a","seat":"lead"}}`)
}

// TestRoomReseatRefusesAtPeakBeforeTheSeatIsStopped. A restart is a launch, so the gate decides BEFORE the
// stop: the daemon's own recorded frames are the proof of the order - the room's seats are read to decide,
// and a refusal leaves nothing to stop. A gate that ran after StopSeat would record that frame here.
func TestRoomReseatRefusesAtPeakBeforeTheSeatIsStopped(t *testing.T) {
	// 2026-10-09 02:00 is inside 01:00-04:00 UTC on a Friday; the setting is the default (on).
	t.Setenv(clientoffpeak.HolidaysEnv, "")
	t.Setenv(clientoffpeak.PinEnv, "2026-10-09 02:00")
	t.Setenv(clientoffpeak.ModeEnv, "")

	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + deepSeekSeatJSON("lead") + "]"),
		"StopSeat":      okData("null"),
		"RestartSeat":   okData(seatJSON("lead", "omp", "running")),
	})
	// NO ENVIRONMENT SOCKET: the verb's own `--socket` (daemon.args) is what the gate must read, and this
	// test runs without one in the environment - the configuration CI has, and the one that was red.
	code, stdout, stderr := runRoom(t, daemon.args("reseat", testRoom, "lead")...)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, stderr)
	}
	// ONE FRAME, and it is the decision: no StopSeat, no RestartSeat.
	assertRequests(t, daemon, `{"action":"GetRoomStatus","payload":{"room":"room-a"}}`)
	if !strings.Contains(stdout, "room: "+testRoom+"@"+daemon.stateDir) {
		t.Errorf("stdout = %q, want the pair printed before the refusal", stdout)
	}
	for _, want := range []string{"lead: deepseek/deepseek-flash", "PEAK", clientoffpeak.FlagAllowPeak, clientoffpeak.ModeEnv + "=" + clientoffpeak.ValueOff} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, stderr)
		}
	}
}

// TestRoomReseatProceedsAtPeakWithTheFlag is the refusal's control: the flag passes the same peak room through
// the same two frames, so the test above is the gate refusing and not the room being unrunnable.
func TestRoomReseatProceedsAtPeakWithTheFlag(t *testing.T) {
	t.Setenv(clientoffpeak.HolidaysEnv, "")
	t.Setenv(clientoffpeak.PinEnv, "2026-10-09 02:00")
	t.Setenv(clientoffpeak.ModeEnv, "")
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTHUB_TOKEN", "tok")

	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + deepSeekSeatJSON("lead") + "]"),
		"StopSeat":      okData("null"),
		"RestartSeat":   okData(seatJSON("lead", "omp", "running")),
	})
	// NO ENVIRONMENT SOCKET, as above: the flag is the only socket this run was given, so a pass here is the
	// gate having read the daemon the flag names.
	args := append(daemon.args("reseat", testRoom, "lead"), clientoffpeak.FlagAllowPeak)
	code, _, stderr := runRoom(t, args...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	assertRequests(t, daemon,
		`{"action":"GetRoomStatus","payload":{"room":"room-a"}}`,
		`{"action":"StopSeat","payload":{"room":"room-a","seat":"lead"}}`,
		`{"action":"RestartSeat","payload":{"room":"room-a","seat":"lead"}}`)
}

// TestRoomReseatGatesTheDaemonTheSocketFlagNames is the defect itself, with two daemons answering
// differently: the one `--socket` names hosts a DeepSeek seat, and the machine's default (the environment's)
// hosts an omp seat. At peak the gate must refuse ON THE FLAG'S DAEMON - and the default must not be asked at
// all, which is what its empty request log proves. A gate reading the default would pass this room silently.
//
// The environment daemon registers no answers on purpose: a read of it would be recorded as a test failure by
// fakeDaemon.serve, so "never asked" is asserted rather than assumed.
func TestRoomReseatGatesTheDaemonTheSocketFlagNames(t *testing.T) {
	t.Setenv(clientoffpeak.HolidaysEnv, "")
	t.Setenv(clientoffpeak.PinEnv, "2026-10-09 02:00")
	t.Setenv(clientoffpeak.ModeEnv, "")

	flagDaemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + deepSeekSeatJSON("lead") + "]"),
		"StopSeat":      okData("null"),
		"RestartSeat":   okData(seatJSON("lead", "omp", "running")),
	})
	defaultDaemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("lead", "omp", "running") + "]"),
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, defaultDaemon.socket)

	code, _, stderr := runRoom(t, flagDaemon.args("reseat", testRoom, "lead")...)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, stderr)
	}
	if !strings.Contains(stderr, "lead: deepseek/deepseek-flash") {
		t.Errorf("the refusal does not name the flag daemon's DeepSeek seat:\n%s", stderr)
	}
	assertRequests(t, flagDaemon, `{"action":"GetRoomStatus","payload":{"room":"room-a"}}`)
	if got := defaultDaemon.requests(); len(got) != 0 {
		t.Errorf("the default daemon was asked %q: the gate read the environment's socket, not the flag's", got)
	}
}

// deepSeekSeatJSON is one seat whose model is DeepSeek's, the shape the gate reads the model out of.
func deepSeekSeatJSON(seat string) string {
	return `{"room":"` + testRoom + `","seat":"` + seat + `","session_name":"agenthub--` + testRoom + `--` + seat +
		`","runtime":"omp","state":"running","model":"deepseek/deepseek-flash"}`
}

// TestRoomReseatReportsASeatThatDidNotComeBack pins the partial case: the daemon answered, the seat is not
// running, and the verdict is 2 with the seat named - never a bare 0.
func TestRoomReseatReportsASeatThatDidNotComeBack(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("lead", "omp", "running") + "]"),
		"StopSeat":      okData("null"),
		"RestartSeat":   okData(seatJSON("lead", "omp", "stopped")),
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
	useTempAgentState(t, "other")
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": okData("null")})

	code, stdout, stderr := runRoom(t, daemon.args("stop", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	assertRequests(t, daemon, `{"action":"StopRoom","payload":{"room":"room-a"}}`)

	rendered := stdout + stderr
	for _, forbidden := range []string{"tmux", "agent", "kill-session", "compact-run", "watchdog", "bridge"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the stop reported %q, which names %q: StopRoom is the whole of this verb", rendered, forbidden)
		}
	}
	// The room orchestrator's state home is NOT in that list, and the omission is deliberate: the pair this verb
	// prints echoes back the --state-root the caller passed, so a substring test there would fail on the checkout
	// path rather than on the verb. What the verb touched is the
	// wire, and assertRequests above already pins it to one StopRoom and nothing else.
	// The directory is LEFT IN PLACE, and the report says so: spec.json, seats/ and logs/ are what makes the
	// room startable again.
	if !strings.Contains(stdout, "left in place") || !strings.Contains(stdout, "spec.json") {
		t.Errorf("stdout = %q, want the room's directory named as left in place", stdout)
	}
}

// useTempAgentState points the rooms-meant-to-run file at a temp store holding `rooms`, so a stop under test
// never rewrites the machine's own list.
func useTempAgentState(t *testing.T, rooms ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	previous := clientservices.AgentStatePath
	clientservices.AgentStatePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { clientservices.AgentStatePath = previous })
	if err := clientservices.WriteAgentState(clientservices.AgentState{Rooms: rooms}); err != nil {
		t.Fatal(err)
	}
	return path
}

// agentRooms is the rooms-meant-to-run list for tests whose subject is not the read itself.
func agentRooms(t *testing.T) []string {
	t.Helper()
	state, err := clientservices.ReadAgentState()
	if err != nil {
		t.Fatalf("ReadAgentState: %v", err)
	}
	return state.Rooms
}

// TestRoomStopDropsTheRoomFromTheRoomsMeantToRun pins the fix for a stopped room that agent kept supervising:
// the stop removes exactly that room from the list and leaves the others.
func TestRoomStopDropsTheRoomFromTheRoomsMeantToRun(t *testing.T) {
	useTempAgentState(t, testRoom, "other")
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": okData("null")})

	if code, _, stderr := runRoom(t, daemon.args("stop", testRoom)...); code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stderr %q)", code, stderr)
	}
	if got := strings.Join(agentRooms(t), ","); got != "other" {
		t.Errorf("rooms meant to run = %q, want only %q", got, "other")
	}
}

// A room the runtime no longer knows is still dropped, so a stale entry can be cleared.
func TestRoomStopDropsTheRoomEvenWhenTheRuntimeRefusesTheStop(t *testing.T) {
	useTempAgentState(t, testRoom)
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": daemonError("no such room")})

	if code, _, _ := runRoom(t, daemon.args("stop", testRoom)...); code == clientcmd.ExitOK {
		t.Fatal("a refused StopRoom must not exit 0")
	}
	if rooms := agentRooms(t); len(rooms) != 0 {
		t.Errorf("rooms meant to run = %v, want none", rooms)
	}
}

// TestRoomStopRefusesToWriteAListItCouldNotRead pins the destructive half of the failed-read class this pod
// has been closing all day. `stop` rewrites the rooms-meant-to-run list with what it read, so ONE unreadable
// agent.json turned the machine's list into the empty one and dropped every OTHER room from agent's view - the
// rooms it is there to keep running. Measured before the fix: with `not json{` written over a two-room list,
// the file came back as the empty state and `other` was gone. A read that failed is not the answer "nothing
// is meant to run", and a verb must not write the answer it could not read.
func TestRoomStopRefusesToWriteAListItCouldNotRead(t *testing.T) {
	path := useTempAgentState(t, testRoom, "other")
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
	// have agent bring it straight back, which is the lie the ordering in `runStop` exists to prevent.
	assertRequests(t, daemon)
}

// TestEveryDispatchedRoomVerbIsAnnounced: the group's announcements are what a caller reads when they guess
// wrong, so a verb the dispatcher accepts must be named by all three of them - and a name in the list must
// reach the dispatcher. `queue`, `report`, `metrics` and `limits` all shipped while every announcement still
// said four verbs, which is the drift this pins (the reviewer measured it as F2 on the budget change).
//
// Each verb is driven with NO room, which is what keeps this off the socket: a dispatched verb prints its own
// shape, while a name the switch does not know prints "unknown verb". Driving one WITH a room would resolve
// this machine's daemon and answer on the live room, which no test may do.
func TestEveryDispatchedRoomVerbIsAnnounced(t *testing.T) {
	// THE ROOM VARIABLE IS CLEARED FIRST. ROOM defaults from $AGENTHUB_ROOM (queue.go,
	// queueDefaultRoom), and every seat shell exports it; left alone, a verb driven with NO room
	// answers on this machine's live room instead of printing its own usage - the ambient-env red
	// measured 2026-10-19, where the suite passed for the owner and failed in any seat shell. An empty
	// value reads as unset (queueDefaultRoom trims it), so clearing and unsetting are one state here.
	// The tests that PIN the default set it themselves, which is why this clear is local and not in
	// runRoom.
	t.Setenv(roomEnv, "")
	code, _, pick := runRoom(t)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("room with no verb: exit %d, want %d", code, clientcmd.ExitRoomRefused)
	}
	code, _, bogus := runRoom(t, "nosuchverb")
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("room nosuchverb: exit %d, want %d", code, clientcmd.ExitRoomRefused)
	}
	for _, verb := range roomVerbs {
		for _, text := range []string{pick, bogus} {
			if !strings.Contains(text, verb) {
				t.Errorf("announcement %q does not name the verb %q", text, verb)
			}
		}
		if !strings.Contains(groupUsage, verb) {
			t.Errorf("group usage %q does not name the verb %q", groupUsage, verb)
		}
		if _, ok := subUsages[verb]; !ok {
			t.Errorf("subUsages has no shape for %q, so a usage error over it prints the group's grammar", verb)
		}
		if _, _, stderr := runRoom(t, verb); strings.Contains(stderr, "unknown verb") {
			t.Errorf("the dispatcher does not know %q, which roomVerbs announces: %s", verb, stderr)
		} else if !strings.Contains(stderr, "4genteam room "+verb) {
			t.Errorf("room %s answered %q, want the verb's own shape: it was not dispatched", verb, stderr)
		}
	}
	// And the list must carry every verb the group has a SHAPE for. That is the direction this test exists for:
	// `limits` had a shape in subUsages while the announcements still said four verbs, and a check that walks
	// roomVerbs alone cannot see an omission from the list itself.
	for verb := range subUsages {
		listed := false
		for _, known := range roomVerbs {
			if known == verb {
				listed = true
				break
			}
		}
		if !listed {
			t.Errorf("subUsages has a shape for %q and roomVerbs does not announce it: the list is incomplete", verb)
		}
	}
}

// TestARoomVerbOutsideTheListIsRefusedByName: the other direction - a name that roomVerbs does not carry is
// refused together with the list, so the list is the announcement and not a decoration.
func TestARoomVerbOutsideTheListIsRefusedByName(t *testing.T) {
	code, _, stderr := runRoom(t, "queues", testRoom)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("room queues: exit %d, want %d", code, clientcmd.ExitRoomRefused)
	}
	if !strings.Contains(stderr, roomVerbList()) {
		t.Errorf("stderr = %q, want the whole verb list %q", stderr, roomVerbList())
	}
}

// agyTranscriptFile points the verb at a transcript of the test's own, whose newest record either ends the
// turn (a final answer) or leaves it running (a pending tool call).
func agyTranscriptFile(t *testing.T, ended bool) {
	t.Helper()
	last := `{"type":"PLANNER_RESPONSE","status":"DONE","input_tokens":1,"cache_read_tokens":2,"output_tokens":3}`
	if !ended {
		last = `{"type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"run_command"}]}`
	}
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"USER_INPUT","status":"DONE"}`+"\n"+last+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := agyTranscript
	t.Cleanup(func() { agyTranscript = previous })
	agyTranscript = func(string, string, string) string { return path }
}

// An agy seat has no runner, so ForceCompact can only refuse it (room aa2-rs-peak, 2026-10-28). At its
// prompt it is sent agy's own /compact through SendText, and no ForceCompact is asked for it.
func TestRoomCompactTypesSlashCompactIntoAnAgySeatAtItsPrompt(t *testing.T) {
	agyTranscriptFile(t, true)
	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("peak-dev", "agy", "running") + "]"),
		"SendText":      okData("null"),
	})
	code, stdout, stderr := runRoom(t, daemon.args("compact", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	requests := daemon.requests()
	if len(requests) != 2 || !strings.Contains(requests[1], `"action":"SendText"`) ||
		!strings.Contains(requests[1], `"agenthub--room-a--peak-dev"`) || !strings.Contains(requests[1], `"/compact"`) {
		t.Fatalf("requests = %q, want GetRoomStatus then one SendText of /compact to the agy seat", requests)
	}
	if !strings.Contains(stdout, "peak-dev agenthub--room-a--peak-dev: /compact typed") {
		t.Errorf("stdout = %q, want the agy seat's line", stdout)
	}
}

// Mid-turn, agy queues a pasted /compact as a plain message, so nothing is typed and the seat is refused by name.
func TestRoomCompactRefusesAnAgySeatMidTurnAndTypesNothing(t *testing.T) {
	agyTranscriptFile(t, false)
	daemon := newFakeDaemon(t, map[string]string{
		"GetRoomStatus": okData("[" + seatJSON("peak-dev", "agy", "running") + "]"),
	})
	code, _, stderr := runRoom(t, daemon.args("compact", testRoom)...)
	if code != clientcmd.ExitPartialStart {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitPartialStart, stderr)
	}
	assertRequests(t, daemon, `{"action":"GetRoomStatus","payload":{"room":"room-a"}}`)
	if !strings.Contains(stderr, "peak-dev") || !strings.Contains(stderr, "mid-turn") {
		t.Errorf("stderr = %q, want the agy seat refused as mid-turn", stderr)
	}
}
