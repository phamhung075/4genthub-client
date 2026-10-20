package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHelpAnswersUsageOnStdoutAndZero: -h, --help and help are REQUESTS, at every level this command
// has. 2 is the documented usage code, so a help request returning it is indistinguishable from a
// mistyped flag - and -h is the flag package's own trigger, which is how the usage became unreachable
// that way in the first place. A parse error that is NOT help keeps 2 and stderr.
func TestHelpAnswersUsageOnStdoutAndZero(t *testing.T) {
	for _, args := range [][]string{
		{"-h"}, {"--help"}, {"help"},
		{"audit-scan", "-h"}, {"audit-scan", "--help"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(args, &out, &errb)
			if code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}
			if !strings.HasPrefix(out.String(), "usage: seatcheck") {
				t.Errorf("stdout = %q, want the usage", out.String())
			}
			if errb.Len() != 0 {
				t.Errorf("stderr = %q, want nothing: a help request is not an error", errb.String())
			}
		})
	}
}

// TestUnknownSubcommandNamesTheSurvivingRoute is the acceptance for the deleted verb: `send` is an
// UNKNOWN subcommand now rather than a stub that refuses for its own reason, so its refusal is the
// generic one - non-zero, on stderr, naming nothing it did - and it NAMES `4genteam send`, the audited
// path that replaced it. A seat still typing the old spelling reads where to go instead of guessing,
// and no code path here can deliver a message any more.
func TestUnknownSubcommandNamesTheSurvivingRoute(t *testing.T) {
	for _, args := range [][]string{
		{"send", "--to", "b", "--intent", "task", "--", "hi"},
		{"send"},
		{"gossip"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(args, &out, &errb)
			if code == 0 {
				t.Fatalf("exit = 0, want non-zero for the unknown subcommand %q", args[0])
			}
			if !strings.Contains(errb.String(), `unknown subcommand "`+args[0]+`"`) {
				t.Errorf("stderr = %q, want it to refuse %q as unknown", errb.String(), args[0])
			}
			if !strings.Contains(errb.String(), "4genteam send") {
				t.Errorf("stderr = %q, want it to name `4genteam send`", errb.String())
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: an unknown subcommand is an error", out.String())
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestAuditScanFindsBypassAndSkipsKnownWrapper(t *testing.T) {
	dir := t.TempDir()
	observed := writeFile(t, filepath.Join(dir, "observed.txt"),
		"rig send b@r hi\nFOO=1 /usr/bin/rig queue y\nseatcheck send --to b --intent task -- hi\nroom status\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"audit-scan", "--known", "seatcheck", "--file", observed}, &stdout, &stderr)
	if code != 4 {
		t.Fatalf("exit = %d, want 4 (stderr %q)", code, stderr.String())
	}
	want := "bypass-suspected: rig send b@r hi\nbypass-suspected: FOO=1 /usr/bin/rig queue y\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestAuditScanCleanExits0(t *testing.T) {
	dir := t.TempDir()
	observed := writeFile(t, filepath.Join(dir, "observed.txt"), "seatcheck send --to b --intent task -- hi\nroom status\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"audit-scan", "--known", "seatcheck", "--file", observed}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
