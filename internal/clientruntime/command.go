package clientruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// Commands returns the clientruntime commands to register in 4genteam.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{
		whoamiCommand{},
		queueCommand{},
		captureCommand{},
		shimCommand{},
	}
}

type whoamiCommand struct{}

func (whoamiCommand) Name() string    { return "whoami" }
func (whoamiCommand) Summary() string { return "report seat identity (Rule 1 parity, zero OpenRig)" }
func (whoamiCommand) NeedsRig() bool  { return false }

func (whoamiCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam whoami [--json] [--full] [--session <name>]")
		fmt.Fprintln(stdout, "\nReports the seat identity. The runtime's own socket is asked FIRST — it is this client's")
		fmt.Fprintln(stdout, "transport and needs no OpenRig daemon — and only when no runtime answers is a rig the")
		fmt.Fprintln(stdout, "OpenRig daemon hosts asked, as the fallback the live rig still runs. With neither, the")
		fmt.Fprintln(stdout, "seat's own environment (AGENTHUB_ROOM/AGENTHUB_SEAT, or OPENRIG_SESSION_NAME) is the")
		fmt.Fprintln(stdout, "identity, so `whoami --json` works with no daemon at all.")
		fmt.Fprintln(stdout, "\nWith --json the document is the identity-recovery allowlist the OpenRig CLI prints by default;")
		fmt.Fprintln(stdout, "--full prints the daemon's complete payload instead.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("whoami", flag.ContinueOnError)
	jsonFlag := flags.Bool("json", false, "print as JSON")
	sessionFlag := flags.String("session", "", "specific session name")
	fullFlag := flags.Bool("full", false, "print the complete payload instead of the compact projection")
	verboseFlag := flags.Bool("verbose", false, "the CLI's synonym for --full")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	full := *fullFlag || *verboseFlag

	// THE CHAIN IS ONE FUNCTION (ResolveIdentity): the runtime socket first, the OpenRig daemon as the
	// fallback the live rig still runs, the seat's own environment last — so this verb PRINTS an answer and
	// the seat guard (cmd/seatcheck) reasons about the same one, instead of each exec-ing `rig whoami --json`
	// or re-deriving the order. `full` decides whether the daemon is asked for its compact projection.
	resolved, err := ResolveIdentity(ctx, *sessionFlag, !full)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam whoami: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
	}

	// A document at all means the OpenRig daemon answered: the CLI prints the payload, not a pretty-print of
	// it, and projects it to the identity-recovery allowlist unless --full was asked for.
	if resolved.Document != nil {
		if *jsonFlag {
			out := resolved.Document
			if !full {
				out = projectCompactWhoami(resolved.Document)
			}
			_, _ = stdout.Write(out)
			_, _ = stdout.Write([]byte("\n"))
			return clientcmd.ExitOK
		}
		return printOpenRigIdentity(stdout, resolved.Document)
	}

	// The runtime's answer and the environment's are VALUES, printed by the ONE rendering of this client's
	// own identity document, so the two sources cannot drift apart.
	return printWhoami(stdout, stderr, &resolved.Response, *jsonFlag)
}

// seatEnvIdentity builds the identity document from the seat's own environment, and reports whether there
// was one to build. It reads the SAME pair the runtime's own `environment` fallback reads (AGENTHUB_ROOM /
// AGENTHUB_SEAT / AGENTHUB_ROLE / AGENTHUB_RUNTIME) and, for a seat those do not name, the OpenRig seat's
// session name, which carries both halves in one string.
//
// resolvedBy stays "environment": that field is the reader's only way to tell an identity the daemon
// resolved from one this shell asserted, and dressing a guess up as a daemon answer is the class of
// untruth this verb exists to avoid.
func seatEnvIdentity() (WhoamiResponse, bool) {
	room, seat, session, ok := seatEnvParts()
	if !ok {
		return WhoamiResponse{}, false
	}
	runtime := strings.TrimSpace(os.Getenv(agentHubRuntimeEnv))
	if runtime == "" {
		runtime = strings.TrimSpace(os.Getenv(openRigRuntimeEnv))
	}
	return WhoamiResponse{
		ResolvedBy: "environment",
		Identity: WhoamiIdentity{
			RigName:     room,
			MemberId:    seat,
			Runtime:     runtime,
			SessionName: session,
			LogicalId:   seat,
			Role:        strings.TrimSpace(os.Getenv(agentHubRoleEnv)),
		},
		Peers: []WhoamiPeer{},
		Edges: WhoamiEdges{
			Outgoing: []WhoamiEdgeTarget{},
			Incoming: []WhoamiEdgeTarget{},
		},
		Transcript: WhoamiTranscript{
			Enabled: true,
		},
	}, true
}

// printWhoami is the ONE rendering of the client's own identity document, so the runtime's answer and the
// seat's environment print the same way: --json the document, otherwise the fields a reader is looking for.
func printWhoami(stdout, stderr io.Writer, resp *WhoamiResponse, asJSON bool) int {
	if asJSON {
		data, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam whoami: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		_, _ = stdout.Write(data)
		_, _ = stdout.Write([]byte("\n"))
		return clientcmd.ExitOK
	}
	fmt.Fprintf(stdout, "Rig:     %s\n", resp.Identity.RigName)
	fmt.Fprintf(stdout, "Seat:    %s\n", resp.Identity.MemberId)
	fmt.Fprintf(stdout, "Role:    %s\n", resp.Identity.Role)
	fmt.Fprintf(stdout, "Runtime: %s\n", resp.Identity.Runtime)
	fmt.Fprintf(stdout, "Session: %s\n", resp.Identity.SessionName)
	return clientcmd.ExitOK
}

// projectCompactWhoami is the OpenRig CLI's identity-recovery allowlist (whoami.ts
// projectCompactWhoami): exactly the fields the boot and compaction-restore contracts treat as ground
// truth. It is an ALLOWLIST rather than a denylist — the CLI's reason is worth keeping, because it is
// the difference between a stable boot path and one that grows with the payload's payload: a field the
// daemon adds later defaults to --full instead of silently re-bloating every boot.
//
// The daemon's document is the authority on its own JSON, so a body this build cannot read is printed
// as it arrived rather than replaced by an empty projection.
func projectCompactWhoami(raw []byte) []byte {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return raw
	}
	projected := map[string]any{}
	for _, key := range []string{"resolvedBy", "peersNote", "edges"} {
		if value, ok := doc[key]; ok {
			projected[key] = value
		}
	}
	if identity, ok := doc["identity"].(map[string]any); ok {
		projected["identity"] = pickFields(identity, "rigName", "nodeId", "logicalId", "podId", "podNamespace", "memberId", "sessionName", "runtime")
	}
	if peers, ok := doc["peers"].([]any); ok {
		kept := make([]any, 0, len(peers))
		for _, peer := range peers {
			if fields, ok := peer.(map[string]any); ok {
				kept = append(kept, pickFields(fields, "logicalId", "sessionName", "runtime"))
				continue
			}
			kept = append(kept, peer)
		}
		projected["peers"] = kept
	}
	if transcript, ok := doc["transcript"].(map[string]any); ok {
		projected["transcript"] = pickFields(transcript, "path", "tailCommand")
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return raw
	}
	return encoded
}

// pickFields keeps the named fields and nothing else. Go marshals a map in key order rather than
// insertion order, so the projection's FIELD SET is the CLI's while its key order is canonical — named
// in the change record rather than passed off as byte-identical stdout.
func pickFields(fields map[string]any, names ...string) map[string]any {
	kept := make(map[string]any, len(names))
	for _, name := range names {
		if value, ok := fields[name]; ok {
			kept[name] = value
		}
	}
	return kept
}

type shimCommand struct{}

func (shimCommand) Name() string { return "rig-shim" }
func (shimCommand) Summary() string {
	return "install ~/.local/bin/rig compatibility symlink to 4genteam"
}
func (shimCommand) NeedsRig() bool { return false }

func (shimCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam rig-shim\n\nInstalls ~/.local/bin/rig -> ~/.local/bin/4genteam compatibility symlink.")
		return clientcmd.ExitOK
	}
	home := os.Getenv("HOME")
	if home == "" {
		fmt.Fprintln(stderr, "HOME not set")
		return clientcmd.ExitUnavailable
	}

	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		fmt.Fprintf(stderr, "failed to create %s: %v\n", localBin, err)
		return clientcmd.ExitUnavailable
	}

	target := filepath.Join(localBin, "4genteam")
	linkPath := filepath.Join(localBin, "rig")

	// Remove existing rig if it's a symlink or old binary
	_ = os.Remove(linkPath)

	if err := os.Symlink(target, linkPath); err != nil {
		fmt.Fprintf(stderr, "failed to symlink %s -> %s: %v\n", linkPath, target, err)
		return clientcmd.ExitUnavailable
	}

	fmt.Fprintf(stdout, "Installed rig compatibility shim: %s -> %s\n", linkPath, target)
	return clientcmd.ExitOK
}

type queueCommand struct{}

func (queueCommand) Name() string    { return "queue" }
func (queueCommand) Summary() string { return "queue compatibility shim for seat and bridge tasks" }
func (queueCommand) NeedsRig() bool  { return false }

// queueCommand answers `rig queue` from the RUNTIME's own rail — its room store is where a seat's work
// actually sits — and falls back to the OpenRig daemon's queue only when no runtime answers. It never
// renders an empty list to stand in for a measurement it did not make: with neither transport it names the
// runtime that is missing and exits non-zero, a daemon failure is reported rather than flattened to `[]`,
// and a queue WRITE is refused by name instead of being answered as though it had run. `show` and
// `transitions` exist because a note the queue accepts does not ride the row: it lands in the row's
// transition log, which is the only surface that carries it.
func (queueCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, queueUsage)
		fmt.Fprintln(stdout, "\nlist (the default) prints the active rows this seat's RUNTIME holds for its room")
		fmt.Fprintln(stdout, "and session, or for --destination's; when no runtime answers, a rig's OpenRig daemon")
		fmt.Fprintln(stdout, "is asked instead. show and transitions read one row and its log. With neither")
		fmt.Fprintln(stdout, "transport the verb reports that nothing was measured, never an empty queue.")
		fmt.Fprintln(stdout, "Writes (create/claim/update/block/resolve/handoff/inbox/outbox) are the OpenRig CLI's;")
		fmt.Fprintln(stdout, "this shim refuses them by name rather than reporting a write it did not make.")
		return clientcmd.ExitOK
	}
	// A subcommand this verb does not read is refused BY NAME before anything else runs, so the flag
	// spelling of a write (`queue update <id> --state done`) gets the same refusal the bare spelling gets,
	// instead of an unknown-flag message about the write's own argument.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && !queueReadSubcommands[args[0]] {
		fmt.Fprintf(stderr, "4genteam queue: %q is not a read this shim performs; it reads the queue (list, show, transitions). Queue writes (create/claim/update/block/resolve/handoff/inbox/outbox) are the OpenRig CLI's own. Nothing was read or written.\n", args[0])
		return clientcmd.ExitUsage
	}

	opts, rest, err := SplitFlags(args, []FlagSpec{{Name: "json"}, {Name: "destination", Value: true}})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam queue: %v; nothing was read\n", err)
		return clientcmd.ExitUsage
	}
	_, asJSON := opts["json"]

	sub := "list"
	if len(rest) > 0 {
		sub, rest = rest[0], rest[1:]
	}
	switch sub {
	case "list":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, queueUsage)
			return clientcmd.ExitUsage
		}
		return queueList(ctx, opts["destination"], asJSON, stdout, stderr)
	case "show", "transitions":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, queueUsage)
			return clientcmd.ExitUsage
		}
		return queueReadOne(ctx, sub, rest[0], asJSON, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "4genteam queue: %q is not a read this shim performs; it reads the queue (list, show, transitions). Queue writes (create/claim/update/block/resolve/handoff/inbox/outbox) are the OpenRig CLI's own. Nothing was read or written.\n", sub)
		return clientcmd.ExitUsage
	}
}

// queueReadSubcommands are the reads this verb performs. Every other first word is a write (or a typo),
// and both are refused by name: the measured failure this refusal exists for is `queue update <id>
// --note ...` reporting success while the note went nowhere the caller could read.
var queueReadSubcommands = map[string]bool{"list": true, "show": true, "transitions": true}

const queueUsage = `usage: 4genteam queue [list] [--json] [--destination <dest>]
       4genteam queue show <qitemId> [--json]
       4genteam queue transitions <qitemId> [--json]`

// queueList is the default read: THIS seat's active rows, rendered one line each. An empty document is only
// ever reported as a measurement of a named session against a named transport.
//
// THE TRANSPORT IS RESOLVED FIRST, and the runtime wins. A runtime that answers is the transport this seat
// actually has: its room store holds the rail, and reading it needs no OpenRig daemon at all. The daemon's
// own queue is read only when no runtime answers, and when neither transport is there the refusal names the
// RUNTIME — because with the runtime first, a dead $OPENRIG_URL is not what stopped this verb.
func queueList(ctx context.Context, destination string, asJSON bool, stdout, stderr io.Writer) int {
	switch ResolveTransport() {
	case TransportRuntime:
		return queueListRuntime(destination, asJSON, stdout, stderr)
	case TransportDaemon:
		return queueListDaemon(ctx, destination, asJSON, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "4genteam queue: %s; nothing was measured\n", FallbackMissing())
		return clientcmd.ExitUnavailable
	}
}

// queueListRuntime reads the room's active rows through the runtime socket. The ROOM comes from the seat's
// own environment (AGENTHUB_ROOM, else the rig half of OPENRIG_SESSION_NAME) and the destination from the
// caller — or from this seat's native session name, which is the one shape the runtime's rail is keyed on.
//
// A room this seat cannot name is still ASKED FOR: the runtime refuses it by name, and its refusal is the
// measurement's answer. Guessing an empty queue here would be the false reading this verb exists to avoid.
func queueListRuntime(destination string, asJSON bool, stdout, stderr io.Writer) int {
	room, seat, _, _ := seatEnvParts()
	if destination == "" {
		// The destination default is this seat's NATIVE session name — the runtime's rail is keyed on the
		// session names the client's own convention renders ("agenthub--<room>--<seat>", the same name
		// SendTextToSeat addresses) — and it stays empty when this shell names no seat, so the filter
		// narrows nothing rather than narrowing to a name nobody used.
		destination = SeatSessionName(room, seat)
	}
	filter := QueueListFilter{ActiveOnly: true}
	if destination != "" {
		filter.DestinationSession = &destination
	}
	// The frame is the one Client.QueueList writes (room + filter); it is written at the frame because
	// `--json` prints the runtime's OWN document, and the typed read drops the answer's bytes.
	resp, err := NewClient("").Call("QueueList", map[string]any{"room": room, "filter": filter})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam queue: the runtime at %s refused it: %v\n", DefaultSocketPath(), err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
	}
	// THE ONE DECODER, EVEN THOUGH THIS VERB PRINTS THE RUNTIME'S OWN DOCUMENT FOR --json. Decoding by hand here
	// re-introduced the defect decodeAnswer was fixed for, on the verb an operator actually runs: json.Marshal(nil) is
	// `null`, unmarshalling null into a nil slice SUCCEEDS, and a bare ok envelope therefore printed "Measured 0
	// pending items" at exit 0 - identical to a real empty queue, which is the same class of lie as printing blanks.
	// The typed read is taken FIRST, so a document nobody can read refuses at the room contract's exit 3 (the same
	// code the typed path carries, rather than this site's ExitRemote), and the runtime's bytes are only printed once
	// they have been read.
	var rows []QueueRow
	if err := decodeAnswer("QueueList", resp.Data, &rows); err != nil {
		fmt.Fprintf(stderr, "4genteam queue: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitRoomRefused)
	}
	if asJSON {
		_, _ = stdout.Write(runtimeJSONDocument(resp.Data))
		_, _ = stdout.Write([]byte("\n"))
		return clientcmd.ExitOK
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "Measured 0 pending items for %s on the runtime at %s.\n", orDash(destination), DefaultSocketPath())
		return clientcmd.ExitOK
	}
	for _, row := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", row.QitemID, orDash(row.State), orDash(row.SourceSession), row.Summary)
	}
	return clientcmd.ExitOK
}

// queueListDaemon is the fallback read: the OpenRig daemon's own queue for this seat's session.
func queueListDaemon(ctx context.Context, destination string, asJSON bool, stdout, stderr io.Writer) int {
	if destination == "" {
		destination = openRigSession()
	}
	query := url.Values{}
	query.Set("destinationSession", destination)
	query.Set("activeOnly", "1")
	query.Set("compact", "1")
	raw, err := openRigRequest(ctx, "GET", "/api/queue/list?"+query.Encode(), nil)
	if err != nil {
		return queueFallbackFailed(err, stderr)
	}
	if asJSON {
		printDaemonJSON(stdout, raw)
		return clientcmd.ExitOK
	}
	var rows []struct {
		QItemID       string `json:"qitemId"`
		State         string `json:"state"`
		SourceSession string `json:"sourceSession"`
		Summary       string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		fmt.Fprintf(stderr, "4genteam queue: could not read the daemon's queue document: %v\n", err)
		return clientcmd.ExitRemote
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "Measured 0 pending items for %s on the OpenRig daemon at %s.\n", orDash(destination), openRigDaemonURL())
		return clientcmd.ExitOK
	}
	for _, row := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", row.QItemID, orDash(row.State), orDash(row.SourceSession), row.Summary)
	}
	return clientcmd.ExitOK
}

// queueReadOne reads a single row (`show`) or its transition log (`transitions`). The log is where a note
// lives: `update --note` writes it there and the row document carries no note field at all.
//
// The transport resolves exactly as `list` resolves it, and the same refusal applies: with neither a runtime
// nor a configured daemon, the thing named as missing is the RUNTIME.
func queueReadOne(ctx context.Context, sub, qitemID string, asJSON bool, stdout, stderr io.Writer) int {
	switch ResolveTransport() {
	case TransportRuntime:
		return queueReadOneRuntime(sub, qitemID, asJSON, stdout, stderr)
	case TransportDaemon:
		return queueReadOneDaemon(ctx, sub, qitemID, asJSON, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "4genteam queue: %s; nothing was measured\n", FallbackMissing())
		return clientcmd.ExitUnavailable
	}
}

// queueReadOneRuntime reads one row of the room's rail from the runtime. Both subcommands are ONE call: the
// runtime answers `show` with the row's transition log inside it, which is where a note lands — the daemon
// needs a second route for that log, the runtime does not.
func queueReadOneRuntime(sub, qitemID string, asJSON bool, stdout, stderr io.Writer) int {
	room, _, _, _ := seatEnvParts()
	resp, err := NewClient("").Call("QueueShow", map[string]string{"room": room, "qitem_id": qitemID})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam queue: the runtime at %s refused it: %v\n", DefaultSocketPath(), err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
	}
	if asJSON {
		_, _ = stdout.Write(runtimeJSONDocument(resp.Data))
		_, _ = stdout.Write([]byte("\n"))
		return clientcmd.ExitOK
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam queue: could not read the runtime's row document: %v\n", err)
		return clientcmd.ExitRemote
	}
	var row QueueRow
	if err := json.Unmarshal(raw, &row); err != nil {
		fmt.Fprintf(stderr, "4genteam queue: could not read the runtime's row document: %v\n", err)
		return clientcmd.ExitRemote
	}
	if sub == "show" {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", orDash(row.QitemID), orDash(row.State), orDash(row.SourceSession), row.Summary)
		if blockedOn := readOptional(row.BlockedOn); blockedOn != "" {
			fmt.Fprintf(stdout, "blockedOn: %s\n", blockedOn)
		}
		if reason := readOptional(row.ClosureReason); reason != "" {
			fmt.Fprintf(stdout, "closed: %s -> %s\n", reason, orDash(readOptional(row.ClosureTarget)))
		}
		fmt.Fprintf(stdout, "tsUpdated: %s\n", orDash(row.TsUpdated))
		return clientcmd.ExitOK
	}
	for _, transition := range row.Transitions {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", orDash(transition.Ts), orDash(transition.State), orDash(transition.ActorSession), strings.ReplaceAll(transition.TransitionNote, "\n", " "))
	}
	return clientcmd.ExitOK
}

// readOptional is an optional string field as the empty string when it is absent: the row's Options are
// pointers precisely so "unset" and "set to empty" stay different questions, and a reader that flattened
// both to one value would answer them the same way.
func readOptional(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// queueReadOneDaemon is the fallback read: the OpenRig daemon's row, or its transition route for the log.
func queueReadOneDaemon(ctx context.Context, sub, qitemID string, asJSON bool, stdout, stderr io.Writer) int {
	path := "/api/queue/" + url.PathEscape(qitemID)
	if sub == "transitions" {
		path += "/transitions"
	}
	raw, err := openRigRequest(ctx, "GET", path, nil)
	if err != nil {
		return queueFallbackFailed(err, stderr)
	}
	if asJSON {
		printDaemonJSON(stdout, raw)
		return clientcmd.ExitOK
	}
	if sub == "show" {
		var row struct {
			QItemID       string `json:"qitemId"`
			State         string `json:"state"`
			SourceSession string `json:"sourceSession"`
			Summary       string `json:"summary"`
			BlockedOn     string `json:"blockedOn"`
			ClosureReason string `json:"closureReason"`
			ClosureTarget string `json:"closureTarget"`
			TSUpdated     string `json:"tsUpdated"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			fmt.Fprintf(stderr, "4genteam queue: could not read the daemon's row document: %v\n", err)
			return clientcmd.ExitRemote
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", orDash(row.QItemID), orDash(row.State), orDash(row.SourceSession), row.Summary)
		if row.BlockedOn != "" {
			fmt.Fprintf(stdout, "blockedOn: %s\n", row.BlockedOn)
		}
		if row.ClosureReason != "" {
			fmt.Fprintf(stdout, "closed: %s -> %s\n", row.ClosureReason, orDash(row.ClosureTarget))
		}
		fmt.Fprintf(stdout, "tsUpdated: %s\n", orDash(row.TSUpdated))
		return clientcmd.ExitOK
	}
	var transitions []struct {
		TS             string `json:"ts"`
		State          string `json:"state"`
		ActorSession   string `json:"actorSession"`
		TransitionNote string `json:"transitionNote"`
	}
	if err := json.Unmarshal(raw, &transitions); err != nil {
		fmt.Fprintf(stderr, "4genteam queue: could not read the daemon's transition log: %v\n", err)
		return clientcmd.ExitRemote
	}
	for _, transition := range transitions {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", orDash(transition.TS), orDash(transition.State), orDash(transition.ActorSession), strings.ReplaceAll(transition.TransitionNote, "\n", " "))
	}
	return clientcmd.ExitOK
}

// queueFallbackFailed reports the OpenRig fallback's failure. Two failures, two answers, because they are
// not the same event:
//
//   - a daemon that ANSWERED (a 4xx/5xx refusal) has answered — its words are reported exactly as they were
//     before, with its status and its own code;
//   - a daemon that could NOT be reached is not an answer. With the runtime first, the transport that is
//     missing is the runtime, so the refusal names IT (and says the fallback was tried), instead of blaming
//     a $OPENRIG_URL the shim was never going to reach.
func queueFallbackFailed(err error, stderr io.Writer) int {
	if clientcmd.CodeOf(err, clientcmd.ExitRemote) == clientcmd.ExitUnavailable {
		fmt.Fprintf(stderr, "4genteam queue: %s; nothing was measured\n", FallbackMissing())
		return clientcmd.ExitUnavailable
	}
	fmt.Fprintf(stderr, "4genteam queue: %v\n", err)
	return clientcmd.CodeOf(err, clientcmd.ExitRemote)
}

// printDaemonJSON writes the daemon's document as it arrived, indented when it is JSON. --json prints the
// document rather than a re-encode of it, so a field this build has not heard of still reaches the caller.
func printDaemonJSON(stdout io.Writer, raw []byte) {
	out := raw
	var indented bytes.Buffer
	if json.Indent(&indented, raw, "", "  ") == nil {
		out = indented.Bytes()
	}
	_, _ = stdout.Write(out)
	_, _ = stdout.Write([]byte("\n"))
}

type captureCommand struct{}

func (captureCommand) Name() string    { return "capture" }
func (captureCommand) Summary() string { return "capture a session's pane through the OpenRig daemon" }
func (captureCommand) NeedsRig() bool  { return false }

const captureUsage = "usage: 4genteam capture <session> [--lines <n>] [--json]"

func (captureCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, captureUsage)
		fmt.Fprintln(stdout, "\nCaptures the named session's pane from the OpenRig daemon and prints it verbatim.")
		fmt.Fprintln(stdout, "--lines bounds how many lines are captured; --json prints the daemon's own document.")
		return clientcmd.ExitOK
	}
	opts, rest, err := SplitFlags(args, []FlagSpec{{Name: "json"}, {Name: "lines", Value: true}})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam capture: %v; nothing was captured\n", err)
		return clientcmd.ExitUsage
	}
	if len(rest) != 1 {
		fmt.Fprintln(stderr, captureUsage)
		return clientcmd.ExitUsage
	}
	name := rest[0]
	_, asJSON := opts["json"]
	lines := -1
	if value, given := opts["lines"]; given {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			fmt.Fprintf(stderr, "4genteam capture: --lines wants a positive number of lines, got %q; nothing was captured\n", value)
			return clientcmd.ExitUsage
		}
		lines = parsed
	}

	if !openRigDaemonConfigured() {
		fmt.Fprintf(stderr, "4genteam capture: no OpenRig daemon is configured in this environment (set %s or %s); nothing was captured\n", openRigURLEnv, openRigPortEnv)
		return clientcmd.ExitUnavailable
	}

	payload := map[string]any{"session": name}
	if lines > 0 {
		payload["lines"] = lines
	}
	raw, err := openRigRequest(ctx, "POST", "/api/transport/capture", payload)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam capture: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
	}

	var doc struct {
		OK      *bool  `json:"ok"`
		Content string `json:"content"`
		Reason  string `json:"reason"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(stderr, "4genteam capture: could not read the daemon's capture document: %v\n", err)
		return clientcmd.ExitRemote
	}
	// A refusal is reported by the transport's status (any non-2xx is already an error above) or by an
	// explicit `ok:false` in a 2xx body. An absent `ok` is NOT a refusal: treating it as one reported a
	// successful capture as a failure — the same class of untrue answer this row exists to remove.
	if doc.OK != nil && !*doc.OK {
		reason := strings.TrimSpace(doc.Reason)
		if reason == "" {
			reason = strings.TrimSpace(doc.Error)
		}
		if reason == "" {
			reason = daemonDetail(raw)
		}
		fmt.Fprintf(stderr, "4genteam capture: %s\n", reason)
		return clientcmd.ExitRemote
	}

	if asJSON {
		printDaemonJSON(stdout, raw)
		return clientcmd.ExitOK
	}
	// The content is a pane: it goes out verbatim, trailing whitespace and all, with no heading added.
	_, _ = io.WriteString(stdout, doc.Content)
	return clientcmd.ExitOK
}
