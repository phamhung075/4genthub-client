// The director rule, git's side. A room may put a principal above its lead - a director, a session outside the
// room that alone updates the protected branch, while the founder pushes (aa2-rs, 2026-10-28). The room
// declares it by holding <roomDir>/director/, the directory the director keeps its ready list in; the seat
// guard (rust/agenthub-runtime/assets/seat-guard.ts) reads the same declaration. A room without it keeps the
// lead's merge and push right unchanged.
package clientguard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
)

// HookReferenceTransaction is git's name for the hook every ref update runs through.
const HookReferenceTransaction = "reference-transaction"

// LanesRootEnv is the room's `wt/` the runtime hands every seat; its parent is the room's directory.
const LanesRootEnv = "AGENTHUB_LANES_ROOT"

// DefaultProtectedBranch is the branch a director room keeps for its director when team.json names none.
const DefaultProtectedBranch = "main"

// roomDirFor is the room's directory: the parent of the lane root the runtime handed this seat, else
// <state root>/rooms/<room>. "" when neither can be named.
func roomDirFor(session Session, getenv func(string) string) string {
	if lanes := strings.TrimSpace(getenv(LanesRootEnv)); lanes != "" {
		return filepath.Dir(filepath.Clean(lanes))
	}
	root := clientenv.DefaultStateRoot()
	if root == "" || session.Room == "" {
		return ""
	}
	return filepath.Join(root, "rooms", session.Room)
}

// DirectorRoom answers whether the room a seat's shell belongs to declares a director, and the room's
// directory. A directory that cannot be read is no declaration.
func DirectorRoom(session Session, getenv func(string) string) (string, bool) {
	dir := roomDirFor(session, getenv)
	if dir == "" {
		return "", false
	}
	info, err := os.Stat(filepath.Join(dir, "director"))
	return dir, err == nil && info.IsDir()
}

// ProtectedBranch is the branch only the director moves: team.json's room.protected_branch, else main.
func ProtectedBranch(roomDir string) string {
	if name := clientteam.RoomProtectedBranch(filepath.Join(roomDir, "team")); name != "" {
		return name
	}
	return DefaultProtectedBranch
}

// refusalDirectorRef is what a seat reads when its ref update would move the protected branch.
func refusalDirectorRef(session Session, roomDir, branch string) string {
	return fmt.Sprintf("4genteam git guard: refused - room %s has a director (%s), and only the director updates %s. "+
		"Seat %s: gate the branch and list it as <branch>=<sha> with the gate's verdict in %s; the director merges "+
		"it, and only the founder or the director pushes. Nothing was changed.",
		session.Room, filepath.Join(roomDir, "director"), branch, session.Seat,
		filepath.Join(roomDir, "director", "ready.md"))
}

// runReferenceTransaction is the hook's decision: only the `prepared` state is judged (a refusal there aborts
// the whole transaction before any ref moves), only a seat's shell, and only in a director room. stdin is
// git's "<old> <new> <ref>" lines.
func runReferenceTransaction(args []string, stdin io.Reader, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || args[0] != "prepared" {
		return 0
	}
	session, ok := SessionFromEnv(getenv)
	if !ok {
		return 0
	}
	roomDir, director := DirectorRoom(session, getenv)
	if !director {
		return 0
	}
	branch := ProtectedBranch(roomDir)
	protected := "refs/heads/" + branch
	scanner := bufio.NewScanner(stdin)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[2] == protected {
			fmt.Fprintln(stderr, refusalDirectorRef(session, roomDir, branch))
			return ExitRefused
		}
	}
	return 0
}
