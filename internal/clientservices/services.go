// Package clientservices is the client's health report: ONE row per safeguard per rig, computed from
// the heartbeat files and the process table, exactly as section 7.2 of
// ai_docs/core-architecture/rigd-boundaries.md rules.
//
// WHY THE ROWS ARE COMPUTED HERE AND NOWHERE ELSE. `status --json` and `doctor` compute them
// THEMSELVES and do not read a report from rigd, because a DEAD rigd must still be reported - a report
// rigd wrote about itself would be missing exactly when it is needed. So the computation is one
// function, Verify, and every reader is a caller of it.
//
// THE PRECEDENCE IS THE RULING'S, in this order:
//
//  1. PROCESS. A live process (pgrep, or rigd's own Wait while it supervises) decides running against
//     stopped. A dead process is stopped whatever its heartbeat says.
//  2. HEARTBEAT. A live process whose heartbeat is older than its threshold (7.4), or whose heartbeat
//     pid is not the live pid, is silent.
//  3. LOG TIMESTAMPS ARE NEVER AUTHORITATIVE. The compaction supervisor logs only when it acts, so on a
//     quiet rig a healthy supervisor writes nothing and its log age proves nothing.
//
// The package also owns the two file conventions the report stands on - the heartbeat files
// (<LogDir>/heartbeat/<safeguard>-<rig>.json) and rigd's local state file (rigd.json) - and the log
// directory itself, so a child in any other package can write a beat without copying the XDG rule.
package clientservices

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// The four safeguards of 7.2, in the order a report lists them.
const (
	Compact  = "compact"
	Watchdog = "watchdog"
	Bridge   = "bridge"
	Rigd     = "rigd"
)

// The four states of 7.2. There is no fifth, and `running` is the ONLY state doctor accepts.
const (
	Running = "running"
	Stopped = "stopped"
	Silent  = "silent"
	Failing = "failing"
)

// The longest legitimate sleep of each safeguard, from 7.4. 7.4's silence threshold is three times
// these, the same 3x the socket heartbeat uses, and rigd derives each child's threshold from the flags
// it launched that child with - these are the values those flags default to.
const (
	// CompactEvery is the compaction supervisor's `--every`, 10 s.
	CompactEvery = 10 * time.Second
	// WatchdogInterval is the watchdog's watch interval, 60 s: rigd launches `watchdog R --watch` with no
	// SECONDS, which is the documented default.
	WatchdogInterval = 60 * time.Second
	// BridgeBackoffCap is the bridge's own retry cap, 120 s (clientbridge.MaxBackoffSecs).
	BridgeBackoffCap = 120 * time.Second
	// RigdHeartbeat is the socket heartbeat rigd answers to, 25 s (the server's ready.heartbeat_s).
	RigdHeartbeat = 25 * time.Second
)

// Safeguards is the list a report walks: the rig-scoped pair first, then the two machine-scoped rows.
// One bridge per machine and one rigd per machine, which is why those two carry no rig.
func Safeguards() []string { return []string{Compact, Watchdog, Bridge, Rigd} }

// RigScoped reports whether a safeguard runs once per rig rather than once per machine. It decides both
// the heartbeat file's name and whether the row carries a rig at all.
func RigScoped(safeguard string) bool { return safeguard == Compact || safeguard == Watchdog }

// SilentAfter is 7.4's rule in one line: silent means a heartbeat age past three times the child's
// longest legitimate sleep.
func SilentAfter(longestSleep time.Duration) time.Duration { return 3 * longestSleep }

// DefaultSilentAfter is 7.4's table: 30 s, 180 s, 360 s and 75 s. `doctor` has no launch flags, so it
// uses these; rigd computes each child's from the flags it launched it with, through SilentAfter.
func DefaultSilentAfter(safeguard string) time.Duration {
	switch safeguard {
	case Compact:
		return SilentAfter(CompactEvery)
	case Watchdog:
		return SilentAfter(WatchdogInterval)
	case Bridge:
		return SilentAfter(BridgeBackoffCap)
	case Rigd:
		return SilentAfter(RigdHeartbeat)
	}
	return 0
}

// Pattern is the command line that identifies a safeguard's process, as `pgrep -f` takes it. The two
// rig-scoped patterns name the rig, so a report for one rig can never be answered by another rig's
// child; the two machine patterns are anchored to the start of the command line so a shell or an editor
// whose text merely contains them is not mistaken for the process.
func Pattern(safeguard, rig string) string {
	switch safeguard {
	case Compact:
		return "compact-run --rig " + rig
	case Watchdog:
		return "^[^ ]*4genteam watchdog " + rig + " --watch"
	case Bridge:
		return "^[^ ]*4genteam bridge run"
	case Rigd:
		return "^[^ ]*4genteam rigd( |$)"
	}
	return ""
}

// The seams the tests replace - package-level variables rather than an interface added for testing,
// which is the repo's rule. The clock is a seam because a report is a function of "now", and no test may
// read wall time; Live is a seam because a test cannot produce a process table.
var (
	// Now is the clock every age and every silent decision is measured against.
	Now = time.Now
	// Live returns the pids whose command line matches pattern.
	Live = livePIDs
	// LogDir is where the client's logs live. It is a variable so a test writes heartbeats into a temp
	// tree instead of the machine's own state directory.
	LogDir = logDir
)

// logDir is where the lifecycle verbs keep their logs: $XDG_STATE_HOME/4genthub/logs, else
// <real home>/.local/state/4genthub/logs. It is the ONE computation of that path in the client, so the
// supervisor's log, the bridge's log, the heartbeat files and alerts.log cannot land in two places.
func logDir() (string, error) {
	home, err := clientenv.RealHome()
	if err != nil {
		return "", err
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "4genthub", "logs"), nil
}

// HeartbeatDir is the directory every child writes its beat into.
func HeartbeatDir() (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "heartbeat"), nil
}

// HeartbeatPath is one child's heartbeat file: <LogDir>/heartbeat/<safeguard>-<rig>.json, and
// <safeguard>.json for the two machine-scoped safeguards - 7.2 names rigd's own file `rigd.json`.
func HeartbeatPath(safeguard, rig string) (string, error) {
	dir, err := HeartbeatDir()
	if err != nil {
		return "", err
	}
	name := safeguard
	if rig != "" {
		name = safeguard + "-" + rig
	}
	return filepath.Join(dir, name+".json"), nil
}

// Heartbeat is what a child writes on every pass, and it is the WHOLE file: {pid, at}. Nothing else may
// be added here, because the row this file feeds has three sources and a third one would make them
// disagree (7.2).
type Heartbeat struct {
	PID int       `json:"pid"`
	At  time.Time `json:"at"`
}

// WriteHeartbeat stamps the caller's own process into its heartbeat file. It is called on EVERY pass of
// every child - the supervisor per Step, the watchdog per poll, the bridge per Cycle, rigd per tick -
// because a heartbeat that is written only when something happens cannot tell a stopped child from a
// quiet one, which is the incident this report exists for.
//
// It is a variable so ONE test seam covers all four children and no test needs the machine's own log
// directory to observe a pass, in the repo's style of a package-level variable rather than an interface
// added for testing.
var WriteHeartbeat = writeHeartbeatFile

// writeHeartbeatFile is the write itself. It goes through a temporary file and a rename: a reader must
// never observe a half-written beat and read "no heartbeat" where there is one.
func writeHeartbeatFile(safeguard, rig string) error {
	path, err := HeartbeatPath(safeguard, rig)
	if err != nil {
		return err
	}
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
	if err := temp.Chmod(0o644); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// ReadHeartbeat reads a child's last beat. A missing or unreadable file answers false, which the report
// reads as "no beat": never as a fresh one - an unreadable beat must not look like a live child.
func ReadHeartbeat(safeguard, rig string) (Heartbeat, bool) {
	path, err := HeartbeatPath(safeguard, rig)
	if err != nil {
		return Heartbeat{}, false
	}
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
func livePIDs(pattern string) []int {
	if pattern == "" {
		return nil
	}
	path, err := exec.LookPath("pgrep")
	if err != nil {
		return nil
	}
	out, _ := exec.Command(path, "-f", pattern).Output()
	var pids []int
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// Health is ONE row of 7.2, and the field set is the ruling's exactly: rig, safeguard, state, pid,
// started_at, restarts, last_beat_at, age_s. Nothing else is added, because a reader that has to know
// which of two sources a field came from cannot use the row.
//
// age_s is -1 when there is no heartbeat at all: an age that has no measurement is reported as unknown
// rather than as zero, which would read as a beat that just arrived.
type Health struct {
	Rig        string `json:"rig"`
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

// Env is what a caller supplies to Verify: the rigs rigd supervises, what rigd recorded about them, and
// the silence thresholds to judge with. Everything else the report needs it reads itself - the heartbeat
// files, the process table and the clock - because those are the three sources the ruling names.
type Env struct {
	// Rigs are the rigs of rigd.json's `rigs` list: the rooms that are meant to run. A rig not in the
	// list is stopped on purpose and has no report.
	Rigs []string
	// Supervised is rigd's own record, from rigd.json: the fields pgrep and a heartbeat CANNOT know -
	// restarts, started_at, and the failing state a restart loop decides. A row is matched by
	// (safeguard, rig).
	Supervised []Health
	// Thresholds overrides 7.4's per-safeguard defaults. rigd fills it from the flags it launched each
	// child with; `doctor`, which has no launch flags, leaves it empty and gets the defaults.
	Thresholds map[string]time.Duration
}

func (e Env) threshold(safeguard string) time.Duration {
	if value, ok := e.Thresholds[safeguard]; ok && value > 0 {
		return value
	}
	return DefaultSilentAfter(safeguard)
}

func (e Env) recorded(safeguard, rig string) (Health, bool) {
	for _, row := range e.Supervised {
		if row.Safeguard == safeguard && row.Rig == rig {
			return row, true
		}
	}
	return Health{}, false
}

// Verify computes every row of the report. It is read-only in the strict sense 7.5 gives `doctor`: it
// starts nothing, restarts nothing and signals nothing. The context is accepted for the callers that
// will grow a bounded read; the computation itself touches only local files and pgrep.
func Verify(ctx context.Context, env Env) []Health {
	_ = ctx
	rows := make([]Health, 0, len(env.Rigs)*2+2)
	for _, rig := range env.Rigs {
		rows = append(rows, verifyRow(env, Compact, rig))
		rows = append(rows, verifyRow(env, Watchdog, rig))
	}
	rows = append(rows, verifyRow(env, Bridge, ""), verifyRow(env, Rigd, ""))
	return rows
}

// verifyRow is the precedence of 7.2, once, for one safeguard.
func verifyRow(env Env, safeguard, rig string) Health {
	row := Health{Rig: rig, Safeguard: safeguard, AgeS: UnknownAge}
	if recorded, ok := env.recorded(safeguard, rig); ok {
		row.StartedAt, row.Restarts = recorded.StartedAt, recorded.Restarts
	}
	beat, hasBeat := ReadHeartbeat(safeguard, rig)
	age := time.Duration(0)
	if hasBeat {
		age = Now().Sub(beat.At)
		row.LastBeatAt = beat.At.UTC().Format(time.RFC3339)
		row.AgeS = int(age.Seconds())
		row.PID = beat.PID
	}
	live := Live(Pattern(safeguard, rig))
	// 7.2, as ruled 2026-10-11 (architect 3fa2125d): the precedence is stopped -> failing -> silent ->
	// running, and `failing` BEATS A LIVE PROCESS. rigd counted the deaths inside its window, and a fresh
	// heartbeat does not undo a crash loop - so a child rigd calls failing is reported failing here even
	// while it is momentarily alive, rather than reading `running` until the next death. The dead case
	// keeps its own branch below, where a dead-and-failing row still reads failing.
	if len(live) > 0 {
		if recorded, ok := env.recorded(safeguard, rig); ok && recorded.State == Failing {
			row.State = Failing
			row.PID = live[0]
			return row
		}
	}
	switch {
	case len(live) == 0:
		// The PROCESS decides, and a dead process is stopped whatever its heartbeat says. `failing` is
		// the one state only rigd can decide: it counted the deaths.
		row.State = Stopped
		if recorded, ok := env.recorded(safeguard, rig); ok && recorded.State == Failing {
			row.State = Failing
		}
	case !hasBeat:
		// Live, and this child has never written a beat: there is no signal, so this is the hung case
		// rather than a healthy one. 7.7's rule is explicit - a missing signal is not evidence of work.
		row.State = Silent
		row.PID = live[0]
	case beat.PID != live[0]:
		row.State = Silent
		row.PID = live[0]
	case age > env.threshold(safeguard):
		row.State = Silent
		row.PID = live[0]
	default:
		row.State = Running
		row.PID = live[0]
	}
	return row
}

// StateLine is one row as the operator reads it: `<rig> <safeguard>: <state>, age <n>s`, which is the
// line 7.5 requires `doctor` to print on stderr for every row that is not running.
//
// The two machine-scoped safeguards carry no rig, and the line then names the safeguard alone rather
// than opening with a space. A row with no heartbeat prints `age unknown` instead of `age 0s`, because
// zero is a measurement and this is the absence of one.
func StateLine(row Health) string {
	name := strings.TrimSpace(row.Rig + " " + row.Safeguard)
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
