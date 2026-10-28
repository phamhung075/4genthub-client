// The `4genteam guard` verb: `install` puts the guard's hooks into a checkout and its entries into the runtime
// CLIs this machine has, and `run` is what those hooks call. Neither needs a room: the guard is a fact about a
// checkout and about a home directory, not about a room (one checkout holds every room's worktrees).
package clientguard

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

const guardUsage = `usage: 4genteam guard install [--checkout DIR] [--home DIR]
       4genteam guard uninstall [--checkout DIR] [--home DIR]
       4genteam guard run pre-commit|pre-push|reference-transaction [args...]`

// ExitRefused is the git guard declining the command git called the hook for. Any non-zero exit from a hook
// aborts the git command, and this is the number the client's other refusals use (clientcmd.ExitUnavailable
// and clientseat's ExitFailed share it), named here because it answers its own question: the guard, not the
// platform, refused.
const ExitRefused = 3

// Commands is what the dispatcher registers from this package.
func Commands() []clientcmd.Command { return []clientcmd.Command{guardCommand{}} }

type guardCommand struct{}

func (guardCommand) Name() string { return "guard" }
func (guardCommand) Summary() string {
	return "the guard: install its git hooks and runtime entries, or run one"
}

func (guardCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, guardUsage)
		fmt.Fprintln(stdout, "\nThe guard refuses a push from a seat, a commit made in the shared checkout and a commit whose")
		fmt.Fprintln(stdout, "author is not the seat's own; it warns when a change looks like it carries a credential. It lives")
		fmt.Fprintln(stdout, "in git, so it covers every runtime git runs under. `install` writes the hooks git resolves for the")
		fmt.Fprintln(stdout, "checkout you run it in, and the guarded entry each runtime CLI this machine has reads before a seat")
		fmt.Fprintln(stdout, "exists (claude's `~/.claude/settings.json`, agy's `~/.gemini/config/hooks.json`); `run` is what")
		fmt.Fprintln(stdout, "those hooks call.")
		return clientcmd.ExitOK
	}
	if len(args) == 0 {
		fmt.Fprintf(stderr, "4genteam guard: pick a verb: install, uninstall, run\n%s\n", guardUsage)
		return clientcmd.ExitUsage
	}
	switch args[0] {
	case "install":
		return runInstall(args[1:], stdout, stderr)
	case "uninstall":
		return runUninstall(args[1:], stdout, stderr)
	case "run":
		return runHook(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "4genteam guard: unknown verb %q; the verbs are install, uninstall, run\n", args[0])
	return clientcmd.ExitUsage
}

func runInstall(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("guard install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	checkout := flags.String("checkout", ".", "the checkout whose hooks git should run the guard from")
	home := flags.String("home", "", "the home whose runtime config files are written (default: the registered user's home)")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "4genteam guard install: unexpected argument %q\n%s\n", flags.Arg(0), guardUsage)
		return clientcmd.ExitUsage
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam guard install: cannot name the client that is running: %v\n", err)
		return ExitRefused
	}
	installed, err := Install(*checkout, binary, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam guard install: %v\n", err)
		return ExitRefused
	}
	fmt.Fprintf(stdout, "the git guard is in force for %s: %d hook(s) written, and the hooks folder git resolves is "+
		"the one they went into\n", *checkout, len(installed))
	// THE RUNTIME SIDE, in the same verb: the git guard covers git, and these entries are what covers the runtimes
	// whose only hook surface is their owner-wide file. A machine with none of the runtimes installed writes
	// nothing and says so in one line.
	root := *home
	if root == "" {
		root, err = clientenv.RealHome()
		if err != nil {
			fmt.Fprintf(stderr, "4genteam guard install: cannot resolve the home whose runtime configs are "+
				"written: %v\n", err)
			return ExitRefused
		}
	}
	hooks, err := InstallRuntimeHooks(root)
	if err != nil {
		// A partial install is named before the refusal: the entries written before the failing file are in force,
		// and a reader who sees only the error would go looking for an install that half happened.
		for _, hook := range hooks {
			fmt.Fprintln(stdout, RuntimeInstallLine(hook))
		}
		fmt.Fprintf(stderr, "4genteam guard install: %v\n", err)
		return ExitRefused
	}
	if len(hooks) == 0 {
		fmt.Fprintf(stdout, "no runtime of this client (agy, claude, codex) was found under %s, so no runtime "+
			"entry was written\n", root)
		return clientcmd.ExitOK
	}
	unread := 0
	for _, hook := range hooks {
		fmt.Fprintln(stdout, RuntimeInstallLine(hook))
		if hook.ReadError != "" {
			unread++
		}
	}
	// A DETECTED RUNTIME WHOSE CONFIG COULD NOT BE READ IS NOT AN INSTALL THAT SUCCEEDED: its guard is not in
	// force, and exiting 0 would report a machine as guarded when the runtime's hooks never call the guard. The
	// lines above are printed first, so a reader sees which runtimes DID land.
	if unread > 0 {
		fmt.Fprintf(stderr, "4genteam guard install: %d runtime config(s) could not be read, so their guard "+
			"entries were not placed\n", unread)
		return ExitRefused
	}
	return clientcmd.ExitOK
}

func runUninstall(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("guard uninstall", flag.ContinueOnError)
	flags.SetOutput(stderr)
	checkout := flags.String("checkout", ".", "the checkout whose guard hooks should be taken back out")
	home := flags.String("home", "", "the home whose runtime config files are cleaned (default: the registered user's home)")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "4genteam guard uninstall: unexpected argument %q\n%s\n", flags.Arg(0), guardUsage)
		return clientcmd.ExitUsage
	}
	// The runtime entries come out FIRST, because they do not depend on the checkout at all: an entry install wrote
	// is removed by the home it was written in, and a checkout that has since been deleted is exactly the case where
	// the owner still needs the entry gone. Taking the git hooks out first would refuse here and leave it behind.
	failed := false
	root := *home
	if root == "" {
		resolved, err := clientenv.RealHome()
		if err != nil {
			fmt.Fprintf(stderr, "4genteam guard uninstall: cannot resolve the home whose runtime configs are "+
				"cleaned: %v\n", err)
			return ExitRefused
		}
		root = resolved
	}
	hooks, err := UninstallRuntimeHooks(root)
	for _, hook := range hooks {
		fmt.Fprintln(stdout, RuntimeUninstallLine(hook))
	}
	if err != nil {
		fmt.Fprintf(stderr, "4genteam guard uninstall: %v\n", err)
		failed = true
	}
	removed, err := Uninstall(*checkout, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam guard uninstall: %v\n", err)
		return ExitRefused
	}
	if len(removed) == 0 {
		fmt.Fprintf(stdout, "the git guard is not installed for %s: nothing to take out\n", *checkout)
	} else {
		fmt.Fprintf(stdout, "the git guard is out of %s: %d hook(s) removed, and any hook it had replaced is back "+
			"where it was\n", *checkout, len(removed))
	}
	if failed {
		return ExitRefused
	}
	return clientcmd.ExitOK
}

// runHook is the entry point a hook calls. `dir` is the working tree git runs the hook in, and this process
// inherits git's own environment, which is where the daemon's session name and any per-commit author override
// live.
func runHook(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == HookReferenceTransaction {
		return runReferenceTransaction(args[1:], os.Stdin, stderr, os.Getenv)
	}
	if len(args) == 0 || (args[0] != HookPreCommit && args[0] != HookPrePush) {
		fmt.Fprintf(stderr, "4genteam guard run: name the hook git called: %s or %s\n%s\n", HookPreCommit, HookPrePush, guardUsage)
		return clientcmd.ExitUsage
	}
	hook := args[0]
	facts, err := ReadFacts(hook, ".", os.Getenv)
	if err != nil {
		// A check that cannot run: for a push that means REFUSE (4.4: push and force fail closed), and for a
		// commit it means say so and let git carry on (nothing else is a blocking hook).
		if hook == HookPrePush {
			fmt.Fprintf(stderr, "4genteam git guard: %v - so this push cannot be checked, and a push cannot be "+
				"undone: refusing it.\n", err)
			return ExitRefused
		}
		fmt.Fprintf(stderr, "4genteam git guard: %v - so this commit was not checked.\n", err)
		return clientcmd.ExitOK
	}
	verdict := Decide(facts)
	for _, line := range verdict.Lines() {
		fmt.Fprintln(stderr, line)
	}
	if verdict.Refused() {
		return ExitRefused
	}
	_ = stdout
	return clientcmd.ExitOK
}
