package clientenv

import (
	"os/exec"
	"path/filepath"
)

// TmuxRoot is the directory of the tmux server the runtime owns: <state root>/tmux. It is the same
// derivation as the runtime's tmux_root_for (rust/agenthub-runtime/src/main.rs), which is what makes the
// operator's attach path and this client agree without either memorising a name.
func TmuxRoot(stateRoot string) string {
	return filepath.Join(stateRoot, "tmux")
}

// TmuxArgv is the ONE way the client runs tmux: the argv that reaches the runtime's own server, whatever
// the caller's environment holds. `env` sets TMUX_TMPDIR to TmuxRoot, which overrides an inherited value,
// and unsets TMUX, which would otherwise make tmux talk to the server of the pane the client happens to run
// in. A caller without a resolvable state root gets the refusal, never the host's default server.
func TmuxArgv(args ...string) ([]string, error) {
	root, err := ResolveStateRoot("")
	if err != nil {
		return nil, err
	}
	return append([]string{"env", "-u", "TMUX", "TMUX_TMPDIR=" + TmuxRoot(root), "tmux"}, args...), nil
}

// TmuxCommand is TmuxArgv as a command, for the callers that exec directly.
func TmuxCommand(args ...string) (*exec.Cmd, error) {
	argv, err := TmuxArgv(args...)
	if err != nil {
		return nil, err
	}
	return exec.Command(argv[0], argv[1:]...), nil
}
