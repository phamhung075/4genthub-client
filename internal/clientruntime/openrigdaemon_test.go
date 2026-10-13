package clientruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// whoamiDoc is a daemon document shaped like the one GET /api/whoami answers with, trimmed to the
// fields these tests read.
const whoamiDoc = `{"resolvedBy":"session_name","identity":{"rigName":"4genthub-min","logicalId":"4genthub-min.go-dev","rack":"v1","podNamespace":"4genthub-min","podId":"01M4V95400D8PC5WNZ2YJG2GNN","memberId":"go-dev","sessionName":"4genthub-min-go-dev@4genthub-min","runtime":"omp","attachmentType":"tmux"},"peers":[],"edges":{"outgoing":[],"incoming":[]}}`

// TestWhoamiAsksTheOpenRigDaemonAndPrintsItsDocument is the R7 contract for `rig whoami` on a rig the
// OpenRig daemon hosts: the seat's environment is enough, the daemon is asked for the named session, its
// identity rides the sender header, and --json prints the daemon's own document.
func TestWhoamiAsksTheOpenRigDaemonAndPrintsItsDocument(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir()) // no terminal-token: exercises the no-bearer path

	var gotPath, gotSession, gotSender string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotSession, gotSender = r.URL.Path, r.URL.Query().Get("sessionName"), r.Header.Get(senderHeader)
		_, _ = w.Write([]byte(whoamiDoc))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("whoami --json exit = %d, stderr = %q", code, errOut.String())
	}
	if gotPath != "/api/whoami" {
		t.Errorf("asked %q, want /api/whoami", gotPath)
	}
	if gotSession != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("sessionName = %q, want the seat's OPENRIG_SESSION_NAME", gotSession)
	}
	if gotSender != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("%s = %q, want the seat's OPENRIG_SESSION_NAME", senderHeader, gotSender)
	}

	// The document is the daemon's, not a re-rendering: every field it carried is still readable.
	var printed struct {
		ResolvedBy string `json:"resolvedBy"`
		Identity   struct {
			RigName   string `json:"rigName"`
			LogicalID string `json:"logicalId"`
			Runtime   string `json:"runtime"`
		} `json:"identity"`
		Edges map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not the daemon's JSON document: %v (stdout=%q)", err, out.String())
	}
	if printed.Identity.RigName != "4genthub-min" || printed.Identity.LogicalID != "4genthub-min.go-dev" || printed.Identity.Runtime != "omp" {
		t.Errorf("identity printed as %+v, want the daemon's own fields", printed.Identity)
	}
	if printed.ResolvedBy != "session_name" {
		t.Errorf("resolvedBy = %q, want the daemon's own value", printed.ResolvedBy)
	}
	if printed.Edges == nil {
		t.Error("the printed document dropped the daemon's edges")
	}
}

// TestWhoamiWithoutJsonNamesTheSeatAndPod covers the bare verb, which a seat runs by hand.
func TestWhoamiWithoutJsonNamesTheSeatAndPod(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(whoamiDoc))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	if code := (whoamiCommand{}).Run(context.Background(), nil, nil, &out, &errOut); code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	for _, want := range []string{"Rig:        4genthub-min\n", "Logical ID: 4genthub-min.go-dev\n", "Session:    4genthub-min-go-dev@4genthub-min\n", "Resolved:   via session name\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("bare whoami printed %q, want it to contain %q", out.String(), want)
		}
	}
}

// TestWhoamiReportsTheDaemonsRefusalAsUnavailable keeps a wrong session name from looking like a seat.
func TestWhoamiReportsTheDaemonsRefusalAsUnavailable(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Session 'nope@rig' not found in any managed rig."}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "nope@rig")

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)

	if code != 3 {
		t.Fatalf("exit = %d, want the platform-matrix refusal 3", code)
	}
	if out.Len() != 0 {
		t.Errorf("a refused whoami printed a document anyway: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "not found in any managed rig") {
		t.Errorf("stderr = %q, want the daemon's own reason", errOut.String())
	}
}

// TestSendMessagePostsTheOpenRigEnvelopeToTheDaemon is the R7 contract for `rig send <session> <text>`:
// the text arrives in the pane wrapped in the CLI's From/To/Sent envelope, attributed to this shell's
// seat, and addressed to the session the caller named.
func TestSendMessagePostsTheOpenRigEnvelopeToTheDaemon(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())

	var gotPath string
	var got map[string]string
	var gotSender, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotSender, gotAuth = r.URL.Path, r.Header.Get(senderHeader), r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")
	t.Setenv(openRigGenerationEnv, "17ec5cb7-771b-4dd6-8fd0-b59dbde19a46")

	err := OpenRigSendMessage(context.Background(), "4genthub-min-fe-dev@4genthub-min", "no action needed")
	if err != nil {
		t.Fatalf("OpenRigSendMessage: %v", err)
	}
	if gotPath != "/api/transport/send" {
		t.Errorf("posted to %q, want /api/transport/send", gotPath)
	}
	if got["session"] != "4genthub-min-fe-dev@4genthub-min" {
		t.Errorf("session = %q, want the caller's target", got["session"])
	}
	if gotSender != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("%s = %q, want this shell's OPENRIG_SESSION_NAME", senderHeader, gotSender)
	}
	if gotAuth != "" {
		t.Errorf("sent Authorization %q although no token exists on this machine", gotAuth)
	}

	lines := strings.Split(got["text"], "\n")
	if len(lines) != 7 {
		t.Fatalf("envelope has %d lines, want 7: %q", len(lines), got["text"])
	}
	if lines[0] != "From: 4genthub-min-go-dev@4genthub-min" {
		t.Errorf("line 1 = %q, want the sender's From: line", lines[0])
	}
	if lines[1] != "To: 4genthub-min-fe-dev@4genthub-min" {
		t.Errorf("line 2 = %q, want the recipient's To: line", lines[1])
	}
	if !regexp.MustCompile(`^Sent: \d\d-\d\d \d\d:\d\dZ · gen 17ec5cb7$`).MatchString(lines[2]) {
		t.Errorf("line 3 = %q, want `Sent: MM-DD HH:MMZ · gen <first8>`", lines[2])
	}
	if lines[3] != "---" || lines[5] != "---" || lines[4] != "no action needed" {
		t.Errorf("body block = %q, want the text between two --- rules", strings.Join(lines[3:6], "|"))
	}
	if lines[6] != `↩ Reply: rig send 4genthub-min-go-dev@4genthub-min "..."` {
		t.Errorf("line 7 = %q, want the reply hint addressed back to the sender", lines[6])
	}
}

// TestSendMessageReportsTheDaemonsRefusal: an unknown session is the daemon's call, and its reason is
// what the caller sees.
func TestSendMessageReportsTheDaemonsRefusal(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":"Session 'ghost@rig' not found. Check session names with: rig ps --nodes"}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	err := OpenRigSendMessage(context.Background(), "ghost@rig", "hello")
	if err == nil {
		t.Fatal("an unknown session was reported as delivered")
	}
	if !strings.Contains(err.Error(), "Check session names with: rig ps --nodes") {
		t.Errorf("error = %q, want the daemon's own reason", err.Error())
	}
}

// TestSendBodyIsTheOpenRigCLIsProse pins the envelope byte for byte, including the two spellings a
// sender label can produce and the generation suffix's absence.
func TestSendBodyIsTheOpenRigCLIsProse(t *testing.T) {
	stamp := time.Date(2026, 10, 13, 18, 50, 0, 281000000, time.UTC)
	got := openRigSendBody("4genthub-min-lead@4genthub-min", "4genthub-min-go-dev@4genthub-min", "probe", stamp, "17ec5cb7-771b-4dd6-8fd0-b59dbde19a46")
	want := strings.Join([]string{
		"From: 4genthub-min-lead@4genthub-min",
		"To: 4genthub-min-go-dev@4genthub-min",
		"Sent: 10-13 18:50Z · gen 17ec5cb7",
		"---",
		"probe",
		"---",
		`↩ Reply: rig send 4genthub-min-lead@4genthub-min "..."`,
	}, "\n")
	if got != want {
		t.Errorf("envelope =\n%s\nwant\n%s", got, want)
	}

	noGen := openRigSendBody("lead@rig", "go-dev@rig", "x", stamp, "")
	if strings.Contains(noGen, "gen") {
		t.Errorf("a send with no occupant generation still rendered one: %q", noGen)
	}

	unknown := openRigSendBody("  ", "go-dev@rig", "x", time.Time{}, "")
	if !strings.Contains(unknown, "From: "+senderFallback) {
		t.Errorf("an env-less send was not labelled %q: %q", senderFallback, unknown)
	}
	if strings.Contains(unknown, "Sent:") {
		t.Errorf("a send with no stamp rendered a Sent: line: %q", unknown)
	}

	external := openRigSendBody("human@external", "go-dev@rig", "x", stamp, "")
	if !strings.Contains(external, `↩ Reply if needed: rig queue create --destination human@external`) {
		t.Errorf("an @external sender got the seat reply hint: %q", external)
	}
}

// TestDaemonURLPrecedence: OPENRIG_URL wins, OPENRIG_PORT is the loopback fallback, and neither means
// this shell names no daemon (the native path stays in charge).
func TestDaemonURLPrecedence(t *testing.T) {
	t.Setenv("OPENRIG_URL", "")
	t.Setenv("OPENRIG_PORT", "")
	if openRigDaemonConfigured() {
		t.Error("no OPENRIG_URL and no OPENRIG_PORT, yet a daemon was reported configured")
	}

	t.Setenv("OPENRIG_PORT", "7433")
	if got := openRigDaemonURL(); got != "http://127.0.0.1:7433" {
		t.Errorf("port-only daemon URL = %q, want the loopback address", got)
	}

	t.Setenv("OPENRIG_URL", "http://127.0.0.1:9001/")
	if got := openRigDaemonURL(); got != "http://127.0.0.1:9001" {
		t.Errorf("OPENRIG_URL = %q, want it to win and lose its trailing slash", got)
	}
}

// TestTerminalTokenPreference: the environment's bearer wins, else the home file, else no header at all.
func TestTerminalTokenPreference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPENRIG_HOME", home)
	if got := openRigTerminalToken(); got != "" {
		t.Errorf("token = %q, want none when the home carries no terminal-token", got)
	}

	if err := os.WriteFile(filepath.Join(home, "terminal-token"), []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("writing terminal-token: %v", err)
	}
	if got := openRigTerminalToken(); got != "from-file" {
		t.Errorf("token = %q, want the file's trimmed value", got)
	}

	t.Setenv(openRigBearerEnv, "from-env")
	if got := openRigTerminalToken(); got != "from-env" {
		t.Errorf("token = %q, want OPENRIG_TERMINAL_BEARER_TOKEN to win", got)
	}
}
