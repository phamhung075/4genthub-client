package clientruntime

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func TestDefaultSocketPath(t *testing.T) {
	path := DefaultSocketPath()
	if path == "" {
		t.Fatal("expected non-empty socket path")
	}
}

func TestNewClient(t *testing.T) {
	client := NewClient("/tmp/test.sock")
	if client.SocketPath != "/tmp/test.sock" {
		t.Fatalf("expected /tmp/test.sock, got %s", client.SocketPath)
	}
}

// The tests below are the three legs of the socket-knob contract, at the ONE resolution point. The live form of each
// leg creates a socket and dials it - which is what a cutover gate runs against a scrubbed HOME - and what is asserted
// here is the resolution those legs rest on, so a regression fails in a second rather than in a room.
//
// The NAME is pinned rather than discovered: an owner's harness and a branch client meet on exactly this string, which
// is why renaming it would be a breaking change rather than a cleanup.
func TestRuntimeSocketEnvIsTheContractedName(t *testing.T) {
	if RuntimeSocketEnv != "AGENTHUB_RUNTIME_SOCKET" {
		t.Fatalf("RuntimeSocketEnv = %q, want %q", RuntimeSocketEnv, "AGENTHUB_RUNTIME_SOCKET")
	}
}

// UNSET - the regression guard. With no override the path is byte-for-byte the machine's own location, so a machine
// that never sets the knob cannot be moved by this change.
func TestSocketPathUnsetIsTheUnchangedDerivation(t *testing.T) {
	t.Setenv(RuntimeSocketEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_RUNTIME_DIR", "")
	if got, want := DefaultSocketPath(), filepath.Join(home, ".config", "4genthub", "run", "runtime.sock"); got != want {
		t.Fatalf("no override: got %s, want %s", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", home)
	if got, want := DefaultSocketPath(), filepath.Join(home, "agenthub.sock"); got != want {
		t.Fatalf("no override with XDG_RUNTIME_DIR: got %s, want %s", got, want)
	}
}

// SET - the explicit path is the path the client uses, and it is the path a daemon this client starts binds.
func TestSocketPathSetIsUsedAtTheExplicitPath(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit.sock")
	t.Setenv(RuntimeSocketEnv, explicit)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")

	if got := DefaultSocketPath(); got != explicit {
		t.Fatalf("with override: got %s, want %s", got, explicit)
	}
	if got := NewClient("").SocketPath; got != explicit {
		t.Fatalf(`with override through NewClient(""): got %s, want %s`, got, explicit)
	}
}

// NEVER BOTH - two inputs, one socket, which is the whole point of a single resolution point. A machine whose
// environment would ALSO derive a path must resolve to the override and NOT to the derivation: the knob REPLACES the
// second input rather than joining it, and that is what keeps a branch client and the branch daemon it starts on one
// socket. A blank value counts as unset, so a copied .env that blanked the switch cannot resolve to the working dir.
func TestSocketPathNeverResolvesTwoInputsToOneSocket(t *testing.T) {
	scrubbed := t.TempDir()
	explicit := filepath.Join(scrubbed, "explicit.sock")
	derived := filepath.Join(scrubbed, "agenthub.sock")
	t.Setenv("HOME", scrubbed)
	t.Setenv("XDG_RUNTIME_DIR", scrubbed)
	t.Setenv(RuntimeSocketEnv, explicit)

	got := DefaultSocketPath()
	if got == derived {
		t.Fatalf("two inputs, one socket: the derivation %s was consulted while the override %s was set", derived, explicit)
	}
	if got != explicit {
		t.Fatalf("two inputs, one socket: got %s, want the override %s", got, explicit)
	}
	t.Setenv(RuntimeSocketEnv, "   ")
	if got := DefaultSocketPath(); got != derived {
		t.Fatalf("a blank override is unset: got %s, want %s", got, derived)
	}
}

// PRECEDENCE - an explicit path beats the variable, and the variable beats the HOME default. The failure mode this
// guards is not the knob working NOWHERE; it is the knob working EVERYWHERE, because one resolution point means every
// caller inherits the variable, and a caller that passed a path on purpose must still get that path.
//
// It is falsifiable in the way that matters: make the variable win inside NewClient and this test goes red.
func TestExplicitPathBeatsTheOverride(t *testing.T) {
	caller := filepath.Join(t.TempDir(), "caller.sock")
	fromEnv := filepath.Join(t.TempDir(), "from-env.sock")
	t.Setenv(RuntimeSocketEnv, fromEnv)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")

	if got := NewClient(caller).SocketPath; got != caller {
		t.Fatalf("an explicit path lost to the variable: got %s, want %s", got, caller)
	}
	if got := NewClient("").SocketPath; got != fromEnv {
		t.Fatalf("the variable lost to the HOME default: got %s, want %s", got, fromEnv)
	}
}

// TestAnOlderRuntimesFramingFailureIsNamedAsSuch: an action the installed runtime does not know comes back in
// serde's own words, and as the verb's ANSWER that reads as something about the room - which is a lie about
// versions. It is typed, it names the runtime and the action, and it leaves with ExitUnavailable.
func TestAnOlderRuntimesFramingFailureIsNamedAsSuch(t *testing.T) {
	sock, _ := fakeRuntime(t, func(string) string {
		return "{\"ok\":false,\"error\":\"invalid request JSON: unknown variant `QueueList`, expected one of `StartRoom`, `StopRoom`\"}\n"
	})
	client := NewClient(sock)

	_, err := client.Call("QueueList", nil)
	var tooOld *RuntimeTooOld
	if !errors.As(err, &tooOld) {
		t.Fatalf("err = %#v, want a RuntimeTooOld", err)
	}
	if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUnavailable {
		t.Errorf("exit = %d, want ExitUnavailable (%d)", code, clientcmd.ExitUnavailable)
	}
	for _, want := range []string{"QueueList", sock, "unknown variant", "different builds"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, err.Error())
		}
	}
}

// TestAVerbsOwnRefusalIsNotRewrittenAsAVersionMismatch: `unknown qitem_id 'x'` is the runtime refusing WORK.
// Rewriting it as "unimplemented" would convert a real refusal into a lie about versions, so only the framing
// markers convert and every other ok=false stays the runtime's own refusal, with its own code.
func TestAVerbsOwnRefusalIsNotRewrittenAsAVersionMismatch(t *testing.T) {
	sock, _ := fakeRuntime(t, func(string) string {
		return `{"ok":false,"error":"unknown qitem_id 'qitem-1'"}` + "\n"
	})
	client := NewClient(sock)

	_, err := client.Call("QueueStop", nil)
	var refused *DaemonRefused
	if !errors.As(err, &refused) {
		t.Fatalf("err = %#v, want a DaemonRefused", err)
	}
	var tooOld *RuntimeTooOld
	if errors.As(err, &tooOld) {
		t.Errorf("a verb's own refusal was rewritten as a version mismatch: %s", err.Error())
	}
	if code := clientcmd.CodeOf(err, 0); code == clientcmd.ExitUnavailable {
		t.Errorf("a verb's own refusal left with ExitUnavailable")
	}
}

// TestSeatControlWritesTheFramesTheRuntimeReads pins the three seat-control frames at the wire: the plain restart is
// still exactly {room, seat} (older callers and their tests write it), the fresh restart adds `fresh`, and the model
// is its own action.
func TestSeatControlWritesTheFramesTheRuntimeReads(t *testing.T) {
	sock, requests := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"SetSeatModel"`) {
			return `{"ok":true,"data":{"seat":"seat-1","model":"m"}}` + "\n"
		}
		return `{"ok":true,"data":{"room":"room-a","seat":"seat-1","session_name":"agenthub--room-a--seat-1","runtime":"omp","status":"running"}}` + "\n"
	})
	client := NewClient(sock)

	if _, err := client.RestartSeat("room-a", "seat-1", nil); err != nil {
		t.Fatalf("RestartSeat: %v", err)
	}
	if _, err := client.RestartSeatFresh("room-a", "seat-1", nil); err != nil {
		t.Fatalf("RestartSeatFresh: %v", err)
	}
	if err := client.SetSeatModel("room-a", "seat-1", "deepseek/deepseek-flash"); err != nil {
		t.Fatalf("SetSeatModel: %v", err)
	}

	want := []string{
		`{"action":"RestartSeat","payload":{"room":"room-a","seat":"seat-1"}}`,
		`{"action":"RestartSeat","payload":{"fresh":true,"room":"room-a","seat":"seat-1"}}`,
		`{"action":"SetSeatModel","payload":{"model":"deepseek/deepseek-flash","room":"room-a","seat":"seat-1"}}`,
	}
	for _, frame := range want {
		if got := strings.TrimSpace(<-requests); got != frame {
			t.Errorf("frame = %s, want %s", got, frame)
		}
	}
}
