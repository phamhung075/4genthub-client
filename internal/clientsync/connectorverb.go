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

	"github.com/phamhung075/4genthub-client/internal/clientbridge"
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

	// The seat this session belongs to, as `rig ps --json` reports it. When the rig names no entry for
	// the session, the frame carries NO room and NO seat - the server records a session it was not told
	// the seat of, rather than one it was told to guess.
	room, seat := rigSeatFor(ctx, session)

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
	// ONE SHOT, AND HONEST ABOUT IT: this verb registers the session with the state it can see and the
	// transcript source, and sends no cursor - it is not tailing anything, so claiming a position in the
	// source would make the cloud resume a stream nobody keeps. The long-running `rigd` is the tail.
	ack, err := conn.RegisterSession(SessionRegistration{
		Key: session, Name: session, Room: room, Seat: seat,
		State: SessionRunning, Source: SourceTranscript,
	})
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: session registration failed: %v\n", err)
		return clientcmd.ExitRemote
	}
	eventsAck, err := conn.AppendEvents(session, events, "")
	if err != nil {
		fmt.Fprintf(stderr, "sync connector: appending events failed: %v\n", err)
		return clientcmd.ExitRemote
	}

	// The report is the acceptance's own nouns: which connector, which session, which session id the
	// cloud assigned, how many events, and the resume point. Never the token.
	fmt.Fprintf(stdout, "connector %s\n", connectorID)
	fmt.Fprintf(stdout, "session %s -> %s\n", session, ack.SessionID)
	fmt.Fprintf(stdout, "events %d, last_seq %d\n", len(events), eventsAck.LastSeq)
	return 0
}

// sessionCandidate is one entry `rig ps --json` reports for a running session: the names a verb may
// address it by, the rig that runs it, and the member inside that rig.
//
// THE RIG IS THE ROOM and the member after the FIRST dot of logicalId is the SEAT KEY. Every rig the
// server renders is named after its room (seat_management's rigspec names a rig and its pod after the
// room slug), and this client already splits logicalId that way for the seat-status report
// (clientbridge.SeatName). A candidate carrying neither is left with no seat, and the session frame
// then names neither room nor seat rather than guessing one.
type sessionCandidate struct {
	name        string
	identifiers []string
	rigName     string
	member      string
	// state is the node's status reduced to 2.2's two values: the `session` frame carries it, and a
	// seat that leaves the node list is sent once with `stopped`.
	state string
}

// answersTo reports whether a verb naming this session means this entry: --session may carry the
// canonical name, the logicalId, or whatever else `rig ps --json` listed.
func (c sessionCandidate) answersTo(session string) bool {
	for _, id := range c.identifiers {
		if id == session {
			return true
		}
	}
	return false
}

// rigSessionCandidates asks the rig which sessions exist, in the order `rig ps --json` reports them.
// The shape of `rig ps --json` is not re-derived here: every plausible key is accepted, a candidate
// without a usable name is skipped, and duplicates are dropped rather than listed twice.
//
// CANONICAL FIRST, and that order is a fix rather than a preference: the real output carries
// canonicalSessionName (<pod>-<member>@<rig>, the name the UI shows and the one a person passes to
// --session), rigName and logicalId, and NO plain "name" - so the previous key order fell through to
// logicalId and the default path addressed sessions by an identifier the UI never shows.
//
// It exists separately from firstRigSession because FIRST is only a safe answer when there is exactly
// one: this environment reports fourteen sessions, so a verb that types text into a prompt must be able
// to see the whole set and refuse, rather than taking the first and hoping. That distinction is why the
// extraction is here and the refusal is at the caller.
func rigSessionCandidates(ctx context.Context) ([]sessionCandidate, error) {
	out, err := runRig(ctx, "ps", "--json")
	if err != nil {
		return nil, err
	}
	return parseRigPsSessions(out)
}

// parseRigPsSessions reads `rig ps --json` output - a list, or an object carrying one under sessions,
// items, nodes or result - into candidates.
func parseRigPsSessions(out string) ([]sessionCandidate, error) {
	var decoded any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		return nil, fmt.Errorf("rig ps --json did not answer JSON: %w", err)
	}
	var entries []map[string]any
	switch v := decoded.(type) {
	case []any:
		entries = sessionMaps(v)
	case map[string]any:
		for _, key := range []string{"sessions", "items", "nodes", "result"} {
			list, ok := v[key].([]any)
			if !ok {
				continue
			}
			entries = sessionMaps(list)
			if len(entries) > 0 {
				break
			}
		}
	}
	var result []sessionCandidate
	seen := map[string]bool{}
	for _, entry := range entries {
		c := sessionCandidate{
			identifiers: sessionIdentifiers(entry),
			rigName:     sessionRigName(entry),
			member:      sessionMember(entry),
			state:       sessionState(entry),
		}
		if len(c.identifiers) == 0 {
			continue
		}
		c.name = c.identifiers[0]
		if seen[c.name] {
			continue
		}
		seen[c.name] = true
		result = append(result, c)
	}
	return result, nil
}

// candidateNames is the list a refusal prints: the names the verb would have had to choose between.
func candidateNames(candidates []sessionCandidate) []string {
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.name)
	}
	return names
}

func sessionMaps(list []any) []map[string]any {
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// sessionNameKeys are the keys an entry may carry a name under, MOST CANONICAL FIRST.
var sessionNameKeys = []string{"canonicalSessionName", "sessionName", "session_name", "name", "id", "logicalId"}

// sessionIdentifiers lists every name an entry answers to, in that order and without repeats, so the
// first is the one a verb should print and type.
func sessionIdentifiers(entry map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, key := range sessionNameKeys {
		value, ok := entry[key].(string)
		if !ok || value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sessionRigName(entry map[string]any) string {
	for _, key := range []string{"rigName", "rig_name"} {
		if value, ok := entry[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// sessionStateKeys are the keys a node record may carry its status under, most authoritative first.
var sessionStateKeys = []string{"sessionStatus", "session_status", "status"}

// sessionState is the node's status as one of 2.2's two session states. ONLY `running` is running: a
// `detached` or `stopped` node is not a live session, and the Sessions page's `seat_state` distinguishes
// a reported stop (stopped) from no report at all (null), so a guess here would erase that difference.
func sessionState(entry map[string]any) string {
	for _, key := range sessionStateKeys {
		value, ok := entry[key].(string)
		if !ok || value == "" {
			continue
		}
		if value == SessionRunning {
			return SessionRunning
		}
		return SessionStopped
	}
	return ""
}

// sessionMember is the seat key: the member after the FIRST dot of logicalId, by the rule the rest of
// this client already uses (clientbridge.SeatName). A logicalId carrying no dot names no member, so
// the candidate reports NO seat rather than one named after its own pod.
func sessionMember(entry map[string]any) string {
	logical, _ := entry["logicalId"].(string)
	if !strings.Contains(logical, ".") {
		return ""
	}
	return clientbridge.SeatName(entry)
}

// rigSeatFor returns the room and the seat key the rig reports for a session, or two empty strings
// when it reports no entry naming it. A rig query that fails is not an error here: the session and
// its transcript are still worth reporting, and a session with no seat is one the UI shows without
// an input box.
func rigSeatFor(ctx context.Context, session string) (string, string) {
	candidates, err := rigSessionCandidates(ctx)
	if err != nil {
		return "", ""
	}
	for _, c := range candidates {
		if c.answersTo(session) {
			return c.rigName, c.member
		}
	}
	return "", ""
}

// firstRigSession returns the FIRST session the rig reports. ITS BEHAVIOUR IS UNCHANGED by the
// extraction above, deliberately: other verbs call it, and redefining what "the first" means for them
// is a different change with a different blast radius. A verb that TYPES text into a prompt does not use
// this — it reads the whole candidate set and refuses when it holds more than one.
func firstRigSession(ctx context.Context) (string, error) {
	candidates, err := rigSessionCandidates(ctx)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("rig ps --json named no session this verb can send; pass --session <name>")
	}
	return candidates[0].name, nil
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
