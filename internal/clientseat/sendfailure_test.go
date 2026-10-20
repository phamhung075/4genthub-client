package clientseat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/commpolicy"
)

// aSocketTimeout is a REAL timeout from a real socket, not a manufactured error: the failure this client
// names when the daemon goes quiet is the one net.Conn itself produces.
func aSocketTimeout(t *testing.T) error {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	_ = a.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	var one [1]byte
	if _, err := a.Read(one[:]); err == nil {
		t.Fatal("the piped read was expected to time out")
	} else {
		return err
	}
	return nil
}

// aRefusedDial is a real dial error: nobody is listening at the path.
func aRefusedDial(t *testing.T) error {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(t.TempDir(), "nobody.sock"))
	if err == nil {
		_ = conn.Close()
		t.Fatal("the dial was expected to fail")
	}
	return err
}

// THE FALSE SENTENCE THE U3 GATE CAUGHT. The verb printed "; nothing was sent" for a delivery the runtime
// had HELD in the runner's mailbox and handed back at its reply bound - a claim nobody measured (task
// 83a39aa5). The mapping is three-way and every leg is pinned: nothing left the client, the runner never
// answered (the text MAY be held), or the answer itself could not be read (unknown, so also not a refusal).
func TestTheSendFailureSentenceSeparatesARefusalFromAHold(t *testing.T) {
	var netErr net.Error
	timeout := aSocketTimeout(t)
	if !errors.As(timeout, &netErr) || !netErr.Timeout() {
		t.Fatalf("the fixture is not a socket timeout: %v", timeout)
	}

	for _, c := range []struct {
		name     string
		err      error
		nothing  bool
		contains []string
		absent   []string
	}{
		{
			name:    "the daemon never answered the call",
			err:     timeout,
			nothing: false,
			contains: []string{
				"the runtime at /run/x.sock did not answer within its " + clientruntime.CallDeadline.String() + " call deadline",
				"the text may be held for its next turn",
				"unconfirmed",
			},
			absent: []string{"nothing was sent"},
		},
		{
			name:    "the runner never answered its frame",
			err:     &clientruntime.DaemonRefused{Reason: "no answer from the runner for 4genthub-min/fe-dev within 8s: the frame reached its mailbox, so the text may be held for its next turn"},
			nothing: false,
			contains: []string{
				"no answer from the runner for 4genthub-min/fe-dev",
				"the text may be held for its next turn",
				"unconfirmed",
			},
			absent: []string{"nothing was sent"},
		},
		{
			name:     "no runner exists for the seat",
			err:      &clientruntime.DaemonRefused{Reason: "no runner for 4genthub-min/fe-dev: cannot reach its mailbox"},
			nothing:  true,
			contains: []string{"nothing was sent", "no runner for 4genthub-min/fe-dev"},
			absent:   []string{"may be held"},
		},
		{
			name:     "the daemon refused the work",
			err:      &clientruntime.DaemonRefused{Reason: "a parked seat is not handed work"},
			nothing:  true,
			contains: []string{"nothing was sent", "a parked seat is not handed work"},
			absent:   []string{"may be held"},
		},
		{
			name: "an older daemon could not frame the request",
			err: &clientruntime.RuntimeTooOld{
				Action: "SendText",
				Socket: "/run/x.sock",
				Reason: `invalid request JSON: unknown variant "SendText"`,
				CodedError: &clientcmd.CodedError{
					Message: `SendText is not implemented by the runtime at /run/x.sock (it is older than this client; it answered "invalid request JSON: unknown variant \"SendText\"")`,
					Code:    clientcmd.ExitUnavailable,
				},
			},
			nothing:  true,
			contains: []string{"nothing was sent", "not implemented by the runtime at /run/x.sock"},
			absent:   []string{"may be held"},
		},
		{
			name: "the daemon answered something this client cannot read",
			err: &clientruntime.UnreadableAnswer{
				Action:     "SendText",
				CodedError: &clientcmd.CodedError{Message: "the answer is not JSON: EOF"},
			},
			nothing:  false,
			contains: []string{"unconfirmed", "may be held", "the answer is not JSON"},
			absent:   []string{"nothing was sent"},
		},
		{
			name:     "no daemon is listening at the socket",
			err:      aRefusedDial(t),
			nothing:  true,
			contains: []string{"nothing was sent", "the runtime at /run/x.sock refused it"},
			absent:   []string{"may be held"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			sentence := sendFailureSentence("/run/x.sock", c.err)
			if got := nothingWasSent(c.err); got != c.nothing {
				t.Errorf("nothingWasSent(%v) = %v, want %v", c.err, got, c.nothing)
			}
			for _, want := range c.contains {
				if !strings.Contains(sentence, want) {
					t.Errorf("sentence %q does not contain %q", sentence, want)
				}
			}
			for _, unwanted := range c.absent {
				if strings.Contains(sentence, unwanted) {
					t.Errorf("sentence %q contains %q, which this failure cannot claim", sentence, unwanted)
				}
			}
		})
	}
}

// runnerSilence is the daemon's own sentence when a frame reached the runner's mailbox and the runner never
// answered it (REPLY_TIMEOUT in rust/agenthub-runtime/src/runner/mod.rs).
const runnerSilence = "no answer from the runner for 4genthub-min/fe-dev within 8s: the frame reached its mailbox, so the text may be held for its next turn"

// daemonForTheSendTest answers the four requests a `send` makes before its delivery - the recipient's record,
// this shell's identity, the room's links - and hands SendText the failure under test.
func daemonForTheSendTest(t *testing.T, sendTextFailure string) {
	t.Helper()
	sock, _ := fakeRuntime(t, func(line string) string {
		switch {
		case strings.Contains(line, `"GetRoomLinks"`):
			return roomLinksDoc("go-dev", "fe-dev")
		case strings.Contains(line, `"Whoami"`):
			return `{"ok":true,"data":{"identity":{"roomName":"4genthub-min","memberId":"go-dev",` +
				`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
		case strings.Contains(line, `"GetRoomStatus"`):
			return `{"ok":true,"data":[{"room":"4genthub-min","seat":"fe-dev",` +
				`"session_name":"4genthub-min-fe-dev@4genthub-min","runtime":"omp","state":"Running"}]}` + "\n"
		case strings.Contains(line, `"SendText"`):
			return `{"ok":false,"error":"` + sendTextFailure + `"}` + "\n"
		default:
			return `{"ok":true,"data":{}}` + "\n"
		}
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)
}

// daemonAnsweringTheSend is the success twin of daemonForTheSendTest: the same prelude, and SendText answered
// with the runtime's own verdict document, so a test can pin what an ACCEPTED answer does to the pair (the
// printed line and the audit line).
func daemonAnsweringTheSend(t *testing.T, data string) {
	t.Helper()
	sock, _ := fakeRuntime(t, func(line string) string {
		switch {
		case strings.Contains(line, `"GetRoomLinks"`):
			return roomLinksDoc("go-dev", "fe-dev")
		case strings.Contains(line, `"Whoami"`):
			return `{"ok":true,"data":{"identity":{"roomName":"4genthub-min","memberId":"go-dev",` +
				`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
		case strings.Contains(line, `"GetRoomStatus"`):
			return `{"ok":true,"data":[{"room":"4genthub-min","seat":"fe-dev",` +
				`"session_name":"4genthub-min-fe-dev@4genthub-min","runtime":"omp","state":"Running"}]}` + "\n"
		case strings.Contains(line, `"SendText"`):
			return `{"ok":true,"data":` + data + `}` + "\n"
		default:
			return `{"ok":true,"data":{}}` + "\n"
		}
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)
}

// storeUnder points the audit store at a temp directory this test owns, so the outcome line it reads is the
// one its own send wrote.
func storeUnder(t *testing.T) string {
	t.Helper()
	store := t.TempDir()
	previous := sendStore
	sendStore = func() (string, error) { return store, nil }
	t.Cleanup(func() { sendStore = previous })
	return store
}

// auditLines is every record the store's audit file holds, in the order it was written.
func auditLines(t *testing.T, store string) []commpolicy.AuditRecord {
	t.Helper()
	raw, err := os.ReadFile(commpolicy.AuditPath(store, "4genthub-min", "go-dev"))
	if err != nil {
		t.Fatalf("the audit file: %v", err)
	}
	var lines []commpolicy.AuditRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var record commpolicy.AuditRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("an audit line is not JSON: %v (%q)", err, line)
		}
		lines = append(lines, record)
	}
	return lines
}

// END TO END through the verb: a daemon that reports the runner's silence prints the HOLD, carries the
// runner's own words, and never says nothing was sent.
func TestSendToAHeldFrameSaysTheTextMayBeHeld(t *testing.T) {
	pinGoDevShell(t)
	auditStoreUnder(t)
	daemonForTheSendTest(t, runnerSilence)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"4genthub-min-fe-dev@4genthub-min", "probe", "--intent", "notice"}, &out, &errOut)

	if code == 0 {
		t.Fatalf("a held delivery exited 0; stdout = %q", out.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing reported as delivered", out.String())
	}
	for _, want := range []string{
		"no answer from the runner for 4genthub-min/fe-dev",
		"the text may be held for its next turn",
		"unconfirmed",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", errOut.String(), want)
		}
	}
	if strings.Contains(errOut.String(), "nothing was sent") {
		t.Errorf("stderr = %q claims nothing was sent for a frame the runner received", errOut.String())
	}
}

// THE AUDIT LINE CARRIES THE SAME VERDICT THE PRINTED LINE DOES. Measured at 84bcd8c against a real answer:
// SendText answered `{"delivered":…,"outcome":"relayed"}` printed "Relayed …, not claimed as delivered" and
// exited 0, while the outcome line written for that same send was `delivered` - the audit claimed what the
// sentence refused to (task 83a39aa5). Only `submitted` proves the text was written where the verdict says,
// so only `submitted` earns `delivered`; every other answer is `delivery_unconfirmed`, and a send that exited
// 0 for any of them still exits 0.
func TestASuccessfulSendAuditsTheVerdictItPrints(t *testing.T) {
	for _, c := range []struct {
		name    string
		data    string
		outcome string
		line    string
	}{
		{"the pane took the text", `{"delivered":"4genthub-min-fe-dev@4genthub-min","outcome":"submitted"}`,
			commpolicy.OutcomeDelivered, "Sent to 4genthub-min-fe-dev@4genthub-min"},
		{"the runner only wrote the frame into the channel", `{"delivered":"4genthub-min-fe-dev@4genthub-min","outcome":"relayed"}`,
			commpolicy.OutcomeDeliveryUnconfirmed, "not claimed as delivered"},
		{"the answer carries no verdict", `{"delivered":"4genthub-min-fe-dev@4genthub-min"}`,
			commpolicy.OutcomeDeliveryUnconfirmed, "nothing is claimed as delivered"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pinGoDevShell(t)
			store := storeUnder(t)
			daemonAnsweringTheSend(t, c.data)

			var out, errOut bytes.Buffer
			code := sendCommand{}.Run(context.Background(),
				[]string{"4genthub-min-fe-dev@4genthub-min", "probe", "--intent", "notice"}, &out, &errOut)
			if code != 0 {
				t.Fatalf("an accepted send exited %d; stdout = %q, stderr = %q", code, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), c.line) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), c.line)
			}
			if strings.Contains(out.String(), "Sent to") && c.outcome != commpolicy.OutcomeDelivered {
				t.Errorf("stdout = %q claims delivery for an answer that proves no submission", out.String())
			}

			lines := auditLines(t, store)
			if len(lines) != 2 {
				t.Fatalf("audit lines = %d, want the decision and one outcome: %+v", len(lines), lines)
			}
			if lines[0].Outcome != "" {
				t.Errorf("the decision line carries outcome %q, want none", lines[0].Outcome)
			}
			if lines[1].Outcome != c.outcome {
				t.Errorf("outcome line = %q, want %q, the verdict the printed line names", lines[1].Outcome, c.outcome)
			}
		})
	}
}

// THE AUDIT LINE CARRIES THE SAME THREE FACTS THE SENTENCE DOES. A held send used to be audited
// `delivery_failed`, which claims a loss nobody measured; it is `delivery_unconfirmed` now, and a request
// that provably never left the client keeps `delivery_failed` (task 83a39aa5).
func TestAHeldSendIsAuditedUnconfirmedAndARefusalFailed(t *testing.T) {
	for _, c := range []struct {
		name    string
		failure string
		outcome string
		stderr  string
	}{
		{"held by the runner", runnerSilence, commpolicy.OutcomeDeliveryUnconfirmed, "may be held"},
		{"refused by the daemon", "a parked seat is not handed work", commpolicy.OutcomeDeliveryFailed, "nothing was sent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pinGoDevShell(t)
			store := storeUnder(t)
			daemonForTheSendTest(t, c.failure)

			var out, errOut bytes.Buffer
			code := sendCommand{}.Run(context.Background(),
				[]string{"4genthub-min-fe-dev@4genthub-min", "probe", "--intent", "notice"}, &out, &errOut)
			if code == 0 {
				t.Fatalf("a failed delivery exited 0; stdout = %q", out.String())
			}
			if !strings.Contains(errOut.String(), c.stderr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), c.stderr)
			}

			lines := auditLines(t, store)
			if len(lines) != 2 {
				t.Fatalf("audit lines = %d, want the decision and one outcome: %+v", len(lines), lines)
			}
			if lines[0].Outcome != "" {
				t.Errorf("the decision line carries outcome %q, want none", lines[0].Outcome)
			}
			if lines[1].Outcome != c.outcome {
				t.Errorf("outcome line = %q, want %q", lines[1].Outcome, c.outcome)
			}
		})
	}
}
