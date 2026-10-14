package clientruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	noRuntimeSocket(t)                    // the DAEMON path is the contract under test here

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
	noRuntimeSocket(t) // the DAEMON path is the contract under test here
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

// TestWhoamiReportsTheDaemonsRefusalAsTheCLIsExitCode: an unknown session is the daemon's 404, and the
// CLI ends on 1 for it (whoami.ts: `if (res.status >= 400) process.exitCode = 1`) — not on this package's
// platform-matrix refusal, which is now reserved for a daemon that cannot be reached at all.
func TestWhoamiReportsTheDaemonsRefusalAsTheCLIsExitCode(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the DAEMON's refusal is the answer under test here
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Session 'nope@rig' not found in any managed rig."}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "nope@rig")

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)

	if code != 1 {
		t.Fatalf("exit = %d, want the CLI's 1 for a refused whoami", code)
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

	result, status, err := OpenRigSend(context.Background(), "4genthub-min-fe-dev@4genthub-min", "no action needed", SendOptions{})
	if err != nil {
		t.Fatalf("OpenRigSend: %v", err)
	}
	if status != http.StatusOK || !result.OK {
		t.Fatalf("status = %d, ok = %v, want a readable 200", status, result.OK)
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
	if lines[6] != `↩ Reply: 4genteam send 4genthub-min-go-dev@4genthub-min "..."` {
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

	result, status, err := OpenRigSend(context.Background(), "ghost@rig", "hello", SendOptions{})
	if err != nil {
		t.Fatalf("a 404 is the daemon answering, not a transport failure: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want the daemon's 404", status)
	}
	if result.OK {
		t.Error("an unknown session was read as delivered")
	}
	if !strings.Contains(result.Error, "Check session names with: rig ps --nodes") {
		t.Errorf("error = %q, want the daemon's own reason", result.Error)
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
		`↩ Reply: 4genteam send 4genthub-min-lead@4genthub-min "..."`,
	}, "\n")
	if got != want {
		t.Errorf("envelope =\n%s\nwant\n%s", got, want)
	}

	noGen := openRigSendBody("lead@rig", "go-dev@rig", "x", stamp, "")
	if strings.Contains(noGen, "· gen ") {
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
	if !strings.Contains(external, "no local command delivers a reply to it") {
		t.Errorf("an @external sender was not told the local queue cannot reach it: %q", external)
	}
	if strings.Contains(external, "rig queue create") || strings.Contains(external, "Reply if needed") {
		t.Errorf("the @external footer names a command this box refuses, instead of the limit: %q", external)
	}

	// Gate finding m4: the label is rendered EXACTLY as it arrived — the CLI's twin is
	// `sender.trim().length > 0 ? sender : SENDER_FALLBACK`, so only the blank TEST is on the trimmed
	// value, which is why the fallback line above and this one differ.
	padded := openRigSendBody("  padded@rig  ", "go-dev@rig", "x", stamp, "")
	if !strings.Contains(padded, "From:   padded@rig  \n") {
		t.Errorf("a padded sender label was trimmed: %q", padded)
	}

	// Gate finding m3: the generation is rendered by PRESENCE and truncated to eight characters, so a
	// short one is rendered rather than dropped.
	short := openRigSendBody("lead@rig", "go-dev@rig", "x", stamp, "abc")
	if !strings.Contains(short, "Sent: 10-13 18:50Z · gen abc") {
		t.Errorf("a generation shorter than eight characters was dropped: %q", short)
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

// TestSplitFlagsTakesAnOptionFromEitherSideOfThePositionals is the root of gate finding M1: Go's flag
// package stops at the first positional, so `rig send <session> 'text' --verify` — the form the daemon's
// own whoami prints as commands.sendExamples — parsed as rig=<session>, seat="text", message="--verify"
// and delivered nothing to the named seat.
func TestSplitFlagsTakesAnOptionFromEitherSideOfThePositionals(t *testing.T) {
	specs := []FlagSpec{{Name: "verify"}, {Name: "reason", Value: true}}

	cases := []struct {
		name    string
		args    []string
		want    map[string]string
		wantPos []string
	}{
		{"trailing", []string{"seat@rig", "text", "--verify"}, map[string]string{"verify": "true"}, []string{"seat@rig", "text"}},
		{"leading", []string{"--verify", "seat@rig", "text"}, map[string]string{"verify": "true"}, []string{"seat@rig", "text"}},
		{"between the positionals", []string{"seat@rig", "--verify", "text"}, map[string]string{"verify": "true"}, []string{"seat@rig", "text"}},
		{"value after", []string{"seat@rig", "text", "--reason", "why"}, map[string]string{"reason": "why"}, []string{"seat@rig", "text"}},
		{"value joined with =", []string{"seat@rig", "text", "--reason=why"}, map[string]string{"reason": "why"}, []string{"seat@rig", "text"}},
		{"terminator", []string{"seat@rig", "--", "--verify"}, map[string]string{}, []string{"seat@rig", "--verify"}},
		{"a lone dash is positional", []string{"-", "--verify"}, map[string]string{"verify": "true"}, []string{"-"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, positional, err := SplitFlags(tc.args, specs)
			if err != nil {
				t.Fatalf("SplitFlags(%q): %v", tc.args, err)
			}
			if !reflect.DeepEqual(positional, tc.wantPos) {
				t.Errorf("positionals = %q, want %q", positional, tc.wantPos)
			}
			if !reflect.DeepEqual(opts, tc.want) {
				t.Errorf("options = %v, want %v", opts, tc.want)
			}
		})
	}

	// An option this verb does not declare is REFUSED, never handed on as text — the whole reason this
	// helper exists rather than the flag package.
	if _, _, err := SplitFlags([]string{"seat@rig", "text", "--nope"}, specs); err == nil {
		t.Error("an undeclared flag was accepted as a positional")
	}
	if _, _, err := SplitFlags([]string{"seat@rig", "--reason"}, specs); err == nil {
		t.Error("a value-taking flag with no value was accepted")
	}
	if _, _, err := SplitFlags([]string{"--verify=yes", "x"}, specs); err == nil {
		t.Error("a value was accepted for a flag that takes none")
	}
}

// TestOpenRigSendCarriesTheOptionedFlags: every flag the verb implements reaches the route under the name
// the route reads (transport.js's body keys), and --raw is the one that changes what is delivered.
func TestOpenRigSendCarriesTheOptionedFlags(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	_, status, err := OpenRigSend(context.Background(), "peer@rig", "text", SendOptions{Verify: true, Force: true, WaitForIdleMs: 30000})
	if err != nil || status != http.StatusOK {
		t.Fatalf("OpenRigSend: status %d, err %v", status, err)
	}
	if got["verify"] != true || got["force"] != true || got["waitForIdleMs"] != float64(30000) {
		t.Errorf("body = %v, want verify/force/waitForIdleMs as the route names them", got)
	}
	if text, _ := got["text"].(string); !strings.HasPrefix(text, "From: 4genthub-min-go-dev@4genthub-min\n") {
		t.Errorf("an optioned send lost its envelope: %q", text)
	}
	if _, present := got["dangerouslyInteract"]; present {
		t.Error("dangerouslyInteract was sent although the caller did not ask for it")
	}

	_, _, err = OpenRigSend(context.Background(), "peer@rig", "text", SendOptions{Raw: true})
	if err != nil {
		t.Fatalf("OpenRigSend raw: %v", err)
	}
	if got["text"] != "text" {
		t.Errorf("--raw delivered %q, want the text exactly", got["text"])
	}

	_, _, err = OpenRigSend(context.Background(), "peer@rig", "text", SendOptions{DangerouslyInteract: true, Reason: "why", Raw: true})
	if err != nil {
		t.Fatalf("OpenRigSend interact: %v", err)
	}
	if got["dangerouslyInteract"] != true || got["reason"] != "why" {
		t.Errorf("body = %v, want the interactive override with its audit reason", got)
	}
}

// whoamiCompactableDoc carries a field inside the CLI's compact allowlist AND three outside it
// (attachmentType, contextUsage, commands); one peer carries a label that the projection drops.
const whoamiCompactableDoc = `{"resolvedBy":"session_name","identity":{"rigName":"r","logicalId":"r.go-dev","podId":"p","podNamespace":"ns","memberId":"go-dev","sessionName":"r-go-dev@r","runtime":"omp","attachmentType":"tmux"},"peers":[{"logicalId":"r.lead","sessionName":"r-lead@r","runtime":"omp","label":"lead"}],"contextUsage":{"tokens":1},"commands":{"captureExamples":["rig capture r-lead@r"]}}`

// TestWhoamiAsksForTheCompactProjectionAndPrintsTheAllowlist covers gate findings m1 and the --json nit:
// the CLI sends compact=1 by default and projects the answer to its identity-recovery allowlist, and
// --full prints the daemon's document as it arrived rather than a re-encoding of it.
func TestWhoamiAsksForTheCompactProjectionAndPrintsTheAllowlist(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the DAEMON's compact projection is the contract under test here
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		_, _ = w.Write([]byte(whoamiCompactableDoc))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "r-go-dev@r")

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if len(queries) != 1 || !strings.Contains(queries[0], "compact=1") {
		t.Errorf("first query = %v, want compact=1 by default", queries)
	}
	doc := out.String()
	for _, kept := range []string{`"sessionName":"r-go-dev@r"`, `"logicalId":"r.lead"`} {
		if !strings.Contains(doc, kept) {
			t.Errorf("the projection dropped %s: %s", kept, doc)
		}
	}
	for _, dropped := range []string{"contextUsage", "commands", "attachmentType", `"label"`} {
		if strings.Contains(doc, dropped) {
			t.Errorf("the projection kept %s, which is outside the allowlist: %s", dropped, doc)
		}
	}

	out.Reset()
	errOut.Reset()
	code = whoamiCommand{}.Run(context.Background(), nil, []string{"--json", "--full"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("--full exit = %d, stderr = %q", code, errOut.String())
	}
	if len(queries) != 2 || strings.Contains(queries[1], "compact") {
		t.Errorf("--full asked for the compact document: %v", queries)
	}
	if out.String() != whoamiCompactableDoc+"\n" {
		t.Errorf("--full printed %q, want the daemon's own bytes", out.String())
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

// TestQueueCommandReadsDaemonRows is the R7 contract for `rig queue`: the verb asks the daemon for THIS
// seat's active queue (the compact shape it advertises) and prints one line per real row rather than a
// canned empty list.
func TestQueueCommandReadsDaemonRows(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())

	noRuntimeSocket(t) // no runtime here: this test exercises the daemon fallback
	var gotPath, gotDest, gotActive, gotCompact string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDest = r.URL.Query().Get("destinationSession")
		gotActive = r.URL.Query().Get("activeOnly")
		gotCompact = r.URL.Query().Get("compact")
		_, _ = w.Write([]byte(`[{"qitemId":"qitem-aaa","state":"pending","sourceSession":"4genthub-min-lead@4genthub-min","summary":"first owed task"},{"qitemId":"qitem-bbb","state":"claimed","sourceSession":"4genthub-min-fe-dev@4genthub-min","summary":"second owed task"}]`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("queue exit = %d, stderr = %q", code, errOut.String())
	}
	if gotPath != "/api/queue/list" {
		t.Errorf("asked %q, want /api/queue/list", gotPath)
	}
	if gotDest != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("destinationSession = %q, want this seat's session", gotDest)
	}
	if gotActive != "1" || gotCompact != "1" {
		t.Errorf("activeOnly=%q compact=%q, want both 1", gotActive, gotCompact)
	}
	for _, want := range []string{"qitem-aaa", "qitem-bbb", "pending", "claimed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("queue printed %q, want it to carry %q", out.String(), want)
		}
	}
}

// TestQueueCommandMeasuresAnEmptyQueue: an empty document is reported as a measurement of THIS session
// against THIS daemon, not as the old false sentence.
func TestQueueCommandMeasuresAnEmptyQueue(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // no runtime here: the empty document must come from the daemon
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("queue exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "4genthub-min-go-dev@4genthub-min") {
		t.Errorf("an empty queue did not name the session it measured: %q", out.String())
	}
	if !strings.Contains(out.String(), "0") {
		t.Errorf("an empty queue did not state a count of zero: %q", out.String())
	}
	if !strings.Contains(out.String(), srv.URL) {
		t.Errorf("an empty queue did not name the daemon it measured: %q", out.String())
	}
	if strings.Contains(out.String(), "No pending items in local queue") {
		t.Errorf("the removed false sentence came back: %q", out.String())
	}
}

// TestQueueCommandWithoutADaemonIsNotAnEmptyQueue: a shell that names no daemon gets a refusal naming
// the missing configuration, never a queue-looking empty answer.
func TestQueueCommandWithoutADaemonIsNotAnEmptyQueue(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // neither transport: this is the refusal, not a fallback
	t.Setenv("OPENRIG_URL", "")
	t.Setenv("OPENRIG_PORT", "")
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code == 0 {
		t.Fatalf("a daemonless queue exited 0, stdout = %q", out.String())
	}
	for _, want := range []string{"OPENRIG_URL", "OPENRIG_PORT"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to name the missing %s", errOut.String(), want)
		}
	}
	if strings.Contains(out.String(), "[]") {
		t.Errorf("a daemonless queue printed %q on stdout", out.String())
	}
	if strings.Contains(out.String(), "Measured 0") || strings.Contains(out.String(), "No pending items") {
		t.Errorf("a daemonless queue claimed an empty queue: %q", out.String())
	}
}

// TestQueueCommandSurfacesADaemonFailure: a daemon that refuses is reported, and never flattened to [].
func TestQueueCommandSurfacesADaemonFailure(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the daemon is the transport under test here
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"queue store unavailable"}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code == 0 {
		t.Fatalf("a failing daemon exited 0, stdout = %q", out.String())
	}
	if strings.Contains(out.String(), "[]") {
		t.Errorf("a failing daemon printed %q on stdout", out.String())
	}
	if !strings.Contains(errOut.String(), "500") || !strings.Contains(errOut.String(), "queue store unavailable") {
		t.Errorf("stderr = %q, want the daemon's HTTP status and reason", errOut.String())
	}
}

// TestQueueTransitionsPrintsTheNotesTheRowDoesNotCarry is the measured half of the queue defect: a note
// `queue update --note` accepts is written to the row's TRANSITION LOG and the row document carries no
// note field at all, so the shim must read the log or a caller can never see what was recorded.
func TestQueueTransitionsPrintsTheNotesTheRowDoesNotCarry(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the daemon's transition route is the transport under test here
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`[{"transitionId":9129,"qitemId":"qitem-aaa","ts":"2026-10-13T19:59:43.804Z","state":"done","transitionNote":"custody-verified: the successor holds the work","actorSession":"4genthub-min-go-dev@4genthub-min"}]`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, []string{"transitions", "qitem-aaa"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("queue transitions exit = %d, stderr = %q", code, errOut.String())
	}
	if gotPath != "/api/queue/qitem-aaa/transitions" {
		t.Errorf("asked %q, want the row's transition log", gotPath)
	}
	for _, want := range []string{"custody-verified: the successor holds the work", "done", "2026-10-13T19:59:43.804Z"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("transitions printed %q, want it to carry %q", out.String(), want)
		}
	}
}

// TestQueueShowReadsOneRow: the row read names the row, its state and why it is where it is.
func TestQueueShowReadsOneRow(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // the daemon's row route is the transport under test here
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"qitemId":"qitem-aaa","state":"blocked","sourceSession":"4genthub-min-lead@4genthub-min","summary":"owner decision owed","blockedOn":"human@kernel","closureRequiredAt":"2026-10-13T23:50:42.519Z","tsUpdated":"2026-10-13T20:00:00.000Z"}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, []string{"show", "qitem-aaa"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("queue show exit = %d, stderr = %q", code, errOut.String())
	}
	if gotPath != "/api/queue/qitem-aaa" {
		t.Errorf("asked %q, want /api/queue/qitem-aaa", gotPath)
	}
	for _, want := range []string{"qitem-aaa", "blocked", "owner decision owed", "human@kernel"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show printed %q, want it to carry %q", out.String(), want)
		}
	}
}

// TestQueueRefusesAWriteSubcommandWithoutTouchingTheDaemon: `queue update <id> --note ...` is the shape
// that reported success while the note went nowhere readable. This shim reads the queue; it must say so
// and send nothing, in both the flagged and the bare spelling.
func TestQueueRefusesAWriteSubcommandWithoutTouchingTheDaemon(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		wantInErr string
	}{
		// A write is refused by name in both spellings (bare and flag-carrying) — the flag spelling must not
		// be answered with a complaint about the write's own argument — while an undeclared flag on a read is
		// refused by the parser, since no verb declares `--nope`.
		{[]string{"update", "qitem-aaa", "--note", "a note"}, "not a read this shim performs"},
		{[]string{"update", "qitem-aaa", "--state", "done"}, "not a read this shim performs"},
		{[]string{"update", "qitem-aaa"}, "not a read this shim performs"},
		{[]string{"claim", "qitem-aaa"}, "not a read this shim performs"},
		{[]string{"list", "--nope"}, "unknown flag --nope"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Setenv("OPENRIG_HOME", t.TempDir())
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			t.Setenv("OPENRIG_URL", srv.URL)
			t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

			var out, errOut bytes.Buffer
			code := queueCommand{}.Run(context.Background(), nil, tc.args, &out, &errOut)

			if code != 2 {
				t.Errorf("exit = %d, want the caller-error code 2", code)
			}
			if calls != 0 {
				t.Errorf("%d requests reached the daemon, want none", calls)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing reported as read", out.String())
			}
			if !strings.Contains(errOut.String(), tc.wantInErr) {
				t.Errorf("stderr = %q, want it to carry %q", errOut.String(), tc.wantInErr)
			}
		})
	}
}

// TestQueueJSONPrintsTheDaemonsOwnDocument: --json is the daemon's bytes, not a re-encode of a struct.
func TestQueueJSONPrintsTheDaemonsOwnDocument(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	noRuntimeSocket(t) // --json here is the DAEMON's document; the runtime has its own test
	document := `[{"qitemId":"qitem-aaa","fieldsElided":["body"],"pickup":{"state":"working"}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(document))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("queue --json exit = %d, stderr = %q", code, errOut.String())
	}
	for _, want := range []string{"fieldsElided", "pickup", "qitem-aaa"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("queue --json printed %q, want the daemon's own document (missing %q)", out.String(), want)
		}
	}
}

// TestOpenRigSendNamesTheDeliveryLikeTheCLI: the route reads `deliveryId` and the daemon uses it as this
// delivery's outbox id, refusing a reuse that names other content rather than re-delivering it. The CLI
// always sends one (send.ts:475); a shim that omits it leaves the id to the daemon, so a replayed send
// is never recognised as the same delivery. One fresh v4 identity per invocation, never a constant.
func TestOpenRigSendNamesTheDeliveryLikeTheCLI(t *testing.T) {
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]bool, 2)
	for i := 0; i < 2; i++ {
		t.Setenv("OPENRIG_HOME", t.TempDir())
		var got map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"ok":true,"outcome":"delivered"}`))
		}))
		defer srv.Close()
		t.Setenv("OPENRIG_URL", srv.URL)
		t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

		if _, _, err := OpenRigSend(context.Background(), "peer@rig", "text", SendOptions{}); err != nil {
			t.Fatalf("OpenRigSend: %v", err)
		}
		id, _ := got["deliveryId"].(string)
		if !v4.MatchString(id) {
			t.Fatalf("deliveryId = %q, want the CLI's own v4 shape", id)
		}
		if seen[id] {
			t.Fatalf("two invocations sent the same deliveryId %q; a reused id is a replay to the daemon", id)
		}
		seen[id] = true
	}
}
