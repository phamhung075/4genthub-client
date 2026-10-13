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
		fmt.Fprintln(stdout, "\nReports the seat identity. On a rig the OpenRig daemon hosts (a shell carrying")
		fmt.Fprintln(stdout, "OPENRIG_SESSION_NAME) it asks that daemon and prints its document; otherwise it asks the")
		fmt.Fprintln(stdout, "native runtime's own rooms.")
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

	session := *sessionFlag
	if session == "" {
		session = os.Getenv("AGENTHUB_SESSION_NAME")
	}

	// A rig the OpenRig daemon hosts has no native room for this seat, so its daemon is asked FIRST and
	// its document is what this verb prints. The native runtime stays the fallback for a shell that
	// names no daemon, which leaves a native rig's behaviour exactly as it was.
	openRigSeat := *sessionFlag
	if openRigSeat == "" {
		openRigSeat = openRigSession()
	}
	if openRigSeat != "" && openRigDaemonConfigured() {
		// Compact by default, exactly as the CLI asks the daemon (`if (!full) params.set("compact", "1")`,
		// whoami.ts) and then projects the answer to the identity-recovery allowlist below. --full opts
		// out of both, so the daemon also skips the contextUsage/runtimeContext compute.
		raw, err := openRigWhoami(ctx, openRigSeat, !full)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam whoami: %v\n", err)
			// CodeOf keeps the raiser's own code: a 404 from the daemon is ExitRemote (1, the CLI's
			// answer for an unknown session), and "no daemon is configured" or "not answering" stays the
			// platform-matrix refusal (3) that no reachable daemon can turn into an answer.
			return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
		}
		if *jsonFlag {
			// The document is printed as it arrived (the CLI prints the payload, not a pretty-print of
			// it), and the compact projection is the CLI's allowlist — see projectCompactWhoami.
			out := raw
			if !full {
				out = projectCompactWhoami(raw)
			}
			_, _ = stdout.Write(out)
			_, _ = stdout.Write([]byte("\n"))
			return clientcmd.ExitOK
		}
		return printOpenRigIdentity(stdout, raw)
	}

	client := NewClient("")
	// If daemon is not running, ensure it starts
	if err := client.EnsureDaemon(ctx); err != nil {
		// Fallback: build response from local env if present
		room := os.Getenv("AGENTHUB_ROOM")
		seat := os.Getenv("AGENTHUB_SEAT")
		role := os.Getenv("AGENTHUB_ROLE")
		runtime := os.Getenv("AGENTHUB_RUNTIME")
		if room != "" && seat != "" {
			resp := WhoamiResponse{
				ResolvedBy: "environment",
				Identity: WhoamiIdentity{
					RigName:     room,
					MemberId:    seat,
					Runtime:     runtime,
					SessionName: fmt.Sprintf("agenthub--%s--%s", room, seat),
					LogicalId:   seat,
					Role:        role,
				},
				Peers: []WhoamiPeer{},
				Edges: WhoamiEdges{
					Outgoing: []WhoamiEdgeTarget{},
					Incoming: []WhoamiEdgeTarget{},
				},
				Transcript: WhoamiTranscript{
					Enabled: true,
				},
			}
			if *jsonFlag {
				data, _ := json.MarshalIndent(resp, "", "  ")
				_, _ = stdout.Write(data)
				_, _ = stdout.Write([]byte("\n"))
			} else {
				fmt.Fprintf(stdout, "Rig:  %s\nSeat: %s\nRole: %s\n", room, seat, role)
			}
			return clientcmd.ExitOK
		}
		fmt.Fprintf(stderr, "4genteam whoami: %v\n", err)
		return clientcmd.ExitUnavailable
	}

	resp, err := client.Whoami(session)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam whoami: %v\n", err)
		return clientcmd.ExitUnavailable
	}

	if *jsonFlag {
		data, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam whoami: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		_, _ = stdout.Write(data)
		_, _ = stdout.Write([]byte("\n"))
	} else {
		fmt.Fprintf(stdout, "Rig:     %s\n", resp.Identity.RigName)
		fmt.Fprintf(stdout, "Seat:    %s\n", resp.Identity.MemberId)
		fmt.Fprintf(stdout, "Role:    %s\n", resp.Identity.Role)
		fmt.Fprintf(stdout, "Runtime: %s\n", resp.Identity.Runtime)
		fmt.Fprintf(stdout, "Session: %s\n", resp.Identity.SessionName)
	}

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

// queueCommand answers `rig queue` from the OpenRig daemon's OWN queue, so a seat on a daemon-hosted
// rig learns what it actually owes. It never renders an empty list to stand in for a measurement it did
// not make: with no daemon configured it says so and exits non-zero, a daemon failure is reported rather
// than flattened to `[]`, and a queue WRITE is refused by name instead of being answered as though it
// had run. `show` and `transitions` exist because a note the queue accepts does not ride the row: it
// lands in the row's transition log, which is the only surface that carries it.
func (queueCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, queueUsage)
		fmt.Fprintln(stdout, "\nlist (the default) prints the active rows the OpenRig daemon holds for this seat's")
		fmt.Fprintln(stdout, "session, or for --destination's. show and transitions read one row and its log. With")
		fmt.Fprintln(stdout, "no daemon configured the verb reports that nothing was measured, never an empty queue.")
		fmt.Fprintln(stdout, "Writes (create/claim/update/block/resolve/handoff/inbox/outbox) are the OpenRig CLI's;")
		fmt.Fprintln(stdout, "this shim refuses them by name rather than reporting a write it did not make.")
		return clientcmd.ExitOK
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
		// The measured failure this refusal exists for: `queue update <id> --note ...` printing a success
		// while the note went nowhere the caller could read. A verb that cannot do the thing says so.
		fmt.Fprintf(stderr, "4genteam queue: %q is not a read this shim performs; it reads the queue (list, show, transitions). Queue writes (create/claim/update/block/resolve/handoff/inbox/outbox) are the OpenRig CLI's own. Nothing was read or written.\n", sub)
		return clientcmd.ExitUsage
	}
}

const queueUsage = `usage: 4genteam queue [list] [--json] [--destination <dest>]
       4genteam queue show <qitemId> [--json]
       4genteam queue transitions <qitemId> [--json]`

// queueList is the default read: THIS seat's active rows on the daemon, rendered one line each. An empty
// document is only ever reported as a measurement of a named session against a named daemon.
func queueList(ctx context.Context, destination string, asJSON bool, stdout, stderr io.Writer) int {
	if destination == "" {
		destination = openRigSession()
	}
	if !openRigDaemonConfigured() {
		fmt.Fprintf(stderr, "4genteam queue: no OpenRig daemon is configured in this environment (set %s or %s); nothing was measured\n", openRigURLEnv, openRigPortEnv)
		return clientcmd.ExitUnavailable
	}
	query := url.Values{}
	query.Set("destinationSession", destination)
	query.Set("activeOnly", "1")
	query.Set("compact", "1")
	raw, err := openRigRequest(ctx, "GET", "/api/queue/list?"+query.Encode(), nil)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam queue: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
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
func queueReadOne(ctx context.Context, sub, qitemID string, asJSON bool, stdout, stderr io.Writer) int {
	if !openRigDaemonConfigured() {
		fmt.Fprintf(stderr, "4genteam queue: no OpenRig daemon is configured in this environment (set %s or %s); nothing was measured\n", openRigURLEnv, openRigPortEnv)
		return clientcmd.ExitUnavailable
	}
	path := "/api/queue/" + url.PathEscape(qitemID)
	if sub == "transitions" {
		path += "/transitions"
	}
	raw, err := openRigRequest(ctx, "GET", path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam queue: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
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
		OK      bool   `json:"ok"`
		Content string `json:"content"`
		Reason  string `json:"reason"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(stderr, "4genteam capture: could not read the daemon's capture document: %v\n", err)
		return clientcmd.ExitRemote
	}
	if !doc.OK {
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
