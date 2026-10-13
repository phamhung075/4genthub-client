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
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
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
		return room, seat, seatEnvSessionName(room, seat), true
	}
	name := openRigSession()
	at := strings.LastIndex(name, "@")
	if at <= 0 || at == len(name)-1 {
		return "", "", "", false
	}
	return strings.TrimSpace(name[at+1:]), strings.TrimSpace(name[:at]), name, true
}

// seatEnvSessionName is the native session name for a room/seat pair — the one shape the runtime's own
// `session_name` helper renders — or "" when either half is missing (a half-named seat names no session).
func seatEnvSessionName(room, seat string) string {
	if room == "" || seat == "" {
		return ""
	}
	return "agenthub--" + room + "--" + seat
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
