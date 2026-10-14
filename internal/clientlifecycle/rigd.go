// rigd, the always-on local client: it streams every local session to the cloud, and it is the SINGLE
// RESTART AUTHORITY for the safeguards (rigd-boundaries.md 7.1).
//
// WHY SUPERVISION LIVES IN THIS PROCESS AND NOT IN A SECOND ONE. A separate keeper would be a second
// always-on process that itself needs supervising - two mechanisms where the owner ruled one. So rigd
// starts `compact-run --rig R`, `watchdog R --watch`, `sync messages --rig R --watch` and `bridge run`
// as its own children, through the EXISTING spawnChild/awaitStart pair the supervisor and the bridge
// already use, and restarts them with backoff from 1 s doubling to 120 s. A child that dies three times
// inside ten minutes is `failing`, and rigd keeps retrying it at the cap.
//
// WHAT CHANGED AROUND IT, so the reader does not go looking for the old mechanism: `up` starts rigd (one
// detached child, checked with awaitStart) and records the rig in rigd.json's `rigs` list; `compact` and
// `stop` no longer spawn or kill a safeguard directly, and `stop` stops rigd, which stops its children.
package clientlifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

const rigdUsage = "usage: 4genteam rigd [status [--json]]"

// 7.1's restart curve and the window that makes a crash loop visible.
const (
	// rigdRestartStart is the first retry delay: 1 s, doubling.
	rigdRestartStart = time.Second
	// rigdRestartCap is where the doubling stops, and where a `failing` child keeps retrying.
	rigdRestartCap = 120 * time.Second
	// rigdFailingWindow and rigdFailingDeaths are 7.1's "dies 3 times inside 10 minutes is failing".
	rigdFailingWindow = 10 * time.Minute
	rigdFailingDeaths = 3
	// rigdPass is one pass of the supervision loop. It is short because a pass is one file read plus one
	// pgrep per child.
	rigdPass = time.Second
)

// RigdLogPath is rigd's own log, where `up` sends its output.
func RigdLogPath() (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rigd.log"), nil
}

// WatchdogLogPath is the log of the watchdog CHILD rigd starts. It is deliberately NOT the watchdog's own
// ~/.config/4genthub/logs/rig-watchdog.log: that file is written by the watchdog itself through its tee, and
// pointing its stdout at the same file would append every line twice.
func WatchdogLogPath(rig string) (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "watchdog-"+rig+".log"), nil
}

type rigdCommand struct{}

func (rigdCommand) Name() string { return "rigd" }
func (rigdCommand) Summary() string {
	return "the always-on client: stream local sessions, supervise the safeguards"
}

func (rigdCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, rigdUsage,
			"One outbound /ws/connector socket: every local rig session registered, its transcript tailed and\n"+
				"uploaded redacted, and every safeguard transition published. It also starts and restarts\n"+
				"`compact-run --rig R`, `watchdog R --watch`, `sync messages --rig R --watch` and `bridge run`\n"+
				"for the rigs listed in rigd.json,\n"+
				"so it is the single restart authority: `stop` stops rigd and its children, and `up` starts it.\n\n"+
				"  status [--json]      the safeguard rows and the per-session withheld counts\n\n"+
				"Requires AGENTHUB_URL and AGENTHUB_TOKEN (the value is never printed).")
		return clientcmd.ExitOK
	}
	if len(args) > 0 && args[0] == "status" {
		return runRigdStatus(args[1:], stdout, stderr)
	}
	if len(args) > 0 {
		fmt.Fprintf(stderr, "4genteam rigd: unknown argument %s. %s\n", args[0], rigdUsage)
		return clientcmd.ExitUsage
	}
	return runRigd(ctx, stdout, stderr)
}

// runRigd is the process: the supervision loop in the background, the socket loop in the foreground.
func runRigd(ctx context.Context, stdout, stderr io.Writer) int {
	if missing := missingCloudEnv(); len(missing) > 0 {
		return refuseMissingCloudEnv("rigd", missing, stderr)
	}
	logPath, err := RigdLogPath()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam rigd: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "4genteam rigd: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	manager := newRigdManager(func(line string) {
		fmt.Fprintf(stdout, "%s rigd: %s\n", time.Now().UTC().Format(time.RFC3339), line)
	})
	daemon := clientsync.NewRigDaemon(clientsync.RigDaemonOptions{
		BaseURL:     os.Getenv("AGENTHUB_URL"),
		Token:       os.Getenv("AGENTHUB_TOKEN"),
		ConnectorID: connectorIdentity(),
		Log:         manager.log,
		Rows:        manager.rows,
		Pending:     manager.take,
	})

	// SIGTERM and SIGINT both stop the children before this process exits: the children are detached
	// (their own session), so they would otherwise outlive the process that supervises them and the next
	// rigd would find orphans. `stop` sends exactly this signal.
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var supervise sync.WaitGroup
	supervise.Add(1)
	go func() {
		defer supervise.Done()
		manager.supervise(ctx)
	}()

	code := daemon.Run(ctx)
	// The supervision loop must be stopped BEFORE waiting for it, so the cancel is explicit here rather
	// than deferred to the return.
	cancel()
	manager.shutdown()
	supervise.Wait()
	return code
}

// connectorIdentity is the connector id this machine registers under: its hostname, the same answer the
// one-shot `sync connector` gives, so a machine's sessions do not split across two identities.
func connectorIdentity() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "4genteam"
	}
	return host
}

// rigdSpec is one child rigd must have running.
type rigdSpec struct {
	safeguard string
	rig       string
	argv      []string
	logPath   string
	// threshold is 7.4's silence threshold, derived from the flags rigd launches this child with - not
	// configured anywhere, and not read from the environment.
	threshold time.Duration
}

// rigdChild is what rigd knows about one child: how it starts, when it last started, and how it has been
// dying.
type rigdChild struct {
	spec rigdSpec

	pid       int
	startedAt time.Time
	restarts  int
	backoff   time.Duration
	nextTry   time.Time
	deaths    []time.Time
	failing   bool
	starts    int
}

func (c *rigdChild) key() string { return c.spec.safeguard + " " + c.spec.rig }

// rigdManager is the supervision half of rigd. Its two mutex-guarded maps are the only shared state: the
// children it owns, and the transitions the socket half has not sent yet.
type rigdManager struct {
	log func(string)

	mu       sync.Mutex
	children map[string]*rigdChild
	pending  map[string]clientservices.Health
	last     []clientservices.Health
}

func newRigdManager(log func(string)) *rigdManager {
	return &rigdManager{
		log:      log,
		children: map[string]*rigdChild{},
		pending:  map[string]clientservices.Health{},
	}
}

// supervise is the loop: one pass a second, re-reading rigd.json each time so a room `up` adds while rigd
// runs is picked up without restarting anything.
func (m *rigdManager) supervise(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		m.pass()
		select {
		case <-ctx.Done():
			return
		case <-time.After(rigdPass):
		}
	}
}

// pass is one turn: bring the children in line with the listed rigs, then report what changed.
func (m *rigdManager) pass() {
	// RIGD BEATS ITSELF ON EVERY PASS, and it does so HERE rather than on the socket, because the process
	// must read `running` while the cloud is unreachable: a beat that needs the network would make an
	// offline-but-healthy rigd look dead, and 7.3 has a separate dead-man for the socket.
	if err := clientservices.WriteHeartbeat(clientservices.Rigd, ""); err != nil {
		m.log(fmt.Sprintf("cannot write rigd's own heartbeat: %v", err))
	}
	state := clientservices.ReadRigdState()
	specs, err := rigdSpecs(state.Rigs)
	if err != nil {
		m.log(fmt.Sprintf("cannot resolve the log directory, so no child can be started: %v", err))
		return
	}
	now := clientservices.Now()
	m.reconcile(specs, now)
	rows := clientservices.Verify(context.Background(), clientservices.Env{
		Rigs:       state.Rigs,
		Supervised: m.recorded(),
		Thresholds: m.thresholds(),
	})
	m.report(rows)
	m.persist(rows)
}

// rigdSpecs is the child list of 7.1: a compaction supervisor, a watchdog and a seat-message deliverer
// per listed rig, and ONE bridge per machine.
func rigdSpecs(rigs []string) ([]rigdSpec, error) {
	var specs []rigdSpec
	for _, rig := range rigs {
		supervisorLog, err := LogPath(rig)
		if err != nil {
			return nil, err
		}
		watchdogLog, err := WatchdogLogPath(rig)
		if err != nil {
			return nil, err
		}
		messagesLog, err := MessagesLogPath(rig)
		if err != nil {
			return nil, err
		}
		specs = append(specs,
			rigdSpec{
				safeguard: clientservices.Compact, rig: rig,
				argv:      []string{"compact-run", "--rig", rig},
				logPath:   supervisorLog,
				threshold: clientservices.SilentAfter(clientservices.CompactEvery),
			},
			rigdSpec{
				safeguard: clientservices.Watchdog, rig: rig,
				argv:      []string{"watchdog", rig, "--watch"},
				logPath:   watchdogLog,
				threshold: clientservices.SilentAfter(clientservices.WatchdogInterval),
			},
			rigdSpec{
				safeguard: clientservices.Messages, rig: rig,
				argv:      []string{"sync", "messages", "--rig", rig, "--watch"},
				logPath:   messagesLog,
				threshold: clientservices.SilentAfter(clientservices.MessagesEvery),
			},
		)
	}
	bridgeLog, err := BridgeLogPath()
	if err != nil {
		return nil, err
	}
	specs = append(specs, rigdSpec{
		safeguard: clientservices.Bridge,
		argv:      []string{"bridge", "run"},
		logPath:   bridgeLog,
		threshold: clientservices.SilentAfter(clientservices.BridgeBackoffCap),
	})
	return specs, nil
}

// reconcile starts what is missing, adopts what is already running, and stops what a rig that left the
// list no longer needs.
func (m *rigdManager) reconcile(specs []rigdSpec, now time.Time) {
	wanted := map[string]bool{}
	for _, spec := range specs {
		wanted[spec.safeguard+" "+spec.rig] = true
		m.ensure(spec, now)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, child := range m.children {
		if wanted[key] {
			continue
		}
		if child.pid > 0 {
			if err := signalPID(child.pid, syscall.SIGTERM); err == nil {
				m.log(fmt.Sprintf("%s: stopped, its rig left rigd.json's list", key))
			}
		}
		delete(m.children, key)
	}
}

// ensure is one child's turn: adopt a live process, restart a dead one when its backoff has elapsed.
//
// ADOPTION IS NOT A NICETY. The children are detached processes, so one can outlive a rigd that was
// SIGKILLed; spawning a second copy would put two supervisors on one rig. A live process matching the
// child's command line is therefore adopted rather than duplicated, and liveness is read from the SAME
// pgrep the report uses, so rigd and `doctor` can never disagree about who is running.
func (m *rigdManager) ensure(spec rigdSpec, now time.Time) {
	key := spec.safeguard + " " + spec.rig
	m.mu.Lock()
	child := m.children[key]
	if child == nil {
		child = &rigdChild{spec: spec, nextTry: now}
		m.children[key] = child
	}
	live := clientservices.Live(clientservices.Pattern(spec.safeguard, spec.rig))
	if len(live) > 0 {
		if child.pid != live[0] {
			child.pid, child.startedAt = live[0], now
			child.backoff, child.nextTry = 0, time.Time{}
		}
		child.prune(now)
		m.mu.Unlock()
		return
	}
	// Not running. A child rigd believed was alive has just died: that is the death, counted once, on the
	// pass that misses it.
	if child.pid > 0 {
		child.died(now)
		m.log(fmt.Sprintf("%s: it is no longer running", key))
	}
	if now.Before(child.nextTry) {
		m.mu.Unlock()
		return
	}
	child.nextTry = now.Add(rigdRestartCap)
	m.mu.Unlock()
	m.start(child, now)
}

// start spawns one child and waits out the start grace through awaitStart, the same start every other verb
// uses: a child that exits at once is a failed start and not a running safeguard.
//
// THE ATTEMPT is what `restarts` counts, not the survival: a child in a crash loop was started again every
// time rigd tried, and a reader watching `restarts` go up is watching exactly that.
func (m *rigdManager) start(child *rigdChild, now time.Time) {
	spawned, err := spawnChild(child.spec.argv, child.spec.logPath)
	m.mu.Lock()
	if child.starts > 0 {
		child.restarts++
	}
	child.starts++
	m.mu.Unlock()
	if err != nil {
		m.log(fmt.Sprintf("%s: could not start it: %v", child.key(), err))
		m.mu.Lock()
		child.died(now)
		m.mu.Unlock()
		return
	}
	survived, err := awaitStart(spawned)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !survived {
		m.log(fmt.Sprintf("%s: it exited immediately (status %d); see %s", child.key(), exitStatus(err), child.spec.logPath))
		child.died(now)
		return
	}
	child.pid, child.startedAt = spawned.Pid(), now
	child.backoff, child.nextTry = 0, time.Time{}
	if child.failing {
		m.log(fmt.Sprintf("%s: started again, pid %d, but it is FAILING (%d deaths inside %s); it will keep being retried every %s",
			child.key(), child.pid, len(child.deaths), rigdFailingWindow, rigdRestartCap))
		return
	}
	m.log(fmt.Sprintf("%s: started, pid %d (log %s)", child.key(), child.pid, child.spec.logPath))
}

// died records one death and schedules the retry: backoff from 1 s doubling to 120 s, and `failing` once
// three deaths fall inside ten minutes.
func (c *rigdChild) died(now time.Time) {
	c.deaths = append(c.deaths, now)
	c.prune(now)
	switch {
	case len(c.deaths) >= rigdFailingDeaths:
		c.failing = true
		c.nextTry = now.Add(rigdRestartCap)
	case c.backoff == 0:
		c.backoff = rigdRestartStart
		c.nextTry = now.Add(c.backoff)
	default:
		c.backoff *= 2
		if c.backoff > rigdRestartCap {
			c.backoff = rigdRestartCap
		}
		c.nextTry = now.Add(c.backoff)
	}
	c.pid = 0
}

// prune drops the deaths that have left the window, so a child that ran healthily for ten minutes is no
// longer failing.
func (c *rigdChild) prune(now time.Time) {
	kept := c.deaths[:0]
	for _, at := range c.deaths {
		if now.Sub(at) <= rigdFailingWindow {
			kept = append(kept, at)
		}
	}
	c.deaths = kept
	if len(c.deaths) < rigdFailingDeaths {
		c.failing = false
	}
}

// recorded is what rigd must persist for a reader: the fields pgrep and a heartbeat CANNOT answer -
// restarts, started_at, and the failing state a restart loop decides (7.2).
func (m *rigdManager) recorded() []clientservices.Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := make([]clientservices.Health, 0, len(m.children))
	for _, child := range m.children {
		row := clientservices.Health{
			Rig:       child.spec.rig,
			Safeguard: child.spec.safeguard,
			Restarts:  child.restarts,
		}
		if !child.startedAt.IsZero() {
			row.StartedAt = child.startedAt.UTC().Format(time.RFC3339)
		}
		if child.failing {
			row.State = clientservices.Failing
		}
		rows = append(rows, row)
	}
	sortRows(rows)
	return rows
}

// thresholds is 7.4's per-child table, computed from the flags rigd launched each child with.
func (m *rigdManager) thresholds() map[string]time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]time.Duration, len(m.children))
	for _, child := range m.children {
		out[child.spec.safeguard] = child.spec.threshold
	}
	return out
}

// report publishes every row whose state, pid or restart count changed, and appends it to alerts.log. The
// alert log is written on EVERY transition rather than only when the cloud is unreachable: it is the local
// record, and 7.3's plain statement is that it reaches nobody until someone runs `doctor`.
//
// The first pass publishes every row: a state nobody has seen is a transition, and starting with silence
// would leave the very first report empty.
func (m *rigdManager) report(rows []clientservices.Health) {
	m.mu.Lock()
	previous := map[string]clientservices.Health{}
	for _, row := range m.last {
		previous[row.Safeguard+" "+row.Rig] = row
	}
	m.last = rows
	var changed []clientservices.Health
	for _, row := range rows {
		before, known := previous[row.Safeguard+" "+row.Rig]
		if known && before.State == row.State && before.Restarts == row.Restarts && before.PID == row.PID {
			continue
		}
		m.pending[row.Safeguard+" "+row.Rig] = row
		changed = append(changed, row)
	}
	m.mu.Unlock()
	for _, row := range changed {
		line := clientservices.Now().UTC().Format(time.RFC3339) + " " + clientservices.StateLine(row)
		if err := clientservices.AppendAlert(line); err != nil {
			m.log(fmt.Sprintf("cannot append to alerts.log: %v", err))
		}
		m.log(clientservices.StateLine(row))
	}
}

// persist rewrites rigd.json's `safeguards` list with what rigd knows now. The rigs list is re-read and
// kept: `up` may have added a room between this pass's read and this write, and dropping it would make up
// a liar.
func (m *rigdManager) persist(rows []clientservices.Health) {
	fresh := clientservices.ReadRigdState()
	recorded := m.recorded()
	merged := make([]clientservices.Health, 0, len(rows))
	for _, row := range rows {
		for _, known := range recorded {
			if known.Safeguard == row.Safeguard && known.Rig == row.Rig {
				row.Restarts, row.StartedAt = known.Restarts, known.StartedAt
				break
			}
		}
		merged = append(merged, row)
	}
	if sameRows(fresh.Safeguards, merged) {
		return
	}
	fresh.Safeguards = merged
	if err := clientservices.WriteRigdState(fresh); err != nil {
		m.log(fmt.Sprintf("cannot write rigd.json: %v", err))
	}
}

// sameRows compares what a reader would see, so an unchanged report does not rewrite a 0600 file every
// second.
func sameRows(before, after []clientservices.Health) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i] != after[i] {
			return false
		}
	}
	return true
}

// rows is the daemon's re-send source: every row, in report order.
func (m *rigdManager) rows() []clientservices.Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]clientservices.Health, len(m.last))
	copy(out, m.last)
	return out
}

// take drains the transitions the socket half has not sent yet. Latest state wins, so a row is replaced
// rather than queued twice - which is the frame's own contract (7.3).
func (m *rigdManager) take() []clientservices.Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	out := make([]clientservices.Health, 0, len(m.pending))
	for key, row := range m.pending {
		out = append(out, row)
		delete(m.pending, key)
	}
	sortRows(out)
	return out
}

// sortRows makes the report order stable: by rig, then by safeguard, so two runs of the same state print
// the same lines in the same order.
func sortRows(rows []clientservices.Health) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Rig != rows[j].Rig {
			return rows[i].Rig < rows[j].Rig
		}
		return rows[i].Safeguard < rows[j].Safeguard
	})
}

// shutdown stops every child, which is what `stop` means now: rigd stops, and rigd stops its children.
func (m *rigdManager) shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, child := range m.children {
		if child.pid <= 0 {
			continue
		}
		if err := signalPID(child.pid, syscall.SIGTERM); err != nil {
			m.log(fmt.Sprintf("%s: could not be stopped: %v", key, err))
			continue
		}
		m.log(fmt.Sprintf("%s: stopped with rigd", key))
		child.pid = 0
	}
}

// signalPID is one child's signal, as a seam: a test must assert WHAT would be signalled without
// signalling the machine's own processes - the same reason stopPattern is a variable.
var signalPID = func(pid int, signal syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}

// recordRig adds a room to rigd.json's list, which is what "meant to run" means: rigd supervises a
// supervisor and a watchdog for it from its next pass on.
func recordRig(rig string, stderr io.Writer) bool {
	state := clientservices.AddRig(clientservices.ReadRigdState(), rig)
	if err := clientservices.WriteRigdState(state); err != nil {
		fmt.Fprintf(stderr, "4genteam: could not record rig %s in rigd.json: %v\n", rig, err)
		return false
	}
	return true
}

// rigdRunning is the process half of rigd's own row, through the same pattern the report uses. It is a
// variable so a test drives a verb's decision without a process on this machine.
var rigdRunning = func() int {
	live := clientservices.Live(clientservices.Pattern(clientservices.Rigd, ""))
	if len(live) == 0 {
		return 0
	}
	return live[0]
}

// startRigd starts the supervision authority, once: a running rigd is LEFT ALONE, because `up` is the
// idempotent repair verb (7.5) and a second rigd would be two restart authorities.
func startRigd(stdout, stderr io.Writer) bool {
	if pid := rigdRunning(); pid != 0 {
		fmt.Fprintf(stdout, "rigd: already running, pid %d\n", pid)
		return true
	}
	logPath, err := RigdLogPath()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: %v\n", err)
		return false
	}
	child, err := spawnChild([]string{"rigd"}, logPath)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: could not start rigd: %v\n", err)
		return false
	}
	survived, err := awaitStart(child)
	if !survived {
		fmt.Fprintf(stderr, "4genteam: rigd exited immediately (status %d); its output is in %s\n",
			exitStatus(err), logPath)
		return false
	}
	fmt.Fprintf(stdout, "rigd: pid %d, log %s\n", child.Pid(), logPath)
	return true
}

// stopRigd stops the authority and, through its own shutdown, its children. The stop is machine-wide, so
// the rig name is named as staying listed rather than used to pick a target: `stop` is not room intent
// (7.9), and a verb that silently dropped its argument would be worse than one that says why it ignores it.
func stopRigd(rig string, stdout io.Writer) {
	if stopPattern(clientservices.Pattern(clientservices.Rigd, "")) {
		fmt.Fprintf(stdout, "rigd: stopped with its children (a machine-wide stop; rig %s stays listed)\n", rig)
		return
	}
	fmt.Fprintln(stdout, "rigd: was not running")
}

// runRigdStatus is `4genteam rigd status`: the safeguard rows, the listed rigs, and the per-session
// withheld counts 4.4 names. It is a reading surface and not a checker - `doctor` exits 3, this exits 0.
func runRigdStatus(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "-h", "--help":
			printHelp(stdout, "usage: 4genteam rigd status [--json]", "The safeguard rows and the per-session withheld counts.")
			return clientcmd.ExitOK
		default:
			fmt.Fprintf(stderr, "4genteam rigd status: unknown argument %s\n", arg)
			return clientcmd.ExitUsage
		}
	}
	state := clientservices.ReadRigdState()
	rows := rowsFor("")
	withheld := withheldCounts()
	if asJSON {
		body, err := json.MarshalIndent(map[string]any{
			"rigs":       state.Rigs,
			"safeguards": rows,
			"withheld":   withheld,
		}, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam rigd status: cannot print the rows: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintln(stdout, string(body))
		return clientcmd.ExitOK
	}
	fmt.Fprintf(stdout, "rigs: %s\n", strings.Join(state.Rigs, ", "))
	for _, row := range rows {
		fmt.Fprintln(stdout, clientservices.StateLine(row))
	}
	for _, entry := range withheld {
		fmt.Fprintf(stdout, "withheld %s: %d\n", entry.session, entry.count)
	}
	return clientcmd.ExitOK
}

// withheldEntry is one session's count of raw events kept locally because they were withheld.
type withheldEntry struct {
	session string
	count   int
}

// resolveSeatStore is the store resolution as a seam, so a test counts withheld events in a temp store.
var resolveSeatStore = clientenv.ResolveSeatStore

// withheldCounts counts the raw withheld events per session, in the order the directories come back so a
// run is reproducible.
func withheldCounts() []withheldEntry {
	store, err := resolveSeatStore("")
	if err != nil {
		return nil
	}
	dir := filepath.Join(store, "rigd-withheld")
	sessions, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	counts := make([]withheldEntry, 0, len(sessions))
	for _, session := range sessions {
		if !session.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, session.Name()))
		if err != nil {
			continue
		}
		counts = append(counts, withheldEntry{session: session.Name(), count: len(files)})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].session < counts[j].session })
	return counts
}
