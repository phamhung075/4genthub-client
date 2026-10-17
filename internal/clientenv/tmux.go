package clientenv

import (
	"os/exec"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// TmuxRoot is the directory of the tmux server the runtime owns: <state root>/tmux. It is the same
// derivation as the runtime's tmux_root_for (rust/agenthub-runtime/src/main.rs), which is what makes the
// operator's attach path and this client agree without either memorising a name.
func TmuxRoot(stateRoot string) string {
	return filepath.Join(stateRoot, "tmux")
}

// TmuxArgvAt is TmuxArgv for a state root the caller has already resolved - the shape a verb that takes
// `--state-root` needs, because the runtime puts its tmux server under the state dir it was started with,
// and a client told about that root must ask that server and no other. An empty root is REFUSED with the
// one state-root refusal rather than falling back to this machine's own: a fallback here would make the
// verb read a tree it was not told about, which is the split this function exists to prevent.
func TmuxArgvAt(stateRoot string, args ...string) ([]string, error) {
	if stateRoot == "" {
		return nil, &clientcmd.CodedError{Message: StateRootMessage, Code: clientcmd.ExitUsage}
	}
	return append([]string{"env", "-u", "TMUX", "TMUX_TMPDIR=" + TmuxRoot(stateRoot), "tmux"}, args...), nil
}

// TmuxCommandAt is TmuxArgvAt as a command, for the callers that exec directly.
func TmuxCommandAt(stateRoot string, args ...string) (*exec.Cmd, error) {
	argv, err := TmuxArgvAt(stateRoot, args...)
	if err != nil {
		return nil, err
	}
	return exec.Command(argv[0], argv[1:]...), nil
}

// TmuxArgv is the ONE way the client runs tmux when the verb has no root of its own: the argv that reaches
// the runtime's own server for THIS machine's root, whatever the caller's environment holds. `env` sets
// TMUX_TMPDIR to TmuxRoot, which overrides an inherited value, and unsets TMUX, which would otherwise make
// tmux talk to the server of the pane the client happens to run in. A caller without a resolvable state
// root gets the refusal, never the host's default server.
func TmuxArgv(args ...string) ([]string, error) {
	root, err := ResolveStateRoot("")
	if err != nil {
		return nil, err
	}
	return TmuxArgvAt(root, args...)
}

// TmuxCommand is TmuxArgv as a command, for the callers that exec directly.
func TmuxCommand(args ...string) (*exec.Cmd, error) {
	argv, err := TmuxArgv(args...)
	if err != nil {
		return nil, err
	}
	return exec.Command(argv[0], argv[1:]...), nil
}
