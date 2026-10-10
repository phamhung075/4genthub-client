package clientteam

import (
	"testing"
)

// Module content is compared byte-for-byte with what the cloud stores, so the emitter must produce the
// Python's bytes. The expected strings below were produced by running the Python client's own
// json.dumps, not by hand.

func TestPyIndentedMatchesPythonJSONDumpsIndentTwo(t *testing.T) {
	weather := newPyObject().
		set("name", "weather").
		set("type", "http").
		set("url", "https://mcp.example.com/mcp").
		set("headers", newPyObject().set("Authorization", "Bearer ${WEATHER_TOKEN}"))
	want := "{\n" +
		"  \"name\": \"weather\",\n" +
		"  \"type\": \"http\",\n" +
		"  \"url\": \"https://mcp.example.com/mcp\",\n" +
		"  \"headers\": {\n" +
		"    \"Authorization\": \"Bearer ${WEATHER_TOKEN}\"\n" +
		"  }\n" +
		"}"
	if got := pyIndented(weather); got != want {
		t.Errorf("http block:\n got %q\nwant %q", got, want)
	}

	filesystem := newPyObject().
		set("name", "filesystem").
		set("type", "stdio").
		set("command", "npx").
		set("args", []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"}).
		set("env", newPyObject().set("FS_ROOT", "${FS_ROOT}"))
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
	if got := pyIndented(filesystem); got != want {
		t.Errorf("stdio block:\n got %q\nwant %q", got, want)
	}
}

func TestPyCompactMatchesPythonJSONDumps(t *testing.T) {
	block := newPyObject().
		set("content", "# Alpha\nbody\n").
		set("source_path", ".claude/skills/alpha-skill/SKILL.md").
		set("sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	want := `{"content": "# Alpha\nbody\n", "source_path": ".claude/skills/alpha-skill/SKILL.md", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	if got := pyCompact(block); got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	block.set("mirror_path", "x/SKILL.md").
		set("mirror_sha256", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	want = `{"content": "# Alpha\nbody\n", "source_path": ".claude/skills/alpha-skill/SKILL.md", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mirror_path": "x/SKILL.md", "mirror_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
	if got := pyCompact(block); got != want {
		t.Errorf("mirror block:\n got %q\nwant %q", got, want)
	}
}

// Python's encoder neither escapes '&' nor leaves a non-ASCII rune raw, and Go's does the opposite of
// both. Either difference alone would turn a re-run's SKIP into a spurious NEW-VERSION.
func TestPyJSONMatchesPythonEscaping(t *testing.T) {
	object := newPyObject().set("url", "https://x/mcp?a=1&b=2").set("n", "café")
	want := "{\n  \"url\": \"https://x/mcp?a=1&b=2\",\n  \"n\": \"caf\\u00e9\"\n}"
	if got := pyIndented(object); got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func TestPyJSONRendersEmptyContainers(t *testing.T) {
	if got := pyCompact(newPyObject()); got != "{}" {
		t.Errorf("empty object: got %q, want {}", got)
	}
	if got := pyCompact([]string{}); got != "[]" {
		t.Errorf("empty array: got %q, want []", got)
	}
}
