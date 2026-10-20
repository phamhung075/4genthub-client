// agent, the always-on local client: it streams every local session to the cloud, and it is the SINGLE
// RESTART AUTHORITY for the safeguards (agent-boundaries.md 7.1).
//
// WHY SUPERVISION LIVES IN THIS PROCESS AND NOT IN A SECOND ONE. A separate keeper would be a second
// always-on process that itself needs supervising - two mechanisms where the owner ruled one. So agent
// starts `compact-run --room R`, `watchdog R --watch`, `sync messages --room R --watch` and `bridge run`
// as its own children, through the EXISTING spawnChild/awaitStart pair the supervisor and the bridge
// already use, and restarts them with backoff from 1 s doubling to 120 s. A child that dies three times
// inside ten minutes is `failing`, and agent keeps retrying it at the cap.
//
// WHAT CHANGED AROUND IT, so the reader does not go looking for the old mechanism: `up` starts agent (one
// detached child, checked with awaitStart) and records the room in agent.json's `rooms` list; `compact` and
// `stop` no longer spawn or kill a safeguard directly, and `stop` stops agent, which stops its children.
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

const agentUsage = "usage: 4genteam agent [status [--json]] [--state-root DIR]"

// 7.1's restart curve and the window that makes a crash loop visible.
const (
	// agentRestartStart is the first retry delay: 1 s, doubling.
	agentRestartStart = time.Second
	// agentRestartCap is where the doubling stops, and where a `failing` child keeps retrying.
	agentRestartCap = 120 * time.Second
	// agentFailingWindow and agentFailingDeaths are 7.1's "dies 3 times inside 10 minutes is failing".
	agentFailingWindow = 10 * time.Minute
	agentFailingDeaths = 3
	// agentPass is one pass of the supervision loop. It is short because a pass is one file read plus one
	// pgrep per child.
	agentPass = time.Second
)

// AgentLogPath is agent's own log, where `up` sends its output, under the root its caller resolved.
func AgentLogPath(root string) string {
	return filepath.Join(LogDir(root), "agent.log")
}

// WatchdogLogPath is the log of the watchdog CHILD agent starts. It is deliberately NOT the watchdog's own
// <root>/logs/room-watchdog.log: that file is written by the watchdog itself through its tee, and
// pointing its stdout at the same file would append every line twice.
func WatchdogLogPath(room, root string) string {
	return filepath.Join(LogDir(root), "watchdog-"+room+".log")
}

type agentCommand struct{}

func (agentCommand) Name() string { return "agent" }
func (agentCommand) Summary() string {
	return "the always-on client: stream local sessions, supervise the safeguards"
}

func (agentCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, agentUsage,
			"One outbound /ws/connector socket: every local room session registered, its transcript tailed and\n"+
				"uploaded redacted, and every safeguard transition published. It also starts and restarts\n"+
				"`compact-run --room R`, `watchdog R --watch`, `sync messages --room R --watch` and `bridge run`\n"+
				"for the rooms listed in agent.json,\n"+
				"so it is the single restart authority: `stop` stops agent and its children, and `up` starts it.\n\n"+
				"  status [--json]      the safeguard rows and the per-session withheld counts\n\n"+
				"Requires AGENTHUB_URL and AGENTHUB_CONNECTOR_TOKEN, a token with the sessions:write scope\n"+
				"(the value is never printed).")
		return clientcmd.ExitOK
	}
	rootFlag, rest, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam agent: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUsage)
	}
	if len(rest) > 0 && rest[0] == "status" {
		root, err := clientenv.ResolveStateRoot(rootFlag)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam agent status: %v\n", err)
			return clientcmd.ExitUsage
		}
		return runAgentStatus(rest[1:], root, stdout, stderr)
	}
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "4genteam agent: unknown argument %s. %s\n", rest[0], agentUsage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam agent: %v\n", err)
		return clientcmd.ExitUsage
	}
	return runAgent(ctx, root, stdout, stderr)
}

// runAgent is the process: the supervision loop in the background, the socket loop in the foreground.
func runAgent(ctx context.Context, root string, stdout, stderr io.Writer) int {
	if missing := missingCloudEnv(); len(missing) > 0 {
		return refuseMissingCloudEnv("agent", missing, stderr)
	}
	logPath := AgentLogPath(root)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "4genteam agent: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	manager := newAgentManager(root, func(line string) {
		fmt.Fprintf(stdout, "%s agent: %s\n", time.Now().UTC().Format(time.RFC3339), line)
	})
	daemon := clientsync.NewAgentDaemon(clientsync.AgentDaemonOptions{
		BaseURL:     os.Getenv("AGENTHUB_URL"),
		Token:       os.Getenv("AGENTHUB_CONNECTOR_TOKEN"),
		ConnectorID: connectorIdentity(),
		Log:         manager.log,
		Rows:        manager.rows,
		Pending:     manager.take,
	})

	// SIGTERM and SIGINT both stop the children before this process exits: the children are detached
	// (their own session), so they would otherwise outlive the process that supervises them and the next
	// agent would find orphans. `stop` sends exactly this signal.
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var supervise sync.WaitGroup
	supervise.Add(1)
	// A PROBE THAT COULD NOT RUN ENDS THE SUPERVISION (7.12). The loop answers an error when pgrep could not
	// be read at all: no child was adopted, none was started, and the pass that would have decided either
	// from an empty process table never runs. The refusal is printed once, on stderr, and the daemon exits
	// 3 - the detached children it may already have keep running, and 7.3's dead-man is what reports it.
	refused := make(chan error, 1)
	go func() {
		defer supervise.Done()
		if err := manager.supervise(ctx); err != nil {
			fmt.Fprintf(stderr, "4genteam agent: refusing to supervise: %v\n", err)
			refused <- err
			cancel()
		}
	}()

	code := daemon.Run(ctx)
	// The supervision loop must be stopped BEFORE waiting for it, so the cancel is explicit here rather
	// than deferred to the return.
	cancel()
	manager.shutdown()
	supervise.Wait()
	if len(refused) > 0 {
		return clientcmd.ExitUnavailable
	}
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

// agentSpec is one child agent must have running.
type agentSpec struct {
	safeguard string
	room      string
	argv      []string
	logPath   string
	// threshold is 7.4's silence threshold, derived from the flags agent launches this child with - not
	// configured anywhere, and not read from the environment.
	threshold time.Duration
}

// agentChild is what agent knows about one child: how it starts, when it last started, and how it has been
// dying.
type agentChild struct {
	spec agentSpec

	pid       int
	startedAt time.Time
	restarts  int
	backoff   time.Duration
	nextTry   time.Time
	deaths    []time.Time
	failing   bool
	starts    int
}

func (c *agentChild) key() string { return c.spec.safeguard + " " + c.spec.room }

// agentManager is the supervision half of agent. Its two mutex-guarded maps are the only shared state: the
// children it owns, and the transitions the socket half has not sent yet.
type agentManager struct {
	// root is the state root this run resolved: every child it starts is handed it, and every file this
	// loop reads or writes lives under it.
	root string
	log  func(string)

	mu       sync.Mutex
	children map[string]*agentChild
	pending  map[string]clientservices.Health
	last     []clientservices.Health

	// lastOverlap and lastStuck are when the two detectors last ran: the zero value is "never", so the first
	// pass scans and a room `up` adds while agent runs is looked at on the next pass, not ten minutes later.
	lastOverlap time.Time
	lastStuck   time.Time
}

func newAgentManager(root string, log func(string)) *agentManager {
	return &agentManager{
		root:     root,
		log:      log,
		children: map[string]*agentChild{},
		pending:  map[string]clientservices.Health{},
	}
}

// supervise is the loop: one pass a second, re-reading agent.json each time so a room `up` adds while agent
// runs is picked up without restarting anything.
//
// It returns the pass's error and stops: a probe that could not run is not a pass to repeat, because
// repeating it would keep deciding "nothing is running" from a process table nobody read.
func (m *agentManager) supervise(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := m.pass(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(agentPass):
		}
	}
}

// pass is one turn: bring the children in line with the listed rooms, then report what changed.
func (m *agentManager) pass() error {
	// AGENT BEATS ITSELF ON EVERY PASS, and it does so HERE rather than on the socket, because the process
	// must read `running` while the cloud is unreachable: a beat that needs the network would make an
	// offline-but-healthy agent look dead, and 7.3 has a separate dead-man for the socket.
	if err := clientservices.WriteHeartbeat(m.root, clientservices.Agent, ""); err != nil {
		m.log(fmt.Sprintf("cannot write agent's own heartbeat: %v", err))
	}
	state, err := clientservices.ReadAgentState()
	if err != nil {
		m.log(fmt.Sprintf("the rooms meant to run could not be read, so no child is started this pass: %v", err))
	}
	specs := agentSpecs(state.Rooms, m.root)
	now := clientservices.Now()
	if err := m.reconcile(specs, now); err != nil {
		return err
	}
	// THE TWO DETECTORS RIDE THIS LOOP (ruling 4.5 and plan item 5: every ten minutes), on their own timers
	// rather than as more children, because 7.1's reason for keeping supervision in one process is that a
	// second always-on process is a second thing to supervise. Two timers rather than one because the two
	// read different things, and one being slow must not delay the other.
	if m.overlapDue(now) {
		m.scanOverlaps(state.Rooms, now)
	}
	if m.stuckDue(now) {
		m.scanStuck(state.Rooms, now)
	}
	rows, err := clientservices.Verify(context.Background(), clientservices.Env{
		Root:       m.root,
		Rooms:      state.Rooms,
		Supervised: m.recorded(),
		Thresholds: m.thresholds(),
	})
	if err != nil {
		return err
	}
	m.report(rows)
	m.persist(rows)
	return nil
}

// agentSpecs is the child list of 7.1: a compaction supervisor, a watchdog and a seat-message deliverer
// per listed room, and ONE bridge per machine.
func agentSpecs(rooms []string, root string) []agentSpec {
	var specs []agentSpec
	for _, room := range rooms {
		supervisorLog := LogPath(room, root)
		watchdogLog := WatchdogLogPath(room, root)
		messagesLog := MessagesLogPath(room, root)
		guardLog := IdleLogPath(room, root)
		budgetLog := BudgetLogPath(room, root)
		specs = append(specs,
			agentSpec{
				safeguard: clientservices.Compact, room: room,
				argv:      []string{"compact-run", "--room", room, "--state-root", root},
				logPath:   supervisorLog,
				threshold: clientservices.SilentAfter(clientservices.CompactEvery),
			},
			agentSpec{
				safeguard: clientservices.Watchdog, room: room,
				argv:      []string{"watchdog", room, "--watch", "--state-root", root},
				logPath:   watchdogLog,
				threshold: clientservices.SilentAfter(clientservices.WatchdogInterval),
			},
			agentSpec{
				safeguard: clientservices.Messages, room: room,
				argv:      []string{"sync", "messages", "--room", room, "--watch", "--state-root", root},
				logPath:   messagesLog,
				threshold: clientservices.SilentAfter(clientservices.MessagesEvery),
			},
			agentSpec{
				safeguard: clientservices.RoomGuard, room: room,
				argv:      []string{"idle-run", "--room", room, "--state-root", root},
				logPath:   guardLog,
				threshold: clientservices.SilentAfter(clientservices.RoomGuardEvery),
			},
			agentSpec{
				safeguard: clientservices.Budget, room: room,
				argv:      []string{"budget-run", "--room", room, "--state-root", root},
				logPath:   budgetLog,
				threshold: clientservices.SilentAfter(clientservices.BudgetEvery),
			},
		)
	}
	specs = append(specs, agentSpec{
		safeguard: clientservices.Bridge,
		argv:      []string{"bridge", "run", "--state-root", root},
		logPath:   BridgeLogPath(root),
		threshold: clientservices.SilentAfter(clientservices.BridgeBackoffCap),
	})
	return specs
}

// reconcile starts what is missing, adopts what is already running, and stops what a room that left the
// list no longer needs. The first probe error ends the pass: nothing is adopted, nothing is started, and
// nothing is stopped from a process table nobody read (7.12).
func (m *agentManager) reconcile(specs []agentSpec, now time.Time) error {
	wanted := map[string]bool{}
	for _, spec := range specs {
		wanted[spec.safeguard+" "+spec.room] = true
		if err := m.ensure(spec, now); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, child := range m.children {
		if wanted[key] {
			continue
		}
		if child.pid > 0 {
			if err := signalPID(child.pid, syscall.SIGTERM); err == nil {
				m.log(fmt.Sprintf("%s: stopped, its room left agent.json's list", key))
			}
		}
		delete(m.children, key)
	}
	return nil
}

// ensure is one child's turn: adopt a live process, restart a dead one when its backoff has elapsed.
//
// ADOPTION IS NOT A NICETY. The children are detached processes, so one can outlive an agent that was
// SIGKILLed; spawning a second copy would put two supervisors on one room. A live process matching the
// child's command line is therefore adopted rather than duplicated, and liveness is read from the SAME
// pgrep the report uses, so agent and `doctor` can never disagree about who is running.
//
// A PROBE ERROR IS NOT "NOT RUNNING" (7.12). It is returned before any decision is taken, so the child is
// neither adopted nor spawned and the caller ends the pass. The old spelling read the error as an empty
// process table, and one missing pgrep started a second supervision tree.
func (m *agentManager) ensure(spec agentSpec, now time.Time) error {
	key := spec.safeguard + " " + spec.room
	live, err := clientservices.Live(clientservices.Pattern(spec.safeguard, spec.room))
	if err != nil {
		return err
	}
	m.mu.Lock()
	child := m.children[key]
	if child == nil {
		child = &agentChild{spec: spec, nextTry: now}
		m.children[key] = child
	}
	if len(live) > 0 {
		if child.pid != live[0] {
			child.pid, child.startedAt = live[0], now
			child.backoff, child.nextTry = 0, time.Time{}
		}
		child.prune(now)
		m.mu.Unlock()
		return nil
	}
	// Not running. A child agent believed was alive has just died: that is the death, counted once, on the
	// pass that misses it.
	if child.pid > 0 {
		child.died(now)
		m.log(fmt.Sprintf("%s: it is no longer running", key))
	}
	if now.Before(child.nextTry) {
		m.mu.Unlock()
		return nil
	}
	child.nextTry = now.Add(agentRestartCap)
	m.mu.Unlock()
	m.start(child, now)
	return nil
}

// start spawns one child and waits out the start grace through awaitStart, the same start every other verb
// uses: a child that exits at once is a failed start and not a running safeguard.
//
// THE ATTEMPT is what `restarts` counts, not the survival: a child in a crash loop was started again every
// time agent tried, and a reader watching `restarts` go up is watching exactly that.
func (m *agentManager) start(child *agentChild, now time.Time) {
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
			child.key(), child.pid, len(child.deaths), agentFailingWindow, agentRestartCap))
		return
	}
	m.log(fmt.Sprintf("%s: started, pid %d (log %s)", child.key(), child.pid, child.spec.logPath))
}

// died records one death and schedules the retry: backoff from 1 s doubling to 120 s, and `failing` once
// three deaths fall inside ten minutes.
func (c *agentChild) died(now time.Time) {
	c.deaths = append(c.deaths, now)
	c.prune(now)
	switch {
	case len(c.deaths) >= agentFailingDeaths:
		c.failing = true
		c.nextTry = now.Add(agentRestartCap)
	case c.backoff == 0:
		c.backoff = agentRestartStart
		c.nextTry = now.Add(c.backoff)
	default:
		c.backoff *= 2
		if c.backoff > agentRestartCap {
			c.backoff = agentRestartCap
		}
		c.nextTry = now.Add(c.backoff)
	}
	c.pid = 0
}

// prune drops the deaths that have left the window, so a child that ran healthily for ten minutes is no
// longer failing.
func (c *agentChild) prune(now time.Time) {
	kept := c.deaths[:0]
	for _, at := range c.deaths {
		if now.Sub(at) <= agentFailingWindow {
			kept = append(kept, at)
		}
	}
	c.deaths = kept
	if len(c.deaths) < agentFailingDeaths {
		c.failing = false
	}
}

// recorded is what agent must persist for a reader: the fields pgrep and a heartbeat CANNOT answer -
// restarts, started_at, and the failing state a restart loop decides (7.2).
func (m *agentManager) recorded() []clientservices.Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := make([]clientservices.Health, 0, len(m.children))
	for _, child := range m.children {
		row := clientservices.Health{
			Room:      child.spec.room,
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

// thresholds is 7.4's per-child table, computed from the flags agent launched each child with.
func (m *agentManager) thresholds() map[string]time.Duration {
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
func (m *agentManager) report(rows []clientservices.Health) {
	m.mu.Lock()
	previous := map[string]clientservices.Health{}
	for _, row := range m.last {
		previous[row.Safeguard+" "+row.Room] = row
	}
	m.last = rows
	var changed []clientservices.Health
	for _, row := range rows {
		before, known := previous[row.Safeguard+" "+row.Room]
		if known && before.State == row.State && before.Restarts == row.Restarts && before.PID == row.PID {
			continue
		}
		m.pending[row.Safeguard+" "+row.Room] = row
		changed = append(changed, row)
	}
	m.mu.Unlock()
	for _, row := range changed {
		line := clientservices.Now().UTC().Format(time.RFC3339) + " " + clientservices.StateLine(row)
		if err := clientservices.AppendAlert(m.root, line); err != nil {
			m.log(fmt.Sprintf("cannot append to alerts.log: %v", err))
		}
		m.log(clientservices.StateLine(row))
	}
}

// persist rewrites agent.json's `safeguards` list with what agent knows now. The rooms list is re-read and
// kept: `up` may have added a room between this pass's read and this write, and dropping it would make up
// a liar.
func (m *agentManager) persist(rows []clientservices.Health) {
	fresh, err := clientservices.ReadAgentState()
	if err != nil {
		// NOTHING IS WRITTEN: this pass rewrites the rooms list from what it read, so persisting over a list
		// that could not be read would drop every room from the rooms meant to run. The next pass reads again.
		m.log(fmt.Sprintf("the rooms meant to run could not be read, so this pass kept no safeguard rows: %v", err))
		return
	}
	recorded := m.recorded()
	merged := make([]clientservices.Health, 0, len(rows))
	for _, row := range rows {
		for _, known := range recorded {
			if known.Safeguard == row.Safeguard && known.Room == row.Room {
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
	if err := clientservices.WriteAgentState(fresh); err != nil {
		m.log(fmt.Sprintf("cannot write agent.json: %v", err))
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
func (m *agentManager) rows() []clientservices.Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]clientservices.Health, len(m.last))
	copy(out, m.last)
	return out
}

// take drains the transitions the socket half has not sent yet. Latest state wins, so a row is replaced
// rather than queued twice - which is the frame's own contract (7.3).
func (m *agentManager) take() []clientservices.Health {
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

// sortRows makes the report order stable: by room, then by safeguard, so two runs of the same state print
// the same lines in the same order.
func sortRows(rows []clientservices.Health) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Room != rows[j].Room {
			return rows[i].Room < rows[j].Room
		}
		return rows[i].Safeguard < rows[j].Safeguard
	})
}

// shutdown stops every child, which is what `stop` means now: agent stops, and agent stops its children.
func (m *agentManager) shutdown() {
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
		m.log(fmt.Sprintf("%s: stopped with agent", key))
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

// recordRoom adds a room to agent.json's list, which is what "meant to run" means: agent supervises a
// supervisor and a watchdog for it from its next pass on.
func recordRoom(room string, stderr io.Writer) bool {
	state, err := clientservices.ReadAgentState()
	if err != nil {
		// NOT RECORDED, AND NOTHING WRITTEN: adding this room to a list that could not be read would record it
		// into the empty list and drop every OTHER room from the rooms meant to run.
		fmt.Fprintf(stderr, "4genteam: the rooms meant to run could not be read, so %s was NOT recorded in agent.json: %v\n", room, err)
		return false
	}
	state = clientservices.AddRoom(state, room)
	if err := clientservices.WriteAgentState(state); err != nil {
		fmt.Fprintf(stderr, "4genteam: could not record room %s in agent.json: %v\n", room, err)
		return false
	}
	return true
}

// agentRunning is the process half of agent's own row, through the same pattern the report uses. It is a
// variable so a test drives a verb's decision without a process on this machine.
//
// The error is the probe's, not an absence (7.12): a pgrep that could not run used to answer 0, and 0 was
// read as "agent is not running" by a verb that then started a second one.
var agentRunning = func() (int, error) {
	live, err := clientservices.Live(clientservices.Pattern(clientservices.Agent, ""))
	if err != nil {
		return 0, err
	}
	if len(live) == 0 {
		return 0, nil
	}
	return live[0], nil
}

// startAgent starts the supervision authority, once: a running agent is LEFT ALONE, because `up` is the
// idempotent repair verb (7.5) and a second agent would be two restart authorities.
func startAgent(root string, stdout, stderr io.Writer) bool {
	pid, err := agentRunning()
	if err != nil {
		// NOT STARTED ON A PROBE THE VERB COULD NOT TAKE (7.12): "cannot see agent" is not "agent is not
		// running", and the second is the only fact a second start may be built on.
		fmt.Fprintf(stderr, "4genteam up: nothing was started or stopped: %v\n", err)
		return false
	}
	if pid != 0 {
		fmt.Fprintf(stdout, "agent: already running, pid %d\n", pid)
		return true
	}
	logPath := AgentLogPath(root)
	child, err := spawnChild([]string{"agent", "--state-root", root}, logPath)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: could not start agent: %v\n", err)
		return false
	}
	survived, err := awaitStart(child)
	if !survived {
		fmt.Fprintf(stderr, "4genteam: agent exited immediately (status %d); its output is in %s\n",
			exitStatus(err), logPath)
		return false
	}
	fmt.Fprintf(stdout, "agent: pid %d, log %s\n", child.Pid(), logPath)
	return true
}

// stopAgent stops the authority and, through its own shutdown, its children. The stop is machine-wide, so
// the room name is named as staying listed rather than used to pick a target: `stop` is not room intent
// (7.9), and a verb that silently dropped its argument would be worse than one that says why it ignores it.
//
// IT ANSWERS THIS VERB'S EXIT CODE (7.12). A pkill that could not run is not "nothing matched": the caller
// gets 3 and the word NOT, never the `was not running` line that let `4genteam stop` exit 0 on a stop it
// never attempted. `verb` is the caller's spelling (`stop`, `up`), so the refusal names the command the
// operator ran.
func stopAgent(verb, room string, stdout, stderr io.Writer) int {
	stopped, err := stopPattern(clientservices.Pattern(clientservices.Agent, ""))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam %s: agent was NOT stopped: %v\n", verb, err)
		return clientcmd.ExitUnavailable
	}
	if !stopped {
		fmt.Fprintln(stdout, "agent: was not running")
		return clientcmd.ExitOK
	}
	fmt.Fprintf(stdout, "agent: stopped with its children (a machine-wide stop; room %s stays listed)\n", room)
	return clientcmd.ExitOK
}

// runAgentStatus is `4genteam agent status`: the safeguard rows, the listed rooms, and the per-session
// withheld counts 4.4 names. It is a reading surface and not a checker - `doctor` exits 3, this exits 0.
func runAgentStatus(args []string, root string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "-h", "--help":
			printHelp(stdout, "usage: 4genteam agent status [--json]", "The safeguard rows and the per-session withheld counts.")
			return clientcmd.ExitOK
		default:
			fmt.Fprintf(stderr, "4genteam agent status: unknown argument %s\n", arg)
			return clientcmd.ExitUsage
		}
	}
	state, stateErr := clientservices.ReadAgentState()
	if stateErr != nil {
		// The rows and the counts below are measured from pgrep and heartbeat files, so they still print; the
		// rooms list does not, and saying so is the difference between "no rooms are meant to run" and "the list
		// could not be read".
		fmt.Fprintf(stderr, "4genteam agent status: the rooms meant to run could not be read, so the rooms list below is empty for that reason: %v\n", stateErr)
	}
	rows, err := rowsFor("", root)
	if err != nil {
		// A READING VERB KEEPS ITS EXIT (7.12): the rows could not be decided, so none is printed and the
		// probe is named on stderr. The rooms list and the counts are unrelated readings and still print.
		fmt.Fprintf(stderr, "4genteam agent status: no safeguard row was decided: %v\n", err)
		fmt.Fprintf(stdout, "rooms: %s\n", strings.Join(state.Rooms, ", "))
		return clientcmd.ExitOK
	}
	withheld := withheldCounts()
	if asJSON {
		body, err := json.MarshalIndent(map[string]any{
			"rooms":      state.Rooms,
			"safeguards": rows,
			"withheld":   withheld,
		}, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam agent status: cannot print the rows: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintln(stdout, string(body))
		return clientcmd.ExitOK
	}
	fmt.Fprintf(stdout, "rooms: %s\n", strings.Join(state.Rooms, ", "))
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
	dir := filepath.Join(store, "agent-withheld")
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
