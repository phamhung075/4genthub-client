package clientenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTmuxArgvPointsAtTheRuntimesServerWhateverTheEnvironmentHolds(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "/elsewhere")
	realHome = func() (string, error) { return "/home/someone", nil }
	t.Cleanup(func() { realHome = RealHome })

	argv, err := TmuxArgv("list-sessions", "-F", "#{session_name}")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"env", "-u", "TMUX", "TMUX_TMPDIR=" + filepath.Join("/home/someone", ".config", "4genthub", "tmux"), "tmux", "list-sessions", "-F", "#{session_name}"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

func TestTmuxArgvRefusesRatherThanUseTheDefaultServer(t *testing.T) {
	realHome = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { realHome = RealHome })
	if argv, err := TmuxArgv("ls"); err == nil {
		t.Fatalf("a machine with no state root got %q", argv)
	}
}

func TestTmuxRootIsTheRuntimesDerivation(t *testing.T) {
	if got := TmuxRoot("/s"); got != "/s/tmux" {
		t.Fatalf("TmuxRoot = %q", got)
	}
}
