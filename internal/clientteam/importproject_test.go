package clientteam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mirrors tests/test_team_setup.py::test_import_project_pushes_mcp_blocks_and_skills.
func TestImportProjectPushesMCPBlocksAndSkills(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	t.Chdir(writeProject(t))

	code, out, errOut := runTeam(t, "import-project")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	puts := map[string]map[string]any{}
	paths := map[string]bool{}
	for _, request := range fake.puts() {
		parts := strings.Split(request.path, "/")
		puts[parts[5]] = decodeObject(t, request.body)
		paths[request.path] = true
	}
	for _, slug := range []string{"weather", "filesystem", "alpha-skill", "beta-skill"} {
		if _, ok := puts[slug]; !ok {
			t.Fatalf("no PUT for %s; PUTs were %v", slug, keysOf(puts))
		}
		if !paths["/api/v2/openrig/modules/"+slug+"/versions/1.0.0"] {
			t.Errorf("no PUT to the 1.0.0 path for %s", slug)
		}
	}
	for _, request := range fake.recorded() {
		if request.method == "GET" && !strings.HasPrefix(request.path, "/api/v2/openrig/modules/") {
			t.Errorf("module resolution GET went to %s", request.path)
		}
	}
	if got := puts["weather"]["kind"]; got != "mcp" {
		t.Errorf("weather kind = %v, want mcp", got)
	}
	want := "{\n" +
		"  \"name\": \"weather\",\n" +
		"  \"type\": \"http\",\n" +
		"  \"url\": \"https://mcp.example.com/mcp\",\n" +
		"  \"headers\": {\n" +
		"    \"Authorization\": \"Bearer ${WEATHER_TOKEN}\"\n" +
		"  }\n" +
		"}"
	if got := puts["weather"]["content"]; got != want {
		t.Errorf("weather content =\n%v\nwant\n%v", got, want)
	}
	want = "{\n" +
		"  \"name\": \"filesystem\",\n" +
		"  \"type\": \"stdio\",\n" +
		"  \"command\": \"npx\",\n" +
		"  \"args\": [\n" +
		"    \"-y\",\n" +
		"    \"@modelcontextprotocol/server-filesystem\",\n" +
		"    \"/tmp\"\n" +
		"  ],\n" +
		"  \"env\": {\n" +
		"    \"FS_ROOT\": \"${FS_ROOT}\"\n" +
		"  }\n" +
		"}"
	if got := puts["filesystem"]["content"]; got != want {
		t.Errorf("filesystem content =\n%v\nwant\n%v", got, want)
	}
	if got := puts["alpha-skill"]["kind"]; got != "skill" {
		t.Errorf("alpha-skill kind = %v, want skill", got)
	}
	want = `{"content": "# Alpha\nbody\n", "source_path": ".claude/skills/alpha-skill/SKILL.md", "sha256": ` +
		`"` + sha256Hex([]byte("# Alpha\nbody\n")) + `"}`
	if got := puts["alpha-skill"]["content"]; got != want {
		t.Errorf("alpha-skill content =\n%v\nwant\n%v", got, want)
	}
	if !strings.Contains(out, "module weather@1.0.0: applied") {
		t.Errorf("stdout did not report the weather push: %q", out)
	}
}

// Mirrors test_import_project_refuses_a_secret_literal.
func TestImportProjectRefusesASecretLiteral(t *testing.T) {
	fake := installFake(t)
	root := writeProject(t)
	t.Chdir(root)
	leaky := `{"mcpServers": {"leaky": {"type": "http", "url": "https://mcp.example.com/mcp",` +
		` "headers": {"Authorization": "Bearer sk-abcdefghijklmnopqrstuvwxyz012345"}}}}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(leaky), 0o644); err != nil {
		t.Fatalf("write leaky .mcp.json: %v", err)
	}

	code, _, errOut := runTeam(t, "import-project")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "leaky") || !strings.Contains(errOut, "ENV_VAR") {
		t.Errorf("stderr did not name the field and the fix: %q", errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("a refused import still sent %d request(s)", len(fake.recorded()))
	}
}

// Mirrors test_import_project_dry_run_makes_no_requests.
func TestImportProjectDryRunMakesNoRequests(t *testing.T) {
	fake := installFake(t)
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	t.Chdir(writeProject(t))

	code, out, errOut := runTeam(t, "import-project", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("dry run sent %d request(s)", len(fake.recorded()))
	}
	want := "plan: module weather@1.0.0: PUSH unless stored content is identical (then SKIP); " +
		"if stored content differs, NEW-VERSION 1.0.1"
	if !strings.Contains(out, want) {
		t.Errorf("stdout missing the unresolved plan line:\n%s", out)
	}
}

// Mirrors test_import_project_rerun_skips_identical_modules.
func TestImportProjectRerunSkipsIdenticalModules(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	t.Chdir(writeProject(t))
	if code, _, errOut := runTeam(t, "import-project"); code != 0 {
		t.Fatalf("first import exit %d, stderr %q", code, errOut)
	}
	fake.storePushes()

	code, out, errOut := runTeam(t, "import-project")
	if code != 0 {
		t.Fatalf("second import exit %d, stderr %q", code, errOut)
	}
	if len(fake.puts()) != 4+0 {
		t.Errorf("second import left %d PUT(s) total, want the first run's 4 only", len(fake.puts()))
	}
	if got := strings.Count(out, ": skipped (identical content already stored)"); got != 4 {
		t.Errorf("skipped lines = %d, want 4\n%s", got, out)
	}
	if !strings.Contains(out, "import summary: 0 pushed, 0 new version(s), 4 skipped") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

// Mirrors test_import_project_changed_content_pushes_the_next_patch.
func TestImportProjectChangedContentPushesTheNextPatch(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	t.Chdir(root)
	if code, _, errOut := runTeam(t, "import-project"); code != 0 {
		t.Fatalf("first import exit %d, stderr %q", code, errOut)
	}
	fake.storePushes()
	changed := "# Alpha v2\nbody\n"
	if err := writeSkillFile(t, filepath.Join(root, ".claude", "skills", "alpha-skill"), changed); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runTeam(t, "import-project")
	if code != 0 {
		t.Fatalf("second import exit %d, stderr %q", code, errOut)
	}
	var pushed []string
	for _, request := range fake.puts()[4:] {
		pushed = append(pushed, request.path)
	}
	if len(pushed) != 1 || pushed[0] != "/api/v2/openrig/modules/alpha-skill/versions/1.0.1" {
		t.Fatalf("PUTs after the change = %v, want the single 1.0.1 alpha-skill push", pushed)
	}
	body := decodeObject(t, fake.puts()[4].body)
	content, _ := body["content"].(string)
	var block map[string]any
	if err := json.Unmarshal([]byte(content), &block); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if block["content"] != changed {
		t.Errorf("pushed content = %v, want %q", block["content"], changed)
	}
	if block["sha256"] != sha256Hex([]byte(changed)) {
		t.Errorf("pushed sha256 = %v, want %s", block["sha256"], sha256Hex([]byte(changed)))
	}
	if !strings.Contains(out, "module alpha-skill@1.0.1 (new version; 1.0.0 stored with different content): applied") {
		t.Errorf("stdout missing the new-version line:\n%s", out)
	}
	if !strings.Contains(out, "import summary: 0 pushed, 1 new version(s), 3 skipped") {
		t.Errorf("summary wrong:\n%s", out)
	}
}

// Mirrors test_import_project_dry_run_classifies_against_stored_versions.
func TestImportProjectDryRunClassifiesAgainstStoredVersions(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	root := writeProject(t)
	t.Chdir(root)
	if code, _, errOut := runTeam(t, "import-project"); code != 0 {
		t.Fatalf("first import exit %d, stderr %q", code, errOut)
	}
	fake.storePushes()
	if err := writeSkillFile(t, filepath.Join(root, ".claude", "skills", "alpha-skill"), "# Alpha v2\nbody\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeSkillFile(t, filepath.Join(root, ".claude", "skills", "gamma-skill"), "# Gamma\nbody\n"); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runTeam(t, "import-project", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run exit %d, stderr %q", code, errOut)
	}
	if got := len(fake.puts()); got != 4 {
		t.Errorf("dry run made %d PUT(s) in total, want the first run's 4 only", got)
	}
	for _, want := range []string{
		"plan: module alpha-skill@1.0.1: NEW-VERSION (1.0.0 stored with different content)",
		"plan: module weather@1.0.0: SKIP (identical content already stored)",
		"plan: module gamma-skill@1.0.0: PUSH",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\n%s", want, out)
		}
	}
}

// Mirrors test_publish_skills_names_its_inventory_and_has_no_default.
func TestImportProjectHasNoProjectRootDefault(t *testing.T) {
	installFake(t)
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	root := writeProject(t)
	t.Chdir(root)

	if _, _, errOut := runTeam(t, "import-project", "--dry-run"); strings.Contains(errOut, "error") {
		t.Errorf("dry run reported an error with no credentials: %q", errOut)
	}
	if code, _, _ := runTeam(t, "import-project", "--bogus"); code != ExitUsage {
		t.Errorf("an unknown flag exited %d, want %d", code, ExitUsage)
	}
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return object
}

func keysOf(values map[string]map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
