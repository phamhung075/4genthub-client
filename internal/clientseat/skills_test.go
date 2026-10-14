package clientseat

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeliverSkillsReplacesTheSeatDirectoryWithTheStoreCopy(t *testing.T) {
	store, state := t.TempDir(), t.TempDir()
	write(t, filepath.Join(store, "r", "rig", "agents", "lead", "skills", "seat-worktree", "SKILL.md"), "new")
	write(t, filepath.Join(state, "rooms", "r", "seats", "lead", "skills", "old-skill", "SKILL.md"), "old")
	if err := deliverSkills(store, state, "r", "lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "rooms", "r", "seats", "lead", "skills", "seat-worktree", "SKILL.md")); err != nil {
		t.Fatalf("the store skill must arrive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "rooms", "r", "seats", "lead", "skills", "old-skill")); err == nil {
		t.Fatal("a skill outside the store copy must be gone")
	}
}

func TestDeliverSkillsLeavesASeatWithoutAnAgentDirectoryAlone(t *testing.T) {
	if err := deliverSkills(t.TempDir(), t.TempDir(), "r", "architect"); err != nil {
		t.Fatalf("a non-omp seat is not an error: %v", err)
	}
}

func TestDeliverSkillsRefusesWhenTheStoreHasNoSkills(t *testing.T) {
	store, state := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "rooms", "r", "seats", "lead"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := deliverSkills(store, state, "r", "lead"); err == nil {
		t.Fatal("an omp seat with no store skills must be refused, not left on stale ones")
	}
}
