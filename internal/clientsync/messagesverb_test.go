// The parts of `sync messages` that can be proven without the real stack: the argument grammar, the
// ledger, and — against a SCRIPTED cloud — the ORDER the verb's three steps happen in. The order is the
// contract: the text is typed first, the ledger records it second, and the ack is sent last, so an
// interruption after the typing redelivers (safe) and an interruption before the ack does not type the
// same text twice (the one duplicate at-least-once allows, and what the ledger is for).
//
// What this does NOT prove, stated rather than implied: that the real server's pull and ack behave as
// scripted here (that is the httpapp package's tests, against the real handlers), and that a real `rig
// send` reaches a real session (that needs a running seat, which is the row's end-to-end check).
package clientsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// messageCloud is the two endpoints the verb calls, scripted, and it records every call in order so a
// test can assert the SEQUENCE rather than only the outcome.
type messageCloud struct {
	mu      sync.Mutex
	pending []map[string]any
	refuse  map[string]bool // ids whose ack is refused
	calls   []string
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
		c.record("GET")
		messages := c.pending
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

// fakeRig puts a `rig` on PATH that answers `ps --json` with the given payload, so the verb's REAL
// resolution path runs instead of a substituted function. The only other place this verb shells out to
// rig is the local send, which the fixture replaces.
func fakeRig(t *testing.T, psJSON string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rig"), []byte("#!/bin/sh\necho '"+psJSON+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// THE VERB MUST NOT GUESS WHICH PROMPT TO TYPE INTO, and this is the case that made the difference
// matter: `rig ps --json` in this environment reports many sessions, and firstRigSession returns the
// FIRST of them. A verb that typed into that answer would put an operator's message into somebody
// else's prompt — which this verb's own documentation calls worse than not delivering it. So no
// --session and more than one candidate means REFUSE, and the refusal must name the candidates rather
// than call the situation ambiguous.
func TestMessagesVerbRefusesToGuessWhichSessionToTypeInto(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello")}}
	_, sent, out := messagesFixture(t, cloud, nil)
	fakeRig(t, `[{"sessionName":"alpha"},{"sessionName":"beta"}]`)

	var stdout, stderr strings.Builder
	code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--out", out}, &stdout, &stderr)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d, want ExitUnavailable: %s", code, stderr.String())
	}
	if len(*sent) != 0 {
		t.Fatalf("typed %v, want NOTHING typed while the session is ambiguous", *sent)
	}
	if calls := cloud.seen(); len(calls) != 0 {
		t.Fatalf("calls = %v, want the cloud untouched: the refusal happens before the pull", calls)
	}
	for _, want := range []string{"alpha", "beta", "--session"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to name %q", stderr.String(), want)
		}
	}
}

// AND WITH EXACTLY ONE SESSION THE MESSAGE STILL FLOWS. The refusal is about ambiguity, not about
// requiring a flag: the resolution the connector verb uses stays usable when it is unambiguous.
func TestMessagesVerbUsesTheOnlySessionTheRigReports(t *testing.T) {
	cloud := &messageCloud{pending: []map[string]any{message("m1", "hello")}}
	_, sent, out := messagesFixture(t, cloud, nil)
	fakeRig(t, `[{"sessionName":"only-one"}]`)

	var stdout, stderr strings.Builder
	if code := RunMessagesVerb(context.Background(), []string{"dev", "coder", "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if got := strings.Join(*sent, ","); got != "only-one hello" {
		t.Fatalf("typed %q, want the one session the rig reported", got)
	}
	want := []string{"GET", "SEND only-one hello", "ACK m1"}
	if got := strings.Join(cloud.seen(), " | "); got != strings.Join(want, " | ") {
		t.Fatalf("calls = %s\nwant    %s", got, strings.Join(want, " | "))
	}
}
