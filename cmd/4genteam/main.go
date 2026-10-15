// Command 4genteam is the single local client: one Go binary that replaces the Python bridge,
// the sync scripts and the shell feedback helper, and absorbs seatcheck under its own name.
//
// THIS FILE IS A THIN DISPATCHER, and it stays thin: it reads argv[0] and the first argument, and calls Run.
// Subcommand packages register themselves through internal/clientcmd; nothing here knows what a sync verb does.
//
// TWO RULES SHAPE THE WHOLE CLIENT (ONE-CLIENT.md, "Cross-platform rules"):
//
//   - NO SHELL STRINGS. Every external command is exec.Command with an argument list, because the
//     quoting rules on Windows are not the Unix ones.
//   - NO SILENT DOWNGRADE. A command that needs something this machine does not have exits non-zero
//     with ONE message naming what is missing, by naming the verb that refuses.
package main

import (
	"context"
	"io"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientactivity"
	"github.com/phamhung075/4genthub-client/internal/clientbridge"
	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientcomplete"
	"github.com/phamhung075/4genthub-client/internal/clientdecide"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientevidence"
	"github.com/phamhung075/4genthub-client/internal/clientfeedback"
	"github.com/phamhung075/4genthub-client/internal/clientheal"
	"github.com/phamhung075/4genthub-client/internal/clientlifecycle"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientoutbox"
	"github.com/phamhung075/4genthub-client/internal/clientpolicy"
	"github.com/phamhung075/4genthub-client/internal/clientps"
	"github.com/phamhung075/4genthub-client/internal/clientrestore"
	"github.com/phamhung075/4genthub-client/internal/clientroom"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientseat"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
	"github.com/phamhung075/4genthub-client/internal/clientwatch"
)

// pending registers a verb whose implementation package does not exist yet. It REFUSES rather than
// doing something weaker, so a verb that has not moved can never look like one that has.
func pending(name, summary string) clientcmd.Command {
	return pendingCommand{name: name, summary: summary}
}

type pendingCommand struct {
	name    string
	summary string
}

func (c pendingCommand) Name() string    { return c.name }
func (c pendingCommand) Summary() string { return c.summary }

// Run refuses, and the owner sentence is per-command because getting it WRONG sends a reader to the
// wrong tool. The client does not implement `seatcheck`, but the checker itself is already Go, so
// naming the Python client as the authority for it would be false as well as unhelpful. The branch
// this replaces keyed on a `bridge` command that is no longer registered at all, so it could not run.
func (c pendingCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
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
		// The checker's own `send` verb is DELETED (`cmd/seatcheck`), and this help must not send a
		// reader to it: the audited send path is the client's own `4genteam send`, and the checker is
		// left with `audit-scan`, which only reads.
		return "usage: 4genteam seatcheck <audit-scan> [args]\n\n" +
			"The checker is its own binary (cmd/seatcheck). This name refuses instead of forwarding, so " +
			"nothing is half-implemented here; run `seatcheck audit-scan --file <path>` to scan observed " +
			"command lines, or `4genteam send --to <seat> --intent <intent> -- <text>` to send a message.\n"
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
	// The room-scoped sibling of the lifecycle verbs: `room status|stop|reseat|compact` over the same IPC,
	// with the pair (state dir + socket) printed on every run. `up` is the room's own start path and stays
	// where it is, in clientlifecycle.
	registry = append(registry, clientroom.Commands()...)
	registry = append(registry, clientheal.Commands()...)
	registry = append(registry, clientoffpeak.Commands()...)
	registry = append(registry, clientseat.Commands()...)
	registry = append(registry, clientpolicy.Commands()...)
	registry = append(registry, clientteam.Commands()...)
	registry = append(registry, clientwatch.Commands()...)
	registry = append(registry, clientfeedback.Commands()...)
	registry = append(registry, clientactivity.Commands()...)
	registry = append(registry, clientcomplete.Commands()...)
	registry = append(registry, clientdecide.Commands()...)
	registry = append(registry, clientevidence.Commands()...)
	registry = append(registry, clientrestore.Commands()...)
	registry = append(registry, clientruntime.Commands()...)
	registry = append(registry, clientoutbox.Commands()...)
	registry = append(registry, clientps.Commands()...)
	return append(registry,
		pending("seatcheck", "the seat-side checker (also reachable as `seatcheck` via argv[0])"),
	)
}

func main() {
	// The client's ONE env file, loaded once, with a variable already in the environment winning.
	if path, err := clientenv.DefaultPath(); err == nil {
		_ = clientenv.Load(path)
	}
	// NO VERB STARTS A SAFEGUARD AS A SIDE EFFECT. `EnsureBridge` used to start the status bridge from any
	// cloud-reading verb; 7.1 made agent the SINGLE RESTART AUTHORITY, so the bridge is agent's child and a
	// second start path would be a second mechanism - and a bridge agent does not supervise.
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// `status` is the safeguard rows; the node views of a room are `ps`.
	if len(args) > 0 && args[0] == "status" {
		for _, arg := range args[1:] {
			if arg == "--nodes" || arg == "--compact" || arg == "--grid" {
				args[0] = "ps"
			}
		}
	}

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
		return cmd.Run(context.Background(), args[1:], stdout, stderr)
	}
	_, _ = io.WriteString(stderr, "4genteam: unknown command "+args[0]+"\n")
	usage(stderr)
	return clientcmd.ExitUsage
}

func usage(w io.Writer) {
	_, _ = io.WriteString(w, "4genteam - the local 4genthub client\n\ncommands:\n")
	for _, cmd := range commands() {
		_, _ = io.WriteString(w, "  "+cmd.Name()+"\t"+cmd.Summary()+"\n")
	}
	_, _ = io.WriteString(w, "  version\tthe client's own version\n")
	_, _ = io.WriteString(w, "\nA command that needs rig and cannot reach it exits non-zero with the reason; nothing downgrades silently.\n")
}
