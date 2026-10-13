// The shim verbs' transport seam: which of the two transports a request goes to, resolved in ONE place.
// This is the file the verbs' runtime-first resolution lives in, and nothing else in the tree knows that
// name yet — so, in full, the rule it implements:
//
//	runtime present (with or without an OpenRig daemon also up)  -> the RUNTIME answers;
//	runtime absent, OpenRig daemon present                       -> the OpenRig path answers, unchanged;
//	neither                                                      -> the refusal names the RUNTIME.
//
// WHY THE RUNTIME IS ASKED FIRST. The runtime socket is the transport this client owns: it is the client's
// own daemon, its room store is where a seat's rail actually lives, and reaching it needs no daemon at
// $OPENRIG_URL to exist at all. The OpenRig daemon's HTTP API is kept as a FALLBACK for a rig that still
// runs it — asked whenever no runtime answers — so the live rig keeps working while the runtime takes over.
//
// THE READING IS A DIAL, NOT A PREFERENCE: `ResolveTransport` decides from whether the socket ANSWERS, so a
// verb reports the transport that is really carrying it and a verb that must not fabricate a measurement
// (queue) can refuse by naming the transport that is missing instead of blaming the other one.
package clientruntime

import (
	"context"
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
	// TransportRuntime is the PREFERRED transport: the runtime socket answered.
	TransportRuntime Transport = iota
	// TransportDaemon is the fallback: no runtime answered, but this shell names an OpenRig daemon
	// ($OPENRIG_URL or $OPENRIG_PORT), which is what a rig the daemon hosts still carries.
	TransportDaemon
	// TransportNone is neither. A verb must refuse, and the refusal names the RUNTIME socket.
	TransportNone
)

// ResolveTransport probes the runtime socket once and answers where the shim's request goes. The socket is
// the one DefaultSocketPath names (the same one resolution point every other verb uses), so a client built
// from a branch, a seam test and an owner's harness all reach the same daemon.
func ResolveTransport() Transport {
	if NewClient("").Reachable() {
		return TransportRuntime
	}
	if openRigDaemonConfigured() {
		return TransportDaemon
	}
	return TransportNone
}

// RuntimeMissing is the refusal a shim verb owes when the PREFERRED transport is not there. It NAMES the
// thing that is missing — the runtime socket, by path, and the knob that points elsewhere — and `fallback`
// is one clause about the OpenRig path, or "" when there is nothing more to say.
//
// It is deliberately not the OpenRig daemon's "is not answering" sentence: with the runtime first, a dead
// $OPENRIG_URL is not what stopped the verb, so blaming it would send a reader to the wrong transport.
func RuntimeMissing(fallback string) string {
	message := fmt.Sprintf("no runtime is answering at %s (set %s, or start agenthub-runtime with --socket %s daemon)", DefaultSocketPath(), RuntimeSocketEnv, DefaultSocketPath())
	if fallback != "" {
		message += "; " + fallback
	}
	return message
}

// FallbackMissing is RuntimeMissing for the common case: the runtime did not answer, AND the OpenRig fallback
// did not carry the request either — because this shell names no daemon, or because the daemon it names could
// not be reached. The clause it adds is chosen from this machine's own configuration rather than from the
// caller's guess, so every verb says the same thing about the same state.
func FallbackMissing() string {
	clause := "no OpenRig daemon is configured in this environment (set " + openRigURLEnv + " or " + openRigPortEnv + ")"
	if base := openRigDaemonURL(); base != "" {
		clause = "the OpenRig fallback at " + base + " is not available either"
	}
	return RuntimeMissing(clause)
}

// The seat's environment. A native seat carries AGENTHUB_*; a seat the OpenRig daemon hosts carries
// OPENRIG_SESSION_NAME (and, for pre-rename shells, RIGGED_SESSION_NAME, which openRigSession reads).
const (
	agentHubRoomEnv    = "AGENTHUB_ROOM"
	agentHubSeatEnv    = "AGENTHUB_SEAT"
	agentHubRoleEnv    = "AGENTHUB_ROLE"
	agentHubRuntimeEnv = "AGENTHUB_RUNTIME"
	agentHubSessionEnv = "AGENTHUB_SESSION_NAME"
	openRigRuntimeEnv  = "OPENRIG_RUNTIME"
)

// seatEnvParts is what this shell's environment claims about the seat it is: the (room, seat) pair, the
// session name the ENVIRONMENT ITSELF spells that seat with, and whether it claimed anything at all. AGENTHUB_ROOM
// / AGENTHUB_SEAT win, because a native seat states both halves by name; otherwise the OpenRig seat's own
// session name is read, because "<seat>@<rig>" IS that seat's identity and it is the only name such a seat
// carries. The pair is the namespace the runtime's rooms, sessions and rail all live in.
func seatEnvParts() (room, seat, session string, ok bool) {
	room, seat = strings.TrimSpace(os.Getenv(agentHubRoomEnv)), strings.TrimSpace(os.Getenv(agentHubSeatEnv))
	if room != "" && seat != "" {
		return room, seat, SeatSessionName(room, seat), true
	}
	name := openRigSession()
	at := strings.LastIndex(name, "@")
	if at <= 0 || at == len(name)-1 {
		return "", "", "", false
	}
	return strings.TrimSpace(name[at+1:]), strings.TrimSpace(name[:at]), name, true
}

// RuntimeSendText delivers one message to one session through the runtime socket and returns the runtime's
// OWN answer document.
//
// The text is wrapped in the same From/To/Sent envelope the OpenRig path sends, so the pane text a receiver
// reads does not depend on which transport carried it. `--json` prints the document returned here, which is
// why the answer is taken at the frame rather than through Client.SendText (that helper drops it): the
// frame is the identical one SendText writes.
func RuntimeSendText(session, text string, opts SendOptions) ([]byte, error) {
	body := text
	if !opts.Raw {
		sender := opts.Sender
		if sender == "" {
			sender = openRigSession()
		}
		body = openRigSendBody(sender, session, text, time.Now(), openRigGeneration())
	}
	resp, err := NewClient("").Call("SendText", map[string]string{"session": session, "text": body})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("the runtime's answer could not be re-encoded: %w", err)
	}
	return raw, nil
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

// ResolvedIdentity is one answer of the whoami chain, in the two forms its callers need.
type ResolvedIdentity struct {
	// Response is the identity, parsed: what a caller that REASONS with the identity reads (a roster to
	// address, a member name to compare against a policy).
	Response WhoamiResponse
	// Document is the answer as the transport rendered it, and it is non-nil ONLY when the OpenRig daemon
	// answered. The CLI prints that document unmodified, so a caller that PRINTS must use these bytes rather
	// than a re-encode of them; the runtime's and the environment's answers are values this build marshals
	// (printWhoami), so they are not carried twice.
	Document []byte
}

// ResolveIdentity answers who this seat is, over the ONE chain: the runtime socket first, then a rig's OpenRig
// daemon when this shell names one, then the seat's own environment. It is the chain `4genteam whoami` prints,
// extracted so that a SECOND caller (the seat guard, cmd/seatcheck) asks the same question the same way
// instead of exec-ing `rig whoami --json` — a name that does not exist after the cutover.
//
// `session` is the caller's explicit session name (the verb's --session); "" means the seat's own
// environment. The two transports read it differently and deliberately so, because that is what each one
// carries: the runtime is asked for the session this shell's environment spells (AGENTHUB_SESSION_NAME, the
// native name), while the daemon is asked for this shell's OpenRig session name (OPENRIG_SESSION_NAME) unless
// the caller named one, since that is the only name such a seat has.
//
// The failure is a SINGLE shape: a CodedError carrying ExitUnavailable whose message names the runtime that
// did not answer AND the environment that named no seat. A daemon that ANSWERED with a refusal keeps its own
// words and its own code (the CLI's exit for an unknown session), because a daemon that answered has
// answered — the same distinction a caller made before this function existed.
func ResolveIdentity(ctx context.Context, session string, compact bool) (ResolvedIdentity, error) {
	// THE RUNTIME FIRST. It is this client's own transport: a machine with a runtime and no OpenRig daemon
	// resolves identity here, which is what `whoami --json` has to do. Only the identity verb's ANSWER is
	// taken from it — a runtime that answers with a refusal ("could not be resolved from active rooms") has
	// not answered this question, and whoami's job is to RECOVER an identity, so the chain continues rather
	// than reporting one transport's mood.
	if ResolveTransport() == TransportRuntime {
		asked := session
		if asked == "" {
			asked = strings.TrimSpace(os.Getenv(agentHubSessionEnv))
		}
		if asked == "" {
			// A native seat may name its HALVES rather than the session (AGENTHUB_ROOM/AGENTHUB_SEAT, which is
			// the pair a native seat always states): the runtime's own helper renders that pair to exactly the
			// name below, so the seat asked for is THIS one rather than "the first seat the room lists" — the
			// reading a bare empty session would invite.
			room, seat, _, _ := seatEnvParts()
			asked = SeatSessionName(room, seat)
		}
		if resp, err := NewClient("").Whoami(asked); err == nil {
			return ResolvedIdentity{Response: *resp}, nil
		}
	}

	// A rig the OpenRig daemon hosts has no native room for this seat, so its daemon is asked SECOND, as the
	// fallback the live rig still runs, and its document is what a caller prints when it answers.
	openRigSeat := session
	if openRigSeat == "" {
		openRigSeat = openRigSession()
	}
	if openRigSeat != "" && openRigDaemonConfigured() {
		// Compact by default, exactly as the CLI asks the daemon (`if (!full) params.set("compact", "1")`,
		// whoami.ts); the caller that prints then projects the answer to the identity-recovery allowlist.
		raw, err := openRigWhoami(ctx, openRigSeat, compact)
		if err != nil {
			// A daemon that ANSWERED — it refused this session — has answered: report its words with the
			// raiser's own code (a 404 is ExitRemote, 1, the CLI's answer for an unknown session). Only a
			// daemon that is merely ABSENT (no configuration, or no answer at all) leaves the chain below to
			// recover the identity without it.
			if clientcmd.CodeOf(err, clientcmd.ExitUnavailable) != clientcmd.ExitUnavailable {
				return ResolvedIdentity{}, err
			}
		} else {
			// The document is the daemon's, so it is carried whole and re-read rather than re-typed: a field
			// this build has not heard of still reaches a caller that prints, and the parsed copy carries the
			// fields the shape in types.go names (identity.rigName/memberId, peers[].logicalId/sessionName).
			var parsed WhoamiResponse
			_ = json.Unmarshal(raw, &parsed)
			return ResolvedIdentity{Response: parsed, Document: raw}, nil
		}
	}

	// NO DAEMON AND NO RUNTIME ANSWER: the seat's own environment IS the identity. This is the path that makes
	// `whoami --json` work on a machine with no daemon at all — a native seat states AGENTHUB_ROOM /
	// AGENTHUB_SEAT, and a seat the OpenRig daemon hosts states its identity as OPENRIG_SESSION_NAME
	// ("<seat>@<rig>"). The runtime is NOT started here as a side effect: booting a daemon to answer an
	// identity question would make the answer depend on a process this call could have left alone.
	if resp, ok := seatEnvIdentity(); ok {
		return ResolvedIdentity{Response: resp}, nil
	}

	return ResolvedIdentity{}, &clientcmd.CodedError{
		Message: fmt.Sprintf("%s; no seat environment names this seat either (set %s and %s, or %s)",
			RuntimeMissing(""), agentHubRoomEnv, agentHubSeatEnv, openRigSessionEnv),
		Code: clientcmd.ExitUnavailable,
	}
}

// DeliverToSession delivers ONE message to ONE session over the transport this machine actually has: the
// runtime socket first, the OpenRig daemon as the fallback the live rig still runs. It is the native half of
// the seat guard's delivery (cmd/seatcheck calls it where it used to exec `rig send -- SESSION TEXT`), so the
// guard delivers through the SAME transports the `send` verb uses instead of through a CLI that does not
// exist after the cutover.
//
// NO DAEMON-ONLY SWITCH IS SET, and that is the whole shape of the call it replaces: the two-positional
// `rig send SESSION TEXT` carries no `--verify`/`--force`/`--wait-for-idle`/`--dangerously-interact`, so
// nothing here has to decide whether a handshake the runtime does not run was asked for. The text is wrapped
// in the same From/To/Sent envelope either way, attributed to this shell's own session name.
//
// The exit code is the client's own: 0 delivered, ExitRemote (1) when the answering transport refused it,
// ExitUnavailable (3) when no transport answered. A caller that must not report a policy result as a delivery
// failure maps it at its own boundary — the guard maps every non-zero to its own one delivery failure code.
func DeliverToSession(ctx context.Context, session, text string, stdout, stderr io.Writer) int {
	switch ResolveTransport() {
	case TransportRuntime:
		_, err := RuntimeSendText(session, text, SendOptions{})
		if err != nil {
			fmt.Fprintf(stderr, "4genteam send: the runtime at %s refused it: %v\n", DefaultSocketPath(), err)
			return clientcmd.ExitRemote
		}
		fmt.Fprintf(stdout, "Sent to %s\n", session)
		return clientcmd.ExitOK
	case TransportDaemon:
		result, status, err := OpenRigSend(ctx, session, text, SendOptions{})
		if err != nil {
			// A daemon that could not be REACHED has not refused anything: with the runtime first, the
			// transport that is missing is the runtime, so the refusal names it (and says the fallback was
			// tried) rather than blaming a $OPENRIG_URL the delivery was never going to reach. A daemon that
			// ANSWERED keeps its own words and its own code below.
			if clientcmd.CodeOf(err, clientcmd.ExitRemote) == clientcmd.ExitUnavailable {
				fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", FallbackMissing())
				return clientcmd.ExitUnavailable
			}
			fmt.Fprintf(stderr, "4genteam send: %v\n", err)
			return clientcmd.CodeOf(err, clientcmd.ExitRemote)
		}
		if status >= 400 {
			words := result.Error
			if words == "" {
				words = result.Detail
			}
			fmt.Fprintf(stderr, "4genteam send: the daemon refused it (HTTP %d) %s\n", status, words)
			return clientcmd.ExitRemote
		}
		fmt.Fprintf(stdout, "Sent to %s\n", session)
		// The advisory is not noise: it is the only signal that the message landed in a pane that was not
		// reading, and the guard's caller is the one party that can act on it.
		if warning := strings.TrimSpace(result.Warning); warning != "" {
			fmt.Fprintf(stdout, "Advisory: %s\n", warning)
		}
		return clientcmd.ExitOK
	default:
		fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", FallbackMissing())
		return clientcmd.ExitUnavailable
	}
}
