package clientseat

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// The ruling's method C, at its two failure points and its promise: the typed command carries ONLY file paths
// (never the message text, never a sender name - the node's own terminal is what signs it), and the result the
// node's command wrote is shown.
func TestSendViaOwnerTypesOnlyThePathsAndShowsTheResult(t *testing.T) {
	var session, typed, messagePath, resultPath string
	text := "please re-cut the gate list; my name must not appear in this text"
	// The fake node behaves as the real one does: it reads the paths out of the typed line, to which only the
	// paths are passed, then runs the command's job and writes the result file.
	deps := &ownerSendDeps{
		timeout: time.Second, poll: time.Millisecond,
		sendKeys: func(s, command string) error {
			session, typed = s, command
			afterCat := command[strings.Index(command, "cat '")+len("cat '"):]
			messagePath = afterCat[:strings.Index(afterCat, "'")]
			afterArrow := command[strings.Index(command, "> '")+len("> '"):]
			resultPath = afterArrow[:strings.Index(afterArrow, "'")]
			return os.WriteFile(resultPath, []byte("message 8f21c0 sent to demo-lead@demo\n"), 0o600)
		},
	}

	var stdout, stderr bytes.Buffer
	code := sendViaOwnerNode(deps, "demo", "lead", text, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if session != "demo-owner@demo" {
		t.Errorf("typed into %q, want the room's owner node demo-owner@demo", session)
	}
	if strings.Contains(typed, text) || strings.Contains(typed, "owner session") || strings.Contains(typed, "From ") {
		t.Errorf("the typed command carries the message text or a sender name: %q", typed)
	}
	if !strings.Contains(typed, "4genteam send demo-lead@demo") || !strings.Contains(typed, "message") {
		t.Errorf("the typed command does not send the message file to the seat: %q", typed)
	}
	if got := stdout.String(); !strings.Contains(got, "message 8f21c0 sent to demo-lead@demo") {
		t.Errorf("the result the node's command wrote was not shown: %q", got)
	}
	// The private files are gone once the result is in hand, message text and all.
	if _, err := os.Stat(filepath.Dir(messagePath)); !os.IsNotExist(err) {
		t.Errorf("the private directory of %s was left behind: %v", messagePath, err)
	}
	if _, err := os.Stat(resultPath); !os.IsNotExist(err) {
		t.Errorf("the result file was left behind: %v", err)
	}
}

// A node that never answers is NAMED, and the message files are LEFT: the node may still run the command, so
// removing the message could send nothing and removing the result would throw away the only copy. The line says
// where the result will land and never repeats the message.
func TestSendViaOwnerRefusesWhenTheNodeNeverAnswers(t *testing.T) {
	deps := &ownerSendDeps{
		sendKeys: func(string, string) error { return nil },
		timeout:  time.Millisecond, poll: time.Millisecond,
	}
	var stdout, stderr bytes.Buffer
	code := sendViaOwnerNode(deps, "demo", "lead", "a message nobody will answer", &stdout, &stderr)
	if code != ExitFailed {
		t.Fatalf("exit %d, want the failure exit", code)
	}
	if !strings.Contains(stderr.String(), "demo-owner@demo") || !strings.Contains(stderr.String(), "did not answer") {
		t.Errorf("the refusal does not name the node and what happened: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "nobody will answer") {
		t.Errorf("the refusal repeats the message text: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "result") {
		t.Errorf("the refusal does not say where the result will land: %q", stderr.String())
	}
}

// A node that cannot be typed into sends NOTHING, and says so: no message file is left for a command that was
// never run.
func TestSendViaOwnerRefusesWhenTheNodeCannotBeTypedInto(t *testing.T) {
	deps := &ownerSendDeps{
		sendKeys: func(string, string) error { return os.ErrPermission },
		timeout:  time.Second, poll: time.Millisecond,
	}
	var stdout, stderr bytes.Buffer
	code := sendViaOwnerNode(deps, "demo", "lead", "text", &stdout, &stderr)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want the unavailable exit", code)
	}
	if !strings.Contains(stderr.String(), "was not sent") {
		t.Errorf("the refusal does not say the message was not sent: %q", stderr.String())
	}
}

// The default route sends the text VERBATIM: no "From NAME:" is added, because a name written into the text is
// a claim, and the sender a seat sees must come from the environment of the shell that ran the send. The frame
// the runtime receives carries the caller's words and nothing else.
func TestSendSendsTheTextVerbatimWithNoAddedSender(t *testing.T) {
	pinGoDevShell(t)
	auditStoreUnder(t)
	sock, requests := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"GetRoomLinks"`) {
			return roomLinksDoc("go-dev", "lead")
		}
		if strings.Contains(line, `"Whoami"`) {
			return `{"ok":true,"data":{"identity":{"rigName":"4genthub-min","memberId":"go-dev",` +
				`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
		}
		if strings.Contains(line, `"GetRoomStatus"`) {
			return `{"ok":true,"data":[{"room":"4genthub-min","seat":"lead","session_name":"agenthub--4genthub-min--lead",` +
				`"runtime":"omp","state":"Running"}]}` + "\n"
		}
		return `{"ok":true,"data":{"delivered":"agenthub--4genthub-min--lead","outcome":"submitted"}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var stdout, stderr bytes.Buffer
	code := (sendCommand{}).Run(context.Background(),
		[]string{"4genthub-min", "lead", "keep `this` and $that as typed", "--intent", "notice", "--raw"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var sent string
	for len(requests) > 0 {
		if line := <-requests; strings.Contains(line, `"SendText"`) {
			sent = line
		}
	}
	if sent == "" {
		t.Fatalf("no SendText frame reached the runtime; stdout %q stderr %q", stdout.String(), stderr.String())
	}
	var frame struct {
		Payload map[string]string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(sent), &frame); err != nil {
		t.Fatalf("the SendText frame does not parse: %q (%v)", sent, err)
	}
	if got := frame.Payload["text"]; got != "keep `this` and $that as typed" {
		t.Errorf("delivered text = %q, want the caller's words exactly", got)
	}
	if strings.Contains(frame.Payload["text"], "From owner session:") {
		t.Errorf("a sender name was written into the text: %q", frame.Payload["text"])
	}
}

// --via-owner reaches the node route, so the flag is not accepted and then ignored: nothing is resolved on the
// runtime and nothing is delivered there.
func TestSendViaOwnerFlagReachesTheNodeRoute(t *testing.T) {
	restore := ownerSendDepsFor
	t.Cleanup(func() { ownerSendDepsFor = restore })

	var session, typed string
	ownerSendDepsFor = func() *ownerSendDeps {
		return &ownerSendDeps{
			sendKeys: func(s, command string) error { session, typed = s, command; return os.ErrPermission },
			timeout:  time.Second, poll: time.Millisecond,
		}
	}
	var stdout, stderr bytes.Buffer
	code := (sendCommand{}).Run(context.Background(),
		[]string{"--via-owner", "demo", "lead", "hello"}, &stdout, &stderr)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if session != "demo-owner@demo" || !strings.Contains(typed, "4genteam send demo-lead@demo") {
		t.Errorf("--via-owner did not type into the owner node: session %q command %q", session, typed)
	}
}
