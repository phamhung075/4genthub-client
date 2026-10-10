package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/commpolicy"
)

const (
	testRig       = "r"
	testMember    = "a"
	allowPolicy   = `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"delegates_to","Allow":true}]}`
	noLinkPolicy  = `{"Seat":"a"}`
	explicitDeny  = `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"delegates_to","Allow":false}]}`
	unknownIntent = "gossip"
)

type delivery struct {
	calls  int
	target string
	text   string
	code   int
}

// seatEnv installs identity and delivery stubs and returns the pins dir, the seat dir and the
// recorded delivery. policy "" writes no policy file.
func seatEnv(t *testing.T, policy string) (seatDir string, got *delivery) {
	t.Helper()
	pins := t.TempDir()
	seatDir = filepath.Join(pins, testRig, testMember)
	if err := os.MkdirAll(seatDir, 0700); err != nil {
		t.Fatal(err)
	}
	if policy != "" {
		if err := os.WriteFile(filepath.Join(seatDir, "policy.json"), []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got = &delivery{}
	oldPins, oldIdentify, oldDeliver := pinsDir, identify, deliver
	pinsDir = func() (string, error) { return pins, nil }
	identify = func() (identity, error) {
		return identity{Rig: testRig, Member: testMember, Peers: map[string][]string{"b": {"pod-b@r"}}}, nil
	}
	deliver = func(target, text string, _, _ io.Writer) int {
		got.calls++
		got.target, got.text = target, text
		return got.code
	}
	t.Cleanup(func() { pinsDir, identify, deliver = oldPins, oldIdentify, oldDeliver })
	return seatDir, got
}

func send(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(append([]string{"send"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func auditRecords(t *testing.T, seatDir string) []commpolicy.AuditRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(seatDir, "audit.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var records []commpolicy.AuditRecord
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var r commpolicy.AuditRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		records = append(records, r)
	}
	return records
}

func TestSendAllowedDelivers(t *testing.T) {
	seatDir, got := seatEnv(t, allowPolicy)
	code, _, stderr := send("--to", "b", "--intent", "task", "--", "hello", "world")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if got.calls != 1 || got.target != "pod-b@r" || got.text != "hello world" {
		t.Fatalf("delivery = %+v, want one call to pod-b@r with %q", got, "hello world")
	}
	records := auditRecords(t, seatDir)
	if len(records) != 2 || !records[0].Allowed || records[0].From != "a" || records[0].To != "b" || records[0].Intent != commpolicy.IntentTask {
		t.Fatalf("audit = %+v", records)
	}
	if records[0].Outcome != "" || records[1].Outcome != commpolicy.OutcomeDelivered {
		t.Fatalf("outcomes = %q then %q, want none then delivered", records[0].Outcome, records[1].Outcome)
	}
	info, err := os.Stat(filepath.Join(seatDir, "audit.jsonl"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("audit file mode = %v, %v, want 0600", info, err)
	}
}

// Whatever rig send exits with, a failed delivery is seatcheck's own code 5, never one of the
// policy codes (2 usage/policy, 3 denied, 4 bypass) and never rig's code unchanged.
func TestSendDeliveryFailureIsItsOwnExitCode(t *testing.T) {
	for _, rigCode := range []int{1, 2, 3, 4, 7} {
		seatDir, got := seatEnv(t, allowPolicy)
		got.code = rigCode
		code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi")
		if code != exitDeliveryFailed || !strings.Contains(stderr, "delivery failed") {
			t.Fatalf("rig send exit %d: exit = %d, stderr %q, want %d and a delivery failure message", rigCode, code, stderr, exitDeliveryFailed)
		}
		records := auditRecords(t, seatDir)
		if len(records) != 2 || records[0].Outcome != "" || records[1].Outcome != commpolicy.OutcomeDeliveryFailed || !records[1].Allowed {
			t.Fatalf("audit = %+v, want the decision line then delivery_failed", records)
		}
	}
}

func TestSendDeniedWritesAuditAndSkipsDelivery(t *testing.T) {
	cases := []struct {
		name, policy, to, intent, reason string
	}{
		{"no link", noLinkPolicy, "b", "task", "no link"},
		{"wrong intent", allowPolicy, "b", "report", ""},
		{"explicit deny", explicitDeny, "b", "task", ""},
		{"unlinked recipient", allowPolicy, "c", "task", "no link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seatDir, got := seatEnv(t, tc.policy)
			code, stdout, stderr := send("--to", tc.to, "--intent", tc.intent, "--", "hi")
			if code != 3 {
				t.Fatalf("exit = %d, want 3 (stderr %q)", code, stderr)
			}
			records := auditRecords(t, seatDir)
			if len(records) != 1 || records[0].Allowed || records[0].Reason == "" {
				t.Fatalf("audit = %+v, want one denied record with a reason", records)
			}
			if !strings.HasPrefix(stderr, "denied: "+records[0].Reason+"\n") {
				t.Fatalf("stderr = %q, want denied: %s", stderr, records[0].Reason)
			}
			if tc.reason != "" && records[0].Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", records[0].Reason, tc.reason)
			}
			if got.calls != 0 || stdout != "" {
				t.Fatalf("delivery ran on a denied send: %+v, stdout %q", got, stdout)
			}
		})
	}
}

func TestSendPolicyMissingOrCorruptFailsClosed(t *testing.T) {
	for name, policy := range map[string]string{"missing": "", "corrupt": `{"Seat":`} {
		t.Run(name, func(t *testing.T) {
			seatDir, got := seatEnv(t, policy)
			code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi")
			if code != 2 || stderr == "" {
				t.Fatalf("exit = %d, stderr %q, want 2 with a message", code, stderr)
			}
			if got.calls != 0 {
				t.Fatal("delivery ran without a usable policy")
			}
			if records := auditRecords(t, seatDir); len(records) != 0 {
				t.Fatalf("audit = %+v, want none", records)
			}
		})
	}
}

// The store does NOT follow HOME. A seat is launched with HOME pointed at its own state
// directory, so a store resolved from the environment is a function of who invokes the guard:
// the seat looks somewhere the client never writes and a policy that WAS installed reads as
// never installed. Every other test replaces pinsDir through seatEnv, so this one is the only
// place the real resolver is exercised at all.
func TestDefaultPinsDoesNotFollowHome(t *testing.T) {
	seatHome := t.TempDir()
	t.Setenv("HOME", seatHome)

	first, err := defaultPins()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(first, seatHome) {
		t.Fatalf("the store followed HOME into the seat's own directory: %s", first)
	}
	if !strings.HasSuffix(first, filepath.Join(".openrig", "agenthub-seats")) {
		t.Fatalf("the store is not the pins directory: %s", first)
	}

	other := t.TempDir()
	t.Setenv("HOME", other)
	second, err := defaultPins()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("the store depends on HOME: %q then %q", first, second)
	}

	// Driven through the real resolver and no stubs: a seat with no policy is refused CLOSED and
	// is told the exact path that was missing, which is the store a writer must fill.
	oldPins, oldIdentify, oldDeliver := pinsDir, identify, deliver
	got := &delivery{}
	pinsDir = defaultPins
	identify = func() (identity, error) {
		return identity{Rig: testRig, Member: testMember, Peers: map[string][]string{"b": {"pod-b@r"}}}, nil
	}
	deliver = func(target, text string, _, _ io.Writer) int {
		got.calls++
		got.target, got.text = target, text
		return got.code
	}
	t.Cleanup(func() { pinsDir, identify, deliver = oldPins, oldIdentify, oldDeliver })

	code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi")
	if code != 2 || got.calls != 0 {
		t.Fatalf("no policy: exit = %d, calls = %d, stderr %q, want a closed refusal with no delivery", code, got.calls, stderr)
	}
	wantPath := filepath.Join(first, testRig, testMember, "policy.json")
	if !strings.Contains(stderr, wantPath) {
		t.Fatalf("the refusal must name the path it looked for:\n got %q\nwant it to contain %q", stderr, wantPath)
	}
}

// The store can be fixed at BUILD time, and that is the only way it can be set from outside: a
// binary built with one reads exactly that directory, and nothing a process does afterwards moves
// it - there is no flag and no environment read to change it, which is the property that keeps the
// store out of the hands of the seat the guard constrains.
func TestDefaultPinsUsesTheBuildTimeStore(t *testing.T) {
	old := installedPinsDir
	installedPinsDir = "/baked/store"
	t.Cleanup(func() { installedPinsDir = old })

	got, err := defaultPins()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/baked/store" {
		t.Fatalf("defaultPins = %q, want the baked store", got)
	}
}

func TestSendUsageErrorsExit2(t *testing.T) {
	cases := map[string][]string{
		"no recipient":   {"--intent", "task", "--", "hi"},
		"no intent":      {"--to", "b", "--", "hi"},
		"no message":     {"--to", "b", "--intent", "task"},
		"unknown intent": {"--to", "b", "--intent", unknownIntent, "--", "hi"},
		"bad flag":       {"--bogus"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			seatDir, got := seatEnv(t, allowPolicy)
			if code, _, stderr := send(args...); code != 2 || stderr == "" {
				t.Fatalf("exit = %d, stderr %q, want 2 with a message", code, stderr)
			}
			if got.calls != 0 || len(auditRecords(t, seatDir)) != 0 {
				t.Fatal("a usage error must neither deliver nor audit")
			}
		})
	}
}

func TestSendIdentityFailureExits2(t *testing.T) {
	_, got := seatEnv(t, allowPolicy)
	identify = func() (identity, error) { return identity{}, io.ErrUnexpectedEOF }
	if code, _, _ := send("--to", "b", "--intent", "task", "--", "hi"); code != 2 || got.calls != 0 {
		t.Fatalf("exit = %d, calls = %d, want 2 and no delivery", code, got.calls)
	}
}

// The policy may allow a recipient that is not in the rig roster: nothing is delivered.
func TestSendAllowedRecipientOutsideRosterFails(t *testing.T) {
	seatDir, got := seatEnv(t, `{"Seat":"a","Links":[{"From":"a","To":"ghost","Kind":"delegates_to","Allow":true}]}`)
	code, _, stderr := send("--to", "ghost", "--intent", "task", "--", "hi")
	if code != exitDeliveryFailed || !strings.Contains(stderr, "not a seat of rig") || got.calls != 0 {
		t.Fatalf("exit = %d, stderr %q, calls = %d, want %d, a roster message, no delivery", code, stderr, got.calls, exitDeliveryFailed)
	}
	if records := auditRecords(t, seatDir); len(records) != 2 || !records[0].Allowed || records[1].Outcome != commpolicy.OutcomeDeliveryFailed {
		t.Fatalf("audit = %+v, want the allowed decision then delivery_failed", records)
	}
}

// parseWhoami reads the roster the way rig whoami prints it: peers are keyed by member name
// and carry the full session name rig send needs.
func TestParseWhoamiRoster(t *testing.T) {
	who, err := parseWhoami([]byte(`{"identity":{"rigName":"scratchcomm","memberId":"alpha","sessionName":"scratchcomm-alpha@scratchcomm"},` +
		`"peers":[{"logicalId":"scratchcomm.beta","sessionName":"scratchcomm-beta@scratchcomm"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if who.Rig != "scratchcomm" || who.Member != "alpha" || len(who.Peers) != 1 || strings.Join(who.Peers["beta"], ",") != "scratchcomm-beta@scratchcomm" {
		t.Fatalf("identity = %+v", who)
	}
	if _, err := parseWhoami([]byte(`{"identity":{}}`)); err == nil {
		t.Fatal("an identity without rig or member must fail")
	}
}

// In a multi-pod rig the pod is not the rig name: the member is what follows the first dot.
func TestParseWhoamiMultiPodRig(t *testing.T) {
	who, err := parseWhoami([]byte(`{"identity":{"rigName":"4genthub-go","memberId":"coder"},"peers":[` +
		`{"logicalId":"dev.reviewer","sessionName":"dev-reviewer@4genthub-go"},` +
		`{"logicalId":"agy.check","sessionName":"agy-check@4genthub-go"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(who.Peers["reviewer"], ",") != "dev-reviewer@4genthub-go" || strings.Join(who.Peers["check"], ",") != "agy-check@4genthub-go" || len(who.Peers) != 2 {
		t.Fatalf("peers = %v", who.Peers)
	}
}

// A member name repeated across pods is ambiguous only for a send to that name.
func TestParseWhoamiKeepsEverySessionOfARepeatedMember(t *testing.T) {
	who, err := parseWhoami([]byte(`{"identity":{"rigName":"r","memberId":"a"},"peers":[` +
		`{"logicalId":"agy.check","sessionName":"agy-check@r"},{"logicalId":"dev.check","sessionName":"dev-check@r"},{"logicalId":"dev.lead","sessionName":"dev-lead@r"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(who.Peers["check"]) != 2 || len(who.Peers["lead"]) != 1 {
		t.Fatalf("peers = %v", who.Peers)
	}
}

func TestSendToARepeatedMemberNameIsAmbiguousButOthersWork(t *testing.T) {
	policy := `{"Seat":"a","Links":[{"From":"a","To":"check","Kind":"delegates_to","Allow":true},{"From":"a","To":"b","Kind":"delegates_to","Allow":true}]}`
	_, got := seatEnv(t, policy)
	identify = func() (identity, error) {
		return identity{Rig: testRig, Member: testMember, Peers: map[string][]string{
			"b": {"pod-b@r"}, "check": {"agy-check@r", "dev-check@r"},
		}}, nil
	}
	if code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi"); code != 0 || got.target != "pod-b@r" {
		t.Fatalf("send to the unique peer: exit %d, stderr %q, target %q", code, stderr, got.target)
	}
	got.calls = 0
	code, _, stderr := send("--to", "check", "--intent", "task", "--", "hi")
	if code != exitDeliveryFailed || got.calls != 0 || !strings.Contains(stderr, "agy-check@r") || !strings.Contains(stderr, "dev-check@r") {
		t.Fatalf("send to the repeated name: exit %d, calls %d, stderr %q, want %d and both sessions named", code, got.calls, stderr, exitDeliveryFailed)
	}
}

// The seat this guard constrains cannot choose where the policy and audit live: there is no
// --pins flag, and a policy that belongs to another seat is refused.
func TestSendHasNoPinsFlag(t *testing.T) {
	_, got := seatEnv(t, allowPolicy)
	if code, _, stderr := send("--pins", t.TempDir(), "--to", "b", "--intent", "task", "--", "hi"); code != 2 || got.calls != 0 || stderr == "" {
		t.Fatalf("exit = %d, calls = %d, stderr %q, want usage error 2 and no delivery", code, got.calls, stderr)
	}
}

func TestSendRefusesAPolicyOfAnotherSeat(t *testing.T) {
	seatDir, got := seatEnv(t, `{"Seat":"mallory","Links":[{"From":"mallory","To":"b","Kind":"delegates_to","Allow":true}]}`)
	code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi")
	if code != 2 || !strings.Contains(stderr, "belongs to seat") || got.calls != 0 {
		t.Fatalf("exit = %d, stderr %q, calls = %d, want 2, a mismatch message, no delivery", code, stderr, got.calls)
	}
	if records := auditRecords(t, seatDir); len(records) != 0 {
		t.Fatalf("audit = %+v, want none", records)
	}
}

func TestSendRefusesAnIdentityThatIsNotADirectoryName(t *testing.T) {
	for _, bad := range []identity{
		{Rig: "..", Member: "a"}, {Rig: "r", Member: "../x"}, {Rig: "r/x", Member: "a"}, {Rig: ".", Member: "a"},
	} {
		_, got := seatEnv(t, allowPolicy)
		identify = func() (identity, error) { return bad, nil }
		if code, _, _ := send("--to", "b", "--intent", "task", "--", "hi"); code != 2 || got.calls != 0 {
			t.Errorf("identity %+v: exit = %d, calls = %d, want 2 and no delivery", bad, code, got.calls)
		}
	}
}

// The decision line is on disk before delivery starts.
func TestSendAuditsBeforeDelivering(t *testing.T) {
	seatDir, got := seatEnv(t, allowPolicy)
	var seenAtDelivery []commpolicy.AuditRecord
	deliver = func(target, text string, _, _ io.Writer) int {
		got.calls++
		seenAtDelivery = auditRecords(t, seatDir)
		return 0
	}
	if code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi"); code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if len(seenAtDelivery) != 1 || !seenAtDelivery[0].Allowed || seenAtDelivery[0].Outcome != "" {
		t.Fatalf("audit at delivery time = %+v, want exactly the decision line", seenAtDelivery)
	}
}

func TestSendAuditFileMustBePrivate(t *testing.T) {
	seatDir, got := seatEnv(t, allowPolicy)
	path := filepath.Join(seatDir, "audit.jsonl")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := send("--to", "b", "--intent", "task", "--", "hi"); code != exitAuditFailed || got.calls != 0 {
		t.Fatalf("exit = %d, calls = %d, want %d and no delivery", code, got.calls, exitAuditFailed)
	}
}

func TestSendAuditAppendsAcrossRuns(t *testing.T) {
	seatDir, _ := seatEnv(t, allowPolicy)
	for i := 0; i < 2; i++ {
		if code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi"); code != 0 {
			t.Fatalf("run %d exit = %d (stderr %q)", i, code, stderr)
		}
	}
	if got := auditRecords(t, seatDir); len(got) != 4 {
		t.Fatalf("audit lines = %d, want 4 (decision and outcome per run)", len(got))
	}
}

func TestSendAuditFailureExits1WithoutDelivery(t *testing.T) {
	seatDir, got := seatEnv(t, allowPolicy)
	if err := os.Mkdir(filepath.Join(seatDir, "audit.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := send("--to", "b", "--intent", "task", "--", "hi"); code != 1 || got.calls != 0 {
		t.Fatalf("exit = %d, calls = %d, want 1 and no delivery", code, got.calls)
	}
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestAuditScanFindsBypassAndSkipsKnownWrapper(t *testing.T) {
	dir := t.TempDir()
	observed := writeFile(t, filepath.Join(dir, "observed.txt"),
		"rig send b@r hi\nFOO=1 /usr/bin/rig queue y\nseatcheck send --to b --intent task -- hi\nrig status\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"audit-scan", "--known", "seatcheck", "--file", observed}, &stdout, &stderr)
	if code != 4 {
		t.Fatalf("exit = %d, want 4 (stderr %q)", code, stderr.String())
	}
	want := "bypass-suspected: rig send b@r hi\nbypass-suspected: FOO=1 /usr/bin/rig queue y\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestAuditScanCleanExits0(t *testing.T) {
	dir := t.TempDir()
	observed := writeFile(t, filepath.Join(dir, "observed.txt"), "seatcheck send --to b --intent task -- hi\nrig status\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"audit-scan", "--known", "seatcheck", "--file", observed}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// A send end to end through the real delivery seam: seatcheck joins the message words and
// hands the roster session the whole text as one positional argument after --.
func TestSendEndToEndDeliversThroughRig(t *testing.T) {
	seatDir, _ := seatEnv(t, allowPolicy)
	deliver = rigSend
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	t.Setenv(fakeRigArgsVar, argsPath)
	t.Setenv(fakeRigStdinVar, filepath.Join(dir, "stdin"))
	fakeRig(t, fakeRigArgvNul)

	code, _, stderr := send("--to", "b", "--intent", "task", "--", "fix", "the", "bug")
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q, want 0", code, stderr)
	}
	got := readArgv(t, argsPath)
	want := []string{"send", "--", "pod-b@r", "fix the bug"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	records := auditRecords(t, seatDir)
	if len(records) != 2 || records[0].Outcome != "" || records[1].Outcome != commpolicy.OutcomeDelivered {
		t.Fatalf("audit = %+v, want the decision line then delivered", records)
	}
}

// A delivery that succeeds but whose outcome line cannot be written must still exit 0: a
// retrying caller must not send the message twice. A failed delivery keeps its own code.
func TestSendOutcomeAuditFailureAfterDeliveryKeepsExitZero(t *testing.T) {
	cases := []struct {
		name     string
		delivery int
		want     int
	}{
		{"delivery succeeds", 0, 0},
		{"delivery fails", 3, exitDeliveryFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seatDir, got := seatEnv(t, allowPolicy)
			deliver = func(target, text string, _, _ io.Writer) int {
				got.calls++
				got.target, got.text = target, text
				if err := os.Chmod(filepath.Join(seatDir, "audit.jsonl"), 0644); err != nil {
					t.Fatalf("chmod audit: %v", err)
				}
				return tc.delivery
			}
			code, _, stderr := send("--to", "b", "--intent", "task", "--", "hi")
			if code != tc.want {
				t.Fatalf("exit = %d, stderr %q, want %d", code, stderr, tc.want)
			}
			if !strings.Contains(stderr, "outcome could not be audited") {
				t.Fatalf("stderr = %q, want it to mention the unaudited outcome", stderr)
			}
			if got.calls != 1 {
				t.Fatalf("delivery calls = %d, want exactly 1", got.calls)
			}
			records := auditRecords(t, seatDir)
			if len(records) != 1 || records[0].Outcome != "" {
				t.Fatalf("audit = %+v, want exactly the decision line", records)
			}
		})
	}
}

// A --to that is a full roster session name (<pod>-<member>@<rig>) is mapped to its member
// before the policy check: the audit carries the member key and delivery goes to exactly the
// session named.
func TestSendAcceptsAFullSessionName(t *testing.T) {
	seatDir, got := seatEnv(t, allowPolicy)
	code, _, stderr := send("--to", "pod-b@r", "--intent", "task", "--", "hi")
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q, want 0", code, stderr)
	}
	if got.calls != 1 || got.target != "pod-b@r" {
		t.Fatalf("delivery = %+v, want one call to pod-b@r", got)
	}
	records := auditRecords(t, seatDir)
	if len(records) != 2 {
		t.Fatalf("audit lines = %d, want 2 (decision then outcome)", len(records))
	}
	for i, r := range records {
		if r.To != "b" {
			t.Fatalf("record %d To = %q, want b", i, r.To)
		}
	}
	if records[0].Outcome != "" || records[1].Outcome != commpolicy.OutcomeDelivered {
		t.Fatalf("outcomes = %q then %q, want none then delivered", records[0].Outcome, records[1].Outcome)
	}
}

// A full session name of a real roster member the policy does not link is still a known
// recipient: the send is denied for lack of a link, not for being unknown.
func TestSendFullSessionNameOfAnUnlinkedSeatIsDenied(t *testing.T) {
	seatDir, got := seatEnv(t, allowPolicy)
	identify = func() (identity, error) {
		return identity{Rig: testRig, Member: testMember, Peers: map[string][]string{
			"b": {"pod-b@r"}, "c": {"pod-c@r"},
		}}, nil
	}
	code, stdout, stderr := send("--to", "pod-c@r", "--intent", "task", "--", "hi")
	if code != exitDenied {
		t.Fatalf("exit = %d, stderr %q, want %d (denied)", code, stderr, exitDenied)
	}
	if got.calls != 0 || stdout != "" {
		t.Fatalf("delivery ran on a denied send: %+v, stdout %q", got, stdout)
	}
	records := auditRecords(t, seatDir)
	if len(records) != 1 || records[0].Allowed || records[0].To != "c" {
		t.Fatalf("audit = %+v, want one denied record with To == c", records)
	}
	if !strings.Contains(stderr, "denied: no link") {
		t.Fatalf("stderr = %q, want it to say denied: no link", stderr)
	}
	if strings.Contains(stderr, "unknown recipient") {
		t.Fatalf("stderr = %q, must not call a real roster member unknown", stderr)
	}
}

// A recipient that is neither a seat key nor a roster session is unknown: denied by policy and
// told which seat keys could be used.
func TestSendUnknownRecipientListsSeatKeys(t *testing.T) {
	seatDir, got := seatEnv(t, allowPolicy)
	code, stdout, stderr := send("--to", "ghost@nowhere", "--intent", "task", "--", "hi")
	if code != exitDenied {
		t.Fatalf("exit = %d, stderr %q, want %d (denied)", code, stderr, exitDenied)
	}
	if got.calls != 0 || stdout != "" {
		t.Fatalf("delivery ran on an unknown recipient: %+v, stdout %q", got, stdout)
	}
	records := auditRecords(t, seatDir)
	if len(records) != 1 || records[0].Allowed {
		t.Fatalf("audit = %+v, want one denied record", records)
	}
	if !strings.HasPrefix(stderr, "denied: no link\n") {
		t.Fatalf("stderr = %q, want it to start with denied: no link", stderr)
	}
	want := `unknown recipient "ghost@nowhere"; use a seat key: b`
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
}

// Naming one session of a member whose name is repeated across pods delivers to exactly that
// session, while sending to the bare member name stays ambiguous.
func TestSendFullSessionNameWithRepeatedMemberDeliversToThatSession(t *testing.T) {
	_, got := seatEnv(t, allowPolicy)
	identify = func() (identity, error) {
		return identity{Rig: testRig, Member: testMember, Peers: map[string][]string{
			"b": {"pod-b@r", "other-b@r"},
		}}, nil
	}
	code, _, stderr := send("--to", "other-b@r", "--intent", "task", "--", "hi")
	if code != 0 || got.calls != 1 || got.target != "other-b@r" {
		t.Fatalf("send to other-b@r: exit %d, delivery %+v, stderr %q, want 0 and target other-b@r", code, got, stderr)
	}
	got.calls = 0
	code, _, stderr = send("--to", "b", "--intent", "task", "--", "hi")
	if code != exitDeliveryFailed || got.calls != 0 {
		t.Fatalf("send to b: exit %d, calls %d, stderr %q, want %d and no delivery", code, got.calls, stderr, exitDeliveryFailed)
	}
	if !strings.Contains(stderr, "pod-b@r") || !strings.Contains(stderr, "other-b@r") {
		t.Fatalf("stderr = %q, want both sessions named", stderr)
	}
}

// resolveRecipient is the seam that maps --to to a seat key and, when the name was a session,
// to the session to deliver to.
func TestResolveRecipient(t *testing.T) {
	who := identity{Rig: testRig, Member: testMember, Peers: map[string][]string{"b": {"pod-b@r"}}}
	policy := commpolicy.Policy{Seat: "a", Links: []commpolicy.Link{
		{From: "a", To: "c", Kind: commpolicy.KindDelegatesTo, Allow: true},
	}}
	cases := []struct {
		name          string
		to            string
		wantRecipient string
		wantSession   string
		wantKnown     bool
	}{
		{"seat key as is", "b", "b", "", true},
		{"calling seat", "a", "a", "", true},
		{"policy link seat not in roster", "c", "c", "", true},
		{"roster session maps to member", "pod-b@r", "b", "pod-b@r", true},
		{"unknown", "ghost@nowhere", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recipient, session, known := resolveRecipient(who, policy, tc.to)
			if recipient != tc.wantRecipient || session != tc.wantSession || known != tc.wantKnown {
				t.Fatalf("resolveRecipient(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.to, recipient, session, known, tc.wantRecipient, tc.wantSession, tc.wantKnown)
			}
		})
	}
}

// The hint names only the seats the caller's policy allows for the intent, never other roster
// members, and says so when there are none.
func TestSendUnknownRecipientHintListsOnlyAllowedSeats(t *testing.T) {
	_, _ = seatEnv(t, `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"delegates_to","Allow":true},{"From":"a","To":"d","Kind":"delegates_to","Allow":false}]}`)
	identify = func() (identity, error) {
		return identity{Rig: testRig, Member: testMember, Peers: map[string][]string{
			"b": {"pod-b@r"}, "c": {"pod-c@r"}, "d": {"pod-d@r"},
		}}, nil
	}
	_, _, stderr := send("--to", "ghost", "--intent", "task", "--", "hi")
	if !strings.Contains(stderr, `unknown recipient "ghost"; use a seat key: b`+"\n") {
		t.Fatalf("stderr = %q, want only b listed", stderr)
	}
	for _, other := range []string{", c", ", d", "key: c", "key: d"} {
		if strings.Contains(stderr, other) {
			t.Errorf("stderr %q names a seat the caller may not message (%q)", stderr, other)
		}
	}

	_, _ = seatEnv(t, noLinkPolicy)
	_, _, stderr = send("--to", "ghost", "--intent", "task", "--", "hi")
	if !strings.Contains(stderr, `unknown recipient "ghost"; your policy allows no recipient for intent "task"`) {
		t.Fatalf("stderr = %q, want the no-recipient hint", stderr)
	}
}
