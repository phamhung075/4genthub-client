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
	"strconv"
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
		output := asked[5] // go build -ldflags <flags> -o <output> <package>
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

	pins, binary := mustAbs(t, out), CheckerBinary(mustAbs(t, out))
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
		"-ldflags", "-X main.installedPinsDir=" + pins,
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
		"restart the daemon",
		"note: checked " + CheckerName + " on the ",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("stderr does not say %q:\n%s", want, message)
		}
	}
}

// ssLtnpListing is a fake `ss -ltnp`: the real column layout, an OpenRig-named listener on the
// daemon port (v4 and v6 rows), a DIFFERENT OpenRig process on the neighbouring port 7434 (so a
// prefix match on "7433" would be caught), and an unrelated listener.
const ssLtnpListing = "State  Recv-Q Send-Q Local Address:Port Peer Address:Port  Process\n" +
	"LISTEN 0      128          0.0.0.0:22         0.0.0.0:*      users:((\"sshd\",pid=812,fd=3))\n" +
	"LISTEN 0      4096       127.0.0.1:7433       0.0.0.0:*      users:((\"openrig\",pid=5150,fd=8))\n" +
	"LISTEN 0      4096           [::]:7433          [::]:*       users:((\"openrig\",pid=5150,fd=9))\n" +
	"LISTEN 0      4096       127.0.0.1:7434       0.0.0.0:*      users:((\"openrig\",pid=6001,fd=8))\n"

// TestOpenrigDaemonPIDRunsSsLtnp pins the command openrigDaemonPID runs to list listening sockets:
// exactly `ss -ltnp`, and the pid it picks from that text is the OpenRig process on the daemon port.
func TestOpenrigDaemonPIDRunsSsLtnp(t *testing.T) {
	original := captureCommand
	t.Cleanup(func() { captureCommand = original })

	var asked []string
	captureCommand = func(command []string, _ time.Duration) (string, int, error) {
		asked = append([]string(nil), command...)
		return ssLtnpListing, 0, nil
	}
	t.Setenv("OPENRIG_PORT", "7433")
	t.Setenv("OPENRIG_URL", "")

	if got, want := openrigDaemonPID(), 5150; got != want {
		t.Errorf("pid = %d, want %d (the OpenRig listener on 7433, not 6001 on 7434)", got, want)
	}
	if got, want := strings.Join(asked, " "), "ss -ltnp"; got != want {
		t.Errorf("ran %q, want %q", got, want)
	}
}

// TestOpenrigDaemonPortPrefersOpenrigPortThenOpenrigURL pins the port rule: a numeric OPENRIG_PORT
// wins, else the port parsed out of OPENRIG_URL, else the empty fallback (no daemon port -> pid 0 ->
// the shell PATH source).
func TestOpenrigDaemonPortPrefersOpenrigPortThenOpenrigURL(t *testing.T) {
	cases := []struct {
		name string
		port string
		url  string
		want string
	}{
		{"OPENRIG_PORT wins over OPENRIG_URL", "8322", "http://127.0.0.1:9999", "8322"},
		{"a non-numeric OPENRIG_PORT falls through to OPENRIG_URL", "not-a-port", "http://127.0.0.1:9999", "9999"},
		{"OPENRIG_URL with a path after the port", "", "http://127.0.0.1:9099/api", "9099"},
		{"OPENRIG_URL with no port", "", "http://127.0.0.1", ""},
		{"neither variable set is the empty fallback", "", "", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OPENRIG_PORT", test.port)
			t.Setenv("OPENRIG_URL", test.url)
			if got := openrigDaemonPort(); got != test.want {
				t.Errorf("port = %q, want %q", got, test.want)
			}
		})
	}
}

// TestOpenrigDaemonPIDRefusesWhenNoListenerMatches pins the refusals: with no daemon port ss is never
// run, a listing with no listener on the port has no pid, and a failed ss has no pid - each answers
// the function's own zero rather than a guess.
func TestOpenrigDaemonPIDRefusesWhenNoListenerMatches(t *testing.T) {
	original := captureCommand
	t.Cleanup(func() { captureCommand = original })

	t.Run("no daemon port never runs ss", func(t *testing.T) {
		t.Setenv("OPENRIG_PORT", "")
		t.Setenv("OPENRIG_URL", "")
		captureCommand = func([]string, time.Duration) (string, int, error) {
			t.Error("ss ran even though no daemon port is known")
			return "", 0, nil
		}
		if got := openrigDaemonPID(); got != 0 {
			t.Errorf("pid = %d, want 0", got)
		}
	})

	t.Run("no listener on the port", func(t *testing.T) {
		t.Setenv("OPENRIG_PORT", "5555")
		t.Setenv("OPENRIG_URL", "")
		captureCommand = func([]string, time.Duration) (string, int, error) {
			return ssLtnpListing, 0, nil
		}
		if got := openrigDaemonPID(); got != 0 {
			t.Errorf("pid = %d, want 0 (nothing listens on 5555)", got)
		}
	})

	t.Run("ss itself fails", func(t *testing.T) {
		t.Setenv("OPENRIG_PORT", "7433")
		t.Setenv("OPENRIG_URL", "")
		captureCommand = func([]string, time.Duration) (string, int, error) {
			return "", 0, errors.New("ss: command not found")
		}
		if got := openrigDaemonPID(); got != 0 {
			t.Errorf("pid = %d, want 0 after a failed ss", got)
		}
	})
}

// TestProcEnvPathReadsThePathOfTheGivenPid pins the /proc rule against a scratch process table: the
// PATH comes from <rigProcRoot>/<pid>/environ, the pid selects the file, and an unreadable or
// PATH-less process is the empty refusal.
func TestProcEnvPathReadsThePathOfTheGivenPid(t *testing.T) {
	originalRoot := rigProcRoot
	t.Cleanup(func() { rigProcRoot = originalRoot })
	root := t.TempDir()
	rigProcRoot = root

	writeEnviron(t, root, 5150, "HOME=/home/seat\x00PATH=/opt/openrig/bin:/usr/bin\x00LANG=C.UTF-8\x00")
	writeEnviron(t, root, 6001, "HOME=/home/other\x00PATH=/usr/bin\x00\x00")
	writeEnviron(t, root, 6002, "HOME=/home/pathless\x00LANG=C.UTF-8\x00")

	if got, want := procEnvPath(5150), "/opt/openrig/bin:/usr/bin"; got != want {
		t.Errorf("PATH = %q, want %q from /proc/5150/environ", got, want)
	}
	if got, want := procEnvPath(6001), "/usr/bin"; got != want {
		t.Errorf("PATH = %q, want %q from /proc/6001/environ", got, want)
	}
	if got := procEnvPath(9999); got != "" {
		t.Errorf("path = %q, want the empty refusal for an unreadable pid", got)
	}
	if got := procEnvPath(6002); got != "" {
		t.Errorf("path = %q, want the empty refusal for a pid whose environ has no PATH", got)
	}
}

// writeEnviron writes <root>/<pid>/environ, the file procEnvPath reads.
func writeEnviron(t *testing.T, root string, pid int, contents string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
