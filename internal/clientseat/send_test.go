package clientseat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// The R7 compatibility contract for `rig send` on a rig the OpenRig daemon hosts. Measured before this
// change, the verb refused both shapes the promise names: `rig send <session> <text>` answered
// "RIG, SEAT and MESSAGE are required", and its own fallback re-entered that same refusal.
//
// THE SELF-CALL, PINNED AS A REGRESSION: the RIG SEAT MESSAGE shape used to fall back to
// `exec.Command(exec.LookPath("rig"), "send", ...)`, and `rig` on this machine is a SYMLINK TO THIS BINARY —
// so the "fallback" handed the two-argument OpenRig form to itself, its own validation refused it, and the
// user read a failure report about a command nobody asked for.
//
// PATH is emptied here, which is what makes this a test of the ABSENCE rather than of the message: with the
// fallback in place the verb answers "rig is not on PATH" (`exec.LookPath` failing), and without it the
// failure can only come from the runtime, named with the target it could not reach.
func TestTheRigSeatMessageShapeNeverFallsBackToShellingOutToRig(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil,
		[]string{"nosuchpod", "nosuchseat", "probe"}, &out, &errOut)

	if strings.Contains(errOut.String(), "is not on PATH") || strings.Contains(out.String(), "is not on PATH") {
		t.Fatalf("the verb fell back to shelling out to `rig`: stdout %q stderr %q", out.String(), errOut.String())
	}
	if code == 0 {
		t.Fatalf("a send to a seat that does not exist exited 0; stdout %q", out.String())
	}
	// The failure must name the target it could not reach, so a reader can tell a refused send from one that
	// never happened.
	if !strings.Contains(errOut.String(), "nosuchpod/nosuchseat") {
		t.Errorf("stderr does not name the target it could not reach: %q", errOut.String())
	}
}

func TestSendWithASessionAndAMessageGoesToTheOpenRigDaemon(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the DAEMON path is the fallback contract under test here

	var gotPath, gotSender string
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotSender = r.URL.Path, r.Header.Get("X-OpenRig-Session")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil,
		[]string{"4genthub-min-fe-dev@4genthub-min", "probe"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("send exit = %d, stderr = %q", code, errOut.String())
	}
	if gotPath != "/api/transport/send" {
		t.Errorf("posted to %q, want /api/transport/send", gotPath)
	}
	if got["session"] != "4genthub-min-fe-dev@4genthub-min" {
		t.Errorf("delivered to session %q, want the one the caller named", got["session"])
	}
	if gotSender != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("X-OpenRig-Session = %q, want this shell's seat", gotSender)
	}
	if !strings.Contains(got["text"], "probe") || !strings.HasPrefix(got["text"], "From: 4genthub-min-go-dev@4genthub-min\nTo: 4genthub-min-fe-dev@4genthub-min\n") {
		t.Errorf("delivered text = %q, want the envelope addressed to the target", got["text"])
	}
	if out.String() != "Sent to 4genthub-min-fe-dev@4genthub-min\n" {
		t.Errorf("stdout = %q, want the delivery confirmed by session name", out.String())
	}
}

// The daemon refuses an unknown session, and that reason is the caller's answer — a typo must not look
// like a delivery.
func TestSendReportsTheDaemonsRefusalAndExitsNonZero(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the DAEMON's refusal is the answer under test here
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":"Session 'ghost@r' not found. Check session names with: rig ps --nodes"}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"ghost@r", "probe"}, &out, &errOut)

	if code == 0 {
		t.Fatalf("an unknown session exited 0; stdout = %q", out.String())
	}
	if code != clientcmd.ExitRemote {
		t.Errorf("exit = %d, want %d — the CLI's code for a refused send", code, clientcmd.ExitRemote)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing reported as sent", out.String())
	}
	if !strings.Contains(errOut.String(), "Check session names with: rig ps --nodes") {
		t.Errorf("stderr = %q, want the daemon's own reason", errOut.String())
	}
}

// A call with neither shape is still a caller error, and it must not reach the network to find out.
func TestSendWithTooFewArgumentsIsRefusedBeforeAnyDelivery(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an incomplete call reached the daemon: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"only-one-argument"}, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d, want the caller-error code 2", code)
	}
	if !strings.Contains(errOut.String(), "are required") {
		t.Errorf("stderr = %q, want it to name what is missing", errOut.String())
	}
}

// TestSendTakesTheDocumentedFlagsOnEitherSideOfTheMessage is gate finding M1's own measurement, run
// through the verb: `rig send <session> 'text' --verify` is the form the daemon's whoami prints as
// commands.sendExamples, and the shim used to read it as rig=<session>, seat="text", message="--verify"
// and deliver nothing to the named seat (Go's flag package stops at the first positional).
func TestSendTakesTheDocumentedFlagsOnEitherSideOfTheMessage(t *testing.T) {
	const target = "4genthub-min-reviewer@4genthub-min"

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"the daemon's own trailing form", []string{target, "GATE PROBE: verify is parsed", "--verify"}},
		{"leading", []string{"--verify", target, "GATE PROBE: verify is parsed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENRIG_HOME", t.TempDir())
			var got map[string]any
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				// The daemon is asked for its seat records first — the canonical name the caller typed is
				// resolved, never composed — and what it names is what the send is addressed to.
				switch r.URL.Path {
				case "/api/ps":
					_, _ = w.Write([]byte(`[{"rigId":"rig-min","rigName":"4genthub-min","name":"4genthub-min"}]`))
				case "/api/rigs/rig-min/nodes":
					_, _ = w.Write([]byte(`[{"logicalId":"4genthub-min.reviewer","canonicalSessionName":"4genthub-min-reviewer@4genthub-min"}]`))
				default:
					_ = json.NewDecoder(r.Body).Decode(&got)
					_, _ = w.Write([]byte(`{"ok":true,"outcome":"delivered","verified":true}`))
				}
			}))
			defer srv.Close()
			t.Setenv("OPENRIG_URL", srv.URL)
			t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

			var out, errOut bytes.Buffer
			code := sendCommand{}.Run(context.Background(), nil, tc.args, &out, &errOut)

			if code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
			}
			if calls != 3 {
				t.Fatalf("%d requests reached the daemon, want three: its rig list, its seat record, and the send", calls)
			}
			if got["session"] != target {
				t.Errorf("delivered to %v, want the named seat %s", got["session"], target)
			}
			if got["verify"] != true {
				t.Errorf("verify = %v, want the flag carried through, not consumed", got["verify"])
			}
			text, _ := got["text"].(string)
			if !strings.Contains(text, "GATE PROBE: verify is parsed") || strings.Contains(text, "--verify") {
				t.Errorf("delivered text = %q, want the message with no flag inside it", text)
			}
		})
	}
}

// TestSendSurfacesTheDaemonsAdvisory is gate finding M2: the readiness guard sends-with-advisory, so that
// warning is the only signal a seat gets that its message went into a pane that was not reading. The CLI
// prints it; the shim dropped it.
func TestSendSurfacesTheDaemonsAdvisory(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the DAEMON's advisory is the signal under test here
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"sessionName":"4genthub-min-reviewer@4genthub-min","warning":"target pane appears mid-task; sent anyway (busy is advisory, not a block)."}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"4genthub-min-reviewer@4genthub-min", "probe"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	want := "Sent to 4genthub-min-reviewer@4genthub-min\nAdvisory: target pane appears mid-task; sent anyway (busy is advisory, not a block).\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want the advisory the daemon sent", out.String())
	}
}

// TestSendRefusesTheFlagsItCannotHonour: --to/--pod/--rig/--host/--context address a surface this local
// shim does not have, so they STOP the send. The alternative — taking the flag and sending anyway — is
// the silent no-delivery the gate was about.
func TestSendRefusesTheFlagsItCannotHonour(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Errorf("a refused flag still reached the daemon: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil,
		[]string{"4genthub-min-fe-dev@4genthub-min", "text", "--to", "a@r,b@r"}, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d, want the caller-error code 2", code)
	}
	if calls != 0 {
		t.Errorf("%d requests reached the daemon, want none", calls)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing reported as sent", out.String())
	}
	if !strings.Contains(errOut.String(), "--to") || !strings.Contains(errOut.String(), "nothing was sent") {
		t.Errorf("stderr = %q, want it to name the flag and say nothing was sent", errOut.String())
	}
}

// TestSendRefusesAnUnknownFlag: an option this verb does not know is refused rather than handed on as
// message text.
func TestSendRefusesAnUnknownFlag(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("OPENRIG_URL", "http://127.0.0.1:1")
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "text", "--nope"}, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d, want the caller-error code 2", code)
	}
	if !strings.Contains(errOut.String(), "--nope") {
		t.Errorf("stderr = %q, want it to name the flag", errOut.String())
	}
}

// TestSendVerifyPrintsTheDaemonsVerdictAndNamesTheProbeItDoesNotRun: --verify carries into the request,
// and the verdict printed is the daemon's own re-confirmation — with the client-side consumption probe
// named as not run, in the CLI's own words, rather than a bare `Verified: yes`.
func TestSendVerifyPrintsTheDaemonsVerdictAndNamesTheProbeItDoesNotRun(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"sessionName":"peer@r","outcome":"delivered","verified":true}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "probe", "--verify"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got["verify"] != true {
		t.Errorf("verify = %v, want it carried into the request", got["verify"])
	}
	for _, want := range []string{
		"Sent to peer@r\n",
		"Verified: yes\n",
		"Delivery: delivered (message landed; render confirmed)\n",
		"Note: the pane-effect check could not run (",
		"the verdict above is transport-level only.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", out.String(), want)
		}
	}
}

// TestSendRetainedIsNotADelivery: a `retained` outcome is the daemon saying it did NOT deliver, and the
// CLI gives it its own line. Reporting it as `Sent to ...` would be the exact dishonesty this row is
// about.
func TestSendRetainedIsNotADelivery(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the DAEMON's retained outcome is the answer under test here
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"outcome":"retained","warning":"delivery quota full"}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "probe"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if out.String() != "Retained, not delivered to peer@r. delivery quota full\n" {
		t.Errorf("stdout = %q, want the CLI's retained line", out.String())
	}
}

// TestSendWaitForIdleReachesTheRouteInMilliseconds, and a non-positive value stops before the network.
func TestSendWaitForIdleReachesTheRouteInMilliseconds(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "probe", "--wait-for-idle", "30"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got["waitForIdleMs"] != float64(30000) {
		t.Errorf("waitForIdleMs = %v, want 30000 for --wait-for-idle 30", got["waitForIdleMs"])
	}

	// A non-positive value is the caller's error and never reaches the network.
	calls := 0
	unreached := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer unreached.Close()
	t.Setenv("OPENRIG_URL", unreached.URL)
	code = sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "probe", "--wait-for-idle", "0"}, &out, &errOut)
	if code != 2 {
		t.Errorf("exit = %d, want the caller-error code 2 for --wait-for-idle 0", code)
	}
	if calls != 0 {
		t.Errorf("%d requests reached the daemon, want none", calls)
	}
}

// TestSendJsonPrintsTheDaemonsAnswer: --json is the daemon's own document, not a re-encode of it.
func TestSendJsonPrintsTheDaemonsAnswer(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // --json here is the DAEMON's answer; the runtime has its own test
	const answer = `{"ok":true,"sessionName":"peer@r","outcome":"delivered","warning":"busy"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "probe", "--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if out.String() != answer+"\n" {
		t.Errorf("stdout = %q, want the daemon's answer verbatim", out.String())
	}
}
