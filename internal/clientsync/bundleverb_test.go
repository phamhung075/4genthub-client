package clientsync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
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

// bundleStoreHome points the seat store at a scratch root and returns it. The store no longer follows
// $HOME (a seat runs with HOME at its own state directory), so this overrides the machine's store seam
// rather than the environment; the Python tests monkeypatch DEFAULT_OUT for the same reason.
func bundleStoreHome(t *testing.T) string {
	t.Helper()
	store := filepath.Join(t.TempDir(), ".config", "4genthub", "seats")
	original := seatStore
	seatStore = func(string) (string, error) { return store, nil }
	t.Cleanup(func() { seatStore = original })
	return store
}

// A verb with no store flag keeps the reason alone: `sync bundle` takes --out-dir, which is where the
// bundle is written, so its refusal must not advertise a --out it does not accept.
func TestSyncBundleDoesNotAdvertiseAStoreFlagItLacks(t *testing.T) {
	original := seatStore
	seatStore = func(string) (string, error) {
		return "", &clientcmd.CodedError{Message: clientenv.SeatStoreMessage, Code: clientcmd.ExitUsage}
	}
	t.Cleanup(func() { seatStore = original })

	var stdout, stderr bytes.Buffer
	code := RunBundleVerb(context.Background(), []string{
		"room1", "seat1",
		"--rig-yaml", filepath.Join(t.TempDir(), "rig.yaml"),
		"--rig-root", t.TempDir(),
	}, &stdout, &stderr)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d: %s", code, clientcmd.ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), clientenv.SeatStoreMessage) {
		t.Errorf("the refusal dropped the reason: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "--out") {
		t.Errorf("the refusal advertises a flag this verb does not take: %q", stderr.String())
	}
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

// test_bundle_uses_pinned_hash: the pinned hash names the file and rides in the notes, and the archive
// carries the rig spec and the agent directory under a manifest that hashes each file.
func TestBundleUsesPinnedHashAndPacksTheRigRoot(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, map[string]any{"Seat": "seat1", "Links": []any{}})

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

	files := bundleRead(t, bundlePath)
	var manifest bundleManifest
	if err := json.Unmarshal(files[bundleManifestName], &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.Name != "room1-seat1" || manifest.Version != "1.0.0" ||
		manifest.Notes != "resolved_sha256="+bundleHashLong+" source=4genteam" {
		t.Errorf("manifest = %+v", manifest)
	}
	if string(files["rig.yaml"]) != bundleRigYAML {
		t.Errorf("rig.yaml was not carried")
	}
	for _, want := range []string{"agents/seat1/agent.yaml", "agents/seat1/policy.json"} {
		if _, ok := files[want]; !ok {
			t.Errorf("the archive lacks %s: %v", want, manifest.Files)
		}
	}
	for _, file := range manifest.Files {
		sum := sha256.Sum256(files[file.Path])
		if file.SHA256 != hex.EncodeToString(sum[:]) || file.Size != int64(len(files[file.Path])) {
			t.Errorf("manifest entry %s does not match its contents", file.Path)
		}
	}
}

// The same inputs always make the same bytes, so a bundle's hash is a fact about its contents.
func TestBundleIsDeterministic(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, map[string]any{"Seat": "seat1", "Links": []any{}})
	read := func() []byte {
		if code, _, errOut := bundleRun(t, rigYAML, rigRoot, outDir); code != clientcmd.ExitOK {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		raw, err := os.ReadFile(filepath.Join(outDir, "room1-seat1-"+bundleHashLong[:8]+".rigbundle"))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	first := read()
	if err := os.Chtimes(filepath.Join(rigRoot, "agents", "seat1", "agent.yaml"), time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, read()) {
		t.Error("a changed mtime changed the bundle")
	}
}

// bundleRead unpacks an archive into a path-to-content map.
func bundleRead(t *testing.T, path string) map[string][]byte {
	t.Helper()
	handle, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	gz, err := gzip.NewReader(handle)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = content
	}
}

// test_bundle_without_lock_exits_2: no pin at all is a usage error naming the pull that would create one.
func TestBundleWithoutLockExitsTwo(t *testing.T) {
	bundleStoreHome(t)
	outDir := t.TempDir()

	code, out, errOut := bundleRun(t, "/nonexistent/rig.yaml", "/nonexistent/rig-root", outDir)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "no pinned snapshot") {
		t.Errorf("stderr = %q, want it to name the missing pin", errOut)
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
		t.Errorf("a bundle was written without a pin: %v", entries)
	}
}

// test_bundle_warns_when_the_rig_root_carries_no_pin: the artifact builds, stdout stays the machine line,
// and the WARNING on stderr says what the bundle does not contain.
func TestBundleWarnsWhenTheRigRootCarriesNoPin(t *testing.T) {
	rigYAML, rigRoot, outDir := bundleFixture(t, nil)

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

// An unreadable rig spec is a remote failure that names what could not be read, and leaves no bundle behind.
func TestBundleUnreadableRigSpecExitsOne(t *testing.T) {
	_, rigRoot, outDir := bundleFixture(t, nil)

	code, out, errOut := bundleRun(t, filepath.Join(t.TempDir(), "missing.yaml"), rigRoot, outDir)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "cannot read the rig spec") {
		t.Errorf("stderr = %q, want it to name the unreadable spec", errOut)
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
		t.Errorf("a failed build left %v behind", entries)
	}
}
