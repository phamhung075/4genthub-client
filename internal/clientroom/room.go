// Package clientroom is the `room` verb group: one room's lifecycle, over the runtime's own IPC.
//
// `room status|stop|reseat|compact` is the client half of the native cutover. Each sub-verb asks the runtime
// daemon for something the wire already carries - GetRoomStatus, StopRoom, StopSeat and RestartSeat,
// ForceCompact - so this package is a FRAME over clientruntime.Client rather than a second implementation of
// any of it. Nothing here shells out to tmux, and nothing here resolves the rig orchestrator's paths.
//
// THREE RULES ARE LOAD-BEARING, and each one is the reason a line of this file looks the way it does:
//
//   - NO SUB-VERB RESOLVES THE PAIR IMPLICITLY. The socket decides which daemon is asked and the state dir
//     decides which rooms that daemon can see (DESIGN-room-scoped-up-and-siblings §2); with both flags
//     absent they come from this machine's own environment, where the caller's HOME decides them. So every
//     run PRINTS `<room>@<state-dir>` and the socket it used, and the room's own lines are read against it.
//   - `stop` IS NOT "STOP SUPERVISING". It is StopRoom and nothing else: that room's own
//     `agenthub--<room>--<seat>` sessions, and nothing under the rig orchestrator's state home - never agent, never its children
//     (compact-run, watchdog, sync messages), never the bridge, never another room (design §3).
//   - A SUB-VERB THAT DID NOT DO THE THING SAYS SO. `compact` refuses a seat that is not running BY NAME
//     instead of printing a success line over it, and a runtime that refuses the compaction is reported in
//     its own words; a room-scoped compact over a plain-omp pane cannot work at all (there is no
//     `omp --mode rpc` child to inject into), and the honest refusal is the design (§3, F5).
package clientroom

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// Commands is what the dispatcher registers from this package: one verb, `room`, whose Run switches over
// the four sub-verbs - the shape clientseat and clientteam already register, so the dispatcher's own list
// gains one line rather than four.
func Commands() []clientcmd.Command { return []clientcmd.Command{RoomCommand{}} }

// RoomCommand is the `room` verb group. Exported because the dispatcher registers the value by name and a
// reader of cmd/4genteam/main.go should be able to follow it to the type without a grep.
type RoomCommand struct{}

func (RoomCommand) Name() string { return "room" }
func (RoomCommand) Summary() string {
	return "one room's lifecycle over the runtime's own socket: status, stop, reseat, compact"
}

func (RoomCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "4genteam room: pick a verb: status, stop, reseat, compact")
		return clientcmd.ExitRoomRefused
	}
	if args[0] == "-h" || args[0] == "--help" {
		printUsage(stdout, "")
		return clientcmd.ExitOK
	}
	switch args[0] {
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "stop":
		return runStop(args[1:], stdout, stderr)
	case "reseat":
		return runReseat(args[1:], stdout, stderr)
	case "compact":
		return runCompact(args[1:], stdout, stderr)
	case "queue":
		return runQueue(args[1:], stdout, stderr)
	case "report":
		return runReport(args[1:], stdout, stderr)
	case "metrics":
		return runMetrics(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "4genteam room: unknown verb %q; the verbs are status, stop, reseat, compact\n", args[0])
	return clientcmd.ExitRoomRefused
}

const groupUsage = "usage: 4genteam room (status ROOM | stop ROOM | reseat ROOM SEAT | compact ROOM | report ROOM | metrics ROOM) [--state-dir DIR] [--socket PATH]"

// subUsages is one line per sub-verb, so a usage error prints the shape of the verb the caller actually
// typed instead of the whole group's grammar.
var subUsages = map[string]string{
	"status":  "usage: 4genteam room status ROOM [--state-dir DIR] [--socket PATH]",
	"stop":    "usage: 4genteam room stop ROOM [--state-dir DIR] [--socket PATH]",
	"reseat":  "usage: 4genteam room reseat ROOM SEAT [--state-dir DIR] [--socket PATH]",
	"compact": "usage: 4genteam room compact ROOM [--state-dir DIR] [--socket PATH]",
	"report":  reportUsage,
	"metrics": metricsUsage,
}

func subUsage(verb string) string {
	if line, ok := subUsages[verb]; ok {
		return line
	}
	return groupUsage
}

// printUsage prints the group's shape, or one sub-verb's, and then the rules that are the same for all four.
// The pair's resolution is spelled out with the variable each half falls back to, because "optional flag"
// is only honest when the reader is told what fills the gap.
func printUsage(out io.Writer, verb string) {
	fmt.Fprintln(out, subUsage(verb))
	fmt.Fprintln(out, "\nEvery sub-verb prints the pair it ran against - `room: <room>@<state-dir> socket: <socket>` -")
	fmt.Fprintln(out, "because neither is resolved implicitly. --state-dir is the flag, else ~/.config/4genthub,")
	fmt.Fprintln(out, "--socket is the flag, else $"+clientruntime.RuntimeSocketEnv+", else this")
	fmt.Fprintln(out, "machine's own.")
	fmt.Fprintln(out, "\nstatus reads GetRoomStatus and prints one line per seat: SEAT RUNTIME STATE SESSION, plus an")
	fmt.Fprintln(out, "ALERT segment when the runtime flagged the seat - alert=<kind> quiet_seconds=<secs|unknown>")
	fmt.Fprintln(out, "waiting_qitem_ids=<id,id|none>. A seat with no alert prints the four fields and nothing else, and")
	fmt.Fprintln(out, "`unknown` is a seat that published no freshness stamp: unknown quiet time is NOT zero seconds.")
	fmt.Fprintln(out, "stop sends StopRoom: exactly that room's own sessions. The room's directory (spec.json, seats/,")
	fmt.Fprintln(out, "logs/) is LEFT IN PLACE so the room can be started again; nothing outside the room is touched.")
	fmt.Fprintln(out, "reseat sends StopSeat then RestartSeat for one seat, in that order.")
	fmt.Fprintln(out, "compact asks the runtime to compact each RUNNING seat (the protocol's compact is session-scoped,")
	fmt.Fprintln(out, "so ForceCompact takes the session name). A seat that is not running is refused by name, and a")
	fmt.Fprintln(out, "runtime refusal is reported in its own words; both exit 2, because a compact that did not happen")
	fmt.Fprintln(out, "is never reported as one.")
	fmt.Fprintln(out, "\nExit codes (design §2.5): 0 every seat answered; 2 the daemon answered but a seat would not run; 3 a")
	fmt.Fprintln(out, "usage error, or a room the daemon does not know.")
}

// invocation is one sub-verb's resolved arguments: the room as the caller named it, the seat for the one
// sub-verb that takes one, the state dir and the socket this run is bound to, and the client that speaks to
// that socket. A struct rather than four returns because the pair is PRINTED by every sub-verb and must be
// resolved by exactly one function - the client is carried so that the socket it dials and the socket that
// is printed cannot be two different answers.
type invocation struct {
	room     string
	seat     string
	stateDir string
	client   *clientruntime.Client
}

// printPair prints the pair this run was bound to, with the room named `<room>@<state-dir>`. One room name
// can exist in many state dirs and the session names carry no state-dir component, so the pair is what
// tells two rooms of the same name apart - which is why the header is a rule rather than a nicety.
func (inv invocation) printPair(stdout io.Writer) {
	fmt.Fprintf(stdout, "room: %s@%s socket: %s\n", inv.room, inv.stateDir, inv.client.SocketPath)
}

// parseInvocation reads one sub-verb's flags and positionals and resolves the pair. The three results are
// clientseat's parseOptions' shape: the invocation, the code the caller returns when ok is false (0 when the
// flag was --help, which every sub-verb answers, and ExitRoomRefused for a bad or missing argument), and ok.
//
// `positionals` is how many bare words the sub-verb takes - one for status/stop/compact, two for reseat.
func parseInvocation(verb string, args []string, positionals int, stdout, stderr io.Writer) (invocation, int, bool) {
	if clientcmd.HelpRequested(args) {
		printUsage(stdout, verb)
		return invocation{}, clientcmd.ExitOK, false
	}
	flags := flag.NewFlagSet("room "+verb, flag.ContinueOnError)
	flags.SetOutput(stderr)
	stateDir := flags.String("state-dir", "", "where the runtime keeps its state: the flag, else ~/.config/4genthub")
	socket := flags.String("socket", "", "the runtime daemon's Unix socket: the flag, else $"+clientruntime.RuntimeSocketEnv+", else this machine's own")
	flagArgs, rest := splitFlagsAndPositionals(args)
	if err := flags.Parse(flagArgs); err != nil {
		return invocation{}, clientcmd.ExitRoomRefused, false
	}
	if len(rest) != positionals {
		fmt.Fprintf(stderr, "4genteam room %s: %s. %s\n", verb, argumentRefusal(rest, positionals), subUsage(verb))
		return invocation{}, clientcmd.ExitRoomRefused, false
	}
	// The room is the FIRST positional and the seat, when the sub-verb takes one, the second - the order
	// the usage line documents, so it reads the same way in help and in an argument list.
	root, err := clientenv.ResolveStateRoot(*stateDir)
	if err != nil {
		// The refusal's OWN code is deliberately NOT returned. ResolveStateRoot answers ExitUsage (2)
		// because that is the wider CLI's caller-error code, and this group's contract numbers a bad
		// invocation 3 (§2.5) - returning 2 here would tell an operator the daemon had started and a seat
		// come up short, which is the opposite of what happened.
		fmt.Fprintf(stderr, "4genteam room %s: %v\n", verb, err)
		return invocation{}, clientcmd.ExitRoomRefused, false
	}
	inv := invocation{room: rest[0], stateDir: root, client: clientruntime.NewClient(*socket)}
	if positionals > 1 {
		inv.seat = rest[1]
	}
	return inv, clientcmd.ExitOK, true
}

// splitFlagsAndPositionals separates this group's flags from its positionals, and puts the positionals LAST:
// the flag package STOPS at the first bare word, so `room status ROOM --state-dir DIR` - the order the usage
// line documents, and the one a reader writes - would otherwise parse no flags at all and report the room's
// neighbour as an unexpected argument. clientlifecycle carries this same helper for this same reason, and
// clientpolicy's parser carries the warning; it is spelled out rather than rediscovered.
func splitFlagsAndPositionals(args []string) (flagArgs, positional []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !isFlag(arg) {
			positional = append(positional, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		// A flag that takes a value takes its VALUE with it, so `--state-dir DIR` is never read as a
		// positional named DIR. `--state-dir=DIR` carries its own value and needs no lookahead.
		if flagTakesValue(flagName(arg)) && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return flagArgs, positional
}

func isFlag(arg string) bool { return strings.HasPrefix(arg, "-") && arg != "-" }

// argumentRefusal is what a sub-verb says when its positionals are not what it takes: the argument NAMED as
// missing or as extra, because a bare usage line leaves a caller to diff their own command against it. The
// seat's absence is named apart from the room's: `reseat ROOM` is missing a SEAT, not a room.
func argumentRefusal(rest []string, positionals int) string {
	switch {
	case len(rest) > positionals:
		return fmt.Sprintf("unexpected argument %q", strings.Join(rest[positionals:], " "))
	case len(rest) == 0 && positionals > 1:
		return "a ROOM and a SEAT are required"
	case len(rest) == 0:
		return "a ROOM is required"
	default:
		return "a SEAT is required"
	}
}

// flagName is a flag's name without its dashes and without any =value; "" for something that is not a flag.
func flagName(arg string) string {
	if !isFlag(arg) {
		return ""
	}
	name := strings.TrimLeft(arg, "-")
	if at := strings.Index(name, "="); at >= 0 {
		name = name[:at]
	}
	return name
}

// flagTakesValue is this group's flag list. It has exactly two flags and both take a value, so a bare word
// after one of them is that flag's value rather than a room or a seat.
func flagTakesValue(name string) bool { return name == "state-dir" || name == "socket" }

// SessionName is the session name of one seat: `agenthub--<room>--<seat>`.
//
// It names a session because ForceCompact is session-scoped; the format lives in
// clientruntime.SeatSessionName, and the Rust side's `Supervisor::session_name`
// (rust/agenthub-runtime/src/supervisor/mod.rs) spells the same string.
//
// The design's other rule for these names is the load-bearing one: a session is targeted by this EXACT name
// and never by a prefix (design §5.2). Nothing in this package ever builds `agenthub--<room>` alone.
func SessionName(room, seat string) string { return clientruntime.SeatSessionName(room, seat) }

// seatRunning reads the runtime's state string case-insensitively: "Running" and "running" are the same
// fact, and the runtime's own enum is the only thing that decides it - which is also why the state a report
// prints is the daemon's own word rather than this client's.
func seatRunning(seat clientruntime.SeatStatus) bool {
	return strings.EqualFold(strings.TrimSpace(seat.State), "running")
}

// seatRow is the ONE rendering of a seat's line: the four fields the usage line documents - SEAT RUNTIME
// STATE SESSION - and then the runtime's advisory when there is one. `status` and `reseat` each print a
// seat's row out of a daemon-returned SeatStatus, so both come through here rather than each keeping its own
// format string: an alert that showed up on one verb and not the other would be the field carried but not
// rendered, one verb at a time.
func seatRow(seat clientruntime.SeatStatus) string {
	row := fmt.Sprintf("%s %s %s %s", seat.Seat, seat.Runtime, seat.State, seat.SessionName)
	return row + alertNote(seat.Alert)
}

// alertNote is the trailing segment of a seat's row for the runtime's alert, or the empty string when the
// seat carries none - an ABSENT `alert` key is no alert, never an alert of zeroes.
//
// TWO CASES THAT MUST NOT READ THE SAME, and they are the reason this is a function rather than a wider
// format string: `quiet_seconds=0` is a seat whose own stamp is from just now (it acted a moment ago), and an
// ABSENT quiet_seconds is a seat that published no stamp at all, so how long it has been quiet is UNKNOWN.
// Rendering the second as a number would tell a reader the exact opposite of the truth - "this seat just did
// something" about the seat the alert exists to raise - so the unknown case says `unknown` where the number
// would be. The waiting ids are spelled out rather than counted, because the ids are what a reader needs to
// go and look at the work; an alert with an empty list says `none` rather than leaving a dangling key.
func alertNote(alert *clientruntime.SeatAlert) string {
	if alert == nil {
		return ""
	}
	quiet := "unknown"
	if alert.QuietSeconds != nil {
		quiet = strconv.FormatInt(*alert.QuietSeconds, 10)
	}
	waiting := "none"
	if len(alert.WaitingQitemIDs) > 0 {
		waiting = strings.Join(alert.WaitingQitemIDs, ",")
	}
	return fmt.Sprintf(" alert=%s quiet_seconds=%s waiting_qitem_ids=%s", alert.Kind, quiet, waiting)
}

// reportDaemon is the one rendering of a daemon-side failure: the daemon's own words, naming the sub-verb,
// and the code the room contract gives any failure that is not a partial start.
//
// ExitRemote is deliberately NOT used. A room the daemon does not know and a daemon that cannot be reached
// at all answer the same question from the operator's side - "this room was not read" - and the pair printed
// above says which daemon was asked. The daemon's message is printed verbatim, including its own path or
// syscall text, because a paraphrase of `cannot read room spec` is where a real cause gets lost.
func reportDaemon(stderr io.Writer, verb string, err error) int {
	fmt.Fprintf(stderr, "4genteam room %s: %v\n", verb, err)
	return clientcmd.ExitRoomRefused
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	inv, code, ok := parseInvocation("status", args, 1, stdout, stderr)
	if !ok {
		return code
	}
	inv.printPair(stdout)
	seats, err := inv.client.GetRoomStatus(inv.room)
	if err != nil {
		return reportDaemon(stderr, "status", err)
	}
	// One line per seat, and the SEAT's own rows in the daemon's order: the state word and the session name
	// are the daemon's, not this client's, so a reader can compare this output with the room's own reading.
	// A seat the runtime flagged carries its alert as the row's last segment (see seatRow/alertNote).
	for _, seat := range seats {
		fmt.Fprintln(stdout, seatRow(seat))
	}
	return clientcmd.ExitOK
}

// runStop sends StopRoom and NOTHING ELSE.
//
// One request is the whole contract, and the reason is the must-not list (design §3): StopRoom kills exactly
// the sessions of the room's own spec.json - `agenthub--<room>--<seat>`, one by one - so this verb must not
// reach for tmux itself, must not stop agent or any of its children, and must not touch the bridge or
// another room. the rig orchestrator's state home is never a path in this function's world at all.
//
// The room's directory is left in place and the report says so: spec.json, seats/ and logs/ are what makes
// the room startable again, and deleting them is a separate verb's business, not a stop's.
func runStop(args []string, stdout, stderr io.Writer) int {
	inv, code, ok := parseInvocation("stop", args, 1, stdout, stderr)
	if !ok {
		return code
	}
	inv.printPair(stdout)
	// A stopped room is one that is no longer meant to run: leave the list first, so the restart authority
	// never brings a safeguard back for it, even when the runtime cannot be reached to stop the seats.
	//
	// AND THE LIST IS NOT REWRITTEN WHEN IT COULD NOT BE READ: writing back the empty state would drop every
	// OTHER room from the rooms meant to run - agent would stop restarting them - and stopping this room with
	// its row still unreadable would have agent bring it straight back. Both are refusals, and the message
	// names which one happened.
	state, err := clientservices.ReadAgentState()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room stop: the rooms meant to run could not be read, so %s was NOT dropped from them and was not stopped: %v\n", inv.room, err)
		return clientcmd.ExitRoomRefused
	}
	if err := clientservices.WriteAgentState(clientservices.RemoveRig(state, inv.room)); err != nil {
		fmt.Fprintf(stderr, "4genteam room stop: could not drop %s from the rooms meant to run: %v\n", inv.room, err)
		return clientcmd.ExitRoomRefused
	}
	if err := inv.client.StopRoom(inv.room); err != nil {
		return reportDaemon(stderr, "stop", err)
	}
	fmt.Fprintf(stdout, "room %s: stopped, and no longer meant to run\n", inv.room)
	fmt.Fprintf(stdout, "the room's directory was left in place under %s: spec.json, seats/ and logs/ are still there, so the room can be started again\n", inv.stateDir)
	return clientcmd.ExitOK
}

// runReseat sends StopSeat and then RestartSeat, IN THAT ORDER.
//
// The order is not interchangeable with the single RestartSeat call, even though the daemon's restart_seat
// stops the seat itself: a stop that fails must be reported as a stop that failed, and a restart that does
// not come back must be reported as a seat that is not running - two different sentences for two different
// causes, and the second one is why the returned SeatStatus is read rather than assumed.
//
// A seat that is not running afterwards exits ExitPartialStart (2) with the seat's own state named: the
// daemon answered, and the room came up short of what was asked. Exit 0 there would be the "success over a
// no-op" this rig has already paid for.
func runReseat(args []string, stdout, stderr io.Writer) int {
	inv, code, ok := parseInvocation("reseat", args, 2, stdout, stderr)
	if !ok {
		return code
	}
	inv.printPair(stdout)
	if err := inv.client.StopSeat(inv.room, inv.seat); err != nil {
		return reportDaemon(stderr, "reseat", err)
	}
	status, err := inv.client.RestartSeat(inv.room, inv.seat)
	if err != nil {
		return reportDaemon(stderr, "reseat", err)
	}
	if status == nil {
		fmt.Fprintf(stderr, "4genteam room reseat: the runtime answered RestartSeat for %s with no seat status, so this verb cannot say the seat is running\n", inv.seat)
		return clientcmd.ExitPartialStart
	}
	fmt.Fprintln(stdout, seatRow(*status))
	if !seatRunning(*status) {
		fmt.Fprintf(stderr, "4genteam room reseat: %s came back %q, not running\n", inv.seat, status.State)
		return clientcmd.ExitPartialStart
	}
	return clientcmd.ExitOK
}

// runCompact asks the runtime to compact every RUNNING seat of the room, and refuses the rest by name.
//
// The room's seats are read FIRST because the running set is what this verb decides on: compact is
// session-scoped on the wire (ForceCompact{session}), and a seat this verb did not see is a seat it cannot
// honestly claim to have compacted.
//
// THREE THINGS IT MUST NOT DO, each of them a lie this rig has already paid for:
//   - print a success line over a seat that is not running: a stopped seat has no session to inject into,
//     nothing is asked for it, and the refusal names it so a reader knows which seat was skipped;
//   - flatten a runtime refusal into "ok": the runtime refuses when it finds no `omp --mode rpc` child under
//     a process carrying the session name, which is exactly a plain-omp pane (design §3, F5) - the refusal
//     is printed VERBATIM, because it names the capability that is missing;
//   - exit 0 over either. ExitPartialStart (2) is the code for both, and it is the design's own "the daemon
//     answered but a seat would not run" read from the operator's side: not everything asked for happened.
func runCompact(args []string, stdout, stderr io.Writer) int {
	inv, code, ok := parseInvocation("compact", args, 1, stdout, stderr)
	if !ok {
		return code
	}
	inv.printPair(stdout)
	seats, err := inv.client.GetRoomStatus(inv.room)
	if err != nil {
		return reportDaemon(stderr, "compact", err)
	}
	if len(seats) == 0 {
		// The daemon reads the room's spec.json, so an empty reading is a room that HAS no seats rather than
		// a room it does not know (that one is the error above). Said out loud, because silence plus exit 0
		// is indistinguishable from a room whose seats were all compacted.
		fmt.Fprintf(stdout, "room %s: the daemon reports no seats, so there was nothing to compact\n", inv.room)
		return clientcmd.ExitOK
	}
	verdict := clientcmd.ExitOK
	for _, seat := range seats {
		if !seatRunning(seat) {
			fmt.Fprintf(stderr, "4genteam room compact: %s is %q, not running: a stopped seat cannot be compacted and nothing was asked for it\n", seat.Seat, seat.State)
			verdict = clientcmd.ExitPartialStart
			continue
		}
		session := SessionName(inv.room, seat.Seat)
		answer, err := inv.client.ForceCompact(session)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam room compact: %s (%s): %v\n", seat.Seat, session, err)
			verdict = clientcmd.ExitPartialStart
			continue
		}
		fmt.Fprintf(stdout, "%s %s: %s\n", seat.Seat, session, answer)
	}
	return verdict
}
