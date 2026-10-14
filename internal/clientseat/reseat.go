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
				Message: fmt.Sprintf("rig %q has no seat %q: %s", rig, seat, seatArgumentHint(seat)),
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

func (sendCommand) Name() string { return "send" }

// seatArgumentHint says what the SEAT argument is, for the refusal that fires when it resolves to no seat.
//
// The verifier who passed the canonical session name here spent two attempts learning the difference: "has no
// seat" is true both of a seat key that does not exist and of a session name handed to the wrong argument, so
// the reader had to guess which mistake they had made. Naming the expected argument is the whole difference
// between a refusal that can be acted on and one that has to be reverse-engineered.
func seatArgumentHint(seat string) string {
	if strings.Contains(seat, "@") {
		return fmt.Sprintf("%q is a session name; the SEAT argument is the rig's own seat key (e.g. \"reviewer\"), "+
			"and a session is addressed with `4genteam send SESSION MESSAGE`", seat)
	}
	return "SEAT is the rig's own seat key as the rig declares it (e.g. \"reviewer\")"
}

func (sendCommand) Summary() string { return "send one signed message to one seat" }

const defaultSender = "owner session"

// sendFlagSpecs is the `send` option surface, spelled out so that SplitFlags can take an option from either
// side of the message. Two groups, and the difference matters:
//
//   - honoured: --raw, --json (and --from for the native `send RIG SEAT MESSAGE` shape);
//   - refused explicitly (see sendRefused): a documented flag must stop the send, never silently become
//     message text.
var sendFlagSpecs = []clientruntime.FlagSpec{
	{Name: "from", Value: true},
	{Name: "raw"},
	{Name: "json"},
	{Name: "verify"},
	{Name: "force"},
	{Name: "dangerously-interact"},
	{Name: "wait-for-idle", Value: true},
	{Name: "reason", Value: true},
	{Name: "to", Value: true},
	{Name: "pod", Value: true},
	{Name: "rig", Value: true},
	{Name: "host", Value: true},
	{Name: "context", Value: true},
}

// sendRefused is the options this verb will not run, each with why. Nothing is sent when one is present: a
// flag that cannot be honoured must stop the send, not ride along with it.
var sendRefused = []struct{ Name, Why string }{
	{"verify", "--verify asks for a post-send verification the runtime does not run"},
	{"force", "--force is not a switch the runtime has"},
	{"wait-for-idle", "--wait-for-idle is not a switch the runtime has"},
	{"dangerously-interact", "--dangerously-interact is not a switch the runtime has"},
	{"reason", "--reason belongs to --dangerously-interact, which the runtime does not have"},
	{"to", "--to is a fan-out to several sessions; this verb sends to one"},
	{"pod", "--pod is a pod fan-out; this verb sends to one session"},
	{"rig", "--rig is a rig fan-out; this verb sends to one session"},
	{"host", "--host sends through a declared remote host; this verb speaks to the local runtime only"},
	{"context", "--context resolves a context pack and delivers it; this verb does not resolve packs"},
}

func (sendCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam send SESSION MESSAGE [options]")
		fmt.Fprintln(stdout, "       4genteam send [--from NAME] RIG SEAT MESSAGE")
		fmt.Fprintln(stdout, "\nSESSION is a session name (`4genteam ps <RIG> --nodes --json` names every seat's). It goes to")
		fmt.Fprintln(stdout, "the runtime's own socket, wrapped in a From/To/Sent envelope attributed to this shell's")
		fmt.Fprintln(stdout, "AGENTHUB_SESSION_NAME. A canonical name `ROOM-SEAT@ROOM` is TRANSLATED before it is sent, because the")
		fmt.Fprintln(stdout, "runtime's own session for a seat is `agenthub--ROOM--SEAT`: the name is READ from the room's runtime")
		fmt.Fprintln(stdout, "record. A session no record lists keeps the string you typed, and the runtime's refusal for it names")
		fmt.Fprintln(stdout, "what it expected.")
		fmt.Fprintln(stdout, "\nOptions, wherever they appear on the line:")
		fmt.Fprintln(stdout, "  --raw    send the text exactly, with no From/To/Sent envelope")
		fmt.Fprintln(stdout, "  --json   print the runtime's answer as JSON")
		fmt.Fprintln(stdout, "  --verify/--force/--wait-for-idle/--dangerously-interact/--reason and --to/--pod/--rig/--host/--context")
		fmt.Fprintln(stdout, "  are REFUSED: nothing is sent when one is present")
		fmt.Fprintln(stdout, "\nThe RIG SEAT MESSAGE shape sends MESSAGE to that seat, starting it with \"From NAME:\" so the seat")
		fmt.Fprintln(stdout, "knows who wrote it; --raw drops that prefix. SEAT is the rig's own seat key, as the rig declares it")
		fmt.Fprintln(stdout, "(e.g. \"reviewer\"); a canonical session name handed to that argument is refused BY NAME. The seat's")
		fmt.Fprintln(stdout, "session name is READ from the room's runtime record, never composed. NAME defaults to \""+defaultSender+"\".")
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

	// `send SESSION MESSAGE`: the message goes through the runtime socket.
	if len(positional) == 2 {
		return sendToSession(positional[0], positional[1], opts, stdout, stderr)
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

	// THE SEAT'S SESSION NAME IS READ, NEVER COMPOSED, from the room's runtime record: a name invented from
	// a convention cannot be split back into halves for a room whose name carries a dash.
	address, err := clientruntime.ResolveSeatAddress(rig, seat)
	if err != nil {
		return report(stderr, err)
	}
	// --raw means raw: no "From NAME:" prefix, because a caller who asked for the text exactly asked for the
	// text exactly.
	if opts["raw"] == "true" {
		text = strings.Join(positional[2:], " ")
	}
	return runtimeSend(address.SessionName, text, opts, stdout, stderr)
}

// sendToSession delivers one message to one session through the runtime socket.
//
// THE NAME IS TRANSLATED FIRST. `<room>-<seat>@<room>` is the canonical form the help documents and every
// seat's own session carries, but the runtime's session for that seat is `agenthub--<room>--<seat>`. The
// room's runtime record resolves the pair. A pair no record lists is left ALONE, deliberately: the caller's
// own string then goes to the runtime as typed, and its refusal is the honest answer for a session that
// does not exist.
func sendToSession(session, text string, opts map[string]string, stdout, stderr io.Writer) int {
	if rig, seat, ok := clientruntime.CanonicalRigSeat(session); ok {
		if address, err := clientruntime.ResolveSeatAddress(rig, seat); err == nil {
			session = address.SessionName
		}
	}
	return runtimeSend(session, text, opts, stdout, stderr)
}

// runtimeSend sends through the socket, wrapped in the From/To/Sent envelope unless --raw. `--json` prints
// the runtime's own answer document.
func runtimeSend(session, text string, opts map[string]string, stdout, stderr io.Writer) int {
	raw, err := clientruntime.RuntimeSendText(session, text, clientruntime.SendOptions{
		Raw: opts["raw"] == "true",
	})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam send: the runtime at %s refused it: %v; nothing was sent\n", clientruntime.DefaultSocketPath(), err)
		return clientcmd.ExitRemote
	}
	if opts["json"] == "true" {
		_, _ = stdout.Write(append(raw, '\n'))
		return clientcmd.ExitOK
	}
	fmt.Fprint(stdout, clientruntime.RuntimeSendLine(session, raw))
	return clientcmd.ExitOK
}
