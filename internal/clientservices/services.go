// Package clientservices is the client's health report: ONE row per safeguard per room, computed from
// the heartbeat files and the process table, exactly as section 7.2 of
// ai_docs/core-architecture/agent-boundaries.md rules.
//
// WHY THE ROWS ARE COMPUTED HERE AND NOWHERE ELSE. `status --json` and `doctor` compute them
// THEMSELVES and do not read a report from agent, because a DEAD agent must still be reported - a report
// agent wrote about itself would be missing exactly when it is needed. So the computation is one
// function, Verify, and every reader is a caller of it.
//
// THE PRECEDENCE IS THE RULING'S, in this order:
//
//  1. PROCESS. A live process (pgrep, or agent's own Wait while it supervises) decides running against
//     stopped. A dead process is stopped whatever its heartbeat says.
//  2. HEARTBEAT. A live process whose heartbeat is older than its threshold (7.4), or whose heartbeat
//     pid is not the live pid, is silent.
//  3. LOG TIMESTAMPS ARE NEVER AUTHORITATIVE. The compaction supervisor logs only when it acts, so on a
//     quiet room a healthy supervisor writes nothing and its log age proves nothing.
//
// The package also owns the two file conventions the report stands on - the heartbeat files
// (<LogDir>/heartbeat/<safeguard>-<room>.json) and agent's local state file (agent.json) - and the log
// directory itself, so a child in any other package can write a beat without copying the XDG rule.
package clientservices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The safeguards of 7.2, in the order a report lists them.
const (
	Compact  = "compact"
	Watchdog = "watchdog"
	Messages = "messages"
	// RoomGuard is the idle-room guard: it asks Jev when every seat is idle and nudges a stuck seat.
	RoomGuard = "roomguard"
	// Budget is the room's budget guard: it parks a seat that crosses the room's own spend or call ceiling.
	Budget = "budget"
	Bridge = "bridge"
	Agent  = "agent"
	// OrphanHandoffs is 7.11's row about WORK rather than about a child: the pending handoffs whose
	// destination seat is not running. It carries no process, so its rows have no pid and no heartbeat.
	OrphanHandoffs = "orphan_handoffs"
	// Identity is `doctor`'s row about ATTRIBUTION rather than about a process: the nodes of a room whose
	// messages carry a sender identity, and the ones whose do not. Like OrphanHandoffs it carries no pid and
	// no heartbeat.
	Identity = "identity"
	// Seats is `doctor`'s row about the skills the seats of a room actually loaded. Like OrphanHandoffs it
	// is about WORK rather than about a child: there is no single process to pgrep and no heartbeat to age.
	Seats = "seats"
)

// The state the identity row adds to 7.2's. It is not a safeguard and never appears in Obligatory; it is the
// row's own verdict about a fact it read. `GuardAbsent`, the hooks row's state, sits beside `CloneDirty`
// below because the two machine rows are one family.
const (
	// IdentityUnset is a node the daemon cannot name, so every message it sends arrives as `<unknown sender>`.
	IdentityUnset = "identity_unset"
	// SkillsStale is the seats row's one failing state: a seat's loaded skills differ from the set the
	// runtime installed for it. It is deliberately NOT one of the four states below - it names a different
	// question - and NotRunning counts it, so a stale seat fails `doctor` the way a dead safeguard does.
	SkillsStale = "skills_stale"
)

// The four states of 7.2. There is no fifth, and `running` is the ONLY state doctor accepts.
const (
	Running = "running"
	Stopped = "stopped"
	Silent  = "silent"
	Failing = "failing"
	// CloneDirty is the clone row's one failing state: the shared checkout holds changes, and every one of
	// them belongs to no seat's worktree. It is a failure because a change nobody owns is a change nobody
	// will commit or review.
	CloneDirty = "clone_dirty"
	// GuardAbsent is the hooks row's one failing state: the checkout NAMES a hooks folder (core.hooksPath is
	// set) and no hook in it carries the git guard's marker. That is a switched-off guard, as opposed to a
	// guard that was never installed, which is a note and not a failure.
	GuardAbsent = "guard_absent"
)

// The longest legitimate sleep of each safeguard, from 7.4. 7.4's silence threshold is three times
// these, the same 3x the socket heartbeat uses, and agent derives each child's threshold from the flags
// it launched that child with - these are the values those flags default to.
const (
	// CompactEvery is the compaction supervisor's `--every`, 10 s.
	CompactEvery = 10 * time.Second
	// WatchdogInterval is the watchdog's watch interval, 60 s: agent launches `watchdog R --watch` with no
	// SECONDS, which is the documented default.
	WatchdogInterval = 60 * time.Second
	// MessagesEvery is the seat deliverer's pass interval, 10 s: one pass over every running seat of one
	// room, then one heartbeat, then this sleep - the cast of `sync messages --room R --watch`.
	MessagesEvery = 10 * time.Second
	// RoomGuardEvery is the idle guard's pass interval, 30 s.
	RoomGuardEvery = 30 * time.Second
	// BudgetEvery is the budget guard's pass interval, 15 s: one of its two ceilings is counted per minute,
	// so a pass that waited longer than a minute could not tell a burst from a habit.
	BudgetEvery = 15 * time.Second
	// BridgeBackoffCap is the bridge's own retry cap, 120 s (clientbridge.MaxBackoffSecs).
	BridgeBackoffCap = 120 * time.Second
	// AgentHeartbeat is the socket heartbeat agent answers to, 25 s (the server's ready.heartbeat_s).
	AgentHeartbeat = 25 * time.Second
)

// Safeguards is the list a report walks: the room-scoped rows first, then the two machine-scoped rows.
// One bridge per machine and one agent per machine, which is why those two carry no room.
func Safeguards() []string {
	return []string{Compact, Watchdog, Messages, RoomGuard, Budget, Bridge, Agent}
}

// RoomScoped reports whether a safeguard runs once per room rather than once per machine. It decides both
// the heartbeat file's name and whether the row carries a room at all.
func RoomScoped(safeguard string) bool {
	return safeguard == Compact || safeguard == Watchdog || safeguard == Messages || safeguard == RoomGuard ||
		safeguard == Budget
}

// Scope is WHERE a service runs: once per MACHINE, or once per ROOM. It is 7.8's second column, and
// RoomScoped answers the same question for the safeguard rows: the table below carries the answer as DATA,
// and a test holds the table and that predicate together so the heartbeat file's name cannot come from one
// answer while the row's room comes from the other.
type Scope string

const (
	// Machine is one service for the whole machine: one bridge, one agent.
	Machine Scope = "machine"
	// Room is one service per room: the compaction supervisor, the watchdog, the seat deliverer, the idle-room
	// guard and the budget guard.
	Room Scope = "rig"
)

// Service is one row of 7.8: a thing that must run, where it runs, how it starts, how it is checked, and
// what restarts it. Check returns the EXISTING Health row, and an error when the probe the row is decided
// from could not run at all (7.12) - a row is either read or refused, never defaulted to `stopped`.
// AllRunning, NotRunning and StateLine keep working untouched.
//
// Start is nil for a row whose start lives outside this package. The import direction is measured in the
// ruling: clientservices imports no lifecycle or restore package, so a start body can only be a
// package-level seam var that the command layer assigns, and a row with no seam yet carries nil.
//
// Check is the row's report. A scope-Room row's Check is handed an Env whose Rooms holds exactly the one room
// it is for (Env.forRoom); a scope-Machine row is handed the caller's env as it is and reads no room.
type Service struct {
	Name      string
	Scope     Scope
	Start     func(ctx context.Context, env Env) error
	Check     func(ctx context.Context, env Env) (Health, error)
	RestartBy string
}

// Obligatory is 7.8's table, in the report's own order: the room-scoped rows per room first, then the two
// machine-scoped rows. The order is data rather than a comment because Verify walks it: a table whose
// literal order drifts from the report's prints a report nobody asked for, so a test asserts the order.
var Obligatory = []Service{
	{Name: Compact, Scope: Room, Check: safeguardCheck(Compact), RestartBy: Agent},
	{Name: Watchdog, Scope: Room, Check: safeguardCheck(Watchdog), RestartBy: Agent},
	{Name: Messages, Scope: Room, Check: safeguardCheck(Messages), RestartBy: Agent},
	{Name: RoomGuard, Scope: Room, Check: safeguardCheck(RoomGuard), RestartBy: Agent},
	{Name: Budget, Scope: Room, Check: safeguardCheck(Budget), RestartBy: Agent},
	{Name: Bridge, Scope: Machine, Check: safeguardCheck(Bridge), RestartBy: Agent},
	// Nothing LOCAL restarts agent: `up` starts it, and its own death is reported rather than repaired.
	{Name: Agent, Scope: Machine, Check: safeguardCheck(Agent), RestartBy: ""},
}

// safeguardCheck binds one row's name to the precedence of 7.2, which is verifyRow: the table holds the
// check as a function, and the function is exactly the computation every reader already had. The room comes
// from the env the walk hands it (Env.forRoom), and a machine-scoped name reads none.
func safeguardCheck(name string) func(context.Context, Env) (Health, error) {
	return func(_ context.Context, env Env) (Health, error) {
		room := ""
		if RoomScoped(name) && len(env.Rooms) > 0 {
			room = env.Rooms[0]
		}
		return verifyRow(env, name, room)
	}
}

// SilentAfter is 7.4's rule in one line: silent means a heartbeat age past three times the child's
// longest legitimate sleep.
func SilentAfter(longestSleep time.Duration) time.Duration { return 3 * longestSleep }

// DefaultSilentAfter is 7.4's table - 30 s for the supervisor, 180 s for the watchdog, 30 s for the seat
// deliverer, 360 s for the bridge and 75 s for agent. `doctor` has no launch flags, so it uses these;
// agent computes each child's from the flags it launched it with, through SilentAfter.
func DefaultSilentAfter(safeguard string) time.Duration {
	switch safeguard {
	case Compact:
		return SilentAfter(CompactEvery)
	case Watchdog:
		return SilentAfter(WatchdogInterval)
	case Messages:
		return SilentAfter(MessagesEvery)
	case RoomGuard:
		return SilentAfter(RoomGuardEvery)
	case Budget:
		return SilentAfter(BudgetEvery)
	case Bridge:
		return SilentAfter(BridgeBackoffCap)
	case Agent:
		return SilentAfter(AgentHeartbeat)
	}
	return 0
}

// Pattern is the command line that identifies a safeguard's process, as `pgrep -f` takes it. The
// room-scoped patterns name the room, so a report for one room can never be answered by another room's
// child; the machine patterns are anchored to the start of the command line so a shell or an editor
// whose text merely contains them is not mistaken for the process.
func Pattern(safeguard, room string) string {
	switch safeguard {
	case Compact:
		return "compact-run --room " + room
	case Watchdog:
		return "^[^ ]*4genteam watchdog " + room + " --watch"
	case RoomGuard:
		return "^[^ ]*4genteam idle-run --room " + room
	case Budget:
		return "^[^ ]*4genteam budget-run --room " + room
	case Messages:
		return "^[^ ]*4genteam sync messages --room " + room + " --watch"
	case Bridge:
		return "^[^ ]*4genteam bridge run"
	case Agent:
		return "^[^ ]*4genteam agent( |$)"
	}
	return ""
}

// The seams the tests replace - package-level variables rather than an interface added for testing,
// which is the repo's rule. The clock is a seam because a report is a function of "now", and no test may
// read wall time; Live is a seam because a test cannot produce a process table.
var (
	// Now is the clock every age and every silent decision is measured against.
	Now = time.Now
	// Live returns the pids whose command line matches pattern, and an error when the probe itself could
	// not run: pgrep's own "nothing matched" is (nil, nil), and every other failure is the caller's to
	// refuse on (7.12).
	Live = livePIDs
	// LogDir is where the client's logs live: <root>/logs, for the root its caller RESOLVED once per run
	// (clientenv.ResolveStateRoot). It is the ONE computation of that path in the client, so the
	// supervisor's log, the bridge's log, the heartbeat files and alerts.log cannot land in two places -
	// and a verb pointed at another tree writes its logs there and nothing under this machine's.
	//
	// It is a seam so a test points it at a temp tree instead of the machine's own state directory.
	LogDir = func(root string) string { return filepath.Join(root, "logs") }
)

// HeartbeatDir is the directory every child writes its beat into.
func HeartbeatDir(root string) string {
	return filepath.Join(LogDir(root), "heartbeat")
}

// HeartbeatPath is one child's heartbeat file: <root>/logs/heartbeat/<safeguard>-<room>.json, and
// <safeguard>.json for the two machine-scoped safeguards - 7.2 names agent's own file `agent.json`.
func HeartbeatPath(root, safeguard, room string) string {
	name := safeguard
	if room != "" {
		name = safeguard + "-" + room
	}
	return filepath.Join(HeartbeatDir(root), name+".json")
}

// Heartbeat is what a child writes on every pass, and it is the WHOLE file: {pid, at}. Nothing else may
// be added here, because the row this file feeds has three sources and a third one would make them
// disagree (7.2).
type Heartbeat struct {
	PID int       `json:"pid"`
	At  time.Time `json:"at"`
}

// WriteHeartbeat stamps the caller's own process into its heartbeat file, under the root its caller
// resolved. It is called on EVERY pass of
// every child - the supervisor per Step, the watchdog per poll, the seat deliverer per pass, the bridge
// per Cycle, agent per tick - because a heartbeat that is written only when something happens cannot
// tell a stopped child from a quiet one, which is the incident this report exists for.
//
// It is a variable so ONE test seam covers every child and no test needs the machine's own log
// directory to observe a pass, in the repo's style of a package-level variable rather than an interface
// added for testing.
var WriteHeartbeat = writeHeartbeatFile

// writeHeartbeatFile is the write itself. It goes through a temporary file and a rename: a reader must
// never observe a half-written beat and read "no heartbeat" where there is one.
//
// 0600, not 0644: a heartbeat is state under the state root like any other file there, and an explicit mode is
// the one thing the process umask (`clientenv.RestrictUmask`) cannot clamp - 0644 here stayed 0644 whatever the
// umask said. Measured 2026-10-16: the guard heartbeats were world-readable with everything else.
func writeHeartbeatFile(root, safeguard, room string) error {
	path := HeartbeatPath(root, safeguard, room)
	body, err := json.Marshal(Heartbeat{PID: os.Getpid(), At: Now().UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
	}()
	if _, err := temp.Write(body); err != nil {
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// ReadHeartbeat reads a child's last beat. A missing or unreadable file answers false, which the report
// reads as "no beat": never as a fresh one - an unreadable beat must not look like a live child.
func ReadHeartbeat(root, safeguard, room string) (Heartbeat, bool) {
	path := HeartbeatPath(root, safeguard, room)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Heartbeat{}, false
	}
	var beat Heartbeat
	if err := json.Unmarshal(raw, &beat); err != nil {
		return Heartbeat{}, false
	}
	if beat.At.IsZero() {
		return Heartbeat{}, false
	}
	return beat, true
}

// livePIDs runs the one process lookup the report has: pgrep -f, whose pids are parsed rather than
// whose exit status is trusted (pgrep answers 1 for "none found", which is not a failure here).
//
// A PROBE THAT COULD NOT RUN IS NOT AN ABSENCE (7.12). pgrep exiting 1 observed the absence itself and
// answers (nil, nil); every other failure - no pgrep on PATH, a pgrep that refused the pattern - answers
// an error naming the probe, and the caller refuses rather than reporting "no processes". The two were
// one answer before, and `respawnDaemon` read it as "the daemon is not running" and started a second.
func livePIDs(pattern string) ([]int, error) {
	if pattern == "" {
		return nil, nil
	}
	path, err := exec.LookPath("pgrep")
	if err != nil {
		return nil, fmt.Errorf("pgrep: %w", err)
	}
	out, err := exec.Command(path, "-f", pattern).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("pgrep -f %q: %w", pattern, err)
	}
	var pids []int
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// Probe answers whether the process probe the client stands on can run at all: pgrep for every read of
// the process table and pkill for every stop. It is the one preflight `up` takes before it touches the
// daemon, agent or a seat, because every step after it needs one of the two - and a verb that cannot look
// must say so instead of acting on "nothing is running".
func Probe() error {
	for _, tool := range []string{"pgrep", "pkill"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s: %w", tool, err)
		}
	}
	return nil
}

// Health is ONE row of 7.2, and the field set is the ruling's exactly: room, safeguard, state, pid,
// started_at, restarts, last_beat_at, age_s. Nothing else is added, because a reader that has to know
// which of two sources a field came from cannot use the row.
//
// age_s is -1 when there is no heartbeat at all: an age that has no measurement is reported as unknown
// rather than as zero, which would read as a beat that just arrived.
type Health struct {
	Room       string `json:"room"`
	Safeguard  string `json:"safeguard"`
	State      string `json:"state"`
	PID        int    `json:"pid"`
	StartedAt  string `json:"started_at"`
	Restarts   int    `json:"restarts"`
	LastBeatAt string `json:"last_beat_at"`
	AgeS       int    `json:"age_s"`
}

// UnknownAge is the age_s of a row whose safeguard has no heartbeat file to measure.
const UnknownAge = -1

// Env is what a caller supplies to Verify: the rooms agent supervises, what agent recorded about them, and
// the silence thresholds to judge with. Everything else the report needs it reads itself - the heartbeat
// files, the process table and the clock - because those are the three sources the ruling names.
type Env struct {
	// Root is the state root the caller resolved: the heartbeat files are read under it and nowhere
	// else, so a reader pointed at another tree reports that tree.
	Root string
	// Rooms are the rooms of agent.json's `rooms` list: the rooms that are meant to run. A room not in the
	// list is stopped on purpose and has no report.
	Rooms []string
	// Supervised is agent's own record, from agent.json: the fields pgrep and a heartbeat CANNOT know -
	// restarts, started_at, and the failing state a restart loop decides. A row is matched by
	// (safeguard, room).
	Supervised []Health
	// Thresholds overrides 7.4's per-safeguard defaults. agent fills it from the flags it launched each
	// child with; `doctor`, which has no launch flags, leaves it empty and gets the defaults.
	Thresholds map[string]time.Duration
}

func (e Env) threshold(safeguard string) time.Duration {
	if value, ok := e.Thresholds[safeguard]; ok && value > 0 {
		return value
	}
	return DefaultSilentAfter(safeguard)
}

func (e Env) recorded(safeguard, room string) (Health, bool) {
	for _, row := range e.Supervised {
		if row.Safeguard == safeguard && row.Room == room {
			return row, true
		}
	}
	return Health{}, false
}

// Verify computes every row of the report. It is read-only in the strict sense 7.5 gives `doctor`: it
// starts nothing, restarts nothing and signals nothing. The context is accepted for the callers that
// will grow a bounded read; the computation itself touches only local files and pgrep.
// It walks Obligatory - the ONE list of what a report covers and in what order - so a service added to the
// table is reported without a second edit here. The states, the lines and the decisions are untouched; only
// which of the two lists the walk reads has changed.
//
// IT STOPS AT THE FIRST PROBE ERROR AND ANSWERS NO ROWS (7.12). A partial report is the failure this
// replaces: four rows decided from a probe that could not run, and every one of them reading `stopped`.
// A caller that cannot take a report refuses in words, naming the probe.
func Verify(ctx context.Context, env Env) ([]Health, error) {
	_ = ctx
	size := 0
	for _, service := range Obligatory {
		if service.Scope == Room {
			size += len(env.Rooms)
			continue
		}
		size++
	}
	rows := make([]Health, 0, size)
	// ROOM-MAJOR, not service-major: a reader comparing two rooms wants each room's rows together, so the
	// outer walk is the rooms and the inner walk is the table. The table's own order still decides where each
	// row sits inside a room, and where the two machine rows sit after every room's.
	for _, room := range env.Rooms {
		for _, service := range Obligatory {
			if service.Scope != Room {
				continue
			}
			row, err := service.Check(ctx, env.forRoom(room))
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	for _, service := range Obligatory {
		if service.Scope != Machine {
			continue
		}
		row, err := service.Check(ctx, env)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// forRoom is the env Verify hands a room-scoped row: the same env, carrying the one room that row is for. The
// slice is replaced on a COPY of the struct, so the caller's own env is untouched. This is the only channel
// by which a table row learns which rig it is reporting on, because Service.Check takes the env and nothing
// else.
func (e Env) forRoom(room string) Env {
	e.Rooms = []string{room}
	return e
}

// verifyRow is the precedence of 7.2, once, for one safeguard. A probe that could not run answers an
// error rather than a row: there is no verdict to report when the process reading it starts from is
// missing (7.12).
func verifyRow(env Env, safeguard, room string) (Health, error) {
	row := Health{Room: room, Safeguard: safeguard, AgeS: UnknownAge}
	if recorded, ok := env.recorded(safeguard, room); ok {
		row.StartedAt, row.Restarts = recorded.StartedAt, recorded.Restarts
	}
	beat, hasBeat := ReadHeartbeat(env.Root, safeguard, room)
	age := time.Duration(0)
	if hasBeat {
		age = Now().Sub(beat.At)
		row.LastBeatAt = beat.At.UTC().Format(time.RFC3339)
		row.AgeS = int(age.Seconds())
		row.PID = beat.PID
	}
	live, err := Live(Pattern(safeguard, room))
	if err != nil {
		return Health{}, err
	}
	// 7.2, as ruled 2026-10-11 (architect 3fa2125d): the precedence is stopped -> failing -> silent ->
	// running, and `failing` BEATS A LIVE PROCESS. agent counted the deaths inside its window, and a fresh
	// heartbeat does not undo a crash loop - so a child agent calls failing is reported failing here even
	// while it is momentarily alive, rather than reading `running` until the next death. The dead case
	// keeps its own branch below, where a dead-and-failing row still reads failing.
	if len(live) > 0 {
		if recorded, ok := env.recorded(safeguard, room); ok && recorded.State == Failing {
			row.State = Failing
			row.PID = live[0]
			return row, nil
		}
	}
	switch {
	case len(live) == 0:
		// The PROCESS decides, and a dead process is stopped whatever its heartbeat says. `failing` is
		// the one state only agent can decide: it counted the deaths.
		row.State = Stopped
		if recorded, ok := env.recorded(safeguard, room); ok && recorded.State == Failing {
			row.State = Failing
		}
	case !hasBeat:
		// Live, and this child has never written a beat: there is no signal, so this is the hung case
		// rather than a healthy one. 7.7's rule is explicit - a missing signal is not evidence of work.
		row.State = Silent
		row.PID = live[0]
	case !slices.Contains(live, beat.PID):
		row.State = Silent
		row.PID = live[0]
	case age > env.threshold(safeguard):
		row.State = Silent
		row.PID = beat.PID
	default:
		row.State = Running
		row.PID = beat.PID
	}
	return row, nil
}

// StateLine is one row as the operator reads it: `<room> <safeguard>: <state>, age <n>s`, which is the
// line 7.5 requires `doctor` to print on stderr for every row that is not running.
//
// The two machine-scoped safeguards carry no room, and the line then names the safeguard alone rather
// than opening with a space. A row with no heartbeat prints `age unknown` instead of `age 0s`, because
// zero is a measurement and this is the absence of one.
func StateLine(row Health) string {
	name := strings.TrimSpace(row.Room + " " + row.Safeguard)
	age := fmt.Sprintf("%ds", row.AgeS)
	if row.AgeS == UnknownAge {
		age = "unknown"
	}
	return fmt.Sprintf("%s: %s, age %s", name, row.State, age)
}

// AllRunning is the only condition under which `doctor` and `up` may answer ExitOK.
func AllRunning(rows []Health) bool {
	for _, row := range rows {
		if row.State != Running {
			return false
		}
	}
	return true
}

// NotRunning is every row that is not running, in report order: the rows `doctor` names on stderr and
// `up` refuses with.
func NotRunning(rows []Health) []Health {
	var down []Health
	for _, row := range rows {
		if row.State != Running {
			down = append(down, row)
		}
	}
	return down
}
