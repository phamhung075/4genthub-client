package clientsync

import (
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// TestSafeRelativeRefusesEveryEscapeThePythonSpecLists uses the Python's own parametrized bad paths -
// ../evil.txt, /etc/passwd, a\b.txt, a//b.txt, "", ./x.txt, .. - and two more that the same rule catches
// (a leading backslash, which cannot happen on a POSIX path but is legal to write, and a component that
// is only dots). Each one is a way a server-supplied path could land outside the snapshot directory.
func TestSafeRelativeRefusesEveryEscapeThePythonSpecLists(t *testing.T) {
	for _, bad := range []string{
		"../evil.txt", "/etc/passwd", `a\b.txt`, "a//b.txt", "", "./x.txt", "..",
		`\absolute`, "docs/../../etc/passwd", "../..", "a/..",
	} {
		parts, err := SafeRelative(bad)
		if err == nil {
			t.Errorf("SafeRelative(%q) = %v, want a refusal (the Python spec rejects this path)", bad, parts)
			continue
		}
		if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUsage {
			t.Errorf("SafeRelative(%q) code = %d, want %d", bad, code, clientcmd.ExitUsage)
		}
		if !strings.Contains(err.Error(), "unsafe file path") {
			t.Errorf("SafeRelative(%q) = %q, want the Python's message", bad, err.Error())
		}
	}

	for path, want := range map[string][]string{
		"docs/readme.md":          {"docs", "readme.md"},
		"nested/deeper/notes.txt": {"nested", "deeper", "notes.txt"},
		"pinned.json":             {"pinned.json"},
		"a b/c-d_e.txt":           {"a b", "c-d_e.txt"},
	} {
		parts, err := SafeRelative(path)
		if err != nil {
			t.Errorf("SafeRelative(%q) refused a legal path: %v", path, err)
			continue
		}
		if strings.Join(parts, "/") != strings.Join(want, "/") {
			t.Errorf("SafeRelative(%q) = %v, want %v", path, parts, want)
		}
	}
}

// TestValidateHashRefusesAnythingThatIsNotADirectoryName: the pattern is HASH_RE and the extra ".." check
// is what stops "a..b", which the pattern allows and a filesystem would read as a step up.
func TestValidateHashRefusesAnythingThatIsNotADirectoryName(t *testing.T) {
	for _, good := range []string{"a1b2c3", "a1b2c3d4e5f6", "A.b-c_d", "1"} {
		if _, err := ValidateHash(good); err != nil {
			t.Errorf("ValidateHash(%q) refused a legal hash: %v", good, err)
		}
	}
	for _, bad := range []string{"", "..", "a..b", "../evil", "a/b", "a\\b", "a b", "-leading", ".hidden", "a\nb"} {
		_, err := ValidateHash(bad)
		if err == nil {
			t.Errorf("ValidateHash(%q) accepted it, want the unsafe-hash refusal", bad)
			continue
		}
		if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUsage {
			t.Errorf("ValidateHash(%q) code = %d, want %d", bad, code, clientcmd.ExitUsage)
		}
	}
}

// TestExtractFilesSeparatesTheCloudsTwoFailures pins the split the Python keeps and a port would
// flatten: a malformed ANSWER is EXIT_REMOTE (the cloud is wrong), while an unsafe PATH is EXIT_USAGE
// (the cloud is dangerous). A caller scripting retries cares about the difference.
func TestExtractFilesSeparatesTheCloudsTwoFailures(t *testing.T) {
	ok := map[string]any{"files": []any{
		map[string]any{"path": "docs/readme.md", "content": "hello"},
		map[string]any{"path": "nested/deeper/notes.txt", "content": "notes"},
	}}
	entries, err := ExtractFiles(ok)
	if err != nil {
		t.Fatalf("ExtractFiles: %v", err)
	}
	if len(entries) != 2 || strings.Join(entries[1].Parts, "/") != "nested/deeper/notes.txt" || entries[1].Content != "notes" {
		t.Fatalf("entries = %+v", entries)
	}

	for _, c := range []struct {
		name     string
		resolved map[string]any
		wantCode int
		wantText string
	}{
		{"no files list", map[string]any{}, clientcmd.ExitRemote, "resolved_seat has no files list"},
		{"files is not a list", map[string]any{"files": "docs"}, clientcmd.ExitRemote, "resolved_seat has no files list"},
		{"an entry that is not an object", map[string]any{"files": []any{"docs/readme.md"}}, clientcmd.ExitRemote, "malformed file entry"},
		{"a missing content", map[string]any{"files": []any{map[string]any{"path": "docs/readme.md"}}}, clientcmd.ExitRemote, "malformed file entry"},
		{"a numeric content", map[string]any{"files": []any{map[string]any{"path": "a.txt", "content": 1}}}, clientcmd.ExitRemote, "malformed file entry"},
		{"a path that escapes", map[string]any{"files": []any{map[string]any{"path": "../evil.txt", "content": "x"}}}, clientcmd.ExitUsage, "unsafe file path"},
		{"an absolute path", map[string]any{"files": []any{map[string]any{"path": "/etc/passwd", "content": "x"}}}, clientcmd.ExitUsage, "unsafe file path"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ExtractFiles(c.resolved)
			if err == nil {
				t.Fatalf("ExtractFiles accepted %+v", c.resolved)
			}
			if !strings.Contains(err.Error(), c.wantText) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantText)
			}
			if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != c.wantCode {
				t.Errorf("code = %d, want %d", code, c.wantCode)
			}
		})
	}
}
