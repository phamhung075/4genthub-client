package clientlifecycle

import (
	"bytes"
	"testing"
)

func TestEnsureBridgeStartsTheBridgeOnlyForAVerbThatNeedsIt(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "https://cloud.invalid")
	t.Setenv("AGENTHUB_TOKEN", "x")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	running, spawn := bridgeRunning, spawnChild
	t.Cleanup(func() { bridgeRunning, spawnChild = running, spawn })
	var started [][]string
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		started = append(started, args)
		return nil, nil
	}

	cases := []struct {
		name    string
		args    []string
		running bool
		want    int
	}{
		{"seat status with no bridge", []string{"seat", "status", "r"}, false, 1},
		{"a bridge already runs", []string{"seat", "status", "r"}, true, 0},
		{"no verb is up's job", nil, false, 0},
		{"up starts its own", []string{"up", "r"}, false, 0},
		{"bridge is the bridge", []string{"bridge", "run"}, false, 0},
		{"stop stops it", []string{"stop", "r"}, false, 0},
		{"help prints", []string{"seat", "--help"}, false, 0},
		{"version prints", []string{"version"}, false, 0},
		{"seatcheck stays cheap", []string{"seatcheck"}, false, 0},
	}
	for _, c := range cases {
		started = nil
		bridgeRunning = func() bool { return c.running }
		EnsureBridge(c.args, &bytes.Buffer{})
		if len(started) != c.want {
			t.Errorf("%s: started %d bridges, want %d", c.name, len(started), c.want)
		}
	}
	if len(started) == 1 && (started[0][0] != "bridge" || started[0][1] != "run") {
		t.Errorf("started %v, want bridge run", started[0])
	}
}

func TestEnsureBridgeWithoutCredentialsStartsNothing(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	running, spawn := bridgeRunning, spawnChild
	t.Cleanup(func() { bridgeRunning, spawnChild = running, spawn })
	bridgeRunning = func() bool { return false }
	spawnChild = func([]string, string) (supervisorChild, error) {
		t.Fatal("spawned a bridge without credentials")
		return nil, nil
	}
	EnsureBridge([]string{"seat", "status", "r"}, &bytes.Buffer{})
}
