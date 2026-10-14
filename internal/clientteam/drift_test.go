package clientteam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provenanceBlock is one stored skill block's content JSON, built the way the publish path builds it.
func provenanceBlock(t *testing.T, sourcePath, sourceSHA, mirrorPath, mirrorSHA string) string {
	t.Helper()
	block := newPyObject().set("source_path", sourcePath).set("sha256", sourceSHA)
	if mirrorPath != "" {
		block.set("mirror_path", mirrorPath).set("mirror_sha256", mirrorSHA)
	}
	return pyCompact(block)
}

func driftArgs(root string) []string {
	return []string{"drift-check", "--root", root, "--library-root", filepath.Join(root, "library-root")}
}

// Mirrors tests/test_team_setup.py::test_drift_check_clean_tree_reports_nothing.
func TestDriftCheckCleanTreeReportsNothing(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	alpha, beta := projectSkills["alpha-skill"], projectSkills["beta-skill"]
	fake.storeSkillBlock("alpha-skill", "1.0.0", skillKind,
		provenanceBlock(t, ".claude/skills/alpha-skill/SKILL.md", sha256Hex([]byte(alpha)), "", ""))
	fake.storeSkillBlock("beta-skill", "1.0.0", skillKind,
		provenanceBlock(t, ".claude/skills/beta-skill/SKILL.md", sha256Hex([]byte(beta)),
			".claude/skills/alpha-skill/SKILL.md", sha256Hex([]byte(alpha))))
	// An mcp block is never a skill block, even without provenance, so it is not a finding.
	fake.storeSkillBlock("weather", "1.0.0", "mcp", "{}")

	code, out, errOut := runTeam(t, driftArgs(root)...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if out != "" || errOut != "" {
		t.Errorf("a clean tree printed %q / %q", out, errOut)
	}
	for _, request := range fake.recorded() {
		if request.method != "GET" {
			t.Errorf("drift-check wrote: %s %s", request.method, request.path)
		}
	}
}

// Mirrors test_drift_check_names_the_skill_and_both_hashes.
func TestDriftCheckNamesTheSkillAndBothHashes(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	stored := sha256Hex([]byte("some other bytes\n"))
	fake.storeSkillBlock("alpha-skill", "1.0.0", skillKind,
		provenanceBlock(t, ".claude/skills/alpha-skill/SKILL.md", stored, "", ""))

	code, out, errOut := runTeam(t, driftArgs(root)...)
	if code != ExitDrift {
		t.Fatalf("exit %d, want %d", code, ExitDrift)
	}
	if out != "" {
		t.Errorf("findings belong on stderr; stdout was %q", out)
	}
	want := []string{
		driftHeader,
		"  alpha-skill: .claude/skills/alpha-skill/SKILL.md (digest @ " + root + ") stored " + stored +
			" computed " + sha256Hex([]byte(projectSkills["alpha-skill"])),
	}
	if got := strings.Split(strings.TrimRight(errOut, "\n"), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("stderr =\n%s\nwant\n%s", errOut, strings.Join(want, "\n"))
	}
	for _, request := range fake.recorded() {
		if request.method != "GET" {
			t.Errorf("drift-check wrote: %s %s", request.method, request.path)
		}
	}
}

// Mirrors test_drift_check_reports_a_missing_source_file.
func TestDriftCheckReportsAMissingSourceFile(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	fake.storeSkillBlock("alpha-skill", "1.0.0", skillKind,
		provenanceBlock(t, ".claude/skills/gone/SKILL.md", sha256Hex([]byte("x")), "", ""))

	code, _, errOut := runTeam(t, driftArgs(root)...)
	if code != ExitDrift {
		t.Fatalf("exit %d, want %d", code, ExitDrift)
	}
	want := "  alpha-skill: .claude/skills/gone/SKILL.md (missing-source) no file under " +
		root + " or " + filepath.Join(root, "library-root")
	if !strings.Contains(errOut, want) {
		t.Errorf("stderr missing %q\n%s", want, errOut)
	}
}

// Mirrors test_drift_check_reports_a_block_without_provenance.
func TestDriftCheckReportsABlockWithoutProvenance(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	fake.storeSkillBlock("alpha-skill", "1.0.0", skillKind, projectSkills["alpha-skill"])

	code, _, errOut := runTeam(t, driftArgs(root)...)
	if code != ExitDrift {
		t.Fatalf("exit %d, want %d", code, ExitDrift)
	}
	if !strings.Contains(errOut, "  alpha-skill: stored 1.0.0 (no-provenance) carries no source_path/sha256") {
		t.Errorf("stderr missing the no-provenance line:\n%s", errOut)
	}
}

// Mirrors test_drift_check_reports_a_divergent_mirror_pair: the source matches disk and the mirror
// does not, so there is exactly one mirror finding and no digest one.
func TestDriftCheckReportsADivergentMirrorPair(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	mirrorText := "# Mirror\nbody\n"
	if err := writeSkillFile(t, filepath.Join(root, ".claude", "skills", "mirror-skill"), mirrorText); err != nil {
		t.Fatal(err)
	}
	mirrorStored := sha256Hex([]byte("a different mirror\n"))
	fake.storeSkillBlock("alpha-skill", "1.0.0", skillKind,
		provenanceBlock(t, ".claude/skills/alpha-skill/SKILL.md", sha256Hex([]byte(projectSkills["alpha-skill"])),
			".claude/skills/mirror-skill/SKILL.md", mirrorStored))

	code, _, errOut := runTeam(t, driftArgs(root)...)
	if code != ExitDrift {
		t.Fatalf("exit %d, want %d", code, ExitDrift)
	}
	want := []string{
		driftHeader,
		"  alpha-skill: .claude/skills/mirror-skill/SKILL.md (mirror @ " + root + ") stored " + mirrorStored +
			" computed " + sha256Hex([]byte(mirrorText)),
	}
	got := strings.Split(strings.TrimRight(errOut, "\n"), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("stderr =\n%s\nwant\n%s", errOut, strings.Join(want, "\n"))
	}
}

// Mirrors test_drift_check_resolves_a_library_path_under_library_root.
func TestDriftCheckResolvesALibraryPathUnderLibraryRoot(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	library := filepath.Join(root, "library-root")
	text := "# Library\nbody\n"
	if err := writeSkillFile(t, filepath.Join(library, "skills/_canonical/core/library-skill"), text); err != nil {
		t.Fatal(err)
	}
	fake.storeSkillBlock("library-skill", "1.0.0", skillKind,
		provenanceBlock(t, "skills/_canonical/core/library-skill/SKILL.md", sha256Hex([]byte(text)), "", ""))

	code, out, errOut := runTeam(t, driftArgs(root)...)
	if code != 0 {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, errOut)
	}
}

// Mirrors test_drift_check_library_root_defaults_to_the_skills_root.
func TestDriftCheckLibraryRootDefaultsToTheEnvironment(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	library := filepath.Join(root, "env-library")
	text := "# Env Library\nbody\n"
	if err := writeSkillFile(t, filepath.Join(library, "skills/_canonical/core/env-skill"), text); err != nil {
		t.Fatal(err)
	}
	t.Setenv(libraryRootEnv, library)
	fake.storeSkillBlock("env-skill", "1.0.0", skillKind,
		provenanceBlock(t, "skills/_canonical/core/env-skill/SKILL.md", sha256Hex([]byte(text)), "", ""))

	code, out, errOut := runTeam(t, "drift-check", "--root", root)
	if code != 0 {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, errOut)
	}
}

// Mirrors test_drift_check_separates_mismatch_from_no_provenance: both categories are reported, one
// line each, in the order the cloud listed the modules.
func TestDriftCheckSeparatesMismatchFromNoProvenance(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	stored := sha256Hex([]byte("other\n"))
	fake.storeSkillBlock("alpha-skill", "1.0.0", skillKind,
		provenanceBlock(t, ".claude/skills/alpha-skill/SKILL.md", stored, "", ""))
	fake.storeSkillBlock("beta-skill", "1.0.0", skillKind, projectSkills["beta-skill"])

	code, _, errOut := runTeam(t, driftArgs(root)...)
	if code != ExitDrift {
		t.Fatalf("exit %d, want %d", code, ExitDrift)
	}
	if !strings.Contains(errOut, "(digest @ ") {
		t.Errorf("the mismatch finding is missing:\n%s", errOut)
	}
	if !strings.Contains(errOut, "(no-provenance)") {
		t.Errorf("the no-provenance finding is missing:\n%s", errOut)
	}
}

// drift-check refuses with no credentials, like every other verb that reads the cloud.
func TestDriftCheckNeedsCredentials(t *testing.T) {
	installFake(t)
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	code, _, errOut := runTeam(t, "drift-check")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "AGENTHUB_URL is not set") {
		t.Errorf("stderr did not name the missing half: %q", errOut)
	}
}

// The inventory's own guard: an inventory that is not an object with a skills array is refused.
func TestPublishSkillsRefusesAnInventoryWithoutSkills(t *testing.T) {
	installFake(t)
	library := writeLibrary(t)
	path := filepath.Join(library, "inventory.json")
	if err := writeSkillFile(t, filepath.Join(library, "empty"), "x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runTeam(t, "publish-skills", "--inventory", path, "--source-root", library, "--dry-run")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "skills array") {
		t.Errorf("stderr did not name the missing array: %q", errOut)
	}
}
