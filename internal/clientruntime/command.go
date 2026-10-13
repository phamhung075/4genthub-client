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
		fmt.Fprintln(stdout, "usage: 4genteam whoami [--json] [--session <name>]")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("whoami", flag.ContinueOnError)
	jsonFlag := flags.Bool("json", false, "print as JSON")
	sessionFlag := flags.String("session", "", "specific session name")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}

	session := *sessionFlag
	if session == "" {
		session = os.Getenv("AGENTHUB_SESSION_NAME")
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

type shimCommand struct{}

func (shimCommand) Name() string    { return "rig-shim" }
func (shimCommand) Summary() string { return "install ~/.local/bin/rig compatibility symlink to 4genteam" }
func (shimCommand) NeedsRig() bool  { return false }

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
