package clientteam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The project fixture, byte-identical to the Python spec's: an http server whose header keeps a
// ${ENV_VAR} reference, a command entry with no declared type, and two skills.
const projectMCPConfig = `{
  "mcpServers": {
    "weather": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {"Authorization": "Bearer ${WEATHER_TOKEN}"}
    },
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {"FS_ROOT": "${FS_ROOT}"}
    }
  }
}`

var projectSkills = map[string]string{
	"alpha-skill": "# Alpha\nbody\n",
	"beta-skill":  "# Beta\nbody\n",
}

func writeProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(projectMCPConfig), 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	for slug, text := range projectSkills {
		directory := filepath.Join(root, ".claude", "skills", slug)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(text), 0o644); err != nil {
			t.Fatalf("write SKILL.md: %v", err)
		}
	}
	return root
}

// The library fixture: a canonical-only skill, a plugin-only skill, and one committed on both edges.
var libraryTexts = map[string]string{
	"alpha-skill": "# Alpha\nlibrary body\n",
	"beta-skill":  "# Beta\nlibrary body\n",
	"gamma-skill": "# Gamma\nlibrary body\n",
}

var canonicalPaths = map[string]string{
	"alpha-skill": "skills/_canonical/process/alpha-skill",
	"gamma-skill": "skills/_canonical/core/gamma-skill",
}

var pluginPaths = map[string]string{
	"beta-skill":  "packages/daemon/assets/plugins/openrig-core/skills/beta-skill",
	"gamma-skill": "packages/daemon/assets/plugins/openrig-core/skills/gamma-skill",
}

// writeLibrary is the inventory GENERATOR's output: the three-skill library committed, with
// generated_from naming that revision. A case that moves the bytes afterwards regenerates the
// inventory the same way (regenerateInventory), because an inventory whose pin does not describe the
// revision it names - or that carries no pin at all - is refused.
func writeLibrary(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	rows := []map[string]any{}
	for _, name := range []string{"alpha-skill", "beta-skill", "gamma-skill"} {
		text := libraryTexts[name]
		row := map[string]any{"name": name}
		if path, known := canonicalPaths[name]; known {
			if err := writeSkillFile(t, filepath.Join(root, path), text); err != nil {
				t.Fatal(err)
			}
			row["canonical"] = map[string]any{"path": path, "group": "core", "sha256": sha256Hex([]byte(text))}
		}
		if path, known := pluginPaths[name]; known {
			if err := writeSkillFile(t, filepath.Join(root, path), text); err != nil {
				t.Fatal(err)
			}
			row["plugin"] = map[string]any{"path": path, "sha256": sha256Hex([]byte(text))}
		}
		rows = append(rows, row)
	}
	inventory := map[string]any{
		"version": 1, "count": len(rows), "skills": rows,
		"seat_curation": map[string]any{}, "unused_by_default": []string{},
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("encode inventory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "inventory.json"), encoded, 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	regenerateInventory(t, root)
	return root
}

func writeSkillFile(t *testing.T, directory, text string) error {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, skillFile), []byte(text), 0o644)
}

// rewriteInventorySHA records a new digest for one skill's edge, the way regenerating the inventory
// would.
func rewriteInventorySHA(t *testing.T, libraryRoot, name, edge, digest string) {
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
	rows, _ := inventory["skills"].([]any)
	for _, entry := range rows {
		row, _ := entry.(map[string]any)
		if row["name"] != name {
			continue
		}
		side, _ := row[edge].(map[string]any)
		side["sha256"] = digest
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("encode inventory: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
}

// writeTeamDefinition writes a minimal but complete team.json into a new directory.
func writeTeamDefinition(t *testing.T, moduleSlug, moduleVersion, seatType string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "instruction.md"), []byte("module text\n"), 0o644); err != nil {
		t.Fatalf("write module file: %v", err)
	}
	definition := map[string]any{
		"room": map[string]any{"slug": "demo", "name": "Demo"},
		"modules": []map[string]any{
			{"slug": moduleSlug, "kind": "instruction", "version": moduleVersion, "file": "instruction.md"},
		},
		"seats": []map[string]any{
			{
				"seat_key": "lead", "seat_type": seatType, "runtime": "omp", "model": "",
				"pinned_version": "1.0.0", "permission_policy": "standard",
			},
		},
		"links":           []map[string]any{{"from": "lead", "kind": "delegates_to", "to": "lead"}},
		"company_overlay": []string{moduleSlug},
		"seat_overlays":   map[string]any{"lead": []string{moduleSlug}},
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("encode team.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "team.json"), encoded, 0o644); err != nil {
		t.Fatalf("write team.json: %v", err)
	}
	return directory
}
