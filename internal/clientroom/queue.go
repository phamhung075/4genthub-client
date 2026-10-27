// The `room queue` verb group: one room's durable rail, over the runtime's own IPC.
//
// `room queue list|show|create|update|claim|close` is the queue half of the native cutover. Each sub-verb is
// a FRAME over clientruntime.Client.Queue* - the six actions the daemon already answers - so nothing here
// opens a file, resolves a path of its own, or keeps a second copy of a row: the daemon owns each room's
// queue store and is its only writer, and the row a verb renders is the row the daemon re-read before it
// answered.
//
// FOUR RULES ARE LOAD-BEARING, and each is why a line of this file looks the way it does:
//
//   - THE PAIR IS RESOLVED ONCE, BY ITS SIBLINGS' FUNCTION. Every run prints `<room>@<state-dir>` and the
//     socket it used, and those two are resolved by room.go's own parseInvocation - this group re-emits the
//     two pair flags it reads and hands the positionals over, so the state dir's environment fallback and
//     the refusal that names it cannot become a second answer written here. ROOM is the first positional and
//     the ONE that defaults: a sub-verb one positional short takes that room from $AGENTHUB_ROOM (the one
//     reader of it is queueDefaultRoom), and the default is applied here so parseInvocation still sees the
//     room it documents.
//   - THE ACTOR IS THE SEAT'S OWN SESSION, OR THERE IS NO ACTOR. create, update, claim and close act as
//     $AGENTHUB_SESSION_NAME. When it is unset the verb REFUSES naming the variable: it never guesses and
//     never takes an actor from a flag, because a rail that records an actor nobody named is a rail nobody
//     can audit.
//   - A SUB-VERB THAT DID NOT DO THE THING SAYS SO. A refused operation and a room the daemon does not know
//     are told apart by the daemon's own sentence (the wire carries no code), and the two verbs whose
//     refusals this client can make without spending a round trip - close's --reason, the list filter's
//     --state - are refused HERE, so a typo costs nothing.
//   - NOTHING IS RENDERED THAT WAS NOT MEASURED. An empty rail says so in words rather than printing
//     nothing (silence plus exit 0 is indistinguishable from a read that never happened), and a write that
//     appends a transition prints it: the row line cannot carry a note, and the rail's own record is the
//     only surface a note is legible on.

package clientroom

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// sessionEnv is the variable a seat's own session name arrives in: the runtime launches every seat with it.
// It is read here rather than through a helper because what this group
// needs is not "a session if there is one" but the ONE session that may act: a fallback to another variable
// would let a sub-verb write an actor the seat never named.
const sessionEnv = "AGENTHUB_SESSION_NAME"

// roomEnv is the variable ROOM defaults to. The runtime launches every seat with it, so a seat asking about
// its own room - the overwhelmingly common call: `queue show <qitem>` - need not name it, and the 147 failed
// calls measured on 2026-10-18 (every one an id typed where the room belonged) become the working form.
const roomEnv = "AGENTHUB_ROOM"

// queueGroupUsage is the group's grammar. It is nine characters longer than one line because a sub-verb's
// own flags are the difference between two of them, and a caller reading help should not have to diff six
// lines to find the verb they typed.
const queueGroupUsage = `usage: 4genteam room queue (list [ROOM] | show [ROOM] QITEM_ID | create [ROOM] --to SESSION --summary TEXT |
                             update [ROOM] QITEM_ID | claim [ROOM] QITEM_ID | close [ROOM] QITEM_ID --reason R)
                             [--state-root DIR] [--socket PATH]`

// queueSubUsages is one line per sub-verb, so a usage error prints the shape of the verb the caller
// actually typed instead of the whole group's grammar - room.go's own rule for the four verbs above it.
var queueSubUsages = map[string]string{
	"list":   "usage: 4genteam room queue list [ROOM] [--destination SESSION] [--source SESSION] [--state S[,S]] [--active-only] [--limit N] [--state-root DIR] [--socket PATH]",
	"show":   "usage: 4genteam room queue show [ROOM] QITEM_ID [--state-root DIR] [--socket PATH]",
	"create": "usage: 4genteam room queue create [ROOM] --to SESSION --summary TEXT<=120 [--body TEXT<=600] [--priority low|normal|high] [--reply-to QITEM_ID] [--tag T] [--questions-file PATH] [--state-root DIR] [--socket PATH]",
	"update": "usage: 4genteam room queue update [ROOM] QITEM_ID [--state S] [--note TEXT] [--summary TEXT] [--closure-reason R] [--closure-target SESSION] [--blocked-on QITEM_ID] [--wake-after SECONDS] [--state-root DIR] [--socket PATH]",
	"claim":  "usage: 4genteam room queue claim [ROOM] QITEM_ID [--state-root DIR] [--socket PATH]",
	"close":  "usage: 4genteam room queue close [ROOM] QITEM_ID --reason R [--target SESSION] [--note TEXT] [--state-root DIR] [--socket PATH]",
}

func queueSubUsage(sub string) string {
	if line, ok := queueSubUsages[sub]; ok {
		return line
	}
	return queueGroupUsage
}

// runQueue is the group's dispatcher, the shape RoomCommand.Run has: one word chooses the sub-verb, and the
// word this group does not read is refused BY NAME with the six it does.
func runQueue(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "4genteam room queue: pick a verb: list, show, create, update, claim, close")
		return clientcmd.ExitRoomRefused
	}
	if clientcmd.HelpRequested(args[:1]) {
		printQueueUsage(stdout, "")
		return clientcmd.ExitOK
	}
	switch args[0] {
	case "list":
		return runQueueList(args[1:], stdout, stderr)
	case "show":
		return runQueueShow(args[1:], stdout, stderr)
	case "create":
		return runQueueCreate(args[1:], stdout, stderr)
	case "update":
		return runQueueUpdate(args[1:], stdout, stderr)
	case "claim":
		return runQueueClaim(args[1:], stdout, stderr)
	case "close":
		return runQueueClose(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "4genteam room queue: unknown verb %q; the verbs are list, show, create, update, claim, close\n", args[0])
	return clientcmd.ExitRoomRefused
}

// printQueueUsage prints one sub-verb's shape (or the whole group's) and then the rules that are the same
// for all six. The pair's resolution is spelled out with the variable each half falls back to, and the
// actor's rule is spelled out with the variable it is read from, because "optional flag" and "the seat's own
// shell" are only honest when the reader is told what fills the gap.
func printQueueUsage(out io.Writer, sub string) {
	fmt.Fprintln(out, queueSubUsage(sub))
	fmt.Fprintln(out, "\nThe pair is printed by every sub-verb - `room: <room>@<state-dir> socket: <socket>`. ROOM is the first")
	fmt.Fprintln(out, "positional and the only one that defaults: leave it out and it is $"+roomEnv+", the room the runtime")
	fmt.Fprintln(out, "launched this seat into, so `queue show <qitem>` reads this seat's own rail. --state-root is the flag,")
	fmt.Fprintln(out, "else ~/.config/4genthub; --socket is the flag, else $"+clientruntime.RuntimeSocketEnv+", else this machine's own.")
	fmt.Fprintln(out, "\nThe daemon owns the room's rail and is its only writer: every verb renders the rows the daemon")
	fmt.Fprintln(out, "re-read from its store, so the state printed is the state the next read shows. list prints")
	fmt.Fprintln(out, "<qitem_id> <state> <priority|-> <summary>, one row per line; show prints a row's own fields and then")
	fmt.Fprintln(out, "its transitions oldest to newest.")
	fmt.Fprintln(out, "\nRows are typed; the daemon refuses a create that breaks a rule and names the rule and its cap: priority is")
	fmt.Fprintln(out, "low, normal or high (default normal); high requires --tag prod-break; summary <= 120 chars, body <= 600;")
	fmt.Fprintln(out, "each body line's value is capped at "+queueBodyFieldCaps()+", and a create over one or")
	fmt.Fprintln(out, "more of them is refused once, naming every over-cap line;")
	fmt.Fprintln(out, "tags are lowercase kebab from "+strings.Join(clientruntime.QueueTags, ", ")+".")
	fmt.Fprintln(out, "A create's --to is a session name, a seat key, or <seat>@<room>: all three are stored as the session")
	fmt.Fprintln(out, "name the nudge pass matches, and a value naming no seat of the room is refused before anything is written.")
	fmt.Fprintln(out, "A repeated identical list by the same session within 5 minutes with no new row is answered")
	fmt.Fprintln(out, "`no change since <qitem>` (exit 2); wake on pushes instead of polling.")
	fmt.Fprintln(out, "\ncreate, update, claim and close act as THE SEAT'S OWN SESSION, read from $"+sessionEnv+". When that variable")
	fmt.Fprintln(out, "is unset the verb refuses and asks nothing of the daemon: the actor is never guessed and never taken")
	fmt.Fprintln(out, "from a flag. A write prints the row it was answered with and the transition it appended, because a")
	fmt.Fprintln(out, "note does not ride the row - the rail's transition log is the only surface it is legible on.")
	fmt.Fprintln(out, "\nExit codes (design §2.5): 0 the daemon answered; 2 an operation the daemon refused (a row that is not")
	fmt.Fprintln(out, "pending, an unknown id, a value the store will not take); 3 a usage error, a missing actor, or a room")
	fmt.Fprintln(out, "the daemon does not know.")
}

// queueBodyFieldCaps renders the ruled per-field caps as the help names them, from clientruntime's own
// table: a help that carried its own copy of the numbers would drift from the refusal the client writes.
func queueBodyFieldCaps() string {
	caps := clientruntime.BodyFieldCaps()
	parts := make([]string, 0, len(caps))
	for _, field := range caps {
		parts = append(parts, fmt.Sprintf("%s <= %d", field.Key, field.Cap))
	}
	return strings.Join(parts, ", ")
}

// queueArgs is one queue sub-verb's resolved arguments: the pair every sub-verb prints (room, state dir,
// socket and the client that speaks to that socket - one resolution, room.go's), the sub-verb's own options
// exactly as they were written, and the positionals.
type queueArgs struct {
	invocation
	opts       map[string]string
	positional []string
}

// qitemID is the second positional, which every sub-verb but list and create takes. It is called only after
// parseQueue has counted the positionals against the sub-verb's own list, so the index is always in range.
func (a queueArgs) qitemID() string { return a.positional[1] }

// option is one of the sub-verb's own flags: present WITH A VALUE, present with nothing, and absent are
// three different answers ("set the state to X", "set the state to the empty string", "do not move the
// state"), so presence is returned rather than a default standing in for it.
func (a queueArgs) option(name string) (string, bool) {
	value, ok := a.opts[name]
	return value, ok
}

// queueFlagSpecs is one sub-verb's flag table: its own flags, plus the socket. The ROOT is not here because
// it is taken off the argument list BEFORE this table is consulted (clientenv.StateRootFromFlags), which is
// also where the runtime's spelling is refused with the ruling's own code instead of this group's 3.
func queueFlagSpecs(own ...clientruntime.FlagSpec) []clientruntime.FlagSpec {
	specs := []clientruntime.FlagSpec{{Name: "socket", Value: true}}
	return append(specs, own...)
}

// parseQueue reads one queue sub-verb's arguments and resolves the pair. `positionals` are the names the
// sub-verb takes, in order, and it is used both to count what arrived and to name what is missing.
//
// The pair is resolved by room.go's parseInvocation and NOT by a second copy of it: the two pair flags are
// re-emitted with the values this sub-verb read and handed over with the positionals, so the room, the state
// dir, the socket, the environment fallbacks and the refusal that names them stay one function's answer.
func parseQueue(sub string, args []string, own []clientruntime.FlagSpec, positionals []string, stdout, stderr io.Writer) (queueArgs, int, bool) {
	if clientcmd.HelpRequested(args) {
		printQueueUsage(stdout, sub)
		return queueArgs{}, clientcmd.ExitOK, false
	}
	rootFlag, args, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam room queue %s: %v\n", sub, rootErr)
		return queueArgs{}, clientcmd.CodeOf(rootErr, clientcmd.ExitRoomRefused), false
	}
	opts, rest, err := clientruntime.SplitFlags(args, queueFlagSpecs(own...))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room queue %s: %v. %s\n", sub, err, queueSubUsage(sub))
		return queueArgs{}, clientcmd.ExitRoomRefused, false
	}
	// ROOM DEFAULTS, AND THE COUNT IS WHAT DECIDES: one positional short of the sub-verb's own list means the
	// caller left ROOM out, so it is $AGENTHUB_ROOM. Counting rather than inspecting the words keeps this
	// checkable - `queue show <qitem>` and `queue show <room> <qitem>` cannot be confused, because the number
	// of positionals names which form was typed - and a room left out with no variable to fill it is refused
	// HERE, before anything is asked of the daemon.
	if len(rest) == len(positionals)-1 {
		room := queueDefaultRoom(sub, stderr)
		if room == "" {
			return queueArgs{}, clientcmd.ExitRoomRefused, false
		}
		rest = append([]string{room}, rest...)
	}
	if len(rest) != len(positionals) {
		fmt.Fprintf(stderr, "4genteam room queue %s: %s. %s\n", sub, queueArgumentRefusal(rest, positionals), queueSubUsage(sub))
		return queueArgs{}, clientcmd.ExitRoomRefused, false
	}
	pairArgs := append(append(make([]string, 0, len(rest)+4), rest...),
		"--state-root", rootFlag, "--socket", opts["socket"])
	inv, code, ok := parseInvocation("queue "+sub, pairArgs, len(positionals), stdout, stderr)
	if !ok {
		return queueArgs{}, code, false
	}
	return queueArgs{invocation: inv, opts: opts, positional: rest}, clientcmd.ExitOK, true
}

// queueDefaultRoom is the room a sub-verb one positional short uses, or a refusal naming the variable that
// would have supplied it. It never guesses a room from a word already on the line: an id typed where the
// ROOM belongs is not a room, and treating it as one would ask the daemon about a room that does not exist.
func queueDefaultRoom(sub string, stderr io.Writer) string {
	if room := strings.TrimSpace(os.Getenv(roomEnv)); room != "" {
		return room
	}
	fmt.Fprintf(stderr, "4genteam room queue %s: ROOM was left out and $%s is unset, so there is no room to default to; name the room, or run this from a seat the runtime launched. %s\n", sub, roomEnv, queueSubUsage(sub))
	return ""
}

// queueArgumentRefusal names the argument a queue sub-verb was not handed, or the ones it was handed too
// many of. ROOM is the first positional and the only one with a default, so a count that is short by one or
// more is a MISSING TAIL - the id, then the flag - rather than a missing room: telling a caller who typed
// `queue close <room>` that a ROOM is required would send them to the form they already used.
func queueArgumentRefusal(rest []string, wanted []string) string {
	switch {
	case len(rest) > len(wanted):
		return fmt.Sprintf("unexpected argument %q", strings.Join(rest[len(wanted):], " "))
	case len(rest) < len(wanted):
		return fmt.Sprintf("a %s is required", wanted[len(wanted)-1])
	default:
		return ""
	}
}

// queueSession is the acting session, or a refusal that names the variable it should have come from. Every
// caller of it writes to the rail, so an absent actor is fatal rather than a default.
func queueSession(sub string, stderr io.Writer) (string, bool) {
	actor := strings.TrimSpace(os.Getenv(sessionEnv))
	if actor == "" {
		fmt.Fprintf(stderr, "4genteam room queue %s: $%s is unset, so this verb cannot name the session that moves the row; the rail records who acted and this client never guesses an actor or takes one from a flag. Nothing was asked of the daemon.\n", sub, sessionEnv)
		return "", false
	}
	return actor, true
}

// queueRequired is a flag the sub-verb cannot run without. `why` is the reason it is required rather than a
// restatement of its name, because a caller who left `--to` off wants to know what --to is FOR.
func queueRequired(run queueArgs, sub, flag, why string, stderr io.Writer) (string, bool) {
	value, present := run.option(flag)
	if !present || strings.TrimSpace(value) == "" {
		fmt.Fprintf(stderr, "4genteam room queue %s: --%s is required: %s. %s\n", sub, flag, why, queueSubUsage(sub))
		return "", false
	}
	return value, true
}

// queueWholeNumber reads a flag whose value must be a whole number. A value the flag could not carry is
// refused the same way a missing positional is: the caller is told which flag, and nothing is asked of the
// daemon.
func queueWholeNumber(run queueArgs, sub, flag string, stderr io.Writer) (int64, int, bool) {
	raw, _ := run.option(flag)
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room queue %s: --%s takes a whole number, got %q. %s\n", sub, flag, raw, queueSubUsage(sub))
		return 0, clientcmd.ExitRoomRefused, false
	}
	return n, clientcmd.ExitOK, true
}

// queueCommaList splits a flag's comma-separated value. An empty member is refused rather than dropped:
// `--state pending,` is a typo, and a list quietly shorter than the one the caller wrote is the same class
// of wrong answer as a filter that matches nothing.
func queueCommaList(raw string) ([]string, bool) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false
		}
		out = append(out, part)
	}
	return out, true
}

// queueContains is membership in one of clientruntime's exported sets - the daemon's own lists, so a check
// made here cannot drift into a second opinion about which states or reasons exist.
func queueContains(allowed []string, value string) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

// queueUnknownRoomPrefix is how the daemon words the one ROOM-LEVEL failure a queue verb can be handed:
// `no such room '<room>': <dir> is not a room directory` (rust/agenthub-runtime/src/supervisor/mod.rs,
// queue_store). It is matched on the opening words because THE WIRE CARRIES NO CODE - IpcResponse is
// {ok, error, data} and a refusal is the store's own sentence - so this sentence is the only signal that
// separates a room the daemon does not know from an operation the daemon refused.
const queueUnknownRoomPrefix = "no such room "

// queueDaemonRefusal renders a daemon-side failure and picks the code the room contract gives it. The
// daemon's message is printed VERBATIM in both branches: it names the value, the id or the directory at
// fault, and a paraphrase of `unknown state 'pendig'; valid states: ...` is where the real cause gets lost.
//
//	3 - the daemon does not know this room: the same reading room.go gives a room that did not read, and
//	    the same helper, so this group has one voice for it rather than two.
//	2 - the daemon ANSWERED and refused the operation: a row that is not pending, an unknown qitem_id, a
//	    state or reason the store will not take. The daemon did its part and the ask did not happen, which
//	    is exactly what 2 is for (§2.5); 0 here would be the "success over a no-op" this room has paid for.
func queueDaemonRefusal(stderr io.Writer, sub string, err error) int {
	if strings.HasPrefix(err.Error(), queueUnknownRoomPrefix) {
		return reportDaemon(stderr, "queue "+sub, err)
	}
	fmt.Fprintf(stderr, "4genteam room queue %s: %v\n", sub, err)
	return clientcmd.ExitPartialStart
}

// orDash is a value's rendering when the empty string is a fact to be shown rather than a blank: a row with
// no priority, no tier or no body prints `-`, so a field that is absent cannot be read as one that was
// dropped from the rendering.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func dashPointer(value *string) string {
	if value == nil {
		return "-"
	}
	return orDash(*value)
}

func dashWholeNumber(value *int64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatInt(*value, 10)
}

// queueRowLine is one row as list and the four write verbs render it: `<qitem_id> <state> <priority|-> <summary>`.
// One form, one place: a row the client printed after a write and the same row printed by a later list are
// then the same string, and a reader can compare them by eye.
func queueRowLine(row clientruntime.QueueRow) string {
	return fmt.Sprintf("%s %s %s %s", row.QitemID, row.State, orDash(row.Priority), row.Summary)
}

// queueTransitionLine is one transition as show and the write verbs render it:
// `<transition_id> <ts> <from_state> -> <state> by <actor> | <note>`. The actor is LABELLED because printed bare
// after the arrow it read as the row's new DESTINATION: on 2026-10-18 an owner row was believed routed away from
// its addressee three times - "blocked -> pending agenthub--room--reviewer" - while the destination field never
// changed and the line only named who closed the blocker. The note is the transition's own text, appended after
// the bar; a transition that carries no note still ends at the bar, so a note-less step is visibly note-less
// rather than looking like a row whose note was not printed.
func queueTransitionLine(transition clientruntime.QueueTransition) string {
	line := fmt.Sprintf("%d %s %s -> %s by %s |",
		transition.TransitionID, transition.Ts, dashPointer(transition.FromState),
		transition.State, orDash(transition.ActorSession))
	if transition.TransitionNote != "" {
		line += " " + transition.TransitionNote
	}
	return line
}

// queueLatestTransition is the transition a write just appended. The store answers a write with the row
// re-read from its store, transitions included and ordered oldest first, so the last one is this write's
// own - and for a handoff it is the SOURCE row's transition, because the successor's is on another row.
func queueLatestTransition(row clientruntime.QueueRow) (clientruntime.QueueTransition, bool) {
	if len(row.Transitions) == 0 {
		return clientruntime.QueueTransition{}, false
	}
	return row.Transitions[len(row.Transitions)-1], true
}

// printQueueWritten renders a row a write was answered with: the pair is already printed, then the row line
// every queue verb uses, then the transition this write appended.
//
// The transition is printed because THE ROW LINE CANNOT CARRY A NOTE - the note lands in the rail's
// append-only log, which is the only surface it is legible on - and a write that reports "the row is
// in-progress" over a note that went somewhere the caller cannot see is the measured failure this group's
// sibling shim exists to refuse.
func printQueueWritten(stdout io.Writer, row clientruntime.QueueRow) {
	fmt.Fprintln(stdout, queueRowLine(row))
	if transition, ok := queueLatestTransition(row); ok {
		fmt.Fprintln(stdout, queueTransitionLine(transition))
	}
}

// printQueueRow renders one row of the rail IN FULL: its own fields, one per line, then its transitions. It
// is what show prints, and it is deliberately wider than the row line - show is the verb a caller runs when
// the one-line form did not answer the question, so a field it dropped would send them to the store's file.
func printQueueRow(out io.Writer, row clientruntime.QueueRow) {
	fmt.Fprintf(out, "qitem_id: %s\n", row.QitemID)
	fmt.Fprintf(out, "state: %s\n", row.State)
	fmt.Fprintf(out, "priority: %s\n", orDash(row.Priority))
	fmt.Fprintf(out, "summary: %s\n", orDash(row.Summary))
	fmt.Fprintf(out, "body: %s\n", orDash(row.Body))
	fmt.Fprintf(out, "source_session: %s\n", orDash(row.SourceSession))
	fmt.Fprintf(out, "destination_session: %s\n", orDash(row.DestinationSession))
	fmt.Fprintf(out, "reply_to: %s\n", dashPointer(row.ReplyTo))
	fmt.Fprintf(out, "blocked_on: %s\n", dashPointer(row.BlockedOn))
	fmt.Fprintf(out, "wake_after_seconds: %s\n", dashWholeNumber(row.WakeAfterSeconds))
	fmt.Fprintf(out, "closure_reason: %s\n", dashPointer(row.ClosureReason))
	fmt.Fprintf(out, "closure_target: %s\n", dashPointer(row.ClosureTarget))
	fmt.Fprintf(out, "tier: %s\n", dashPointer(row.Tier))
	fmt.Fprintf(out, "tags: %s\n", orDash(strings.Join(row.Tags, ",")))
	fmt.Fprintf(out, "chain_of_record: %s\n", orDash(strings.Join(row.ChainOfRecord, ",")))
	fmt.Fprintf(out, "ts_created: %s\n", orDash(row.TsCreated))
	fmt.Fprintf(out, "ts_updated: %s\n", orDash(row.TsUpdated))
	fmt.Fprintf(out, "transitions: %d\n", len(row.Transitions))
	for _, transition := range row.Transitions {
		fmt.Fprintln(out, queueTransitionLine(transition))
	}
}

// runQueueList reads the room's rail through a filter, one row per line. An empty set is REPORTED, never
// implied: a list that printed nothing and exited 0 could not be told from a read that never happened, so
// the empty rail is a sentence - and a success, because an empty rail is a measurement the daemon made.
func runQueueList(args []string, stdout, stderr io.Writer) int {
	run, code, ok := parseQueue("list", args, []clientruntime.FlagSpec{
		{Name: "destination", Value: true},
		{Name: "source", Value: true},
		{Name: "state", Value: true},
		{Name: "active-only"},
		{Name: "limit", Value: true},
	}, []string{"ROOM"}, stdout, stderr)
	if !ok {
		return code
	}
	filter := clientruntime.QueueListFilter{}
	if destination, present := run.option("destination"); present {
		filter.DestinationSession = &destination
	}
	if source, present := run.option("source"); present {
		filter.SourceSession = &source
	}
	if raw, present := run.option("state"); present {
		states, ok := queueCommaList(raw)
		if !ok {
			fmt.Fprintf(stderr, "4genteam room queue list: --state needs one or more states, got %q; the states are %s. %s\n",
				raw, strings.Join(clientruntime.QueueStates, ", "), queueSubUsage("list"))
			return clientcmd.ExitRoomRefused
		}
		// THE FILTER'S STATES ARE CHECKED HERE, because a filter is not validated where it is applied: an
		// unknown state matches no row, so `--state pendig` would come back as "the rail is empty" - a
		// wrong answer dressed as a measurement. The set is clientruntime.QueueStates, the daemon's own
		// list, so this check cannot drift into a second opinion about which states exist.
		for _, state := range states {
			if !queueContains(clientruntime.QueueStates, state) {
				fmt.Fprintf(stderr, "4genteam room queue list: --state %q is not one of %s. %s\n",
					state, strings.Join(clientruntime.QueueStates, ", "), queueSubUsage("list"))
				return clientcmd.ExitRoomRefused
			}
		}
		filter.State = states
	}
	if _, present := run.option("active-only"); present {
		filter.ActiveOnly = true
	}
	if _, present := run.option("limit"); present {
		limit, code, ok := queueWholeNumber(run, "list", "limit", stderr)
		if !ok {
			return code
		}
		if limit < 0 {
			// A negative limit is refused rather than passed on: SQLite reads `LIMIT -1` as "no limit at
			// all", so a negative --limit would silently WIDEN the read the caller meant to narrow.
			fmt.Fprintf(stderr, "4genteam room queue list: --limit must not be negative (the store reads a negative limit as no limit at all). %s\n", queueSubUsage("list"))
			return clientcmd.ExitRoomRefused
		}
		filter.Limit = &limit
	}
	if session := strings.TrimSpace(os.Getenv(sessionEnv)); session != "" {
		filter.RequesterSession = &session
	}
	run.printPair(stdout)
	rows, err := run.client.QueueList(run.room, filter)
	if err != nil {
		return queueDaemonRefusal(stderr, "list", err)
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "room %s: the rail is empty\n", run.room)
		return clientcmd.ExitOK
	}
	for _, row := range rows {
		fmt.Fprintln(stdout, queueRowLine(row))
	}
	return clientcmd.ExitOK
}

// runQueueShow reads one row and prints it in full, transitions included - the surface a note is legible
// on, and the reason show exists beside list.
func runQueueShow(args []string, stdout, stderr io.Writer) int {
	run, code, ok := parseQueue("show", args, nil, []string{"ROOM", "QITEM_ID"}, stdout, stderr)
	if !ok {
		return code
	}
	run.printPair(stdout)
	row, err := run.client.QueueShow(run.room, run.qitemID())
	if err != nil {
		return queueDaemonRefusal(stderr, "show", err)
	}
	printQueueRow(stdout, *row)
	return clientcmd.ExitOK
}

// runQueueCreate appends a row to the rail. The SOURCE session is this seat's own, read from the
// environment: a create whose source were a flag would let a caller write a row in another seat's name, and
// the rail's whole value is that its record of who owes what is true.
func runQueueCreate(args []string, stdout, stderr io.Writer) int {
	run, code, ok := parseQueue("create", args, []clientruntime.FlagSpec{
		{Name: "to", Value: true},
		{Name: "summary", Value: true},
		{Name: "body", Value: true},
		{Name: "priority", Value: true},
		{Name: "reply-to", Value: true},
		{Name: "tag", Value: true},
		{Name: "questions-file", Value: true},
	}, []string{"ROOM"}, stdout, stderr)
	if !ok {
		return code
	}
	destination, ok := queueRequired(run, "create", "to", "a row is owed BY a session, and the store refuses a create without one", stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	summary, ok := queueRequired(run, "create", "summary", "a row's summary is the only line another seat reads before deciding to take it", stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	actor, ok := queueSession("create", stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	input := clientruntime.QueueCreateInput{
		SourceSession:      actor,
		DestinationSession: destination,
		Summary:            summary,
	}
	if body, present := run.option("body"); present {
		input.Body = body
	}
	if priority, present := run.option("priority"); present {
		input.Priority = &priority
	}
	if replyTo, present := run.option("reply-to"); present {
		input.ReplyTo = &replyTo
	}
	if raw, present := run.option("tag"); present {
		tags, ok := queueCommaList(raw)
		if !ok {
			fmt.Fprintf(stderr, "4genteam room queue create: --tag needs one or more tags, got %q. %s\n", raw, queueSubUsage("create"))
			return clientcmd.ExitRoomRefused
		}
		input.Tags = tags
	}
	questions, ok := ownerAskQuestions(run, input, stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	run.printPair(stdout)
	row, err := run.client.QueueCreate(run.room, input)
	if err != nil {
		return queueDaemonRefusal(stderr, "create", err)
	}
	printQueueWritten(stdout, *row)
	if questions != nil {
		record := clientdecisions.Record{QitemID: row.QitemID, Room: run.room, Questions: questions}
		if err := clientdecisions.Write(run.stateDir, record); err != nil {
			fmt.Fprintf(stderr, "4genteam room queue create: row %s was created, but its questions were not recorded (%v), so the dashboard's Decision Requests panel cannot offer it: add a --note to the row and tell the owner\n", row.QitemID, err)
			return clientcmd.ExitPartialStart
		}
		fmt.Fprintf(stdout, "questions recorded: the bridge mirrors %s to the dashboard's Decision Requests panel and applies the owner's answer\n", row.QitemID)
	}
	return clientcmd.ExitOK
}

// ownerAskQuestions enforces the owner ask's one shape before anything is asked of the daemon: a row tagged
// owner-decision is addressed to human@<room> and carries --questions-file, and --questions-file belongs to
// such a row only. The questions are validated with the hub's own limits (clientdecisions.ParseQuestions), so an
// ask that passes here is one the Decision Requests panel accepts. It returns nil questions for any other row.
func ownerAskQuestions(run queueArgs, input clientruntime.QueueCreateInput, stderr io.Writer) ([]clientdecisions.Question, bool) {
	file, hasFile := run.option("questions-file")
	ownerAsk := queueContains(input.Tags, clientdecisions.Tag)
	if !ownerAsk {
		if hasFile {
			fmt.Fprintf(stderr, "4genteam room queue create: --questions-file belongs to an owner ask (--tag %s --to %s); this row is neither. Nothing was asked of the daemon.\n", clientdecisions.Tag, clientdecisions.Human(run.room))
			return nil, false
		}
		return nil, true
	}
	if input.DestinationSession != clientdecisions.Human(run.room) {
		fmt.Fprintf(stderr, "4genteam room queue create: an %s row is the owner's ask and goes to %s, not %q: a seat's row never reaches the owner. Nothing was asked of the daemon.\n", clientdecisions.Tag, clientdecisions.Human(run.room), input.DestinationSession)
		return nil, false
	}
	if !hasFile || strings.TrimSpace(file) == "" {
		fmt.Fprintf(stderr, "4genteam room queue create: an %s row needs --questions-file PATH, the 1-4 questions the owner answers on the dashboard (the room-decision-request skill shows the shape). Nothing was asked of the daemon.\n", clientdecisions.Tag)
		return nil, false
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room queue create: --questions-file: %v. Nothing was asked of the daemon.\n", err)
		return nil, false
	}
	questions, err := clientdecisions.ParseQuestions(raw)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room queue create: --questions-file %s: %v. Nothing was asked of the daemon.\n", file, err)
		return nil, false
	}
	return questions, true
}

// runQueueUpdate appends a transition to a row, and REFUSES a call that would append nothing.
//
// The refusal is the point of the verb's shape: a transition with no state, no note and no field change
// would append a step that says "somebody touched this row" and moves nothing, and the rail's log is what
// the team reads to know what happened. The rule below MIRRORS THE STORE'S OWN (rust/agenthub-runtime/src/
// queue/mod.rs, `update`: `state_or_note_required`) so the caller is told here, before a round trip, exactly
// what the store would have told them after one - including the store's reading that a note of only
// whitespace is no note at all.
//
// The values themselves are NOT validated here: --state and --closure-reason have one home for their set,
// and the store refuses an unknown one BY NAME, listing the valid set in its own words. Nothing about such
// a typo can be mistaken for success, so the round trip buys the daemon's own sentence - which is worth it,
// where a filter's unknown state (refused above) would have bought silence.
func runQueueUpdate(args []string, stdout, stderr io.Writer) int {
	run, code, ok := parseQueue("update", args, []clientruntime.FlagSpec{
		{Name: "state", Value: true},
		{Name: "note", Value: true},
		{Name: "summary", Value: true},
		{Name: "closure-reason", Value: true},
		{Name: "closure-target", Value: true},
		{Name: "blocked-on", Value: true},
		{Name: "wake-after", Value: true},
	}, []string{"ROOM", "QITEM_ID"}, stdout, stderr)
	if !ok {
		return code
	}
	input := clientruntime.QueueUpdateInput{QitemID: run.qitemID()}
	state, hasState := run.option("state")
	note, hasNote := run.option("note")
	_, hasSummary := run.option("summary")
	closureReason, hasClosureReason := run.option("closure-reason")
	closureTarget, hasClosureTarget := run.option("closure-target")
	blockedOn, hasBlockedOn := run.option("blocked-on")
	_, hasWakeAfter := run.option("wake-after")
	if !hasState && strings.TrimSpace(note) == "" && !hasSummary && !hasClosureReason && !hasClosureTarget && !hasBlockedOn && !hasWakeAfter {
		fmt.Fprintf(stderr, "4genteam room queue update: nothing to change: this verb needs a --state, a --note, or a field change (--summary, --closure-reason, --closure-target, --blocked-on, --wake-after). A transition that moves nothing is worse than a refusal, so nothing was asked of the daemon. %s\n", queueSubUsage("update"))
		return clientcmd.ExitRoomRefused
	}
	// THE SAME CLOSE BY ANOTHER VERB. `--state done --closure-reason gate` is the close path this verb
	// offers, so the verdict rule holds here too; otherwise the one requirement of a gate close could be
	// stepped around by choosing update.
	if hasState && strings.TrimSpace(state) == "done" && hasClosureReason && gateCloseNeedsVerdict(closureReason, note) {
		fmt.Fprintf(stderr, "4genteam room queue update: --state done with --closure-reason gate is the reviewer's verdict close, and %s\n", gateCloseRefusal)
		return clientcmd.ExitRoomRefused
	}
	actor, ok := queueSession("update", stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	input.ActorSession = actor
	if hasState {
		input.State = &state
	}
	if hasNote {
		input.TransitionNote = &note
	}
	if summary, present := run.option("summary"); present {
		input.Summary = &summary
	}
	if hasClosureReason {
		input.ClosureReason = &closureReason
	}
	if hasClosureTarget {
		input.ClosureTarget = &closureTarget
	}
	if hasBlockedOn {
		input.BlockedOn = &blockedOn
	}
	if hasWakeAfter {
		wake, code, ok := queueWholeNumber(run, "update", "wake-after", stderr)
		if !ok {
			return code
		}
		input.WakeAfterSeconds = &wake
	}
	run.printPair(stdout)
	row, err := run.client.QueueUpdate(run.room, input)
	if err != nil {
		return queueDaemonRefusal(stderr, "update", err)
	}
	printQueueWritten(stdout, *row)
	return clientcmd.ExitOK
}

// runQueueClaim takes a pending row for this seat: the row moves to in-progress and the claim is recorded,
// so who took it is legible afterwards. Claiming a row that is not pending is the daemon's refusal to make
// (2, in its own words) - this verb will not pre-check the state, because that check is a read the row's
// owner would have to trust and the store's own transition already decides it.
func runQueueClaim(args []string, stdout, stderr io.Writer) int {
	run, code, ok := parseQueue("claim", args, nil, []string{"ROOM", "QITEM_ID"}, stdout, stderr)
	if !ok {
		return code
	}
	actor, ok := queueSession("claim", stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	run.printPair(stdout)
	row, err := run.client.QueueClaim(run.room, run.qitemID(), actor)
	if err != nil {
		return queueDaemonRefusal(stderr, "claim", err)
	}
	printQueueWritten(stdout, *row)
	return clientcmd.ExitOK
}

// gateCloseNeedsVerdict reports the one closure whose NOTE is required rather than optional: a reviewer's
// gate close. The rail is the loop's proof surface, and the measured defect is a gate row closed with a bare
// `closed` transition - body still holding the request, no PASS/FAIL anywhere, five rows 21:14-21:33 on
// 2026-10-18 (review 3 line 2). The tests ran; the record did not carry them.
func gateCloseNeedsVerdict(reason, note string) bool {
	return strings.TrimSpace(reason) == "gate" && strings.TrimSpace(note) == ""
}

// gateCloseRefusal is the sentence both verbs print when that note is missing; it names the verdict, because
// that is the field the caller has to fill.
const gateCloseRefusal = "the row is the loop's proof surface: --note must carry the verdict (PASS or FAIL) and the command you ran. Nothing was asked of the daemon."

// runQueueClose closes a row with a reason from the daemon's own set.
//
// THE REASON IS CHECKED HERE, BEFORE THE REQUEST, and the allowed set is printed with the refusal: a typo
// then costs no round trip, which is the invariant this verb keeps even though the store would refuse the
// same value by name. A handoff without a --target is refused here for the same reason and by name, because
// the store requires a session to hand the work TO and there is no third answer: a closure that hands work
// to nobody is not a closure.
func runQueueClose(args []string, stdout, stderr io.Writer) int {
	run, code, ok := parseQueue("close", args, []clientruntime.FlagSpec{
		{Name: "reason", Value: true},
		{Name: "target", Value: true},
		{Name: "note", Value: true},
	}, []string{"ROOM", "QITEM_ID"}, stdout, stderr)
	if !ok {
		return code
	}
	reason, present := run.option("reason")
	reason = strings.TrimSpace(reason)
	if !present || reason == "" {
		fmt.Fprintf(stderr, "4genteam room queue close: --reason is required and must be one of %s. %s\n",
			strings.Join(clientruntime.QueueClosureReasons, ", "), queueSubUsage("close"))
		return clientcmd.ExitRoomRefused
	}
	if !queueContains(clientruntime.QueueClosureReasons, reason) {
		fmt.Fprintf(stderr, "4genteam room queue close: --reason %q is not one of %s\n",
			reason, strings.Join(clientruntime.QueueClosureReasons, ", "))
		return clientcmd.ExitRoomRefused
	}
	note, hasNote := run.option("note")
	if gateCloseNeedsVerdict(reason, note) {
		fmt.Fprintf(stderr, "4genteam room queue close: --reason gate is the reviewer's verdict close, and %s\n",
			gateCloseRefusal)
		return clientcmd.ExitRoomRefused
	}
	target, hasTarget := run.option("target")
	if reason == "handed_off_to" && (!hasTarget || strings.TrimSpace(target) == "") {
		fmt.Fprintf(stderr, "4genteam room queue close: --reason handed_off_to requires --target SESSION: the row rests at handed-off while the work continues in the successor row the store creates FOR that session, and the store refuses the closure without one\n")
		return clientcmd.ExitRoomRefused
	}
	actor, ok := queueSession("close", stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	input := clientruntime.QueueCloseInput{
		QitemID:       run.qitemID(),
		ActorSession:  actor,
		ClosureReason: reason,
	}
	if hasTarget {
		input.ClosureTarget = &target
	}
	if hasNote {
		input.TransitionNote = note
	}
	run.printPair(stdout)
	row, err := run.client.QueueClose(run.room, input)
	if err != nil {
		return queueDaemonRefusal(stderr, "close", err)
	}
	printQueueWritten(stdout, *row)
	return clientcmd.ExitOK
}
