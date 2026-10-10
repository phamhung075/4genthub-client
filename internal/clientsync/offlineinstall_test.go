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

// bundleFor writes a materialized bundle: a rig spec, and one agent directory per agent reference
// carrying the pinned policy that travels with it.
func bundleFor(t *testing.T, spec string, agents map[string]string) string {
	t.Helper()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "rig.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	for agent, policy := range agents {
		directory := filepath.Join(target, "agents", agent)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "policy.json"), []byte(policy), 0o644); err != nil {
			t.Fatal(err)
		}
		pinned, err := json.Marshal(map[string]string{"hash": "HASH_" + agent})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "pinned.json"), pinned, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func offlineReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(text, &document); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return document
}

// TestOfflineInstallWritesTheStoreAndSkipsAForeignPolicy: seatcheck reads
// <home>/.openrig/agenthub-seats/<room>/<member>/policy.json, so this is what makes a materialized
// bundle decidable offline. Two members sharing one seat type share one agent directory, so only one
// of their policies can ride there: installing must refuse a policy naming another seat instead of
// handing this seat the wrong links.
func TestOfflineInstallWritesTheStoreAndSkipsAForeignPolicy(t *testing.T) {
	const spec = `name: room1
pods:
  - id: main
    members:
      - id: alpha
        agent_ref: local:agents/dev
      - id: beta
        agent_ref: local:agents/dev
      - id: gamma
        agent_ref: local:agents/ops
`
	target := bundleFor(t, spec, map[string]string{
		"dev": `{"Seat":"alpha"}`,
		"ops": `{"Seat":"gamma"}`,
	})
	home := t.TempDir()
	store := filepath.Join(home, ".openrig", "agenthub-seats")

	var stdout, stderr bytes.Buffer
	code := RunOfflineInstallVerb(context.Background(),
		[]string{target, "--home", home}, &stdout, &stderr)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	want := "installed room1/alpha -> " + filepath.Join(store, "room1", "alpha") + "\n" +
		"installed room1/gamma -> " + filepath.Join(store, "room1", "gamma") + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if got := offlineReadJSON(t, filepath.Join(store, "room1", "alpha", "policy.json"))["Seat"]; got != "alpha" {
		t.Errorf("alpha's policy names seat %v", got)
	}
	if got := offlineReadJSON(t, filepath.Join(store, "room1", "alpha", "pinned.json"))["hash"]; got != "HASH_dev" {
		t.Errorf("alpha's pin is %v, want the pin that rode with its agent", got)
	}
	if got := offlineReadJSON(t, filepath.Join(store, "room1", "gamma", "policy.json"))["Seat"]; got != "gamma" {
		t.Errorf("gamma's policy names seat %v", got)
	}
	// beta shares agents/dev with alpha, so it gets NOTHING rather than alpha's policy.
	if _, err := os.Lstat(filepath.Join(store, "room1", "beta")); err == nil {
		t.Error("beta was installed a policy that belongs to another seat")
	}
	message := stderr.String()
	for _, want := range []string{"skipped room1/beta", "belongs to seat 'alpha'"} {
		if !strings.Contains(message, want) {
			t.Errorf("stderr does not say %q:\n%s", want, message)
		}
	}
}

// TestOfflineInstallWithoutAnyPolicyFailsLoudly: a bundle with nothing to install must not report
// success, because an offline seat whose policy is absent cannot decide or audit.
func TestOfflineInstallWithoutAnyPolicyFailsLoudly(t *testing.T) {
	target := bundleFor(t, `name: room1
pods:
  - id: main
    members:
      - id: alpha
        agent_ref: local:agents/dev
`, nil)

	var stdout, stderr bytes.Buffer
	code := RunOfflineInstallVerb(context.Background(),
		[]string{target, "--home", t.TempDir()}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "no pinned policy found") {
		t.Errorf("stderr = %q, want it to say there was nothing to install", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing installed", stdout.String())
	}
}
