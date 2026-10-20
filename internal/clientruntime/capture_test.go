// The capture verb reads a pane over the client's OWN transport now - the runtime's terminal manager, the
// process that owns the tmux server - and these tests MOVED here from the room orchestrator-daemon suite when that path
// was deleted. A daemon stub would have gone on passing while testing nothing, which is exactly the failure
// mode the cutover keeps finding: a test that stubs a path the verb no longer walks.
//
// What is pinned: the frame the verb writes (pinned whole, so a changed key fails here), the pane going out
// verbatim, --json printing this client's own answer, and every refusal - no runtime at all, and the runtime's
// own refusal - reaching the caller in words with nothing on stdout.
package clientruntime

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// assertCaptureFrame pins the whole request line, like assertQueueFrame: the contract is one line per request,
// newline-terminated, with the action and its payload as siblings.
func assertCaptureFrame(t *testing.T, requests <-chan string, want string) {
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

// TestCaptureCommandPrintsThePane: the session and the line window the caller asked for travel as the frame,
// and the pane goes out verbatim - trailing whitespace and all, with no heading added.
func TestCaptureCommandPrintsThePane(t *testing.T) {
	hermeticSeat(t)
	const pane = "line one  \nline two\n"
	sock, requests := fakeRuntime(t, func(string) string {
		return okDataResponse(`{"session":"4genthub-min-fe-dev@4genthub-min","content":"line one  \nline two\n"}`)
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := captureCommand{}.Run(context.Background(), []string{"--lines", "40", "4genthub-min-fe-dev@4genthub-min"}, &out, &errOut)

	if code != clientcmd.ExitOK {
		t.Fatalf("capture exit = %d, stderr = %q", code, errOut.String())
	}
	assertCaptureFrame(t, requests,
		`{"action":"Capture","payload":{"lines":40,"session":"4genthub-min-fe-dev@4genthub-min"}}`)
	if out.String() != pane {
		t.Errorf("stdout = %q, want the pane verbatim %q", out.String(), pane)
	}

	// Without --lines the field is omitted entirely rather than sent as a default - the runtime's own "read it
	// whole" case.
	out.Reset()
	errOut.Reset()
	code = captureCommand{}.Run(context.Background(), []string{"4genthub-min-fe-dev@4genthub-min"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("capture exit = %d, stderr = %q", code, errOut.String())
	}
	assertCaptureFrame(t, requests, `{"action":"Capture","payload":{"session":"4genthub-min-fe-dev@4genthub-min"}}`)
}

// TestCaptureCommandPrintsItsOwnAnswerForJSON: --json still means "the answer, machine-readable", and the
// answer is this client's own capture document now - the seat and its content - because there is no daemon body
// to pass through any more.
func TestCaptureCommandPrintsItsOwnAnswerForJSON(t *testing.T) {
	hermeticSeat(t)
	sock, _ := fakeRuntime(t, func(string) string {
		return okDataResponse(`{"session":"peer@room","content":"PANE-LINE\n"}`)
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := captureCommand{}.Run(context.Background(), []string{"--json", "peer@room"}, &out, &errOut)

	if code != clientcmd.ExitOK {
		t.Fatalf("capture --json exit = %d, stderr = %q", code, errOut.String())
	}
	for _, want := range []string{`"session": "peer@room"`, `"content": "PANE-LINE\n"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout = %q, want it to carry %q", out.String(), want)
		}
	}
}

// TestCaptureCommandNamesTheRuntimeItCannotReach: with no runtime on this machine the verb refuses in words and
// names the transport it could not reach. It must never print an empty pane as if the pane were empty.
func TestCaptureCommandNamesTheRuntimeItCannotReach(t *testing.T) {
	hermeticSeat(t)
	noRuntimeSocket(t)

	var out, errOut bytes.Buffer
	code := captureCommand{}.Run(context.Background(), []string{"peer@room"}, &out, &errOut)

	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d, want ExitUnavailable (%d); stderr = %q", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("a runtime-less capture printed %q on stdout, want nothing", out.String())
	}
	for _, want := range []string{"could not capture peer@room", "agenthub-runtime"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to name %q", errOut.String(), want)
		}
	}
}

// TestCaptureCommandRefusesTheRuntimesOwnRefusal: a runtime that ANSWERS and refuses has answered - the reason
// is the caller's whole answer, nothing pane-like reaches stdout, and the exit is a refusal rather than silence.
func TestCaptureCommandRefusesTheRuntimesOwnRefusal(t *testing.T) {
	hermeticSeat(t)
	sock, _ := fakeRuntime(t, func(string) string {
		return `{"ok":false,"error":"no such session 'ghost@4genthub-min': the runtime hosts no seat by that name"}` + "\n"
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := captureCommand{}.Run(context.Background(), []string{"ghost@4genthub-min"}, &out, &errOut)

	if code == clientcmd.ExitOK {
		t.Fatalf("a refused capture exited 0, stdout = %q", out.String())
	}
	if out.Len() != 0 {
		t.Errorf("a refused capture printed %q on stdout", out.String())
	}
	if !strings.Contains(errOut.String(), "no such session 'ghost@4genthub-min'") {
		t.Errorf("stderr = %q, want the runtime's own reason", errOut.String())
	}
}

// TestCaptureTakesItsOptionsOnEitherSideOfTheSession: capture is the read half of send, so it parses its
// options the same way send does. Both forms must write the SAME frame.
func TestCaptureTakesItsOptionsOnEitherSideOfTheSession(t *testing.T) {
	for _, args := range [][]string{
		{"4genthub-min-reviewer@4genthub-min", "--lines", "20"},
		{"--lines", "20", "4genthub-min-reviewer@4genthub-min"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			hermeticSeat(t)
			sock, requests := fakeRuntime(t, func(string) string {
				return okDataResponse(`{"session":"4genthub-min-reviewer@4genthub-min","content":"pane bytes"}`)
			})
			t.Setenv(RuntimeSocketEnv, sock)

			var out, errOut bytes.Buffer
			code := captureCommand{}.Run(context.Background(), args, &out, &errOut)

			if code != clientcmd.ExitOK {
				t.Fatalf("capture exit = %d, stderr = %q", code, errOut.String())
			}
			assertCaptureFrame(t, requests,
				`{"action":"Capture","payload":{"lines":20,"session":"4genthub-min-reviewer@4genthub-min"}}`)
			if out.String() != "pane bytes" {
				t.Errorf("stdout = %q, want the pane verbatim", out.String())
			}
		})
	}
}
