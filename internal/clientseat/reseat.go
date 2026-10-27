// `seat reseat` and `send`: the two lifecycle steps the removed `seat sync` could not do on its own.
//
// The removed `seat sync --relaunch quiet` restarted only the seats whose snapshot changed, so a seat
// holding an old skill, an old cwd or an old guide while its pin was already current counted as "in
// sync" and was never restarted. `seat reseat` restarts the named seats at their own quiet point,
// whatever their pin says, through the same quiet wait and relaunch the removed verb used.
package clientseat

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientlifecycle"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/commpolicy"
	"github.com/phamhung075/4genthub-client/internal/seatpark"
)

// reseatDeps is what reseat touches outside its arguments, as fields so the suite needs no room and no wait.
type reseatDeps struct {
	nodes     func(room string) ([]map[string]any, error)
	waitQuiet func(room, seat string) bool
	relaunch  func(room, seat string) error
	checkSeat func(room, seat string) []string
	getenv    func(string) string
	logf      func(format string, args ...any)
}

func defaultReseatDeps(stateRoot string) *reseatDeps {
	return &reseatDeps{
		nodes:     roomNodes,
		waitQuiet: func(room, seat string) bool { return NewQuietWaiter(stateRoot).Wait(room, seat) },
		relaunch:  relaunch,
		getenv:    os.Getenv,
		checkSeat: func(room, seat string) []string {
			return clientlifecycle.SeatCheckLines(stateRoot, room, seat)
		},
		logf: logLine,
	}
}

// reseatDepsFor builds the verb's dependencies for the root it resolved. It is a package-level variable, the
// repo's style (a seam without an interface), so the suite can read the root the verb hands its deps: the
// flag is only honoured if it reaches the quiet waiter and the seat doctor, not just the parser.
var reseatDepsFor = defaultReseatDeps

func runReseat(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam seat reseat ROOM --seat NAME [--seat NAME]... [--force] "+clientoffpeak.ModeFlagUsage+" [--state-root DIR]")
		fmt.Fprintln(stdout, "\nRestarts each named seat once it is quiet, so it loads the skills, guide and cwd it is now")
		fmt.Fprintln(stdout, "configured with. The seats go one at a time, in the order given. --force skips the wait: a seat in the middle of a turn loses that turn and resumes its session. The DeepSeek peak gate applies:")
		fmt.Fprintln(stdout, "with OFFPEAK_ONLY on (the default) a deepseek/ seat is not restarted at peak.")
		return clientcmd.ExitOK
	}
	rootFlag, remaining, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam seat reseat: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUsage)
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(remaining)
	flags := flag.NewFlagSet("seat reseat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var seats repeatedFlag
	flags.Var(&seats, "seat", "seat to restart (repeatable, required)")
	force := flags.Bool("force", false, "restart at once, without waiting for the seat to be quiet")
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		fmt.Fprintln(stderr, "4genteam seat reseat: the ROOM comes first, then the --seat flags")
		return clientcmd.ExitUsage
	}
	room := rest[0]
	if err := flags.Parse(rest[1:]); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() != 0 || len(seats) == 0 {
		fmt.Fprintln(stderr, "4genteam seat reseat: a ROOM and at least one --seat are required")
		return clientcmd.ExitUsage
	}
	stateRoot, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		return report(stderr, err)
	}
	deps := reseatDepsFor(stateRoot)
	if *force {
		deps.waitQuiet = func(string, string) bool { return true }
	}
	code, err := reseatSeats(room, seats, allowPeak, deps)
	if err != nil {
		return report(stderr, err)
	}
	return code
}

// reseatSeats refuses before touching anything when a seat is unknown or the peak gate says no, then
// restarts the seats one by one. A seat that is not quiet in time is left running and the exit code says so.
func reseatSeats(room string, seats []string, allowPeak bool, deps *reseatDeps) (int, error) {
	nodes, err := deps.nodes(room)
	if err != nil {
		return 0, err
	}
	targets := make([]map[string]any, 0, len(seats))
	for _, seat := range seats {
		found := false
		for _, node := range nodes {
			if node["logicalId"] == room+"."+seat {
				targets = append(targets, node)
				found = true
				break
			}
		}
		if !found {
			return 0, &clientcmd.CodedError{
				Message: fmt.Sprintf("room %q has no seat %q: %s", room, seat, seatArgumentHint(seat)),
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
	for i, seat := range seats {
		status, _ := targets[i]["sessionStatus"].(string)
		status = strings.TrimSpace(status)
		seatIsRunning := status == "" || strings.EqualFold(status, "running")
		if seatIsRunning {
			// THE WAIT IS ANNOUNCED BEFORE IT BEGINS, and that is the half of this fix a caller feels: the quiet
			// wait is up to quietWaitSeconds, it used to print NOTHING until it ended, and with nothing said a seat
			// being waited for and a verb that has hung are the same thing on the terminal. Measured live
			// 2026-10-15: `seat reseat` was killed at 300s inside this wait having printed nothing at all, and the
			// reading was "the verb never returns".
			deps.logf("%s: waiting for quiet (up to %s), then the seat is restarted", seat, quietBudget())
			if !deps.waitQuiet(room, seat) {
				deps.logf("%s: not quiet within %s, left running", seat, quietBudget())
				failed++
				continue
			}
		} else {
			deps.logf("%s: not running (%s), restarting without waiting for quiet", seat, status)
		}
		// AND THE RESTART IS ANNOUNCED BEFORE THE CALL, because that call stops a session and starts another:
		// the line after it is the answer, and with no line before it `restarting` is indistinguishable from the
		// silence this verb was reported for.
		deps.logf("%s: restarting - the runtime stops the session and starts it again", seat)
		if err := deps.relaunch(room, seat); err != nil {
			return 0, err
		}
		deps.logf("%s: restarted", seat)
		// The seat doctor's checks, on the seat just restarted: the cwd it came back in, the skills it loaded
		// and whether they are the tree's copies. They are what the restart was for, so they are reported
		// the moment it returns - a runtime relaunch that did not bring the seat back leaves its status
		// saying so, and the check answers with that fact rather than silently. They are findings, never a
		// refusal: reseat's exit code is the one it already had.
		for _, line := range deps.checkSeat(room, seat) {
			deps.logf("%s", line)
		}
	}
	if failed > 0 {
		return ExitFailed, nil
	}
	return clientcmd.ExitOK, nil
}

// roomNodes is the room's node records, read from the LOCAL RUNTIME instead of from `rig ps --nodes`.
//
// NAMED roomNodes, NOT roomNodes, ON PURPOSE: clientlifecycle has a function of the older name that takes the SAME
// runtime call and has the OPPOSITE failure contract - it treats an unreadable read as EMPTY, because its gate must
// never read an unreadable poll as a seat death. Two same-named functions in two packages with opposite contracts is
// a trap whose only guard would be a commit message, and later readers do not read those. Whichever name you arrived
// from, the other is one grep away and deliberately different.
//
// The exec is gone, the capability is not: this is the client's own reading (clientruntime.GetRoomStatus,
// the same one clientlifecycle's roomNodes takes), and it produces the SAME record shape the consumers
// below already key on - `logicalId` for the seat match in reseatSeats, and `runtime`/`model` for the peak
// gate - so nothing downstream learns a new vocabulary. After the client's own rename the exec was also a
// self-call into the installed binary, which is the class this row exists to remove.
//
// Unlike the lifecycle gate, a failed read here is an ERROR rather than an empty reading: `reseat` is a
// command that must be able to say why it will not restart a seat, and "no seats" and "could not ask"
// are different answers.
func roomNodes(room string) ([]map[string]any, error) {
	seats, err := clientruntime.NewClient("").GetRoomStatus(room)
	if err != nil {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("could not read %s's seats from the local runtime: %v", room, err),
			Code:    ExitFailed,
		}
	}
	nodes := make([]map[string]any, 0, len(seats))
	for _, seat := range seats {
		nodes = append(nodes, map[string]any{
			"roomName":      room,
			"logicalId":     fmt.Sprintf("%s.%s", room, seat.Seat),
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
		return fmt.Sprintf("%q is a session name; the SEAT argument is the room's own seat key (e.g. \"reviewer\"), "+
			"and a session is addressed with `4genteam send SESSION MESSAGE`", seat)
	}
	return "SEAT is the room's own seat key as the room declares it (e.g. \"reviewer\")"
}

func (sendCommand) Summary() string { return "send one signed message to one seat" }

// unknownSender is the label OpenRig itself puts on a message sent with no sender in the environment
// (`cli/src/sender-identity.ts`, SENDER_FALLBACK). It is named here only so the help can say what a seat sees
// when the signature is missing: nothing in this package writes a sender name anywhere.
const unknownSender = "<unknown sender>"

// sendFlagSpecs is the `send` option surface, spelled out so that SplitFlags can take an option from either
// side of the message. Two groups, and the difference matters:
//
//   - honoured: --raw, --json (and --from for the native `send ROOM SEAT MESSAGE` shape);
//   - refused explicitly (see sendRefused): a documented flag must stop the send, never silently become
//     message text.
var sendFlagSpecs = []clientruntime.FlagSpec{
	// --via-owner is the OWNER's route (ruling 4.6, method C): the message is left in a private file and one
	// short command carrying only its path is typed into the room's owner node, so the signature a seat sees is
	// the one the node's own terminal carries rather than a name this client wrote.
	{Name: "via-owner"},
	// --intent is OPTIONAL AND DEFAULTED TO `notice`, and the usage line below names that default: the intent
	// picks the link kind that can authorize the send, so it is recorded on the decision line the send writes
	// (which intent was used, not which the sender had in mind) and a default nobody was told about would be
	// the silent guess this flag exists to prevent.
	{Name: "intent", Value: true},
	{Name: "raw"},
	{Name: "json"},
	{Name: "socket", Value: true},
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
	{"rig", "--rig belongs to the predecessor's send, where it was a room fan-out; this verb sends to one session"},
	{"host", "--host sends through a declared remote host; this verb speaks to the local runtime only"},
	{"context", "--context resolves a context pack and delivers it; this verb does not resolve packs"},
}

func (sendCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, "usage: 4genteam send SESSION MESSAGE [options]")
		fmt.Fprintln(stdout, "       4genteam send ROOM SEAT MESSAGE [options]")
		fmt.Fprintln(stdout, "\nThe sender is this shell's own seat; there is no flag that names another one. From the owner's shell,")
		fmt.Fprintln(stdout, "which is no seat, send with `printf 'message\\n' | 4genteam input --room ROOM --seat SEAT` instead.")
		fmt.Fprintln(stdout, "\nSESSION is a session name (`4genteam ps <ROOM> --nodes --json` names every seat's). It goes to")
		fmt.Fprintln(stdout, "the runtime's own socket, wrapped in a From/To/Sent envelope attributed to this shell's")
		fmt.Fprintln(stdout, "AGENTHUB_SESSION_NAME. A canonical name `ROOM-SEAT@ROOM` is TRANSLATED before it is sent, because the")
		fmt.Fprintln(stdout, "runtime's own session for a seat is `agenthub--ROOM--SEAT`: the name is READ from the room's runtime")
		fmt.Fprintln(stdout, "record. A session no record lists keeps the string you typed, and the runtime's refusal for it names")
		fmt.Fprintln(stdout, "what it expected.")
		fmt.Fprintln(stdout, "\nOptions, wherever they appear on the line:")
		fmt.Fprintln(stdout, "  --intent <"+commpolicy.IntentNames+">")
		fmt.Fprintln(stdout, "           what the message IS. It picks the link kind that can authorize the send (task ->")
		fmt.Fprintln(stdout, "           delegates_to, escalation/report -> escalates_to, question/notice -> collaborates_with), the")
		fmt.Fprintln(stdout, "           room's declared links are what decide, and the intent actually used is recorded on the")
		fmt.Fprintln(stdout, "           decision line. DEFAULT: "+string(commpolicy.IntentNotice)+".")
		fmt.Fprintln(stdout, "  --via-owner  send from the room's owner node: the message goes to a private file and one command")
		fmt.Fprintln(stdout, "  carrying only its path is typed into that node, so the signature is the node's own - attribution, NOT")
		fmt.Fprintln(stdout, "  authentication. Only that node may ask for it: any other shell is refused (exit "+fmt.Sprint(clientcmd.ExitRefused)+")")
		fmt.Fprintln(stdout, "  --raw    send the text exactly, with no From/To/Sent envelope")
		fmt.Fprintln(stdout, "  --json   print the runtime's answer as JSON")
		fmt.Fprintln(stdout, "  --socket PATH  the runtime daemon's Unix socket: the flag, else $"+clientruntime.RuntimeSocketEnv+", else")
		fmt.Fprintln(stdout, "  this machine's own. A seat of another daemon - an isolated run, a second daemon on this box - is reached")
		fmt.Fprintln(stdout, "  with it, and the seat's name is read from THAT daemon's record.")
		fmt.Fprintln(stdout, "  --verify/--force/--wait-for-idle/--dangerously-interact/--reason and --to/--pod/--rig/--host/--context")
		fmt.Fprintln(stdout, "  are REFUSED: nothing is sent when one is present")
		fmt.Fprintln(stdout, "\nThe ROOM SEAT MESSAGE shape sends MESSAGE to that seat AS TYPED, with no sender written into the text:")
		fmt.Fprintln(stdout, "the sender a seat reads comes from the environment of the shell that ran the send, and a message with")
		fmt.Fprintln(stdout, "none shows as \""+unknownSender+"\"; --raw drops the runtime's own envelope. SEAT is the room's")
		fmt.Fprintln(stdout, "own seat key, as the room declares it")
		fmt.Fprintln(stdout, "(e.g. \"reviewer\"); a canonical session name handed to that argument is refused BY NAME. The seat's")
		fmt.Fprintln(stdout, "session name is READ from the room's runtime record, never composed.")
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

	// THE SOCKET IS RESOLVED ONCE, and every read and write this verb makes goes through it: the seat's name
	// is READ from a record, so a name read from one daemon and a send written to another is a pair that can
	// only be right by luck.
	socket := sendSocket(opts)

	// THE SHAPES ARE CHECKED BEFORE ANYTHING IS DECIDED, so a caller error stays a caller error rather than
	// being reported as a policy refusal.
	if len(positional) < 2 {
		fmt.Fprintln(stderr, "4genteam send: SESSION and MESSAGE, or ROOM SEAT and MESSAGE, are required")
		return clientcmd.ExitUsage
	}

	// THE INTENT IS PART OF THE SEND, and its DEFAULT IS DOCUMENTED AND PRINTED (the architect's ruling,
	// 2026-10-15): `notice`, which authorizes under `collaborates_with`. It is not silently guessed - the
	// usage line names the default, and the DECISION LINE records the intent actually used - so a sender who
	// meant `task` and sent a `notice` is visible in the audit rather than invisible. The flag is not
	// required at once because every existing caller (the skills, the seats, the lead's loops) would break
	// the moment the gate turns on.
	intentWord := opts["intent"]
	if intentWord == "" {
		intentWord = string(commpolicy.IntentNotice)
	}
	intent, known := commpolicy.ParseIntent(intentWord)
	if !known {
		fmt.Fprintf(stderr, "4genteam send: unknown intent %q (one of %s); nothing was sent\n", intentWord, commpolicy.IntentNames)
		return clientcmd.ExitUsage
	}

	// `send SESSION MESSAGE`: the message goes through the runtime socket.
	if len(positional) == 2 {
		return sendToSession(positional[0], positional[1], opts, socket, intent, stdout, stderr)
	}
	room, seat := positional[0], positional[1]
	// THE TEXT GOES VERBATIM, with no "From NAME:" written into it: that prefix is a CLAIM inside the message,
	// and the sender a seat sees must come from the environment of the shell that ran the send (the runtime's
	// own envelope here, or the owner node's terminal on the --via-owner route).
	text := strings.Join(positional[2:], " ")
	if opts["via-owner"] == "true" {
		// THE NODE'S SIGNATURE IS THE ROUTE, so the node is the only caller allowed to ask for it: the
		// refusal is decided here, before a message file is written or a key is typed (ruling 2026-10-18).
		if refusal := viaOwnerRefusal(room, os.Getenv); refusal != "" {
			fmt.Fprintf(stderr, "4genteam send: %s; nothing was sent\n", refusal)
			return clientcmd.ExitRefused
		}
		return sendViaOwnerNode(ownerSendDepsFor(), room, seat, text, stdout, stderr)
	}

	// THE SEAT'S SESSION NAME IS READ, NEVER COMPOSED, from the room's runtime record: a name invented from
	// a convention cannot be split back into halves for a room whose name carries a dash.
	address, err := clientruntime.ResolveSeatAddressOn(socket, room, seat)
	if err != nil {
		return report(stderr, err)
	}
	// --raw means raw: no "From NAME:" prefix, because a caller who asked for the text exactly asked for the
	// text exactly.
	if opts["raw"] == "true" {
		text = strings.Join(positional[2:], " ")
	}
	// A PARKED SEAT IS NOT HANDED WORK, and here the seat key IS the caller's, so no lookup stands between
	// the park and the refusal (seatpark).
	if err := seatpark.Refuse(socket, room, seat); err != nil {
		return report(stderr, err)
	}
	// THE DECISION COMES BEFORE THE DELIVERY. On this shape the seat key the caller typed IS the key the
	// policy speaks in, so no lookup stands between them.
	handle, err := authorizeToSeat(socket, room, seat, intent)
	if err != nil {
		return report(stderr, err)
	}
	return runtimeSend(address.SessionName, text, opts, socket, handle, stdout, stderr)
}

// sendSocket is the socket this send is aimed at: the flag, else this shell's own resolution. It is named in
// the refusal as well, because a caller who pointed the verb at another daemon has to read WHICH daemon
// answered - and `4genteam send SESSION MESSAGE --socket PATH` is the only way to reach a daemon that does not
// own this machine's own socket (an isolated run, a second daemon on the same box).
func sendSocket(opts map[string]string) string {
	if explicit := strings.TrimSpace(opts["socket"]); explicit != "" {
		return explicit
	}
	return clientruntime.DefaultSocketPath()
}

// sendToSession delivers one message to one session through the runtime socket.
//
// THE NAME IS TRANSLATED FIRST. `<room>-<seat>@<room>` is the canonical form the help documents and every
// seat's own session carries, but the runtime's session for that seat is `agenthub--<room>--<seat>`. The
// room's runtime record resolves the pair. A pair no record lists is left ALONE, deliberately: the caller's
// own string then goes to the runtime as typed, and its refusal is the honest answer for a session that
// does not exist.
func sendToSession(session, text string, opts map[string]string, socket string, intent commpolicy.Intent, stdout, stderr io.Writer) int {
	// THE CANONICAL FORM CARRIES ITS OWN SPLIT, so the seat key is the caller's, not something to read back:
	// only the address is resolved through the seat's record.
	room, seat := "", ""
	if owner, key, ok := clientruntime.CanonicalRoomSeat(session); ok {
		if address, err := clientruntime.ResolveSeatAddressOn(socket, owner, key); err == nil {
			session, room, seat = address.SessionName, owner, key
		}
	}
	// A NATIVE SESSION NAME CARRIES NO SPLIT RULE THIS VERB MAY INVENT, so the daemon the delivery goes to is
	// asked which seat that session is - the room's links name seat keys, and a session that cannot be read
	// back to one cannot be authorized. It is refused in words rather than delivered on a guess.
	if room == "" {
		var ok bool
		room, seat, ok = recipientSeat(socket, session)
		if !ok {
			return report(stderr, &clientcmd.CodedError{
				Code: clientcmd.ExitUsage,
				Message: fmt.Sprintf("the runtime at %s lists no session %q, so the seat key a send is decided "+
					"by cannot be read for it; nothing was sent", socket, session),
			})
		}
	}
	// A PARKED SEAT IS NOT HANDED WORK, and it is asked BEFORE the policy decision: a park is a state of the
	// destination rather than a permission of the sender, so it writes no decision line - the record and the
	// rail row are where a reader finds it (seatpark).
	if err := seatpark.Refuse(socket, room, seat); err != nil {
		return report(stderr, err)
	}
	handle, err := authorizeToSeat(socket, room, seat, intent)
	if err != nil {
		return report(stderr, err)
	}
	return runtimeSend(session, text, opts, socket, handle, stdout, stderr)
}

// runtimeSend sends through the socket, wrapped in the From/To/Sent envelope unless --raw. `--json` prints
// the runtime's own answer document.
//
// IT ALSO CLOSES THE PAIR: the decision line is already on disk when this runs, and the outcome line is
// written here whatever the delivery did. That is the half the checker could never write, because the
// delivery was another process's.
func runtimeSend(session, text string, opts map[string]string, socket string, handle sendHandle, stdout, stderr io.Writer) int {
	raw, err := clientruntime.RuntimeSendTextOn(socket, session, text, clientruntime.SendOptions{
		Raw: opts["raw"] == "true",
	})
	if err != nil {
		// THE OUTCOME LINE CARRIES THE SAME THREE FACTS THE SENTENCE DOES: a frame the runner received and never
		// answered is unconfirmed, not failed - the text may be held for its next turn (task 83a39aa5).
		outcome := commpolicy.OutcomeDeliveryFailed
		if !nothingWasSent(err) {
			outcome = commpolicy.OutcomeDeliveryUnconfirmed
		}
		if auditErr := handle.outcome(outcome); auditErr != nil {
			fmt.Fprintf(stderr, "4genteam send: the outcome of the delivery could not be audited in %s: %v\n",
				handle.path, auditErr)
		}
		fmt.Fprintf(stderr, "4genteam send: %s\n", sendFailureSentence(socket, err))
		return clientcmd.ExitRemote
	}
	// THE OUTCOME LINE CARRIES THE RUNTIME'S OWN VERDICT, the same one the printed line reads: `delivered`
	// claims the text was written where the verdict says, so only `submitted` earns it. A relayed frame and an
	// answer with no verdict prove no submission, and audited as delivered they would contradict the line this
	// same call prints (task 83a39aa5).
	verdict, _ := clientruntime.SendVerdict(raw)
	outcome := commpolicy.OutcomeDeliveryUnconfirmed
	if verdict == clientruntime.VerdictSubmitted {
		outcome = commpolicy.OutcomeDelivered
	}
	// A delivery whose outcome cannot be recorded is reported as a failure even though the message arrived: an
	// unrecorded outcome is exactly what the pair exists to rule out.
	if auditErr := handle.outcome(outcome); auditErr != nil {
		fmt.Fprintf(stderr, "4genteam send: the outcome of the delivery could not be audited in %s: %v\n",
			handle.path, auditErr)
		return ExitFailed
	}
	if opts["json"] == "true" {
		_, _ = stdout.Write(append(raw, '\n'))
		return clientcmd.ExitOK
	}
	fmt.Fprint(stdout, clientruntime.RuntimeSendLine(session, raw))
	return clientcmd.ExitOK
}

// sendFailureSentence names the failure the delivery got. THREE facts, three sentences, because the delivery
// path is three-way and one sentence for all of them was false for two: a refusal means nothing was written
// anywhere; a runner the daemon could not hear from - and a socket that went quiet - means the frame reached
// the runner's mailbox, so the text may be held for its next turn and "nothing was sent" is a claim
// nobody measured. The U3 gate caught exactly that sentence against a delivery the runtime had HELD (task
// 83a39aa5).
func sendFailureSentence(socket string, err error) string {
	if nothingWasSent(err) {
		return fmt.Sprintf("the runtime at %s refused it: %v; nothing was sent", socket, err)
	}
	var refused *clientruntime.DaemonRefused
	if errors.As(err, &refused) && clientruntime.IsNoAnswer(refused.Reason) {
		// The daemon's own sentence already says why the wait elapsed and that the frame reached the mailbox,
		// so only the verdict is added: repeating the hold would read as two separate findings.
		return refused.Reason + "; the delivery is unconfirmed"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Sprintf("the runtime at %s did not answer within its %s call deadline (%v); the text may be "+
			"held for its next turn, so the delivery is unconfirmed", socket, clientruntime.CallDeadline, err)
	}
	return fmt.Sprintf("the runtime at %s answered the call with something this client cannot read as a "+
		"refusal (%v); the text may be held for its next turn, so the delivery is unconfirmed", socket, err)
}

// nothingWasSent reports whether the failure guarantees the request never left this client: the daemon
// refused it (its own refusal, or an older daemon that could not frame it), or no daemon was reachable at
// the socket. Every other failure leaves the request between this client and the runner, where only the
// runtime can say whether the text was held - and an unreadable answer is that same unknown, so it may not
// be reported as a refusal.
func nothingWasSent(err error) bool {
	var refused *clientruntime.DaemonRefused
	if errors.As(err, &refused) {
		return !clientruntime.IsNoAnswer(refused.Reason)
	}
	var tooOld *clientruntime.RuntimeTooOld
	if errors.As(err, &tooOld) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}
