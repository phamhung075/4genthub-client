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

// TestEveryCommitStepNamesTheGuardAClientCommitRuns: the check must name a hook a commit reaches, and must say
// whether that check is LIVE. The reviewer's corrections (qitem-20261022181254, qitem-20261022181457) measured
// both halves: the wrapper does run in the client (30 captures 10-18..10-21 in the client's git-dir), so naming
// it is right, and its skills arm is NOT on main, so a text that calls it a refusal today names a gate that does
// not run. The client's `commit-msg` shim and the guard it execs are the hook a plain `git commit` needs; both
// names are required in every text, and the share text must carry the landing state.
func TestEveryCommitStepNamesTheGuardAClientCommitRuns(t *testing.T) {
	root := repoRoot(t)
	for _, path := range skillEvidenceTexts {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, want := range []string{"scripts/git-hooks/commit-msg", "task-trailer-guard"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s does not name %q, so it points at a check a client commit never reaches", path, want)
			}
		}
	}
	share, err := os.ReadFile(filepath.Join(root, "skills", "share", "seat-crew", "SKILL.md"))
	if err != nil {
		t.Fatalf("read seat-crew: %v", err)
	}
	for _, want := range []string{"on main yet", "lands with the parked patch"} {
		if !strings.Contains(string(share), want) {
			t.Errorf("seat-crew does not carry %q, so it presents a parked arm as a live refusal", want)
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
