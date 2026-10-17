package clientseat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/commpolicy"
)

// THE DECISION ATTACHES TO THE CAPABILITY, and these are the four measurements the gate asks for. They run the
// REAL verb (`sendCommand{}.Run`) against a fake runtime, so what is measured is the verb's own frames and its
// own audit file - not a helper's idea of them.
//
// The live order of one send, once wired: the seat's record (GetRoomStatus) when the recipient is named as a
// canonical session, the room's declared links (GetRoomLinks), then the delivery (SendText). The sender's
// identity is asked of THIS shell's resolver, which is why every case pins AGENTHUB_SESSION_NAME.
const (
	auditSender = "go-dev"
	auditPeer   = "reviewer"
	liveSession = "agenthub--4genthub-min--reviewer"
)

// auditHarness is a fake daemon that answers the three reads a send makes and records every frame it saw, plus
// the seat store the audit lines land in.
type auditHarness struct {
	socket string
	frames []string
	store  string
}

// links is what the fake daemon answers GetRoomLinks with; sendReadErr, when set, makes the socket refuse
// nothing but tells the harness to answer that call with a refusal document instead.
func newAuditHarness(t *testing.T, links string) *auditHarness {
	t.Helper()

	dir, err := os.MkdirTemp("", "sendaudit")
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

	harness := &auditHarness{socket: sock, store: filepath.Join(dir, "store")}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				harness.frames = append(harness.frames, string(line))
				text := string(line)
				switch {
				case strings.Contains(text, `"GetRoomLinks"`):
					_, _ = conn.Write([]byte(links))
				case strings.Contains(text, `"Whoami"`):
					_, _ = conn.Write([]byte(`{"ok":true,"data":{"identity":{"rigName":"4genthub-min","memberId":"` + auditSender + `","sessionName":"agenthub--4genthub-min--` + auditSender + `"}}}` + "\n"))
				case strings.Contains(text, `"GetRoomStatus"`):
					_, _ = conn.Write([]byte(`{"ok":true,"data":[{"room":"4genthub-min","seat":"reviewer",` +
						`"session_name":"` + liveSession + `","runtime":"omp","state":"Running"}]}` + "\n"))
				default:
					// The runtime's own success document: a verdict is part of the answer, and only `submitted`
					// earns a `delivered` outcome line (the audit line reads the same token the printed line does).
					_, _ = conn.Write([]byte(`{"ok":true,"data":{"delivered":"` + liveSession + `","outcome":"submitted"}}` + "\n"))
				}
			}(conn)
		}
	}()

	t.Setenv(clientruntime.RuntimeSocketEnv, sock)
	t.Setenv("AGENTHUB_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	previousStore := sendStore
	sendStore = func() (string, error) { return harness.store, nil }
	t.Cleanup(func() { sendStore = previousStore })
	return harness
}

// sent reports whether the delivery frame reached the daemon.
func (h *auditHarness) sent() bool {
	for _, frame := range h.frames {
		if strings.Contains(frame, `"SendText"`) {
			return true
		}
	}
	return false
}

// lines reads the seat's audit file, one decoded record per line.
func (h *auditHarness) lines(t *testing.T) []commpolicy.AuditRecord {
	t.Helper()
	raw, err := os.ReadFile(commpolicy.AuditPath(h.store, "4genthub-min", auditSender))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the audit file: %v", err)
	}
	out := []commpolicy.AuditRecord{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var record commpolicy.AuditRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("audit line is not JSON: %v (%q)", err, line)
		}
		out = append(out, record)
	}
	return out
}

func linkDoc(from, to, kind string) string {
	return `{"ok":true,"data":[{"from":"` + from + `","to":"` + to + `","kind":"` + kind + `"}]}` + "\n"
}

// (a) A SEND THE ROOM'S LINKS AUTHORIZE IS DELIVERED, AND ITS TWO LINES ARE ON DISK. The pair is the point:
// the decision line first, the outcome line after, both citing the same policy hash.
func TestAnAuthorizedSendIsDeliveredAndAuditedInTwoLines(t *testing.T) {
	harness := newAuditHarness(t, linkDoc(auditSender, auditPeer, "collaborates_with"))

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"4genthub-min", auditPeer, "the gate's own text", "--intent", "notice"}, &out, &errOut)

	if code != clientcmd.ExitOK {
		t.Fatalf("send exit = %d, stderr = %q", code, errOut.String())
	}
	if !harness.sent() {
		t.Fatalf("the delivery never reached the runtime; frames %v", harness.frames)
	}
	lines := harness.lines(t)
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d, want the decision line and the outcome line: %+v", len(lines), lines)
	}
	decision, outcome := lines[0], lines[1]
	if !decision.Allowed || decision.Reason != "allowed" {
		t.Errorf("decision line = %+v, want allowed/allowed", decision)
	}
	if decision.From != auditSender || decision.To != auditPeer || decision.Intent != commpolicy.IntentNotice {
		t.Errorf("decision line names %s -> %s (%s), want %s -> %s (notice)",
			decision.From, decision.To, decision.Intent, auditSender, auditPeer)
	}
	if decision.PolicyHash == "" || decision.PolicyHash != outcome.PolicyHash {
		t.Errorf("policy hashes do not pair: decision %q, outcome %q", decision.PolicyHash, outcome.PolicyHash)
	}
	if outcome.Outcome != commpolicy.OutcomeDelivered {
		t.Errorf("outcome line = %q, want %q", outcome.Outcome, commpolicy.OutcomeDelivered)
	}
}

// (b) A SEND TO AN UNLINKED PEER IS REFUSED IN WORDS, AUDITED ALLOWED:FALSE, AND NOT DELIVERED.
func TestASendNoLinkAuthorizesIsRefusedAndNotDelivered(t *testing.T) {
	harness := newAuditHarness(t, linkDoc(auditSender, "fe-dev", "collaborates_with"))

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"4genthub-min", auditPeer, "this must not arrive", "--intent", "notice"}, &out, &errOut)

	if code == clientcmd.ExitOK {
		t.Fatalf("a send with no link authorizing it exited 0; stdout %q", out.String())
	}
	for _, want := range []string{auditSender, auditPeer, "notice", "no link", "collaborates_with", "declares no link to", "nothing was sent"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to name %q", errOut.String(), want)
		}
	}
	if harness.sent() {
		t.Fatalf("the message was delivered anyway; frames %v", harness.frames)
	}
	lines := harness.lines(t)
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want the decision line alone: %+v", len(lines), lines)
	}
	if lines[0].Allowed || lines[0].Reason != "no link" || lines[0].Outcome != "" {
		t.Errorf("decision line = %+v, want denied, no link, and no outcome", lines[0])
	}
}

// (c) A SEND WHOSE POLICY SOURCE CANNOT BE READ IS REFUSED, AND ITS REFUSAL IS NOT THE DENIAL'S: a gate over a
// broken wire must be tellable apart from a gate over a rule.
func TestASendWhoseLinkSourceCannotBeReadIsRefusedInItsOwnWords(t *testing.T) {
	harness := newAuditHarness(t, `{"ok":false,"error":"the room's links are unavailable"}`+"\n")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"4genthub-min", auditPeer, "must not arrive either", "--intent", "notice"}, &out, &errOut)

	if code == clientcmd.ExitOK {
		t.Fatalf("a send whose source could not be read exited 0; stdout %q", out.String())
	}
	if !strings.Contains(errOut.String(), "declared links could not be read") {
		t.Errorf("stderr = %q, want the source failure named as such", errOut.String())
	}
	if strings.Contains(errOut.String(), "may not send") {
		t.Errorf("stderr = %q, want the source failure NOT reported as a policy denial", errOut.String())
	}
	if harness.sent() {
		t.Fatalf("the message was delivered anyway; frames %v", harness.frames)
	}
	if lines := harness.lines(t); len(lines) != 0 {
		t.Errorf("audit lines = %+v, want none: nothing was decided", lines)
	}
}

// A ROOM THAT DECLARES NO LINKS IS NOT A BROKEN WIRE: the answer is the denial, with its own reason, and this
// is the state 4genthub-min is in today (measured 2026-10-15) - so this case is also the one that says what a
// room with no declared links experiences.
func TestARoomThatDeclaresNoLinksDeniesEverySend(t *testing.T) {
	harness := newAuditHarness(t, `{"ok":true,"data":[]}`+"\n")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"4genthub-min", auditPeer, "no room declares this pair", "--intent", "notice"}, &out, &errOut)

	if code == clientcmd.ExitOK {
		t.Fatalf("a send in a room with no declared links exited 0; stdout %q", out.String())
	}
	if !strings.Contains(errOut.String(), "no link") {
		t.Errorf("stderr = %q, want the denial's own reason", errOut.String())
	}
	if harness.sent() {
		t.Fatalf("the message was delivered anyway; frames %v", harness.frames)
	}
	lines := harness.lines(t)
	if len(lines) != 1 || lines[0].Allowed || lines[0].Reason != "no link" {
		t.Errorf("audit lines = %+v, want one denied line whose reason is no link", lines)
	}
}

// THE INTENT DEFAULTS TO `notice`, DOCUMENTED (the architect's ruling, 2026-10-15): a send that names no
// intent is not refused and not guessed silently - it is decided as a notice, it is delivered when the room's
// links authorize that kind, and THE DECISION LINE RECORDS `notice` as the intent used, so a sender who meant
// `task` is visible in the audit instead of invisible. The default is also printed in the verb's own usage.
func TestASendWithoutAnIntentIsDecidedAsANotice(t *testing.T) {
	harness := newAuditHarness(t, linkDoc(auditSender, auditPeer, "collaborates_with"))

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"4genthub-min", auditPeer, "no intent"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d, want 0: collaborates_with authorizes a notice (stderr %q)", code, errOut.String())
	}
	if !harness.sent() {
		t.Fatalf("nothing was delivered; frames %v", harness.frames)
	}
	lines := harness.lines(t)
	if len(lines) != 2 {
		t.Fatalf("audit lines = %+v, want the decision and the outcome", lines)
	}
	if !lines[0].Allowed || lines[0].Intent != commpolicy.IntentNotice {
		t.Errorf("decision = %+v, want an allowed line whose intent is the default notice", lines[0])
	}
	if lines[1].Outcome != commpolicy.OutcomeDelivered {
		t.Errorf("outcome = %q, want %q", lines[1].Outcome, commpolicy.OutcomeDelivered)
	}
}

// THE SHAPE THIS ROOM ACTUALLY PRODUCES, AND THE MISMATCH THE DECLARATION WAS ABOUT TO SHIP: a send that names
// NO intent is decided as a notice, and a link declared `delegates_to` authorizes only an explicit
// `--intent task`. So a pair declared delegates_to REFUSES the traffic it exists to license - which is exactly
// what the reviewer measured on 2026-10-15 in the live room, where `reviewer -> go-dev` and `reviewer ->
// fe-dev` were declared delegates_to while every verdict row the reviewer sends carries no intent at all.
//
// AND THE REFUSAL SAID `no link`, WHICH IS NOT THE STATE: the pair declares one. The room read the words as
// they were written and changed the declaration, which is the lesson that message taught; the lead therefore
// ruled the refusal must name the kind that WOULD license the send (qitem-20261015055946-cb7e92b5d2b2,
// 2026-10-15). This case pins the remedy as well as the refusal: the kind for the intent that was sent, and
// the intent this pair's declaration does license. It fails on the words that came before it.
func TestASendWithNoIntentIsRefusedByADelegatesToLink(t *testing.T) {
	harness := newAuditHarness(t, linkDoc(auditSender, auditPeer, "delegates_to"))

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"4genthub-min", auditPeer, "a verdict sent the way verdicts are sent"}, &out, &errOut)

	if code == clientcmd.ExitOK {
		t.Fatalf("a default-intent send over a delegates_to link exited 0; stdout %q", out.String())
	}
	for _, want := range []string{"no link", "collaborates_with", "task (delegates_to)"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to name %q", errOut.String(), want)
		}
	}
	if harness.sent() {
		t.Fatalf("the message was delivered anyway; frames %v", harness.frames)
	}
	lines := harness.lines(t)
	if len(lines) != 1 {
		t.Fatalf("audit lines = %+v, want the decision line alone", lines)
	}
	if lines[0].Allowed || lines[0].Intent != commpolicy.IntentNotice {
		t.Errorf("decision = %+v, want a denied line whose intent is the default notice", lines[0])
	}
}
