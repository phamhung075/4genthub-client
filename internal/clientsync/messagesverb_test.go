// The parts of `sync messages` that can be proven without the real stack: the argument grammar, the
// ledger, and — against a SCRIPTED cloud — the ORDER the verb's three steps happen in. The order is the
// contract: the text is typed first, the ledger records it second, and the ack is sent last, so an
// interruption after the typing redelivers (safe) and an interruption before the ack does not type the
// same text twice (the one duplicate at-least-once allows, and what the ledger is for).
//
// What this does NOT prove, stated rather than implied: that the real server's pull and ack behave as
// scripted here (that is the httpapp package's tests, against the real handlers), and that a real daemon
// send lands in a real seat's pane (the stub daemon proves the request the verb makes, not the pane it
// reaches — that is the row's end-to-end check).
package clientsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// messageCloud is the two endpoints the verb calls, scripted, and it records every call in order so a
// test can assert the SEQUENCE rather than only the outcome.
type messageCloud struct {
	mu      sync.Mutex
	pending []map[string]any
	// bySeat, when set, serves one pending list PER (room, seat) the GET names - what the rig-shape
	// tests need, where one pass pulls several different seats' mailboxes. Left nil, every GET is
	// answered with `pending`, which is what the single-seat tests below were written against.
	bySeat map[string][]map[string]any
	refuse map[string]bool // ids whose ack is refused
	calls  []string
}

func (c *messageCloud) record(call string) {
	c.calls = append(c.calls, call)
}

func (c *messageCloud) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.calls...)
}

func (c *messageCloud) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/openrig/rooms/{room}/seats/{seat}/messages", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.bySeat == nil {
			c.record("GET")
		} else {
			c.record("GET " + r.PathValue("room") + "/" + r.PathValue("seat"))
		}
		messages := c.pending
		if c.bySeat != nil {
			messages = c.bySeat[r.PathValue("room")+"/"+r.PathValue("seat")]
		}
		if messages == nil {
			messages = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "messages": messages, "cursor": "2026-10-10T09:00:00Z|m9",
		})
	})
	mux.HandleFunc("POST /api/v2/openrig/rooms/{room}/seats/{seat}/messages/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		id := r.PathValue("id")
		c.record("ACK " + id)
		if c.refuse[id] {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":"seat message is not pending: unknown id, or already delivered"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "delivered": true})
	})
	return mux
}

// messagesFixture wires a scripted cloud, a recorded local send, and a temporary seat store.
func messagesFixture(t *testing.T, cloud *messageCloud, sendErr error) (*messageCloud, *[]string, string) {
	t.Helper()
	server := httptest.NewServer(cloud.handler())
	t.Cleanup(server.Close)
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "test-token")
	out := t.TempDir()

	var sent []string
	previous := sendToSession
	sendToSession = func(_ context.Context, session, text string) error {
		cloud.mu.Lock()
		cloud.record("SEND " + session + " " + text)
		cloud.mu.Unlock()
		sent = append(sent, session+" "+text)
		return sendErr
	}
	t.Cleanup(func() { sendToSession = previous })
	return cloud, &sent, out
}

func message(id, text string) map[string]any {
	return map[string]any{"id": id, "room": "dev", "seat": "coder", "text": text, "created_at": "2026-10-10T09:00:00Z"}
}

// THE DELIVERY ORDER, which is the whole point: type the text, then record it, then acknowledge it.
func TestMessagesVerbDeliversThenRecordsThenAcknowledges(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello"), message("m2", "again")}}
	_, sent, out := messagesFixture(t, cloud, nil)

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--session", "rig-seat", "--out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if got := strings.Join(*sent, ","); got != "rig-seat hello,rig-seat again" {
		t.Fatalf("typed %q, want both messages in the order the pull returned them", got)
	}
	want := []string{"GET", "SEND rig-seat hello", "ACK m1", "SEND rig-seat again", "ACK m2"}
	if got := strings.Join(cloud.seen(), " | "); got != strings.Join(want, " | ") {
		t.Fatalf("calls = %s\nwant    %s", got, strings.Join(want, " | "))
	}
	// And the ledger holds both, which is what a later run reads before typing anything.
	typed, err := readMessagesLedger(messagesLedgerPath(out, "dev", "coder"))
	if err != nil {
		t.Fatal(err)
	}
	if len(typed) != 2 {
		t.Errorf("ledger = %v, want both ids", typed)
	}
	if !strings.Contains(stdout.String(), "2 delivered, 0 re-acked, 0 failed") {
		t.Errorf("report = %q, want the counts", stdout.String())
	}
}

// A SEND THAT FAILED IS NOT ACKNOWLEDGED AND NOT RECORDED: the message stays pending in the cloud, so
// the next run delivers it, which is the failure at-least-once exists to tolerate.
func TestMessagesVerbDoesNotAcknowledgeWhatItDidNotDeliver(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello")}}
	_, _, out := messagesFixture(t, cloud, context.DeadlineExceeded)

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--session", "rig-seat", "--out", out}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit = %d, want ExitRemote: %s", code, stderr.String())
	}
	for _, call := range cloud.seen() {
		if strings.HasPrefix(call, "ACK") {
			t.Fatalf("a message that was never typed was acknowledged: %v", cloud.seen())
		}
	}
	typed, err := readMessagesLedger(messagesLedgerPath(out, "dev", "coder"))
	if err != nil {
		t.Fatal(err)
	}
	if len(typed) != 0 {
		t.Errorf("ledger = %v, want nothing recorded for a message that did not land", typed)
	}
	if !strings.Contains(stderr.String(), "putting m1 into session rig-seat failed") {
		t.Errorf("stderr = %q, want the failing message named", stderr.String())
	}
}

// A MESSAGE ALREADY IN THE LEDGER IS RE-ACKED, NEVER TYPED TWICE — this is the duplicate that
// at-least-once permits, and the ledger is what removes it.
func TestMessagesVerbReAcksWhatItAlreadyTyped(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello")}}
	_, sent, out := messagesFixture(t, cloud, nil)
	if err := appendMessagesLedger(messagesLedgerPath(out, "dev", "coder"), "m1"); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--session", "rig-seat", "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if len(*sent) != 0 {
		t.Fatalf("typed %v, want nothing typed for an id the ledger already holds", *sent)
	}
	want := []string{"GET", "ACK m1"}
	if got := strings.Join(cloud.seen(), " | "); got != strings.Join(want, " | ") {
		t.Fatalf("calls = %s\nwant    %s", got, strings.Join(want, " | "))
	}
	if !strings.Contains(stdout.String(), "0 delivered, 1 re-acked") {
		t.Errorf("report = %q, want the re-ack counted", stdout.String())
	}
}

// THE WINDOW THE LEDGER COVERS: the text landed but the ack failed. The ledger must ALREADY hold the id
// at that moment, or the next run would type it again.
func TestMessagesVerbRecordsTheDeliveryBeforeTheAckCanFail(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello")}, refuse: map[string]bool{"m1": true}}
	_, sent, out := messagesFixture(t, cloud, nil)

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--session", "rig-seat", "--out", out}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit = %d, want ExitRemote: %s", code, stderr.String())
	}
	if len(*sent) != 1 {
		t.Fatalf("typed %v, want the one message", *sent)
	}
	typed, err := readMessagesLedger(messagesLedgerPath(out, "dev", "coder"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := typed["m1"]; !ok {
		t.Fatalf("ledger = %v, want m1 recorded even though the ack was refused", typed)
	}
	if !strings.Contains(stderr.String(), "reached session rig-seat but the ack failed") {
		t.Errorf("stderr = %q, want the delivered-but-unacked case named", stderr.String())
	}
}

// --dry-run SENDS AND ACKNOWLEDGES NOTHING, and it does not even need a session to resolve.
func TestMessagesVerbDryRunSendsNothing(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello\nthere")}}
	_, sent, out := messagesFixture(t, cloud, nil)

	var stdout, stderr strings.Builder
	if code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--dry-run", "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if len(*sent) != 0 {
		t.Fatalf("a dry run typed %v", *sent)
	}
	if got := strings.Join(cloud.seen(), " | "); got != "GET" {
		t.Fatalf("calls = %s, want the pull alone", got)
	}
	if !strings.Contains(stdout.String(), "would deliver m1: hello there") {
		t.Errorf("report = %q, want the flattened text of the message", stdout.String())
	}
}

// The verb refuses before it pulls when it cannot deliver at all: without the credential there
// is nothing to pull WITH, and an EMPTY cloud is reported as empty rather than as a silent success.
func TestMessagesVerbRefusalsAndTheEmptyCase(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		t.Setenv("AGENTHUB_URL", "https://api.example.test")
		t.Setenv("AGENTHUB_TOKEN", "")
		var stdout, stderr strings.Builder
		code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--dry-run", "--out", t.TempDir()}, &stdout, &stderr)
		if code != clientcmd.ExitUsage {
			t.Fatalf("exit = %d, want ExitUsage: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "AGENTHUB_TOKEN") {
			t.Errorf("stderr = %q, want the missing credential named", stderr.String())
		}
	})

	t.Run("usage needs a room and a seat", func(t *testing.T) {
		for _, args := range [][]string{{}, {"dev"}, {"dev", "coder", "extra"}, {"dev", "coder", "--limit", "0"}, {"dev", "coder", "--nope"}} {
			var stdout, stderr strings.Builder
			if code := RunMessagesVerb(context.Background(), args, &stdout, &stderr); code != clientcmd.ExitUsage {
				t.Errorf("args %v: exit = %d, want ExitUsage", args, code)
			}
			if !strings.Contains(stderr.String(), "usage") {
				t.Errorf("args %v: stderr = %q, want the usage line", args, stderr.String())
			}
		}
	})

	t.Run("an empty seat says so", func(t *testing.T) {
		cloud := &messageCloud{}
		_, _, out := messagesFixture(t, cloud, nil)
		var stdout, stderr strings.Builder
		if code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--session", "rig-seat", "--out", out}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "nothing is waiting") {
			t.Errorf("stdout = %q, want the empty case said plainly", stdout.String())
		}
	})
}

// The page size is the caller's, and the cursor the cloud returns is reported rather than consumed:
// nothing here walks to the second page, so a caller can see what is left behind.
func TestMessagesVerbAsksForThePageItWasGiven(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello")}}
	_, _, out := messagesFixture(t, cloud, nil)

	var stdout, stderr strings.Builder
	if code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--session", "rig-seat", "--limit", "7", "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "cursor 2026-10-10T09:00:00Z|m9") {
		t.Errorf("stdout = %q, want the opaque cursor echoed to the caller", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(out, "delivered")); err != nil {
		t.Errorf("the ledger directory was not created: %v", err)
	}
}

// fakeRoom answers the verb's seat discovery from the RUNTIME seam rather than from a stub `rig` on PATH, and the
// difference is the point: these tests no longer need a fake binary, because the verb no longer runs one. The seats
// are given in the order the tests expect the pass to take them.
//
// IT ALSO EMPTIES PATH, and that is what makes these tests able to fail: with the stub `rig` gone and no tool on PATH,
// a pass that went back to reading the node table cannot succeed, so a reversion is a RED test rather than a silent
// delivery through the live rig this machine happens to be running.
func fakeRoom(t *testing.T, seats ...seatSession) {
	t.Helper()
	previous := roomSeats
	t.Cleanup(func() { roomSeats = previous })
	roomSeats = func(room string) ([]seatSession, error) { return seats, nil }
	t.Setenv("PATH", t.TempDir())
}

// THE DELIVERY GOES TO THE DAEMON, AND THE ENVELOPE IS THE CLI'S OWN. This test runs the REAL sendToSession - the
// fixture's substitute is taken back - against a stub daemon, with no `rig` on PATH. A reversion to the exec fails
// twice over: the tool is not on PATH, and nothing would reach the stub.
//
// The sender is the point of the assertions: the deliverer runs outside any seat shell, and the decoy session in
// OPENRIG_SESSION_NAME is what a caller who inherited its identity would write. The envelope must name the human.
func TestMessagesVerbDeliversThroughTheDaemonWithTheCLIEnvelope(t *testing.T) {
	const decoy = "4genthub-min-go-dev@4genthub-min"
	var got struct {
		Session string `json:"session"`
		Text    string `json:"text"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/transport/send" {
			t.Errorf("path = %q, want the transport send route", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("request body = %s, want the send payload: %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"sessionName":"4genthub-min-lead@4genthub-min","outcome":"delivered","verified":true}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", decoy)
	t.Setenv("PATH", t.TempDir())

	cloud := &messageCloud{bySeat: map[string][]map[string]any{
		"4genthub-min/lead": {message("m1", "hello lead")},
	}}
	realDelivery := sendToSession
	_, _, out := messagesFixture(t, cloud, nil)
	sendToSession = realDelivery
	fakeRoom(t, seatSession{seat: "lead", session: "4genthub-min-lead@4genthub-min"})

	var stdout, stderr strings.Builder
	if code := RunMessagesVerb(context.Background(), []string{"--rig", "4genthub-min", "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if got.Session != "4genthub-min-lead@4genthub-min" {
		t.Errorf("delivered to session %q, want the seat's session", got.Session)
	}
	if !strings.Contains(got.Text, "hello lead") {
		t.Errorf("envelope = %q, want the message body in it", got.Text)
	}
	if !strings.Contains(got.Text, "From: "+humanSeatSession) {
		t.Errorf("envelope = %q, want it written AS %s", got.Text, humanSeatSession)
	}
	if strings.Contains(got.Text, decoy) {
		t.Errorf("envelope = %q, want the deliverer's own session absent from the sender", got.Text)
	}
	if !strings.Contains(stdout.String(), "4genthub-min/lead: 1 delivered, 0 re-acked, 0 failed") {
		t.Errorf("report = %q, want the delivery counted", stdout.String())
	}
}

// seatSessionsFrom carries the skip rule, and it is proven here rather than only through a room: a seat that is not
// running is skipped, whatever the reason - and the runtime's own death states are reasons too, so their mail waits
// with the stopped seat's instead of being typed into a prompt that is not reading.
func TestSeatSessionsFromSkipsEverySeatWithoutALivePrompt(t *testing.T) {
	statuses := []clientruntime.SeatStatus{
		{Seat: "lead", SessionName: "4genthub-min-lead@4genthub-min", State: "running"},
		{Seat: "stopped", SessionName: "4genthub-min-stopped@4genthub-min", State: "stopped"},
		{Seat: "dead", SessionName: "4genthub-min-dead@4genthub-min", State: "agent_socket_closed"},
		{Seat: "eof", SessionName: "4genthub-min-eof@4genthub-min", State: "agent_stdio_eof"},
		{Seat: "exited", SessionName: "4genthub-min-exited@4genthub-min", State: "agent_exited"},
		{Seat: "ghost", SessionName: "", State: "running"},
		{Seat: "", SessionName: "4genthub-min-nameless@4genthub-min", State: "running"},
		{Seat: "other", SessionName: "4genthub-min-other@4genthub-min", State: "running"},
		// The runtime's word is `running`, but a padded or differently-cased one is still that word: the sibling
		// consumer (clientlifecycle/lifecycle.go) trims and folds before comparing, and matching it here is what
		// keeps a formatting difference from skipping EVERY seat - which would deliver nothing, silently.
		{Seat: "padded", SessionName: "4genthub-min-padded@4genthub-min", State: "  Running "},
	}
	got := seatSessionsFrom(statuses)
	want := []seatSession{
		{seat: "lead", session: "4genthub-min-lead@4genthub-min"},
		{seat: "other", session: "4genthub-min-other@4genthub-min"},
		{seat: "padded", session: "4genthub-min-padded@4genthub-min"},
	}
	if len(got) != len(want) {
		t.Fatalf("seats = %v, want every seat whose state reads running, normalised: %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("seat %d = %v, want %v (order included: the pass takes seats in the room's own order)", i, got[i], want[i])
		}
	}
}

// THE FLAG IS NOW REQUIRED, AND THE REFUSAL IS THE INTERFACE. This case used to resolve the session from
// `rig ps --json` - refusing only when several candidates were running - and that whole path was a shell-out to the
// CLI the cutover deletes, so it is gone rather than repaired. The user-visible contract is the message, so the
// message names BOTH ways to say where a message goes and what each takes.
//
// THE PROOF IS THE EMPTY PATH: the old resolution could not produce this answer without asking the rig, so with no
// `rig` anywhere it failed with a dial-shaped error. This refusal is produced without running anything.
func TestMessagesVerbRequiresAFlagAndSaysSoWithoutShellingOut(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello")}}
	_, sent, out := messagesFixture(t, cloud, nil)
	t.Setenv("PATH", t.TempDir())

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--out", out}, &stdout, &stderr)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit = %d, want ExitUsage: %s", code, stderr.String())
	}
	if len(*sent) != 0 {
		t.Fatalf("typed %v, want NOTHING typed when no prompt was named", *sent)
	}
	if calls := cloud.seen(); len(calls) != 0 {
		t.Fatalf("calls = %v, want the cloud untouched: the refusal happens before the pull", calls)
	}
	for _, want := range []string{"--rig <room>", "--session <name>"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to name %q and what it takes", stderr.String(), want)
		}
	}
}

// THE SUPERVISED SHAPE: `--rig` passes over every RUNNING seat of the rig, types each seat's mail into
// that seat's OWN canonical session, and skips a stopped seat - or a node with no session - rather than
// acknowledging text that no prompt will ever read.
func TestMessagesVerbRigPassDeliversEveryRunningSeat(t *testing.T) {
	cloud := &messageCloud{bySeat: map[string][]map[string]any{
		"4genthub-min/lead":    {message("m1", "hello lead")},
		"4genthub-min/other":   {message("m2", "hello other")},
		"4genthub-min/stopped": {message("m3", "stays pending")},
	}}
	_, sent, out := messagesFixture(t, cloud, nil)
	fakeRoom(t,
		seatSession{seat: "lead", session: "4genthub-min-lead@4genthub-min"},
		seatSession{seat: "other", session: "4genthub-min-other@4genthub-min"},
	)

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(context.Background(), []string{"--rig", "4genthub-min", "--out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	want := []string{
		"4genthub-min-lead@4genthub-min hello lead",
		"4genthub-min-other@4genthub-min hello other",
	}
	if got := strings.Join(*sent, ","); got != strings.Join(want, ",") {
		t.Fatalf("typed %q, want each running seat's own canonical session, in node order", got)
	}
	wantCalls := "GET 4genthub-min/lead | SEND 4genthub-min-lead@4genthub-min hello lead | ACK m1 | " +
		"GET 4genthub-min/other | SEND 4genthub-min-other@4genthub-min hello other | ACK m2"
	if got := strings.Join(cloud.seen(), " | "); got != wantCalls {
		t.Fatalf("calls = %s\nwant    %s", got, wantCalls)
	}
	// The stopped seat and the sessionless node were never even pulled: their mail stays pending.
	for _, seat := range []string{"stopped", "ghost"} {
		if _, err := os.Stat(messagesLedgerPath(out, "4genthub-min", seat)); err == nil {
			t.Errorf("a ledger exists for %s, which has no prompt to read it", seat)
		}
	}
	for seat, want := range map[string]string{"lead": "m1", "other": "m2"} {
		typed, err := readMessagesLedger(messagesLedgerPath(out, "4genthub-min", seat))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := typed[want]; !ok {
			t.Errorf("%s ledger = %v, want %s recorded", seat, typed, want)
		}
	}
	if !strings.Contains(stdout.String(), "4genthub-min/lead: 1 delivered, 0 re-acked, 0 failed") {
		t.Errorf("report = %q, want per-seat counts", stdout.String())
	}
}

// ONE SEAT'S FAILURE DOES NOT STOP THE OTHER SEATS. The pass keeps going - a broken prompt must not
// starve every other mailbox - the failed message stays pending for the next pass, and a ONE-SHOT run
// exits non-zero so a human sees that a seat is not receiving.
func TestMessagesVerbRigPassIsolatesOneSeatFailure(t *testing.T) {
	cloud := &messageCloud{bySeat: map[string][]map[string]any{
		"4genthub-min/lead":  {message("m1", "hello lead")},
		"4genthub-min/other": {message("m2", "hello other")},
	}}
	_, _, out := messagesFixture(t, cloud, nil)
	previous := sendToSession
	sendToSession = func(_ context.Context, session, _ string) error {
		if strings.Contains(session, "lead") {
			return context.DeadlineExceeded
		}
		return nil
	}
	t.Cleanup(func() { sendToSession = previous })
	fakeRoom(t,
		seatSession{seat: "lead", session: "4genthub-min-lead@4genthub-min"},
		seatSession{seat: "other", session: "4genthub-min-other@4genthub-min"},
	)

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(context.Background(), []string{"--rig", "4genthub-min", "--out", out}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit = %d, want ExitRemote: %s", code, stderr.String())
	}
	// The failing lead message was NOT acknowledged; the other seat's still was - in the same pass.
	wantCalls := "GET 4genthub-min/lead | GET 4genthub-min/other | ACK m2"
	if got := strings.Join(cloud.seen(), " | "); got != wantCalls {
		t.Fatalf("calls = %s\nwant    %s", got, wantCalls)
	}
	if !strings.Contains(stderr.String(), "putting m1 into session 4genthub-min-lead@4genthub-min failed") {
		t.Errorf("stderr = %q, want the failed seat's message named", stderr.String())
	}
	if !strings.Contains(stdout.String(), "4genthub-min/other: 1 delivered, 0 re-acked, 0 failed") {
		t.Errorf("stdout = %q, want the other seat's delivery counted", stdout.String())
	}
	typed, err := readMessagesLedger(messagesLedgerPath(out, "4genthub-min", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if len(typed) != 0 {
		t.Errorf("lead ledger = %v, want nothing recorded for a message that did not land", typed)
	}
}

// THE SUPERVISED WATCH: one pass, then a heartbeat, then a sleep - and a cancelled context ends the
// loop cleanly with exit 0, which is how rigd stops it. The heartbeat is the row doctor reads, so it
// must be written on a pass that delivered nothing every bit as much as on one that did.
func TestMessagesVerbWatchBeatsEachPassAndExitsOnCancel(t *testing.T) {
	cloud := &messageCloud{bySeat: map[string][]map[string]any{
		"4genthub-min/lead": {message("m1", "hello lead")},
	}}
	_, sent, out := messagesFixture(t, cloud, nil)
	fakeRoom(t, seatSession{seat: "lead", session: "4genthub-min-lead@4genthub-min"})

	var beaten []string
	previousBeat := clientservices.WriteHeartbeat
	ctx, cancel := context.WithCancel(context.Background())
	clientservices.WriteHeartbeat = func(safeguard, rig string) error {
		beaten = append(beaten, safeguard+" "+rig)
		cancel()
		return nil
	}
	t.Cleanup(func() { clientservices.WriteHeartbeat = previousBeat })

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(ctx, []string{"--rig", "4genthub-min", "--watch", "--out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 after a cancelled watch: %s", code, stderr.String())
	}
	if got := strings.Join(beaten, ","); got != "messages 4genthub-min" {
		t.Fatalf("heartbeats = %q, want one beat for the deliverer's row", got)
	}
	if got := strings.Join(*sent, ","); got != "4genthub-min-lead@4genthub-min hello lead" {
		t.Fatalf("typed %q, want the first pass to have delivered before the cancel", got)
	}
	if !strings.Contains(stdout.String(), "4genthub-min: delivering for every running seat, one pass every 10s") {
		t.Errorf("stdout = %q, want the banner naming the pass interval", stdout.String())
	}
}

// THE RIG SHAPE'S GRAMMAR AND ITS EMPTY CASE: the mixtures are refused before anything is pulled, and
// a rig with no running seat is a FAILURE - never a silent success that lets a mailbox grow unseen.
func TestMessagesVerbRigShapeRefusals(t *testing.T) {
	t.Run("the mixtures", func(t *testing.T) {
		for _, args := range [][]string{
			{"dev", "coder", "--rig", "4genthub-min"},
			{"--rig", "4genthub-min", "--session", "rig-seat"},
			{"--watch"},
			{"--rig", "4genthub-min", "--watch", "--dry-run"},
		} {
			var stdout, stderr strings.Builder
			if code := RunMessagesVerb(context.Background(), args, &stdout, &stderr); code != clientcmd.ExitUsage {
				t.Errorf("args %v: exit = %d, want ExitUsage", args, code)
			}
			if !strings.Contains(stderr.String(), "usage") {
				t.Errorf("args %v: stderr = %q, want the usage line", args, stderr.String())
			}
		}
	})

	t.Run("a rig with no running seat", func(t *testing.T) {
		cloud := &messageCloud{}
		_, _, out := messagesFixture(t, cloud, nil)
		fakeRoom(t)

		var stdout, stderr strings.Builder
		if code := RunMessagesVerb(context.Background(), []string{"--rig", "4genthub-min", "--out", out}, &stdout, &stderr); code != clientcmd.ExitRemote {
			t.Fatalf("exit = %d, want ExitRemote: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "reports no running seat") {
			t.Errorf("stderr = %q, want the empty seat set said plainly", stderr.String())
		}
		if calls := cloud.seen(); len(calls) != 0 {
			t.Errorf("calls = %v, want the cloud untouched", calls)
		}
	})
}
