package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// THE EXEC IS GONE, AND THIS FILE IS WHERE THAT IS FALSIFIABLE.
//
// The guard's two seams used to shell out: `rig whoami --json` for the identity and `rig send -- TARGET TEXT`
// for the delivery. They are `seatIdentity` and `seatDelivery` now, and they exec nothing: after the cutover
// there is no `rig` to exec —
// the name is a symlink to this client, so the "fallback" would be a self-call — and the identity has a rung
// the exec never had: the seat's own environment.
//
// So every test below arranges the NATIVE sources (the runtime socket, a fake OpenRig daemon, the seat
// environment) and then proves the claim that matters: `rig` was never started. The recording fake rig on
// PATH is the instrument — it writes a file when it runs, and its ABSENCE is the assertion. These tests fail
// on the pre-change code for the plainest reason: it execs the fake rig, so the file exists (or, with PATH
// emptied, the seam returns an error instead of an identity).

// Environment variables the fake rig reads. A test sets them with t.Setenv so the script's execution, if it
// happens, is observable.
const (
	fakeRigArgsVar  = "FAKE_RIG_ARGS"
	fakeRigStdinVar = "FAKE_RIG_STDIN"
)

// fakeRig writes an executable /bin/sh script named rig into a fresh temp dir and puts that dir FIRST on
// PATH, so a shell-out to `rig` would find this script. It records argv one argument per line, drains stdin,
// and exits 0: a test asserts on the recorded file being ABSENT, which is the only honest way to say "no
// process was started".
func fakeRig(t *testing.T) (argsPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake rig is a /bin/sh script")
	}
	dir := t.TempDir()
	argsPath = filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"" + argsPath + "\"\n" +
		"if [ ! -t 0 ]; then cat > \"$FAKE_RIG_STDIN\" 2>/dev/null || true; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "rig"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rig: %v", err)
	}
	t.Setenv(fakeRigArgsVar, argsPath)
	t.Setenv(fakeRigStdinVar, filepath.Join(dir, "stdin"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

// assertRigNeverRan is the instrument's other half: the fake rig records its own execution, so the file not
// existing is the proof that the seam is native.
func assertRigNeverRan(t *testing.T, argsPath string) {
	t.Helper()
	data, err := os.ReadFile(argsPath)
	if err == nil {
		t.Fatalf("`rig` was exec'd with argv %q; the seam must be native", strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"))
	}
	if !os.IsNotExist(err) {
		t.Fatalf("read the fake rig's record: %v", err)
	}
}

// hermeticSeat empties the AGENTHUB_* / OPENRIG_* variables a test does not explicitly want, so a test run
// from inside a seat shell (which is how this repo is worked on) cannot be answered by the shell's own
// environment instead of by the thing under test. It ALSO points the runtime socket at a path nothing can be
// listening on: a test that does not install a fake runtime must never dial this machine's real one.
func hermeticSeat(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AGENTHUB_ROOM", "AGENTHUB_SEAT", "AGENTHUB_ROLE", "AGENTHUB_RUNTIME", "AGENTHUB_SESSION_NAME",
		"OPENRIG_SESSION_NAME", "RIGGED_SESSION_NAME", "OPENRIG_URL", "OPENRIG_PORT", "OPENRIG_HOME",
		"OPENRIG_OCCUPANT_GENERATION", "OPENRIG_TERMINAL_BEARER_TOKEN",
	} {
		t.Setenv(name, "")
	}
	noRuntimeSocket(t)
}

// noRuntimeSocket points the runtime transport at a path nothing can be listening on.
func noRuntimeSocket(t *testing.T) {
	t.Helper()
	t.Setenv("OPENRIG_RUNTIME_SOCKET", filepath.Join(t.TempDir(), "no-runtime.sock"))
}

// fakeRuntimeSocket answers the runtime socket the way the daemon does. It accepts connections for the life
// of the test, IGNORES the reachability probe (dialled and closed with no line), and answers each request
// LINE with handler(line). It returns the socket path and a channel of the lines it saw.
//
// The directory is a SHORT prefix under the system temp dir rather than t.TempDir(): a unix socket path is
// capped near 108 bytes, and t.TempDir() under this repo's own temp root is already most of that.
func fakeRuntimeSocket(t *testing.T, handler func(line string) string) (sock string, requests <-chan string) {
	t.Helper()

	dir, err := os.MkdirTemp("", "scrt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock = filepath.Join(dir, "s")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	lines := make(chan string, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				lines <- string(line)
				_, _ = conn.Write([]byte(handler(string(line))))
			}(conn)
		}
	}()
	t.Setenv("OPENRIG_RUNTIME_SOCKET", sock)
	return sock, lines
}

// awaitRequest reads the frame the seam wrote, so a test can pin it as well as the outcome.
func awaitRequest(t *testing.T, requests <-chan string) string {
	t.Helper()
	select {
	case line := <-requests:
		return strings.TrimSuffix(line, "\n")
	case <-time.After(5 * time.Second):
		t.Fatal("the seam never wrote a request line to the runtime socket")
		return ""
	}
}

// okData wraps a runtime answer document in the frame the runtime replies with.
func okData(data string) string { return `{"ok":true,"data":` + data + "}\n" }

// identityDocument is the identity the runtime answers with: a two-pod rig, so the roster rule (the member is
// the part after the FIRST dot, not the rig name) is exercised rather than assumed.
const identityDocument = `{"resolved_by":"session_name","identity":{"rigName":"room-a","memberId":"go-dev","sessionName":"agenthub--room-a--go-dev","runtime":"omp"},"peers":[{"logicalId":"dev.reviewer","sessionName":"dev-reviewer@room-a"},{"logicalId":"agy.reviewer","sessionName":"agy-reviewer@room-a"},{"logicalId":"dev.lead","sessionName":"dev-lead@room-a"}],"edges":{"outgoing":[],"incoming":[]}}`

// TestIdentityIsResolvedNativelyWithNoRigOnPath IS the acceptance for the identity seam: with a fake `rig`
// first on PATH, no runtime and no daemon, the identity still resolves — from the seat's own environment —
// and the fake rig was never started. Pre-change this failed twice over: the exec ran (or, with PATH emptied,
// the seam answered an error instead of an identity).
func TestIdentityIsResolvedNativelyWithNoRigOnPath(t *testing.T) {
	hermeticSeat(t)
	argsPath := fakeRig(t)
	t.Setenv("AGENTHUB_ROOM", "room-a")
	t.Setenv("AGENTHUB_SEAT", "go-dev")

	who, err := seatIdentity()
	if err != nil {
		t.Fatalf("seatIdentity: %v", err)
	}
	if who.Rig != "room-a" || who.Member != "go-dev" {
		t.Fatalf("identity = rig %q member %q, want the seat the environment names", who.Rig, who.Member)
	}
	assertRigNeverRan(t, argsPath)
}

// TestIdentityComesFromTheRuntimeSocket: the runtime is the PREFERRED source (it is this client's own
// transport), its document is parsed into the guard's identity, and the peers' member names are the part
// after the first dot — two peers share a member name here, so the ambiguity the guard reports later is built
// from the RIGHT sessions.
func TestIdentityComesFromTheRuntimeSocket(t *testing.T) {
	hermeticSeat(t)
	argsPath := fakeRig(t)
	t.Setenv("AGENTHUB_ROOM", "room-a")
	t.Setenv("AGENTHUB_SEAT", "go-dev")

	_, requests := fakeRuntimeSocket(t, func(string) string { return okData(identityDocument) })

	who, err := seatIdentity()
	if err != nil {
		t.Fatalf("seatIdentity: %v", err)
	}
	if who.Rig != "room-a" || who.Member != "go-dev" {
		t.Fatalf("identity = rig %q member %q, want the runtime's own answer", who.Rig, who.Member)
	}
	if got := strings.Join(who.Peers["reviewer"], ","); got != "dev-reviewer@room-a,agy-reviewer@room-a" {
		t.Fatalf("peers[reviewer] = %q, want both pods' sessions of that member", got)
	}
	if got := strings.Join(who.Peers["lead"], ","); got != "dev-lead@room-a" {
		t.Fatalf("peers[lead] = %q, want the one session of that member", got)
	}
	if frame := awaitRequest(t, requests); frame != `{"action":"Whoami","payload":{"session":"agenthub--room-a--go-dev"}}` {
		t.Fatalf("runtime frame = %q, want the Whoami frame for this seat's native session name", frame)
	}
	assertRigNeverRan(t, argsPath)
}

// TestIdentityFallsBackToTheOpenRigDaemon: with no runtime answering, the daemon is still asked — the live
// rig's shape — and its own document is what the guard reads. The fake rig is first on PATH and still not
// started, which is what makes this a fallback to the DAEMON rather than to the CLI.
func TestIdentityFallsBackToTheOpenRigDaemon(t *testing.T) {
	hermeticSeat(t)
	argsPath := fakeRig(t)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")
	t.Setenv("OPENRIG_HOME", t.TempDir())

	var askedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		askedPath = r.URL.Path
		_, _ = w.Write([]byte(`{"resolvedBy":"session_name","identity":{"rigName":"4genthub-min","memberId":"go-dev","sessionName":"4genthub-min-go-dev@4genthub-min"},"peers":[{"logicalId":"4genthub-min.lead","sessionName":"4genthub-min-lead@4genthub-min"}]}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)

	who, err := seatIdentity()
	if err != nil {
		t.Fatalf("seatIdentity: %v", err)
	}
	if askedPath != "/api/whoami" {
		t.Fatalf("asked %q, want the daemon's whoami route", askedPath)
	}
	if who.Rig != "4genthub-min" || who.Member != "go-dev" || strings.Join(who.Peers["lead"], ",") != "4genthub-min-lead@4genthub-min" {
		t.Fatalf("identity = %+v, want the daemon's document", who)
	}
	assertRigNeverRan(t, argsPath)
}

// TestDeliveryGoesToTheRuntimeSocket IS the acceptance for the delivery seam: the message reaches the runtime
// as its own SendText frame, the guard reports success, and `rig` was never started. Pre-change the fake rig
// ran and its argv (send -- TARGET TEXT) was the record — which is exactly what is gone.
func TestDeliveryGoesToTheRuntimeSocket(t *testing.T) {
	hermeticSeat(t)
	argsPath := fakeRig(t)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	_, requests := fakeRuntimeSocket(t, func(string) string {
		return okData(`{"session":"pod-b@r","outcome":"delivered","ok":true}`)
	})

	var stdout, stderr bytes.Buffer
	if code := seatDelivery("pod-b@r", "hello world", &stdout, &stderr); code != 0 {
		t.Fatalf("seatDelivery = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pod-b@r") {
		t.Fatalf("stdout = %q, want the delivery's own line", stdout.String())
	}
	frame := awaitRequest(t, requests)
	var sent struct {
		Action  string `json:"action"`
		Payload struct {
			Session string `json:"session"`
			Text    string `json:"text"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(frame), &sent); err != nil {
		t.Fatalf("the runtime frame is not the SendText document: %v (%q)", err, frame)
	}
	if sent.Action != "SendText" || sent.Payload.Session != "pod-b@r" {
		t.Fatalf("frame = %q, want a SendText to pod-b@r", frame)
	}
	// The text rides the From/To/Sent envelope either transport uses, attributed to THIS seat.
	if !strings.Contains(sent.Payload.Text, "hello world") || !strings.Contains(sent.Payload.Text, "From: 4genthub-min-go-dev@4genthub-min") {
		t.Fatalf("payload text = %q, want the message in this seat's envelope", sent.Payload.Text)
	}
	assertRigNeverRan(t, argsPath)
}

// TestDeliveryFallsBackToTheOpenRigDaemon: with no runtime answering, the message goes to the daemon's own
// transport route — the live rig's shape — and still not through the CLI.
func TestDeliveryFallsBackToTheOpenRigDaemon(t *testing.T) {
	hermeticSeat(t)
	argsPath := fakeRig(t)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")
	t.Setenv("OPENRIG_HOME", t.TempDir())

	var gotPath string
	var gotBody struct {
		Session string `json:"session"`
		Text    string `json:"text"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"ok":true,"sessionName":"pod-b@r","outcome":"delivered"}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)

	var stdout, stderr bytes.Buffer
	if code := seatDelivery("pod-b@r", "hello world", &stdout, &stderr); code != 0 {
		t.Fatalf("seatDelivery = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if gotPath != "/api/transport/send" {
		t.Fatalf("asked %q, want the daemon's send route", gotPath)
	}
	if gotBody.Session != "pod-b@r" || !strings.Contains(gotBody.Text, "hello world") {
		t.Fatalf("body = %+v, want the message addressed to pod-b@r", gotBody)
	}
	assertRigNeverRan(t, argsPath)
}

// TestDeliveryWithNoTransportAtAllIsOneRefusal: with neither transport, the seam refuses ONCE — naming what is
// missing — rather than reporting a shell's exit code. It is a native code (ExitUnavailable, 3) and the guard
// maps it, like every other non-zero delivery, to its own single delivery-failure code.
func TestDeliveryWithNoTransportAtAllIsOneRefusal(t *testing.T) {
	hermeticSeat(t)
	argsPath := fakeRig(t)

	var stdout, stderr bytes.Buffer
	code := seatDelivery("pod-b@r", "hi", &stdout, &stderr)
	if code == 0 {
		t.Fatalf("seatDelivery = 0 with no transport; stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no runtime is answering at") {
		t.Fatalf("stderr = %q, want the refusal to name the runtime", stderr.String())
	}
	if strings.Contains(stderr.String(), "exec") || strings.Contains(stderr.String(), "rig:") {
		t.Fatalf("stderr = %q, want no shell-out in the refusal", stderr.String())
	}
	assertRigNeverRan(t, argsPath)
}
