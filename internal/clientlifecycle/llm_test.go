// THE MODEL-EGRESS ROW AND ITS ONE START PATH, as cases: the row is red exactly when a listener answers
// nothing, the start path writes the redirect before it spawns and spawns only the port that is silent,
// a listener that never answers is a named refusal, and agent's pass does not spawn against a port more
// often than the throttle allows.
//
// NO CASE HERE BINDS A PORT: the verdict, the probe and the start grace are seams (TestMain's default is a
// machine where every listener is up), so these cases assert the decisions rather than this workstation.
package clientlifecycle

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientllmproxy"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// stubLlmStart replaces the four seams the start path uses and returns the argv list it spawned.
func stubLlmStart(t *testing.T, listening map[int]bool, answered bool) *[][]string {
	t.Helper()
	var spawned [][]string
	previousEnsure, previousListeners := llmEnsureEnv, llmListeners
	previousListening, previousWait := llmListening, llmWait
	previousSpawn := spawnChild
	t.Cleanup(func() {
		llmEnsureEnv, llmListeners = previousEnsure, previousListeners
		llmListening, llmWait = previousListening, previousWait
		spawnChild = previousSpawn
	})
	llmEnsureEnv = func(string) ([]string, error) { return nil, nil }
	llmListeners = func() []clientllmproxy.Listener { return clientllmproxy.Listeners() }
	llmListening = func(listener clientllmproxy.Listener) bool { return listening[listener.Port] }
	llmWait = func(clientllmproxy.Listener, time.Duration) bool { return answered }
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		spawned = append(spawned, append([]string(nil), args...))
		return fakeChild{pid: 4242}, nil
	}
	return &spawned
}

// fakeChild is a spawn that needs no process: the start path only needs the pid and the log path the spawn
// was given.
type fakeChild struct{ pid int }

func (c fakeChild) Pid() int    { return c.pid }
func (c fakeChild) Wait() error { return nil }

// TestLlmRowIsRunningOnlyWhileEveryListenerAnswers is the row's falsification: the same fixture reads
// running with no missing listener and stopped with one, so the row discriminates instead of always
// passing. A missing listener also prints a reason naming the surface.
func TestLlmRowIsRunningOnlyWhileEveryListenerAnswers(t *testing.T) {
	previous := llmMissing
	t.Cleanup(func() { llmMissing = previous })

	llmMissing = func() []string { return nil }
	rows := llmRowsFor(t.TempDir())
	if len(rows) != 1 || rows[0].Row.State != clientservices.Running {
		t.Fatalf("rows = %+v, want one running row", rows)
	}
	if rows[0].Row.Safeguard != clientservices.Llm || rows[0].Row.AgeS != clientservices.UnknownAge {
		t.Errorf("the row is %+v, want the llm row with an unknown age", rows[0].Row)
	}
	if !strings.HasPrefix(rows[0].Detail, "llm: ") {
		t.Errorf("the detail line is %q, want it to open with the row's name", rows[0].Detail)
	}

	llmMissing = func() []string { return []string{"ANTHROPIC_BASE_URL"} }
	rows = llmRowsFor(t.TempDir())
	if rows[0].Row.State != clientservices.Stopped {
		t.Fatalf("the row is %q with a missing listener, want stopped", rows[0].Row.State)
	}
	lines := strings.Join(llmLines(rows[0]), "\n")
	if !strings.Contains(lines, "ANTHROPIC_BASE_URL: nothing answers on its port") {
		t.Errorf("the reasons do not name the unserved surface:\n%s", lines)
	}
}

// TestEnsureLlmListenersSpawnsOnlyTheSilentPort: a listener that already answers is reused (the other room's
// `up` or agent's earlier start), and the silent one is started with the address the .env names and a log
// under the state root. The redirect writer runs first, before anything is spawned.
func TestEnsureLlmListenersSpawnsOnlyTheSilentPort(t *testing.T) {
	root := t.TempDir()
	var order []string
	spawned := stubLlmStart(t, map[int]bool{7391: true, 7392: false}, true)
	llmEnsureEnv = func(string) ([]string, error) { order = append(order, "env"); return nil, nil }

	if err := ensureLlmListeners(root); err != nil {
		t.Fatalf("ensureLlmListeners: %v", err)
	}
	if len(*spawned) != 1 {
		t.Fatalf("spawned %v, want only the silent listener", *spawned)
	}
	argv := strings.Join((*spawned)[0], " ")
	for _, want := range []string{"llmproxy", "--runtime codex", "--listen 127.0.0.1:7392", "--state-root " + root} {
		if !strings.Contains(argv, want) {
			t.Errorf("the spawned argv %q is missing %q", argv, want)
		}
	}
	if order[0] != "env" {
		t.Errorf("the redirect did not run before the spawn: %v", order)
	}

	// The log is one file per listener, under the state root's log directory.
	if _, err := filepath.Glob(filepath.Join(clientservices.LogDir(root), "llmproxy-codex.log")); err != nil {
		t.Fatal(err)
	}
	if got := llmListenerLogPath(root, clientllmproxy.Listener{Surface: "", Port: 7392, Route: "codex"}); !strings.HasSuffix(got, "llmproxy-codex.log") {
		t.Errorf("the log path = %q", got)
	}
}

// TestEnsureLlmListenersRefusesAListenerThatNeverAnswers: the start grace expiring is a refusal that names
// the address and the log - the shape 7.5 gives a safeguard that did not come up - rather than a sealed
// silence that leaves the seats pointed at a closed port.
func TestEnsureLlmListenersRefusesAListenerThatNeverAnswers(t *testing.T) {
	root := t.TempDir()
	stubLlmStart(t, map[int]bool{7391: true, 7392: false}, false)

	err := ensureLlmListeners(root)
	if err == nil {
		t.Fatal("ensureLlmListeners answered nil for a listener that never answered")
	}
	for _, want := range []string{"127.0.0.1:7392", "llmproxy-codex.log", "fail closed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q is missing %q", err, want)
		}
	}
}

// TestEnsureLlmListenersNamesAStartFailure: a spawn that fails is refused with the listener and the log,
// not treated as a start.
func TestEnsureLlmListenersNamesAStartFailure(t *testing.T) {
	stubLlmStart(t, map[int]bool{7391: false, 7392: true}, true)
	spawnChild = func([]string, string) (supervisorChild, error) { return nil, errors.New("exec: no such file") }

	err := ensureLlmListeners(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_BASE_URL") {
		t.Fatalf("err = %v, want a refusal naming the listener", err)
	}
}

// TestAgentProbesTheListenersNoMoreOftenThanTheThrottle: the pass runs every second, the probe every
// llmCheckEvery. Without the throttle a crashing listener would be spawned on every pass.
func TestAgentProbesTheListenersNoMoreOftenThanTheThrottle(t *testing.T) {
	root := t.TempDir()
	stubLlmStart(t, map[int]bool{7391: true, 7392: true}, true)
	probes := 0
	llmEnsureEnv = func(string) ([]string, error) { probes++; return nil, nil }

	m := newAgentManager(root, func(string) {})
	start := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	m.ensureLlm(start)
	m.ensureLlm(start.Add(time.Second))
	if probes != 1 {
		t.Fatalf("probed %d times within one interval, want once", probes)
	}
	m.ensureLlm(start.Add(llmCheckEvery))
	if probes != 2 {
		t.Fatalf("probed %d times after the interval, want twice", probes)
	}
}

// TestDoctorNamesTheModelEgressRow: the row reaches the report, its detail line prints, and a listener that
// answers nothing is a stopped row - so `doctor` exits 3 with the unserved surface named, rather than a
// healthy room above seats whose model calls all fail closed.
func TestDoctorNamesTheModelEgressRow(t *testing.T) {
	f := newHealthFixture(t, []string{"demo"})
	f.everythingRunning()
	llmRowsFor = func(string) []llmRow {
		return []llmRow{{
			Row:     clientservices.Health{Safeguard: clientservices.Llm, State: clientservices.Stopped, AgeS: clientservices.UnknownAge},
			Detail:  "llm: 1 proxied route, 3 unproxied (omp-deepseek, codex-unselected, agy), 2 withheld today",
			Missing: []string{"ANTHROPIC_BASE_URL"},
		}}
	}

	code, out, errOut := doctor(t, f)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d with a stopped listener, want %d", code, clientcmd.ExitUnavailable)
	}
	for _, want := range []string{"llm: 1 proxied route", "ANTHROPIC_BASE_URL: nothing answers on its port"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not print %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "llm: stopped") {
		t.Errorf("the refusal does not name the stopped row:\n%s", errOut)
	}
}
