package clientsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// CheckerName is the seat checker: the binary `sync pull` runs, and the name that has to resolve on
// the PATH a seat inherits.
const CheckerName = "seatcheck"

// The PATH a new seat inherits is not this process's PATH, and that difference is the whole reason
// this file reads three sources instead of one.
const (
	shellPathSource = "shell PATH (no tmux server and no readable rig daemon PATH were found, so no " +
		"seat exists yet)"
	daemonPathSource = "rig daemon PATH (the daemon starts the first tmux server, so the first seat " +
		"inherits it)"
	tmuxPathSource = "tmux global PATH"

	tmuxTimeoutSeconds = 5 * time.Second
)

// pathLimit is the operator-facing explanation the refusal carries, kept verbatim from the Python.
var pathLimit = "seats inherit the tmux global PATH, which is what this checks while a tmux server " +
	"runs (only the default tmux socket is queried); at cold start it reads the PATH of " +
	"the running rig daemon (the daemon starts the first tmux server, so the first seat " +
	"inherits the daemon's environment). If the daemon's PATH does not resolve " +
	CheckerName + ", restart the daemon from a shell where it resolves " +
	"(`rig daemon stop`, `rig daemon start`)"

// CheckerBinary is where the install puts the checker: <store>/bin/<name>.
func CheckerBinary(out string) string { return filepath.Join(out, "bin", CheckerName) }

// CheckerLink is where the install puts the link seats resolve: <real home>/.local/bin/<name>.
func CheckerLink() (string, error) { return checkerLink() }

// checkerLink is that path, and a var so a test never writes into a real home directory.
var checkerLink = func() (string, error) {
	home, err := clientenv.RealHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin", CheckerName), nil
}

// buildCommand is the one place the checker is compiled, so a test can replace the toolchain instead
// of needing one. It returns the command's error unchanged, which is how "go is not installed" stays
// distinguishable from "go build failed".
var buildCommand = func(command []string, cwd string, env []string) error {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	return cmd.Run()
}

// captureCommand runs a command for its output: tmux and ss both answer on stdout.
var captureCommand = func(command []string, timeout time.Duration) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), exit.ExitCode(), nil
	}
	return "", 0, err
}

// tmuxGlobalPath is the PATH new seats inherit, and false without a tmux server.
func tmuxGlobalPath() (string, bool) {
	stdout, code, err := captureCommand([]string{"tmux", "show-environment", "-g", "PATH"}, tmuxTimeoutSeconds)
	if err != nil || code != 0 || !strings.HasPrefix(stdout, "PATH=") {
		return "", false
	}
	return strings.TrimSpace(stdout)[len("PATH="):], true
}

// openrigDaemonPID is the rig daemon pid listening on the OpenRig port, or 0 when it cannot be found.
// The daemon starts the first tmux server, so at cold start the first seat inherits its environment,
// and /proc still has it.
func openrigDaemonPID() int {
	port := openrigDaemonPort()
	if port == "" {
		return 0
	}
	stdout, _, err := captureCommand([]string{"ss", "-ltnp"}, tmuxTimeoutSeconds)
	if err != nil {
		return 0
	}
	seen := regexp.MustCompile(`pid=(\d+)`)
	needle := ":" + port + " "
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.Contains(line, needle) && !strings.Contains(line, ":"+port+"\t") {
			continue
		}
		if match := seen.FindStringSubmatch(line); match != nil {
			pid, err := strconv.Atoi(match[1])
			if err == nil {
				return pid
			}
		}
	}
	return 0
}

// openrigDaemonPort is OPENRIG_PORT, else the port in OPENRIG_URL.
func openrigDaemonPort() string {
	if port := strings.TrimSpace(os.Getenv("OPENRIG_PORT")); port != "" {
		if _, err := strconv.Atoi(port); err == nil {
			return port
		}
	}
	match := regexp.MustCompile(`:(\d+)(?:/|$)`).FindStringSubmatch(os.Getenv("OPENRIG_URL"))
	if match == nil {
		return ""
	}
	return match[1]
}

// procEnvPath is the PATH of process pid from /proc/<pid>/environ, or "" when unreadable.
func procEnvPath(pid int) string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return ""
	}
	for _, entry := range bytes.Split(raw, []byte{0}) {
		if bytes.HasPrefix(entry, []byte("PATH=")) {
			return string(entry[len("PATH="):])
		}
	}
	return ""
}

// seatPath is the PATH a seat inherits, and which source it came from: tmux's global environment
// while a tmux server runs, else the rig daemon's, else this process's. A var so a test can hand the
// resolver a PATH instead of a machine.
var seatPath = func() (string, string) {
	if path, ok := tmuxGlobalPath(); ok {
		return path, tmuxPathSource
	}
	if pid := openrigDaemonPID(); pid != 0 {
		if path := procEnvPath(pid); path != "" {
			return path, daemonPathSource
		}
	}
	return os.Getenv("PATH"), shellPathSource
}

// lookupChecker is the bare name on that PATH, or "" when it does not resolve.
func lookupChecker() string {
	path, _ := seatPath()
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, CheckerName)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// resolveChecker is true when the bare name resolves to the store's own binary. The note on the
// fallback source is printed because an operator reading a failure needs to know which PATH was
// consulted.
func resolveChecker(out string, note io.Writer) bool {
	_, source := seatPath()
	if source == shellPathSource {
		fmt.Fprintf(note, "note: checked %s on the %s\n", CheckerName, source)
	}
	found := lookupChecker()
	if found == "" {
		return false
	}
	return sameFile(found, CheckerBinary(out))
}

// describeFound is what the refusals report: where the name went and which PATH was searched.
func describeFound() string {
	_, source := seatPath()
	found := lookupChecker()
	where := "not on PATH"
	if found != "" {
		real, err := filepath.EvalSymlinks(found)
		if err != nil {
			real = found
		}
		where = found + " -> " + real
	}
	return where + ", on the " + source
}

func sameFile(left, right string) bool {
	one, err := filepath.EvalSymlinks(left)
	if err != nil {
		return false
	}
	two, err := filepath.EvalSymlinks(right)
	if err != nil {
		return false
	}
	return one == two
}

// placeAgent points target at source with a RELATIVE symlink, through a temporary name so a
// half-written or wrong link is never the thing seats resolve.
func placeAgent(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+"."+strconv.Itoa(os.Getpid())+".tmp")
	removePath(temporary)
	relative, err := filepath.Rel(filepath.Dir(target), source)
	if err != nil {
		relative = source
	}
	if err := os.Symlink(relative, temporary); err != nil {
		return fmt.Errorf("cannot link %s to %s: %w", target, source, err)
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		if link, err := os.Lstat(target); err == nil && link.Mode()&os.ModeSymlink == 0 {
			removePath(target)
		}
	}
	return os.Rename(temporary, target)
}

// removePath clears one path whether it is a link, a file or a directory, which is what the
// temporary-name dance needs.
func removePath(path string) {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return
	case info.IsDir():
		_ = os.RemoveAll(path)
	default:
		_ = os.Remove(path)
	}
}

// installCheckerUsage is what `sync install-checker --help` prints.
const installCheckerUsage = `usage: 4genteam sync install-checker --go-dir DIR [--out DIR]

Build <GO_DIR>/cmd/seatcheck with the pinned store compiled in, install it at <OUT>/bin/seatcheck
(OUT defaults to the seat store) and link it where seats resolve it. The install is finished only
when the NAME resolves on the PATH a seat inherits.
`

// RunInstallCheckerVerb is `sync install-checker`: build cmd/seatcheck from the Go module that holds
// it, put it in the store, and link it where seats will find it.
func RunInstallCheckerVerb(_ context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			_, _ = io.WriteString(stdout, installCheckerUsage)
			return clientcmd.ExitOK
		}
	}
	goDir, out, err := parseInstallCheckerArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync install-checker: %v\n", err)
		return clientcmd.ExitUsage
	}
	return installChecker(out, goDir, stdout, stderr)
}

// parseInstallCheckerArgs is the Python's subparser for `install-checker`: `--go-dir` is required,
// `--out` carries the seat-store default, and anything else is a usage error, as argparse's exit 2 is.
func parseInstallCheckerArgs(args []string) (goDir, out string, err error) {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--go-dir" || arg == "--out":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("%s needs a value", arg)
			}
			i++
			if arg == "--go-dir" {
				goDir = args[i]
			} else {
				out = args[i]
			}
		case strings.HasPrefix(arg, "--go-dir="):
			goDir = strings.TrimPrefix(arg, "--go-dir=")
		case strings.HasPrefix(arg, "--out="):
			out = strings.TrimPrefix(arg, "--out=")
		default:
			return "", "", fmt.Errorf("unknown argument: %s", arg)
		}
	}
	if goDir == "" {
		return "", "", errors.New("missing argument: --go-dir")
	}
	out, err = resolveSeatStore(out)
	if err != nil {
		return "", "", err
	}
	return goDir, out, nil
}

func installChecker(out, goDir string, stdout, stderr io.Writer) int {
	resolvedOut, err := filepath.Abs(expandHome(out))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync install-checker: %v\n", err)
		return clientcmd.ExitUsage
	}
	resolvedGoDir, err := filepath.Abs(expandHome(goDir))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync install-checker: %v\n", err)
		return clientcmd.ExitUsage
	}
	binary := CheckerBinary(resolvedOut)
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		fmt.Fprintf(stderr, "4genteam sync install-checker: %v\n", err)
		return clientcmd.ExitUsage
	}
	// The Go cache and temporary files live beside the module being built, so a seat's build never
	// writes into whatever TMPDIR the daemon happened to hand it.
	cache := filepath.Join(resolvedGoDir, ".gocache")
	temp := filepath.Join(resolvedGoDir, ".gotmp")
	for _, directory := range []string{cache, temp} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			fmt.Fprintf(stderr, "4genteam sync install-checker: %v\n", err)
			return clientcmd.ExitUsage
		}
	}
	command := []string{
		"go", "build",
		"-ldflags", "-X main.installedPinsDir=" + resolvedOut,
		"-o", binary,
		"./cmd/seatcheck",
	}
	env := append(os.Environ(), "GOCACHE="+cache, "TMPDIR="+temp)
	switch err := buildCommand(command, resolvedGoDir, env); {
	case errors.Is(err, exec.ErrNotFound):
		fmt.Fprintln(stderr, "4genteam sync install-checker: go is not installed; cannot build the seat checker")
		return clientcmd.ExitUsage
	case err != nil:
		code := 0
		var status interface{ ExitCode() int }
		if errors.As(err, &status) {
			code = status.ExitCode()
		}
		fmt.Fprintf(stderr, "4genteam sync install-checker: go build failed with exit code %d\n", code)
		return clientcmd.ExitRemote
	}
	link, err := checkerLink()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync install-checker: %v\n", err)
		return clientcmd.ExitRemote
	}
	if err := placeAgent(binary, link); err != nil {
		fmt.Fprintf(stderr, "4genteam sync install-checker: %v\n", err)
		return clientcmd.ExitUsage
	}
	// The install is only finished when the NAME resolves: a binary in the store that nothing can
	// call is not an installed checker, and saying so here is cheaper than a seat failing later.
	if !resolveChecker(resolvedOut, stderr) {
		fmt.Fprintf(stderr,
			"4genteam sync install-checker: installed %s and linked %s, but %s does not resolve to it "+
				"on PATH (found: %s); add %s to PATH (%s)\n",
			binary, link, CheckerName, describeFound(), filepath.Dir(link), pathLimit)
		return clientcmd.ExitUsage
	}
	fmt.Fprintf(stdout, "checker:%s\n", binary)
	return clientcmd.ExitOK
}

// expandHome is `--out ~/x` made absolute before it is used as a path.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := clientenv.RealHome(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
