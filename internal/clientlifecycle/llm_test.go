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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientllmproxy"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
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
	llmListeners = func(routes []string) []clientllmproxy.Listener { return clientllmproxy.ListenersFor(routes) }
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

	llmMissing = func([]string) []string { return nil }
	rows := llmRowsFor("fixtureroom", t.TempDir())
	if len(rows) != 1 || rows[0].Row.State != clientservices.Running {
		t.Fatalf("rows = %+v, want one running row", rows)
	}
	if rows[0].Row.Safeguard != clientservices.Llm || rows[0].Row.AgeS != clientservices.UnknownAge {
		t.Errorf("the row is %+v, want the llm row with an unknown age", rows[0].Row)
	}
	if !strings.HasPrefix(rows[0].Detail, "llm: ") {
		t.Errorf("the detail line is %q, want it to open with the row's name", rows[0].Detail)
	}

	llmMissing = func([]string) []string { return []string{"ANTHROPIC_BASE_URL"} }
	rows = llmRowsFor("fixtureroom", t.TempDir())
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

	if err := ensureLlmListeners(root, clientllmproxy.Routes()); err != nil {
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

	err := ensureLlmListeners(root, clientllmproxy.Routes())
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

	err := ensureLlmListeners(t.TempDir(), clientllmproxy.Routes())
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
	m.ensureLlm(start, clientllmproxy.Routes())
	m.ensureLlm(start.Add(time.Second), clientllmproxy.Routes())
	if probes != 1 {
		t.Fatalf("probed %d times within one interval, want once", probes)
	}
	m.ensureLlm(start.Add(llmCheckEvery), clientllmproxy.Routes())
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
	llmRowsFor = func(string, string) []llmRow {
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

// TestOnlyTheRoutesTheRoomUsesAreStarted: a room is a set of RUNNERS, so `up` starts the listeners of the
// runtimes its seats run and nothing else. The fixture's start grace refuses, and only 7391 answers, so a
// listener this room does not use would be both spawned and refused - the 2026-10-22 failure, where codex's
// listener exited for a room that seats no codex.
func TestOnlyTheRoutesTheRoomUsesAreStarted(t *testing.T) {
	root := t.TempDir()
	spawned := stubLlmStart(t, map[int]bool{7391: true}, false)
	err := ensureLlmListeners(root, llmRoutes(clientruntime.RoomSpec{Seats: []clientruntime.SeatSpec{
		{SeatKey: "lead", Runtime: "omp"},
		{SeatKey: "go-dev", Runtime: "omp"},
	}}))
	if err != nil {
		t.Fatalf("an all-omp room refused its own start: %v", err)
	}
	for _, argv := range *spawned {
		if strings.Contains(strings.Join(argv, " "), "codex") {
			t.Fatalf("started a listener no seat uses: %v", argv)
		}
	}
}

// TestAClaudeSeatStartsWithTheClaudeListener: the runtime's own loose parse accepts `claude` for claude-code, so
// a room whose spec says `claude` starts claude-code's listener and only that one. Before this the alias fell
// through to the whole set and the room started codex's 7392 as well.
func TestAClaudeSeatStartsWithTheClaudeListener(t *testing.T) {
	root := t.TempDir()
	spawned := stubLlmStart(t, map[int]bool{}, true)
	err := ensureLlmListeners(root, llmRoutes(clientruntime.RoomSpec{Seats: []clientruntime.SeatSpec{
		{SeatKey: "principal", Runtime: "claude"},
	}}))
	if err != nil {
		t.Fatalf("a claude room refused its own start: %v", err)
	}
	if len(*spawned) != 1 {
		t.Fatalf("spawned %d listeners for one claude seat, want 1: %v", len(*spawned), *spawned)
	}
	if got := strings.Join((*spawned)[0], " "); !strings.Contains(got, "--runtime claude-code") || !strings.Contains(got, "--listen 127.0.0.1:7391") {
		t.Fatalf("the claude seat started %q, want claude-code on 127.0.0.1:7391", got)
	}
}

// TestARouteNoSeatUsesNeverBlocksUp: with no proxied route among the seats, nothing is started and nothing is
// waited on, so a route the table carries but no seat uses cannot hold the room down.
func TestARouteNoSeatUsesNeverBlocksUp(t *testing.T) {
	spawned := stubLlmStart(t, map[int]bool{}, false)
	err := ensureLlmListeners(t.TempDir(), llmRoutes(clientruntime.RoomSpec{Seats: []clientruntime.SeatSpec{
		{SeatKey: "lead", Runtime: "agy"},
	}}))
	if err != nil {
		t.Fatalf("a room with no proxied runtime refused: %v", err)
	}
	if len(*spawned) != 0 {
		t.Fatalf("spawned %v for a room that uses no proxied route", *spawned)
	}
}

// TestLlmRoutesAreTheSeatsOwn: each seat's runtime through the table's own figure, unioned - two seats of one
// runtime are one listener, a codex seat adds its own, and a runtime with no proxied route adds nothing.
func TestLlmRoutesAreTheSeatsOwn(t *testing.T) {
	spec := clientruntime.RoomSpec{Seats: []clientruntime.SeatSpec{
		{SeatKey: "lead", Runtime: "omp"},
		{SeatKey: "architect", Runtime: "omp"},
		{SeatKey: "fe-dev", Runtime: "agy"},
		{SeatKey: "reviewer", Runtime: "codex"},
	}}
	if got, want := strings.Join(llmRoutes(spec), ","), "omp-anthropic,codex"; got != want {
		t.Fatalf("llmRoutes = [%s], want [%s]", got, want)
	}
}

// TestLlmRoomRoutesReadsTheRoomsOwnRecord: agent and doctor hold only the room's name, so the routes come from
// the record its start wrote - and a record that cannot be read keeps EVERY route, because this figure decides
// that a listener may be left unstarted.
func TestLlmRoomRoutesReadsTheRoomsOwnRecord(t *testing.T) {
	root := t.TempDir()
	if got, want := strings.Join(llmRoomRoutes(root, "absent"), ","), strings.Join(clientllmproxy.Routes(), ","); got != want {
		t.Fatalf("an unreadable record answered [%s], want every route [%s]", got, want)
	}
	path := clientruntime.RoomSpecPath(root, "demo")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"room":"demo","seats":[{"seat_key":"lead","runtime":"codex"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(llmRoomRoutes(root, "demo"), ","), "codex"; got != want {
		t.Fatalf("the record's routes = [%s], want [%s]", got, want)
	}
}
