// Package clientcmd is the CONTRACT between the 4genteam dispatcher and every subcommand
// package. It exists so that the two implementation packages (clientsync and clientbridge) share one
// definition of what a subcommand is and how it reaches OpenRig, rather than each guessing at a type
// and turning a merge into a rewrite.
//
// The dispatcher is thin on purpose: it reads argv[0] and the first argument, resolves the rig tool
// ONCE for the commands that need it, and calls Run. Registering a subcommand means returning a
// Command from your package; it never means editing main.go, which is why this lives outside it.
package clientcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// Exit codes the dispatcher and every command share.
const (
	ExitOK = 0
	// ExitUsage is a caller error: the verb exists and the arguments are wrong.
	ExitUsage = 2
	// ExitUnavailable is the platform matrix's refusal: the verb exists, this machine cannot do it,
	// and the message says exactly why. An unported verb returns it too, so a verb that is not in the
	// build can never look like one that ran. It coincides with the Python client's EXIT_FAILED (3).
	ExitUnavailable = 3
	// ExitBehind is a seat whose local pin differs from the cloud's snapshot. MEASURED from
	// openrig_seat_client.py: EXIT_BEHIND = 4 - the first draft of the Go status core used 1, and the
	// verification its own comment asked for caught it.
	ExitBehind = 4
	// ExitRemote is a failure talking to the cloud or to the rig: the verb ran and the remote did not
	// answer as it should. It is the bridge's reporting failure and NOT the client script's
	// EXIT_FAILED (3); the two scripts numbered their failures differently, so it is named here.
	//
	// ONE PLACE A CALLER LOOKS: codes scattered per package make a bare 1 ambiguous - a caller could
	// not tell a seat being behind from a report not being accepted.
	ExitRemote = 1
)

// Rig is how a command reaches OpenRig: the resolved program and its argument prefix. On Linux, WSL2
// and macOS the prefix is empty; the Windows route through WSL puts `-e rig` in it, and having the
// shape now is what keeps call sites from assuming a bare `rig`.
type Rig struct {
	Program string
	Prefix  []string
}

// CodedError is the Python client scripts' SyncError: a message plus the exit code the process should
// end with, so a failure deep in a helper carries its own code to the top instead of every layer
// re-deciding. The Python raises it from inside the helpers (fetch_rigspec, read_lock, request_json)
// and its main() unwraps `err.code`; this is the same shape, which is why the codes above live beside
// it rather than in the packages that raise them.
type CodedError struct {
	Message string
	Code    int
}

func (e *CodedError) Error() string { return e.Message }

// CodeOf is the exit code a failure should end with: a CodedError's own code when it carries one, and
// otherwise the caller's fallback - which mirrors the Python's `return EXIT_USAGE if code == EXIT_USAGE
// else EXIT_FAILED` mapping in one place instead of at every call site.
func CodeOf(err error, fallback int) int {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return fallback
}

// Run executes rig with an ARGUMENT LIST - the only place any command starts rig, so the
// no-shell-string rule has one home.
func (r *Rig) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.Program, append(append([]string{}, r.Prefix...), args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("rig %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Command is one subcommand. Name is what the user types.
type Command interface {
	Name() string
	Summary() string
	// NeedsRig decides whether RequireRig runs before Run, so the platform matrix is applied in ONE
	// place rather than remembered at each call site.
	NeedsRig() bool
	Run(ctx context.Context, rig *Rig, args []string, stdout, stderr io.Writer) int
}

// Seams the tests replace. A package-level variable rather than an interface added for testing, which
// is the repo's rule; the platform matrix is untestable without these two.
var (
	// LookPath finds the rig tool.
	LookPath = exec.LookPath
	// GOOS is the platform the matrix branches on.
	GOOS = runtime.GOOS
)

// RequireRig resolves the rig tool, or returns the ONE message that says what is missing. This is the
// platform matrix, and there is no second path out of it:
//
//   - Linux, WSL2, macOS: rig on PATH, used directly.
//   - Native Windows with OpenRig inside WSL2: reached as `wsl.exe -e rig ...`, explicitly.
//   - Native Windows without OpenRig: no rig, and every command that needs one refuses here.
//
// The refusal is a non-zero exit with ONE line naming what is missing - the spec's requirement that a
// command which needs rig and cannot reach it must never downgrade silently.
func RequireRig() (*Rig, error) {
	path, err := LookPath("rig")
	if err == nil {
		return &Rig{Program: path}, nil
	}
	if GOOS == "windows" {
		return nil, errors.New(
			"rig is not on PATH and this is native Windows: OpenRig supports Linux and macOS only " +
				"(its own docs say native Windows is unsupported and WSL2 is untested), so the supported " +
				"route is OpenRig inside WSL2, reached as `wsl.exe -e rig ...`. Install OpenRig there, or " +
				"run a command that does not need it: sync status, sync pull, feedback",
		)
	}
	return nil, errors.New(
		"rig is not on PATH: this command needs the local OpenRig daemon. Install OpenRig, or run a " +
			"command that does not need it: sync status, sync pull, feedback",
	)
}
