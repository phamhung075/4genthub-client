package clientruntime

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// Commands returns the clientruntime commands to register in 4genteam.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{
		whoamiCommand{},
		captureCommand{},
	}
}

type whoamiCommand struct{}

func (whoamiCommand) Name() string    { return "whoami" }
func (whoamiCommand) Summary() string { return "report seat identity" }

func (whoamiCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam whoami [--json] [--session <name>]")
		fmt.Fprintln(stdout, "\nReports the seat identity. The runtime's own socket is asked first; with no runtime, the")
		fmt.Fprintln(stdout, "seat's own environment (AGENTHUB_ROOM and AGENTHUB_SEAT) is the identity.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("whoami", flag.ContinueOnError)
	jsonFlag := flags.Bool("json", false, "print as JSON")
	sessionFlag := flags.String("session", "", "specific session name")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}

	// THE CHAIN IS ONE FUNCTION (ResolveIdentity), so this verb PRINTS an answer and the seat guard
	// (cmd/seatcheck) reasons about the same one.
	resolved, err := ResolveIdentity(*sessionFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam whoami: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
	}
	return printWhoami(stdout, stderr, &resolved, *jsonFlag)
}

// seatEnvIdentity builds the identity document from the seat's own environment, and reports whether there
// was one to build. It reads the SAME pair the runtime's own `environment` fallback reads (AGENTHUB_ROOM /
// AGENTHUB_SEAT / AGENTHUB_ROLE / AGENTHUB_RUNTIME).
//
// resolvedBy stays "environment": that field is the reader's only way to tell an identity the runtime
// resolved from one this shell asserted, and dressing a guess up as a runtime answer is the class of
// untruth this verb exists to avoid.
func seatEnvIdentity() (WhoamiResponse, bool) {
	room, seat, session, ok := seatEnvParts()
	if !ok {
		return WhoamiResponse{}, false
	}
	runtime := strings.TrimSpace(os.Getenv(agentHubRuntimeEnv))
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

type captureCommand struct{}

func (captureCommand) Name() string { return "capture" }
func (captureCommand) Summary() string {
	return "capture a session's pane through the runtime's own terminal manager"
}

const captureUsage = "usage: 4genteam capture <session> [--lines <n>] [--json]"

func (captureCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, captureUsage)
		fmt.Fprintln(stdout, "\nCaptures the named session's pane through the runtime's own terminal manager and prints it verbatim.")
		fmt.Fprintln(stdout, "--lines bounds how many lines are captured; --json prints this client's capture answer.")
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

	// THE PANE IS READ THROUGH THE RUNTIME'S OWN TERMINAL MANAGER - the process that owns the tmux server -
	// over this client's own transport, which is Client.Capture. This verb used to dial the rig daemon:
	// it gated on the daemon URL and port variables, POSTed /api/transport/capture and parsed the document that came
	// back. That is the rig orchestrator path this cutover removes, and capture is meant to work with `rig` off PATH
	// and no the rig orchestrator path at all. A runtime that is not there, or one that refuses, is a refusal in words
	// (ExitUnavailable) and never an empty pane; nothing falls back to the daemon.
	answer, err := NewClient("").Capture(name, lines)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam capture: the runtime at %s could not capture %s: %v\n", DefaultSocketPath(), name, err)
		return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
	}

	if asJSON {
		// The document is this client's own capture answer now - the seat and its content - because there is
		// no daemon body to pass through any more. --json still means "the answer, machine-readable".
		_, _ = stdout.Write(runtimeJSONDocument(answer))
		_, _ = stdout.Write([]byte("\n"))
		return clientcmd.ExitOK
	}
	// The content is a pane: it goes out verbatim, trailing whitespace and all, with no heading added.
	_, _ = io.WriteString(stdout, answer.Content)
	return clientcmd.ExitOK
}
