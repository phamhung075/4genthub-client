package clientenv

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSetCreatesTheFileAndItsDirectoryPrivate: this file holds the account's credential, so a file this
// function creates must not be readable by anyone else - and neither must the directory it made.
func TestSetCreatesTheFileAndItsDirectoryPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "4genthub", ".env")
	if err := Set(path, "OFFPEAK_ONLY", "1"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "OFFPEAK_ONLY=1\n" {
		t.Errorf("content = %q, want the one line", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v, want 0700", dir.Mode().Perm())
	}
}

// TestSetKeepsEveryOtherByteAndTheModeItFound: the file is the operator's, so a mode they chose is not
// this function's business and no other line moves.
func TestSetKeepsEveryOtherByteAndTheModeItFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := "# the client's env\nAGENTHUB_TOKEN=placeholder\nAGENTHUB_URL=https://example.invalid\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	// THE MODE IS SET, NOT ASKED FOR: `os.WriteFile`'s 0640 is a request the process umask clamps, and
	// every seat's tree takes umask 077 at its entry (see `RestrictUmask`), so a case that asked for 0640
	// would be handed 0600 and would then fail on a mode the file never had. What this case is about is
	// the mode the file ALREADY has, so it is put there explicitly.
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, "OFFPEAK_ONLY", "0"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original+"OFFPEAK_ONLY=0\n" {
		t.Errorf("content =\n%q\nwant the original plus the one line", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the 0640 it had", info.Mode().Perm())
	}
}

// TestSetReplacesTheFirstAssignmentIncludingAnExportPrefix: Load reads the first assignment of a key, so
// that is the effective line; and a line written as `export KEY=value` sets the same key, which is why
// the prefix is recognised here too.
func TestSetReplacesTheFirstAssignmentIncludingAnExportPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := "export OFFPEAK_ONLY=1\nKEEP=me\nOFFPEAK_ONLY=0\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, "OFFPEAK_ONLY", "0"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "OFFPEAK_ONLY=0\nKEEP=me\nOFFPEAK_ONLY=0\n"
	if string(content) != want {
		t.Errorf("content =\n%q\nwant\n%q", content, want)
	}
}

// TestSetAppendsToAFileWithNoTrailingNewline: the last line has to stay a line, so the new one goes on
// its own.
func TestSetAppendsToAFileWithNoTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("KEEP=me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, "OFFPEAK_ONLY", "1"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "KEEP=me\nOFFPEAK_ONLY=1\n" {
		t.Errorf("content = %q, want the old line kept whole and the new line added", content)
	}
}

func TestSetRefusesANameOrAValueThatWouldCorruptTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	for _, c := range []struct{ name, value string }{
		{"has space", "1"},
		{"9LEADING_DIGIT", "1"},
		{"", "1"},
		{"OFFPEAK_ONLY", "a\nb"},
		{"OFFPEAK_ONLY", "a\rb"},
	} {
		if err := Set(path, c.name, c.value); err == nil {
			t.Errorf("Set(%q, %q) was accepted", c.name, c.value)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a refused Set wrote %s anyway", path)
	}
}

// TestSetFollowsASymlinkInsteadOfReplacingIt: the client's rooms link their own .env AT this file, so
// writing through the link is the difference between updating the env file and silently detaching it.
func TestSetFollowsASymlinkInsteadOfReplacingIt(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.env")
	link := filepath.Join(dir, ".env")
	if err := os.WriteFile(real, []byte("KEEP=me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if err := Set(link, "OFFPEAK_ONLY", "0"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced by a regular file (mode %v, err %v)", info.Mode(), err)
	}
	content, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "KEEP=me\nOFFPEAK_ONLY=0\n" {
		t.Errorf("the target = %q, want the line written through the link", content)
	}
}

// TestLoadReadsBackWhatSetWrote is the round trip that matters: a switch the verb offers is a switch Load
// honours on the next run. The name is unique to this test so the process environment cannot interfere.
func TestLoadReadsBackWhatSetWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	name := "AGENTHUB_SET_ROUNDTRIP_TEST"
	if err := Set(path, name, "0"); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv(name)
	t.Cleanup(func() { os.Unsetenv(name) })
	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(name); got != "0" {
		t.Errorf("%s = %q after Load, want 0", name, got)
	}
}

// TestSetLeavesNoTemporaryFileBehind: the write goes through a temporary file beside the target, and a
// stray one in the operator's config directory is litter that looks like a second env file.
func TestSetLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := Set(path, "OFFPEAK_ONLY", "1"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".env" {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name()+" ("+strconv.Itoa(len(entry.Name()))+")")
		}
		t.Errorf("directory holds %s, want only .env", strings.Join(names, ", "))
	}
}
