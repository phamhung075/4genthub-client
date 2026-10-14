package clientrestore

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// ExitRestoreFailed is `continue`'s own failure - the restore left no seats up, or a brief the caller
// asked for does not exist. It coincides with clientcmd.ExitRemote's number while answering a different
// question, so it is named here rather than borrowed, the same rule ExitStartFailed and ExitPeak follow.
const ExitRestoreFailed = 1

const continueUsage = "usage: 4genteam continue [RIG [BRIEF|--no-brief] | --all] " + clientoffpeak.ModeFlagUsage

// continueListingUsage is the shell's own no-argument usage line, kept in its shape (the script printed
// it with its own name twice, rig-continue.sh:96, so this prints this binary's name and the verb). It is a
// constant of its own rather than continueUsage inside a "Usage: %s", because continueUsage already opens
// with "usage: " and the pair would print "Usage: usage: ...".
const continueListingUsage = "Usage: 4genteam continue <rig> [brief-file|--no-brief]   |   4genteam continue --all"

type continueCommand struct{}

func (continueCommand) Name() string { return "continue" }
func (continueCommand) Summary() string {
	return "restore a stopped rig from its latest snapshot and brief its seats"
}

// NeedsRig is false and the verb resolves the rig tool itself, because ONE of its four forms does not
// need it: the listing prints the briefs that are on disk, and a machine that has no rig tool is exactly
// where a reader wants to know which rigs have briefs. The restore forms refuse through the same platform
// matrix every other verb uses.
func (continueCommand) NeedsRig() bool { return false }

func (continueCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, continueUsage,
			"Restores a rig from its latest snapshot (every seat resumes its own session) and sends each\n"+
				"seat its brief. The rig is the parameter: its seats are discovered from tmux at run time.\n\n"+
				"  BRIEF  a file whose text every seat gets; default <home>/.openrig/briefs/<rig>.txt\n"+
				"         <home>/.openrig/briefs/<rig>/<seat>.txt replaces it for that seat\n"+
				"  --all  every stopped rig that has a brief; the run exits non-zero when a rig it visited is\n"+
				"         not up (its restore failed, or the peak gate refused it)\n"+
				"  no argument  list the rigs and the briefs on disk\n\n"+
				"The DeepSeek peak gate runs BEFORE a restore: with "+clientoffpeak.ModeEnv+" on (the default) a\n"+
				"room whose seats run a deepseek/ model is refused at peak, and "+clientoffpeak.FlagAllowPeak+
				" skips the check for this run.")
		return clientcmd.ExitOK
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(args)
	return runContinue(allowPeak, rest, stdout, stderr)
}

// runContinue dispatches the shell's own three cases: nothing (list), --all, and one rig with an optional
// brief. `--all` is a rig-name-shaped argument, so it is only recognised in the rig's position - a brief
// FILE is never read as a flag, which is the one place the shell's `case` and this can disagree.
func runContinue(allowPeak bool, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return list(stdout, stderr)
	}
	log, err := openLog(continueLogName, stdout)
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
		return continueAll(allowPeak, log, stderr)
	}
	rigName := args[0]
	if strings.HasPrefix(rigName, "-") {
		fmt.Fprintf(stderr, "4genteam continue: unknown argument %s. %s\n", rigName, continueUsage)
		return clientcmd.ExitUsage
	}
	brief, noBrief, code := briefArgument(args[1:], stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	if brief == "" && !noBrief {
		// The shell's default: <home>/.openrig/briefs/<rig>.txt. It is resolved here rather than in the
		// caller so the help, the listing and this default cannot name three different files.
		defaultPath, err := defaultBrief(rigName)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		brief = defaultPath
	}
	rig, code := resolveRig(stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	restore, err := newRestorer(rig, log)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	return continueOne(restore, rigName, brief, allowPeak, stderr)
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
			fmt.Fprintf(stderr, "4genteam continue: unexpected argument %s - this verb takes one rig and one brief. %s\n",
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
		fmt.Fprintf(stderr, "4genteam continue: unexpected argument %s - this verb takes one rig and one brief. %s\n",
			args[1], continueUsage)
		return "", false, clientcmd.ExitUsage
	}
	return args[0], false, clientcmd.ExitOK
}

// defaultBrief is the shell's `$BRIEFDIR/<rig>.txt`.
func defaultBrief(rigName string) (string, error) {
	dir, err := briefDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, rigName+".txt"), nil
}

// resolveRig is the platform matrix for the two forms that need the daemon. It is the SAME refusal the
// dispatcher gives a NeedsRig verb - one message naming what is missing - because this package asked for
// it only so the listing can run without it.
func resolveRig(stderr io.Writer) (*clientcmd.Rig, int) {
	rig, err := clientcmd.RequireRig()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
		return nil, clientcmd.ExitUnavailable
	}
	return rig, clientcmd.ExitOK
}

// continueOne restores and briefs one rig: the shell's continue_rig(), preceded by the daemon and the
// gate.
//
// THE GATE RUNS ONLY WHEN A RESTORE WILL HAPPEN, and it is placed after the session read that decides
// that, because a refusal exists to stop seats starting: a rig that is already live has nothing to
// start, and its brief is text to seats that are already running. The daemon comes first despite that,
// because the room's models are read through it.
func continueOne(restore *restorer, rigName, brief string, allowPeak bool, stderr io.Writer) int {
	restore.daemonUp()
	if len(rigSessions(rigName, sessionNames())) == 0 {
		if code := restore.gate(rigName, "continue", allowPeak, stderr); code != clientcmd.ExitOK {
			return code
		}
	}
	if restore.restore(rigName, brief) {
		return clientcmd.ExitOK
	}
	return ExitRestoreFailed
}

// continueAll is the shell's --all: every rig that has a brief, one at a time, and one rig's failure does
// not stop the others - the shell's `|| true` inside the loop, kept, because a rig that is not on DeepSeek
// must still be restored while a refused or broken one is not. What the shell did NOT do is let the loop
// decide the exit code: it ended 0 however many rigs failed, so a caller reading only the code could not
// tell a run that resumed every room from one that resumed none. The run visits every rig and then
// reports, the same shape `watchdog --all --check` uses.
func continueAll(allowPeak bool, log *teeLog, stderr io.Writer) int {
	dir, err := briefDir()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	briefs := briefsOnDisk(dir)
	if len(briefs) == 0 {
		log.printf("--all: no brief files in %s, nothing to continue", dir)
		return clientcmd.ExitOK
	}
	rig, code := resolveRig(stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	restore, err := newRestorer(rig, log)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	restore.daemonUp()
	var notUp []string
	for _, brief := range briefs {
		rigName := strings.TrimSuffix(filepath.Base(brief), ".txt")
		if len(rigSessions(rigName, sessionNames())) == 0 {
			if code := restore.gate(rigName, "continue", allowPeak, stderr); code != clientcmd.ExitOK {
				// A refusal for one rig is not a reason to skip the others: the room it refused stays
				// stopped, and a room that is not on DeepSeek is restored normally. It is still a rig this
				// run did not bring up, so it is named in the report below.
				notUp = append(notUp, rigName)
				continue
			}
		}
		if !restore.restore(rigName, brief) {
			notUp = append(notUp, rigName)
		}
	}
	if len(notUp) > 0 {
		log.printf("--all: %d of %d rigs are not up: %s", len(notUp), len(briefs), strings.Join(notUp, ", "))
		return ExitRestoreFailed
	}
	return clientcmd.ExitOK
}

// briefsOnDisk is the shell's `$BRIEFDIR/*.txt`, sorted so `--all` visits the rigs in a fixed order
// rather than in whatever order the directory hands them over.
func briefsOnDisk(dir string) []string {
	found, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		return nil
	}
	sort.Strings(found)
	return found
}

// list is the shell's no-argument form: which rigs exist, which briefs are on disk, and the usage line. It
// needs no rig tool: the briefs are the files on disk, and the rig list is the daemon's own route
// (clientruntime.RigEntries), which is also where every other read of these rigs comes from. When that read
// cannot be made the section says so on one line - and when this shell names no daemon it prints no rig
// line at all, because there is no list to print and no failure either.
func list(stdout, stderr io.Writer) int {
	fmt.Fprintln(stdout, "Rigs that can be continued:")
	entries, ok, err := clientruntime.RigEntries(context.Background(), true)
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "  (the rig list could not be read: %v)\n", err)
	case !ok:
		// NO RECORDS is not a FAILURE - this shell names no OpenRig daemon - but it is also not a list, and an
		// empty section under this heading reads as "no rigs here". So it is said, one line, for the same
		// reason the line it replaces named the absent rig tool: a listing that can see no rigs says why.
		fmt.Fprintln(stdout, "  (no OpenRig daemon is named here, so there is no rig list to read)")
	default:
		// The shell piped the rig tool through `head -30`: a machine with many rigs should not bury the
		// briefs under its rig list. Sorted by name - the id breaks a tie, so two runs against one daemon
		// print one list - and an archived rig is marked as such rather than dropped, because the listing
		// asked for them.
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Name != entries[j].Name {
				return entries[i].Name < entries[j].Name
			}
			return entries[i].RigID < entries[j].RigID
		})
		for i, entry := range entries {
			if i == 30 {
				break
			}
			line := "  " + entry.Name + "  " + entry.Status
			if entry.IsArchived {
				line += "  [archived]"
			}
			fmt.Fprintln(stdout, line)
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "briefs on disk:")
	dir, err := briefDir()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam continue: %v\n", err)
		return clientcmd.ExitUnavailable
	}
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
