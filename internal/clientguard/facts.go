// Reading the facts the guard decides from. Every answer comes from git itself or from the environment the
// daemon sets: nothing here is inferred from a path this package computes, because a rule that misfires on a
// guess blocks a seat for a reason it cannot act on (4.4's first risk).
package clientguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ReadFacts observes one hook's facts in the working tree the hook runs in. `dir` is that working tree's top
// level (git runs a hook with its cwd set to it) and `getenv` reads the environment the git command got.
//
// It returns an error only when git itself cannot be asked. A question git answers with nothing (an author
// this repository has no identity for, a config value nobody set) is an EMPTY field, which Decide treats as
// "not observed" rather than as a value to refuse on.
func ReadFacts(hook, dir string, getenv func(string) string) (Facts, error) {
	session, hasSession := SessionFromEnv(getenv)
	facts := Facts{Hook: hook, Session: session, HasSession: hasSession}
	if hasSession {
		if roomDir, ok := DirectorRoom(session, getenv); ok {
			facts.DirectorRoom = roomDir
		}
	}

	worktree, err := gitLine(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Facts{}, err
	}
	facts.Worktree = worktree

	// A LINKED worktree is git's own answer: its git directory is not the repository's common one. Seats
	// work in linked worktrees and the owner works in the checkout itself, and every runtime answers this
	// the same way, which is why the push rule can fail closed on it.
	gitDir, err := gitLine(dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return Facts{}, err
	}
	commonDir, err := gitLine(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Facts{}, err
	}
	facts.Linked = filepath.Clean(gitDir) != filepath.Clean(commonDir)

	// The author git will record, which includes a per-commit GIT_AUTHOR_* override, and the identity this
	// repository resolves for the SEAT, which is the config alone. The two differ exactly when a commit is
	// being made under an identity that is not this worktree's own.
	facts.AuthorName, facts.AuthorEmail = authorIdent(dir)
	facts.SeatName = gitValue(dir, "config", "--get", "user.name")
	facts.SeatEmail = gitValue(dir, "config", "--get", "user.email")

	// The secret scan reads the change about to be COMMITTED (4.1: the scan is on diffs before commit). A
	// push is refused for a seat before any scan could matter, and for the owner the scan is a warning whose
	// subject is the same change the commit hook already saw.
	if hook == HookPreCommit {
		change, err := gitOutput(dir, "diff", "--cached", "--no-color", "--no-ext-diff", "-U0")
		if err != nil {
			return Facts{}, err
		}
		facts.Change = change
	}
	return facts, nil
}

// authorIdent is `git var GIT_AUTHOR_IDENT`, the identity git will write into the commit, parsed into its
// two halves. An identity this repository cannot resolve leaves both empty: it is the caller's job to report
// that, not this function's to invent one.
func authorIdent(dir string) (string, string) {
	out, err := gitOutput(dir, "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(out)
	open := strings.LastIndex(line, "<")
	close := strings.Index(line, ">")
	if open < 0 || close < open {
		return line, ""
	}
	return strings.TrimSpace(line[:open]), line[open+1 : close]
}

// HooksFolder is git's own answer for the hooks folder of the checkout at dir: `rev-parse --git-path hooks`
// honors core.hooksPath and otherwise names the repository's own hooks folder, which is the folder git will
// run. The `hooks` row of `doctor` asks git the same question for the same reason - where a guard can be in
// force at all - so both must keep taking the answer from git and never compute a path.
func HooksFolder(dir string) (string, error) {
	folder, err := gitLine(dir, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	if folder == "" {
		return "", fmt.Errorf("git named no hooks folder for %s", dir)
	}
	if !filepath.IsAbs(folder) {
		folder = filepath.Join(dir, folder)
	}
	return filepath.Clean(folder), nil
}

// root is the checkout the guard is installed in and read from: the working tree git reports for dir.
func root(dir string) (string, error) { return gitLine(dir, "rev-parse", "--show-toplevel") }

func gitLine(dir string, args ...string) (string, error) {
	out, err := gitOutput(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// gitValue is a question git may answer with nothing (an unset config value exits 1): the empty string is
// then the answer, not an error.
func gitValue(dir string, args ...string) string {
	out, err := gitOutput(dir, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitOutput(dir string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = os.Environ()
	out, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return string(out), nil
}
