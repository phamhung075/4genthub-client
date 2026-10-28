package clientrestore

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// ExitRestoreFailed is `continue`'s own failure - the restore left no seats up, a brief the caller asked
// for does not exist, or the seats are up while a safeguard is still down after continue started it (a room
// `stop`ped and then "continued": stop leaves the sessions standing). It coincides with clientcmd.ExitRemote's number while
// answering a different question, so it is named here rather than borrowed, the same rule ExitStartFailed
// and ExitPeak follow.
const ExitRestoreFailed = 1

const continueUsage = "usage: 4genteam continue [--state-root DIR] [ROOM [BRIEF|--no-brief] | --all] " + clientoffpeak.ModeFlagUsage

// continueListingUsage is the shell's own no-argument usage line, kept in its shape (the script printed
// it with its own name twice, room-continue.sh:96, so this prints this binary's name and the verb). It is a
// constant of its own rather than continueUsage inside a "Usage: %s", because continueUsage already opens
// with "usage: " and the pair would print "Usage: usage: ...".
const continueListingUsage = "Usage: 4genteam continue <room> [brief-file|--no-brief]   |   4genteam continue --all"

type continueCommand struct{}

func (continueCommand) Name() string { return "continue" }
func (continueCommand) Summary() string {
	return "restore a stopped room from its latest snapshot and brief its seats"
}

func (continueCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, continueUsage,
			"Restores a stopped room from its latest snapshot (every seat resumes its own session) and sends each\n"+
				"seat its brief. The room is the parameter: its seats are discovered from tmux at run time.\n\n"+
				"  `stop` stops agent and its children and leaves the sessions standing, so a stop/continue pair\n"+
				"  (the peak pause) finds the seats live and the safeguards down: continue then starts them with\n"+
				"  the same `4genteam up ROOM` heal runs (a live room is read, not restarted) and reads them again.\n"+
				"  A safeguard still down after that is named, the room is reported NOT restored, and it exits 1.\n\n"+
				"  BRIEF  a file whose text every seat gets; default <home>/.config/4genthub/briefs/<room>.txt\n"+
				"         <home>/.config/4genthub/briefs/<room>/<seat>.txt replaces it for that seat\n"+
				"  --all  every stopped room that has a brief; the run exits non-zero when a room it visited is\n"+
				"         not up (its restore failed, or the peak gate refused it)\n"+
				"  no argument  list the rooms and the briefs on disk\n\n"+
				"  --state-root DIR  the state root to read and write (default <home>/.config/4genthub): the\n"+
				"         briefs, the logs and the tmux server asked are all this root's\n\n"+
				"The DeepSeek peak gate runs BEFORE a restore: with "+clientoffpeak.ModeEnv+" on (the default) a\n"+
				"room whose seats run a deepseek/ model is refused at peak, and "+clientoffpeak.FlagAllowPeak+
				" skips the check for this run.")
		return clientcmd.ExitOK
	}
	rootFlag, rest, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUnavailable)
	}
	root, err := resolveRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(rest)
	return runContinue(allowPeak, root, rest, stdout, stderr)
}

// runContinue dispatches the shell's own three cases: nothing (list), --all, and one room with an optional
// brief. `--all` is a room-name-shaped argument, so it is only recognised in the room's position - a brief
// FILE is never read as a flag, which is the one place the shell's `case` and this can disagree.
func runContinue(allowPeak bool, root string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return list(root, stdout, stderr)
	}
	log, err := openLog(continueLogName, root, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	defer log.close()
	if args[0] == "--all" {
		if len(args) > 1 {
			fmt.Fprintf(stderr, "4genteam continue: unexpected argument %s - --all takes none. %s\n", args[1], continueUsage)
			return clientcmd.ExitUsage
		}
		return continueAll(allowPeak, root, log, stderr)
	}
	roomName := args[0]
	if strings.HasPrefix(roomName, "-") {
		fmt.Fprintf(stderr, "4genteam continue: unknown argument %s. %s\n", roomName, continueUsage)
		return clientcmd.ExitUsage
	}
	brief, noBrief, code := briefArgument(args[1:], stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	if brief == "" && !noBrief {
		// The default: <home>/.config/4genthub/briefs/<room>.txt. It is resolved here rather than in the
		// caller so the help, the listing and this default cannot name three different files.
		brief = defaultBrief(root, roomName)
	}
	restore := newRestorer(log, root)
	restore.startsSupervision, restore.allowPeak = true, allowPeak
	return continueOne(restore, roomName, brief, allowPeak, stderr)
}

// briefArgument reads the shell's optional second argument: `--no-brief` means restore only, anything
// else is the brief's path. The three answers are distinct on purpose - no argument at all means the
// DEFAULT file, and `--no-brief` means a path that is empty - because a caller who asked for no brief
// must not get the default one.
func briefArgument(args []string, stderr io.Writer) (brief string, noBrief bool, code int) {
	if len(args) == 0 {
		return "", false, clientcmd.ExitOK
	}
	switch args[0] {
	case "--no-brief":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "4genteam continue: unexpected argument %s - this verb takes one room and one brief. %s\n",
				args[1], continueUsage)
			return "", false, clientcmd.ExitUsage
		}
		return "", true, clientcmd.ExitOK
	}
	if strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "4genteam continue: unknown argument %s. %s\n", args[0], continueUsage)
		return "", false, clientcmd.ExitUsage
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "4genteam continue: unexpected argument %s - this verb takes one room and one brief. %s\n",
			args[1], continueUsage)
		return "", false, clientcmd.ExitUsage
	}
	return args[0], false, clientcmd.ExitOK
}

// defaultBrief is the shell's `$BRIEFDIR/<room>.txt`, in the root the verb resolved.
func defaultBrief(root, roomName string) string {
	return filepath.Join(briefDir(root), roomName+".txt")
}

// continueOne restores and briefs one room: the shell's continue_room(), preceded by the daemon and the
// gate.
//
// THE GATE RUNS ONLY WHEN A RESTORE WILL HAPPEN, and it is placed after the session read that decides
// that, because a refusal exists to stop seats starting: a room that is already live has nothing to
// start, and its brief is text to seats that are already running. The daemon comes first despite that,
// because the room's models are read through it.
func continueOne(restore *restorer, roomName, brief string, allowPeak bool, stderr io.Writer) int {
	restore.daemonUp()
	if len(roomSessions(roomName, sessionNames(restore.root))) == 0 {
		if code := restore.gate(roomName, "continue", allowPeak, stderr); code != clientcmd.ExitOK {
			return code
		}
	}
	if restore.restore(roomName, brief) {
		return clientcmd.ExitOK
	}
	return ExitRestoreFailed
}

// continueAll is the shell's --all: every room that has a brief, one at a time, and one room's failure does
// not stop the others - the shell's `|| true` inside the loop, kept, because a room that is not on DeepSeek
// must still be restored while a refused or broken one is not. What the shell did NOT do is let the loop
// decide the exit code: it ended 0 however many rooms failed, so a caller reading only the code could not
// tell a run that resumed every room from one that resumed none. The run visits every room and then
// reports, the same shape `watchdog --all --check` uses.
func continueAll(allowPeak bool, root string, log *teeLog, stderr io.Writer) int {
	dir := briefDir(root)
	briefs := briefsOnDisk(dir)
	if len(briefs) == 0 {
		log.printf("--all: no brief files in %s, nothing to continue", dir)
		return clientcmd.ExitOK
	}
	restore := newRestorer(log, root)
	restore.startsSupervision, restore.allowPeak = true, allowPeak
	restore.daemonUp()
	var notUp []string
	for _, brief := range briefs {
		roomName := strings.TrimSuffix(filepath.Base(brief), ".txt")
		if len(roomSessions(roomName, sessionNames(root))) == 0 {
			if code := restore.gate(roomName, "continue", allowPeak, stderr); code != clientcmd.ExitOK {
				// A refusal for one room is not a reason to skip the others: the room it refused stays
				// stopped, and a room that is not on DeepSeek is restored normally. It is still a room this
				// run did not bring up, so it is named in the report below.
				notUp = append(notUp, roomName)
				continue
			}
		}
		if !restore.restore(roomName, brief) {
			notUp = append(notUp, roomName)
		}
	}
	if len(notUp) > 0 {
		log.printf("--all: %d of %d rooms did not complete: %s", len(notUp), len(briefs), strings.Join(notUp, ", "))
		return ExitRestoreFailed
	}
	return clientcmd.ExitOK
}

// briefsOnDisk is the shell's `$BRIEFDIR/*.txt`, sorted so `--all` visits the rooms in a fixed order
// rather than in whatever order the directory hands them over.
func briefsOnDisk(dir string) []string {
	found, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		return nil
	}
	sort.Strings(found)
	return found
}

// list is the shell's no-argument form: which rooms exist, which briefs are on disk, and the usage line. The
// briefs are the files on disk, and the room list is the runtime's own (ListAllRooms, with each room's seat
// states). When the runtime cannot be read the section says so on one line rather than printing an empty list.
func list(root string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stdout, "Rooms that can be continued:")
	client := clientruntime.NewClient("")
	rooms, err := client.ListAllRooms()
	if err != nil {
		fmt.Fprintf(stdout, "  (the room list could not be read from the runtime: %v)\n", err)
	} else {
		// A machine with many rooms should not bury the briefs under its room list.
		sort.Strings(rooms)
		for i, room := range rooms {
			if i == 30 {
				break
			}
			fmt.Fprintln(stdout, "  "+room+"  "+roomWord(client, room))
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "briefs on disk:")
	dir := briefDir(root)
	for _, brief := range briefsOnDisk(dir) {
		fmt.Fprintf(stdout, "  %s\n", strings.TrimSuffix(filepath.Base(brief), ".txt"))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, continueListingUsage)
	return clientcmd.ExitOK
}

// printHelp is the two-line help shape the lifecycle verbs print: the usage line, then what the verb does.
func printHelp(out io.Writer, usage, description string) {
	fmt.Fprintf(out, "%s\n\n%s\n", usage, description)
}

// roomWord is a room's state in one word: running when every seat runs, stopped when none does, partial
// otherwise, and "unknown" when the runtime would not list the room's seats.
func roomWord(client *clientruntime.Client, room string) string {
	seats, err := client.GetRoomStatus(room)
	if err != nil || len(seats) == 0 {
		return "unknown"
	}
	running := 0
	for _, seat := range seats {
		if strings.EqualFold(seat.State, "running") {
			running++
		}
	}
	switch running {
	case len(seats):
		return "running"
	case 0:
		return "stopped"
	}
	return "partial"
}
