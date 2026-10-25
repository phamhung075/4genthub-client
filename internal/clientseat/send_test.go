package clientseat

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// A send to a seat that does not exist fails, names the target it could not reach, and never shells out to
// `rig`. PATH is emptied so that a fallback to an external binary would show up as "is not on PATH".
func TestTheRoomSeatMessageShapeNeverShellsOutToRoom(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	noRuntimeSocket(t)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"nosuchpod", "nosuchseat", "probe", "--intent", "notice"}, &out, &errOut)

	if strings.Contains(errOut.String(), "is not on PATH") || strings.Contains(out.String(), "is not on PATH") {
		t.Fatalf("the verb shelled out to `rig`: stdout %q stderr %q", out.String(), errOut.String())
	}
	if code == 0 {
		t.Fatalf("a send to a seat that does not exist exited 0; stdout %q", out.String())
	}
	if !strings.Contains(errOut.String(), "nosuchpod/nosuchseat") {
		t.Errorf("stderr does not name the target it could not reach: %q", errOut.String())
	}
}

// A call with neither shape is a caller error, and it must not reach the runtime to find out.
func TestSendWithTooFewArgumentsIsRefusedBeforeAnyDelivery(t *testing.T) {
	sock, requests := fakeRuntime(t, func(string) string { return `{"ok":true,"data":{}}` + "\n" })
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"only-one-argument"}, &out, &errOut)

	if code != clientcmd.ExitUsage {
		t.Fatalf("exit = %d, want the caller-error code %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "are required") {
		t.Errorf("stderr = %q, want it to name what is missing", errOut.String())
	}
	assertNothingReached(t, requests)
}

// Every option the runtime cannot honour stops the send: the alternative, taking the flag and sending
// anyway, is a silent no-delivery. The flag may stand on either side of the message.
func TestSendRefusesTheFlagsItCannotHonour(t *testing.T) {
	for _, args := range [][]string{
		{"peer@r", "text", "--verify"},
		{"--verify", "peer@r", "text"},
		{"peer@r", "text", "--force"},
		{"peer@r", "text", "--wait-for-idle", "30"},
		{"peer@r", "text", "--dangerously-interact", "--reason", "x"},
		{"peer@r", "text", "--to", "a@r,b@r"},
		{"peer@r", "text", "--pod", "p"},
		{"peer@r", "text", "--room", "r"},
		{"peer@r", "text", "--host", "h"},
		{"peer@r", "text", "--context", "c"},
		{"lab", "dev", "text", "--verify"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			sock, requests := fakeRuntime(t, func(string) string { return `{"ok":true,"data":{}}` + "\n" })
			t.Setenv(clientruntime.RuntimeSocketEnv, sock)

			var out, errOut bytes.Buffer
			code := sendCommand{}.Run(context.Background(), args, &out, &errOut)

			if code != clientcmd.ExitUsage {
				t.Fatalf("exit = %d, want the caller-error code %d", code, clientcmd.ExitUsage)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing reported as sent", out.String())
			}
			if !strings.Contains(errOut.String(), "nothing was sent") {
				t.Errorf("stderr = %q, want it to say nothing was sent", errOut.String())
			}
			assertNothingReached(t, requests)
		})
	}
}

// An option this verb does not know is refused rather than handed on as message text.
func TestSendRefusesAnUnknownFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"peer@r", "text", "--nope"}, &out, &errOut)

	if code != clientcmd.ExitUsage {
		t.Fatalf("exit = %d, want the caller-error code %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "--nope") {
		t.Errorf("stderr = %q, want it to name the flag", errOut.String())
	}
}

func assertNothingReached(t *testing.T, requests <-chan string) {
	t.Helper()
	select {
	case line := <-requests:
		t.Errorf("a refused call reached the runtime: %q", line)
	case <-time.After(100 * time.Millisecond):
	}
}

// The usage names only flags the verb parses. It once printed `[--from NAME]`, which the parser refuses
// ("unknown flag --from for this verb"), so an owner who followed it was refused; the owner's route is
// `4genteam input`, and the help names it.
func TestSendHelpNamesNoFromFlagAndPointsTheOwnerAtInput(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := (sendCommand{}).Run(context.Background(), []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("--help exited %d", code)
	}
	if strings.Contains(out.String(), "--from") {
		t.Errorf("the usage names --from, which the verb refuses:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "4genteam input --room ROOM --seat SEAT") {
		t.Errorf("the usage does not point the owner at `4genteam input`:\n%s", out.String())
	}
	if _, _, err := clientruntime.SplitFlags([]string{"--from", "owner", "r", "s", "m"}, sendFlagSpecs); err == nil {
		t.Errorf("--from now parses; the usage and this test must say what it does")
	}
}
