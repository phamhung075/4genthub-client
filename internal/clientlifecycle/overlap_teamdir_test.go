package clientlifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// A room created from its own team copy (<state root>/rooms/<room>/team) has no entry under the skills
// checkout. The scan used to read only the checkout and logged "cannot load team definition" every ten
// minutes for aa2-rs (agent.log, 2026-10-27); it must find the room's own copy.
func TestOverlapTeamDirFallsBackToTheRoomsOwnTeamCopy(t *testing.T) {
	checkout, root := t.TempDir(), t.TempDir()
	t.Setenv(clientservices.SkillsRootEnv, checkout)
	own := filepath.Join(root, "rooms", "aa2-rs", "team")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "team.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := overlapTeamDir(root, "aa2-rs")
	if err != nil || got != own {
		t.Fatalf("overlapTeamDir = %q, %v; want %q", got, err, own)
	}

	// A room the checkout defines keeps the checkout's definition.
	defined := filepath.Join(checkout, "scripts", "team", "aa2-rs")
	if err := os.MkdirAll(defined, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defined, "team.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := overlapTeamDir(root, "aa2-rs"); got != defined {
		t.Fatalf("overlapTeamDir = %q; want the checkout's %q", got, defined)
	}

	// Neither: the checkout path is still the answer, so the note names what was tried.
	if got, _ := overlapTeamDir(root, "other"); got != filepath.Join(checkout, "scripts", "team", "other") {
		t.Fatalf("overlapTeamDir for an undefined room = %q", got)
	}
}
