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
		fmt.Fprintln(stdout, "\nSESSION is a canonical session name (`rig ps --nodes --json` names every seat's). It goes to")
		fmt.Fprintln(stdout, "the runtime's own socket — this client's transport — wrapped in the From/To/Sent envelope the")
		fmt.Fprintln(stdout, "OpenRig CLI sends and attributed to this shell's OPENRIG_SESSION_NAME, which is the shape")
		fmt.Fprintln(stdout, "`rig send` has always meant. When no runtime answers, a rig's OpenRig daemon is asked instead;")
		fmt.Fprintln(stdout, "the switches below that name a handshake only the daemon runs always go to the daemon.")
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
		fmt.Fprintln(stdout, "no sender when it is not made from a seat shell). When the line carries a switch only the daemon")
		fmt.Fprintln(stdout, "runs — --verify, --force, --wait-for-idle, --dangerously-interact, --raw, --json — that switch")
		fmt.Fprintln(stdout, "decides the transport, and the message goes to the same seat's session (agenthub--RIG--SEAT) over")
		fmt.Fprintln(stdout, "the daemon; --raw drops the \"From NAME:\" prefix, because raw means the text exactly. A switch")
		fmt.Fprintln(stdout, "neither transport can honour stops the send rather than being ignored. It does NOT shell out to")
		fmt.Fprintln(stdout, "`rig`: after this client's rename that name is this binary. NAME defaults to \""+defaultSender+"\".")
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
	// message deliverer already calls. THE TRANSPORT IS RESOLVED, RUNTIME FIRST: the runtime socket is this
	// client's own, so it carries the message when it answers, and the daemon stays the fallback the live
	// rig still runs.
	if len(positional) == 2 {
		return sendToSession(ctx, positional[0], positional[1], opts, stdout, stderr)
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

	// THE SAME RULE AS THE SESSION SHAPE, on the switches THIS shape cannot carry: one its transport cannot
	// honour goes to the transport that can, and the send stops when neither can. Without this the switches
	// below were PARSED AND DROPPED here - `--wait-for-idle` typed into a busy seat without waiting, and
	// `--verify` reporting nothing at all, where reporting is the whole reason a caller passes it.
	// THE SEAT'S SESSION NAME IS READ, NEVER COMPOSED, and the record that answers also decides the transport.
	//
	// What this replaces, and why it had to go: this shape used to render `<rig>-<seat>@<rig>` itself and hand
	// that to the local runtime. Two things were wrong with it. The name was invented from a convention rather
	// than read from the seat's own record - and this rig's name carries a dash, so the canonical string it
	// produces cannot be split back into halves by anyone downstream. And the transport was hard-coded to the
	// runtime, which owns a tmux server of its own and therefore cannot address a pane the daemon created:
	// measured, it answered `can't find pane: 4genthub-min-lead@4genthub-min` for a pane that WAS named that,
	// and `agenthub--4genthub-min--4genthub-min-lead` for a call whose seat argument was already the full
	// session name. Neither name reached the seat, which is the owner's always-fails in one verb.
	//
	// The deleted shell fallback stays deleted: after this client's rename `rig` resolves to THIS binary, so
	// the old fallback was a self-call whose inner refusal was what the user read.
	switches := seatSendSwitches(opts)
	var address clientruntime.SeatAddress
	if switches != "" {
		// A daemon-only switch commits this send to the daemon before anything else happens, so the daemon's
		// seat record is the only source consulted - a healthy runtime is not disturbed for a request it can
		// never carry.
		address, err = clientruntime.ResolveDaemonSeatAddress(ctx, rig, seat)
	} else {
		address, err = clientruntime.ResolveSeatAddress(ctx, rig, seat)
	}
	if err != nil {
		if switches != "" {
			// The send came here BECAUSE of that switch, so naming the switch and the daemon aims the reader at
			// the transport the flag needs, rather than at the record lookup that failed first.
			fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", clientruntime.DaemonSwitchMissing(switches))
			return clientcmd.ExitUnavailable
		}
		return report(stderr, err)
	}
	if switches != "" {
		// --raw means raw: no "From NAME:" prefix, because a caller who asked for the text exactly asked for
		// the text exactly. Every other switch keeps this shape's own attribution.
		body := text
		if opts["raw"] == "true" {
			body = strings.Join(positional[2:], " ")
		}
		return daemonSend(ctx, address.SessionName, body, opts, switches, stdout, stderr)
	}
	// The owner of the pane carries the text: a name read from the daemon's seat table goes back to the
	// daemon, a name read from a room's runtime goes to that runtime. Sending to the wrong one is not a
	// transport preference - the other one cannot address the pane at all.
	if address.Transport == clientruntime.TransportDaemon {
		return daemonSend(ctx, address.SessionName, text, opts, "", stdout, stderr)
	}
	return runtimeSend(address.SessionName, text, opts, stdout, stderr)
}

// sendToSession delivers one message to one session over the transport this machine actually has.
//
// THE RUNTIME GOES FIRST, and the daemon is the fallback the live rig still runs. The one exception is a
// switch only the daemon can honour — --verify, --force, --wait-for-idle, --dangerously-interact — each of
// which names a handshake or a route's own combination rules that the runtime's SendText does not run.
// Answering --verify over the runtime would report a verification that was never performed, so those route
// to the daemon even when a runtime answers; that is the reason the fallback is kept rather than deleted.
func sendToSession(ctx context.Context, session, text string, opts map[string]string, stdout, stderr io.Writer) int {
	switch clientruntime.ResolveTransport() {
	case clientruntime.TransportRuntime:
		// A daemon-only switch still routes to the daemon, even though the runtime could carry the text: the
		// switch is a promise about what the send REPORTS, and only the daemon can keep it.
		if switches := switchesNeedingDaemon(opts); switches != "" {
			return daemonSend(ctx, session, text, opts, switches, stdout, stderr)
		}
		return runtimeSend(session, text, opts, stdout, stderr)
	case clientruntime.TransportDaemon:
		return daemonSend(ctx, session, text, opts, "", stdout, stderr)
	default:
		fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", clientruntime.FallbackMissing())
		return clientcmd.ExitUnavailable
	}
}

// daemonOnlySwitches are the options only the OpenRig daemon runs, in the CLI's own order: each names a
// handshake, or a route's own combination rules, that the runtime's SendText does not run. A send carrying one
// has to travel on the daemon or stop.
var daemonOnlySwitches = []string{"verify", "force", "wait-for-idle", "dangerously-interact"}

// seatShapeSwitches is what the RIG SEAT shape's own seat send cannot carry: the daemon-only list PLUS --raw
// and --json. The two lists differ because the transports do - the runtime path honours --raw and --json for
// a SESSION (`runtimeSend` renders both), while `SendTextToSeat` takes no options at all and answers with an
// error rather than a document. Same rule either way: the switch goes to the transport that honours it.
var seatShapeSwitches = append(append([]string{}, daemonOnlySwitches...), "raw", "json")

// switchesFrom names, in the CLI's spelling, the switches the caller passed from `names`, or "" for none. It
// returns the NAMES as well as the fact because the refusal the send owes has to name the flag that stopped
// it: "a daemon-only switch" would leave the reader to work out which one from the command line.
func switchesFrom(opts map[string]string, names []string) string {
	present := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := opts[name]; ok {
			present = append(present, "--"+name)
		}
	}
	return strings.Join(present, ", ")
}

// switchesNeedingDaemon is one row of the SESSION shape's decision: which daemon-only switch, if any, forces
// that send onto the daemon even when a runtime answers.
func switchesNeedingDaemon(opts map[string]string) string {
	return switchesFrom(opts, daemonOnlySwitches)
}

// seatSendSwitches is the RIG SEAT shape's version of the same question, on its own list.
func seatSendSwitches(opts map[string]string) string {
	return switchesFrom(opts, seatShapeSwitches)
}

// runtimeSend is the runtime half of the verb: the message goes through the socket, wrapped in the SAME
// From/To/Sent envelope the daemon path sends, so the pane text a receiver reads does not depend on which
// transport carried it. `--json` prints the runtime's own answer document.
func runtimeSend(session, text string, opts map[string]string, stdout, stderr io.Writer) int {
	raw, err := clientruntime.RuntimeSendText(session, text, clientruntime.SendOptions{
		Raw: opts["raw"] == "true",
	})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam send: the runtime at %s refused it: %v\n", clientruntime.DefaultSocketPath(), err)
		return clientcmd.ExitRemote
	}
	if opts["json"] == "true" {
		_, _ = stdout.Write(append(raw, '\n'))
		return clientcmd.ExitOK
	}
	fmt.Fprintf(stdout, "Sent to %s\n", session)
	return clientcmd.ExitOK
}

// daemonSend is the daemon-backed half of the verb: it sends, surfaces the daemon's advisory, and — when
// asked — its verification verdict, mirroring the CLI's exit codes (4xx → 1) rather than flattening every
// answer into one.
//
// `switches` is why this send had to come here: the daemon-only flags the caller passed, or "" when the
// runtime simply did not answer. It exists for ONE sentence — the refusal below — because the two reasons a
// send lands here need different words, and the wrong words send a reader to inspect a healthy transport.
func daemonSend(ctx context.Context, session, text string, opts map[string]string, switches string, stdout, stderr io.Writer) int {
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
		// A daemon that could not be REACHED has not refused anything. WHICH transport is missing depends on
		// why this send came here, and the two cases need different sentences: when a daemon-only switch
		// forced it, the runtime answered and is healthy, so the refusal names the daemon and the flag that
		// needs it; when the runtime itself did not answer, the missing transport IS the runtime and the
		// refusal says that instead. A daemon that ANSWERED keeps its own words and its own code below.
		if clientcmd.CodeOf(err, clientcmd.ExitRemote) == clientcmd.ExitUnavailable {
			refusal := clientruntime.FallbackMissing()
			if switches != "" {
				refusal = clientruntime.DaemonSwitchMissing(switches)
			}
			fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", refusal)
			return clientcmd.ExitUnavailable
		}
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
