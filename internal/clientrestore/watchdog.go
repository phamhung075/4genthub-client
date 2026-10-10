package clientrestore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// exitDead is the one-shot `<rig>` verdict: the daemon expects running seats and tmux has none, and the
// caller asked for a report rather than a restore. It shares clientcmd.ExitUsage's number while answering
// a different question - a usage error is a bad command line, this is a live-but-dead rig - so it is named
// here rather than borrowed, the same rule ExitRestoreFailed and ExitStartFailed follow.
const exitDead = 2

// watchdogServerIDName is the shell's `$HOME/.openrig/state/rig-watchdog.server-id`: the tmux server's
// pid + start time, so a changed server leaves a timestamped transition in the log instead of being
// inferred after the fact. A reused pid cannot masquerade as the same server because start_time differs.
const watchdogServerIDName = "rig-watchdog.server-id"

// watchdogUsage is what every refusal in this verb points at, in the same shape as continueUsage.
const watchdogUsage = "usage: 4genteam watchdog [RIG [--watch [SECONDS] [HEARTBEAT_POLLS]] | " +
	"--all --watch [SECONDS] | --check | --self-test] " + clientoffpeak.ModeFlagUsage

// watchdogListingUsage is the shell's own no-argument usage line, kept verbatim (with the binary's name
// spelled the way this client does) so a reader who knew the script sees the same forms.
const watchdogListingUsage = "Usage: 4genteam watchdog --check | <rig> [--watch [secs]] | " +
	"--all --watch [secs] | --self-test"

// The verdict's three answers, named so the watch loop and the report cannot drift over a string.
const (
	verdictStopped = "stopped"
	verdictHealthy = "healthy"
	verdictDead    = "dead"
)

// The two watch defaults, the shell's `${3:-60}` and the beat's `${4:-15}`.
const (
	defaultWatchInterval = 60
	defaultWatchBeat     = 15
)

// Seams the watchdog's tests replace, in the same package-variable style as the rest of this package.
var (
	// tmuxServerIdentity is the shell's `tmux display-message -p '#{pid} #{start_time}'`, with a missing
	// server answered as "none" rather than as an error - the crash case is exactly when there is no
	// server to ask.
	tmuxServerIdentity = func() string {
		out, err := exec.Command("tmux", "display-message", "-p", "#{pid} #{start_time}").Output()
		if err != nil {
			return "none"
		}
		if id := strings.TrimSpace(string(out)); id != "" {
			return id
		}
		return "none"
	}
	// watchdogNodesJSON is the ONE rig read in this verb: the documented JSON the shell piped through
	// python3 (`rig ps --nodes --rig RIG --json`). It is a seam so a test counts seats with no rig.
	watchdogNodesJSON = func(rig *clientcmd.Rig, rigName string) (string, error) {
		return rig.Run(context.Background(), "ps", "--nodes", "--rig", rigName, "--json")
	}
	// watchSleep is the loop's `sleep "$interval"`, a seam so a test drives polls instead of waiting.
	watchSleep = time.Sleep
	// watchPollLimit caps the otherwise-infinite watch loop for tests: 0 means "until the process stops",
	// which is what the shell did, and any positive number makes the loop return after that many polls.
	watchPollLimit = 0
)

// watchdogCommand is the shell's rig-watchdog.sh: notice when a rig's tmux seats have vanished and run the
// same restore `continue` runs.
type watchdogCommand struct{}

func (watchdogCommand) Name() string { return "watchdog" }
func (watchdogCommand) Summary() string {
	return "when a rig's seats vanish, put it back with the same restore continue uses"
}

// NeedsRig is false and the verb resolves the rig tool itself, because TWO of its forms do not need it:
// --self-test exercises the decision rule on fixtures, and the usage listing prints the briefs on disk -
// and a machine that has no rig tool is exactly where a reader wants to know which rigs have briefs. The
// forms that read the daemon refuse through the same one-message matrix every other verb uses.
func (watchdogCommand) NeedsRig() bool { return false }

func (watchdogCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, watchdogUsage,
			"Watches a rig's tmux sessions - the ground truth for \"is it up\"; the daemon's cached node\n"+
				"records said running while the server hosting ten seats was gone. When a rig that the\n"+
				"daemon expects to be running has no sessions, the restore continue runs is run for it.\n\n"+
				"  RIG                  one-shot report; exits 2 when the rig is dead\n"+
				"  RIG --watch [SECONDS] [HEARTBEAT_POLLS]\n"+
				"                       loop; two consecutive misses are required before acting\n"+
				"  --all --watch [SECONDS]\n"+
				"                       a watcher per rig that has a brief, concurrently\n"+
				"  --check              report every rig that has a brief; never restores\n"+
				"  --self-test          exercise the decision rule on fixtures\n"+
				"  no argument          list the rigs that have a brief\n\n"+
				"The DeepSeek peak gate runs BEFORE a restore, the same gate `up` and `continue` obey: with "+
				clientoffpeak.ModeEnv+"\n"+
				"on (the default) a room whose seats run a deepseek/ model is refused at peak, and "+
				clientoffpeak.FlagAllowPeak+"\n"+
				"skips the check for this run.")
		return clientcmd.ExitOK
	}
	allowPeak, args := clientoffpeak.ModeFromFlags(args)
	if len(args) == 0 {
		return watchdogListing(stdout, stderr)
	}
	switch args[0] {
	case "--self-test":
		return selfTest(stdout)
	case "--check":
		return watchdogCheck(allowPeak, stdout, stderr)
	case "--all":
		return watchdogAll(allowPeak, args[1:], stdout, stderr)
	}
	return watchdogOne(allowPeak, args, stdout, stderr)
}

// verdict is the decision rule, kept pure so --self-test can exercise it without a fleet. exp = seats the
// daemon believes are running; live = sessions tmux actually has. A rig the daemon does not expect (exp 0)
// is stopped or already handled and is never acted on; a partial loss is NOT this verb's job, because it
// restores whole rigs.
func verdict(expected, live int) string {
	switch {
	case expected == 0:
		return verdictStopped
	case live > 0:
		return verdictHealthy
	default:
		return verdictDead
	}
}

// liveSeats is the shell's live_seats(): the rig's sessions, straight from tmux, with a missing server
// read as zero rather than as an error.
func liveSeats(rigName string) int {
	return len(rigSessions(rigName, sessionNames()))
}

// expectedSeats is the shell's expected_seats(): the rig's running seats as the daemon reports them. The
// shell piped the JSON through python3; the parsing half is countRunningSeats and this half just runs the
// command. The command's own exit status is deliberately ignored, exactly as the shell's pipe left it: a
// `rig ps` that printed rows and then failed still had those rows counted.
func expectedSeats(rig *clientcmd.Rig, rigName string) int {
	out, _ := watchdogNodesJSON(rig, rigName)
	return countRunningSeats(out)
}

// countRunningSeats is the shell's one-liner python: rows whose lifecycleState is "running". Unreadable or
// malformed input counts as 0, which is what the python's `except Exception: print(0)` and the pipe's
// `2>/dev/null || echo 0` together produced.
func countRunningSeats(data string) int {
	var rows []struct {
		LifecycleState string `json:"lifecycleState"`
	}
	if err := json.Unmarshal([]byte(data), &rows); err != nil {
		return 0
	}
	running := 0
	for _, row := range rows {
		if row.LifecycleState == "running" {
			running++
		}
	}
	return running
}

// noteServerIdentity is the shell's note_server_identity(): read the server's pid + start time, compare it
// with the file, and log only when it changed - a first write says so, a later change says which server
// went away.
func noteServerIdentity(log *teeLog) {
	id := tmuxServerIdentity()
	dir, err := stateDir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, watchdogServerIDName)
	prev := "none"
	if data, err := os.ReadFile(path); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			prev = text
		}
	}
	if id == prev {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(id), 0o644); err != nil {
		return
	}
	if prev == "none" {
		log.printf("tmux server identity: %s (no change recorded before this poll)", id)
		return
	}
	log.printf("tmux SERVER CHANGED: %s -> %s (the previous server is gone)", prev, id)
}

// watcher is one rig's report-and-watch: the shell's check_rig() and watch_rig() with the seam fields a
// test needs. Its action IS the restorer's, bound here so the two verbs restore identically.
type watcher struct {
	rig       *clientcmd.Rig
	log       *teeLog
	allowPeak bool
	stderr    io.Writer
	logPath   string
	// The three steps a restore takes, bound from the restorer in newWatcher and replaced by tests.
	daemonUp func()
	gate     func(rigName, verb string, allowPeak bool, stderr io.Writer) int
	restore  func(rigName, brief string) bool
}

// newWatcher binds the restorer whose work this watcher drives.
func newWatcher(rig *clientcmd.Rig, restore *restorer, log *teeLog, allowPeak bool, stderr io.Writer) *watcher {
	path := ""
	if dir, err := logDir(); err == nil {
		path = filepath.Join(dir, watchdogLogName)
	}
	return &watcher{
		rig:       rig,
		log:       log,
		allowPeak: allowPeak,
		stderr:    stderr,
		logPath:   path,
		daemonUp:  restore.daemonUp,
		gate:      restore.gate,
		restore:   restore.restore,
	}
}

// check is the shell's check_rig(): one report pass over one rig, restoring when the caller asked for it
// (action == "restore") and only then.
func (w *watcher) check(rigName, action string) int {
	noteServerIdentity(w.log)
	exp := expectedSeats(w.rig, rigName)
	live := liveSeats(rigName)
	switch verdict(exp, live) {
	case verdictStopped:
		w.log.printf("%s: daemon reports no running seats (expected 0, live %d) — deliberate stop or already handled; no action", rigName, live)
		return clientcmd.ExitOK
	case verdictHealthy:
		w.log.printf("%s: healthy (daemon expects %d, tmux has %d)", rigName, exp, live)
		return clientcmd.ExitOK
	}
	if action != "restore" {
		w.log.printf("%s: DEAD — daemon expects %d running seats, tmux has 0. Re-run with --watch to restore.", rigName, exp)
		return exitDead
	}
	w.log.printf("%s: DEAD — daemon expects %d running seats, tmux has 0. Restoring via continue's restore.", rigName, exp)
	w.daemonUp()
	if code := w.gate(rigName, "watchdog", w.allowPeak, w.stderr); code != clientcmd.ExitOK {
		// The gate logged its own refusal; nothing is started, so there is nothing else to report.
		return clientcmd.ExitOK
	}
	brief, err := defaultBrief(rigName)
	if err != nil {
		brief = ""
	}
	if w.restore(rigName, brief) {
		w.log.printf("%s: restore invoked; tmux now has %d", rigName, liveSeats(rigName))
	} else {
		w.log.printf("%s: restore FAILED — see %s", rigName, w.logPath)
	}
	return clientcmd.ExitOK
}

// watch is the shell's watch_rig(): poll, require two consecutive misses before acting, and emit a
// heartbeat so a healthy rig is not silent - "watchdog died" and "all is well" must not look the same.
func (w *watcher) watch(rigName string, interval time.Duration, beat int) {
	seconds := int(interval / time.Second)
	w.log.printf("%s: watching every %ds (two consecutive misses required before acting; heartbeat every %ds)",
		rigName, seconds, beat*seconds)
	misses, polls := 0, 0
	for watchPollLimit == 0 || polls < watchPollLimit {
		noteServerIdentity(w.log)
		exp := expectedSeats(w.rig, rigName)
		live := liveSeats(rigName)
		if verdict(exp, live) == verdictDead {
			misses++
			w.log.printf("%s: MISS %d/2 (daemon expects %d, tmux has 0)", rigName, misses, exp)
			if misses >= 2 {
				w.check(rigName, "restore")
				misses = 0
			}
		} else {
			if misses > 0 {
				w.log.printf("%s: live again before acting (live=%d); counter reset", rigName, live)
			}
			misses = 0
		}
		polls++
		if beat > 0 && polls%beat == 0 {
			w.log.printf("%s: heartbeat — alive, daemon expects %d, tmux has %d, no misses", rigName, exp, live)
		}
		// Every poll beats, not only every `beat` polls: a stopped watchdog and a quiet rig must not
		// look the same, and the operator's log line above is not a heartbeat file. A failed write is
		// reported through the same log rather than swallowed, one line per poll that failed.
		if err := clientservices.WriteHeartbeat(clientservices.Watchdog, rigName); err != nil {
			w.log.printf("%s: heartbeat write failed: %v", rigName, err)
		}
		watchSleep(interval)
	}
}

// selfTest is the shell's --self-test: the six-case table, exit 0 when every case passes and 1 when any
// fails.
func selfTest(out io.Writer) int {
	cases := []struct {
		expected, live int
		want           string
	}{
		{10, 10, verdictHealthy}, // normal running rig
		{10, 0, verdictDead},     // the 2026-10-05 crash: daemon expects ten, tmux has none
		{1, 0, verdictDead},      // single-seat rig, same shape
		{3, 2, verdictHealthy},   // partial loss is NOT this verb's job (it restores whole rigs)
		{0, 0, verdictStopped},   // deliberately stopped rig must never trigger a restore
		{0, 10, verdictStopped},  // stale daemon rows but no expectation: no action
	}
	fmt.Fprintln(out, "decision rule:")
	pass, fail := 0, 0
	for _, c := range cases {
		got := verdict(c.expected, c.live)
		if got == c.want {
			pass++
			fmt.Fprintf(out, "  ok   verdict(%d,%d) = %s\n", c.expected, c.live, c.want)
			continue
		}
		fail++
		fmt.Fprintf(out, "  FAIL verdict(%d,%d) = %s, wanted %s\n", c.expected, c.live, got, c.want)
	}
	fmt.Fprintf(out, "result: %d passed, %d failed\n", pass, fail)
	if fail == 0 {
		return clientcmd.ExitOK
	}
	return 1
}

// watchdogListing is the shell's no-argument form: the usage line, then every rig that has a brief. It
// needs no rig tool, which is the point - a machine without one is where a reader wants the brief list.
func watchdogListing(stdout, stderr io.Writer) int {
	fmt.Fprintln(stdout, watchdogListingUsage)
	dir, err := briefDir()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam watchdog: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	for _, brief := range briefsOnDisk(dir) {
		fmt.Fprintf(stdout, "  rig with a brief: %s\n", strings.TrimSuffix(filepath.Base(brief), ".txt"))
	}
	return clientcmd.ExitOK
}

// openWatchdog is the shared head of every daemon-reading form: the log (which also resolves the home),
// the rig tool, and the restorer both the report and the watch will use. It is one message and
// ExitUnavailable when the home or the rig tool is missing, the same rule the rest of this package follows.
func openWatchdog(stdout, stderr io.Writer) (*teeLog, *clientcmd.Rig, *restorer, int) {
	log, err := openLog(watchdogLogName, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam watchdog: %v\n", err)
		return nil, nil, nil, clientcmd.ExitUnavailable
	}
	rig, err := clientcmd.RequireRig()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam watchdog: %v\n", err)
		log.close()
		return nil, nil, nil, clientcmd.ExitUnavailable
	}
	restore, err := newRestorer(rig, log)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam watchdog: %v\n", err)
		log.close()
		return nil, nil, nil, clientcmd.ExitUnavailable
	}
	return log, rig, restore, clientcmd.ExitOK
}

// watchdogCheck is the shell's --check: one report pass over every rig that has a brief, changing nothing.
//
// THE EXIT CODE IS THE AGGREGATE, and that is a deliberate correction of the shell: its `for … done` exited
// with the LAST rig's status, so a dead rig that was not last left the code at 0 - the one thing a caller
// that only reads the exit code would have acted on. "Any rig came back dead" is deterministic, does not
// depend on the order the briefs happen to be listed in, and carries the same signal the script's accident
// carried whenever the last rig was the dead one.
func watchdogCheck(allowPeak bool, stdout, stderr io.Writer) int {
	dir, err := briefDir()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam watchdog: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	log, rig, restore, code := openWatchdog(stdout, stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	defer log.close()
	w := newWatcher(rig, restore, log, allowPeak, stderr)
	dead := false
	for _, brief := range briefsOnDisk(dir) {
		if w.check(strings.TrimSuffix(filepath.Base(brief), ".txt"), "report") == exitDead {
			dead = true
		}
	}
	if dead {
		return exitDead
	}
	return clientcmd.ExitOK
}

// watchdogOne is the shell's two per-rig forms: a one-shot report, or a watch loop.
func watchdogOne(allowPeak bool, args []string, stdout, stderr io.Writer) int {
	rigName := args[0]
	if strings.HasPrefix(rigName, "-") {
		fmt.Fprintf(stderr, "4genteam watchdog: unknown argument %s. %s\n", rigName, watchdogUsage)
		return clientcmd.ExitUsage
	}
	watching := len(args) > 1 && args[1] == "--watch"
	if len(args) > 1 && !watching {
		fmt.Fprintf(stderr, "4genteam watchdog: unknown argument %s. %s\n", args[1], watchdogUsage)
		return clientcmd.ExitUsage
	}
	var interval time.Duration
	beat := defaultWatchBeat
	if watching {
		var err error
		interval, beat, err = watchArguments(args[2:])
		if err != nil {
			fmt.Fprintf(stderr, "4genteam watchdog: %v. %s\n", err, watchdogUsage)
			return clientcmd.ExitUsage
		}
	}
	log, rig, restore, code := openWatchdog(stdout, stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	defer log.close()
	w := newWatcher(rig, restore, log, allowPeak, stderr)
	if !watching {
		return w.check(rigName, "report")
	}
	w.watch(rigName, interval, beat)
	return clientcmd.ExitOK
}

// watchdogAll is the shell's --all: a watcher per rig that has a brief, concurrently, then wait. Without
// --watch it is a usage error, because there is no useful thing for it to do.
func watchdogAll(allowPeak bool, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "--watch" {
		fmt.Fprintln(stderr, "4genteam watchdog: --all requires --watch (or use --check)")
		return clientcmd.ExitUsage
	}
	interval := defaultWatchInterval
	if len(args) > 1 {
		seconds, err := strconv.Atoi(args[1])
		if err != nil || seconds <= 0 {
			fmt.Fprintf(stderr, "4genteam watchdog: watch interval %q must be a positive whole number of seconds. %s\n",
				args[1], watchdogUsage)
			return clientcmd.ExitUsage
		}
		interval = seconds
	}
	dir, err := briefDir()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam watchdog: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	log, rig, restore, code := openWatchdog(stdout, stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	defer log.close()
	w := newWatcher(rig, restore, log, allowPeak, stderr)
	names := make([]string, 0)
	for _, brief := range briefsOnDisk(dir) {
		names = append(names, strings.TrimSuffix(filepath.Base(brief), ".txt"))
	}
	log.printf("--all: watching %d rig(s) at %ds", len(names), interval)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			w.watch(n, time.Duration(interval)*time.Second, defaultWatchBeat)
		}(name)
	}
	wg.Wait()
	return clientcmd.ExitOK
}

// watchArguments reads the optional `[SECONDS] [HEARTBEAT_POLLS]` of a watch form, defaulting them the
// way the shell's `${3:-60}` and `${4:-15}` did.
func watchArguments(args []string) (time.Duration, int, error) {
	seconds, beat := defaultWatchInterval, defaultWatchBeat
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n <= 0 {
			return 0, 0, fmt.Errorf("watch interval %q must be a positive whole number of seconds", args[0])
		}
		seconds = n
	}
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			return 0, 0, fmt.Errorf("heartbeat polls %q must be a positive whole number", args[1])
		}
		beat = n
	}
	if len(args) > 2 {
		return 0, 0, fmt.Errorf("unexpected argument %s", args[2])
	}
	return time.Duration(seconds) * time.Second, beat, nil
}
