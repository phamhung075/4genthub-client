// Command 4genteam is the single local client: one Go binary that replaces the Python bridge,
// the sync scripts and the shell feedback helper, and absorbs seatcheck under its own name.
//
// THIS FILE IS A THIN DISPATCHER, and it stays thin: it reads argv[0] and the first argument, resolves
// the rig tool ONCE for the commands that need it, and calls Run. Subcommand packages register
// themselves through internal/clientcmd; nothing here knows what a sync verb does.
//
// THREE RULES SHAPE THE WHOLE CLIENT (ONE-CLIENT.md, "Cross-platform rules"), and two of them are
// implemented in internal/clientcmd so every command inherits them:
//
//   - NO TMUX. Seat lifecycle goes through rig - `rig seat stop <seat>` (audited; kills only that
//     seat's tmux session), `rig seat launch <seat>`, `rig seat clean <seat>` - so the same code runs
//     wherever rig does. The Python client killed tmux sessions directly, which is what this client
//     must stop doing.
//   - NO SHELL STRINGS. Every external command is exec.Command with an argument list, in
//     clientcmd.Rig.Run - one home for it - because the quoting rules on Windows are not the Unix ones.
//   - NO SILENT DOWNGRADE. A command that needs something this machine does not have exits non-zero
//     with ONE message naming what is missing: clientcmd.RequireRig, or a verb refusing by name.
package main

import (
	"context"
	"io"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientbridge"
	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientevidence"
	"github.com/phamhung075/4genthub-client/internal/clientfeedback"
	"github.com/phamhung075/4genthub-client/internal/clientlifecycle"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientpolicy"
	"github.com/phamhung075/4genthub-client/internal/clientrestore"
	"github.com/phamhung075/4genthub-client/internal/clientseat"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
	"github.com/phamhung075/4genthub-client/internal/clientwatch"
)

// pending registers a verb whose implementation package does not exist yet. It REFUSES rather than
// doing something weaker, so a verb that has not moved can never look like one that has.
func pending(name, summary string, needsRig bool) clientcmd.Command {
	return pendingCommand{name: name, summary: summary, needsRig: needsRig}
}

type pendingCommand struct {
	name     string
	summary  string
	needsRig bool
}

func (c pendingCommand) Name() string    { return c.name }
func (c pendingCommand) Summary() string { return c.summary }
func (c pendingCommand) NeedsRig() bool  { return c.needsRig }

// Run refuses, and the owner sentence is per-command because getting it WRONG sends a reader to the
// wrong tool. The client does not implement `seatcheck`, but the checker itself is already Go, so
// naming the Python client as the authority for it would be false as well as unhelpful. The branch
// this replaces keyed on a `bridge` command that is no longer registered at all, so it could not run.
func (c pendingCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	// HELP IS HELP FOR EVERY VERB, the two that refuse included: without this, `--help` would be the
	// one argument that errors on a verb whose whole job is to say where the real one lives.
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, pendingUsage(c.name))
		return clientcmd.ExitOK
	}
	owner := "the Python client is still the authority for it"
	if c.name == "seatcheck" {
		owner = "run the seatcheck binary instead (it is already Go: cmd/seatcheck, built and linked as " +
			"`seatcheck` by `4genteam sync install-checker`)"
	}
	return refuse(stderr, c.name, owner)
}

// pendingUsage is the help a verb this build does not implement still owes: help is help for every
// verb, so a name that refuses must still say what it is and where the real implementation lives.
func pendingUsage(name string) string {
	if name == "seatcheck" {
		return "usage: 4genteam seatcheck <send|audit-scan> [args]\n\n" +
			"The checker is its own binary (cmd/seatcheck). This name refuses instead of forwarding, so " +
			"nothing is half-implemented here; run `seatcheck send --to <seat> --intent <intent> -- " +
			"<text>` or `seatcheck audit-scan --file <path>`.\n"
	}
	return "usage: 4genteam " + name + " [args]\n"
}

func refuse(stderr io.Writer, name, owner string) int {
	_, _ = io.WriteString(stderr,
		"4genteam "+name+": not ported into the Go client yet, so "+owner+". "+
			"This build refuses rather than doing something weaker.\n")
	return clientcmd.ExitUnavailable
}

// commands is the registry. Every entry comes from a package or is a named pending one, and a second
// package registering is ONE append here rather than a refactor - which is what the shared contract in
// internal/clientcmd buys.
func commands() []clientcmd.Command {
	registry := append(clientsync.Commands(), clientbridge.Commands()...)
	registry = append(registry, clientlifecycle.Commands()...)
	registry = append(registry, clientoffpeak.Commands()...)
	registry = append(registry, clientseat.Commands()...)
	registry = append(registry, clientpolicy.Commands()...)
	registry = append(registry, clientteam.Commands()...)
	registry = append(registry, clientwatch.Commands()...)
	registry = append(registry, clientfeedback.Commands()...)
	registry = append(registry, clientevidence.Commands()...)
	registry = append(registry, clientrestore.Commands()...)
	return append(registry,
		pending("seatcheck", "the seat-side checker (also reachable as `seatcheck` via argv[0])", false),
	)
}

func main() {
	// The client's ONE env file, loaded once, with a variable already in the environment winning.
	if path, err := clientenv.DefaultPath(); err == nil {
		_ = clientenv.Load(path)
	}
	clientlifecycle.EnsureBridge(os.Args[1:], os.Stderr)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		// No argument means `up`, as the Python client did.
		args = []string{"up"}
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return clientcmd.ExitOK
	case "version":
		_, _ = io.WriteString(stdout, "4genteam "+clientVersion+"\n")
		return clientcmd.ExitOK
	}
	for _, cmd := range commands() {
		if cmd.Name() != args[0] {
			continue
		}
		// HELP IS ANSWERED BEFORE THE PLATFORM MATRIX. A verb's own usage must be readable on a machine
		// that has never had OpenRig: six verbs used to refuse here with the matrix's reason instead,
		// because this loop resolved rig before it called the verb, so the verb's own help path was never
		// reached. The predicate is the one every verb answers help with (clientcmd.HelpRequested), so a
		// verb reached here for help never touches a rig, and a verb that does NOT ask for help still
		// goes through the matrix below - the refusal is not softened for anything else.
		if clientcmd.HelpRequested(args[1:]) {
			return cmd.Run(context.Background(), nil, args[1:], stdout, stderr)
		}
		var rig *clientcmd.Rig
		if cmd.NeedsRig() {
			tool, err := clientcmd.RequireRig()
			if err != nil {
				// ONE message, non-zero exit, no partial work.
				_, _ = io.WriteString(stderr, "4genteam "+cmd.Name()+": "+err.Error()+"\n")
				return clientcmd.ExitUnavailable
			}
			rig = tool
		}
		return cmd.Run(context.Background(), rig, args[1:], stdout, stderr)
	}
	_, _ = io.WriteString(stderr, "4genteam: unknown command "+args[0]+"\n")
	usage(stderr)
	return clientcmd.ExitUsage
}

func usage(w io.Writer) {
	_, _ = io.WriteString(w, "4genteam - the local OpenRig client\n\ncommands:\n")
	for _, cmd := range commands() {
		_, _ = io.WriteString(w, "  "+cmd.Name()+"\t"+cmd.Summary()+"\n")
	}
	_, _ = io.WriteString(w, "  version\tthe client's own version\n")
	_, _ = io.WriteString(w, "\nA command that needs rig and cannot reach it exits non-zero with the reason; nothing downgrades silently.\n")
}
