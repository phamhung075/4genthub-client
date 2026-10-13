// `seat reseat` and `send`: the two lifecycle steps `seat sync` could not do on its own.
//
// `seat sync --relaunch quiet` restarts only the seats whose snapshot changed. A seat that holds an old
// skill, an old cwd or an old guide while its pin is already current is "in sync" and is never restarted.
// `seat reseat` restarts the named seats at their own quiet point, whatever their pin says, through the
// same quiet wait and relaunch that `seat sync` uses.
package clientseat

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
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
		nodes:     roomNodes,
		waitQuiet: func(rig, seat string) bool { return NewQuietWaiter(stateRoot).Wait(rig, seat) },
		relaunch:  relaunch,
		getenv:    os.Getenv,
		logf:      logLine,
	}
}

func runReseat(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam seat reseat RIG --seat NAME [--seat NAME]... [--force] "+clientoffpeak.ModeFlagUsage)
		fmt.Fprintln(stdout, "\nRestarts each named seat once it is quiet, so it loads the skills, guide and cwd it is now")
		fmt.Fprintln(stdout, "configured with. The seats go one at a time, in the order given. --force skips the wait: a seat in the middle of a turn loses that turn and resumes its session. The DeepSeek peak gate applies:")
		fmt.Fprintln(stdout, "with OFFPEAK_ONLY on (the default) a deepseek/ seat is not restarted at peak.")
		return clientcmd.ExitOK
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(args)
	flags := flag.NewFlagSet("seat reseat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var seats repeatedFlag
	flags.Var(&seats, "seat", "seat to restart (repeatable, required)")
	force := flags.Bool("force", false, "restart at once, without waiting for the seat to be quiet")
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
	deps := defaultReseatDeps(stateRoot)
	if *force {
		deps.waitQuiet = func(string, string) bool { return true }
	}
	code, err := reseatSeats(rig, seats, allowPeak, deps)
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

// roomNodes is the room's node records, read from the LOCAL RUNTIME instead of from `rig ps --nodes`.
//
// NAMED roomNodes, NOT rigNodes, ON PURPOSE: clientlifecycle has a function of the older name that takes the SAME
// runtime call and has the OPPOSITE failure contract - it treats an unreadable read as EMPTY, because its gate must
// never read an unreadable poll as a seat death. Two same-named functions in two packages with opposite contracts is
// a trap whose only guard would be a commit message, and later readers do not read those. Whichever name you arrived
// from, the other is one grep away and deliberately different.
//
// The exec is gone, the capability is not: this is the client's own reading (clientruntime.GetRoomStatus,
// the same one clientlifecycle's rigNodes takes), and it produces the SAME record shape the consumers
// below already key on - `logicalId` for the seat match in reseatSeats, and `runtime`/`model` for the peak
// gate - so nothing downstream learns a new vocabulary. After the client's own rename the exec was also a
// self-call into the installed binary, which is the class this row exists to remove.
//
// Unlike the lifecycle gate, a failed read here is an ERROR rather than an empty reading: `reseat` is a
// command that must be able to say why it will not restart a seat, and "no seats" and "could not ask"
// are different answers.
func roomNodes(rig string) ([]map[string]any, error) {
	seats, err := clientruntime.NewClient("").GetRoomStatus(rig)
	if err != nil {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("could not read %s's seats from the local runtime: %v", rig, err),
			Code:    ExitFailed,
		}
	}
	nodes := make([]map[string]any, 0, len(seats))
	for _, seat := range seats {
		nodes = append(nodes, map[string]any{
			"rigName":       rig,
			"logicalId":     fmt.Sprintf("%s.%s", rig, seat.Seat),
			"seat":          seat.Seat,
			"runtime":       seat.Runtime,
			"sessionStatus": seat.State,
			"model":         seat.Model,
			"pid":           seat.Pid,
		})
	}
	return nodes, nil
}

type sendCommand struct{}

func (sendCommand) Name() string    { return "send" }
func (sendCommand) Summary() string { return "send one signed message to one seat" }
func (sendCommand) NeedsRig() bool  { return false }

const defaultSender = "owner session"

// sendFlagSpecs is the CLI's own `send` option surface (packages/cli/src/commands/send.ts, the
// `.option(...)` block), spelled out so that SplitFlags can take an option from either side of the
// message. Two groups, and the difference matters:
//
//   - implemented here, because this shim has a daemon to talk to: --verify, --force, --wait-for-idle,
//     --raw, --dangerously-interact, --reason, --json;
//   - refused explicitly (see sendRefusedFlags), because they address a surface this local shim does
//     not have. A refusal is the point: the alternative is a documented flag silently becoming message
//     text, which is gate finding M1.
//
// --from is the CLI's deprecated-and-ignored option; this shim keeps it live for its OWN native
// `send RIG SEAT MESSAGE` shape, which is not the CLI's verb.
var sendFlagSpecs = []clientruntime.FlagSpec{
	{Name: "from", Value: true},
	{Name: "verify"},
	{Name: "force"},
	{Name: "raw"},
	{Name: "dangerously-interact"},
	{Name: "json"},
	{Name: "wait-for-idle", Value: true},
	{Name: "reason", Value: true},
	{Name: "to", Value: true},
	{Name: "pod", Value: true},
	{Name: "rig", Value: true},
	{Name: "host", Value: true},
	{Name: "context", Value: true},
}

// sendRefused is the options this verb will not run, in the CLI's documentation order, each with why.
// Nothing is sent when one is present: a flag that cannot be honoured must stop the send, not ride
// along with it.
var sendRefused = []struct{ Name, Why string }{
	{"to", "--to is a fan-out to several sessions; this shim sends to one"},
	{"pod", "--pod is a pod fan-out; this shim sends to one session"},
	{"rig", "--rig is a rig fan-out; this shim sends to one session"},
	{"host", "--host sends through a declared remote host; this shim speaks to the local daemon only"},
	{"context", "--context resolves a context pack and delivers it; this shim does not resolve packs"},
}

func (sendCommand) Run(ctx context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam send SESSION MESSAGE [options]")
		fmt.Fprintln(stdout, "       4genteam send [--from NAME] RIG SEAT MESSAGE")
		fmt.Fprintln(stdout, "\nSESSION is a canonical session name (`rig ps --nodes --json` names every seat's). It goes to the")
		fmt.Fprintln(stdout, "OpenRig daemon, wrapped in the From/To/Sent envelope the OpenRig CLI sends and attributed to this")
		fmt.Fprintln(stdout, "shell's OPENRIG_SESSION_NAME, which is the shape `rig send` has always meant.")
		fmt.Fprintln(stdout, "\nOptions, wherever they appear on the line, are the OpenRig CLI's own:")
		fmt.Fprintln(stdout, "  --verify                     the daemon re-confirms the snippet in its post-send capture")
		fmt.Fprintln(stdout, "  --wait-for-idle <seconds>    wait for the target to be idle before typing")
		fmt.Fprintln(stdout, "  --raw                        send the text exactly, with no From/To/Sent envelope")
		fmt.Fprintln(stdout, "  --dangerously-interact       type the text as prompt keystrokes (needs --reason)")
		fmt.Fprintln(stdout, "  --reason <text>              why an interactive prompt is being driven")
		fmt.Fprintln(stdout, "  --force                      back-compat no-op: a busy pane already sends-with-advisory")
		fmt.Fprintln(stdout, "  --json                       print the daemon's answer as JSON")
		fmt.Fprintln(stdout, "  --to/--pod/--rig/--host/--context are REFUSED: this shim sends to one local session")
		fmt.Fprintln(stdout, "\nThe RIG SEAT MESSAGE shape is the client's own: it sends MESSAGE to that seat through the local")
		fmt.Fprintln(stdout, "runtime, starting it with \"From NAME:\" so the seat knows who wrote it (a runtime call carries")
		fmt.Fprintln(stdout, "no sender when it is not made from a seat shell). It does NOT shell out to `rig`: after this")
		fmt.Fprintln(stdout, "client's rename that name is this binary. NAME defaults to \""+defaultSender+"\".")
		return clientcmd.ExitOK
	}

	opts, positional, err := clientruntime.SplitFlags(args, sendFlagSpecs)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam send: %v; nothing was sent\n", err)
		return clientcmd.ExitUsage
	}
	for _, refused := range sendRefused {
		if _, present := opts[refused.Name]; present {
			fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", refused.Why)
			return clientcmd.ExitUsage
		}
	}

	// `rig send SESSION MESSAGE` — the OpenRig CLI's own shape, and the shape this client's supervised
	// message deliverer already calls. The daemon owns this machine's session table, so it is the party
	// that resolves the name.
	if len(positional) == 2 {
		return daemonSend(ctx, positional[0], positional[1], opts, stdout, stderr)
	}

	if len(positional) < 3 {
		fmt.Fprintln(stderr, "4genteam send: SESSION and MESSAGE, or RIG SEAT and MESSAGE, are required")
		return clientcmd.ExitUsage
	}
	rig, seat := positional[0], positional[1]
	from := opts["from"]
	if from == "" {
		from = defaultSender
	}
	text := fmt.Sprintf("From %s: %s", from, strings.Join(positional[2:], " "))

	// The seat send is the CLIENT'S OWN, and it goes through the local runtime: the fallback that shelled
	// out to `rig send` is DELETED rather than repaired. After this client's own rename that name resolves
	// to THIS binary (`~/.local/bin/rig` is a symlink to `4genteam`), so the "fallback" was a self-call: the
	// outer verb would hand the two-argument OpenRig form to itself and the inner refusal was what the user
	// read - a failure report about a command nobody asked for. A send that the runtime refuses now reports
	// the runtime's own reason, which is the only party that knows why.
	rtClient := clientruntime.NewClient("")
	if err := rtClient.SendTextToSeat(rig, seat, text); err != nil {
		return report(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("could not send to %s/%s: %v", rig, seat, err),
			Code:    ExitFailed,
		})
	}
	fmt.Fprintf(stdout, "Sent to %s/%s\n", rig, seat)
	return clientcmd.ExitOK
}

// daemonSend is the daemon-backed half of the verb: it sends, surfaces the daemon's advisory, and — when
// asked — its verification verdict, mirroring the CLI's exit codes (4xx → 1) rather than flattening every
// answer into one.
func daemonSend(ctx context.Context, session, text string, opts map[string]string, stdout, stderr io.Writer) int {
	jsonOut := opts["json"] == "true"

	options := clientruntime.SendOptions{
		Verify:              opts["verify"] == "true",
		Force:               opts["force"] == "true",
		DangerouslyInteract: opts["dangerously-interact"] == "true",
		Reason:              opts["reason"],
		// --dangerously-interact implies --raw, exactly as the CLI documents it.
		Raw: opts["raw"] == "true" || opts["dangerously-interact"] == "true",
	}
	if seconds, present := opts["wait-for-idle"]; present {
		value, err := strconv.Atoi(seconds)
		if err != nil || value <= 0 {
			fmt.Fprintf(stderr, "4genteam send: --wait-for-idle wants a positive number of seconds, got %q; nothing was sent\n", seconds)
			return clientcmd.ExitUsage
		}
		options.WaitForIdleMs = value * 1000
	}

	result, status, err := clientruntime.OpenRigSend(ctx, session, text, options)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam send: %v\n", err)
		return report(stderr, err)
	}

	if status >= 400 {
		words := result.Error
		if words == "" {
			words = result.Detail
		}
		if jsonOut {
			_, _ = stdout.Write(append(result.Raw, '\n'))
		} else {
			fmt.Fprintf(stderr, "4genteam send: the daemon refused it (HTTP %d) %s\n", status, words)
		}
		return clientcmd.ExitRemote
	}

	if jsonOut {
		_, _ = stdout.Write(append(result.Raw, '\n'))
		return clientcmd.ExitOK
	}

	// A `retained` outcome is NOT a delivery, and the CLI gives it its own line rather than a flat
	// "Sent to" — mirroring that is the difference between reporting and claiming.
	if result.Outcome == "retained" {
		fmt.Fprintf(stdout, "Retained, not delivered to %s. %s\n", session, strings.TrimSpace(result.Warning))
		return clientcmd.ExitOK
	}

	fmt.Fprintf(stdout, "Sent to %s\n", session)
	if warning := strings.TrimSpace(result.Warning); warning != "" {
		fmt.Fprintf(stdout, "Advisory: %s\n", warning)
	}
	if options.Verify {
		printVerify(stdout, session, result)
	}
	return clientcmd.ExitOK
}

// printVerify renders the verification verdict in the CLI's own words.
//
// WHAT THIS IS, HONESTLY. The daemon re-confirms the snippet in its own post-send capture, so `Verified:
// yes|no` here is that verdict and not a guess: outcome "delivered" means the text AND the Enter landed
// and the capture re-confirmed the snippet, and "rendered-unconfirmed" means it landed but the capture
// raced a TUI redraw (daemon/domain/session-transport.d.ts, SendResult.outcome). The CLI adds a
// CLIENT-side consumption check on top of it — a staged-residual probe plus one guarded Enter — which
// this shim does not run; it therefore prints the CLI's own line for an effect check that could not run
// (send.ts effectHumanLines) instead of staying quiet about the difference. No exit code is set here,
// which is also the CLI's behaviour on the non-staged branch.
func printVerify(stdout io.Writer, session string, result clientruntime.SendResult) {
	if result.HasVerified && result.Verified {
		fmt.Fprintln(stdout, "Verified: yes")
	} else {
		fmt.Fprintln(stdout, "Verified: no")
	}
	switch result.Outcome {
	case "delivered":
		fmt.Fprintln(stdout, "Delivery: delivered (message landed; render confirmed)")
	case "rendered-unconfirmed":
		fmt.Fprintf(stdout, "Delivery: rendered-unconfirmed (landed; pane re-render not confirmed - confirm with: rig capture %s)\n", session)
	}
	if result.PromptInteraction != "" {
		fmt.Fprintf(stdout, "Note: the pane-effect check could not run (explicit interaction (%s); consumption unverified; no automatic submit retry); the verdict above is transport-level only.\n", result.PromptInteraction)
		return
	}
	fmt.Fprintln(stdout, "Note: the pane-effect check could not run (this shim does not run the CLI's staged-residual probe); the verdict above is transport-level only.")
}
