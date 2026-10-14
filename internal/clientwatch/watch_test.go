package clientwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// The verb behaviour: the grid, the input panes, the lead window, and the refusals - all with the exec
// seam faked, so nothing here needs herdr, tmux, a rig or a seat.

func runWatchT(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	code := runWatchArgs(context.Background(), args, stdout, stderr)
	return code, stdout.String(), stderr.String()
}

func runVerbT(t *testing.T, verb string, args ...string) (int, string, string) {
	t.Helper()
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	code := runVerb(context.Background(), verb, args, stdout, stderr)
	return code, stdout.String(), stderr.String()
}

// Mirrors test_inputs_open_splits_a_small_input_pane_under_each_seat_pane.
func TestInputsOpenSplitsASmallInputPaneUnderEachSeatPane(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr([]map[string]any{paneEntry("lead"), paneEntry("go-dev")})
	fake.seatsFromTmux("r", []string{"lead", "go-dev"})
	withSeatStore(t, "r", "lead", "go-dev")

	code, out, errOut := runVerbT(t, "inputs", "open", "--rig", "r")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if got := len(fake.callsOf("herdr")); got == 0 {
		t.Fatal("herdr was never called")
	}
	splits := 0
	renamed := []string{}
	for _, call := range fake.callsOf("herdr") {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "pane split") {
			splits++
		}
		if strings.Contains(joined, "pane rename") {
			renamed = append(renamed, call[len(call)-1])
		}
	}
	if splits != 2 {
		t.Errorf("pane splits = %d, want one under each seat pane", splits)
	}
	if len(renamed) != 2 || renamed[0] != "lead"+inputSuffix || renamed[1] != "go-dev"+inputSuffix {
		t.Errorf("renamed panes = %v", renamed)
	}
	if !strings.Contains(out, "inputs: opened 2") {
		t.Errorf("stdout = %q", out)
	}
}

// Mirrors test_inputs_open_twice_adds_nothing_the_second_time.
func TestInputsOpenTwiceAddsNothingTheSecondTime(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr([]map[string]any{paneEntry("lead"), paneEntry("lead" + inputSuffix)})
	fake.seatsFromTmux("r", []string{"lead"})
	withSeatStore(t, "r", "lead")

	code, out, errOut := runVerbT(t, "inputs", "open", "--rig", "r")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, call := range fake.callsOf("herdr") {
		if strings.Contains(strings.Join(call, " "), "pane split") {
			t.Errorf("a pane was split although the seat already had one: %v", call)
		}
	}
	if !strings.Contains(out, "inputs: opened 0") {
		t.Errorf("stdout = %q", out)
	}
}

// Mirrors the hide half of the Python's inputs tests.
func TestInputsHideClosesTheInputPanes(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr([]map[string]any{paneEntry("lead"), paneEntry("lead" + inputSuffix)})
	fake.seatsFromTmux("r", []string{"lead"})
	withSeatStore(t, "r", "lead")

	code, out, errOut := runVerbT(t, "inputs", "hide", "--rig", "r")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	closed := 0
	for _, call := range fake.callsOf("herdr") {
		if strings.Contains(strings.Join(call, " "), "pane close") {
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("panes closed = %d, want 1", closed)
	}
	if !strings.Contains(out, "inputs: hid 1") {
		t.Errorf("stdout = %q", out)
	}
}

// Mirrors test_a_typed_line_is_sent_to_its_seat_and_hide_leaves_the_loop.
func TestATypedLineIsSentToItsSeatAndHideLeavesTheLoop(t *testing.T) {
	fake := newFakeExec(t)
	fake.on("rig send r-lead@r hello there", "sent\n")
	withSeatStore(t, "r", "lead")

	code, out, errOut := runVerbT(t, "input", "--rig", "r", "--seat", "lead")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(fake.callsOf("rig")) != 0 {
		t.Errorf("the verb sent without any input: %v", fake.callsOf("rig"))
	}

	sends := 0
	// THE INVARIANT THIS ARM EXISTS FOR: a typed line is DELIVERED IN PROCESS, and not by starting a
	// process. The shape this replaces ran `rig send <composed name>`, which after this client's rename
	// is a self-call, and it invented the session name from a convention (`{rig}-{seat}@{rig}`) instead
	// of reading it from the seat's record - the same defect the SEAT shape's send path records as fixed.
	var delivered []string
	previousSend := sendLine
	sendLine = func(_ context.Context, rig, seat, text string) error {
		delivered = append(delivered, rig+"/"+seat+": "+text)
		return nil
	}
	t.Cleanup(func() { sendLine = previousSend })

	stdin := strings.NewReader("hello there\n/hide\n")
	stdout := &strings.Builder{}
	if err := runInputLoop(context.Background(), "r", "lead", stdin, stdout); err != nil {
		t.Fatalf("input loop: %v", err)
	}
	for _, call := range fake.callsOf("rig") {
		sends++
		t.Errorf("the loop started a rig process: %s", strings.Join(call, " "))
	}
	if sends != 0 {
		t.Errorf("rig processes = %d, want 0: the line must be delivered in process", sends)
	}
	if len(delivered) != 1 || delivered[0] != "r/lead: hello there" {
		t.Errorf("delivered = %v, want exactly the one line to r/lead (the /hide line must not be sent)", delivered)
	}
	if !strings.Contains(stdout.String(), "sent") {
		t.Errorf("stdout = %q", stdout.String())
	}
	_ = out
}

// withStateRoot gives the verb a state root to read, so the tests exercise the verb rather than the
// refusal. The root itself is unused by the grid; the panes it opens read it.
func withStateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(stateRootEnv, root)
	return root
}

// withSeatStore gives the verb a seat store holding this room and these seats, so a verb that resolves
// a room from --rig has a machine to resolve it on. It is the store the existence check reads and not
// the live sessions: a room whose seats are all stopped is still a room.
func withSeatStore(t *testing.T, rig string, seats ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, rig), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, seat := range seats {
		if err := os.MkdirAll(filepath.Join(root, rig, seat), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	restore := seatStore
	seatStore = func() string { return root }
	t.Cleanup(func() { seatStore = restore })
	return root
}

// Mirrors the grid's own line.
func TestGridPrintsTheSeatAndColumnCount(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead", "go-dev"})
	withSeatStore(t, "r", "lead", "go-dev")
	withStateRoot(t)

	code, out, errOut := runVerbT(t, "grid", "--rig", "r")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "grid: 2 seats in 2 columns") {
		t.Errorf("stdout = %q", out)
	}
}

// Mirrors test_a_relaunched_grid_replaces_the_old_one_and_keeps_the_same_layout.
func TestARelaunchedGridReplacesTheOldOneAndKeepsTheSameLayout(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead", "go-dev", "reviewer"})
	withSeatStore(t, "r", "lead", "go-dev", "reviewer")
	withStateRoot(t)

	layout := func() []string {
		before := len(fake.callsOf("herdr"))
		if code, _, errOut := runVerbT(t, "grid", "--rig", "r"); code != 0 {
			t.Fatalf("grid exit %d, stderr %q", code, errOut)
		}
		shape := []string{}
		for _, call := range fake.callsOf("herdr")[before:] {
			joined := strings.Join(call, " ")
			if strings.Contains(joined, "pane split") || strings.Contains(joined, "workspace create") {
				shape = append(shape, joined)
			}
		}
		return shape
	}
	first := layout()
	second := layout()
	if len(first) != len(second) {
		t.Fatalf("the second launch made %d shape calls, the first %d", len(second), len(first))
	}
	for index := range first {
		if first[index] != second[index] {
			t.Errorf("shape call %d differs: %q vs %q", index, first[index], second[index])
		}
	}
	closes := 0
	for _, call := range fake.callsOf("herdr") {
		if strings.Contains(strings.Join(call, " "), "workspace close "+fakeGridWorkspace) {
			closes++
		}
	}
	if closes < 2 {
		t.Errorf("workspace closes = %d, want one per launch so the wall is replaced not stacked", closes)
	}
}

// Mirrors test_watch_opens_the_grid_and_a_lead_window_with_its_input.
func TestWatchOpensTheGridAndALeadWindowWithItsInput(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead", "go-dev"})
	withSeatStore(t, "r", "lead", "go-dev")
	fake.on("herdr workspace create --cwd "+workingDirKey()+" --label r lead --no-focus",
		`{"result": {"root_pane": {"pane_id": "w25:lead1"}}}`)
	fake.on("herdr pane split --pane w25:lead1 --direction down --ratio 0.800 --cwd "+workingDirKey(),
		`{"result": {"pane": {"pane_id": "w25:lead2"}}}`)
	withStateRoot(t)

	code, out, errOut := runVerbT(t, "watch", "--rig", "r")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "grid: 2 seats in 2 columns") {
		t.Errorf("the grid half did not run: %q", out)
	}
	typed := []string{}
	for _, call := range fake.callsOf("herdr") {
		if strings.Contains(strings.Join(call, " "), "send-text") {
			typed = append(typed, strings.Join(call, " "))
		}
	}
	sawLeadFeed, sawLeadInput := false, false
	for _, text := range typed {
		if strings.Contains(text, "feed --rig r --seat lead --back 40 --width 200 --lines 25 --detail") {
			sawLeadFeed = true
		}
		if strings.Contains(text, "input --rig r --seat lead") {
			sawLeadInput = true
		}
	}
	if !sawLeadFeed || !sawLeadInput {
		t.Errorf("the lead window is incomplete: feed=%v input=%v\n%v", sawLeadFeed, sawLeadInput, typed)
	}
	for _, text := range typed {
		if strings.Contains(text, "tmux attach") {
			t.Errorf("an input pane was opened as a raw tmux attach: %q", text)
		}
	}
}

// The reviewer's OBSERVATION, as a test: a verb that cannot do the work must refuse by name and
// non-zero - never print a prompt and exit 0, and never stream nothing forever. All five verbs that
// take --rig go through the ONE check, which is how grid, watch and inputs are covered here: they drive
// herdr, and herdr must not be opened from a seat's terminal, so they are covered by construction
// rather than by a live probe.
func TestAnUnknownRoomRefusesEveryVerbThatTakesRig(t *testing.T) {
	for _, verb := range []string{"feed", "grid", "watch", "inputs", "input"} {
		t.Run(verb, func(t *testing.T) {
			fake := newFakeExec(t)
			args := []string{"--rig", "zz-no-such-room"}
			switch verb {
			case "inputs":
				args = append([]string{"open"}, args...)
			case "input":
				args = append(args, "--seat", "zz")
			}
			code, _, errOut := runVerbT(t, verb, args...)
			if code != 2 {
				t.Errorf("exit %d, want 2: %q", code, errOut)
			}
			if !strings.Contains(errOut, "no such room: zz-no-such-room") {
				t.Errorf("the refusal did not name the room: %q", errOut)
			}
			if got := len(fake.callsOf("herdr")) + len(fake.callsOf("tmux")) + len(fake.callsOf("rig")); got != 0 {
				t.Errorf("the verb did %d call(s) for a room that does not exist", got)
			}
		})
	}
}

// The seat half of the same rule: the room is there, the seat named by --seat is not.
func TestAnUnknownSeatInARealRoomRefuses(t *testing.T) {
	for _, verb := range []string{"feed", "input"} {
		t.Run(verb, func(t *testing.T) {
			fake := newFakeExec(t)
			withSeatStore(t, "r", "lead")

			code, _, errOut := runVerbT(t, verb, "--rig", "r", "--seat", "zz-no-such-seat")
			if code != 2 {
				t.Errorf("exit %d, want 2: %q", code, errOut)
			}
			if !strings.Contains(errOut, "no such seat: zz-no-such-seat in r") {
				t.Errorf("the refusal did not name the seat and the room: %q", errOut)
			}
			if got := len(fake.callsOf("rig")); got != 0 {
				t.Errorf("the verb wrote to a seat that does not exist: %v", fake.callsOf("rig"))
			}
		})
	}
}

// The regression that proves the check is EXISTENCE and not a count: a real room whose seats are all
// stopped still streams, with the header the docs call normal. A zero-count refusal would break it.
func TestARealRoomWithNoLiveSeatsStillStreams(t *testing.T) {
	fake := newFakeExec(t)
	fake.seatsFromTmux("r", nil) // every seat stopped
	withSeatStore(t, "r", "lead")
	withStateRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := runVerb(ctx, "feed", []string{"--rig", "r"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "-- following 0 seats") {
		t.Errorf("a real room whose seats are all stopped did not stream its header: %q", stdout.String())
	}
}

// The doc says "Always pass --rig; there is no default room" - so every verb refuses without one.
func TestRigIsRequiredForEveryVerb(t *testing.T) {
	installBareSeam(t)
	for _, args := range [][]string{
		{},
		{"grid", "--rig", ""},
		{"feed", "--rig", ""},
		{"inputs", "open", "--rig", ""},
		{"input", "--rig", "", "--seat", "lead"},
		{"watch", "--rig", ""},
	} {
		code, _, errOut := runWatchT(t, args...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2", args, code)
		}
		if !strings.Contains(errOut, "--rig") {
			t.Errorf("%v did not name --rig: %q", args, errOut)
		}
	}
}

// A machine without herdr is a prerequisite to fix, not to work around.
func TestAMachineWithoutHerdrRefusesByName(t *testing.T) {
	newFakeExec(t) // nothing is on PATH
	withSeatStore(t, "r")
	code, _, errOut := runVerbT(t, "grid", "--rig", "r")
	if code == 0 {
		t.Fatal("grid succeeded without herdr")
	}
	if !strings.Contains(errOut, "herdr") {
		t.Errorf("the refusal did not name herdr: %q", errOut)
	}
	if strings.Count(strings.TrimSpace(errOut), "\n") != 0 {
		t.Errorf("the refusal is not ONE line: %q", errOut)
	}
}

// The state root is `--state-root`, else $OPENRIG_STATE_ROOT, else the machine's own default - the
// third clause docs/WATCH.md promises. That default is the SHARED one now, so this states the
// delegation instead of a path literal, and it holds on any machine: a resolvable default must be
// adopted, a default that is absent must be refused BY PATH (this group's own extra stat), and a
// machine with no resolvable home must be refused as well.
func TestTheStateRootFallsBackToTheMachineDefault(t *testing.T) {
	newFakeExec(t)
	t.Setenv(stateRootEnv, "")

	machine, machineErr := clientenv.ResolveStateRoot("")
	got, gotErr := resolveStateRoot("")
	switch {
	case machineErr != nil:
		if gotErr == nil {
			t.Fatalf("resolveStateRoot = %q with no resolvable home; want this group's refusal", got)
		}
	case !isDir(machine):
		if gotErr == nil {
			t.Fatalf("resolveStateRoot = %q for the absent machine default %q; want a refusal by path",
				got, machine)
		}
	default:
		if gotErr != nil || got != machine {
			t.Fatalf("resolveStateRoot = %q, %v; want the shared machine default %q", got, gotErr, machine)
		}
	}
}

// The order the usage text states: the flag wins over the environment, and the environment over the
// default. The first two clauses are hermetic here; the default is stated in full by
// TestTheStateRootFallsBackToTheMachineDefault, so this only checks it when this machine has one.
func TestTheStateRootOrderIsFlagThenEnvironmentThenDefault(t *testing.T) {
	flagRoot, envRoot := t.TempDir(), t.TempDir()

	t.Setenv(stateRootEnv, envRoot)
	if got, err := resolveStateRoot(flagRoot); err != nil || got != flagRoot {
		t.Errorf("with a flag and the environment both set, got %q, %v; want the flag %q",
			got, err, flagRoot)
	}
	if got, err := resolveStateRoot(""); err != nil || got != envRoot {
		t.Errorf("with only the environment set, got %q, %v; want the environment %q", got, err, envRoot)
	}
	t.Setenv(stateRootEnv, "")
	if machine, err := clientenv.ResolveStateRoot(""); err == nil && isDir(machine) {
		if got, err := resolveStateRoot(""); err != nil || got != machine {
			t.Errorf("with neither set, got %q, %v; want the machine default %q", got, err, machine)
		}
	}
}

// The usage text has to state the third clause, and it has to state it HOME-RELATIVE: help output that
// carried the resolved absolute path would name the machine's user and differ per machine. Both halves
// are checked here and neither needs a real home.
func TestTheUsageStatesTheStateRootHomeRelative(t *testing.T) {
	var usage strings.Builder
	printVerbUsage(&usage, "feed")
	if !strings.Contains(usage.String(), "~/.openrig/state/omp") {
		t.Errorf("the usage does not state the default state root:\n%s", usage.String())
	}
	if strings.Contains(usage.String(), "/home/") {
		t.Errorf("the usage carries an absolute user-home path:\n%s", usage.String())
	}
	if machine := clientenv.DefaultStateRoot(); machine != "" && strings.Contains(usage.String(), machine) {
		t.Errorf("the usage prints the resolved path instead of the home-relative form:\n%s", usage.String())
	}
}

// A root that is named but not there is refused BY ITS PATH, whichever clause named it.
func TestAMissingStateRootIsRefusedByItsPath(t *testing.T) {
	newFakeExec(t)
	withSeatStore(t, "r")
	missing := filepath.Join(t.TempDir(), "absent")

	code, _, errOut := runVerbT(t, "feed", "--rig", "r", "--state-root", missing)
	if code != 3 || !strings.Contains(errOut, missing) {
		t.Errorf("a missing root named %q at exit %d, want the path and 3", errOut, code)
	}

	t.Setenv(stateRootEnv, missing)
	code, _, errOut = runVerbT(t, "feed", "--rig", "r")
	if code != 3 || !strings.Contains(errOut, missing) {
		t.Errorf("a missing root named by the environment gave %q at exit %d, want the path and 3",
			errOut, code)
	}
}

// The wrapper's decision, pinned without a machine that cannot resolve a home: a root this group cannot
// resolve exits unavailable (3) where the five shared paths answer the shared resolver's own usage (2).
// The 2 half is pinned where it is reachable, in clientenv's refusal test; the message on both paths
// stays the shared one, which names both escapes. reportError is what every verb returns through, so
// this pins the code the user gets.
func TestTheWatchRootRefusalIsUnavailableNotUsage(t *testing.T) {
	stderr := &strings.Builder{}
	code := reportError(watchRootError(errors.New("no resolvable home")), stderr)
	if code != clientcmd.ExitUnavailable {
		t.Errorf("the watch root refusal exits %d, want %d (unavailable)", code, clientcmd.ExitUnavailable)
	}
	for _, want := range []string{"--state-root", stateRootEnv} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not name %s: %q", want, stderr.String())
		}
	}
}

func TestTheStateRootComesFromTheEnvironmentToo(t *testing.T) {
	fake := newFakeExec(t)
	root := t.TempDir()
	t.Setenv(stateRootEnv, root)
	fake.seatsFromTmux("r", nil)

	resolved, err := resolveStateRoot("")
	if err != nil || resolved != root {
		t.Fatalf("resolveStateRoot = %q, %v; want %q", resolved, err, root)
	}
}

// Every verb answers --help, and the umbrella still names the verbs.
func TestEveryVerbAnswersHelp(t *testing.T) {
	installBareSeam(t)
	for _, args := range [][]string{
		{"--help"},
		{"grid", "--help"},
		{"feed", "--help"},
		{"inputs", "--help"},
		{"input", "--help"},
		{"watch", "--help"},
	} {
		code, out, errOut := runWatchT(t, args...)
		if code != 0 {
			t.Errorf("%v exited %d, stderr %q", args, code, errOut)
		}
		if !strings.Contains(out, "usage: 4genteam") {
			t.Errorf("%v printed no usage:\n%s", args, out)
		}
	}
}

// `4genteam watch grid` is the grid, and a bare `4genteam watch` is the umbrella.
func TestTheUmbrellaDispatchesToItsVerbs(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead"})
	withSeatStore(t, "r", "lead")
	withStateRoot(t)

	code, out, errOut := runWatchT(t, "grid", "--rig", "r")
	if code != 0 {
		t.Fatalf("watch grid exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "grid: 1 seats in 2 columns") {
		t.Errorf("the umbrella did not run the grid: %q", out)
	}
}

// seatLog prefers the omp state directory and falls back to the Claude Code log.
func TestSeatLogPrefersOmpStateThenClaude(t *testing.T) {
	fake := newFakeExec(t)
	root := t.TempDir()
	seatDir := filepath.Join(root, "r-lead@r", "sessions")
	if err := os.MkdirAll(seatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(seatDir, "one.jsonl")
	if err := os.WriteFile(session, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, runtime := seatLog(context.Background(), root, "r", "lead")
	if runtime != "omp" || path != session {
		t.Errorf("omp seat resolved to (%q, %q), want (%q, omp)", path, runtime, session)
	}

	// No state directory and no claude process: a runtime the client has no reader for.
	fake.on("pgrep -af claude .*--name r-go-dev@r", "")
	path, runtime = seatLog(context.Background(), filepath.Join(root, "empty"), "r", "go-dev")
	if runtime != "claude" || path != "" {
		t.Errorf("a seat with no log resolved to (%q, %q), want (\"\", claude)", path, runtime)
	}

	// OpenRig writes a state directory with only agent/AGENTS.md for a Claude Code seat too. It holds no
	// session, so the seat is not an omp seat and its Claude Code log is looked for.
	if err := os.MkdirAll(filepath.Join(root, "r-architect@r", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake.on("pgrep -af claude .*--name r-architect@r", "")
	if _, runtime = seatLog(context.Background(), root, "r", "architect"); runtime != "claude" {
		t.Errorf("a state directory with no session resolved to runtime %q, want claude", runtime)
	}
}

// Mirrors test_seat_model_is_the_last_model_in_the_log_tail: the model the header shows is the one that
// answered last. The reading is the LAST match in the log (and in its tail on a large log), never the
// first, so a seat that switched models reads as the new one.
func TestTheSeatModelIsTheLastOneInTheLogTail(t *testing.T) {
	log := filepath.Join(t.TempDir(), "session.jsonl")
	contents := `{"message":{"model":"old"}}` + "\n" + `{"message":{"model":"claude-opus-5-5"}}` + "\n"
	if err := os.WriteFile(log, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := seatModel(log); got != "claude-opus-5-5" {
		t.Errorf("seatModel = %q, want the LAST model in the tail, not the first", got)
	}
	if got := seatModel(""); got != "" {
		t.Errorf("seatModel of no log = %q, want empty", got)
	}
}

// installBareSeam swaps the exec seam for one that fails every call, for the tests that only look at
// argument handling.
func installBareSeam(t *testing.T) {
	t.Helper()
	newFakeExec(t)
}

// TestSmokeFeedReadsTheLiveRig is the phase's SMOKE, kept reproducible: it needs this machine's tmux
// seats and omp state root, so it skips unless the two variables are set. It is the only test here that
// touches the real machine - every other test in this package runs against the fake seam.
//
//	WATCH_SMOKE_ROOT=~/.openrig/state/omp WATCH_SMOKE_RIG=<rig> go test ./internal/clientwatch/ -run Smoke -v
func TestSmokeFeedReadsTheLiveRig(t *testing.T) {
	root := os.Getenv("WATCH_SMOKE_ROOT")
	rig := os.Getenv("WATCH_SMOKE_RIG")
	if root == "" || rig == "" {
		t.Skip("set WATCH_SMOKE_ROOT and WATCH_SMOKE_RIG to smoke the live view")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := &strings.Builder{}
	if err := runFeed(ctx, root, shared{rig: rig, back: 2, width: 120, lines: 3, detail: true}, out); err != nil {
		t.Fatalf("feed: %v", err)
	}
	captured := splitLines(out.String())
	t.Logf("feed captured %d line(s) from %s in %s", len(captured), rig, root)
	for index, line := range captured {
		if index >= 12 {
			break
		}
		t.Logf("  %s", line)
	}
	if len(captured) == 0 {
		t.Errorf("the feed rendered nothing; the smoke is only meaningful with a live rig")
	}
}
