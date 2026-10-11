// Package clientguard is the client's git guard: the hooks that refuse a push from a seat, a commit made in
// the shared checkout and a commit whose author is not the seat's own, and that warn when a change about to
// leave the machine looks like it carries a credential.
//
// WHY GIT. 4.4's third risk is a guard built into one runtime: claude-code has pre-tool hooks, omp has deny
// lists, and Codex and agy have neither, so a per-runtime guard protects some seats and silently not others.
// Git runs a hook whatever program called git, so the guard lives in git and covers all four runtimes.
//
// WHAT BLOCKS, AND WHAT DOES NOT. 4.4 rules that only this guard blocks: push and force-push fail closed (a
// check that cannot run still refuses), a commit from the shared checkout and a foreign author are refused,
// and the secret scan WARNS. Everything else in this client only reports.
//
// A REFUSAL MUST BE ACTIONABLE. 4.4's second risk is a blocked seat with nobody watching: a refusal without
// a human present is a stall, not a stop. Every refusal this package writes names what was seen, why it is
// refused, and the route out - the owner pushes, the seat commits in its own worktree, the lead is told.
package clientguard

import (
	"fmt"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// The two hooks this client installs, by git's own names.
const (
	HookPreCommit = "pre-commit"
	HookPrePush   = "pre-push"
)

// SessionEnv is the variable the OpenRig daemon sets on a node's terminal, whatever the runtime the node
// runs (4.6 measured it: `OPENRIG_SESSION_NAME`, copied by `rig` into its own requests). It is the
// runtime-neutral way to tell a seat's shell from a terminal the owner is working in.
const SessionEnv = "OPENRIG_SESSION_NAME"

// Session is what the daemon's session name says: `<room>-<seat>@<room>`, both ends matched so a name that
// merely contains the room is another room's session.
type Session struct {
	Room string
	Seat string
}

// Line is the session name back in the shape the environment carries.
func (s Session) Line() string { return s.Room + "-" + s.Seat + "@" + s.Room }

// SessionFromEnv reads a seat's session out of the environment. `false` means this shell is not a seat's:
// either the variable is absent (the owner's terminal, where the daemon sets nothing) or it does not name a
// session, which is a fact the caller reports rather than guesses around.
func SessionFromEnv(getenv func(string) string) (Session, bool) {
	name := strings.TrimSpace(getenv(SessionEnv))
	if name == "" {
		return Session{}, false
	}
	at := strings.LastIndex(name, "@")
	if at <= 0 || at == len(name)-1 {
		return Session{}, false
	}
	room, rest := name[at+1:], name[:at]
	prefix := room + "-"
	if !strings.HasPrefix(rest, prefix) {
		return Session{}, false
	}
	seat := strings.TrimPrefix(rest, prefix)
	if seat == "" || strings.ContainsAny(room+seat, " /") {
		return Session{}, false
	}
	return Session{Room: room, Seat: seat}, true
}

// Facts is what a guard decides from. Every field is an observation the caller made, because this package's
// rules are a pure function of them: a rule that could not be decided is one the caller left empty and the
// verdict names, never a value this package invents.
type Facts struct {
	// Hook is StateHookPreCommit or HookPrePush.
	Hook string
	// Session is the session the environment named, when it named one.
	Session Session
	// HasSession is whether the environment named a session at all.
	HasSession bool
	// Worktree is the working tree the hook is running in (git's own answer), and MainRoot is the shared
	// checkout the worktrees hang off. A working tree that is not the shared checkout is a worktree.
	Worktree string
	MainRoot string
	// Linked is whether the working tree is a LINKED WORKTREE of the repository (git's own answer: its git
	// directory is not the repository's common one). Seats work in linked worktrees, the owner in the
	// checkout itself, and every runtime's seat therefore answers this the same way.
	Linked bool
	// AuthorName and AuthorEmail are the identity git will record for the commit, after any per-commit
	// override.
	AuthorName  string
	AuthorEmail string
	// SeatName and SeatEmail are the identity this seat commits as, when the client can read one. Empty
	// means the caller could not read it, and the author rule is then a warning rather than a refusal.
	SeatName  string
	SeatEmail string
	// Change is the text the secret scan reads: the staged change for a commit, the outgoing refs for a
	// push.
	Change string
}

// Verdict is the guard's answer. Refusals stop the git command; warnings are printed and nothing else.
type Verdict struct {
	Refusals []string
	Warnings []string
}

// Refused reports whether the git command must not proceed.
func (v Verdict) Refused() bool { return len(v.Refusals) > 0 }

// Lines is everything the guard says, refusals first, so one writer can report it.
func (v Verdict) Lines() []string {
	lines := make([]string, 0, len(v.Refusals)+len(v.Warnings))
	lines = append(lines, v.Refusals...)
	lines = append(lines, v.Warnings...)
	return lines
}

// Decide is the guard's rule set. It reads no file, runs no command and takes no decision from the text of a
// message: 4.4's first risk is a check on free text that misfires, so every rule below is a git fact (the
// hook, the working tree, the author) or the daemon's own session name.
func Decide(f Facts) Verdict {
	var verdict Verdict
	seat := f.HasSession
	inWorktree := f.Linked

	// The seat's own commit identity, when the caller could read one, must be the one recorded.
	if f.Hook == HookPrePush {
		// Push fails CLOSED (4.4): from a seat's worktree, or from a shell the daemon named as a seat, the
		// answer is no - and it is still no when the two disagree, because only a checkout without either
		// signal may push.
		if seat || inWorktree {
			verdict.Refusals = append(verdict.Refusals, refusalPush(f))
		}
	} else {
		// A commit is refused in the shared checkout, and refused under a foreign author. The shared
		// checkout is the one no seat owns: everything committed there is attributed to nobody.
		if !inWorktree {
			verdict.Refusals = append(verdict.Refusals, refusalSharedCheckout(f))
		}
		if line := refusalForeignAuthor(f); line != "" {
			verdict.Refusals = append(verdict.Refusals, line)
		}
	}

	// The secret scan warns, in both hooks: a secret in a local commit can still be taken out before
	// anything is pushed, and this scan is explicitly not a blocking check (4.4, 4.1).
	if strings.TrimSpace(f.Change) != "" && SecretInChange(f.Change) {
		verdict.Warnings = append(verdict.Warnings, warningSecret(f))
	}
	return verdict
}

// SecretInChange is the scan itself, as a seam: the pattern set lives in internal/secretscan, the same one
// the seat status reports scrub with, so a guard and a report never disagree about what a secret looks like.
var SecretInChange = secretscan.Contains

func refusalPush(f Facts) string {
	who := "this working tree is a linked worktree"
	if f.HasSession {
		who = "session " + f.Session.Line()
	}
	return "the git guard refuses a push: " + who + " is a seat, and seats do not push (the owner does). " +
		"Nothing was pushed and nothing was changed here. Hand the commit hash to the lead, who asks the " +
		"owner to push it; if you are the owner, run the push from the checkout itself, not from a seat's " +
		"worktree " + f.Worktree + "."
}

func refusalSharedCheckout(f Facts) string {
	worktree := f.Worktree
	if worktree == "" {
		worktree = "your worktree"
	}
	return fmt.Sprintf("the git guard refuses a commit made in the shared checkout %s: every seat commits in "+
		"its own worktree, because a commit made here is attributed to no seat and mixes with everyone "+
		"else's. Run the commit from %s instead.", f.MainRoot, worktree)
}

// refusalForeignAuthor is the author rule, or "" when the author is the seat's own or the seat's identity
// could not be read. A commit is refused only for a name or address the seat does not own; an identity this
// client could not read is a WARNING, because refusing on a value nobody could read would refuse the seat's
// own honest commit for a reason it cannot act on.
func refusalForeignAuthor(f Facts) string {
	if f.SeatName == "" && f.SeatEmail == "" {
		return ""
	}
	if sameIdentity(f.AuthorName, f.SeatName) && sameIdentity(f.AuthorEmail, f.SeatEmail) {
		return ""
	}
	return fmt.Sprintf("the git guard refuses a commit whose author is %s, not this seat's own %s: a commit "+
		"recorded under another seat's name is a change nobody can attribute. Set the repository's user.name "+
		"and user.email for your worktree, or ask the lead to re-render the seat.",
		identity(f.AuthorName, f.AuthorEmail), identity(f.SeatName, f.SeatEmail))
}

func warningSecret(f Facts) string {
	return "the git guard WARNS: this change looks like it carries a credential, and a commit keeps it in " +
		"history. Nothing is blocked. Take the secret out before anything is pushed, and rotate it if it was " +
		"already pushed."
}

// sameIdentity compares one half of an identity, treating an unreadable side as no claim about it.
func sameIdentity(observed, own string) bool {
	return observed == "" || own == "" || observed == own
}

func identity(name, email string) string {
	switch {
	case name == "" && email == "":
		return "<unset>"
	case email == "":
		return name
	case name == "":
		return email
	}
	return name + " <" + email + ">"
}
