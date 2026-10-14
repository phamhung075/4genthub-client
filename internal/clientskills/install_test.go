package clientskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// developerSpec is the shape of a seat's agent spec as this machine carries one: a profile that selects
// its skills, and the resource pool those ids resolve against.
const developerSpec = `name: developer
version: "1.3.0"
profiles:
  default:
    uses:
      skills:
        - test-driven-development
      guidance: []
resources:
  skills:
    - id: test-driven-development
      path: skills/test-driven-development
  guidance:
    - id: role
      path: guidance/role.md
`

func readSpec(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func decodeSpec(t *testing.T, path string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal([]byte(readSpec(t, path)), &document); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return document
}

// declaredSkills is the resources.skills pool as id -> path.
func declaredSkills(t *testing.T, document map[string]any) map[string]string {
	t.Helper()
	declared := map[string]string{}
	resources, _ := document["resources"].(map[string]any)
	rows, _ := resources["skills"].([]any)
	for _, row := range rows {
		entry, ok := row.(map[string]any)
		if !ok {
			continue
		}
		id, _ := entry["id"].(string)
		path, _ := entry["path"].(string)
		declared[id] = path
	}
	return declared
}

// profileUses is one profile's uses.skills list, in the order the spec holds it.
func profileUses(t *testing.T, document map[string]any, profile string) []string {
	t.Helper()
	profiles, _ := document["profiles"].(map[string]any)
	block, _ := profiles[profile].(map[string]any)
	uses, _ := block["uses"].(map[string]any)
	rows, _ := uses["skills"].([]any)
	out := []string{}
	for _, row := range rows {
		if name, ok := row.(string); ok {
			out = append(out, name)
		}
	}
	return out
}

// The delivery, end to end on a fixture tree: the seat's skills path receives share/, its room's and its
// own seat's skills - every file of each one - and nothing at all from another room, another seat or the
// client kind.
func TestInstallDeliversTheSeatSkillsAndNothingElse(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/plain/SKILL.md":              "# plain",
		"share/plain/reference/deep.md":     "# a file beside SKILL.md",
		"rooms/alpha/room-skill/SKILL.md":   "# room",
		"rooms/alpha/dev/dev-only/SKILL.md": "# dev",
		"rooms/alpha/qa/qa-only/SKILL.md":   "# qa",
		"rooms/beta/beta-only/SKILL.md":     "# beta",
		"client/client-skill/SKILL.md":      "# the client's own",
	})
	agentDir := t.TempDir()

	delivered, err := Install(tree, "alpha", "dev", agentDir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if strings.Join(delivered, ",") != "dev-only,plain,room-skill" {
		t.Fatalf("delivered %v, want [dev-only plain room-skill]", delivered)
	}
	for _, name := range delivered {
		if !exists(filepath.Join(agentDir, SkillsDir, name, SkillFile)) {
			t.Fatalf("%s/%s was not installed", name, SkillFile)
		}
	}
	if !exists(filepath.Join(agentDir, SkillsDir, "plain", "reference", "deep.md")) {
		t.Fatal("the file beside SKILL.md was not copied")
	}
	for _, absent := range []string{"qa-only", "beta-only", "client-skill"} {
		if exists(filepath.Join(agentDir, SkillsDir, absent)) {
			t.Fatalf("%s was installed into this seat", absent)
		}
	}
}

// A second install converges on the tree: the tree is the source of truth for the skills the client
// owns, so an edit made in the skills path does not survive the next delivery.
func TestInstallConvergesOnTheTree(t *testing.T) {
	tree := skillTree(t, map[string]string{"share/plain/SKILL.md": "# the tree's copy"})
	agentDir := t.TempDir()
	if _, err := Install(tree, "alpha", "dev", agentDir); err != nil {
		t.Fatalf("install: %v", err)
	}
	installed := filepath.Join(agentDir, SkillsDir, "plain", SkillFile)
	if err := os.WriteFile(installed, []byte("# edited in place"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if _, err := Install(tree, "alpha", "dev", agentDir); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if got := readSpec(t, installed); got != "# the tree's copy" {
		t.Fatalf("the second install left %q, want the tree's copy", got)
	}
}

// Declaring is what makes the rig orchestrator project a skill, so the id must land BOTH in the resource pool and in
// the profiles that select resources - and the spec's own content must survive untouched.
func TestDeclareAddsTheResourceAndTheProfileUse(t *testing.T) {
	path := writeSpec(t, developerSpec)

	changed, err := Declare(path, []string{"plain", "room-skill"})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if !changed {
		t.Fatal("declare reported no change on a spec that lacked both ids")
	}

	document := decodeSpec(t, path)
	declared := declaredSkills(t, document)
	if declared["plain"] != "skills/plain" || declared["room-skill"] != "skills/room-skill" {
		t.Fatalf("pool holds %v, want both ids at skills/<id>", declared)
	}
	if declared["test-driven-development"] != "skills/test-driven-development" {
		t.Fatal("the spec's own declaration was lost")
	}
	if got := strings.Join(profileUses(t, document, "default"), ","); got != "test-driven-development,plain,room-skill" {
		t.Fatalf("profile selects [%s], want the existing id then the two new ones", got)
	}
	resources, _ := document["resources"].(map[string]any)
	if _, ok := resources["guidance"]; !ok {
		t.Fatal("the spec's guidance resource was dropped")
	}
}

func TestDeclareIsStableWhenEverythingIsAlreadyDeclared(t *testing.T) {
	path := writeSpec(t, developerSpec)
	if _, err := Declare(path, []string{"plain"}); err != nil {
		t.Fatalf("first declare: %v", err)
	}
	after := readSpec(t, path)

	changed, err := Declare(path, []string{"plain"})
	if err != nil {
		t.Fatalf("second declare: %v", err)
	}
	if changed {
		t.Fatal("a second declare reported a change")
	}
	if readSpec(t, path) != after {
		t.Fatal("a no-op declare rewrote the spec")
	}
}

// A spec with no profiles is refused: nothing would select the skills, and inventing a profile would be
// this client deciding a seat's posture.
func TestDeclareRefusesASpecWithNoProfiles(t *testing.T) {
	path := writeSpec(t, "name: developer\nresources:\n  skills: []\n")

	_, err := Declare(path, []string{"plain"})
	if err == nil || !strings.Contains(err.Error(), "profiles") {
		t.Fatalf("err = %v, want a refusal naming profiles", err)
	}
}

// A spec with profiles but no resource pool yet gets one: the seat exists and its profile selects
// resources, it simply had nothing to select before.
func TestDeclareCreatesTheResourcePoolWhenTheSpecHasNone(t *testing.T) {
	path := writeSpec(t, "name: developer\nprofiles:\n  default:\n    uses:\n      skills: []\n")

	changed, err := Declare(path, []string{"plain"})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if !changed {
		t.Fatal("declare reported no change")
	}
	document := decodeSpec(t, path)
	if declaredSkills(t, document)["plain"] != "skills/plain" {
		t.Fatalf("pool holds %v, want plain at skills/plain", declaredSkills(t, document))
	}
	if got := strings.Join(profileUses(t, document, "default"), ","); got != "plain" {
		t.Fatalf("profile selects [%s], want [plain]", got)
	}
}

// The two halves together, and the property that matters: every skill this client declares is a path it
// actually installed. A spec pointing at a directory that is not there is a seat that boots without the
// skill it was told it has.
func TestEveryDeclaredSkillIsOnTheSeatFilesystem(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/plain/SKILL.md":              "# plain",
		"rooms/alpha/dev/dev-only/SKILL.md": "# dev",
	})
	agentDir := t.TempDir()
	spec := filepath.Join(agentDir, "agent.yaml")
	if err := os.WriteFile(spec, []byte(developerSpec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	delivered, err := Install(tree, "alpha", "dev", agentDir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Declare(spec, delivered); err != nil {
		t.Fatalf("declare: %v", err)
	}

	declared := declaredSkills(t, decodeSpec(t, spec))
	for _, name := range delivered {
		relative := declared[name]
		if relative == "" {
			t.Fatalf("%s was installed but not declared", name)
		}
		if !exists(filepath.Join(agentDir, filepath.FromSlash(relative), SkillFile)) {
			t.Fatalf("the spec declares %s, which is not on disk", relative)
		}
	}
}
