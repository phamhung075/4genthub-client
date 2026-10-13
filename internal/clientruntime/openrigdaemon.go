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

// OpenRigSendMessage delivers one message to one session through the OpenRig daemon: the shape
// `rig send <session> <text>` promises, and the shape this client's message deliverer already calls.
// The daemon resolves the session name against this machine's own session table and refuses an unknown
// one, so a typo is reported rather than typed into the wrong prompt.
func OpenRigSendMessage(ctx context.Context, session, text string) error {
	envelope := openRigSendBody(openRigSession(), session, text, time.Now(), openRigGeneration())
	return openRigPost(ctx, "/api/transport/send", map[string]string{"session": session, "text": envelope})
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

// openRigRequest performs one request against the daemon and returns its body as it arrived, so a verb
// that must show the daemon's own document can show it rather than a re-encode of it.
func openRigRequest(ctx context.Context, method, path string, payload any) ([]byte, error) {
	base := openRigDaemonURL()
	if base == "" {
		return nil, &clientcmd.CodedError{
			Message: "no OpenRig daemon is configured in this environment (set " + openRigURLEnv + " or " + openRigPortEnv + ")",
			Code:    clientcmd.ExitUnavailable,
		}
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, &clientcmd.CodedError{Message: fmt.Sprintf("cannot encode the request: %v", err), Code: clientcmd.ExitRemote}
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, &clientcmd.CodedError{Message: fmt.Sprintf("%s %s: %v", method, path, err), Code: clientcmd.ExitRemote}
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
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("the OpenRig daemon at %s is not answering: %v", base, err),
			Code:    clientcmd.ExitUnavailable,
		}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &clientcmd.CodedError{Message: fmt.Sprintf("%s %s: %v", method, path, err), Code: clientcmd.ExitRemote}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s %s: the daemon refused it (HTTP %d) %s", method, path, resp.StatusCode, daemonDetail(raw)),
			Code:    clientcmd.ExitRemote,
		}
	}
	return raw, nil
}

// openRigPost is openRigRequest with a JSON body.
func openRigPost(ctx context.Context, path string, payload any) error {
	_, err := openRigRequest(ctx, "POST", path, payload)
	return err
}

// openRigWhoami resolves one session's identity from the daemon, returning the daemon's document
// unchanged.
func openRigWhoami(ctx context.Context, session string) ([]byte, error) {
	return openRigRequest(ctx, "GET", "/api/whoami?sessionName="+url.QueryEscape(session), nil)
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
	label := strings.TrimSpace(sender)
	if label == "" {
		label = senderFallback
	}
	header := []string{"From: " + label, "To: " + recipient}
	if !stamp.IsZero() {
		suffix := ""
		if len(generation) >= 8 {
			suffix = " · gen " + generation[:8]
		}
		header = append(header, "Sent: "+stamp.UTC().Format("01-02 15:04")+"Z"+suffix)
	}
	reply := `↩ Reply: rig send ` + label + ` "..."`
	if strings.HasSuffix(label, "@external") {
		reply = `↩ Reply if needed: rig queue create --destination ` + label + ` --body "..." --verify`
	}
	return strings.Join(append(header, "---", body, "---", reply), "\n")
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
