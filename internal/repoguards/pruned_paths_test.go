// Package repoguards_test is the WITNESS for the prune that retired this repo's changelog tree.
//
// THE RULE THIS FILE ENFORCES: a path a prune deleted has no live reference and stays deleted.
// testdata/pruned_paths.txt lists what a prune removed; these tests assert every entry is gone from the
// index and that no tracked file outside an explicit allowlist names one.
//
// WHY IT EXISTS: 3c9c95a7 deleted CHANGELOG/ (269 files), CHANGELOG.md and TEST-CHANGELOG.md under the
// 2026-10-21 owner ruling that the commit body is the change record. That commit's own claim is a
// sentence; a seat that re-adds CHANGELOG/<date>--<title>.md, or a skill that sends the next seat to
// write one, broke nothing measurable. The hub learned the same lesson on 2026-10-18, when a prune's
// record claimed no live reference survives it while four did (agenthub_go/internal/repoguards).
//
// History of a removal lives in the commit that made it (`git log --diff-filter=D`), not in a file.
package repoguards_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// allowedLiveReferences names the files that may still contain a pruned path as a string, and why. The
// allowlist is exact files, never directories or prefixes: a whole directory on it would hide the next
// drift inside. Five files, the number the ruling measured; the two workflow skills that carried a dead
// "TEST-CHANGELOG.md line" clause lost the clause rather than gaining a row here, because a retired
// convention needs no hand-out.
var allowedLiveReferences = map[string]string{
	// The 2026-10-09 incident where a `git restore` dropped a CHANGELOG.md layer.
	"skills/client/watch-team/SKILL.md": "the measurement behind the rule it now teaches",
	// A dated ruling record whose example row names a path.
	"skills/share/queue-row-schema/RULING.md": "a quoted queue row, whose paths are data",
	// The sample instruction the jev guard must refuse, and the numstat fixture it reads.
	"internal/clientjev/jev_test.go": "commit-path fixtures for another repo's trailer",
	// The doc-classifier fixtures for the path it classifies.
	"internal/clientroom/report_test.go": "path-classification fixtures (a doc edit, a numstat line)",
	// The skip that keeps a retired verb out of a retired tree.
	"internal/retiredverbs/retiredverbs.go": "the comment that records why the scan skips the tree",
	// The guard's own data and code must name what they scan for.
	"internal/repoguards/pruned_paths_test.go":      "the guard itself",
	"internal/repoguards/testdata/pruned_paths.txt": "the guard's data",
}

// TestAPrunedPathWasDeletedByACommit pins the listing's other end: every entry must be a path that
// existed in history and was deleted there. Without this a typo sits in the data file forever, matching
// nothing and guarding nothing - the list would look maintained while being blind.
func TestAPrunedPathWasDeletedByACommit(t *testing.T) {
	root := repoRoot(t)
	// A SHALLOW CLONE CANNOT ANSWER THIS, AND IT MUST SAY SO RATHER THAN ACCUSE THE LISTING: at depth 1
	// `git log --all` sees no deletion commits, and every entry would be reported as a typo.
	if strings.TrimSpace(runGit(t, root, "rev-parse", "--is-shallow-repository")) == "true" {
		t.Fatalf("this checkout is a SHALLOW clone, so `git log --all` cannot see the deletion commits the listing is judged against - the entries are not typos, the history is missing. Check out with full history (`actions/checkout` with `fetch-depth: 0`), then rerun.")
	}
	pruned := readPrunedPaths(t, filepath.Join(root, "internal", "repoguards", "testdata", "pruned_paths.txt"))
	if len(pruned) == 0 {
		t.Fatal("testdata/pruned_paths.txt lists nothing: an empty guard passes everything")
	}
	for _, path := range pruned {
		// --all, because a prune may land on a branch that is not an ancestor of this checkout. The
		// trailing slash that marks a directory entry is trimmed for the pathspec: git resolves
		// "CHANGELOG" to the tree whose files were deleted, and treats "CHANGELOG/" as no path at all.
		if strings.TrimSpace(runGit(t, root, "log", "--all", "--diff-filter=D", "--format=%H", "-1", "--", strings.TrimSuffix(path, "/"))) == "" {
			t.Errorf("%s has no deletion commit in history: the entry is a typo or a path that was never tracked", path)
		}
	}
}

// TestAPrunedPathHasNoLiveReference walks the prune's payload in both directions: a listed path that is
// tracked again is refused (the tree came back, and with it the retired record), and a live reference
// outside the allowlist is refused (a skill or a doc still sends a seat to the removed file).
func TestAPrunedPathHasNoLiveReference(t *testing.T) {
	root := repoRoot(t)
	pruned := readPrunedPaths(t, filepath.Join(root, "internal", "repoguards", "testdata", "pruned_paths.txt"))
	if len(pruned) == 0 {
		t.Fatal("testdata/pruned_paths.txt lists nothing: an empty guard passes everything")
	}

	tracked := trackedFiles(t, root)
	for _, path := range pruned {
		if strings.HasSuffix(path, "/") {
			for file := range tracked {
				if strings.HasPrefix(file, path) {
					t.Errorf("%s is tracked again under the pruned directory %s: the retired tree came back", file, path)
					break
				}
			}
			continue
		}
		if tracked[path] {
			t.Errorf("%s is tracked again: a pruned path that exists again means the list is stale or the prune did not land", path)
		}
	}

	for _, hit := range grepTracked(t, root, pruned) {
		if _, ok := allowedLiveReferences[hit.file]; ok {
			continue
		}
		t.Errorf("%s:%d names the pruned path %q; the prune removed it, so the line is a dangling citation (the record is the commit body)", hit.file, hit.line, hit.path)
	}
}

// prunedHit is one grep match: the file, the line number and which listed path it named.
type prunedHit struct {
	file string
	line int
	path string
}

// readPrunedPaths reads the listing, refusing a line that is neither one exact path nor one directory
// ending in "/": a glob or a trailing comment would make the fixed-string scan search for something else.
func readPrunedPaths(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open the pruned-path listing: %v", err)
	}
	defer f.Close()
	var paths []string
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, "*?[ ") {
			t.Fatalf("%s:%d: %q is not a single exact path; the scan is a fixed-string grep", path, n, line)
		}
		paths = append(paths, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the pruned-path listing: %v", err)
	}
	return paths
}

// trackedFiles returns the index as a set, so the listing can be checked in one pass.
func trackedFiles(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := runGit(t, root, "ls-files", "-z")
	tracked := make(map[string]bool)
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			tracked[path] = true
		}
	}
	return tracked
}

// grepTracked runs ONE fixed-string grep over the tracked tree with the listing as its pattern file,
// and returns every match.
//
// The listing is written to a scratch file WITHOUT its comments first: `git grep -f` takes every line
// as a pattern, so a `#` header would be searched for literally and match half the tree (measured in the
// hub on 2026-10-18: 6097 hits and 30 seconds before that fix, two files and under two seconds after).
func grepTracked(t *testing.T, root string, pruned []string) []prunedHit {
	t.Helper()
	if len(pruned) == 0 {
		return nil
	}
	patterns := filepath.Join(t.TempDir(), "patterns.txt")
	if err := os.WriteFile(patterns, []byte(strings.Join(pruned, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write the scratch pattern file: %v", err)
	}
	cmd := exec.Command("git", "--no-optional-locks", "grep", "-F", "-n", "-I", "-f", patterns, "--", ".")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil // exit 1 is "no match", not a failure
		}
		t.Fatalf("git grep over the pruned paths: %v", err)
	}
	var hits []prunedHit
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			t.Fatalf("unreadable git grep line %q", line)
		}
		file, text := parts[0], parts[2]
		var number int
		if _, err := fmt.Sscanf(parts[1], "%d", &number); err != nil {
			t.Fatalf("unreadable line number in %q", line)
		}
		for _, p := range pruned {
			if strings.Contains(text, p) {
				hits = append(hits, prunedHit{file: file, line: number, path: p})
			}
		}
	}
	return hits
}

// repoRoot walks up from this package's own directory until it finds the repository root (the directory
// holding .git). No env var and no cwd assumption: a test binary run from the package directory, from a
// worktree or from the main checkout all land here.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no .git above %s", dir)
		}
		dir = parent
	}
}

// runGit runs one git command in the checkout and returns its stdout.
func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
