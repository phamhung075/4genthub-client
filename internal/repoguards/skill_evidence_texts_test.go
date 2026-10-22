// Package repoguards_test carries the WITNESS for the Skill-Evidence trailer's delivered text.
//
// THE RULE THIS FILE ENFORCES: every skill text that teaches a commit rule names the trailer and the shapes a
// value may take. The hub's commit wrapper refuses a skill commit that carries no valid trailer
// (seats/architect/RULING-2026-10-22-skill-evidence-trailer.md, I1-I7); a text that quietly stopped naming it
// would leave each seat to meet the refusal first, which is the shape the pruned-paths witness exists for.
package repoguards_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillEvidenceTexts are the delivered texts that must teach the rule: the share-level section, both seats'
// commit step, and the client tier's own copy. Exact paths, never a directory - a new text that wants the
// clause is added here by the seat that adds the clause, so a copy cannot drift unseen.
var skillEvidenceTexts = []string{
	"skills/share/seat-crew/SKILL.md",
	"skills/rooms/4genthub-min/go-dev/go-dev-workflow/SKILL.md",
	"skills/rooms/4genthub-min/fe-dev/fe-dev-workflow/SKILL.md",
	"skills/client/permanent-change/SKILL.md",
}

// TestEverySkillTextNamesTheTrailer walks the four texts: each carries the key, both token shapes a value can
// take (`qitem-…` and `none - …`), so the four copies cannot teach different rules.
func TestEverySkillTextNamesTheTrailer(t *testing.T) {
	root := repoRoot(t)
	for _, path := range skillEvidenceTexts {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, want := range []string{"Skill-Evidence:", "qitem-", "none - "} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s does not name %q, so its copy of the arms has drifted", path, want)
			}
		}
	}
}

// TestEveryCommitStepNamesTheCheckTheRulingChose: the check is the commit wrapper, and it must be named as such.
// Two reviewer corrections shaped this (qitem-20261022181254, qitem-20261022181457 measured that the wrapper does
// run in the client; qitem-20261022181928 held the arm to its ruling home): the ruling put the check where the
// PATH SET and the MESSAGE are both visible, and a commit-msg hook is blind to a pathspec commit's path set, so a
// text may not move the check to a hook. Every text names the wrapper; the share text also carries the two
// measured facts a reader needs - the hook cannot host it, and the arm is not on main yet.
func TestEveryCommitStepNamesTheCheckTheRulingChose(t *testing.T) {
	root := repoRoot(t)
	for _, path := range skillEvidenceTexts {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if want := "git_commit_capture.py"; !strings.Contains(string(body), want) {
			t.Errorf("%s does not name %q, so it points at a check the ruling did not choose", path, want)
		}
	}
	share, err := os.ReadFile(filepath.Join(root, "skills", "share", "seat-crew", "SKILL.md"))
	if err != nil {
		t.Fatalf("read seat-crew: %v", err)
	}
	for _, want := range []string{"commit-msg", "blind to a pathspec commit's", "path set", "on main yet", "lands with the parked patch"} {
		if !strings.Contains(string(share), want) {
			t.Errorf("seat-crew does not carry %q, so it moves the check to a home the ruling measured blind, or presents a parked arm as live", want)
		}
	}
}

// TestTheTextBoxNamesBothCommands proves a reader is told how to PRODUCE a value rather than only that one is
// due: the census verb and the propose verb are the two producers, and the share-level text names both.
func TestTheTextBoxNamesBothCommands(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "skills", "share", "seat-crew", "SKILL.md"))
	if err != nil {
		t.Fatalf("read seat-crew: %v", err)
	}
	for _, want := range []string{"4genteam skills evidence", "4genteam skills propose"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("seat-crew does not name %q, so a seat cannot produce a value", want)
		}
	}
}
