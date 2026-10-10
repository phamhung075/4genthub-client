// Package clientsync is the sync half of 4genteam: pull, rig, bundle, switch, status and watch,
// ported test-for-test from the Python test now at agenthub_client/tests/test_seat_sync.py (it lived at
// scripts/tests/test_openrig_seat_sync.py until 2026-10-09, when the scripts left the tree, and at
// agenthub_main/src/tests/scripts/ until 2026-10-08).
// Those Python tests are the parity spec, so a verb here is done when the Go tests assert WHAT THEY
// ASSERT, not when the Go tests pass.
//
// The pinned-snapshot rules are the part that must not soften: a seat's snapshot is pinned by hash and
// a pull never falls back to a different one.
package clientsync

import (
	"context"
	"fmt"
	"io"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// Commands is what the dispatcher registers from this package. One entry per package keeps main.go
// thin and keeps this package's surface a single thing to review.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{syncCommand{}}
}

// syncVerbs are the verbs this package answers under `sync`. Each is ported test-for-test; until one
// is, it REFUSES BY NAME rather than answering something plausible, which is the same rule the
// platform matrix follows.
var syncVerbs = []string{"status", "pull", "messages", "rig", "bundle", "switch", "watch", "connector"}

type syncCommand struct{}

func (syncCommand) Name() string    { return "sync" }
func (syncCommand) Summary() string { return "pull and inspect resolved seats (" + verbList() + ")" }
func (syncCommand) NeedsRig() bool  { return false }

func (syncCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "4genteam sync: pick a verb: %s\n", verbList())
		return clientcmd.ExitUsage
	}
	// HELP IS HELP FOR EVERY VERB, this group included: answering `--help` with an unknown-verb error
	// and a non-zero exit was the fault this closes.
	if args[0] == "-h" || args[0] == "--help" {
		printUsage(stdout)
		return clientcmd.ExitOK
	}
	// status, pull and connector are ported: status compares each seat's local pin with the cloud's
	// hash, pull writes the snapshot and moves the pin, and connector reports one local session.
	// messages is the other direction of the same path pull uses: it takes the text a window addressed
	// to a seat and types it into that seat's session here, where the terminal actually is.
	switch args[0] {
	case "status":
		return RunStatusVerb(ctx, args[1:], stdout, stderr)
	case "pull":
		return RunPullVerb(ctx, args[1:], stdout, stderr)
	case "messages":
		return RunMessagesVerb(ctx, args[1:], stdout, stderr)
	case "connector":
		return RunConnectorVerb(ctx, args[1:], stdout, stderr)
	}
	want := args[0]
	for _, verb := range syncVerbs {
		if verb != want {
			continue
		}
		fmt.Fprintf(stderr,
			"4genteam sync %s: not ported into the Go client yet, so the client package's seat_sync "+
				"module (agenthub_client/src/agenthub_client/seat_sync.py) is still the authority for it. "+
				"This build refuses rather than doing something weaker.\n", want)
		return clientcmd.ExitUnavailable
	}
	fmt.Fprintf(stderr, "4genteam sync: unknown verb %q; the verbs are %s\n", want, verbList())
	return clientcmd.ExitUsage
}

func verbList() string {
	out := ""
	for i, verb := range syncVerbs {
		if i > 0 {
			out += ", "
		}
		out += verb
	}
	return out
}

// printUsage is what `4genteam sync --help` prints. It lists the verbs the group declares, so a verb
// added to syncVerbs appears here without a second edit.
func printUsage(w io.Writer) {
	_, _ = io.WriteString(w, "usage: 4genteam sync <verb> [args]\n\nverbs:\n")
	for _, verb := range syncVerbs {
		_, _ = io.WriteString(w, "  "+verb+"\n")
	}
	_, _ = io.WriteString(w, "\nstatus, pull and messages read the cloud and the local pins; rig materializes a room's seats;\n"+
		"bundle, switch, respawn, offline-install and install-checker move or manage what was pulled.\n"+
		"A verb that is not ported into this build refuses by name rather than doing something weaker.\n")
}
