package clientseat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The R7 compatibility contract for `rig send` on a rig the OpenRig daemon hosts. Measured before this
// change, the verb refused both shapes the promise names: `rig send <session> <text>` answered
// "RIG, SEAT and MESSAGE are required", and its own fallback re-entered that same refusal.
func TestSendWithASessionAndAMessageGoesToTheOpenRigDaemon(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())

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
