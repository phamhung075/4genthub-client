package clientwatch

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func (s shared) workingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

// The umbrella `watch` and the four bare verbs. The dispatcher keys on Name, so each bare verb is its
// own command rather than an argv rewrite - which is exactly what cli.py's WATCH_VERBS aliasing did.
type watchCommand struct{}

func (watchCommand) Name() string { return "watch" }
func (watchCommand) Summary() string {
	return "the live view of a room: the seat grid and the lead window"
}
func (watchCommand) NeedsRig() bool { return false }

func (watchCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	return runWatchArgs(ctx, args, stdout, stderr)
}

// runWatchArgs is `4genteam watch ...`: a verb name first is that verb, anything else is the umbrella.
func runWatchArgs(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "feed", "grid", "watch", "inputs", "input":
			return runVerb(ctx, args[0], args[1:], stdout, stderr)
		}
	}
	return runVerb(ctx, "watch", args, stdout, stderr)
}

type bareCommand struct {
	name    string
	summary string
}

func (c bareCommand) Name() string    { return c.name }
func (c bareCommand) Summary() string { return c.summary }
func (c bareCommand) NeedsRig() bool  { return false }

func (c bareCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	return runVerb(ctx, c.name, args, stdout, stderr)
}

type verbUsage struct {
	line  string
	notes string
}

var verbUsages = map[string]verbUsage{
	"watch": {
		line: "usage: 4genteam watch --rig RIG [--cols N] [--back N] [--width N] [--lines N] [--detail]",
		notes: "the seat grid, plus the lead window with its feed and an input pane.\n" +
			"`4genteam watch <feed|grid|inputs|input> ...` runs that verb.",
	},
	"grid": {
		line:  "usage: 4genteam grid --rig RIG [--cols N] [--back N] [--width N] [--lines N] [--detail]",
		notes: "one pane per live seat, each running that seat's own feed.",
	},
	"feed": {
		line:  "usage: 4genteam feed --rig RIG [--seat SEAT ...] [--back N] [--width N] [--lines N] [--detail]",
		notes: "one merged, READ-ONLY stream for a plain terminal or ssh.",
	},
	"inputs": {
		line:  "usage: 4genteam inputs <open|hide> --rig RIG [--seat SEAT ...]",
		notes: "an input pane under each seat pane; a line typed there is sent with `rig send`.",
	},
	"input": {
		line:  "usage: 4genteam input --rig RIG --seat SEAT",
		notes: "one seat's input pane: read lines and send each with `rig send`. /hide or Ctrl-D leaves.",
	},
}

const commonFlags = `
options:
  --rig RIG       the room to watch; required, there is no default room
  --back N        events replayed per seat when a pane starts (default 3 for feed, 40 otherwise)
  --width N       characters kept of each line (default 170 for feed, 200 otherwise)
  --lines N       lines shown of each call, result or message (default 60)
  --detail        also show the reasoning, what the agent says and what it is told
                  (on for grid and watch, off for feed)
  --cols N        grid columns (grid and watch only; default 2)
  --state-root DIR  the omp state root the session logs are read from
                    (default: $OPENRIG_STATE_ROOT)
`

func printVerbUsage(w io.Writer, verb string) {
	usage, known := verbUsages[verb]
	if !known {
		return
	}
	fmt.Fprintf(w, "%s\n\n%s\n", usage.line, usage.notes)
	if verb != "input" {
		fmt.Fprint(w, commonFlags)
	}
	if verb == "feed" || verb == "inputs" {
		fmt.Fprint(w, "  --seat SEAT     limit to this seat (repeatable)\n")
	}
	if verb == "inputs" {
		fmt.Fprint(w, "  action          open or hide\n")
	}
	if verb == "input" {
		fmt.Fprint(w, "\noptions:\n  --rig RIG       the room to watch; required\n  --seat SEAT     the seat to talk to; required\n")
	}
}

// runVerb parses one verb's arguments, checks the machine, and runs it. Every refusal is ONE message
// naming what is missing.
func runVerb(ctx context.Context, verb string, args []string, stdout, stderr io.Writer) int {
	switch verb {
	case "feed", "grid", "watch", "inputs", "input":
	default:
		fmt.Fprintf(stderr, "4genteam watch: unknown verb %q; the verbs are watch, grid, feed, inputs, input\n", verb)
		return clientcmd.ExitUsage
	}
	flags := flag.NewFlagSet(verb, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if hasHelp(args) {
		printVerbUsage(stdout, verb)
		return clientcmd.ExitOK
	}
	state := shared{}
	flags.StringVar(&state.rig, "rig", "", "the room to watch")
	flags.StringVar(&state.stateRoot, "state-root", "", "the omp state root")
	seats := stringList{}
	switch verb {
	case "feed":
		flags.IntVar(&state.back, "back", 3, "events replayed per seat")
		flags.IntVar(&state.width, "width", 170, "characters kept of each line")
		flags.IntVar(&state.lines, "lines", 60, "lines shown of each call")
		flags.BoolVar(&state.detail, "detail", false, "also show reasoning and messages")
		flags.Var(&seats, "seat", "limit to this seat (repeatable)")
	case "grid", "watch":
		flags.IntVar(&state.back, "back", 40, "events replayed per seat")
		flags.IntVar(&state.width, "width", 200, "characters kept of each line")
		flags.IntVar(&state.lines, "lines", 60, "lines shown of each call")
		flags.BoolVar(&state.detail, "detail", true, "also show reasoning and messages")
		flags.IntVar(&state.cols, "cols", 2, "grid columns")
	case "inputs":
		flags.Var(&seats, "seat", "limit to this seat (repeatable)")
	case "input":
		flags.Var(&seats, "seat", "the seat to talk to")
	}
	positional := []string{}
	if verb == "inputs" {
		// The action is a positional, as the Python's subparser has it.
		if len(args) > 0 && (args[0] == "open" || args[0] == "hide") {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printVerbUsage(stdout, verb)
			return clientcmd.ExitOK
		}
		fmt.Fprintf(stderr, "4genteam %s: %v\n", verb, err)
		return clientcmd.ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "4genteam %s: unexpected argument %q\n", verb, flags.Arg(0))
		return clientcmd.ExitUsage
	}
	state.seats = seats

	if state.rig == "" {
		fmt.Fprintf(stderr, "4genteam %s: --rig RIG is required (there is no default room)\n", verb)
		return clientcmd.ExitUsage
	}
	state.cols = max(1, state.cols)

	switch verb {
	case "feed":
		root, err := resolveStateRoot(state.stateRoot)
		if err != nil {
			return reportError(err, stderr)
		}
		if err := runFeed(ctx, root, state, stdout); err != nil {
			return reportError(err, stderr)
		}
		return clientcmd.ExitOK
	case "grid", "watch":
		if err := requireHerdr(); err != nil {
			return reportError(err, stderr)
		}
		// Every pane these verbs open runs `feed`, so the state root is checked now rather than left to
		// N panes each printing the same refusal.
		if _, err := resolveStateRoot(state.stateRoot); err != nil {
			return reportError(err, stderr)
		}
		run := func() error {
			if verb == "watch" {
				return runWatch(ctx, state, stdout)
			}
			return runGrid(ctx, state, stdout)
		}
		if err := run(); err != nil {
			return reportError(err, stderr)
		}
		return clientcmd.ExitOK
	case "inputs":
		if len(positional) == 0 {
			fmt.Fprintln(stderr, "4genteam inputs: pick an action: open or hide")
			return clientcmd.ExitUsage
		}
		if err := requireHerdr(); err != nil {
			return reportError(err, stderr)
		}
		if err := runInputs(ctx, state, positional[0], stdout); err != nil {
			return reportError(err, stderr)
		}
		return clientcmd.ExitOK
	default:
		if len(state.seats) == 0 {
			fmt.Fprintln(stderr, "4genteam input: --seat SEAT is required")
			return clientcmd.ExitUsage
		}
		if err := runInputLoop(ctx, state.rig, state.seats[0], os.Stdin, stdout); err != nil {
			return reportError(err, stderr)
		}
		return clientcmd.ExitOK
	}
}

func hasHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// stringList is a repeatable string flag: `--seat a --seat b`, and `--seat a b` is not accepted because
// the Go flag package stops at the first non-flag, which keeps one seat per flag.
type stringList []string

func (l *stringList) String() string { return fmt.Sprintf("%v", []string(*l)) }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}
