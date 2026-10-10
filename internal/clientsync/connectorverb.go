// The connector verb: the flow that proves the wire. Discover one local session, read its transcript,
// REDACT LOCALLY, then hello -> session -> events on /ws/connector, and report the ids the cloud
// assigned.
//
// ONE SESSION, ONE DIRECTION, ON PURPOSE. Multi-session, JSONL tailing, rig capture, reconnect with
// backoff and the frontend are the rest of C1 and are deliberately NOT here: this slice exists to
// prove that a real session reaches the cloud under the right account, and every extra moving part
// would make a failure harder to attribute.
package clientsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// connectorUsage is the whole surface of this verb.
const connectorUsage = `usage: 4genteam sync connector [--session <name>] [--connector-id <id>] [--lines <n>]

Sends ONE local rig session to the cloud over /ws/connector.
  --session <name>      the rig session to send; otherwise the first rig ps --json reports
  --connector-id <id>   the connector identity the server binds this socket to (default: hostname)
  --lines <n>           transcript lines to send, newest last (default 50)

Requires AGENTHUB_URL and AGENTHUB_TOKEN. The token needs the sessions:write scope; without it the
server completes the handshake and closes with 1008, which this verb reports rather than retries.`

// RunConnectorVerb drives the slice and returns a process exit code.
func RunConnectorVerb(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	session, connectorID, lines := "", "", 50
	for i := 0; i < len(args); i++ {
		value := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch args[i] {
		case "--session":
			v, ok := value()
			if !ok {
				fmt.Fprintln(stderr, connectorUsage)
				return clientcmd.ExitUsage
			}
			session = v
		case "--connector-id":
			v, ok := value()
			if !ok {
				fmt.Fprintln(stderr, connectorUsage)
				return clientcmd.ExitUsage
			}
			connectorID = v
		case "--lines":
			v, ok := value()
			if !ok {
				fmt.Fprintln(stderr, connectorUsage)
				return clientcmd.ExitUsage
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				fmt.Fprintf(stderr, "sync connector: --lines must be a positive integer, got %q\n", v)
				return clientcmd.ExitUsage
			}
			lines = n
		case "-h", "--help":
			fmt.Fprintln(stdout, connectorUsage)
			return 0
		default:
			fmt.Fprintf(stderr, "sync connector: unknown argument %q\n%s\n", args[i], connectorUsage)
			return clientcmd.ExitUsage
		}
	}

	baseURL, err := RequireEnv("AGENTHUB_URL")
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: %v\n", err)
		return clientcmd.ExitUsage
	}
	token, err := RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: %v\n", err)
		return clientcmd.ExitUsage
	}

	if connectorID == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "4genteam"
		}
		connectorID = host
	}

	if session == "" {
		session, err = firstRigSession(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "sync connector: %v\n", err)
			return clientcmd.ExitRemote
		}
	}

	transcript, err := rigTranscript(ctx, session)
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: %v\n", err)
		return clientcmd.ExitRemote
	}
	events := transcriptEvents(transcript, lines)
	if len(events) == 0 {
		fmt.Fprintf(stderr, "sync connector: the session %q has no transcript lines to send\n", session)
		return clientcmd.ExitRemote
	}

	conn, err := DialConnector(ctx, baseURL, token)
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: %v\n", err)
		return clientcmd.ExitRemote
	}
	defer conn.Close()

	if err := conn.Hello(connectorID); err != nil {
		fmt.Fprintf(stderr, "sync connector: hello failed: %v\n", err)
		return clientcmd.ExitRemote
	}
	sessionID, err := conn.RegisterSession(session, session)
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: session registration failed: %v\n", err)
		return clientcmd.ExitRemote
	}
	lastSeq, err := conn.AppendEvents(session, events)
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: appending events failed: %v\n", err)
		return clientcmd.ExitRemote
	}

	// The report is the acceptance's own nouns: which connector, which session, which session id the
	// cloud assigned, how many events, and the resume point. Never the token.
	fmt.Fprintf(stdout, "connector %s\n", connectorID)
	fmt.Fprintf(stdout, "session %s -> %s\n", session, sessionID)
	fmt.Fprintf(stdout, "events %d, last_seq %d\n", len(events), lastSeq)
	return 0
}

// rigSessionCandidates asks the rig which session names exist, in the order `rig ps --json` reports
// them. The shape of `rig ps --json` is not re-derived here: every plausible key is accepted, a
// candidate without a usable name is skipped, and duplicates are dropped rather than listed twice.
//
// It exists separately from firstRigSession because FIRST is only a safe answer when there is exactly
// one: this environment reports fourteen sessions, so a verb that types text into a prompt must be able
// to see the whole set and refuse, rather than taking the first and hoping. That distinction is why the
// extraction is here and the refusal is at the caller.
func rigSessionCandidates(ctx context.Context) ([]string, error) {
	out, err := runRig(ctx, "ps", "--json")
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		return nil, fmt.Errorf("rig ps --json did not answer JSON: %w", err)
	}
	var candidates []map[string]any
	switch v := decoded.(type) {
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				candidates = append(candidates, m)
			}
		}
	case map[string]any:
		for _, key := range []string{"sessions", "items", "nodes", "result"} {
			list, ok := v[key].([]any)
			if !ok {
				continue
			}
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					candidates = append(candidates, m)
				}
			}
			if len(candidates) > 0 {
				break
			}
		}
	}
	var names []string
	seen := map[string]bool{}
	for _, c := range candidates {
		for _, key := range []string{"sessionName", "session_name", "name", "id", "logicalId"} {
			value, ok := c[key].(string)
			if !ok || value == "" {
				continue
			}
			if !seen[value] {
				seen[value] = true
				names = append(names, value)
			}
			break
		}
	}
	return names, nil
}

// firstRigSession returns the FIRST session the rig reports. ITS BEHAVIOUR IS UNCHANGED by the
// extraction above, deliberately: other verbs call it, and redefining what "the first" means for them
// is a different change with a different blast radius. A verb that TYPES text into a prompt does not use
// this — it reads the whole candidate set and refuses when it holds more than one.
func firstRigSession(ctx context.Context) (string, error) {
	names, err := rigSessionCandidates(ctx)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("rig ps --json named no session this verb can send; pass --session <name>")
	}
	return names[0], nil
}

func rigTranscript(ctx context.Context, session string) (string, error) {
	return runRig(ctx, "transcript", session)
}

func runRig(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "rig", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("rig %s failed: %s", strings.Join(args, " "), detail)
	}
	return stdout.String(), nil
}

// transcriptEvents turns the tail of a transcript into the event shapes the wire takes, REDACTING
// EACH LINE FIRST. Redaction runs before the line is wrapped, so a secret never exists in a frame.
func transcriptEvents(transcript string, lines int) []map[string]any {
	trimmed := strings.Split(strings.TrimRight(transcript, "\n"), "\n")
	if len(trimmed) == 1 && trimmed[0] == "" {
		return nil
	}
	if len(trimmed) > lines {
		trimmed = trimmed[len(trimmed)-lines:]
	}
	events := make([]map[string]any, 0, len(trimmed))
	for _, line := range trimmed {
		events = append(events, MessageEvent(map[string]any{"text": Redact(line)}))
	}
	return events
}
