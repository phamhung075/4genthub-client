// The queue verbs' WIRE and their RENDERED OUTPUT are what these tests pin, the choice room_test.go makes
// for the same reason: those are the two things a reader and a daemon meet.
//
// The server is the same real Unix socket, so a test drives `room queue ...` the way the dispatcher does -
// through RoomCommand.Run, with the sub-verb word in the argument list - and the frame it asserts is the
// line the daemon would have read, not a Go struct's shape.
package clientroom

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// testSeat is the session this test's shell carries: what create, update, claim and close act as, and what
// their frames must name as the actor.
const testSeat = "4genthub-min-go-dev@4genthub-min"

// schemaBody is a body the four-line create rule accepts; a create without one is refused since 4/8.
const schemaBody = "facts: task=cd47f19c; base=d6a7be6\nrule: architect ruling 3/8\ndecision: enforce at create\nnext: go-dev returns the runs"

// escapedBody is schemaBody as it rides a JSON string in a frame assertion.
func escapedBody(t *testing.T) string {
	t.Helper()
	encoded, err := json.Marshal(schemaBody)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Trim(string(encoded), `"`)
}

// queueRowFrame is one row as the daemon serializes it, carrying the fields a rendering reads. Only the
// transitions a test names are emitted: a row frame with no `transitions` key is a row whose log the daemon
// did not send, which is what a bare one-line rendering has to survive.
func queueRowFrame(qitemID, state, priority, summary string, transitions ...string) string {
	frame := `{"qitem_id":"` + qitemID + `","state":"` + state + `","priority":"` + priority +
		`","summary":"` + summary + `"`
	if len(transitions) > 0 {
		frame += `,"transitions":[` + strings.Join(transitions, ",") + `]`
	}
	return frame + "}"
}

// queueTransitionFrame is one transition as the daemon serializes it. An empty `from` omits from_state -
// Rust's Option, which is what the row's FIRST transition carries.
func queueTransitionFrame(id int64, ts, from, state, actor, note string) string {
	frame := `{"transition_id":` + strconv.FormatInt(id, 10) + `,"ts":"` + ts + `","state":"` + state +
		`","actor_session":"` + actor + `","transition_note":"` + note + `"`
	if from != "" {
		frame += `,"from_state":"` + from + `"`
	}
	return frame + "}"
}

// TestRoomQueueListPrintsOneLinePerRow pins the read's rendering: the pair, then `<qitem_id> <state>
// <priority|-> <summary>` per row IN THE ORDER THE DAEMON SENT THEM, with an empty priority rendered as `-`
// rather than as a gap a reader would take for a missing field.
func TestRoomQueueListPrintsOneLinePerRow(t *testing.T) {
	t.Setenv(sessionEnv, "")
	daemon := newFakeDaemon(t, map[string]string{
		"QueueList": okData("[" +
			queueRowFrame("qitem-bbb", "in-progress", "high", "second seat's row") + "," +
			queueRowFrame("qitem-aaa", "pending", "", "hand me the build log") + "]"),
	})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "list", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := "room: " + testRoom + "@" + daemon.stateDir + " socket: " + daemon.socket + "\n" +
		"qitem-bbb in-progress high second seat's row\n" +
		"qitem-aaa pending - hand me the build log\n"
	if stdout != want {
		t.Fatalf("stdout:\n got %q\nwant %q (stderr %q)", stdout, want, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing: a list that read the rail has nothing to refuse", stderr)
	}
	// The frame carries the filter even when it narrows nothing: the daemon requires the key, and an
	// omitted filter is a different request than the zero filter.
	assertRequests(t, daemon, `{"action":"QueueList","payload":{"filter":{},"room":"room-a"}}`)
}

// TestRoomQueueListOnAnEmptyRailSaysSo pins the empty read: the rail is named as empty in WORDS and the
// verb exits 0, because an empty read is a measurement the daemon made - while printing nothing would be
// indistinguishable from a read that never happened.
func TestRoomQueueListOnAnEmptyRailSaysSo(t *testing.T) {
	t.Setenv(sessionEnv, "")
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[]")})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "list", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d: an empty rail is not a failure (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := "room: " + testRoom + "@" + daemon.stateDir + " socket: " + daemon.socket + "\n" +
		"room room-a: the rail is empty\n"
	if stdout != want {
		t.Fatalf("stdout:\n got %q\nwant %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing: an empty rail must not look like a failure", stderr)
	}
	assertRequests(t, daemon, `{"action":"QueueList","payload":{"filter":{},"room":"room-a"}}`)
}

// TestRoomQueueCreateSendsTheEnvSessionAsSource pins the create: the SOURCE is the seat's own session read
// from the environment - the request frame names it, and nothing on the command line could have. The
// DESTINATION goes to the daemon AS THE CALLER TYPED IT: resolving `seat@room` into the session name is the
// runtime's job at create time (`resolve_queue_destination`, pinned by the runtime's own test), so the row,
// the alert and the nudge pass read one key no matter which client wrote it.
func TestRoomQueueCreateSendsTheEnvSessionAsSource(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{
		"QueueCreate": okData(queueRowFrame("qitem-new", "pending", "normal", "hand me the build log",
			queueTransitionFrame(1, "2026-10-14T00:17:00Z", "", "pending", testSeat, "created"))),
	})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "create", testRoom,
		"--to", "reviewer@"+testRoom, "--summary", "hand me the build log",
		"--body", schemaBody, "--tag", "gate")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := "room: " + testRoom + "@" + daemon.stateDir + " socket: " + daemon.socket + "\n" +
		"qitem-new pending normal hand me the build log\n" +
		"1 2026-10-14T00:17:00Z - -> pending by " + testSeat + " | created\n"
	if stdout != want {
		t.Fatalf("stdout:\n got %q\nwant %q (stderr %q)", stdout, want, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing: the create was answered", stderr)
	}
	assertRequests(t, daemon,
		`{"action":"QueueCreate","payload":{"input":{"source_session":"`+testSeat+
			`","destination_session":"reviewer@`+testRoom+`","summary":"hand me the build log",`+
			`"body":"`+escapedBody(t)+`","tags":["gate"]},"room":"`+testRoom+`"}}`)
}

// TestRoomQueueCreateRefusesARowOutsideTheSchemaWithoutARoundTrip pins the refusal at the CLI door: the ruled
// sentence is printed, the exit is 2 (the daemon answered nothing), and no frame is written - the client
// checked the four-line body before the round trip.
func TestRoomQueueCreateRefusesARowOutsideTheSchemaWithoutARoundTrip(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{})

	code, _, stderr := runRoom(t, daemon.args("queue", "create", testRoom,
		"--to", "reviewer@"+testRoom, "--summary", "hand me the build log", "--tag", "gate")...)
	if code != clientcmd.ExitPartialStart {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitPartialStart, stderr)
	}
	if !strings.Contains(stderr, "body schema: field 'facts' is missing") {
		t.Errorf("stderr = %q, want the missing facts field named", stderr)
	}
	assertRequests(t, daemon)
}

// TestRoomQueueCreateWithoutTheSessionEnvRefusesAndSendsNothing pins the actor rule's refusal half: with
// $AGENTHUB_SESSION_NAME unset the verb refuses, NAMES the variable, and asks the daemon NOTHING - a guessed
// actor is a row nobody can audit, so there is no frame to send.
func TestRoomQueueCreateWithoutTheSessionEnvRefusesAndSendsNothing(t *testing.T) {
	t.Setenv(sessionEnv, "")
	daemon := newFakeDaemon(t, map[string]string{})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "create", testRoom,
		"--to", "4genthub-min-reviewer@4genthub-min", "--summary", "hand me the build log")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	if !strings.Contains(stderr, sessionEnv) {
		t.Errorf("stderr = %q, want the unset variable named (%s)", stderr, sessionEnv)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing: the pair is printed for a run, and this run never started", stdout)
	}
	assertRequests(t, daemon)
}

// TestRoomQueueActsAsTheNativeSeatSession pins the variable by its literal name: the runtime launches every
// seat with AGENTHUB_SESSION_NAME and no the room orchestrator variable, so a verb reading any other name has no actor on a
// machine without the room orchestrator.
func TestRoomQueueActsAsTheNativeSeatSession(t *testing.T) {
	t.Setenv("AGENTHUB_SESSION_NAME", testSeat)
	daemon := newFakeDaemon(t, map[string]string{
		"QueueClaim": okData(queueRowFrame("qitem-new", "in-progress", "normal", "claimed",
			queueTransitionFrame(2, "2026-10-14T00:18:00Z", "pending", "in-progress", testSeat, "claimed"))),
	})
	code, _, stderr := runRoom(t, daemon.args("queue", "claim", testRoom, "qitem-new")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
}

// TestRoomQueueCloseRefusesAnUnknownReasonWithoutARoundTrip pins the local check: a `--reason` outside the
// daemon's own set is refused with the WHOLE allowed set, and NO request line is written - a typo costs no
// round trip.
func TestRoomQueueCloseRefusesAnUnknownReasonWithoutARoundTrip(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "close", testRoom, "qitem-x", "--reason", "bogus")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	for _, reason := range clientruntime.QueueClosureReasons {
		if !strings.Contains(stderr, reason) {
			t.Errorf("stderr = %q, want the allowed set to name %q", stderr, reason)
		}
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing: nothing was asked of the daemon", stdout)
	}
	assertRequests(t, daemon)
}

// A GATE CLOSE WITH NO VERDICT IS REFUSED BEFORE THE ROUND TRIP. Measured 2026-10-18 (review 3 line 2): gate
// rows were claimed and closed with the bare word `closed`, the body still holding the request, and no
// PASS/FAIL anywhere - the tests ran, the record did not carry them. The refusal names the field to fill.
func TestRoomQueueCloseRefusesAGateCloseWithoutAVerdict(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "close", testRoom, "qitem-gate", "--reason", "gate")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	for _, want := range []string{"verdict", "PASS", "Nothing was asked of the daemon"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to name %q", stderr, want)
		}
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing: nothing was asked of the daemon", stdout)
	}
	assertRequests(t, daemon)
}

// THE SAME CLOSE WITH ITS VERDICT RIDES THE NOTE, and the note is what the rail then shows: the reason names
// the kind of close and the note carries the verdict and the command that produced it.
func TestRoomQueueCloseCarriesTheGateVerdictInTheNote(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	verdict := "PASS 272dda3; go test ./internal/clientroom/... ok"
	daemon := newFakeDaemon(t, map[string]string{
		"QueueClose": okData(queueRowFrame("qitem-gate", "done", "normal", "Gate: a client change",
			queueTransitionFrame(2, "2026-10-18T23:50:00Z", "in-progress", "done", testSeat, verdict))),
	})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "close", testRoom, "qitem-gate",
		"--reason", "gate", "--note", verdict)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if !strings.Contains(stdout, verdict) {
		t.Errorf("stdout = %q, want the verdict the transition carries", stdout)
	}
	assertRequests(t, daemon, `{"action":"QueueClose","payload":{"input":{"qitem_id":"qitem-gate",`+
		`"actor_session":"`+testSeat+`","closure_reason":"gate","transition_note":"`+verdict+`"},`+
		`"room":"`+testRoom+`"}}`)
}

// THE UPDATE PATH IS THE SAME CLOSE: `--state done --closure-reason gate` with no note is refused there too,
// or the one requirement of a gate close could be stepped around by choosing another verb.
func TestRoomQueueUpdateRefusesAGateCloseWithoutAVerdict(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "update", testRoom, "qitem-gate",
		"--state", "done", "--closure-reason", "gate")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	if !strings.Contains(stderr, "verdict") {
		t.Errorf("stderr = %q, want it to name the missing verdict", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing: nothing was asked of the daemon", stdout)
	}
	assertRequests(t, daemon)
}

// TestRoomQueueUpdateWithANoteSendsAStatePreservingTransition pins the append: `--note` with no `--state`
// writes a transition whose frame has NO `state` key - the note is the point, not the move - and the note is
// rendered from the transition the daemon answered with, because it rides no field of the row.
func TestRoomQueueUpdateWithANoteSendsAStatePreservingTransition(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{
		"QueueUpdate": okData(queueRowFrame("qitem-x", "in-progress", "high", "hand me the build log",
			queueTransitionFrame(1, "2026-10-14T00:17:00Z", "", "pending", "lead@room", "created"),
			queueTransitionFrame(2, "2026-10-14T00:24:00Z", "in-progress", "in-progress", testSeat, "waiting on the log"))),
	})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "update", testRoom, "qitem-x",
		"--note", "waiting on the log")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := "room: " + testRoom + "@" + daemon.stateDir + " socket: " + daemon.socket + "\n" +
		"qitem-x in-progress high hand me the build log\n" +
		"2 2026-10-14T00:24:00Z in-progress -> in-progress by " + testSeat + " | waiting on the log\n"
	if stdout != want {
		t.Fatalf("stdout:\n got %q\nwant %q (stderr %q)", stdout, want, stderr)
	}
	assertRequests(t, daemon,
		`{"action":"QueueUpdate","payload":{"input":{"qitem_id":"qitem-x","actor_session":"`+testSeat+
			`","transition_note":"waiting on the log"},"room":"room-a"}}`)
}

// TestRoomQueueUpdateWithoutAnyChangeWritesNothing pins the no-op refusal: an update that would append a
// transition moving nothing, noting nothing and changing no field is refused BEFORE the request, so a rail
// nobody can read the record of never grows a step that says nothing.
func TestRoomQueueUpdateWithoutAnyChangeWritesNothing(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "update", testRoom, "qitem-x")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	if !strings.Contains(stderr, "nothing to change") {
		t.Errorf("stderr = %q, want the refusal named as a call that changes nothing", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing: nothing was asked of the daemon", stdout)
	}
	assertRequests(t, daemon)
}

// TestRoomQueueShowPrintsTheRowsFieldsThenItsTransitions pins the read a note is legible on: the row's own
// fields one per line (an absent pointer rendered as `-`, so a field the daemon did not send cannot be
// confused with one this verb dropped), then the transitions OLDEST FIRST, as the daemon orders them.
func TestRoomQueueShowPrintsTheRowsFieldsThenItsTransitions(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{
		"QueueShow": okData(queueRowFrame("qitem-x", "in-progress", "high", "hand me the build log",
			queueTransitionFrame(1, "2026-10-14T00:17:00Z", "", "pending", "lead@room", "created"),
			queueTransitionFrame(2, "2026-10-14T00:24:00Z", "pending", "in-progress", testSeat, "claimed"))),
	})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "show", testRoom, "qitem-x")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := "room: " + testRoom + "@" + daemon.stateDir + " socket: " + daemon.socket + "\n" +
		"qitem_id: qitem-x\n" +
		"state: in-progress\n" +
		"priority: high\n" +
		"summary: hand me the build log\n" +
		"body: -\n" +
		"source_session: -\n" +
		"destination_session: -\n" +
		"reply_to: -\n" +
		"blocked_on: -\n" +
		"wake_after_seconds: -\n" +
		"closure_reason: -\n" +
		"closure_target: -\n" +
		"tier: -\n" +
		"tags: -\n" +
		"chain_of_record: -\n" +
		"ts_created: -\n" +
		"ts_updated: -\n" +
		"transitions: 2\n" +
		"1 2026-10-14T00:17:00Z - -> pending by lead@room | created\n" +
		"2 2026-10-14T00:24:00Z pending -> in-progress by " + testSeat + " | claimed\n"
	if stdout != want {
		t.Fatalf("stdout:\n got %q\nwant %q (stderr %q)", stdout, want, stderr)
	}
	assertRequests(t, daemon, `{"action":"QueueShow","payload":{"qitem_id":"qitem-x","room":"room-a"}}`)
}

// TestRoomQueueRefusalsCarryTheRoomContractsCodes pins the two codes a daemon-side failure can carry: an
// OPERATION the daemon refused is 2 (the daemon did its part and the ask did not happen), while a room the
// daemon does not know is 3 (this room was not read at all). Both print the daemon's own sentence verbatim.
func TestRoomQueueRefusalsCarryTheRoomContractsCodes(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)

	const claimRefusal = "cannot claim 'qitem-x': current state is 'in-progress'"
	refused := newFakeDaemon(t, map[string]string{"QueueClaim": daemonError(claimRefusal)})
	code, stdout, stderr := runRoom(t, refused.args("queue", "claim", testRoom, "qitem-x")...)
	if code != clientcmd.ExitPartialStart {
		t.Fatalf("a refused claim: exit %d, want %d (stderr %q)", code, clientcmd.ExitPartialStart, stderr)
	}
	if !strings.Contains(stderr, claimRefusal) {
		t.Errorf("stderr = %q, want the daemon's own sentence %q", stderr, claimRefusal)
	}
	if !strings.Contains(stdout, "room: "+testRoom+"@"+refused.stateDir) {
		t.Errorf("stdout = %q, want the pair printed before the ask", stdout)
	}
	assertRequests(t, refused, `{"action":"QueueClaim","payload":{"actor_session":"`+testSeat+`","qitem_id":"qitem-x","room":"room-a"}}`)

	const roomRefusal = "no such room 'nope': /tmp/rooms/nope is not a room directory"
	t.Setenv(sessionEnv, testSeat)
	unknown := newFakeDaemon(t, map[string]string{"QueueList": daemonError(roomRefusal)})
	code, stdout, stderr = runRoom(t, unknown.args("queue", "list", "nope")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("a room the daemon does not know: exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	if !strings.Contains(stderr, roomRefusal) {
		t.Errorf("stderr = %q, want the daemon's own sentence %q", stderr, roomRefusal)
	}
	if !strings.Contains(stdout, "room: nope@"+unknown.stateDir) {
		t.Errorf("stdout = %q, want the pair for the room that was asked about", stdout)
	}
	assertRequests(t, unknown, `{"action":"QueueList","payload":{"filter":{"requester_session":"`+testSeat+`"},"room":"nope"}}`)
}

// THE 147 FAILED CALLS (measured 2026-10-18): a seat typed `queue show <qitem>` and the verb asked for a
// ROOM. ROOM now defaults to $AGENTHUB_ROOM, and these three cases are the whole contract of that default:
// the short form reaches the daemon as the seat's own room, an explicit room still wins, and a short form
// with no variable is refused HERE naming the variable - not by asking the daemon about a room named after
// the id, and not by a round trip that cannot answer.
func TestRoomQueueShowDefaultsTheRoomFromTheSeatEnv(t *testing.T) {
	t.Setenv(roomEnv, testRoom)
	daemon := newFakeDaemon(t, map[string]string{
		"QueueShow": okData(queueRowFrame("qitem-x", "in-progress", "high", "hand me the build log")),
	})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "show", "qitem-x")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if !strings.Contains(stdout, "room: "+testRoom+"@"+daemon.stateDir) {
		t.Errorf("stdout = %q, want the defaulted room printed as the pair", stdout)
	}
	assertRequests(t, daemon, `{"action":"QueueShow","payload":{"qitem_id":"qitem-x","room":"room-a"}}`)
}

func TestRoomQueueListDefaultsTheRoomAndAnExplicitRoomStillWins(t *testing.T) {
	t.Setenv(roomEnv, testRoom)
	t.Setenv(sessionEnv, "")
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[]")})

	if code, _, stderr := runRoom(t, daemon.args("queue", "list")...); code != clientcmd.ExitOK {
		t.Fatalf("the defaulted form: exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if code, stdout, stderr := runRoom(t, daemon.args("queue", "list", "another-room")...); code != clientcmd.ExitOK {
		t.Fatalf("the explicit form: exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	} else if !strings.Contains(stdout, "room: another-room@") {
		t.Errorf("stdout = %q, want the room the caller named, not the default", stdout)
	}
	assertRequests(t, daemon,
		`{"action":"QueueList","payload":{"filter":{},"room":"room-a"}}`,
		`{"action":"QueueList","payload":{"filter":{},"room":"another-room"}}`)
}

func TestRoomQueueWithNoRoomAndNoEnvRefusesNamingTheVariable(t *testing.T) {
	t.Setenv(roomEnv, "")
	daemon := newFakeDaemon(t, map[string]string{"QueueShow": okData(queueRowFrame("qitem-x", "pending", "", "x"))})

	code, stdout, stderr := runRoom(t, daemon.args("queue", "show", "qitem-x")...)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	if !strings.Contains(stderr, "$"+roomEnv+" is unset") {
		t.Errorf("stderr = %q, want the variable named", stderr)
	}
	if strings.Contains(stderr, "qitem-x is required") || strings.Contains(stderr, "ROOM is required") {
		t.Errorf("stderr = %q, want the default's refusal, not a missing-positional one", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing rendered for a call that reached no daemon", stdout)
	}
	assertRequests(t, daemon)
}

func TestQueueArgumentRefusalNamesTheMissingTail(t *testing.T) {
	if got := queueArgumentRefusal(nil, []string{"ROOM", "QITEM_ID"}); got != "a QITEM_ID is required" {
		t.Fatalf("ROOM defaults, so an empty argument list is missing the id, got %q", got)
	}
	if got := queueArgumentRefusal([]string{testRoom, "qitem-x", "extra"}, []string{"ROOM", "QITEM_ID"}); got != `unexpected argument "extra"` {
		t.Fatalf("a third positional is an extra, got %q", got)
	}
}

// TestRoomQueueListNamesTheSeatSoTheDaemonCanRateLimit pins that a seat's own list carries its session as
// requester_session: without it the daemon cannot tell a polling seat from its own internal reads.
func TestRoomQueueListNamesTheSeatSoTheDaemonCanRateLimit(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[]")})
	code, _, stderr := runRoom(t, daemon.args("queue", "list", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stderr %q)", code, stderr)
	}
	assertRequests(t, daemon, `{"action":"QueueList","payload":{"filter":{"requester_session":"`+testSeat+`"},"room":"room-a"}}`)
}

// TestQueueUsageStatesTheTypedRowRules pins that the help names the caps a create is refused on.
func TestQueueUsageStatesTheTypedRowRules(t *testing.T) {
	var out strings.Builder
	printQueueUsage(&out, "create")
	for _, want := range []string{"low|normal|high", "prod-break", "<= 120", "body <= 600", "no change since",
		"facts <= 280, rule <= 110, decision <= 100, next <= 78", "naming every over-cap line"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage lacks %q:\n%s", want, out.String())
		}
	}
}

// THE FOUR LOW-VALUE TAGS ARE RETIRED, and the help a seat reads must not offer them: the daemon refuses a
// create carrying one by name, and a usage line that still listed it would send the seat to a refusal. The
// list is the client's copy of the daemon's, so it is pinned here as the 14 that remain - a row created
// before the retirement keeps its tag; only new creates are refused.
func TestTheRetiredTagsLeaveTheListAndTheUsage(t *testing.T) {
	want := []string{"prod-break", "gate", "ruling", "owner-decision", "finding", "guide", "docs",
		"defect", "skill-check", "landing", "assignment", "handoff", "board", "reseat"}
	if !slices.Equal(clientruntime.QueueTags, want) {
		t.Fatalf("QueueTags = %q, want the 14 the daemon accepts", clientruntime.QueueTags)
	}
	var out strings.Builder
	printQueueUsage(&out, "create")
	for _, retired := range []string{"status", "ack", "checkpoint", "report"} {
		if slices.Contains(clientruntime.QueueTags, retired) {
			t.Errorf("QueueTags still carries the retired tag %q", retired)
		}
		if strings.Contains(out.String(), ", "+retired+",") || strings.Contains(out.String(), retired+",") {
			t.Errorf("the usage still offers the retired tag %q:\n%s", retired, out.String())
		}
	}
}
