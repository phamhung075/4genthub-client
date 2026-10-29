// The shim verbs' transport seam: whether the runtime socket answers, resolved in ONE place.
//
// THE READING IS A DIAL, NOT A PREFERENCE: `ResolveTransport` decides from whether the socket ANSWERS, so a
// verb reports the transport that is really carrying it and a verb that must not fabricate a measurement
// can refuse by naming the runtime that is missing.
package clientruntime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// Transport names where a shim verb's request goes.
type Transport int

const (
	// TransportRuntime means the runtime socket answered.
	TransportRuntime Transport = iota
	// TransportNone means it did not. A verb must refuse, and the refusal names the RUNTIME socket.
	TransportNone
)

// ResolveTransport probes the runtime socket once and answers where the shim's request goes. The socket is
// the one DefaultSocketPath names (the same one resolution point every other verb uses), so a client built
// from a branch, a seam test and an owner's harness all reach the same daemon.
func ResolveTransport() Transport {
	if NewClient("").Reachable() {
		return TransportRuntime
	}
	return TransportNone
}

// RuntimeMissing is the refusal a shim verb owes when the runtime is not there. It NAMES the thing that is
// missing: the runtime socket, by path, and the knob that points elsewhere.
func RuntimeMissing() string {
	return fmt.Sprintf("no runtime is answering at %s (set %s, or start agenthub-runtime with --socket %s daemon)", DefaultSocketPath(), RuntimeSocketEnv, DefaultSocketPath())
}

// The seat's environment: a seat carries AGENTHUB_*.
const (
	agentHubRoomEnv    = "AGENTHUB_ROOM"
	agentHubSeatEnv    = "AGENTHUB_SEAT"
	agentHubRoleEnv    = "AGENTHUB_ROLE"
	agentHubRuntimeEnv = "AGENTHUB_RUNTIME"
	agentHubSessionEnv = "AGENTHUB_SESSION_NAME"
	// The hub itself: the base and the credential every hub call this client makes carries. They are
	// the environment pair `4genteam whoami` reads the seat's current task with (currenttask.go).
	agentHubURLEnv   = "AGENTHUB_URL"
	agentHubTokenEnv = "AGENTHUB_TOKEN"
)

// seatEnvParts is what this shell's environment claims about the seat it is: the (room, seat) pair, the
// session name that pair spells, and whether it claimed anything at all. AGENTHUB_ROOM and AGENTHUB_SEAT are
// the pair a seat states by name; it is the namespace the runtime's rooms, sessions and rail all live in.
func seatEnvParts() (room, seat, session string, ok bool) {
	room, seat = strings.TrimSpace(os.Getenv(agentHubRoomEnv)), strings.TrimSpace(os.Getenv(agentHubSeatEnv))
	if room == "" || seat == "" {
		return "", "", "", false
	}
	return room, seat, SeatSessionName(room, seat), true
}

// SendTextArgs is the IPC request one message is delivered with: the session, the text, and the ORIGIN the
// seat's runner must name if it can neither deliver nor keep the text - the caller's session and the verb
// that carried it.
//
// The runtime stamps both onto the frame it hands the seat's runner, and that runner is where a text can end
// up held: at its bound it hands the text BACK to this session, whole and naming the reason, rather than
// dropping it in silence (hub task c94cecfc, 19 dropped sends measured in one evening). A caller with no
// session of its own - a shell, a hook - sends no `from` at all, and the runner names that origin as unnamed
// instead of guessing.
func SendTextArgs(session, text, sender, verb string) map[string]string {
	args := map[string]string{"session": session, "text": text, "verb": verb}
	if sender = strings.TrimSpace(sender); sender != "" {
		args["from"] = sender
	}
	return args
}

// RuntimeSendText delivers one message to one session through the runtime socket this shell's environment
// resolves, and returns the runtime's OWN answer document.
//
// The text is wrapped in the From/To/Sent envelope unless opts.Raw. `--json` prints the document returned
// here, which is why the answer is taken at the frame rather than through Client.SendText (that helper drops
// it): the frame is the identical one SendText writes.
func RuntimeSendText(session, text string, opts SendOptions) ([]byte, error) {
	return RuntimeSendTextOn("", session, text, opts)
}

// RuntimeSendTextOn is the same frame on a NAMED socket, so a verb that can be pointed at another daemon
// writes to THAT daemon. Sending to one runtime while the address it was resolved from came from another is
// the pair that produced the measured defect: a seat of an isolated run addressed the box's live runtime and
// was refused for a mailbox under the live runtime's state directory.
func RuntimeSendTextOn(socket, session, text string, opts SendOptions) ([]byte, error) {
	sender := opts.Sender
	if sender == "" {
		sender = ownSession()
	}
	body := text
	if !opts.Raw {
		body = sendEnvelope(sender, session, text, time.Now())
	}
	// The verb the frame is stamped with: the caller's own when it names one, `send` otherwise. It rides the
	// frame so the runner can hand a text it could not deliver back to its sender NAMING the way it arrived.
	verb := opts.Verb
	if verb == "" {
		verb = "send"
	}
	resp, err := NewClient(socket).Call("SendText", SendTextArgs(session, body, sender, verb))
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("the runtime's answer could not be re-encoded: %w", err)
	}
	// A PASTED TEXT CAN SIT IN AGY'S QUEUE while the runtime calls it submitted (agyqueue.go): settle it here,
	// so every send and input to an agy seat is a submission and not a queued line nobody runs.
	submitIfQueued(socket, session, raw)
	return raw, nil
}

// RuntimeSendLine is the one line a native send prints for the runtime's answer document.
//
// ONLY THE OUTCOME THAT PROVES A SUBMISSION EARNS THE SENT LINE. The runtime has two delivery paths and two
// vocabularies: a seat it hosts no runner for gets a pane paste, answered `submitted` (the pane changed after
// Enter) or `unconfirmed` (it did not); a runner seat gets ONE frame into the seat's mailbox, answered
// `relayed` the moment the runner wrote it into the agent's channel - which is NOT the agent having taken it.
//
// MEASURED 2026-10-14 (the live room and the U3 harness): a send to a mid-turn seat printed `Sent to
// <session>` and exited 0 while the agent refused the prompt (`Agent is already processing. Use steer() or
// followUp() …`) and the text was dropped - in no surface, not the transcript, the pane, the state file or the
// queue. The refusal goes to the seat's pane; nothing reports it back to this caller, so `relayed` cannot be
// printed as a delivery. An answer this build cannot read is not a delivery either, and the old default line
// claimed the most exactly there (a missing outcome and text that is not JSON both printed `Sent to`).
//
// So: every answer except `submitted` takes an unconfirmed branch, each naming the reason that is known.
func RuntimeSendLine(session string, answer []byte) string {
	verdict, readable := SendVerdict(answer)
	if !readable {
		return fmt.Sprintf("Submission unconfirmed for %s: the runtime's answer could not be read as a verdict, so nothing is claimed as delivered\n", session)
	}
	switch verdict {
	case VerdictSubmitted:
		return fmt.Sprintf("Sent to %s\n", session)
	case VerdictPastedUnconfirmed:
		return fmt.Sprintf("Pasted to %s, submission unconfirmed: the pane did not change after Enter, so the text may still be staged\n", session)
	case VerdictRelayed:
		return fmt.Sprintf("Relayed to %s, submission unconfirmed: the seat's runner wrote the frame into the agent's channel and the agent's own answer is not reported back, so the text is not claimed as delivered\n", session)
	case "":
		return fmt.Sprintf("Submission unconfirmed for %s: the runtime's answer carries no submission verdict, so nothing is claimed as delivered\n", session)
	}
	return fmt.Sprintf("Submission unconfirmed for %s: the runtime answered %q, which is no submission verdict, so nothing is claimed as delivered\n", session, verdict)
}

// The runtime's own outcome vocabulary for SendText. `submitted` is the ONLY token that proves a submission:
// the pane changed after Enter, so the text was written where the verdict says. `relayed` says the runner
// wrote the frame into the agent's channel and `unconfirmed` says the pane did not change - neither is the
// agent having taken the text, which is why the audit line they earn is delivery_unconfirmed rather than
// delivered (the printed line has said so since 2026-10-14; the audit line said delivered for every ok answer
// until it was measured against a real answer, task 83a39aa5).
const (
	VerdictSubmitted         = "submitted"
	VerdictPastedUnconfirmed = "unconfirmed"
	VerdictRelayed           = "relayed"
)

// SendVerdict reads the runtime's own outcome token from an answer document. `readable` is false when the
// answer is not a document this build can parse; a readable document with no token answers "", which proves
// no more than an unreadable one does.
func SendVerdict(answer []byte) (verdict string, readable bool) {
	var doc struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(answer, &doc) != nil {
		return "", false
	}
	return doc.Outcome, true
}

// runtimeJSONDocument renders a value the runtime answered with as the document `--json` prints. The answer
// arrived as JSON and is re-encoded rather than re-typed, so a field this build has not heard of — and a
// null the runtime sent explicitly — still reaches the caller, the same rule printDaemonJSON follows.
func runtimeJSONDocument(value any) []byte {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte(fmt.Sprintf("%v", value))
	}
	return raw
}

// ResolveIdentity answers who this seat is, over the ONE chain: the runtime socket first, then the seat's own
// environment. It is the chain `4genteam whoami` prints, extracted so that the OTHER callers - the send path
// (internal/clientseat) and `4genteam feedback` - ask the same question the same way.
//
// `session` is the caller's explicit session name (the verb's --session); "" means the seat's own
// environment (AGENTHUB_SESSION_NAME, else the AGENTHUB_ROOM/AGENTHUB_SEAT pair).
//
// The failure is a single shape: a CodedError carrying ExitUnavailable whose message names the runtime that
// did not answer AND the environment that named no seat.
func ResolveIdentity(session string) (WhoamiResponse, error) {
	// A runtime that answers with a refusal ("could not be resolved from active rooms") has not answered this
	// question, and whoami's job is to RECOVER an identity, so the chain continues rather than reporting one
	// transport's mood.
	if ResolveTransport() == TransportRuntime {
		asked := session
		if asked == "" {
			asked = ownSession()
		}
		if asked == "" {
			// A seat may name its HALVES rather than the session: the runtime's own helper renders that pair
			// to exactly the name below, so the seat asked for is THIS one rather than "the first seat the
			// room lists" - the reading a bare empty session would invite.
			room, seat, _, _ := seatEnvParts()
			asked = SeatSessionName(room, seat)
		}
		if resp, err := NewClient("").Whoami(asked); err == nil {
			return *resp, nil
		}
	}

	// NO RUNTIME ANSWER: the seat's own environment IS the identity. The runtime is NOT started here as a side
	// effect: booting a daemon to answer an identity question would make the answer depend on a process this
	// call could have left alone.
	if resp, ok := seatEnvIdentity(); ok {
		return resp, nil
	}

	return WhoamiResponse{}, &clientcmd.CodedError{
		Message: fmt.Sprintf("%s; no seat environment names this seat either (set %s and %s)",
			RuntimeMissing(), agentHubRoomEnv, agentHubSeatEnv),
		Code: clientcmd.ExitUnavailable,
	}
}

// DeliverToSession delivers ONE message to ONE session through the runtime socket. It is the delivery the
// restore path uses (internal/clientrestore), on the SAME transport the `4genteam send` verb delivers over.
// The text is wrapped in the From/To/Sent envelope, attributed to this shell's own session name.
//
// `verb` is the client verb that CARRIED this frame, stamped onto it so a text a mid-turn seat cannot take is
// handed back to this session naming the way it arrived. Blank means `send`.
//
// The exit code is the client's own: 0 delivered, ExitRemote (1) when the runtime refused it,
// ExitUnavailable (3) when no runtime answered. A caller that must not report a policy result as a delivery
// failure maps it at its own boundary.
func DeliverToSession(session, text, verb string, stdout, stderr io.Writer) int {
	if ResolveTransport() != TransportRuntime {
		fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", RuntimeMissing())
		return clientcmd.ExitUnavailable
	}
	answer, err := RuntimeSendText(session, text, SendOptions{Verb: verb})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam send: the runtime at %s refused it: %v\n", DefaultSocketPath(), err)
		return clientcmd.ExitRemote
	}
	fmt.Fprint(stdout, RuntimeSendLine(session, answer))
	return clientcmd.ExitOK
}
