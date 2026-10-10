package clientsync

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// bundleHashLong is the Python's HASH_LONG; the bundle name carries its first eight characters.
const bundleHashLong = "c3d4e5f6" +
	"00000000000000000000000000000000000000000000000000000000"

// bundleRigYAML is BUNDLE_RIG_YAML: one pod, one member pointing at a local agent directory.
const bundleRigYAML = "version: \"0.2\"\n" +
	"name: room1\n" +
	"pods:\n" +
	"  - id: room1\n" +
	"    members:\n" +
	"      - id: seat1\n" +
	"        agent_ref: local:agents/seat1\n"

// bundleStoreHome points the seat store (DefaultSeatStore reads the account home) at a scratch home,
// and returns the store root under it. The Python tests monkeypatch DEFAULT_OUT for the same reason.
func bundleStoreHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".openrig", "agenthub-seats")
}

// bundleWritePin writes the lock the verb must use - the PINNED hash, not the cloud's.
func bundleWritePin(t *testing.T, store, hash string) {
	t.Helper()
	seatDir := filepath.Join(store, "room1", "seat1")
	if err := os.MkdirAll(seatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"hash": hash, "path": filepath.Join(seatDir, hash)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seatDir, "pinned.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

// bundleStubRig records every argv and answers with the caller's function, so a test asserts the exact
// command and drives the failure branches without a rig.
func bundleStubRig(t *testing.T, respond func([]string) error) *[][]string {
	t.Helper()
	calls := &[][]string{}
	original := bundleRunRig
	bundleRunRig = func(argv []string) error {
		*calls = append(*calls, append([]string(nil), argv...))
		return respond(argv)
	}
	t.Cleanup(func() { bundleRunRig = original })
	return calls
}

func bundleRun(t *testing.T, rigYAML, rigRoot, outDir string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RunBundleVerb(context.Background(),
		[]string{"room1", "seat1", "--rig-yaml", rigYAML, "--rig-root", rigRoot, "--out-dir", outDir},
		&stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// bundleFixture is the pinned seat in a scratch store, plus a rig root whose agent dir may carry a
// policy; outDir is where the bundle lands.
func bundleFixture(t *testing.T, policy map[string]any) (rigYAML, rigRoot, outDir string) {
	t.Helper()
	store := bundleStoreHome(t)
	bundleWritePin(t, store, bundleHashLong)

	scratch := t.TempDir()
	rigYAML = filepath.Join(scratch, "rig.yaml")
	if err := os.WriteFile(rigYAML, []byte(bundleRigYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	rigRoot = filepath.Join(scratch, "rig-root")
	agentDir := filepath.Join(rigRoot, "agents", "seat1")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte("name: seat1\nversion: \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if policy != nil {
		encoded, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, "policy.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outDir = filepath.Join(scratch, "bundles")
	return rigYAML, rigRoot, outDir
}

// test_bundle_uses_pinned_hash_and_argument_list: the pinned hash is what names the file and what rides
// in the notes, and the argv is exactly the one the Python builds.
func TestBundleUsesPinnedHashAndArgumentList(t *testing.T) {
	store := bundleStoreHome(t)
	bundleWritePin(t, store, bundleHashLong)
	scratch := t.TempDir()
	rigYAML := filepath.Join(scratch, "rig.yaml")
	rigRoot := filepath.Join(scratch, "rig-root")
	outDir := filepath.Join(scratch, "bundles")
	calls := bundleStubRig(t, func([]string) error { return nil })

	code, out, errOut := bundleRun(t, rigYAML, rigRoot, outDir)

	absoluteOut, err := filepath.Abs(outDir)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(absoluteOut, "room1-seat1-"+bundleHashLong[:8]+".rigbundle")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing: the pinned case is silent", errOut)
	}
	if got := strings.TrimSpace(out); got != bundlePath {
		t.Errorf("stdout = %q, want %q", got, bundlePath)
	}
	want := []string{
		"rig", "bundle", "create", rigYAML,
		"--rig-root", rigRoot,
		"-o", bundlePath,
		"--name", "room1-seat1",
		"--bundle-version", "1.0.0",
		"--notes", "resolved_sha256=" + bundleHashLong + " source=4genteam",
	}
	got := *calls
	if len(got) != 1 || strings.Join(got[0], "\x00") != strings.Join(want, "\x00") {
		t.Errorf("rig was called with %v, want exactly %v", got, want)
	}
}

// test_bundle_without_lock_exits_2: no pin at all is a usage error naming the pull that would create one.
func TestBundleWithoutLockExitsTwo(t *testing.T) {
	bundleStoreHome(t)
	calls := bundleStubRig(t, func([]string) error { return nil })

	code, out, errOut := bundleRun(t, "/nonexistent/rig.yaml", "/nonexistent/rig-root", t.TempDir())

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "no pinned snapshot") {
		t.Errorf("stderr = %q, want it to name the missing pin", errOut)
	}
	if len(*calls) != 0 {
		t.Errorf("rig was called anyway: %v", *calls)
	}
}

// test_bundle_warns_when_the_rig_root_carries_no_pin: the artifact builds, stdout stays the machine line,
// and the WARNING on stderr says what the bundle does not contain.
func TestBundleWarnsWhenTheRigRootCarriesNoPin(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, nil)
	bundleStubRig(t, func([]string) error { return nil })

	code, out, errOut := bundleRun(t, rigYAML, rigRoot, outDir)

	absoluteOut, err := filepath.Abs(outDir)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(absoluteOut, "room1-seat1-"+bundleHashLong[:8]+".rigbundle")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut)
	}
	if strings.TrimSpace(out) != bundlePath {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(out), bundlePath)
	}
	for _, want := range []string{"warning:", "seat1", "no policy.json in agents/seat1", "offline install will refuse it"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not say %q:\n%s", want, errOut)
		}
	}
}

// test_bundle_warns_when_the_pin_belongs_to_another_seat: two seats sharing one agent directory can only
// carry one policy, and the seat that lost is named.
func TestBundleWarnsWhenThePinBelongsToAnotherSeat(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, map[string]any{"Seat": "someone-else", "Links": []any{}})
	bundleStubRig(t, func([]string) error { return nil })

	code, _, errOut := bundleRun(t, rigYAML, rigRoot, outDir)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if !strings.Contains(errOut, "warning:") {
		t.Errorf("stderr = %q, want a warning", errOut)
	}
	if !strings.Contains(errOut, "belongs to seat 'someone-else'") {
		t.Errorf("stderr = %q, want the foreign seat named", errOut)
	}
}

// test_bundle_says_nothing_when_the_pin_is_this_seats: no noise when the rig root carries this seat.
func TestBundleSaysNothingWhenThePinIsThisSeats(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, map[string]any{"Seat": "seat1", "Links": []any{}})
	bundleStubRig(t, func([]string) error { return nil })

	code, out, errOut := bundleRun(t, rigYAML, rigRoot, outDir)

	absoluteOut, err := filepath.Abs(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing at all", errOut)
	}
	if want := filepath.Join(absoluteOut, "room1-seat1-"+bundleHashLong[:8]+".rigbundle"); strings.TrimSpace(out) != want {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(out), want)
	}
}

// A missing rig is a remote failure, not a usage error, and it names what is missing.
func TestBundleMissingRigExitsOne(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, nil)
	bundleStubRig(t, func([]string) error { return errBundleRigNotFound })

	code, out, errOut := bundleRun(t, rigYAML, rigRoot, outDir)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "`rig` executable not found on PATH") {
		t.Errorf("stderr = %q, want it to name the missing binary", errOut)
	}
}

// A non-zero `rig bundle create` is a remote failure carrying the code it failed with.
func TestBundleCreateFailureExitsOne(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, nil)
	bundleStubRig(t, func([]string) error { return exitCodeError(7) })

	code, out, errOut := bundleRun(t, rigYAML, rigRoot, outDir)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "rig bundle create failed with exit code 7") {
		t.Errorf("stderr = %q, want the failing code", errOut)
	}
}
