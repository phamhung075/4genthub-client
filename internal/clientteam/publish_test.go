package clientteam

import (
	"encoding/json"
	"os"
	"os/exec"
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
		puts[strings.Split(request.path, "/")[4]] = decodeObject(t, request.body)
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
	fake.override("PUT", "/api/v2/modules/beta-skill/versions/1.0.0", 400,
		`{"detail": "content must be 1 to 65536 bytes"}`)

	code, out, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library)
	if code != ExitRemote {
		t.Fatalf("exit %d, want %d", code, ExitRemote)
	}
	attempted := map[string]bool{}
	for _, request := range fake.puts() {
		attempted[strings.Split(request.path, "/")[4]] = true
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
	regenerateInventory(t, library)

	code, out, errOut := runTeam(t, args...)
	if code != 0 {
		t.Fatalf("second publish exit %d, stderr %q", code, errOut)
	}
	var pushed []string
	for _, request := range fake.puts()[3:] {
		pushed = append(pushed, request.path)
	}
	if len(pushed) != 1 || pushed[0] != "/api/v2/modules/alpha-skill/versions/1.0.1" {
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
	regenerateInventory(t, library)

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

// Mirrors test_publish_skills_reads_the_revision_the_inventory_names, the port target of the retired
// Python case (DISPOSITION row 25).
//
// The digests and the pin disagree: the bytes and the recorded digest moved with a regenerated
// inventory while generated_from kept naming the revision they were taken from before. Before this
// guard the verb published it, because the digests are verified against the checkout the operator
// points at and nothing compared them with the revision the inventory names.
func TestPublishSkillsRefusesAnInventoryThatDoesNotDescribeTheRevisionItNames(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)
	described := gitLibraryCommit(t, library, "the library as the inventory describes it")

	moved := "# Alpha\nlibrary body\nsecond edition\n"
	if err := writeSkillFile(t, filepath.Join(library, canonicalPaths["alpha-skill"]), moved); err != nil {
		t.Fatal(err)
	}
	rewriteInventorySHA(t, library, "alpha-skill", "canonical", sha256Hex([]byte(moved)))
	gitLibraryCommit(t, library, "the moved checkout the inventory was regenerated from")
	setInventoryGeneratedFrom(t, library, described)

	code, out, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library)
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d: the verb accepted an inventory that does not describe %s (stdout %q)",
			code, ExitUsage, described, out)
	}
	if !strings.Contains(errOut, "alpha-skill") || !strings.Contains(errOut, described[:8]) {
		t.Errorf("stderr did not name the skill and the revision it does not describe: %q", errOut)
	}
	if !strings.Contains(errOut, sha256Hex([]byte(moved))) {
		t.Errorf("stderr did not name the digest the named revision holds: %q", errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("an inventory whose pin does not match still sent %d request(s)", len(fake.recorded()))
	}
}

// The other half, and the mirror of test_a_pin_that_moved_without_regenerating_is_refused: a pin that
// DOES describe the digests must publish, through the whole path, losing no request on the way.
func TestPublishSkillsPublishesAnInventoryThatDescribesTheRevisionItNames(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)
	revision := gitLibraryCommit(t, library, "the library as the inventory describes it")
	setInventoryGeneratedFrom(t, library, revision)

	code, out, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"), "--source-root", library)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "publish summary: 3 pushed, 0 new version(s), 0 skipped") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

// A pin that cannot be read back is refused rather than trusted: an unverifiable pin is not a pin.
func TestPublishSkillsRefusesAPinItCannotMaterialise(t *testing.T) {
	installFake(t)
	library := writeLibrary(t)
	absent := strings.Repeat("a", 40)
	setInventoryGeneratedFrom(t, library, absent)

	code, _, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"),
		"--source-root", library, "--dry-run")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, absent[:8]) || !strings.Contains(errOut, "git archive") {
		t.Errorf("stderr did not name the revision and the failed read: %q", errOut)
	}
}

// The ruling's other side of the pin: the format carries generated_from, so a record without one names
// no revision its digests can be checked against and nothing downstream can detect its divergence. It
// is refused by name, before any request - a nameless inventory is worse than a stale one.
func TestPublishSkillsRefusesAnInventoryThatCarriesNoGeneratedFrom(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	library := writeLibrary(t)
	clearInventoryGeneratedFrom(t, library)

	code, out, errOut := runTeam(t, "publish-skills", "--inventory", filepath.Join(library, "inventory.json"),
		"--source-root", library)
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d: the verb published an inventory that names no revision at all (stdout %q)",
			code, ExitUsage, out)
	}
	if !strings.Contains(errOut, filepath.Join(library, "inventory.json")) {
		t.Errorf("stderr did not name the inventory: %q", errOut)
	}
	if !strings.Contains(errOut, "generated_from") {
		t.Errorf("stderr did not name the missing pin: %q", errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("an inventory with no pin still sent %d request(s)", len(fake.recorded()))
	}
}

// gitLibraryCommit commits whatever the library fixture holds and returns the revision, so a case can
// name one state of the tree and then move the tree again. The identity is passed by environment and
// the user's own git configuration is bypassed, so the case cannot depend on the machine.
func gitLibraryCommit(t *testing.T, root, message string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("SKIPPED, NOT PASSED: no git on PATH, so the pin cannot be materialised and this case " +
			"did NOT run - the verb refuses in that case too, naming the revision it could not read")
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		gitIn(t, root, "init", "-q")
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", message)
	return gitIn(t, root, "rev-parse", "HEAD")
}

// regenerateInventory does what the inventory generator does once it has recorded the digests: commit
// the library and name that revision in generated_from, so the pin describes the bytes the inventory
// records. A fixture that moves bytes and digests afterwards must come back through here, because the
// verb refuses an inventory whose pin does not describe the revision it names.
func regenerateInventory(t *testing.T, libraryRoot string) {
	t.Helper()
	setInventoryGeneratedFrom(t, libraryRoot, gitLibraryCommit(t, libraryRoot, "the library as the inventory describes it"))
}

func gitIn(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.test",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, root, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setInventoryGeneratedFrom records the revision the digests came from, the way the inventory
// generator writes its provenance line.
func setInventoryGeneratedFrom(t *testing.T, libraryRoot, revision string) {
	t.Helper()
	path := filepath.Join(libraryRoot, "inventory.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var inventory map[string]any
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	inventory["generated_from"] = "/fixture/library @ HEAD " + revision +
		", re-verified 2026-10-10 (skills/_canonical + packages/daemon/assets/plugins/core/skills)"
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("encode inventory: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
}

// clearInventoryGeneratedFrom strips the pin, the way the pre-2026-10-09 format wrote an inventory
// that carried none.
func clearInventoryGeneratedFrom(t *testing.T, libraryRoot string) {
	t.Helper()
	path := filepath.Join(libraryRoot, "inventory.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var inventory map[string]any
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	delete(inventory, "generated_from")
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("encode inventory: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
}
