package clientoffpeak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// ExitPeak is the gate's answer, not a failure: 0 means off-peak and safe to run, 1 means peak. It
// coincides with clientcmd.ExitRemote's number while answering a different question, so it is named
// here rather than borrowed - the same rule the lifecycle package's ExitStartFailed follows.
const ExitPeak = 1

const offpeakUsage = `usage: 4genteam offpeak [--quiet|--json]
       4genteam offpeak status
       4genteam offpeak set on|off

Is DeepSeek billing at off-peak right now? PEAK is 01:00-04:00 and 06:00-10:00 UTC, Monday-Friday.
Every other hour bills at half rate, and so do all of Saturday, Sunday and the days a file named by
DEEPSEEK_HOLIDAYS lists (one YYYY-MM-DD per line).

Exit 0 = off-peak, 1 = peak, so the verb is usable as a gate:   offpeak --quiet && run-the-work

status prints the OFFPEAK_ONLY setting, where it came from, and the current state. set writes the
setting to the client's one env file and prints nothing else from it.

The setting gates the verbs that start or resume DeepSeek seats - up, compact and sync respawn. With it
ON (the default) those verbs refuse at peak, naming when off-peak resumes; --any-time turns the check
off for one run, and wins over the setting in the environment or the env file.
`

// Commands is what the dispatcher registers from this package: one top-level verb.
func Commands() []clientcmd.Command { return []clientcmd.Command{offpeakCommand{}} }

type offpeakCommand struct{}

func (offpeakCommand) Name() string { return "offpeak" }
func (offpeakCommand) Summary() string {
	return "is DeepSeek billing at off-peak right now, and may this client start DeepSeek seats now"
}
func (offpeakCommand) NeedsRig() bool { return false }

func (offpeakCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, offpeakUsage)
		return clientcmd.ExitOK
	}
	if len(args) > 0 && args[0] == "set" {
		return runSet(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "status" {
		if len(args) > 1 {
			fmt.Fprintf(stderr, "4genteam offpeak status: it takes no arguments, and got %q\n", args[1])
			return clientcmd.ExitUsage
		}
		return runStatus(stdout, stderr)
	}
	quiet, asJSON, err := parseReport(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak: %v\n%s\n", err, offpeakUsage)
		return clientcmd.ExitUsage
	}
	return runReport(quiet, asJSON, stdout, stderr)
}

// parseReport is the script's own flag loop: --quiet silences it, --json makes it machine-readable, and
// --json wins when both are given (the shell branched on JSON first). An argument that is neither is a
// caller error naming it, which is this client's rule everywhere - the shell quietly ignored it.
func parseReport(args []string) (quiet, asJSON bool, err error) {
	for _, arg := range args {
		switch arg {
		case "-q", "--quiet":
			quiet = true
		case "--json":
			asJSON = true
		default:
			return false, false, fmt.Errorf("unknown argument: %s", arg)
		}
	}
	return quiet, asJSON, nil
}

// reportJSON is the shell gate's object, field for field and in its order, so a caller that already
// parses that output keeps working.
type reportJSON struct {
	State   string `json:"state"`
	UTC     string `json:"utc"`
	Weekday int    `json:"weekday"`
	Holiday int    `json:"holiday"`
	Reason  string `json:"reason"`
	Next    string `json:"next"`
}

func runReport(quiet, asJSON bool, stdout, stderr io.Writer) int {
	verdict, err := Now()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak: %v\n", err)
		return clientcmd.ExitUsage
	}
	if err := render(verdict, quiet, asJSON, stdout); err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	if verdict.Peak {
		return ExitPeak
	}
	return clientcmd.ExitOK
}

// render is the verb's three output forms in ONE place - the human line, the silence, and the JSON the
// shell gate emitted - so the boundary table can assert the exact bytes of each rather than re-deriving
// them. --json wins over --quiet when both are given, which is the shell gate's branch order.
func render(verdict Verdict, quiet, asJSON bool, out io.Writer) error {
	switch {
	case asJSON:
		encoded, err := json.Marshal(reportJSON{
			State:   verdict.State(),
			UTC:     verdict.Stamp(),
			Weekday: verdict.Weekday,
			Holiday: holidayFlag(verdict.Holiday),
			Reason:  verdict.Reason,
			Next:    verdict.Next,
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n", encoded)
		return err
	case !quiet:
		if _, err := fmt.Fprintf(out, "%s  %s UTC (%s)  %s\n",
			verdict.State(), verdict.Clock(), verdict.Day(), verdict.Reason); err != nil {
			return err
		}
		if verdict.Next != "" {
			if _, err := fmt.Fprintf(out, "  next: %s\n", verdict.Next); err != nil {
				return err
			}
		}
	}
	return nil
}

func runStatus(stdout, stderr io.Writer) int {
	mode, source, err := ResolveMode(nil, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak status: %v\n", err)
		return clientcmd.ExitUsage
	}
	verdict, err := Now()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak status: %v\n", err)
		return clientcmd.ExitUsage
	}
	fmt.Fprintf(stdout, "setting  %s  (from %s)\n", onOff(mode), source)
	fmt.Fprintf(stdout, "now      %s  %s UTC (%s)  %s\n", verdict.State(), verdict.Clock(), verdict.Day(), verdict.Reason)
	if verdict.Next != "" {
		fmt.Fprintf(stdout, "next     %s\n", verdict.Next)
	}
	fmt.Fprintf(stdout, "gate     %s\n", gateSentence(mode, verdict))
	return clientcmd.ExitOK
}

// envPath is where `offpeak set` writes the setting. A var so a test drives the verb without touching the
// machine's real env file, which also holds the account's credential.
var envPath = clientenv.DefaultPath

// runSet writes the ONE line. It prints the setting it wrote and the file it wrote it to, and nothing
// else from that file - the same file carries this client's credential.
func runSet(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintf(stderr, "4genteam offpeak set: give exactly one of on or off\n%s\n", offpeakUsage)
		return clientcmd.ExitUsage
	}
	mode, err := ParseMode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak set: %v\n", err)
		return clientcmd.ExitUsage
	}
	path, err := envPath()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak set: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	value := ValueOn
	if mode == AnyTime {
		value = ValueOff
	}
	if err := clientenv.Set(path, ModeEnv, value); err != nil {
		fmt.Fprintf(stderr, "4genteam offpeak set: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	fmt.Fprintf(stdout, "offpeak-only is %s  (%s=%s written to %s)\n", onOff(mode), ModeEnv, value, path)
	// An environment value wins over the file (clientenv.Load sets only what is absent), so a shell that
	// exports the variable keeps its own answer and the operator has to know that.
	if live := os.Getenv(ModeEnv); live != "" && live != value {
		fmt.Fprintf(stderr, "4genteam offpeak set: note: %s=%s is set in this environment, and an environment "+
			"value wins over the file - unset it for this line to take effect\n", ModeEnv, live)
	}
	return clientcmd.ExitOK
}

func onOff(mode Mode) string {
	if mode == AnyTime {
		return "off"
	}
	return "on"
}

// gateSentence says what the setting MEANS right now, which is the question `offpeak status` exists to
// answer in one line.
func gateSentence(mode Mode, verdict Verdict) string {
	if mode == AnyTime {
		return "off - this client starts or resumes a seat on a deepseek/ model at any hour"
	}
	if verdict.Peak {
		return "armed - a start or resume of a seat on a deepseek/ model is refused right now; it opens at " + verdict.Next
	}
	return "armed, and open - a start or resume of a seat on a deepseek/ model is allowed right now"
}

func holidayFlag(holiday bool) int {
	if holiday {
		return 1
	}
	return 0
}
