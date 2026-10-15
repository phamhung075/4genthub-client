package retiredverbs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from this package to the module root (the directory holding go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// ownPackageDir is this pin's own directory, repo-relative. The pin is itself a non-test Go file, so the
// scanner finds its table row and Allowed[0]'s own Program/Match literals; the count in the pin's own test
// is split along exactly this line so the delivered corpus is visible on its own.
func ownPackageDir(t *testing.T, root string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatalf("rel(%s, %s): %v", root, dir, err)
	}
	return filepath.ToSlash(rel)
}

// allowedReason answers whether an allowlist entry names THIS finding: same file, same command, and the
// entry's Match present in the text the span was found in.
func allowedReason(f Finding) (string, bool) {
	for _, a := range Allowed {
		if a.File == f.File && a.Program == f.Program && a.Subverb == f.Subverb &&
			strings.Contains(f.Text, a.Match) {
			return a.Reason, true
		}
	}
	return "", false
}

// TestTheDeliveredTextNamesNoRetiredCommandAsAnInstruction is the pin: the printed literals, the skills and
// the docs name no retired program unless an allowlist entry says why that mention is legitimate.
func TestTheDeliveredTextNamesNoRetiredCommandAsAnInstruction(t *testing.T) {
	root := repoRoot(t)
	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}

	// THE COUNT IS SPLIT ALONG THE LINE THAT MATTERS: the pin's own data is not delivered text. This package
	// is itself a non-test Go file, so the scanner finds its own table row and Allowed[0]'s own Program/Match
	// literals - three spans that are statements about the class rather than text a reader is handed. The
	// delivered count is what this pin protects, and it is reported on its own.
	ownDir := ownPackageDir(t, root)
	unexpected := []Finding{}
	delivered, deliveredAllowed, own := 0, 0, 0
	for _, f := range findings {
		isOwn := strings.HasPrefix(f.File, ownDir+"/")
		if isOwn {
			own++
		} else {
			delivered++
		}
		reason, ok := allowedReason(f)
		if !ok {
			unexpected = append(unexpected, f)
			continue
		}
		if !isOwn {
			deliveredAllowed++
		}
		t.Logf("allowlisted: %s: `%s` - %s", f.File, f.Span, reason)
	}
	if len(unexpected) > 0 {
		t.Errorf("%d command span(s) name a retired program and no allowlist entry carries them:", len(unexpected))
		for _, f := range unexpected {
			t.Errorf("  %s", f)
		}
	}
	t.Logf("%d command span(s) name a retired program: %d in the delivered text, %d of them allowlisted with a reason; %d in this pin's own data under %s/",
		len(findings), delivered, deliveredAllowed, own, ownDir)
}

// TestEveryAllowlistEntryStillCoversASpan keeps the allowlist from rotting into a licence: an entry whose
// line was reworded or removed covers nothing, and that has to be read again rather than left in place.
func TestEveryAllowlistEntryStillCoversASpan(t *testing.T) {
	root := repoRoot(t)
	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	for _, a := range Allowed {
		if strings.TrimSpace(a.Reason) == "" || strings.TrimSpace(a.Match) == "" {
			t.Errorf("the allowlist entry for %s (%s %s) carries no both-named mention and reason",
				a.File, a.Program, a.Subverb)
			continue
		}
		covered := false
		for _, f := range findings {
			if f.File == a.File && f.Program == a.Program && f.Subverb == a.Subverb &&
				strings.Contains(f.Text, a.Match) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("the allowlist entry for %s (%s %s, matched by %q) covers nothing: the line was reworded or removed, so read it again and update or drop the entry",
				a.File, a.Program, a.Subverb, a.Match)
		}
	}
}

// TestTheScannerTellsACommandFromProse is the scanner's own teeth, on inputs it cannot get wrong by
// accident: an instruction at a line start, a backticked command inline, and the prose that must NOT be a
// span. A scanner that flagged prose would fill the allowlist with English, and one that missed a
// backticked command would miss exactly the lines this pod prints.
func TestTheScannerTellsACommandFromProse(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"rig up <spec.yaml> --target <dir>", []string{"rig up"}},
		{"$ rig ps --nodes --rig <RIG>", []string{"rig ps"}},
		{"A line typed there is sent to the seat with `rig send`, so it goes through the same route", []string{"rig send"}},
		{"`pi-runner.js` is retired", []string{"pi-runner.js"}},
		{"the daemon said the rig up the tree was stale", nil},
		{"cannot read the seat identity (no runtime, no rig daemon and no seat environment)", nil},
		{"it replaced `4genteam ps <rig> --nodes`", nil},
		{"4genteam send <session> 'message' --verify", nil},
		// A contrast IS a span: the scanner extracts, and the allowlist is what says it is legitimate. This
		// case is here so nobody "fixes" the scanner into judging intent.
		{"NEVER launch with the predecessor orchestrator's `rig up`", []string{"rig up"}},
	}
	for _, c := range cases {
		got := []string{}
		for _, m := range spans(c.line) {
			got = append(got, m.Text)
		}
		if len(got) != len(c.want) {
			t.Errorf("spans(%q) = %v, want %v", c.line, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("spans(%q) = %v, want %v", c.line, got, c.want)
				break
			}
		}
	}
}
