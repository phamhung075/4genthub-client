package clientlifecycle

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// A lifecycle verb carrying the RUNTIME'S spelling exits 2 with the one sentence, and NOTHING is started:
// the refusal comes before the daemon seam, so a script cannot half-start a room and then be told its flag
// was the wrong word. `compact-run` is asserted at its parser, which is the whole of its argument handling.
func TestTheRuntimeSpellingIsRefusedBeforeAnyLifecycleAction(t *testing.T) {
	restoreAll(t)
	var ran []string
	ensureDaemon = func(string, io.Writer) (string, error) {
		ran = append(ran, "ensureDaemon")
		return "", nil
	}
	recordRoomFor = func(string, io.Writer) bool { ran = append(ran, "record"); return true }
	startAuthority = func(string, io.Writer, io.Writer) bool { ran = append(ran, "agent"); return true }

	for _, name := range []string{"up", "idle-run", "budget-run"} {
		for _, flag := range [][]string{{"--state-dir", "/x"}, {"--state-dir=/x"}} {
			args := append([]string{"room-a"}, flag...)
			var out, errOut bytes.Buffer
			code := commandNamed(t, name).Run(context.Background(), args, &out, &errOut)
			if code != clientcmd.ExitUsage {
				t.Errorf("%s %q: exit %d, want %d (stderr %q)", name, flag, code, clientcmd.ExitUsage, errOut.String())
			}
			if !strings.Contains(errOut.String(), clientenv.StateRootRetiredMessage) {
				t.Errorf("%s %q: stderr = %q, want the one sentence", name, flag, errOut.String())
			}
		}
	}

	var errOut bytes.Buffer
	if _, ok := ParseCompactRunOptions([]string{"--room", "room-a", "--state-dir", "/x"}, &errOut); ok {
		t.Error("compact-run accepted the runtime's spelling")
	}
	if !strings.Contains(errOut.String(), clientenv.StateRootRetiredMessage) {
		t.Errorf("compact-run: stderr = %q, want the one sentence", errOut.String())
	}

	if len(ran) != 0 {
		t.Errorf("a refused invocation ran a lifecycle action: %v", ran)
	}
}

// THE RUNTIME'S OWN FLAG DOES NOT MOVE: `up` still spawns the daemon with `--state-dir <root>`, which is
// the runtime binary's spelling. The client's rename stops at the client (ruling R2), and this pins the argv
// that carries the room's absolute root into the daemon the room will run under.
func TestTheDaemonSpawnKeepsTheRuntimesOwnFlag(t *testing.T) {
	cmd := runtimeDaemonCommand("/bin/agenthub-runtime", "/root/state", "/root/run/runtime.sock")
	want := []string{"/bin/agenthub-runtime", "--state-dir", "/root/state", "--socket", "/root/run/runtime.sock", "daemon"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("argv = %q, want %q", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (whole argv %q)", i, cmd.Args[i], want[i], cmd.Args)
		}
	}
}

func commandNamed(t *testing.T, name string) clientcmd.Command {
	t.Helper()
	for _, command := range Commands() {
		if command.Name() == name {
			return command
		}
	}
	t.Fatalf("no command named %q is registered", name)
	return nil
}
