package clientruntime

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// Commands returns the clientruntime commands to register in 4genteam.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{
		whoamiCommand{},
		queueCommand{},
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

func (queueCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam queue [list] [--json] [--destination <dest>]")
		return clientcmd.ExitOK
	}
	isJSON := false
	for _, a := range args {
		if a == "--json" {
			isJSON = true
			break
		}
	}
	if isJSON {
		_, _ = stdout.Write([]byte("[]\n"))
		return clientcmd.ExitOK
	}
	fmt.Fprintln(stdout, "No pending items in local queue. (Tasks are recorded via agenthub_http MCP tools)")
	return clientcmd.ExitOK
}
