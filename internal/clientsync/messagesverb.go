// The messages verb: pull the text a window addressed to a seat, put it into that seat's own local
// session, and ACKNOWLEDGE each message only after the text landed.
//
// THE ACK COMES LAST, AND THAT ORDER IS THE CONTRACT. Delivery is at-least-once: the cloud hands a
// message out until a client says it arrived, so an interruption — this process dying, the rig
// refusing, the ack never reaching the cloud — means the message comes back rather than being lost.
// The price of that is a duplicate in exactly one window, when the text landed but the ACK did not, so
// this verb keeps a small LOCAL LEDGER of the ids it has already typed and re-acks those instead of
// typing them twice. That is the client's half of the same contract the connector keeps for the session
// stream (the resume point is stored client-side there for the same reason).
//
// THE SESSION IS NOT DERIVED FROM THE SEAT, AND THAT IS DELIBERATE. No server table carries a seat's
// session — the store is keyed by (room, seat) and seat_status has no session column — so there is
// nothing to derive it from, and no seat-to-session lookup this verb can rely on. It therefore resolves
// the session the way the connector verb does (`--session <name>`, else the single session
// `rig ps --json` reports) and REFUSES when that is ambiguous: typing an operator's message into the
// wrong prompt is worse than not delivering it, because the wrong prompt is somebody else's.
//
// The transport is the package's own RequestJSON (rigspec.go), so this verb reports the cloud's
// refusals in the shape every other verb here does rather than in one of its own.
package clientsync

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientbridge"
	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// messagesPath is one seat's messages, by room and seat: the pull and the ack both hang off it.
const messagesPath = "/api/v2/openrig/rooms/%s/seats/%s/messages"

// messagesUsage is the verb's whole surface.
const messagesUsage = `usage: 4genteam sync messages <room> <seat> [--session <name>] [--limit <n>] [--dry-run] [--out <dir>]

Pulls the text addressed to one seat and puts it into that seat's own session, one message at a time,
acknowledging each ONLY after the text landed. An unacknowledged message is handed out again on the next
run, so nothing is lost; a message this client already typed is re-acknowledged rather than typed twice.

Requires AGENTHUB_URL and AGENTHUB_TOKEN. The ack names this machine (its hostname) so the audit says
which client typed the message.

The session is --session <name>, or the single session ` + "`rig ps --json`" + ` reports; the verb refuses rather
than guessing which prompt to type into.

  --limit <n>  the page size asked of the cloud (the cloud's own default and clamp apply)
  --dry-run    pull and print, send and acknowledge nothing
  --out <dir>  where the delivered ledger lives (default ` + "`~/.openrig/agenthub-seats`" + `)`

// HTTPTimeout is not redefined here: RequestJSON owns the timeout this package uses.

// sendToSession is the LOCAL delivery step. It is a package variable so a test can substitute it: the
// real one types the text into a session on this machine, which no test can do — and no other machine
// can do at all, which is the whole reason the cloud stores the text instead of delivering it.
var sendToSession = func(ctx context.Context, session, text string) error {
	_, err := runRig(ctx, "send", session, text)
	return err
}

// seatMessage is one pulled message, as the pull describes it.
type seatMessage struct {
	ID        string
	Room      string
	Seat      string
	Text      string
	CreatedAt string
}

// messagesLedgerPath is this client's own record of what it has already typed for one seat. It sits
// beside the seat store rather than inside a snapshot, because it is the client's memory of its own
// deliveries and has nothing to do with a pinned seat definition.
//
// Room and seat are constrained to the platform's name rule (no separators), so joining them with a
// dash cannot collide two pairs into one file.
func messagesLedgerPath(out, room, seat string) string {
	return filepath.Join(out, "delivered", room+"-"+seat+".ids")
}

// RunMessagesVerb drives the pull-deliver-ack loop and returns a process exit code.
func RunMessagesVerb(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, messagesUsage)
		return clientcmd.ExitOK
	}
	room, seat, session, out, limit, dryRun, err := parseMessagesArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync messages: %v\n%s\n", err, messagesUsage)
		return clientcmd.ExitUsage
	}
	baseURL, err := RequireEnv("AGENTHUB_URL")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return clientcmd.ExitUsage
	}
	token, err := RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return clientcmd.ExitUsage
	}
	// Whichever session to type into is resolved ONCE, before anything is pulled: a run that cannot
	// deliver must not spend its ACKs. And it is resolved from the WHOLE candidate set rather than from
	// the first entry: `rig ps --json` reports every session this machine runs (fourteen here), so
	// taking the first would type an operator's message into somebody else's prompt — the one outcome
	// worse than not delivering it.
	if !dryRun && session == "" {
		candidates, err := rigSessionCandidates(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam sync messages: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		if len(candidates) == 0 {
			fmt.Fprintf(stderr, "4genteam sync messages: `rig ps --json` named no session to type into; pass --session <name>\n")
			return clientcmd.ExitUnavailable
		}
		if len(candidates) > 1 {
			fmt.Fprintf(stderr, "4genteam sync messages: %d sessions are running (%s), so this verb will not guess which prompt to type into — the wrong prompt is somebody else's. Name this seat's own with --session <name>\n",
				len(candidates), strings.Join(candidates, ", "))
			return clientcmd.ExitUnavailable
		}
		session = candidates[0]
	}

	ledgerPath := messagesLedgerPath(out, room, seat)
	typed, err := readMessagesLedger(ledgerPath)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync messages: %v\n", err)
		return clientcmd.ExitUsage
	}

	messages, cursor, err := pullMessages(ctx, baseURL, token, room, seat, limit)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync messages: %v\n", err)
		return clientcmd.ExitRemote
	}
	if len(messages) == 0 {
		fmt.Fprintf(stdout, "%s/%s: nothing is waiting\n", room, seat)
		return 0
	}

	delivered, reAcked, failed := 0, 0, 0
	for _, m := range messages {
		if m.ID == "" {
			fmt.Fprintf(stderr, "4genteam sync messages: the cloud returned a message with no id; refusing to type text that could never be acknowledged\n")
			return clientcmd.ExitRemote
		}
		if _, ok := typed[m.ID]; ok {
			// Already typed here once: re-ack instead of typing it a second time.
			if dryRun {
				fmt.Fprintf(stdout, "would re-ack %s (already typed here)\n", m.ID)
				continue
			}
			if err := ackMessage(ctx, baseURL, token, room, seat, m.ID); err != nil {
				fmt.Fprintf(stderr, "4genteam sync messages: re-acking %s failed: %v\n", m.ID, err)
				return clientcmd.ExitRemote
			}
			reAcked++
			continue
		}
		if dryRun {
			fmt.Fprintf(stdout, "would deliver %s: %s\n", m.ID, oneLine(m.Text))
			continue
		}
		if err := sendToSession(ctx, session, m.Text); err != nil {
			// NO ACK and no ledger entry: the message stays pending, so the next run delivers it.
			fmt.Fprintf(stderr, "4genteam sync messages: putting %s into session %s failed: %v\n", m.ID, session, err)
			failed++
			continue
		}
		// The text LANDED, so the ledger records it BEFORE the ack: that record is what stops the one
		// duplicate at-least-once allows, and it must not depend on the call that follows it.
		ledgerErr := appendMessagesLedger(ledgerPath, m.ID)
		if err := ackMessage(ctx, baseURL, token, room, seat, m.ID); err != nil {
			if ledgerErr != nil {
				fmt.Fprintf(stderr, "4genteam sync messages: %v\n", ledgerErr)
			}
			fmt.Fprintf(stderr, "4genteam sync messages: %s reached session %s but the ack failed: %v\n", m.ID, session, err)
			failed++
			continue
		}
		if ledgerErr != nil {
			fmt.Fprintf(stderr, "4genteam sync messages: %s was delivered and acknowledged, but this client could not record it: %v\n", m.ID, ledgerErr)
		}
		delivered++
	}
	fmt.Fprintf(stdout, "%s/%s: %d delivered, %d re-acked, %d failed\n", room, seat, delivered, reAcked, failed)
	if cursor != "" {
		fmt.Fprintf(stdout, "cursor %s\n", cursor)
	}
	if failed > 0 {
		return clientcmd.ExitRemote
	}
	return 0
}

// parseMessagesArgs is the verb's argument grammar: two positionals, then the three flags. The shape
// mirrors `sync pull`'s parser, including where it refuses rather than defaulting.
func parseMessagesArgs(args []string) (room, seat, session, out string, limit int, dryRun bool, err error) {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--session" || arg == "--out" || arg == "--limit":
			if i+1 >= len(args) {
				return "", "", "", "", 0, false, fmt.Errorf("%s needs a value", arg)
			}
			i++
			switch arg {
			case "--session":
				session = args[i]
			case "--out":
				out = args[i]
			case "--limit":
				n, convErr := strconv.Atoi(args[i])
				if convErr != nil || n <= 0 {
					return "", "", "", "", 0, false, fmt.Errorf("--limit needs a positive number, got %q", args[i])
				}
				limit = n
			}
		case arg == "--dry-run":
			dryRun = true
		case strings.HasPrefix(arg, "-"):
			return "", "", "", "", 0, false, fmt.Errorf("unknown argument %s", arg)
		default:
			switch {
			case room == "":
				room = arg
			case seat == "":
				seat = arg
			default:
				return "", "", "", "", 0, false, fmt.Errorf("unexpected argument %s (messages takes a room and a seat)", arg)
			}
		}
	}
	if room == "" || seat == "" {
		return "", "", "", "", 0, false, fmt.Errorf("messages needs a room and a seat")
	}
	out, err = resolveSeatStore(out)
	if err != nil {
		return "", "", "", "", 0, false, err
	}
	return room, seat, session, out, limit, dryRun, nil
}

// messagesURL is the one path this verb uses, with the seat escaped into it.
func messagesURL(room, seat string) string {
	return fmt.Sprintf(messagesPath, url.PathEscape(room), url.PathEscape(seat))
}

// pullMessages is one page of a seat's pending text. RequestJSON owns the transport and the refusal
// shapes; what is read here is the page itself, and the CURSOR IS TAKEN AS OPAQUE — it is carried to
// the caller and echoed back, never composed here.
func pullMessages(ctx context.Context, baseURL, token, room, seat string, limit int) ([]seatMessage, string, error) {
	path := messagesURL(room, seat)
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	body, err := RequestJSON(ctx, "GET", baseURL, token, path, nil)
	if err != nil {
		return nil, "", err
	}
	if success, isBool := body["success"].(bool); !isBool || !success {
		return nil, "", remoteFailure("GET", path, "returned an error response")
	}
	rows, isList := body["messages"].([]any)
	if !isList {
		return nil, "", remoteFailure("GET", path, "response has no messages")
	}
	messages := make([]seatMessage, 0, len(rows))
	for _, row := range rows {
		entry, isObject := row.(map[string]any)
		if !isObject {
			return nil, "", remoteFailure("GET", path, "returned a message that is not an object")
		}
		id, _ := entry["id"].(string)
		text, _ := entry["text"].(string)
		createdAt, _ := entry["created_at"].(string)
		messageRoom, _ := entry["room"].(string)
		messageSeat, _ := entry["seat"].(string)
		messages = append(messages, seatMessage{ID: id, Room: messageRoom, Seat: messageSeat, Text: text, CreatedAt: createdAt})
	}
	cursor, _ := body["cursor"].(string)
	return messages, cursor, nil
}

// ackMessage records that one message reached the seat's session. It is what stops the next pull
// handing the same message out again, so a refusal here is reported rather than retried blindly: the
// ledger already holds the id, and the next run re-acks instead of typing the text twice.
func ackMessage(ctx context.Context, baseURL, token, room, seat, id string) error {
	path := messagesURL(room, seat) + "/" + url.PathEscape(id) + "/ack"
	body, err := RequestJSON(ctx, "POST", baseURL, token, path, map[string]string{"machine_id": clientbridge.HostMachineID()})
	if err != nil {
		return err
	}
	if success, isBool := body["success"].(bool); !isBool || !success {
		return remoteFailure("POST", path, "returned an error response")
	}
	return nil
}

// readMessagesLedger loads the ids this client has already typed for this seat. A missing ledger is an
// empty one: a first run has no history.
func readMessagesLedger(path string) (map[string]struct{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("reading the delivered ledger %s: %w", path, err)
	}
	typed := map[string]struct{}{}
	for _, line := range strings.Split(string(raw), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			typed[id] = struct{}{}
		}
	}
	return typed, nil
}

// appendMessagesLedger records one delivered id. The file grows by one short line per delivery and is
// never trimmed: it is the client's only memory of what it has typed, and forgetting an entry would
// type an operator's message twice.
func appendMessagesLedger(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating the delivered ledger directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening the delivered ledger %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(id + "\n"); err != nil {
		return fmt.Errorf("writing the delivered ledger %s: %w", path, err)
	}
	return nil
}

// oneLine renders a message's text for a --dry-run report: one line, so a multi-line message does not
// read as several.
func oneLine(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	if len(flat) > 120 {
		flat = flat[:120] + "..."
	}
	return flat
}
