// The send verb's two pieces of shared vocabulary: the canonical name of a seat's session, and the refusal a
// send owes when the RUNTIME answered but the request carried a switch only the daemon runs.
//
// WHY THEY ARE IN A FILE OF THEIR OWN, rather than beside ResolveTransport: transport.go and ipc.go were held by
// other writers when this was written, and both built the same session name inline - `seatEnvSessionName` in
// transport.go, and the `agenthub--%s--%s` in ipc.go. BOTH NOW CALL THIS ONE, so the format string has a single
// home on the client side: this file. The runtime derives the same name on its own side, which no Go constant can
// reach. Nothing here replaces the transport seam that U3 landed, and nothing here changes either transport: it is
// the name both of them address, and one sentence about which of them stopped a send.
package clientruntime

import "fmt"

// SeatSessionName is the client's OWN name for a seat's session: `agenthub--<room>--<seat>`, or "" when either half
// is missing (a half-named seat names no session, which is the contract the transport seam relied on).
//
// It is derived, not looked up, because the runtime derives it the same way (`ipc.go`, and the runtime side's
// own session naming): a verb that has a RIG and a SEAT must be able to address the session without first
// asking the room for its status, which costs a round trip and fails for a seat the room has not reported.
// The room is the RIG here, which is what `--state-dir`/the socket's own rooms call it.
func SeatSessionName(room, seat string) string {
	if room == "" || seat == "" {
		return ""
	}
	return fmt.Sprintf("agenthub--%s--%s", room, seat)
}

// DaemonSwitchMissing is the refusal a send owes when the RUNTIME answered but the request carried a switch
// only the OpenRig daemon runs, and the daemon is not there either. `switches` is the caller's own list, in
// the CLI's spelling ("--verify, --wait-for-idle"), so the sentence names the flags that stopped it.
//
// IT MUST NOT SAY THE RUNTIME IS MISSING, and that is the whole reason it is not `FallbackMissing(...)`.
// FallbackMissing's first clause is "no runtime is answering at <socket>", which is true when the runtime
// really was the transport that failed - and FALSE here, where the runtime answered, is healthy, and simply
// does not run this handshake. A refusal that sends the reader to inspect a working transport hides which
// party actually stopped the send; the clause this function adds is about the daemon, and is chosen from this
// machine's own configuration exactly as FallbackMissing chooses its own.
func DaemonSwitchMissing(switches string) string {
	clause := "no OpenRig daemon is configured in this environment (set " + openRigURLEnv + " or " + openRigPortEnv + ")"
	if base := openRigDaemonURL(); base != "" {
		clause = "the OpenRig fallback at " + base + " is not available either"
	}
	return switches + " needs the OpenRig daemon, and " + clause
}
