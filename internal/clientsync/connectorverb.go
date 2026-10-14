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
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// connectorUsage is the whole surface of this verb.
const connectorUsage = `usage: 4genteam sync connector [--session <name>] [--connector-id <id>] [--lines <n>]

Sends ONE local rig session to the cloud over /ws/connector.
  --session <name>      the rig session to send (required)
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

	// THE MACHINE-WIDE FALLBACK IS DELETED HERE TOO, for the reason the messages verb states: it was a shell-out to
	// the CLI the cutover removes, so it could not survive it. This surface has ONE way to name the session, so the
	// refusal names it and says what it takes.
	if session == "" {
		fmt.Fprintf(stderr, "sync connector: --session <name> is required — it names the local rig session this connector sends to the cloud\n")
		return clientcmd.ExitUsage
	}

	// The seat this session belongs to, as the RUNTIME reports it. When the runtime names no entry for
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

// rigSessionCandidates asks the RUNTIME which sessions exist. It is the read the connector's whole seat
// resolution rests on: the runtime answers one entry per seat of every room it hosts, in the CLI's own keys,
// so the same names reach the same code that used to read `rig ps --json`.
//
// THE CLI REACH IS RETIRED RATHER THAN WRAPPED. This exec-ed `rig ps --json` - the shell-out the cutover
// removes (DEPLOY-READY section 65, the parked runtime request this unit answers) - and it now reads the
// runtime's own socket through the one client this package already uses for a room's status.
func rigSessionCandidates(ctx context.Context) ([]sessionCandidate, error) {
	entries, err := clientruntime.NewClient("").ListSessions("")
	if err != nil {
		return nil, err
	}
	return candidatesFrom(entries), nil
}

// candidatesFrom turns the runtime's entries into the candidates the sync verbs resolve seats with.
//
// THIS IS NOT A SECOND PARSER OF THE CLI'S LOOSE SHAPES. Every name it reads is a typed field the runtime
// guarantees and pins by test (`clientruntime.SessionEntry`'s json tags ARE that contract). The CLI allowed
// six spellings of a name and three of a status because its output was nobody's contract; this input is one,
// so the tolerance that existed for the CLI's sake went with the CLI. What stays is what the candidate list
// itself needs: a candidate with no usable name is skipped, and duplicates are dropped, because two entries
// naming one session is a runtime bug and listing it twice would double every event uploaded.
//
// CANONICAL FIRST, and that order is a fix rather than a preference: the canonical name
// (<pod>-<member>@<rig>) is the one the UI shows and the one a person passes to --session, so a verb that
// printed the logicalId instead addressed sessions by an identifier nobody types.
func candidatesFrom(entries []clientruntime.SessionEntry) []sessionCandidate {
	var result []sessionCandidate
	seen := map[string]bool{}
	for _, entry := range entries {
		c := sessionCandidate{
			identifiers: entryIdentifiers(entry),
			rigName:     entry.RigName,
			member:      memberOf(entry.LogicalID),
			state:       entry.SessionStatus,
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
	return result
}

// entryIdentifiers lists every name an entry answers to, MOST CANONICAL FIRST and without repeats, so the
// first is the one a verb should print and type.
func entryIdentifiers(entry clientruntime.SessionEntry) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range []string{entry.CanonicalSessionName, entry.SessionName, entry.LogicalID} {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// memberOf is the seat key: the member after the FIRST dot of a logical id, by the rule the rest of this
// client already uses (clientbridge.SeatName). An id carrying no dot names no member, so the candidate
// reports NO seat rather than one named after its own pod.
func memberOf(logicalID string) string {
	_, member, found := strings.Cut(logicalID, ".")
	if !found || member == "" {
		return ""
	}
	return member
}

// rigSeatFor returns the room and the seat key the rig reports for a session, or two empty strings
// when it reports no entry naming it. A rig query that fails is not an error here: the session and
// its transcript are still worth reporting, and a session with no seat is one the UI shows without
// an input box.
//
// IT IS NO LONGER A DECLARED REACH: the candidate list comes from the runtime's own session list
// (`ListSessions`), which is the interface DEPLOY-READY section 65 parked and this unit builds. The
// alternative it used to wait for - a contract change where the frame carries no room or seat at all -
// remains a different decision, and it is not this one.
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

// rigTranscript reads a session's transcript through the RUNTIME, from the file that seat's runner publishes.
//
// THE OLD PATH COULD NOT WORK WHERE IT MATTERED: `rig transcript` is not a verb the installed client has
// ("4genteam: unknown command transcript"), so the exec this replaces was not a slower route to the same
// bytes - it was a reach that failed wherever the installed client was what `rig` resolved to.
func rigTranscript(ctx context.Context, session string) (string, error) {
	answer, err := clientruntime.NewClient("").Transcript(session, 0)
	if err != nil {
		return "", err
	}
	return answer.Content, nil
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
