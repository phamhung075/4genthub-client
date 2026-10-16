//go:build !windows

package clientenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRestrictUmaskLandsOnThisProcessAndOnWhatItStarts is the contract of the call rather than of the OS: after
// it, a write that asks for 0666 leaves the group and the world no bit, and a CHILD process reports the same
// umask - the agents that write the seats' transcripts are children of a child of this process, and their files'
// mode is the half of the change a per-site mode could not reach.
func TestRestrictUmaskLandsOnThisProcessAndOnWhatItStarts(t *testing.T) {
	RestrictUmask()

	path := filepath.Join(t.TempDir(), "asked-for-0666")
	if err := os.WriteFile(path, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600: a write that asks for 0666 must not leave the others a bit", info.Mode().Perm())
	}

	out, err := exec.Command("sh", "-c", "umask").Output()
	if err != nil {
		t.Fatalf("sh -c umask: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "0077" {
		t.Errorf("a child's umask = %q, want 0077", got)
	}
}
