// Package clientpolicy is the `policy` verb group: the permission policy an omp seat runs under.
//
// THE POLICY HAS ONE SOURCE: the hub's seat render. The hub folds the seat's policy modules into
// `runtime/omp-config.yml` (`seatrenderer`), and the constant's own comment names this client as its
// installer - the file the seat reads is `<seat agent dir>/config.yml`. This package therefore RENDERS
// NOTHING of its own. The role table that used to live here (commonBashDeny, nonLeadBashDeny,
// nonLeadToolDeny, reviewerToolDeny), the `--team` input it was composed from and the `apply` verb that
// wrote the file with it are GONE: a second table is a second source of truth, and this one had already
// drifted from the render in the direction that matters - it left the architect its `edit` tool, which the
// render denies.
//
// The install is the RUNTIME's, not this package's: the client fetches the render (clientsync.SeatRender),
// hands it to the runtime in the seat spec, and the runtime writes it before the seat's runner starts. What
// is left here is the READ-ONLY half - `policy render` prints exactly what would be installed for one seat,
// and `--check` compares an installed file with the render instead of keeping a drifted one silently.
package clientpolicy

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

// ExitDrift is --check's verdict: the installed config.yml is not what the hub's render says. It shares the
// number 1 with clientcmd.ExitRemote but is a different thing - nothing remote failed - so it is named here
// rather than borrowed.
const ExitDrift = 1

// PolicyKeysMatch is whether have carries the policy's own keys exactly as want states them. STRUCTURAL
// rather than byte-equal, because this document is the hub's render and this client is not its only writer's
// reader: a comparison by bytes calls a correctly-policed seat drifty the moment another tool adds a key of
// its own. What must hold is exact: every key want states, at the value it states. Keys want does not define
// are none of its business - but a key INSIDE one it defines is, so an added `tools.approval` entry does not
// slip through a shallow comparison.
func PolicyKeysMatch(want, have string) bool {
	var wanted map[string]any
	if err := yaml.Unmarshal([]byte(want), &wanted); err != nil || wanted == nil {
		return false
	}
	var present map[string]any
	if err := yaml.Unmarshal([]byte(have), &present); err != nil || present == nil {
		return false
	}
	for key, value := range wanted {
		if !reflect.DeepEqual(present[key], value) {
			return false
		}
	}
	return true
}

// Commands is what the dispatcher registers from this package.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{policyCommand{}}
}

const (
	policyUsage = "usage: 4genteam policy render --room RIG --seat SEAT [--state-root DIR] [--check]"
	renderUsage = policyUsage
)

type policyCommand struct{}

func (policyCommand) Name() string    { return "policy" }
func (policyCommand) Summary() string { return "per-seat policy: render what the hub would install" }

func (policyCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "4genteam policy: pick a verb: render")
		return clientcmd.ExitUsage
	}
	if wantsHelp(args) {
		fmt.Fprintf(stdout, "%s\n\nWhat would be installed as a seat's config.yml, printed from the hub's own "+
			"render of that seat's policy - there is no client-side table. With --check, compare the installed "+
			"file at the seat's own directory with the render and exit %d when they differ.\n",
			policyUsage, ExitDrift)
		return clientcmd.ExitOK
	}
	switch args[0] {
	case "render":
		return runRender(ctx, args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "4genteam policy: unknown verb %q; the verb is render\n", args[0])
	return clientcmd.ExitUsage
}

func runRender(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		fmt.Fprintf(stdout, "%s\n\nPrint the document the runtime installs as SEAT's config.yml: the hub's "+
			"render of the seat's policy, with this client's own non-policy keys. --check reads the installed "+
			"file instead and reports whether it still carries those keys, exiting %d when it does not.\n",
			renderUsage, ExitDrift)
		return clientcmd.ExitOK
	}
	values, _, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam policy render: %v. %s\n", err, renderUsage)
		return clientcmd.ExitUsage
	}
	if err := rejectUnknown(values, "room", "seat", "state-root", "check"); err != nil {
		fmt.Fprintf(stderr, "4genteam policy render: %v. %s\n", err, renderUsage)
		return clientcmd.ExitUsage
	}
	room := values["room"]
	seat := values["seat"]
	if room == "" || seat == "" {
		fmt.Fprintf(stderr, "4genteam policy render: --room and --seat are required. %s\n", renderUsage)
		return clientcmd.ExitUsage
	}
	stateRoot, resolveErr := clientenv.ResolveStateRoot(values["state-root"])
	if resolveErr != nil {
		fmt.Fprintf(stderr, "4genteam policy render: %v. %s\n", resolveErr, renderUsage)
		return clientcmd.ExitUsage
	}
	render, found, fetchErr := clientsync.SeatRender(ctx, room, seat)
	if fetchErr != nil {
		fmt.Fprintf(stderr, "4genteam policy render: the hub's render for %s/%s could not be read: %v\n", room, seat, fetchErr)
		return clientcmd.CodeOf(fetchErr, clientcmd.ExitUnavailable)
	}
	if !found {
		// A seat with no policy module gets NO file, which is the absence rule the seat dir already follows
		// for mcp.json - so this is not a drift and not a failure to read, it is a render with no document in
		// it, and saying so is the whole answer.
		fmt.Fprintf(stderr, "4genteam policy render: the hub's render for %s/%s carries no %s, so this seat has "+
			"no document to install\n", room, seat, clientsync.SeatRenderPath)
		return clientcmd.ExitUnavailable
	}
	want := clientsync.InstallDocument(render)
	if values["check"] != "true" {
		_, _ = io.WriteString(stdout, want)
		return clientcmd.ExitOK
	}
	path := clientsync.SeatConfigPath(stateRoot, room, seat)
	have, readErr := os.ReadFile(path)
	if readErr != nil {
		// A file that is not there is the loudest drift there is: the seat is running with no policy file at
		// all, which is the state this whole change exists to end.
		fmt.Fprintf(stdout, "%s: DRIFT (no installed file at %s: %v)\n", seat, path, readErr)
		return ExitDrift
	}
	if PolicyKeysMatch(want, string(have)) {
		fmt.Fprintf(stdout, "%s: ok (%s)\n", seat, path)
		return clientcmd.ExitOK
	}
	fmt.Fprintf(stdout, "%s: DRIFT (%s)\n", seat, path)
	return ExitDrift
}

// parseArgs splits a command line into --name value pairs and bare words. It is hand-rolled because the
// standard flag package STOPS at the first bare word, so `policy render --room R --seat S` - the documented
// order, and one argparse accepted - would parse no flags at all. `--check` is boolean, so it takes no value.
func parseArgs(args []string) (map[string]string, []string, error) {
	values := map[string]string{}
	booleans := map[string]bool{"check": true}
	var positional []string
	for i := range len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positional = append(positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if booleans[name] {
			if !hasValue {
				value = "true"
			}
			values[name] = value
			continue
		}
		if hasValue {
			values[name] = value
			continue
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("--%s needs a value", name)
		}
		i++
		values[name] = args[i]
	}
	return values, positional, nil
}

// rejectUnknown is the other half of a hand-rolled parse: a flag this verb does not define must be a
// usage error naming it, not a value silently carried and ignored.
func rejectUnknown(values map[string]string, allowed ...string) error {
	known := map[string]bool{}
	for _, name := range allowed {
		known[name] = true
	}
	for name := range values {
		if !known[name] {
			return fmt.Errorf("unknown flag --%s", name)
		}
	}
	return nil
}

func wantsHelp(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help")
}
