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
// WHERE A SESSION COMES FROM DEPENDS ON THE SHAPE, AND THE REFUSAL IS ABOUT GUESSING. No SERVER table
// carries a seat's session — the store is keyed by (room, seat) and seat_status has no session column
// — so the single-seat shape (room and seat) resolves what it has the way the connector verb does
// (`--session <name>`, else the single session `rig ps --json` reports) and REFUSES when that is
// ambiguous: typing an operator's message into the wrong prompt is worse than not delivering it,
// because the wrong prompt is somebody else's. The rig shape (`--rig R`) derives each session from
// THIS MACHINE's own node table instead — `rig ps --nodes --rig R --json` names every seat of the rig
// and the canonical session it answers to — and skips a seat that is not running rather than typing
// into a stopped session.
//
// THE RIG SHAPE IS THE SUPERVISED ONE. `sync messages --rig R --watch` is a child of rigd, so it runs
// one pass over every seat, writes a heartbeat per pass, and never exits on a transient failure: the
// next pass retries whatever the cloud, the rig or one seat's session refused.
//
// The transport is the package's own RequestJSON (rigspec.go), so this verb reports the cloud's
// refusals in the shape every other verb here does rather than in one of its own.
package clientsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientbridge"
	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// messagesPath is one seat's messages, by room and seat: the pull and the ack both hang off it.
const messagesPath = "/api/v2/openrig/rooms/%s/seats/%s/messages"

// messagesUsage is the verb's whole surface.
const messagesUsage = `usage: 4genteam sync messages <room> <seat> [--session <name>] [--limit <n>] [--dry-run] [--out <dir>]
       4genteam sync messages --rig <rig> [--watch] [--limit <n>] [--dry-run] [--out <dir>]

Pulls the text addressed to a seat — or to every seat of a rig — and puts it into that seat's own
session, one message at a time, acknowledging each ONLY after the text landed. An unacknowledged
message is handed out again on the next run, so nothing is lost; a message this client already typed is
re-acknowledged rather than typed twice.

Requires AGENTHUB_URL and AGENTHUB_TOKEN. The ack names this machine (its hostname) so the audit says
which client typed the message.

In the room-and-seat shape the session is --session <name>, or the single session ` + "`rig ps --json`" + ` reports;
the verb refuses rather than guessing which prompt to type into.

In the --rig shape the room is the rig itself and every seat's own session is read from this machine's
node table (` + "`rig ps --nodes --rig <rig> --json`" + `); a seat that is not running is skipped, and its messages
stay pending until it is back. rigd supervises this shape with --watch: one pass every 10 s, one
heartbeat per pass, and a transient failure waits for the next pass instead of ending the child.

  --limit <n>  the page size asked of the cloud (the cloud's own default and clamp apply)
  --dry-run    pull and print, send and acknowledge nothing (a --rig read is one pass; refused with --watch)
  --out <dir>  where the delivered ledger lives (default ` + "`~/.openrig/agenthub-seats`" + `)`

// HTTPTimeout is not redefined here: RequestJSON owns the timeout this package uses.

// humanSeatSession is the identity a delivered message is sent AS. The text was written by a human and
// is read in the seat's own pane, and that pane's only confirmed envelope from a human carried exactly
// `From: human@kernel` — the same session the pane's reply hint addresses back.
const humanSeatSession = "human@kernel"

// sendToSession is the LOCAL delivery step. It is a package variable so a test can substitute it: the
// real one types the text into a session on this machine, which no test can do — and no other machine
// can do at all, which is the whole reason the cloud stores the text instead of delivering it.
//
// The envelope is left ON (no --raw): From/To/Sent is the shape the pane already renders, and it is
// what tells the seat who wrote the text. The sender itself is carried in the environment —
// `rig send` reads OPENRIG_SESSION_NAME — so the deliverer, which runs outside any seat shell, stamps
// the human's session on every delivery.
var sendToSession = func(ctx context.Context, session, text string) error {
	cmd := exec.CommandContext(ctx, "rig", "send", session, text)
	cmd.Env = humanSenderEnv(os.Environ())
	done, err := cmd.CombinedOutput()
	if err != nil {
		detail := oneLine(strings.TrimSpace(string(done)))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("rig send %s: %s", session, detail)
	}
	return nil
}

// humanSenderEnv stamps the human's session name onto a delivery's environment, replacing the older
// spelling rather than appending a duplicate: two OPENRIG_SESSION_NAME entries would leave the winner
// to exec's order, and the sender must be the human every time.
func humanSenderEnv(environ []string) []string {
	stamped := make([]string, 0, len(environ)+1)
	for _, pair := range environ {
		if strings.HasPrefix(pair, "OPENRIG_SESSION_NAME=") || strings.HasPrefix(pair, "RIGGED_SESSION_NAME=") {
			continue
		}
		stamped = append(stamped, pair)
	}
	return append(stamped, "OPENRIG_SESSION_NAME="+humanSeatSession)
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
	parsed, err := parseMessagesArgs(args)
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
	// The rig shape is the supervised one: one pass over every seat of the rig, or the same pass
	// forever. Each seat's session is resolved inside the pass, from the machine's own node table.
	if parsed.rig != "" {
		if parsed.watch {
			return runMessagesWatch(ctx, baseURL, token, parsed, stdout, stderr)
		}
		if failed := messagesPass(ctx, baseURL, token, parsed, stdout, stderr); failed > 0 {
			return clientcmd.ExitRemote
		}
		return clientcmd.ExitOK
	}
	// Whichever session to type into is resolved ONCE, before anything is pulled: a run that cannot
	// deliver must not spend its ACKs. And it is resolved from the WHOLE candidate set rather than from
	// the first entry: `rig ps --json` reports every session this machine runs (fourteen here), so
	// taking the first would type an operator's message into somebody else's prompt — the one outcome
	// worse than not delivering it.
	session := parsed.session
	if !parsed.dryRun && session == "" {
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
				len(candidates), strings.Join(candidateNames(candidates), ", "))
			return clientcmd.ExitUnavailable
		}
		session = candidates[0].name
	}
	done, err := syncSeatMessages(ctx, baseURL, token, parsed.room, parsed.seat, session, parsed.out, parsed.limit, parsed.dryRun, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync messages: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
	}
	if done.waiting == 0 {
		fmt.Fprintf(stdout, "%s/%s: nothing is waiting\n", parsed.room, parsed.seat)
		return clientcmd.ExitOK
	}
	fmt.Fprintf(stdout, "%s/%s: %d delivered, %d re-acked, %d failed\n", parsed.room, parsed.seat, done.delivered, done.reAcked, done.failed)
	if done.cursor != "" {
		fmt.Fprintf(stdout, "cursor %s\n", done.cursor)
	}
	if done.failed > 0 {
		return clientcmd.ExitRemote
	}
	return clientcmd.ExitOK
}

// seatSync is what one seat's turn did: what the cloud handed out, what landed, and the cursor that
// came with the page. A turn that ends in an error stopped early, and the counts are what it managed
// before that.
type seatSync struct {
	waiting, delivered, reAcked, failed int
	cursor                              string
}

// syncSeatMessages is one seat's whole turn: read this client's ledger, pull the waiting text, and for
// each message type it into the session, record it, and acknowledge it LAST. The single-seat verb and
// every seat of a supervised rig pass run this same turn, so the at-least-once order exists once.
func syncSeatMessages(ctx context.Context, baseURL, token, room, seat, session, out string, limit int, dryRun bool, stdout, stderr io.Writer) (seatSync, error) {
	var done seatSync
	ledgerPath := messagesLedgerPath(out, room, seat)
	typed, err := readMessagesLedger(ledgerPath)
	if err != nil {
		return done, &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
	}

	messages, cursor, err := pullMessages(ctx, baseURL, token, room, seat, limit)
	if err != nil {
		return done, &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitRemote}
	}
	done.waiting, done.cursor = len(messages), cursor

	for _, m := range messages {
		if m.ID == "" {
			return done, &clientcmd.CodedError{
				Message: "the cloud returned a message with no id; refusing to type text that could never be acknowledged",
				Code:    clientcmd.ExitRemote,
			}
		}
		if _, ok := typed[m.ID]; ok {
			// Already typed here once: re-ack instead of typing it a second time.
			if dryRun {
				fmt.Fprintf(stdout, "would re-ack %s (already typed here)\n", m.ID)
				continue
			}
			if err := ackMessage(ctx, baseURL, token, room, seat, m.ID); err != nil {
				return done, &clientcmd.CodedError{Message: fmt.Sprintf("re-acking %s failed: %v", m.ID, err), Code: clientcmd.ExitRemote}
			}
			done.reAcked++
			continue
		}
		if dryRun {
			fmt.Fprintf(stdout, "would deliver %s: %s\n", m.ID, oneLine(m.Text))
			continue
		}
		if err := sendToSession(ctx, session, m.Text); err != nil {
			// NO ACK and no ledger entry: the message stays pending, so the next run delivers it.
			fmt.Fprintf(stderr, "4genteam sync messages: putting %s into session %s failed: %v\n", m.ID, session, err)
			done.failed++
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
			done.failed++
			continue
		}
		if ledgerErr != nil {
			fmt.Fprintf(stderr, "4genteam sync messages: %s was delivered and acknowledged, but this client could not record it: %v\n", m.ID, ledgerErr)
		}
		done.delivered++
	}
	return done, nil
}

// messagesArgs is one invocation of `sync messages`. The two shapes are exclusive: a room and a seat
// (one prompt), or --rig (every running seat the rig reports).
type messagesArgs struct {
	room, seat, session string
	rig                 string
	out                 string
	limit               int
	dryRun, watch       bool
}

// parseMessagesArgs is the verb's argument grammar: two positionals or --rig, then the flags. The
// shape mirrors `sync pull`'s parser, including where it refuses rather than defaulting.
func parseMessagesArgs(args []string) (messagesArgs, error) {
	var parsed messagesArgs
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--session" || arg == "--out" || arg == "--limit" || arg == "--rig":
			if i+1 >= len(args) {
				return messagesArgs{}, fmt.Errorf("%s needs a value", arg)
			}
			i++
			switch arg {
			case "--session":
				parsed.session = args[i]
			case "--out":
				parsed.out = args[i]
			case "--rig":
				parsed.rig = args[i]
			case "--limit":
				n, convErr := strconv.Atoi(args[i])
				if convErr != nil || n <= 0 {
					return messagesArgs{}, fmt.Errorf("--limit needs a positive number, got %q", args[i])
				}
				parsed.limit = n
			}
		case arg == "--dry-run":
			parsed.dryRun = true
		case arg == "--watch":
			parsed.watch = true
		case strings.HasPrefix(arg, "-"):
			return messagesArgs{}, fmt.Errorf("unknown argument %s", arg)
		default:
			switch {
			case parsed.room == "":
				parsed.room = arg
			case parsed.seat == "":
				parsed.seat = arg
			default:
				return messagesArgs{}, fmt.Errorf("unexpected argument %s (messages takes a room and a seat)", arg)
			}
		}
	}
	switch {
	case parsed.rig != "" && (parsed.room != "" || parsed.seat != ""):
		return messagesArgs{}, fmt.Errorf("name a room and a seat, or --rig <rig> — not both")
	case parsed.rig != "" && parsed.session != "":
		return messagesArgs{}, fmt.Errorf("--session names one seat's session; with --rig each seat's own session is resolved from the rig")
	case parsed.watch && parsed.rig == "":
		return messagesArgs{}, fmt.Errorf("--watch delivers for a rig's seats; it needs --rig <rig>")
	case parsed.watch && parsed.dryRun:
		return messagesArgs{}, fmt.Errorf("--dry-run reads once and exits; it cannot watch")
	case parsed.rig == "" && (parsed.room == "" || parsed.seat == ""):
		return messagesArgs{}, fmt.Errorf("messages needs a room and a seat, or --rig <rig>")
	}
	out, err := resolveSeatStore(parsed.out)
	if err != nil {
		return messagesArgs{}, err
	}
	parsed.out = out
	return parsed, nil
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

// rigNode is the part of a rig's node table (`rig ps --nodes --rig R --json`) the deliverer reads:
// logicalId names the seat, canonicalSessionName the session `rig send` reaches it by, and
// sessionStatus says whether there is a live prompt to type into.
type rigNode struct {
	LogicalID            string `json:"logicalId"`
	CanonicalSessionName string `json:"canonicalSessionName"`
	SessionStatus        string `json:"sessionStatus"`
}

// seatSession is one seat of a rig and the session its prompt lives in.
type seatSession struct {
	seat, session string
}

// rigSeats maps a rig's node table to the seats a pass may type into. A node that is not running is
// skipped rather than typed into: a stopped session has no prompt reading it, so text typed there
// would be acknowledged for a delivery nobody saw. Skipping is safe — the message stays pending, and
// the pass after the watchdog has the seat back delivers it.
func rigSeats(nodes []rigNode, rig string) []seatSession {
	prefix := rig + "."
	seats := make([]seatSession, 0, len(nodes))
	for _, node := range nodes {
		if !strings.HasPrefix(node.LogicalID, prefix) || node.CanonicalSessionName == "" || node.SessionStatus != "running" {
			continue
		}
		seats = append(seats, seatSession{seat: strings.TrimPrefix(node.LogicalID, prefix), session: node.CanonicalSessionName})
	}
	return seats
}

// messagesPass is one delivery pass over every seat a rig reports, and the number of seats whose turn
// ended in failure. The watch ignores that count — the next pass retries — while a one-shot run exits
// non-zero on it. The room is the rig slug: the panel writes to the room named in the seat's own
// address, and a rig and its room carry the same name.
func messagesPass(ctx context.Context, baseURL, token string, parsed messagesArgs, stdout, stderr io.Writer) int {
	out, err := runRig(ctx, "ps", "--nodes", "--rig", parsed.rig, "--json")
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync messages: %v\n", err)
		return 1
	}
	var nodes []rigNode
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		fmt.Fprintf(stderr, "4genteam sync messages: `rig ps --nodes --rig %s` did not answer with a node list: %v\n", parsed.rig, err)
		return 1
	}
	seats := rigSeats(nodes, parsed.rig)
	if len(seats) == 0 {
		fmt.Fprintf(stderr, "4genteam sync messages: rig %s reports no running seat to deliver to\n", parsed.rig)
		return 1
	}
	failed := 0
	for _, target := range seats {
		done, err := syncSeatMessages(ctx, baseURL, token, parsed.rig, target.seat, target.session, parsed.out, parsed.limit, parsed.dryRun, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam sync messages: %s/%s: %v\n", parsed.rig, target.seat, err)
			failed++
			continue
		}
		if done.delivered > 0 || done.reAcked > 0 || done.failed > 0 {
			fmt.Fprintf(stdout, "%s/%s: %d delivered, %d re-acked, %d failed\n", parsed.rig, target.seat, done.delivered, done.reAcked, done.failed)
		}
		if done.failed > 0 {
			failed++
		}
	}
	return failed
}

// runMessagesWatch is the shape rigd supervises: deliver for every running seat of one rig, forever.
// One pass, then one heartbeat, then one sleep. A pass never exits the process on a transient failure
// — a stopped cloud, a missing rig or one seat's refusal must not end the child, and rigd restarts a
// child that dies — and the heartbeat is written on EVERY pass, because a beat that only accompanies
// activity cannot tell a hung deliverer from a quiet one.
func runMessagesWatch(ctx context.Context, baseURL, token string, parsed messagesArgs, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "%s: delivering for every running seat, one pass every %s\n", parsed.rig, clientservices.MessagesEvery)
	for ctx.Err() == nil {
		messagesPass(ctx, baseURL, token, parsed, stdout, stderr)
		if err := clientservices.WriteHeartbeat(clientservices.Messages, parsed.rig); err != nil {
			fmt.Fprintf(stderr, "4genteam sync messages: writing the heartbeat: %v\n", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(clientservices.MessagesEvery):
		}
	}
	return clientcmd.ExitOK
}
