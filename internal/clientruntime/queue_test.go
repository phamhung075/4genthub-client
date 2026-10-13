package clientruntime

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The rail's frames are pinned here as the exact line the client writes, captured at a real Unix socket - the
// same path `Call` uses. The daemon deserializes one trimmed line per request, so the LINE is the contract:
// an absent key, a null key and an empty string are three different requests, and only the bytes on the wire
// tell them apart. Asserting on a Go struct instead would pin the encoder's opinion rather than the frame.

const queueTestRoom = "room-a"

// queueTestRowJSON is a row in the shape the daemon serializes one: the required columns present, `reply_to`
// and `tier` null (Rust's None), `closure_reason` present, and two transitions - the first spelling absence as
// `null` and the second by omitting the key, which is the whole reason those Options are pointers.
const queueTestRowJSON = `{` +
	`"qitem_id":"q-item-7","state":"in-progress","summary":"wire the rail","body":"body text",` +
	`"source_session":"sess-src","destination_session":"sess-dst","chain_of_record":["q-item-1"],` +
	`"reply_to":null,"blocked_on":"q-item-2","wake_after_seconds":30,"closure_reason":"denied",` +
	`"closure_target":"sess-dst","priority":"normal","tags":["ipc","queue"],"tier":null,` +
	`"ts_created":"2026-10-14T00:00:00Z","ts_updated":"2026-10-14T00:00:01Z","transitions":[` +
	`{"transition_id":1,"qitem_id":"q-item-7","ts":"2026-10-14T00:00:00Z","state":"pending",` +
	`"from_state":null,"actor_session":"sess-src","transition_note":"created",` +
	`"closure_reason":null,"closure_target":null},` +
	`{"transition_id":2,"qitem_id":"q-item-7","ts":"2026-10-14T00:00:01Z","state":"in-progress",` +
	`"actor_session":"sess-dst","transition_note":"claimed"}` +
	`]}`

// captureQueueRequest answers exactly one call: it accepts one connection, reads the request line the client
// writes, hands that line to the test, and replies with the canned response (which must end in a newline).
//
// The socket lives in a short temp dir rather than in t.TempDir(): a Unix socket path is capped at about 108
// bytes and t.TempDir() spells out the test's own name, so a repo-local TMPDIR - which is how the build here
// is run - would spend the whole budget before the socket name and fail with "file name too long".
func captureQueueRequest(t *testing.T, response string) (*Client, <-chan string) {
	t.Helper()

	dir, err := os.MkdirTemp("", "queueframe")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	requests := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(requests)
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			close(requests)
			return
		}
		requests <- string(line)
		_, _ = conn.Write([]byte(response))
	}()

	return NewClient(sock), requests
}

// okDataResponse frames one successful response the way the daemon does: the row (or rows) under `data`.
func okDataResponse(data string) string {
	return `{"ok":true,"data":` + data + "}\n"
}

// assertQueueFrame compares the line the client wrote against the one the contract names. The verb's own line
// is pinned per test rather than composed here, so a change to any key fails in the test that names it. The
// terminating newline is pinned too - the daemon reads one line per request - but it is asserted once rather
// than repeated in every expected string.
func assertQueueFrame(t *testing.T, requests <-chan string, want string) {
	t.Helper()

	select {
	case line, ok := <-requests:
		if !ok {
			t.Fatal("the client never wrote a request line")
		}
		if !strings.HasSuffix(line, "\n") {
			t.Fatalf("request frame is not newline-terminated: %q", line)
		}
		if got := strings.TrimSuffix(line, "\n"); got != want {
			t.Fatalf("request frame:\n got %q\nwant %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the client's request line")
	}
}

// assertQueueTestRow checks the fields a decode must not flatten: a null Option stays nil, an absent one stays
// nil, a present one arrives as its value, and the transitions survive with their ids and notes.
func assertQueueTestRow(t *testing.T, row *QueueRow) {
	t.Helper()

	if row == nil {
		t.Fatal("no row decoded")
	}
	if row.QitemID != "q-item-7" || row.State != "in-progress" {
		t.Fatalf("qitem_id/state = %q/%q, want q-item-7/in-progress", row.QitemID, row.State)
	}
	if row.Priority != "normal" || len(row.Tags) != 2 || row.Tags[0] != "ipc" {
		t.Fatalf("priority/tags = %q/%v, want normal/[ipc queue]", row.Priority, row.Tags)
	}
	if len(row.ChainOfRecord) != 1 || row.ChainOfRecord[0] != "q-item-1" {
		t.Fatalf("chain_of_record = %v, want [q-item-1]", row.ChainOfRecord)
	}
	if row.ReplyTo != nil {
		t.Fatalf("a null reply_to decoded to %q, want nil", *row.ReplyTo)
	}
	if row.Tier != nil {
		t.Fatalf("a null tier decoded to %q, want nil", *row.Tier)
	}
	if row.BlockedOn == nil || *row.BlockedOn != "q-item-2" {
		t.Fatalf("blocked_on = %v, want q-item-2", row.BlockedOn)
	}
	if row.WakeAfterSeconds == nil || *row.WakeAfterSeconds != 30 {
		t.Fatalf("wake_after_seconds = %v, want 30", row.WakeAfterSeconds)
	}
	if row.ClosureReason == nil || *row.ClosureReason != "denied" {
		t.Fatalf("closure_reason = %v, want denied", row.ClosureReason)
	}
	if len(row.Transitions) != 2 {
		t.Fatalf("transitions = %d, want 2", len(row.Transitions))
	}
	first, second := row.Transitions[0], row.Transitions[1]
	if first.TransitionID != 1 || first.TransitionNote != "created" || first.ActorSession != "sess-src" {
		t.Fatalf("transition[0] = %+v, want id 1, note created, actor sess-src", first)
	}
	if first.FromState != nil || first.ClosureReason != nil || first.ClosureTarget != nil {
		t.Fatalf("transition[0] null Options decoded to %v/%v/%v, want nil", first.FromState, first.ClosureReason, first.ClosureTarget)
	}
	if second.TransitionID != 2 || second.ActorSession != "sess-dst" || second.TransitionNote != "claimed" {
		t.Fatalf("transition[1] = %+v, want id 2, actor sess-dst, note claimed", second)
	}
	if second.FromState != nil || second.ClosureTarget != nil {
		t.Fatalf("transition[1] omitted Options decoded to %v/%v, want nil/nil", second.FromState, second.ClosureTarget)
	}
}

func TestQueueCreatePinsTheContractedFrame(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	priority, tier, replyTo, blockedOn, reuse := "high", "T1", "q-item-1", "q-item-2", "q-item-7"
	zero := int64(0)

	row, err := client.QueueCreate(queueTestRoom, QueueCreateInput{
		SourceSession:      "sess-src",
		DestinationSession: "sess-dst",
		Summary:            "wire the rail",
		Body:               "body text",
		Priority:           &priority,
		Tags:               []string{"ipc", "queue"},
		Tier:               &tier,
		ReplyTo:            &replyTo,
		ChainOfRecord:      []string{"q-item-1"},
		BlockedOn:          &blockedOn,
		// A pointer to zero is SET, not empty: omitempty must not drop a wake of 0 seconds.
		WakeAfterSeconds: &zero,
		QitemID:          &reuse,
	})
	if err != nil {
		t.Fatalf("QueueCreate: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueCreate","payload":{"input":{`+
		`"source_session":"sess-src","destination_session":"sess-dst","summary":"wire the rail",`+
		`"body":"body text","priority":"high","tags":["ipc","queue"],"tier":"T1","reply_to":"q-item-1",`+
		`"chain_of_record":["q-item-1"],"blocked_on":"q-item-2","wake_after_seconds":0,`+
		`"qitem_id":"q-item-7"},"room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

// TestQueueCreateOmitsWhatTheDaemonDefaults: every field Rust declared with a default or as an Option is
// absent when it is unset, so the frame a caller with only the required fields writes is exactly those three
// keys - the difference between "not set" and "set to empty" survives the wire.
func TestQueueCreateOmitsWhatTheDaemonDefaults(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	row, err := client.QueueCreate(queueTestRoom, QueueCreateInput{
		SourceSession:      "sess-src",
		DestinationSession: "sess-dst",
		Summary:            "minimal",
	})
	if err != nil {
		t.Fatalf("QueueCreate: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueCreate","payload":{"input":{`+
		`"source_session":"sess-src","destination_session":"sess-dst","summary":"minimal"},`+
		`"room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

func TestQueueListPinsTheContractedFrame(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse("["+queueTestRowJSON+"]"))

	destination := "sess-dst"
	limit := int64(25)

	rows, err := client.QueueList(queueTestRoom, QueueListFilter{
		DestinationSession: &destination,
		State:              []string{"pending", "blocked"},
		ActiveOnly:         true,
		Limit:              &limit,
	})
	if err != nil {
		t.Fatalf("QueueList: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueList","payload":{"filter":{`+
		`"destination_session":"sess-dst","state":["pending","blocked"],"active_only":true,"limit":25},`+
		`"room":"room-a"}}`)

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	assertQueueTestRow(t, &rows[0])
}

// TestQueueListCarriesTheFilterEvenWhenItIsEmpty pins the one key the daemon requires rather than defaults: a
// zero filter still rides the frame, and an empty answer is an empty slice rather than a nil one - a room with
// no rows is not a room that was not read.
func TestQueueListCarriesTheFilterEvenWhenItIsEmpty(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(`[]`))

	rows, err := client.QueueList(queueTestRoom, QueueListFilter{})
	if err != nil {
		t.Fatalf("QueueList: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueList","payload":{"filter":{},"room":"room-a"}}`)

	if rows == nil {
		t.Fatal("an empty queue decoded to nil, want an empty slice")
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
}

func TestQueueShowPinsTheContractedFrame(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	row, err := client.QueueShow(queueTestRoom, "q-item-7")
	if err != nil {
		t.Fatalf("QueueShow: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueShow","payload":{"qitem_id":"q-item-7","room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

func TestQueueUpdatePinsTheContractedFrame(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	state, note, reason, target, blockedOn, summary := "done", "finished", "no-follow-on", "sess-dst", "q-item-2", "renamed"
	wake := int64(30)

	row, err := client.QueueUpdate(queueTestRoom, QueueUpdateInput{
		QitemID:          "q-item-7",
		ActorSession:     "sess-src",
		State:            &state,
		TransitionNote:   &note,
		ClosureReason:    &reason,
		ClosureTarget:    &target,
		BlockedOn:        &blockedOn,
		WakeAfterSeconds: &wake,
		Summary:          &summary,
	})
	if err != nil {
		t.Fatalf("QueueUpdate: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueUpdate","payload":{"input":{`+
		`"qitem_id":"q-item-7","actor_session":"sess-src","state":"done","transition_note":"finished",`+
		`"closure_reason":"no-follow-on","closure_target":"sess-dst","blocked_on":"q-item-2",`+
		`"wake_after_seconds":30,"summary":"renamed"},"room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

// TestQueueUpdateNoteAloneOmitsTheRest: a note with no state is a state-preserving append, so the frame is
// the identity and the note - no `"state":""`, which the daemon would have to read as a state it does not have.
func TestQueueUpdateNoteAloneOmitsTheRest(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	note := "still waiting on the schema"
	row, err := client.QueueUpdate(queueTestRoom, QueueUpdateInput{
		QitemID:        "q-item-7",
		ActorSession:   "sess-src",
		TransitionNote: &note,
	})
	if err != nil {
		t.Fatalf("QueueUpdate: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueUpdate","payload":{"input":{`+
		`"qitem_id":"q-item-7","actor_session":"sess-src","transition_note":"still waiting on the schema"},`+
		`"room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

func TestQueueClaimPinsTheContractedFrame(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	row, err := client.QueueClaim(queueTestRoom, "q-item-7", "sess-dst")
	if err != nil {
		t.Fatalf("QueueClaim: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueClaim","payload":{`+
		`"actor_session":"sess-dst","qitem_id":"q-item-7","room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

func TestQueueClosePinsTheContractedFrame(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	target, note := "sess-dst", "handed off"
	row, err := client.QueueClose(queueTestRoom, QueueCloseInput{
		QitemID:        "q-item-7",
		ActorSession:   "sess-src",
		ClosureReason:  "handed_off_to",
		ClosureTarget:  &target,
		TransitionNote: note,
	})
	if err != nil {
		t.Fatalf("QueueClose: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueClose","payload":{"input":{`+
		`"qitem_id":"q-item-7","actor_session":"sess-src","closure_reason":"handed_off_to",`+
		`"closure_target":"sess-dst","transition_note":"handed off"},"room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

// TestQueueCloseReasonRidesUnconditionally: the reason is the one field the daemon refuses to default, so it
// rides the frame even when the caller wrote an empty or unknown string - the refusal belongs to the daemon,
// and a client that quietly dropped the key would turn it into a different error.
func TestQueueCloseReasonRidesUnconditionally(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(queueTestRowJSON))

	row, err := client.QueueClose(queueTestRoom, QueueCloseInput{
		QitemID:       "q-item-7",
		ActorSession:  "sess-src",
		ClosureReason: "canceled",
	})
	if err != nil {
		t.Fatalf("QueueClose: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"QueueClose","payload":{"input":{`+
		`"qitem_id":"q-item-7","actor_session":"sess-src","closure_reason":"canceled"},`+
		`"room":"room-a"}}`)

	assertQueueTestRow(t, row)
}

// TestListAllRoomsPinsTheContractedFrame pins the null payload: the daemon's frame is adjacently tagged, so
// this variant's own serialization is `"payload":null` rather than a missing key, and the client sends that.
func TestListAllRoomsPinsTheContractedFrame(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(`["room-a","room-b"]`))

	rooms, err := client.ListAllRooms()
	if err != nil {
		t.Fatalf("ListAllRooms: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"ListAllRooms","payload":null}`)

	if len(rooms) != 2 || rooms[0] != "room-a" || rooms[1] != "room-b" {
		t.Fatalf("rooms = %v, want [room-a room-b]", rooms)
	}
}

func TestListAllRoomsReadsAnEmptyAnswerAsEmpty(t *testing.T) {
	client, requests := captureQueueRequest(t, okDataResponse(`[]`))

	rooms, err := client.ListAllRooms()
	if err != nil {
		t.Fatalf("ListAllRooms: %v", err)
	}

	assertQueueFrame(t, requests, `{"action":"ListAllRooms","payload":null}`)

	if rooms == nil || len(rooms) != 0 {
		t.Fatalf("rooms = %v, want an empty slice", rooms)
	}
}

// TestQueueWriteReportsTheDaemonsRefusal: a refusal is the daemon's sentence, not a zero row, because a
// caller that read a zero row as an answer would believe a write that never happened.
func TestQueueWriteReportsTheDaemonsRefusal(t *testing.T) {
	refusal := `{"ok":false,"error":"cannot claim 'q-item-7': current state is 'done'"}` + "\n"
	client, requests := captureQueueRequest(t, refusal)

	row, err := client.QueueClaim(queueTestRoom, "q-item-7", "sess-dst")

	assertQueueFrame(t, requests, `{"action":"QueueClaim","payload":{`+
		`"actor_session":"sess-dst","qitem_id":"q-item-7","room":"room-a"}}`)

	if row != nil {
		t.Fatalf("a refused claim answered with a row: %+v", row)
	}
	if err == nil || !strings.Contains(err.Error(), "cannot claim 'q-item-7'") {
		t.Fatalf("err = %v, want the daemon's refusal", err)
	}
}
