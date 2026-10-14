// Package clientcmd is the CONTRACT between the 4genteam dispatcher and every subcommand
// package. It exists so that the two implementation packages (clientsync and clientbridge) share one
// definition of what a subcommand is, rather than each guessing at a type
// and turning a merge into a rewrite.
//
// The dispatcher is thin on purpose: it reads argv[0] and the first argument and calls Run. Registering a subcommand means returning a
// Command from your package; it never means editing main.go, which is why this lives outside it.
package clientcmd

import (
	"context"
	"errors"
	"io"
)

// Version is the CLIENT's own version. It lives here because TWO callers need the same string and
// neither may be the authority for the other: the binary prints it (`4genteam version`) and the connector
// puts it in every `hello` frame, where the cloud records which client an upload came from. It is
// deliberately NOT the server's release identity - config.ReleaseVersion marks what the server deploys,
// while this marks the binary on somebody's machine.
const Version = "0.1.0"

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
	// the retired Python seat client: EXIT_BEHIND = 4 - the first draft of the Go status core used 1, and the
	// verification its own comment asked for caught it.
	ExitBehind = 4
	// ExitRemote is a failure talking to the cloud or to the rig: the verb ran and the remote did not
	// answer as it should. It is the bridge's reporting failure and NOT the client script's
	// EXIT_FAILED (3); the two scripts numbered their failures differently, so it is named here.
	//
	// ONE PLACE A CALLER LOOKS: codes scattered per package make a bare 1 ambiguous - a caller could
	// not tell a seat being behind from a report not being accepted.
	ExitRemote = 1
	// The room-scoped contract (DESIGN-room-scoped-up-and-siblings §2.5) numbers a room's failures
	// differently from the rest of the CLI, because its acceptance has to tell a bad invocation from a room
	// that came up short: 0 only when every seat is running, 2 when the daemon started but a seat did not,
	// 3 for usage or a room the daemon does not know. ExitPartialStart shares ExitUsage's value and
	// ExitRoomRefused shares ExitUnavailable's; both are named rather than borrowed, so `up` and the `room`
	// verbs never have to use a name that means something else to say what happened.
	ExitPartialStart = 2
	ExitRoomRefused  = 3
)

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

// Command is one subcommand. Name is what the user types.
type Command interface {
	Name() string
	Summary() string
	Run(ctx context.Context, args []string, stdout, stderr io.Writer) int
}

// HelpRequested reports whether an argument list asks for help. EVERY verb funnels `--help` through
// this, so help is help wherever the flag sits in the argument list - which is what the Python's
// argparse did, and what a verb that parses its own flags otherwise loses to an unknown-flag error.
// The mission's rule is that every verb answers --help, and this is the one place that is decided.
func HelpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}
