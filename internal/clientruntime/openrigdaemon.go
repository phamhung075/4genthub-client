// The OpenRig-daemon half of the shim.
//
// WHY THIS EXISTS AT ALL. The client assumes the NATIVE runtime owns a seat: `whoami` asks its socket,
// and `send` addresses a session named `agenthub--<room>--<seat>`. A rig the OpenRig daemon hosts breaks
// both assumptions at once — the daemon's own tmux sessions are named `<rig>-<seat>@<rig>`, so the
// native namespace matches nothing, and the native room store (~/.config/4genthub/rooms) holds no row
// for a seat the daemon started. Measured on this rig: `rig send` and `rig whoami` both refuse, and
// every skill that calls them is broken with them.
//
// THE DAEMON IS THE OTHER NAME RESOLVER, AND IT IS ALREADY RUNNING. It exposes exactly the two things
// this file needs, on the port and URL the seat shell already carries in OPENRIG_*:
//
//	GET  /api/whoami?sessionName=<name>  the seat identity document the OpenRig CLI prints
//	POST /api/transport/send             type text into a session's pane on this machine
//
// So the shim asks the daemon rather than guessing, and `whoami --json` prints the daemon's own
// document: the shape a seat reads is the shape the OpenRig CLI returns because it IS that document,
// not a second rendering of it that can drift.
//
// THE SENDER IS THE SEAT'S ENV, NEVER A CALLER-SUPPLIED NAME. X-OpenRig-Session is stamped from
// OPENRIG_SESSION_NAME, which is what the daemon derives the recorded actor from. The rendered
// From:/To: envelope is the CLI's own wrapSendBody prose (see openRigSendBody), so a message sent
// through this shim arrives in a pane in the same wrapper as one sent by the OpenRig CLI itself — which
// matters beyond cosmetics: the client's own message deliverer shells out to `rig send` precisely to get
// that envelope, so the pane text is the receiver's only indication of who wrote it.
package clientruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// The seat shell's OpenRig variables, plus the legacy spelling of the session name (the client's own
// sender convention reads both, so an operator's pre-rename environment keeps working).
const (
	openRigSessionEnv    = "OPENRIG_SESSION_NAME"
	legacySessionEnv     = "RIGGED_SESSION_NAME"
	openRigURLEnv        = "OPENRIG_URL"
	openRigPortEnv       = "OPENRIG_PORT"
	openRigHomeEnv       = "OPENRIG_HOME"
	openRigBearerEnv     = "OPENRIG_TERMINAL_BEARER_TOKEN"
	openRigGenerationEnv = "OPENRIG_OCCUPANT_GENERATION"
)

// senderHeader is the one header the daemon reads a caller's identity from. The spelling matches the
// OpenRig CLI's SENDER_IDENTITY_HEADER; the server matches it case-insensitively.
const senderHeader = "X-OpenRig-Session"

// senderFallback is the honest label for a dispatch with no seat environment — the same literal the
// OpenRig CLI and the daemon's pane envelope both carry, so an unattributable send is labelled rather
// than made to look attributed.
const senderFallback = "<unknown sender>"

// openRigTimeout bounds one daemon request: this is a local socket, and a verb that hangs is worse than
// one that reports the daemon is not answering.
const openRigTimeout = 30 * time.Second

// SendOptions is the send verb's option set: the flags the OpenRig CLI documents, named as the route
// names them (the daemon's SendOpts). The zero value is the plain `rig send <session> <text>`.
type SendOptions struct {
	// Verify asks the daemon to re-confirm the snippet in its own post-send capture. The daemon answers
	// with `outcome`/`verified` — the strong positive is outcome "delivered", which means the text AND
	// the Enter landed and the capture re-confirmed the snippet (daemon/domain/session-transport.d.ts).
	Verify bool
	// Force is the CLI's back-compat no-op: a mid-task pane already sends-with-advisory. It rides the
	// body so the daemon's own combination rules (force cannot combine with wait-for-idle) stay the
	// authority rather than being re-implemented here.
	Force bool
	// WaitForIdleMs is the CLI's `--wait-for-idle <seconds>`, in the route's unit. 0 means not asked.
	WaitForIdleMs int
	// DangerouslyInteract types the text as prompt keystrokes; the daemon requires Reason and audits it.
	DangerouslyInteract bool
	Reason              string
	// Raw sends the text exactly, with no From/To/Sent envelope (the CLI's `--raw`, and what
	// --dangerously-interact implies).
	Raw bool
	// Sender is the identity the envelope is written AS, for a caller whose own environment cannot carry
	// it: the message deliverer runs OUTSIDE any seat shell and delivers text a human wrote, so it must
	// send AS that human rather than as the seat the deliverer happens to run under. This is what the
	// exec it replaces did by stamping the child's OPENRIG_SESSION_NAME. Blank means the caller's own
	// session, which is the plain `rig send`.
	Sender string
}

// SendResult is the daemon's answer to a send, in the daemon's own field names. `Warning` is not noise:
// it is the only signal a seat gets that its message went into a pane that was not reading (the
// readiness guard sends-with-advisory as of OPR.0.4.3.28), and the CLI surfaces it by design.
type SendResult struct {
	OK                bool
	SessionName       string
	Outcome           string // delivered | rendered-unconfirmed | failed | retained
	Verified          bool
	HasVerified       bool
	PromptInteraction string // enter-sent | unverified — means consumption was NOT established
	Warning           string
	Error             string
	// Detail is the answer's own words on one line, kept for a body this build could not read, so a
	// refusal is reported rather than turned into a bare status.
	Detail string
	// Raw is the answer exactly as it arrived. `--json` prints these bytes rather than a re-encode of
	// them, which is the only way the shim's stdout can be the daemon's document.
	Raw []byte
}

// OpenRigSend delivers one message to one session through the OpenRig daemon and returns the daemon's
// answer with the HTTP status, so the caller can surface a warning and mirror the CLI's exit codes
// (4xx → 1, 5xx → 2) instead of flattening every answer into one code. The daemon resolves the session
// name against this machine's own session table and refuses an unknown one, so a typo is reported
// rather than typed into the wrong prompt.
func OpenRigSend(ctx context.Context, session, text string, opts SendOptions) (SendResult, int, error) {
	payload := map[string]any{"session": session}
	// The CLI names every delivery (`deliveryId: randomUUID()`, send.ts:475) and the route reads that
	// field (transport.js:76). The daemon uses it as this delivery's outbox id (session-transport.js:1143
	// `opts?.deliveryId ?? guard-send-<uuid>`) and refuses a reuse that names different content or a
	// different sender rather than re-delivering it (:1160). Omitting it left every shim send on a
	// daemon-minted id, so a replayed shim send could never be recognised as the same delivery.
	if deliveryID := newDeliveryID(); deliveryID != "" {
		payload["deliveryId"] = deliveryID
	}
	if opts.Raw {
		payload["text"] = text
	} else {
		sender := opts.Sender
		if sender == "" {
			sender = openRigSession()
		}
		payload["text"] = openRigSendBody(sender, session, text, time.Now(), openRigGeneration())
	}
	if opts.Verify {
		payload["verify"] = true
	}
	if opts.Force {
		payload["force"] = true
	}
	if opts.WaitForIdleMs > 0 {
		payload["waitForIdleMs"] = opts.WaitForIdleMs
	}
	if opts.DangerouslyInteract {
		payload["dangerouslyInteract"] = true
		payload["reason"] = opts.Reason
	}

	raw, status, err := openRigCall(ctx, "POST", "/api/transport/send", payload)
	result := SendResult{Detail: daemonDetail(raw), Raw: raw}
	var decoded struct {
		OK                bool   `json:"ok"`
		SessionName       string `json:"sessionName"`
		Outcome           string `json:"outcome"`
		Verified          *bool  `json:"verified"`
		PromptInteraction string `json:"promptInteraction"`
		Warning           string `json:"warning"`
		Error             string `json:"error"`
	}
	if json.Unmarshal(raw, &decoded) == nil {
		result.OK, result.SessionName, result.Outcome = decoded.OK, decoded.SessionName, decoded.Outcome
		if decoded.Verified != nil {
			result.Verified, result.HasVerified = *decoded.Verified, true
		}
		result.PromptInteraction = decoded.PromptInteraction
		result.Warning, result.Error = decoded.Warning, decoded.Error
	}
	return result, status, err
}

// newDeliveryID mints the per-invocation delivery identity the OpenRig CLI sends as `deliveryId`
// (send.ts:475, `deliveryId: randomUUID()`), in the RFC 4122 v4 shape the CLI's UUIDs have. A random
// value per invocation — never a fixed one — is the CLI's own choice and the safe one: a constant id
// would make the daemon treat a deliberately repeated message as a replay of the first
// (`delivery_identity_conflict`, session-transport.js:1160-1175).
//
// It returns "" only when the kernel's random source fails. The send then proceeds without an id,
// exactly as it did before, rather than inventing a guessable one.
func newDeliveryID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// openRigSession is the session name this shell's environment claims, or "" when the shell is not a
// seat's.
func openRigSession() string {
	if session := strings.TrimSpace(os.Getenv(openRigSessionEnv)); session != "" {
		return session
	}
	return strings.TrimSpace(os.Getenv(legacySessionEnv))
}

// openRigGeneration is this seat's occupant generation, the daemon's own discriminator for the occupant
// rather than the seat. It rides the Sent: line as a short suffix, and an absent one is OMITTED rather
// than rendered as unknown.
func openRigGeneration() string { return strings.TrimSpace(os.Getenv(openRigGenerationEnv)) }

// openRigDaemonURL is the daemon's base URL: OPENRIG_URL, else the loopback address at OPENRIG_PORT.
// Both empty means this machine names no daemon, and the caller keeps the native path.
func openRigDaemonURL() string {
	if base := strings.TrimSpace(os.Getenv(openRigURLEnv)); base != "" {
		return strings.TrimRight(base, "/")
	}
	if port := strings.TrimSpace(os.Getenv(openRigPortEnv)); port != "" {
		if _, err := strconv.Atoi(port); err == nil {
			return "http://127.0.0.1:" + port
		}
	}
	return ""
}

// openRigDaemonConfigured reports whether this shell names a daemon at all. It is the gate that keeps a
// machine with no OpenRig daemon entirely on the native path.
func openRigDaemonConfigured() bool { return openRigDaemonURL() != "" }

// openRigTerminalToken is the bearer the OpenRig CLI sends when one exists: OPENRIG_TERMINAL_BEARER_TOKEN,
// else <OPENRIG_HOME>/terminal-token. Absent is normal on a loopback daemon — the header is then omitted
// entirely rather than sent empty, which is what the CLI does and what the daemon accepts.
func openRigTerminalToken() string {
	if token := strings.TrimSpace(os.Getenv(openRigBearerEnv)); token != "" {
		return token
	}
	home := strings.TrimSpace(os.Getenv(openRigHomeEnv))
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(user, ".openrig")
	}
	raw, err := os.ReadFile(filepath.Join(home, "terminal-token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// openRigCall performs one request against the daemon and returns the body as it arrived, the HTTP
// status, and — only for a transport failure — an error.
//
// The status is the caller's business because two verbs must mirror the CLI: `send` reads a `warning`
// out of a SUCCESS body (dropping it was a gate finding), and an unknown session must end at exit 1
// (the daemon answers 404; the CLI exits 1) rather than at this package's generic code.
func openRigCall(ctx context.Context, method, path string, payload any) ([]byte, int, error) {
	base := openRigDaemonURL()
	if base == "" {
		return nil, 0, &clientcmd.CodedError{
			Message: "no OpenRig daemon is configured in this environment (set " + openRigURLEnv + " or " + openRigPortEnv + ")",
			Code:    clientcmd.ExitUnavailable,
		}
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, &clientcmd.CodedError{Message: fmt.Sprintf("cannot encode the request: %v", err), Code: clientcmd.ExitRemote}
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, 0, &clientcmd.CodedError{Message: fmt.Sprintf("%s %s: %v", method, path, err), Code: clientcmd.ExitRemote}
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := openRigTerminalToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if session := openRigSession(); session != "" {
		req.Header.Set(senderHeader, session)
	}

	resp, err := (&http.Client{Timeout: openRigTimeout}).Do(req)
	if err != nil {
		return nil, 0, &clientcmd.CodedError{
			Message: fmt.Sprintf("the OpenRig daemon at %s is not answering: %v", base, err),
			Code:    clientcmd.ExitUnavailable,
		}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, &clientcmd.CodedError{Message: fmt.Sprintf("%s %s: %v", method, path, err), Code: clientcmd.ExitRemote}
	}
	return raw, resp.StatusCode, nil
}

// openRigRequest is openRigCall for the callers that only want a document: a non-2xx becomes an error
// carrying the daemon's own words.
//
// The code is ExitRemote (1) for both a 4xx refusal and a 5xx failure. The CLI numbers a 5xx as 2, but 2
// is this client's ExitUsage — "the verb exists and the arguments are wrong" — so spending it on a
// server failure would tell the caller the wrong story; the 404 case the gate named (exit 1) is matched
// exactly, and the 5xx divergence is named in the change record rather than hidden behind the reuse.
func openRigRequest(ctx context.Context, method, path string, payload any) ([]byte, error) {
	raw, status, err := openRigCall(ctx, method, path, payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s %s: the daemon refused it (HTTP %d) %s", method, path, status, daemonDetail(raw)),
			Code:    clientcmd.ExitRemote,
		}
	}
	return raw, nil
}

// openRigWhoami resolves one session's identity from the daemon, returning the daemon's document
// unchanged. `compact` asks the daemon for its own compact projection — the CLI sends `compact=1` by
// default (`if (!full) params.set("compact", "1")`) — rather than asking for the full payload and
// dropping fields here, which would also pay for the contextUsage/runtimeContext compute the daemon
// skips in compact mode.
func openRigWhoami(ctx context.Context, session string, compact bool) ([]byte, error) {
	query := "?sessionName=" + url.QueryEscape(session)
	if compact {
		query += "&compact=1"
	}
	return openRigRequest(ctx, "GET", "/api/whoami"+query, nil)
}

// daemonDetail is the daemon's own `error` field when it sent one, else its body, on one line.
func daemonDetail(raw []byte) string {
	var decoded struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &decoded) == nil && strings.TrimSpace(decoded.Error) != "" {
		return decoded.Error
	}
	detail := strings.Join(strings.Fields(string(raw)), " ")
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return detail
}

// openRigSendBody renders the envelope the OpenRig CLI wraps a one-to-one send in: the From/To/Sent
// header, the body between two `---` rules, and the reply hint. Its field order, its `MM-DD HH:MMZ`
// stamp, its `· gen <first8>` suffix and its two reply spellings are the CLI's wrapSendBody/renderToLine
// ones — the pane text is a receiver's whole account of who wrote the message, so the shim owes it the
// same prose rather than a lookalike.
func openRigSendBody(sender, recipient, body string, stamp time.Time, generation string) string {
	// The label is rendered EXACTLY as it arrived — the CLI's twin is
	// `sender.trim().length > 0 ? sender : SENDER_FALLBACK`, i.e. only the blank TEST is on the trimmed
	// value (gate finding m4: the shim rendered the trimmed one).
	label := sender
	if strings.TrimSpace(label) == "" {
		label = senderFallback
	}
	header := []string{"From: " + label, "To: " + recipient}
	if !stamp.IsZero() {
		// The generation is rendered by PRESENCE and truncated to eight characters, which is the CLI's
		// condition (`meta.genUuid && meta.genUuid.length > 0 ? slice(0, 8) : ""`). Gate finding m3: the
		// `len >= 8` test this replaces dropped a 1-7 character generation the CLI renders.
		suffix := ""
		if generation != "" {
			suffix = " · gen " + firstEight(generation)
		}
		header = append(header, "Sent: "+stamp.UTC().Format("01-02 15:04")+"Z"+suffix)
	}
	reply := `↩ Reply: 4genteam send ` + label + ` "..."`
	if strings.HasSuffix(label, "@external") {
		// The footer this replaces printed the OpenRig CLI's own queue write. On this box that write is
		// refused by name ("create is not a read this shim performs", exit 2), so the footer told a
		// reader to run something that does not exist here. Rather than name another verb, it states
		// the limit: nothing local delivers to a destination that is not a session.
		reply = `↩ Reply: ` + label + ` is not a local session, and this client's queue reads only - no local command delivers a reply to it.`
	}
	return strings.Join(append(header, "---", body, "---", reply), "\n")
}

// firstEight is the CLI's `value.slice(0, 8)`: the first eight characters, or the whole value when it is
// shorter. It is a truncation, never a filter.
func firstEight(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

// printOpenRigIdentity renders the daemon's document the way `rig whoami` renders it: the identity
// block, one field per line. A document this build cannot read is shown as it arrived rather than
// refused — the daemon is the authority on its own JSON, and a shim that refuses to print it is worse
// than one that prints it plainly.
func printOpenRigIdentity(stdout io.Writer, raw []byte) int {
	var doc struct {
		ResolvedBy string `json:"resolvedBy"`
		Identity   struct {
			RigName        string `json:"rigName"`
			LogicalID      string `json:"logicalId"`
			PodNamespace   string `json:"podNamespace"`
			PodID          string `json:"podId"`
			MemberID       string `json:"memberId"`
			SessionName    string `json:"sessionName"`
			Runtime        string `json:"runtime"`
			AttachmentType string `json:"attachmentType"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintln(stdout, strings.Join(strings.Fields(string(raw)), " "))
		return clientcmd.ExitOK
	}
	pod := doc.Identity.PodNamespace
	if pod == "" {
		pod = doc.Identity.PodID
	}
	fmt.Fprintf(stdout, "Rig:        %s\n", orDash(doc.Identity.RigName))
	fmt.Fprintf(stdout, "Logical ID: %s\n", orDash(doc.Identity.LogicalID))
	fmt.Fprintf(stdout, "Pod:        %s / %s\n", orDash(pod), orDash(doc.Identity.MemberID))
	fmt.Fprintf(stdout, "Session:    %s\n", orDash(doc.Identity.SessionName))
	fmt.Fprintf(stdout, "Runtime:    %s\n", orDash(doc.Identity.Runtime))
	fmt.Fprintf(stdout, "Transport:  %s\n", orDash(doc.Identity.AttachmentType))
	fmt.Fprintf(stdout, "Resolved:   via %s\n", orDash(strings.ReplaceAll(doc.ResolvedBy, "_", " ")))
	return clientcmd.ExitOK
}

// orDash is a field the daemon did not carry. The CLI prints an em dash there too.
func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
