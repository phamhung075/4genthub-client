package clientteam

import (
	"path/filepath"
	"strings"
	"testing"
)

// Mirrors tests/test_team_setup.py::test_publish_skills_emits_one_block_per_skill_with_provenance.
func TestPublishSkillsEmitsOneBlockPerSkillWithProvenance(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)

	code, out, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	puts := map[string]map[string]any{}
	for _, request := range fake.puts() {
		puts[strings.Split(request.path, "/")[5]] = decodeObject(t, request.body)
	}
	if len(puts) != 3 {
		t.Fatalf("pushed %d module(s), want 3 (%v)", len(puts), keysOf(puts))
	}
	for slug, body := range puts {
		if body["kind"] != "skill" {
			t.Errorf("%s kind = %v, want skill", slug, body["kind"])
		}
	}
	alpha := decodeObject(t, puts["alpha-skill"]["content"].(string))
	for field, want := range map[string]any{
		"content":     libraryTexts["alpha-skill"],
		"source_path": canonicalPaths["alpha-skill"] + "/SKILL.md",
		"sha256":      sha256Hex([]byte(libraryTexts["alpha-skill"])),
	} {
		if alpha[field] != want {
			t.Errorf("alpha-skill %s = %v, want %v", field, alpha[field], want)
		}
	}
	beta := decodeObject(t, puts["beta-skill"]["content"].(string))
	if beta["source_path"] != pluginPaths["beta-skill"]+"/SKILL.md" {
		t.Errorf("plugin-only source_path = %v", beta["source_path"])
	}
	if _, present := beta["mirror_path"]; present {
		t.Error("a plugin-only skill must carry no mirror_path")
	}
	if _, present := beta["mirror_sha256"]; present {
		t.Error("a plugin-only skill must carry no mirror_sha256")
	}
	gamma := decodeObject(t, puts["gamma-skill"]["content"].(string))
	if gamma["source_path"] != canonicalPaths["gamma-skill"]+"/SKILL.md" {
		t.Errorf("overlap source_path = %v, want the canonical path", gamma["source_path"])
	}
	if gamma["mirror_path"] != pluginPaths["gamma-skill"]+"/SKILL.md" {
		t.Errorf("overlap mirror_path = %v", gamma["mirror_path"])
	}
	if gamma["mirror_sha256"] != sha256Hex([]byte(libraryTexts["gamma-skill"])) {
		t.Errorf("overlap mirror_sha256 = %v", gamma["mirror_sha256"])
	}
	if !strings.Contains(out, "module alpha-skill@1.0.0: applied") {
		t.Errorf("stdout missing the push line:\n%s", out)
	}
	if !strings.Contains(out, "publish summary: 3 pushed, 0 new version(s), 0 skipped") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

// Mirrors test_publish_skills_attempts_every_entry_and_names_every_failure.
func TestPublishSkillsAttemptsEveryEntryAndNamesEveryFailure(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)
	fake.override("PUT", "/api/v2/openrig/modules/beta-skill/versions/1.0.0", 400,
		`{"detail": "content must be 1 to 65536 bytes"}`)

	code, out, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library)
	if code != ExitRemote {
		t.Fatalf("exit %d, want %d", code, ExitRemote)
	}
	attempted := map[string]bool{}
	for _, request := range fake.puts() {
		attempted[strings.Split(request.path, "/")[5]] = true
	}
	for _, slug := range []string{"alpha-skill", "beta-skill", "gamma-skill"} {
		if !attempted[slug] {
			t.Errorf("%s was never attempted; a refusal must not hide the rest", slug)
		}
	}
	for _, want := range []string{
		"module alpha-skill@1.0.0: applied",
		"module beta-skill@1.0.0: FAILED",
		"module gamma-skill@1.0.0: applied",
		"content must be 1 to 65536 bytes",
		"publish summary: 2 of 3 block(s) pushed, 1 FAILED, 0 skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "beta-skill") || !strings.Contains(errOut, "content must be 1 to 65536 bytes") {
		t.Errorf("the exit error did not name the failure: %q", errOut)
	}
}

// Mirrors test_publish_skills_rerun_skips_identical_blocks.
func TestPublishSkillsRerunSkipsIdenticalBlocks(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)
	args := []string{"publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library}
	if code, _, errOut := runTeam(t, args...); code != 0 {
		t.Fatalf("first publish exit %d, stderr %q", code, errOut)
	}
	fake.storePushes()

	code, out, errOut := runTeam(t, args...)
	if code != 0 {
		t.Fatalf("second publish exit %d, stderr %q", code, errOut)
	}
	if got := len(fake.puts()); got != 3 {
		t.Errorf("total PUTs = %d, want the first publish's 3 only", got)
	}
	if got := strings.Count(out, ": skipped (identical content already stored)"); got != 3 {
		t.Errorf("skipped lines = %d, want 3\n%s", got, out)
	}
	if !strings.Contains(out, "publish summary: 0 pushed, 0 new version(s), 3 skipped") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

// Mirrors test_publish_skills_changed_content_pushes_the_next_patch.
func TestPublishSkillsChangedContentPushesTheNextPatch(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)
	args := []string{"publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library}
	if code, _, errOut := runTeam(t, args...); code != 0 {
		t.Fatalf("first publish exit %d, stderr %q", code, errOut)
	}
	fake.storePushes()
	changed := "# Alpha v2\nlibrary body\n"
	if err := writeSkillFile(t, filepath.Join(library, canonicalPaths["alpha-skill"]), changed); err != nil {
		t.Fatal(err)
	}
	rewriteInventorySHA(t, library, "alpha-skill", "canonical", sha256Hex([]byte(changed)))

	code, out, errOut := runTeam(t, args...)
	if code != 0 {
		t.Fatalf("second publish exit %d, stderr %q", code, errOut)
	}
	var pushed []string
	for _, request := range fake.puts()[3:] {
		pushed = append(pushed, request.path)
	}
	if len(pushed) != 1 || pushed[0] != "/api/v2/openrig/modules/alpha-skill/versions/1.0.1" {
		t.Fatalf("PUTs after the change = %v, want the single 1.0.1 push", pushed)
	}
	if !strings.Contains(out, "module alpha-skill@1.0.1 (new version; 1.0.0 stored with different content): applied") {
		t.Errorf("stdout missing the new-version line:\n%s", out)
	}
}

// Mirrors test_publish_skills_refuses_a_secret.
func TestPublishSkillsRefusesASecret(t *testing.T) {
	fake := installFake(t)
	library := writeLibrary(t)
	secret := "token Bearer sk-abcdefghijklmnopqrstuvwxyz012345"
	if err := writeSkillFile(t, filepath.Join(library, canonicalPaths["alpha-skill"]), secret); err != nil {
		t.Fatal(err)
	}
	rewriteInventorySHA(t, library, "alpha-skill", "canonical", sha256Hex([]byte(secret)))

	code, _, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library)
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "alpha-skill") || !strings.Contains(errOut, "ENV_VAR") {
		t.Errorf("stderr did not name the skill and the fix: %q", errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("a refused publish sent %d request(s)", len(fake.recorded()))
	}
}

// Mirrors test_publish_skills_refuses_a_stale_inventory.
func TestPublishSkillsRefusesAStaleInventory(t *testing.T) {
	fake := installFake(t)
	library := writeLibrary(t)
	if err := writeSkillFile(t, filepath.Join(library, canonicalPaths["alpha-skill"]), "# changed without regenerating the inventory\n"); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library)
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "alpha-skill") || !strings.Contains(errOut, "stale") {
		t.Errorf("stderr did not name the skill and the staleness: %q", errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("a stale inventory still sent %d request(s)", len(fake.recorded()))
	}
}

// Mirrors test_publish_skills_needs_a_source_root.
func TestPublishSkillsNeedsASourceRoot(t *testing.T) {
	installFake(t)
	t.Setenv(libraryRootEnv, "")
	library := writeLibrary(t)

	code, _, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--dry-run")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "source-root") {
		t.Errorf("stderr did not name --source-root: %q", errOut)
	}
}

// The library root may come from the environment instead of the flag.
func TestPublishSkillsTakesTheLibraryRootFromTheEnvironment(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)
	t.Setenv(libraryRootEnv, library)

	code, out, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"))
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "publish summary: 3 pushed, 0 new version(s), 0 skipped") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

// Mirrors test_publish_skills_names_its_inventory_and_has_no_default.
func TestPublishSkillsNamesItsInventoryAndHasNoDefault(t *testing.T) {
	installFake(t)
	code, _, errOut := runTeam(t, "publish-skills")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "--inventory") {
		t.Errorf("stderr did not name --inventory: %q", errOut)
	}
}
