package clientllmproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCodexFixture writes a codex config into a fresh temp CODEX_HOME and returns the directory and the
// file's path. The config carries what the owner's real one does: a global provider selection, the
// provider table it names, a second provider of the owner's own, a profile, an unrelated table and
// comments - so the surgical-edit test has something to preserve in every direction.
func writeCodexFixture(t *testing.T) (home, path string) {
	t.Helper()
	home = t.TempDir()
	path = filepath.Join(home, codexConfigName)
	config := `# the owner's codex config
model = "gpt-5"
model_provider = "openai"
approval_policy = "on-request"

[model_providers.openai]
name = "OpenAI"
base_url = "https://api.openai.com/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"

[model_providers.azure]
name = "Azure"
base_url = "https://example.openai.azure.com/openai"
env_key = "AZURE_OPENAI_API_KEY"

[profiles.fast]
model = "gpt-5-mini"
model_provider = "openai"

[other]
x = 1
# trailing note
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write the temp codex config: %v", err)
	}
	return home, path
}

// TestTheCodexEntryIsWrittenSurgically is slice 5's edit contract: one owned provider entry whose fields
// are copied from the provider the seat selected with only base_url changed, one profile that selects
// it, the global model_provider untouched, and every other line - the owner's second provider, their
// profile, an unrelated table, the comments - carried through unchanged.
func TestTheCodexEntryIsWrittenSurgically(t *testing.T) {
	_, path := writeCodexFixture(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}

	changed, err := ensureCodexProvider(path, 7392)
	if err != nil {
		t.Fatalf("ensure the codex provider: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true for a config that had no agenthub-egress entry")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the written config: %v", err)
	}
	text := string(written)

	// The owned entry copies the selected provider's fields, with only base_url replaced.
	for _, want := range []string{
		"[model_providers.agenthub-egress]",
		`name = "OpenAI"`,
		`env_key = "OPENAI_API_KEY"`,
		`wire_api = "responses"`,
		`base_url = "http://127.0.0.1:7392"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the written config does not carry %q:\n%s", want, text)
		}
	}

	// The profile selects the copy, never the global provider.
	profile := "[profiles." + codexProfileName + "]"
	if !strings.Contains(text, profile) {
		t.Fatalf("the written config has no %s:\n%s", profile, text)
	}
	if !strings.Contains(text[strings.Index(text, profile):], `model_provider = "agenthub-egress"`) {
		t.Errorf("the profile does not select the owned entry:\n%s", text[strings.Index(text, profile):])
	}

	// The global selection is untouched: before the first table, the line still names openai.
	root := text[:strings.Index(text, "\n[")]
	if !strings.Contains(root, `model_provider = "openai"`) {
		t.Errorf("the global model_provider changed:\n%s", root)
	}
	if strings.Count(root, "model_provider") != 1 {
		t.Errorf("the root table gained or lost a model_provider line:\n%s", root)
	}

	// The owner's other content survives: the source provider keeps its own base_url, the second
	// provider is untouched, and the unrelated table and comments are still there.
	for _, want := range []string{
		"base_url = \"https://api.openai.com/v1\"",
		"[model_providers.azure]",
		`base_url = "https://example.openai.azure.com/openai"`,
		"[profiles.fast]",
		`model = "gpt-5-mini"`,
		"[other]",
		"x = 1",
		"# the owner's codex config",
		"# trailing note",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the written config lost %q:\n%s", want, text)
		}
	}

	// A second run is byte-identical and reports no change.
	changedAgain, err := ensureCodexProvider(path, 7392)
	if err != nil {
		t.Fatalf("ensure the codex provider a second time: %v", err)
	}
	if changedAgain {
		t.Error("the second run changed the file, want the write to be idempotent")
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the config after the second run: %v", err)
	}
	if string(again) != text {
		t.Errorf("the second run was not byte-identical:\nbefore:\n%s\nafter:\n%s", text, again)
	}
	if string(written) == string(original) {
		t.Error("the first run left the file unchanged")
	}
}

// TestTheCodexWriterRefusesWithoutAProviderToCopy pins the failure direction: no model_provider, or a
// model_provider with no section, is an error that names the file and leaves its bytes untouched - the
// client copies a provider or writes nothing, it never invents a model or an auth field.
func TestTheCodexWriterRefusesWithoutAProviderToCopy(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   []string
	}{
		{
			name:   "no model_provider selected",
			config: "model = \"gpt-5\"\n",
			want:   []string{"model_provider", "invent"},
		},
		{
			name:   "a selection with no provider section",
			config: "model_provider = \"ghost\"\n",
			want:   []string{"ghost", "no [model_providers.ghost]"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, codexConfigName)
			if err := os.WriteFile(path, []byte(c.config), 0o600); err != nil {
				t.Fatalf("write the fixture: %v", err)
			}
			changed, err := ensureCodexProvider(path, 7392)
			if err == nil {
				t.Fatal("err = nil, want a refusal rather than a guessed entry")
			}
			if changed {
				t.Error("changed = true alongside an error")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %q, want the file named %q", err, path)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to say %q", err, want)
				}
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read the config after the refusal: %v", readErr)
			}
			if string(after) != c.config {
				t.Errorf("the refusal changed the file:\n%s", after)
			}
		})
	}
}

// TestTheCodexEntryReadsAsInertUntilSelected pins the report side of amendment B: the entry's presence is
// what turns codex's status into "provider entry present, not selected", and it is still unproxied.
func TestTheCodexEntryReadsAsInertUntilSelected(t *testing.T) {
	home, path := writeCodexFixture(t)
	environ := func() []string { return []string{"CODEX_HOME=" + home} }
	if codexEntryPresent(environ) {
		t.Fatal("codexEntryPresent = true before this client wrote anything")
	}
	t.Setenv("CODEX_HOME", home)
	if got := CodexStatus(); got != "unproxied" {
		t.Errorf("CodexStatus = %q before the entry exists, want the bare unproxied state", got)
	}

	if _, err := ensureCodexProvider(path, 7392); err != nil {
		t.Fatalf("ensure the codex provider: %v", err)
	}
	if !codexEntryPresent(environ) {
		t.Error("codexEntryPresent = false after the entry was written")
	}
	if got := CodexStatus(); got != "unproxied (provider entry present, not selected)" {
		t.Errorf("CodexStatus = %q, want the decision's wording: present and inert", got)
	}
}

// TestTheCodexConfigPathHonoursCodexHome is the reason this writer takes no home directory of its own:
// CODEX_HOME is codex's own override, and it is what a test (or an owner with two configs) uses.
func TestTheCodexConfigPathHonoursCodexHome(t *testing.T) {
	path, err := codexConfigPath(func() []string { return []string{"CODEX_HOME=/tmp/codex-home"} })
	if err != nil {
		t.Fatalf("resolve the config path: %v", err)
	}
	if want := "/tmp/codex-home/config.toml"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	path, err = codexConfigPath(func() []string { return nil })
	if err != nil {
		t.Fatalf("resolve the default config path: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join(".codex", "config.toml")) {
		t.Errorf("path = %q, want it under ~/.codex", path)
	}
}
