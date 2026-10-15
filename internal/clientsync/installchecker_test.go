package clientsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// exitCodeError is a failed command carrying the code it failed with: what the real *exec.ExitError
// is, without needing a real process to fail.
type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitCodeError) ExitCode() int { return int(e) }

// installCheckerFixtures points the inherited PATH, the link and the compiler at scratch
// directories, so the install under test never touches a real home directory or a real toolchain.
func installCheckerFixtures(t *testing.T) (binDir, link string) {
	t.Helper()
	binDir = filepath.Join(t.TempDir(), ".local", "bin")
	link = filepath.Join(binDir, CheckerName)
	originalSeatPath, originalLink, originalBuild := seatPath, checkerLink, buildCommand
	t.Cleanup(func() { seatPath, checkerLink, buildCommand = originalSeatPath, originalLink, originalBuild })
	seatPath = func() (string, string) { return binDir, tmuxPathSource }
	checkerLink = func() (string, error) { return link, nil }
	return binDir, link
}

// compiling is a compiler that SUCCEEDS: it records what it was asked to do and writes the output
// file the real build would have written.
func compiling(command *[]string, cwd *string, env *[]string) func([]string, string, []string) error {
	return func(asked []string, where string, environment []string) error {
		*command, *cwd, *env = asked, where, environment
		output := asked[3] // go build -o <output> <package>
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return err
		}
		return os.WriteFile(output, []byte("fake checker"), 0o755)
	}
}

func hasEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	return real
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

// The four behaviours the Python tests pin: it builds and links, a missing toolchain and a failed
// build both refuse and link NOTHING, and an install whose name still does not resolve says why.
func TestInstallCheckerBuildsAndLinksOnPath(t *testing.T) {
	_, link := installCheckerFixtures(t)
	goDir, out := t.TempDir(), t.TempDir()
	var command []string
	var cwd string
	var env []string
	buildCommand = compiling(&command, &cwd, &env)

	var stdout, stderr bytes.Buffer
	code := RunInstallCheckerVerb(context.Background(), []string{"--go-dir", goDir, "--out", out}, &stdout, &stderr)

	binary := CheckerBinary(mustAbs(t, out))
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	if got, want := stdout.String(), "checker:"+binary+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	// The installed link now satisfies the check `sync pull` makes.
	if got := resolved(t, link); got != binary {
		t.Errorf("the link resolves to %q, want %q", got, binary)
	}
	want := []string{
		"go", "build",
		"-o", binary,
		"./cmd/seatcheck",
	}
	if strings.Join(command, " ") != strings.Join(want, " ") {
		t.Errorf("built with %v, want %v", command, want)
	}
	if cwd != mustAbs(t, goDir) {
		t.Errorf("built in %q, want %q", cwd, mustAbs(t, goDir))
	}
	for _, want := range []string{
		"GOCACHE=" + filepath.Join(mustAbs(t, goDir), ".gocache"),
		"TMPDIR=" + filepath.Join(mustAbs(t, goDir), ".gotmp"),
	} {
		if !hasEnv(env, want) {
			t.Errorf("the build environment carries no %s", want)
		}
	}
}

func TestInstallCheckerWithoutGoRefuses(t *testing.T) {
	_, link := installCheckerFixtures(t)
	buildCommand = func([]string, string, []string) error {
		return &exec.Error{Name: "go", Err: exec.ErrNotFound}
	}

	var stdout, stderr bytes.Buffer
	code := RunInstallCheckerVerb(context.Background(),
		[]string{"--go-dir", t.TempDir(), "--out", t.TempDir()}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "go is not installed") {
		t.Errorf("stderr = %q, want it to name the missing toolchain", stderr.String())
	}
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the install linked something anyway: %v", err)
	}
}

func TestInstallCheckerBuildFailureExitsRemoteAndLinksNothing(t *testing.T) {
	_, link := installCheckerFixtures(t)
	buildCommand = func([]string, string, []string) error { return exitCodeError(3) }

	var stdout, stderr bytes.Buffer
	code := RunInstallCheckerVerb(context.Background(),
		[]string{"--go-dir", t.TempDir(), "--out", t.TempDir()}, &stdout, &stderr)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
	}
	if !strings.Contains(stderr.String(), "go build failed with exit code 3") {
		t.Errorf("stderr = %q, want it to carry the compiler's code", stderr.String())
	}
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed build linked something: %v", err)
	}
}

func TestInstallCheckerRefusesWhenTheNameDoesNotResolve(t *testing.T) {
	_, link := installCheckerFixtures(t)
	// The build succeeds, but the name cannot resolve: the inherited PATH is a directory that does not
	// hold it. The shell source also makes the note the resolver prints appear.
	seatPath = func() (string, string) { return t.TempDir(), shellPathSource }
	var command []string
	var cwd string
	var env []string
	buildCommand = compiling(&command, &cwd, &env)

	var stdout, stderr bytes.Buffer
	code := RunInstallCheckerVerb(context.Background(),
		[]string{"--go-dir", t.TempDir(), "--out", t.TempDir()}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	message := stderr.String()
	for _, want := range []string{
		"does not resolve",
		"add " + filepath.Dir(link) + " to PATH",
		"restart tmux",
		"note: checked " + CheckerName + " on the ",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("stderr does not say %q:\n%s", want, message)
		}
	}
}

// TestSeatPathReadsTmuxThenThisShell pins the REAL resolver behind the seatPath var: tmux's global
// PATH while a server runs, else this process's PATH.
func TestSeatPathReadsTmuxThenThisShell(t *testing.T) {
	original := captureCommand
	t.Cleanup(func() { captureCommand = original })

	t.Run("no tmux server: this process PATH", func(t *testing.T) {
		captureCommand = func([]string, time.Duration) (string, int, error) { return "", 1, nil }
		t.Setenv("PATH", "/shell/only")
		path, source := seatPath()
		if path != "/shell/only" || source != shellPathSource {
			t.Errorf("seatPath = (%q, %q), want (%q, %q)", path, source, "/shell/only", shellPathSource)
		}
	})

	t.Run("a tmux server: the tmux global PATH", func(t *testing.T) {
		captureCommand = func([]string, time.Duration) (string, int, error) {
			return "PATH=/tmux/global/bin\n", 0, nil
		}
		path, source := seatPath()
		if path != "/tmux/global/bin" || source != tmuxPathSource {
			t.Errorf("seatPath = (%q, %q), want (%q, %q)", path, source, "/tmux/global/bin", tmuxPathSource)
		}
	})
}
