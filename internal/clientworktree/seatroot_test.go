// SeatProjectRoot reads the repository the room's seats live in from their own directories. These cases
// are real repositories and real worktrees, like the rest of the package: the answer IS what git reports,
// so a stubbed git could not tell a main checkout from a linked worktree.
package clientworktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plainRepo is one committed repository in a fresh temporary directory.
func plainRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	mkdirAll(t, dir)
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "README.md"), "a repository\n")
	gitIn(t, dir, "add", "README.md")
	gitIn(t, dir, "commit", "-q", "-m", "a repository")
	return dir
}

func TestSeatProjectRootNamesTheRepositoryBehindAWorktree(t *testing.T) {
	requireGit(t)
	mainRoot := plainRepo(t)
	worktree := filepath.Join(t.TempDir(), "wt", "lead")
	mkdirAll(t, filepath.Dir(worktree))
	gitIn(t, mainRoot, "worktree", "add", "-q", "-b", "4genthub-min/lead", worktree, "main")

	// The main checkout and one of its worktrees are ONE repository, so the room's answer is the main
	// checkout's root whichever of the two happens to answer first.
	root, ok, err := SeatProjectRoot([]string{worktree, mainRoot})
	if err != nil || !ok {
		t.Fatalf("SeatProjectRoot = %q, %v, %v; want the repository and no error", root, ok, err)
	}
	if root != filepath.Clean(mainRoot) {
		t.Errorf("SeatProjectRoot = %q, want %q", root, mainRoot)
	}
}

// Two repositories cannot be one root: the caller keeps its own answer and says the disagreement rather
// than deriving half a room into a tree the other half does not live in.
func TestSeatProjectRootRefusesTwoRepositories(t *testing.T) {
	requireGit(t)
	first, second := plainRepo(t), plainRepo(t)

	root, ok, err := SeatProjectRoot([]string{first, second})
	if err == nil {
		t.Fatalf("SeatProjectRoot = %q, %v with no error; want the disagreement named", root, ok)
	}
	if ok || root != "" {
		t.Errorf("SeatProjectRoot = %q, %v; want no root when the seat directories disagree", root, ok)
	}
	for _, want := range []string{filepath.Clean(first), filepath.Clean(second)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
}

// A definition whose seat paths do not exist yet - a room that has never been derived - answers nothing,
// which is not an error: the working directory's root stands.
func TestSeatProjectRootSaysNothingWhenNoSeatDirectoryIsReadable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	plain := filepath.Join(t.TempDir(), "plain-directory")
	mkdirAll(t, plain)

	root, ok, err := SeatProjectRoot([]string{missing, plain, ""})
	if err != nil {
		t.Fatalf("SeatProjectRoot: %v", err)
	}
	if ok || root != "" {
		t.Errorf("SeatProjectRoot = %q, %v; want no answer when no directory is a repository", root, ok)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Errorf("the case expects %s to be absent: %v", missing, statErr)
	}
}
