package clientworktree

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// This file is the `worktree` verb group: the two operator readings of `git worktree list` the ruling
// names. `list` writes nothing at all - it prints the room's derived worktrees and every other entry,
// so an operator sees at one glance which of the machine's worktrees the room owns and which of them
// nobody owns. `prune` removes the unowned ones the ruling names and nothing else, and it names
// everything it kept.

// Commands is what the dispatcher registers from this package.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{worktreeCommand{}}
}

type worktreeCommand struct{}

func (worktreeCommand) Name() string { return "worktree" }
func (worktreeCommand) Summary() string {
	return "the room's derived seat worktrees: list them, prune the ones nobody owns"
}
func (worktreeCommand) NeedsRig() bool { return false }

const worktreeVerbs = "list, prune"

func (worktreeCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "4genteam worktree: pick a verb: %s\n", worktreeVerbs)
		return clientcmd.ExitUsage
	}
	var err error
	switch args[0] {
	case "list":
		err = runList(args[1:], stdout)
	case "prune":
		err = runPrune(args[1:], stdout)
	case "-h", "--help", "help":
		printWorktreeUsage(stdout)
		return clientcmd.ExitOK
	default:
		fmt.Fprintf(stderr, "4genteam worktree: unknown verb %q; the verbs are %s\n", args[0], worktreeVerbs)
		return clientcmd.ExitUsage
	}
	if err != nil {
		if errors.Is(err, errHelpShown) {
			return clientcmd.ExitOK
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
	}
	return clientcmd.ExitOK
}

func printWorktreeUsage(w io.Writer) {
	fmt.Fprint(w, `usage: 4genteam worktree <verb> RIG [--main DIR]

Read and clean up the room's derived seat worktrees. The list is derived from the seats of the room's
own record (clientservices.DerivedWorktrees), so nothing here keeps a list of its own.

verbs:
  list   print the room's derived worktrees and every other foreign entry of git worktree list
  prune  remove only /tmp/wt-*, /tmp/gate-* and prunable entries, naming everything kept

options:
  --main DIR  the checkout whose worktrees these are (default: git rev-parse --show-toplevel)

Every verb answers --help with its own usage.
`)
}

// errHelpShown separates "the caller asked for usage" from a failure: both stop the verb, but usage is
// a success.
var errHelpShown = errors.New("usage shown")

// newVerbFlags is the one place a verb's FlagSet is built: output discarded (the verb prints its own
// usage), so a parse failure produces exactly one message from this package.
func newVerbFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

// parseRigVerb parses a verb's flags and takes its ONE positional argument, the rig name. A missing
// name, a second argument and an argument that is a flag-shaped word are all usage errors: there is no
// default rig, and a rig name silently read out of a misplaced flag is a room this client would then
// read the wrong directory for.
func parseRigVerb(flags *flag.FlagSet, args []string, usage func(io.Writer), stdout io.Writer) (string, error) {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return "", errHelpShown
		}
		return "", usageError(err.Error())
	}
	switch flags.NArg() {
	case 0:
		return "", usageError("a rig name is required - there is no default rig")
	case 1:
	default:
		return "", usageError(fmt.Sprintf("unexpected argument %q - this verb takes one rig name", flags.Arg(1)))
	}
	rig := flags.Arg(0)
	if strings.TrimSpace(rig) == "" || strings.HasPrefix(rig, "-") {
		return "", usageError(fmt.Sprintf("%q is not a rig name", rig))
	}
	return rig, nil
}

func usageError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: clientcmd.ExitUsage}
}

// RoomDir is the room's OWN directory: clientservices.RoomDir is the ONE resolution, and it is
// restated here only so a test can drive the verbs over its own store, in the repo's style of a
// package-level seam rather than an interface added for testing.
var RoomDir = clientservices.RoomDir

// roomOf is the two readings both verbs open with: the room's directory and the seats of the room's OWN
// record. The seats come from the ONE derivation (clientservices.LiveSeats over spec.json), never from a
// list written down here: a room that exists is read from what exists.
func roomOf(rig string) (string, []clientservices.SeatType, error) {
	roomDir, err := RoomDir(rig)
	if err != nil {
		return "", nil, err
	}
	root := filepath.Dir(filepath.Dir(roomDir))
	seats, err := clientservices.LiveSeats(root, rig)
	if err != nil {
		return "", nil, err
	}
	return roomDir, seats, nil
}

// derivedIndex is the derived worktrees keyed by their cleaned path, for the two verbs' lookups.
func derivedIndex(roomDir, rig string, seats []clientservices.SeatType) map[string]clientservices.Worktree {
	index := make(map[string]clientservices.Worktree, len(seats)+1)
	for _, worktree := range clientservices.DerivedWorktrees(roomDir, rig, seats) {
		index[filepath.Clean(worktree.Path)] = worktree
	}
	return index
}

// describe is one listing entry as a line's tail: the branch it is on, or `detached`.
func describe(entry Entry) string {
	if entry.Detached || entry.Branch == "" {
		return "detached"
	}
	return entry.Branch
}

const listUsage = `usage: 4genteam worktree list RIG [--main DIR]

Print the room's derived seat worktrees (one line each, from the room's team.json) and every other
entry of git worktree list, labelled foreign. It writes nothing.

options:
  --main DIR  the checkout whose worktrees these are (default: git rev-parse --show-toplevel)
`

// runList prints the derived worktrees first, in the derivation's own order, then every entry the
// derivation does not name. A derived path that is not in the listing is printed as missing instead of
// being left out, so a room that was never started does not read like a room with no seats.
func runList(args []string, stdout io.Writer) error {
	flags := newVerbFlags("list")
	mainFlag := flags.String("main", "", "the checkout whose worktrees these are")
	rig, err := parseRigVerb(flags, args, func(w io.Writer) { fmt.Fprint(w, listUsage) }, stdout)
	if err != nil {
		return err
	}
	mainRoot, err := MainRoot(*mainFlag)
	if err != nil {
		return err
	}
	roomDir, seats, err := roomOf(rig)
	if err != nil {
		return err
	}
	entries, err := Read(mainRoot)
	if err != nil {
		return err
	}
	derived := derivedIndex(roomDir, rig, seats)
	present := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		present[filepath.Clean(entry.Path)] = entry
	}
	for _, worktree := range clientservices.DerivedWorktrees(roomDir, rig, seats) {
		entry, registered := present[filepath.Clean(worktree.Path)]
		if !registered {
			fmt.Fprintf(stdout, "derived %s missing (not in `git worktree list`)\n", worktree.Path)
			continue
		}
		fmt.Fprintf(stdout, "derived %s %s\n", worktree.Path, describe(entry))
	}
	for _, entry := range entries {
		if _, ours := derived[filepath.Clean(entry.Path)]; ours {
			continue
		}
		fmt.Fprintf(stdout, "foreign %s %s\n", entry.Path, describe(entry))
	}
	return nil
}

const pruneUsage = `usage: 4genteam worktree prune RIG [--main DIR]

Remove the worktrees the ruling names and nothing else: a direct child of /tmp whose name begins with
wt- or gate-, and an entry git marks prunable. Removal is ` + "`git worktree remove`" + ` WITHOUT --force, so a
dirty worktree is named and left standing; a worktree whose detached HEAD is on no branch and is not in
main is refused and named, because removing it would delete the only reference to that commit. Every
other foreign entry is listed and never removed.

options:
  --main DIR  the checkout whose worktrees these are (default: git rev-parse --show-toplevel)
`

// runPrune walks the listing and removes only what the ruling names. It completes the walk - every
// candidate is decided and printed - and then runs `git worktree prune` once for the entries whose
// working tree is already gone. A worktree it kept or refused makes the verb fail, naming every one of
// them: an operator who asked for a prune must not have to read the output to learn that some of it
// did not happen.
func runPrune(args []string, stdout io.Writer) error {
	flags := newVerbFlags("prune")
	mainFlag := flags.String("main", "", "the checkout whose worktrees these are")
	rig, err := parseRigVerb(flags, args, func(w io.Writer) { fmt.Fprint(w, pruneUsage) }, stdout)
	if err != nil {
		return err
	}
	mainRoot, err := MainRoot(*mainFlag)
	if err != nil {
		return err
	}
	roomDir, seats, err := roomOf(rig)
	if err != nil {
		return err
	}
	entries, err := Read(mainRoot)
	if err != nil {
		return err
	}
	derived := derivedIndex(roomDir, rig, seats)
	var kept []string
	for _, entry := range entries {
		if _, ours := derived[filepath.Clean(entry.Path)]; ours {
			fmt.Fprintf(stdout, "derived %s kept: the room's own worktree, prune never removes it\n", entry.Path)
			continue
		}
		if entry.Prunable {
			// `git worktree prune`, run below, is what removes a prunable entry: its working tree is
			// already gone, so `git worktree remove` would have to invent one to delete.
			fmt.Fprintf(stdout, "prunable %s (git worktree prune below)\n", entry.Path)
			continue
		}
		if !ruledForRemoval(entry.Path) {
			fmt.Fprintf(stdout, "foreign %s listed, never removed\n", entry.Path)
			continue
		}
		if entry.Locked {
			kept = append(kept, entry.Path)
			fmt.Fprintf(stdout, "kept %s: git locks it, so prune names it and removes nothing\n", entry.Path)
			continue
		}
		if entry.Detached {
			inMain, err := headInMain(mainRoot, entry.Path)
			switch {
			case err != nil:
				kept = append(kept, entry.Path)
				fmt.Fprintf(stdout, "kept %s: cannot read its HEAD: %v\n", entry.Path, err)
				continue
			case !inMain:
				kept = append(kept, entry.Path)
				fmt.Fprintf(stdout, "refused %s: its detached HEAD is on no branch and is not in main, "+
					"and removing it would delete the reflog - the only reference to that commit\n", entry.Path)
				continue
			}
		}
		if _, err := runGit(mainRoot, "worktree", "remove", entry.Path); err != nil {
			kept = append(kept, entry.Path)
			fmt.Fprintf(stdout, "kept %s: git refused to remove it without --force: %v\n", entry.Path, err)
			continue
		}
		fmt.Fprintf(stdout, "removed %s\n", entry.Path)
	}
	if _, err := runGit(mainRoot, "worktree", "prune"); err != nil {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("removed what the ruling names, but `git worktree prune` then failed: %v", err),
			Code:    clientcmd.ExitUnavailable,
		}
	}
	if len(kept) > 0 {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("%d worktree(s) were named and left in place: %s",
				len(kept), strings.Join(kept, ", ")),
			Code: clientcmd.ExitUnavailable,
		}
	}
	return nil
}

// ruledForRemoval is the ruling's prefix rule in one line: prune may remove a DIRECT child of /tmp
// whose name begins wt- or gate-, and nothing else. The rule keys on the name because the name is what
// says who owns the worktree: the gates run in /tmp/gate-*, a stray seat worktree is /tmp/wt-*, and a
// checkout anywhere else is somebody's.
func ruledForRemoval(path string) bool {
	dir, base := filepath.Split(filepath.Clean(path))
	if filepath.Clean(dir) != "/tmp" {
		return false
	}
	return strings.HasPrefix(base, "wt-") || strings.HasPrefix(base, "gate-")
}

// headInMain reports whether a worktree's detached HEAD is contained in main. It is the check that
// turns "removable" into "refused": a detached commit that main does not contain exists only in that
// worktree's reflog, so `git worktree remove` would quietly delete the last reference to it.
func headInMain(mainRoot, path string) (bool, error) {
	sha, err := runGit(path, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	if _, err := runGit(mainRoot, "merge-base", "--is-ancestor", sha, "main"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
