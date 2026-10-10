// `seat reseat` and `send`: the two lifecycle steps `seat sync` could not do on its own.
//
// `seat sync --relaunch quiet` restarts only the seats whose snapshot changed. A seat that holds an old
// skill, an old cwd or an old guide while its pin is already current is "in sync" and is never restarted.
// `seat reseat` restarts the named seats at their own quiet point, whatever their pin says, through the
// same quiet wait and relaunch that `seat sync` uses.
package clientseat

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
)

// reseatDeps is what reseat touches outside its arguments, as fields so the suite needs no rig and no wait.
type reseatDeps struct {
	nodes     func(rig string) ([]map[string]any, error)
	waitQuiet func(rig, seat string) bool
	relaunch  func(rig, seat string) error
	getenv    func(string) string
	logf      func(format string, args ...any)
}

func defaultReseatDeps(stateRoot string) *reseatDeps {
	return &reseatDeps{
		nodes:     rigNodes,
		waitQuiet: func(rig, seat string) bool { return NewQuietWaiter(stateRoot).Wait(rig, seat) },
		relaunch:  relaunch,
		getenv:    os.Getenv,
		logf:      logLine,
	}
}

func runReseat(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam seat reseat RIG --seat NAME [--seat NAME]... "+clientoffpeak.ModeFlagUsage)
		fmt.Fprintln(stdout, "\nRestarts each named seat once it is quiet, so it loads the skills, guide and cwd it is now")
		fmt.Fprintln(stdout, "configured with. The seats go one at a time, in the order given. The DeepSeek peak gate applies:")
		fmt.Fprintln(stdout, "with OFFPEAK_ONLY on (the default) a deepseek/ seat is not restarted at peak.")
		return clientcmd.ExitOK
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(args)
	flags := flag.NewFlagSet("seat reseat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var seats repeatedFlag
	flags.Var(&seats, "seat", "seat to restart (repeatable, required)")
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		fmt.Fprintln(stderr, "4genteam seat reseat: the RIG comes first, then the --seat flags")
		return clientcmd.ExitUsage
	}
	rig := rest[0]
	if err := flags.Parse(rest[1:]); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() != 0 || len(seats) == 0 {
		fmt.Fprintln(stderr, "4genteam seat reseat: a RIG and at least one --seat are required")
		return clientcmd.ExitUsage
	}
	stateRoot, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return report(stderr, err)
	}
	code, err := reseatSeats(rig, seats, allowPeak, defaultReseatDeps(stateRoot))
	if err != nil {
		return report(stderr, err)
	}
	return code
}

// reseatSeats refuses before touching anything when a seat is unknown or the peak gate says no, then
// restarts the seats one by one. A seat that is not quiet in time is left running and the exit code says so.
func reseatSeats(rig string, seats []string, allowPeak bool, deps *reseatDeps) (int, error) {
	nodes, err := deps.nodes(rig)
	if err != nil {
		return 0, err
	}
	targets := make([]map[string]any, 0, len(seats))
	for _, seat := range seats {
		found := false
		for _, node := range nodes {
			if node["logicalId"] == rig+"."+seat {
				targets = append(targets, node)
				found = true
				break
			}
		}
		if !found {
			return 0, &clientcmd.CodedError{
				Message: fmt.Sprintf("rig %q has no seat %q", rig, seat),
				Code:    clientcmd.ExitUsage,
			}
		}
	}
	proceed, message, err := clientoffpeak.Check(allowPeak, targets, deps.getenv)
	if err != nil {
		return 0, &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
	}
	if !proceed {
		return 0, &clientcmd.CodedError{Message: "seat reseat: " + message, Code: clientcmd.ExitUnavailable}
	}
	failed := 0
	for _, seat := range seats {
		if !deps.waitQuiet(rig, seat) {
			deps.logf("%s: not quiet within %d minutes, left running", seat, int(quietWaitSeconds/60))
			failed++
			continue
		}
		if err := deps.relaunch(rig, seat); err != nil {
			return 0, err
		}
		deps.logf("%s: restarted", seat)
	}
	if failed > 0 {
		return ExitFailed, nil
	}
	return clientcmd.ExitOK, nil
}

// rigNodes is `rig ps --nodes --rig RIG --json`.
func rigNodes(rig string) ([]map[string]any, error) {
	path, err := exec.LookPath("rig")
	if err != nil {
		return nil, &clientcmd.CodedError{Message: "rig is not on PATH", Code: clientcmd.ExitUnavailable}
	}
	out, err := exec.Command(path, "ps", "--nodes", "--rig", rig, "--json").Output()
	if err != nil {
		return nil, &clientcmd.CodedError{Message: fmt.Sprintf("rig ps --nodes failed: %v", err), Code: ExitFailed}
	}
	var nodes []map[string]any
	if json.Unmarshal(out, &nodes) != nil {
		return nil, &clientcmd.CodedError{Message: "rig ps --nodes --json did not answer with a node list", Code: ExitFailed}
	}
	return nodes, nil
}

type sendCommand struct{}

func (sendCommand) Name() string    { return "send" }
func (sendCommand) Summary() string { return "send one signed message to one seat" }
func (sendCommand) NeedsRig() bool  { return false }

const defaultSender = "owner session"

func (sendCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam send [--from NAME] RIG SEAT MESSAGE")
		fmt.Fprintln(stdout, "\nSends MESSAGE to the seat with `rig send`, starting it with \"From NAME:\" so the seat knows who")
		fmt.Fprintln(stdout, "wrote it (rig does not carry the sender when the call is not made from a seat shell). The text goes")
		fmt.Fprintln(stdout, "to rig as one argument, so no shell expands backticks or $ in it. NAME defaults to \""+defaultSender+"\".")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", defaultSender, "who the message is from")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() < 3 {
		fmt.Fprintln(stderr, "4genteam send: RIG, SEAT and MESSAGE are required")
		return clientcmd.ExitUsage
	}
	rig, seat := flags.Arg(0), flags.Arg(1)
	text := fmt.Sprintf("From %s: %s", *from, strings.Join(flags.Args()[2:], " "))
	path, err := exec.LookPath("rig")
	if err != nil {
		return report(stderr, &clientcmd.CodedError{Message: "rig is not on PATH", Code: clientcmd.ExitUnavailable})
	}
	done, err := exec.Command(path, "send", rig+"-"+seat+"@"+rig, text).CombinedOutput()
	fmt.Fprint(stdout, string(done))
	if err != nil {
		return report(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("rig send failed: %s", truncate(strings.TrimSpace(string(done)), 200)),
			Code:    ExitFailed,
		})
	}
	return clientcmd.ExitOK
}
