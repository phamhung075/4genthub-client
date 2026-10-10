package clientcmd

import (
	"errors"
	"strings"
	"testing"
)

func rigMissing(t *testing.T) {
	t.Helper()
	previous := LookPath
	LookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { LookPath = previous })
}

func osAs(t *testing.T, name string) {
	t.Helper()
	previous := GOOS
	GOOS = name
	t.Cleanup(func() { GOOS = previous })
}

// lineCount is how many lines a message has, because "ONE clear message" is the requirement and a
// message that grew into three lines would still pass a Contains check.
func lineCount(s string) int {
	trimmed := strings.TrimRight(s, "\n")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

// TestRequireRigRefusesWithOneMessage is the platform matrix's refusal, asserted by SHAPE: an error,
// one line, the reason named. The dispatcher turns it into a non-zero exit with no partial work.
func TestRequireRigRefusesWithOneMessage(t *testing.T) {
	rigMissing(t)
	osAs(t, "linux")

	rig, err := RequireRig()
	if err == nil {
		t.Fatalf("a machine with no rig resolved one: %+v", rig)
	}
	if !strings.Contains(err.Error(), "rig is not on PATH") {
		t.Errorf("message = %q, want it to name what is missing", err.Error())
	}
	if got := lineCount(err.Error()); got != 1 {
		t.Errorf("message is %d lines, want ONE clear message:\n%s", got, err.Error())
	}
}

// TestRequireRigNamesTheWslRouteOnWindows: the matrix's middle row. A Windows machine without rig must
// be told the supported route rather than left to guess, and it must still be a refusal.
func TestRequireRigNamesTheWslRouteOnWindows(t *testing.T) {
	rigMissing(t)
	osAs(t, "windows")

	_, err := RequireRig()
	if err == nil {
		t.Fatal("native Windows with no rig resolved one")
	}
	for _, want := range []string{"native Windows", "wsl.exe -e rig"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message = %q, want it to name %q", err.Error(), want)
		}
	}
}

// TestRequireRigResolvesOnAPath: the Linux/WSL2/macOS row, and the shape the Windows route would use.
func TestRequireRigResolvesOnAPath(t *testing.T) {
	previous := LookPath
	LookPath = func(string) (string, error) { return "/usr/local/bin/rig", nil }
	t.Cleanup(func() { LookPath = previous })

	rig, err := RequireRig()
	if err != nil {
		t.Fatalf("RequireRig: %v", err)
	}
	if rig.Program != "/usr/local/bin/rig" || len(rig.Prefix) != 0 {
		t.Errorf("rig = %+v, want the program and no prefix on this platform", rig)
	}
}
