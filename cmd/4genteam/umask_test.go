//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// umaskProbeEnv marks an execution of THIS test binary as the client itself. It exists because the
// statement under test is the first one inside `main`, and `main` is an ordinary function in this
// package: `TestMain` is the one place that can choose to run it instead of the suite.
const umaskProbeEnv = "GENTEAM_UMASK_PROBE"

// The umask the tree must run under, and the one it is forked with: nothing the group or another user can
// read. 0022 is the machine default the defect was measured under.
const (
	wantUmask      = "0077"
	inheritedUmask = "0022"
)

func TestMain(m *testing.M) {
	if os.Getenv(umaskProbeEnv) == "1" {
		// The probe: the real entry point, with the argv the case below chose. main() exits, and the verb
		// it is given waits on stdin, so this process is up for as long as the case needs it.
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestTheEntryPointTakesTheUmaskForItselfAndForWhatItStarts covers the CALL SITE rather than the call.
// `TestRestrictUmaskLandsOnThisProcessAndOnWhatItStarts` calls `RestrictUmask` itself, so it stays green
// when the call at the top of `main` is deleted - and everything the client writes, the runtime it starts
// and the agents' transcripts below that take their mode bits from that one call. Here the client is run
// for real, and the child's own umask is read from /proc while it is alive.
//
// WHY THE CHILD HAS TO STAY UP: a process's umask is only readable while the process exists, so the verb
// it is given (`decide`, which reads its questions from stdin) is one that waits, and the writer end of
// that pipe is held open here so stdin never reaches EOF.
func TestTheEntryPointTakesTheUmaskForItselfAndForWhatItStarts(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate this test binary to re-exec as the client: %v", err)
	}

	// WHAT THE CHILD STARTS WITH MUST BE KNOWN, or a client whose call was dropped would pass anyway: a
	// seat's whole tree runs under 0077, so that inherited value would read as the fix.
	previous := syscall.Umask(0o022)
	defer syscall.Umask(previous)

	stdin, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("cannot make the pipe the waiting verb reads: %v", err)
	}
	defer writer.Close()

	cmd := exec.Command(self, "decide", "--tier", "seat", "--summary", "umask probe", "--questions-file", "-")
	cmd.Stdin = stdin
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), umaskProbeEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start the client: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	_ = stdin.Close() // the child holds its own copy of the read end

	got, settled, err := processUmask(cmd.Process.Pid, inheritedUmask, 5*time.Second)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !settled {
		t.Fatalf("the client's entry point never took the umask: the process still reads %q while it is "+
			"waiting in its verb, where a run that took it reads %q - the call at the top of main is what this pins",
			got, wantUmask)
	}
	if got != wantUmask {
		t.Errorf("the process's umask = %q, want %q", got, wantUmask)
	}
}

// processUmask reads a live process's own umask out of /proc/<pid>/status and waits for it to stop being
// `starting` - the value it is forked with, and the value a dropped call leaves it at. It reports whether
// the field ever settled: a process that keeps `starting` until the deadline never took the umask.
func processUmask(pid int, starting string, timeout time.Duration) (string, bool, error) {
	path := fmt.Sprintf("/proc/%d/status", pid)
	deadline := time.Now().Add(timeout)
	value := ""
	for {
		status, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return value, true, fmt.Errorf("the client exited before its umask could be read: the verb did not wait")
		}
		if err != nil {
			return value, true, fmt.Errorf("cannot read %s: %v", path, err)
		}
		if got := umaskField(string(status)); got != "" {
			value = got
			if value != starting {
				return value, true, nil
			}
		}
		if !time.Now().Before(deadline) {
			if value == "" {
				return "", true, fmt.Errorf("%s reports no Umask field: this kernel cannot answer the question", path)
			}
			return value, false, nil
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// umaskField is the value of the `Umask:` line of a /proc/<pid>/status body, or "" when it has no such
// line - which a process whose process group is not fully set up yet can be, so a caller polls.
func umaskField(status string) string {
	for _, line := range strings.Split(status, "\n") {
		if rest, ok := strings.CutPrefix(line, "Umask:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
