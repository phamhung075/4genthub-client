package clientwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// The verb behaviour: the grid, the input panes, the lead window, and the refusals - all with the exec
// seam faked, so nothing here needs herdr, tmux, a room or a seat.

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
	if !slices.Contains(args, "--state-root") {
		args = append(slices.Clone(args), "--state-root", withStateRoot(t))
	}
	code := runVerb(context.Background(), verb, args, stdout, stderr)
	return code, stdout.String(), stderr.String()
}

// Mirrors test_inputs_open_splits_a_small_input_pane_under_each_seat_pane.
func TestInputsOpenSplitsASmallInputPaneUnderEachSeatPane(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr([]map[string]any{paneEntry("lead"), paneEntry("go-dev")})
	fake.seatsFromTmux("r", []string{"lead", "go-dev"})
	withSeatStore(t, "r", "lead", "go-dev")

	code, out, errOut := runVerbT(t, "inputs", "open", "--room", "r")
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

	code, out, errOut := runVerbT(t, "inputs", "open", "--room", "r")
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

	code, out, errOut := runVerbT(t, "inputs", "hide", "--room", "r")
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

	code, out, errOut := runVerbT(t, "input", "--room", "r", "--seat", "lead")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(fake.callsOf("rig")) != 0 {
		t.Errorf("the verb sent without any input: %v", fake.callsOf("rig"))
	}

	sends := 0
	// THE INVARIANT THIS ARM EXISTS FOR: a typed line is DELIVERED IN PROCESS, and not by starting a
	// process. The shape this replaces ran `rig send <composed name>`, which after this client's rename
	// is a self-call, and it invented the session name from a convention (`{room}-{seat}@{room}`) instead
	// of reading it from the seat's record - the same defect the SEAT shape's send path records as fixed.
	var delivered []string
	previousSend := sendLine
	sendLine = func(_ context.Context, room, seat, text string) error {
		delivered = append(delivered, room+"/"+seat+": "+text)
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
		t.Errorf("the loop started a room process: %s", strings.Join(call, " "))
	}
	if sends != 0 {
		t.Errorf("room processes = %d, want 0: the line must be delivered in process", sends)
	}
	if len(delivered) != 1 || delivered[0] != "r/lead: hello there" {
		t.Errorf("delivered = %v, want exactly the one line to r/lead (the /hide line must not be sent)", delivered)
	}
	if !strings.Contains(stdout.String(), "sent") {
		t.Errorf("stdout = %q", stdout.String())
	}
	_ = out
}

// withStateRoot makes a state root to read, so the tests exercise the verb rather than the refusal; the
// caller passes it as --state-root. The root itself is unused by the grid; the panes it opens read it.
func withStateRoot(t *testing.T) string {
	t.Helper()
	if testStateRoot != "" {
		return testStateRoot
	}
	testStateRoot = t.TempDir()
	t.Cleanup(func() { testStateRoot = "" })
	return testStateRoot
}

// testStateRoot is the one state root a test's verbs run against, so the room a test builds and the root
// the verb reads are the same tree.
var testStateRoot string

// withSeatStore gives the verb a native room holding these seats, under the test's state root
// (<root>/rooms/<room>/seats/<seat>, the layout `4genteam up` leaves), so a verb that resolves a room from
// --room has a machine to resolve it on. It is what the existence check reads and not the live sessions: a
// room whose seats are all stopped is still a room.
func withSeatStore(t *testing.T, room string, seats ...string) string {
	t.Helper()
	root := withStateRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "rooms", room), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, seat := range seats {
		if err := os.MkdirAll(filepath.Join(root, "rooms", room, "seats", seat), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Mirrors the grid's own line.
func TestGridPrintsTheSeatAndColumnCount(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead", "go-dev"})
	withSeatStore(t, "r", "lead", "go-dev")
	root := withStateRoot(t)

	code, out, errOut := runVerbT(t, "grid", "--room", "r", "--state-root", root)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "grid: 2 seats in 2 columns") {
		t.Errorf("stdout = %q", out)
	}
}

// The seats are read from the tmux server the runtime owns, not from whatever server the caller's environment names.
func TestGridReadsSeatsFromTheRuntimesTmuxServer(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "/not/the/runtime")
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead"})
	withSeatStore(t, "r", "lead")
	root := withStateRoot(t)
	if code, _, errOut := runVerbT(t, "grid", "--room", "r", "--state-root", root); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(fake.tmuxRoots) == 0 {
		t.Fatal("the grid never reached tmux through the runtime's root")
	}
	for _, got := range fake.tmuxRoots {
		if want := clientenv.TmuxRoot(root); got != want {
			t.Errorf("TMUX_TMPDIR = %q, want %q", got, want)
		}
	}
}

// Mirrors test_a_relaunched_grid_replaces_the_old_one_and_keeps_the_same_layout.
func TestARelaunchedGridReplacesTheOldOneAndKeepsTheSameLayout(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead", "go-dev", "reviewer"})
	withSeatStore(t, "r", "lead", "go-dev", "reviewer")
	root := withStateRoot(t)

	layout := func() []string {
		before := len(fake.callsOf("herdr"))
		if code, _, errOut := runVerbT(t, "grid", "--room", "r", "--state-root", root); code != 0 {
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
	root := withStateRoot(t)

	code, out, errOut := runVerbT(t, "watch", "--room", "r", "--state-root", root)
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
		if strings.Contains(text, "feed --room r --seat lead --back 40 --width 0 --lines 25 --detail") {
			sawLeadFeed = true
		}
		if strings.Contains(text, "input --room r --seat lead") {
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
// take --room go through the ONE check, which is how grid, watch and inputs are covered here: they drive
// herdr, and herdr must not be opened from a seat's terminal, so they are covered by construction
// rather than by a live probe.
func TestAnUnknownRoomRefusesEveryVerbThatTakesRoom(t *testing.T) {
	for _, verb := range []string{"feed", "grid", "watch", "inputs", "input"} {
		t.Run(verb, func(t *testing.T) {
			fake := newFakeExec(t)
			args := []string{"--room", "zz-no-such-room"}
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

			code, _, errOut := runVerbT(t, verb, "--room", "r", "--seat", "zz-no-such-seat")
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
	root := withStateRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := runVerb(ctx, "feed", []string{"--room", "r", "--state-root", root}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "-- following 0 seats") {
		t.Errorf("a real room whose seats are all stopped did not stream its header: %q", stdout.String())
	}
}

// The doc says "Always pass --room; there is no default room" - so every verb refuses without one.
func TestRoomIsRequiredForEveryVerb(t *testing.T) {
	installBareSeam(t)
	for _, args := range [][]string{
		{},
		{"grid", "--room", ""},
		{"feed", "--room", ""},
		{"inputs", "open", "--room", ""},
		{"input", "--room", "", "--seat", "lead"},
		{"watch", "--room", ""},
	} {
		code, _, errOut := runWatchT(t, args...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2", args, code)
		}
		if !strings.Contains(errOut, "--room") {
			t.Errorf("%v did not name --room: %q", args, errOut)
		}
	}
}

// A machine without herdr is a prerequisite to fix, not to work around.
func TestAMachineWithoutHerdrRefusesByName(t *testing.T) {
	newFakeExec(t) // nothing is on PATH
	withSeatStore(t, "r")
	code, _, errOut := runVerbT(t, "grid", "--room", "r")
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

// The state root is `--state-root`, else the machine's own default. That default is the SHARED one now, so this states the
// delegation instead of a path literal, and it holds on any machine: a resolvable default must be
// adopted, a default that is absent must be refused BY PATH (this group's own extra stat), and a
// machine with no resolvable home must be refused as well.
func TestTheStateRootFallsBackToTheMachineDefault(t *testing.T) {
	newFakeExec(t)

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

// The order the usage text states: the flag wins over the default. The default is stated in full by
// TestTheStateRootFallsBackToTheMachineDefault, so this only checks it when this machine has one.
func TestTheStateRootOrderIsFlagThenDefault(t *testing.T) {
	flagRoot := t.TempDir()

	if got, err := resolveStateRoot(flagRoot); err != nil || got != flagRoot {
		t.Errorf("with a flag set, got %q, %v; want the flag %q", got, err, flagRoot)
	}
	if machine, err := clientenv.ResolveStateRoot(""); err == nil && isDir(machine) {
		if got, err := resolveStateRoot(""); err != nil || got != machine {
			t.Errorf("with no flag, got %q, %v; want the machine default %q", got, err, machine)
		}
	}
}

// The usage text has to state the third clause, and it has to state it HOME-RELATIVE: help output that
// carried the resolved absolute path would name the machine's user and differ per machine. Both halves
// are checked here and neither needs a real home.
func TestTheUsageStatesTheStateRootHomeRelative(t *testing.T) {
	var usage strings.Builder
	printVerbUsage(&usage, "feed")
	if !strings.Contains(usage.String(), "~/.config/4genthub") {
		t.Errorf("the usage does not state the default state root:\n%s", usage.String())
	}
	if strings.Contains(usage.String(), "/home/") {
		t.Errorf("the usage carries an absolute user-home path:\n%s", usage.String())
	}
	if machine := clientenv.DefaultStateRoot(); machine != "" && strings.Contains(usage.String(), machine) {
		t.Errorf("the usage prints the resolved path instead of the home-relative form:\n%s", usage.String())
	}
}

// A root that is named but not there is refused BY ITS PATH.
func TestAMissingStateRootIsRefusedByItsPath(t *testing.T) {
	newFakeExec(t)
	withSeatStore(t, "r")
	missing := filepath.Join(t.TempDir(), "absent")

	code, _, errOut := runVerbT(t, "feed", "--room", "r", "--state-root", missing)
	if code != 3 || !strings.Contains(errOut, missing) {
		t.Errorf("a missing root named %q at exit %d, want the path and 3", errOut, code)
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
	for _, want := range []string{"--state-root"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not name %s: %q", want, stderr.String())
		}
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
	root := withStateRoot(t)

	code, out, errOut := runWatchT(t, "grid", "--room", "r", "--state-root", root)
	if code != 0 {
		t.Fatalf("watch grid exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "grid: 1 seats in 2 columns") {
		t.Errorf("the umbrella did not run the grid: %q", out)
	}
}

// seatLog reads the newest *.jsonl of a native room's seat; the per-session artifact directory beside
// the logs is not a log.
func TestSeatLogReadsTheNativeSessionsDirectory(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "rooms", "R", "seats", "S", "sessions")
	if err := os.MkdirAll(filepath.Join(sessions, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(sessions, "x.jsonl")
	if err := os.WriteFile(session, []byte("{\"message\":{\"model\":\"m1\",\"usage\":{\"totalTokens\":42}}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, runtime := seatLog(root, "R", "S")
	if runtime != "omp" || path != session {
		t.Errorf("native seat resolved to (%q, %q), want (%q, omp)", path, runtime, session)
	}
	if path, runtime = seatLog(root, "R", "other"); path != "" || runtime != "" {
		t.Errorf("a seat with no sessions resolved to (%q, %q), want empty", path, runtime)
	}

	snap := SeatSnapshot(root, "R", "S")
	if snap.LogPath != session || snap.Runtime != "omp" || snap.Model != "m1" || snap.ContextTokens != 42 {
		t.Errorf("SeatSnapshot = %+v", snap)
	}
}

// A seat switched to agy keeps its old omp log; the agy transcript, once newer, is the seat's log.
func TestSeatLogFollowsTheAgyTranscriptOnceItIsNewer(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	seat := filepath.Join(root, "rooms", "R", "seats", "S")
	if err := os.MkdirAll(filepath.Join(seat, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	omp := filepath.Join(seat, "sessions", "x.jsonl")
	logs := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "conv-1", ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	agy := filepath.Join(logs, "transcript.jsonl")
	for path, body := range map[string]string{omp: "{}\n", agy: "{}\n", filepath.Join(seat, "agy-conversation"): "conv-1\n"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, now := time.Now().Add(-time.Hour), time.Now()
	_ = os.Chtimes(omp, old, old)
	_ = os.Chtimes(agy, now, now)
	if path, runtime := seatLog(root, "R", "S"); runtime != "agy" || path != agy {
		t.Errorf("a seat on agy resolved to (%q, %q), want (%q, agy)", path, runtime, agy)
	}
	_ = os.Chtimes(omp, now.Add(time.Hour), now.Add(time.Hour))
	if path, runtime := seatLog(root, "R", "S"); runtime != "omp" || path != omp {
		t.Errorf("a seat back on omp resolved to (%q, %q), want (%q, omp)", path, runtime, omp)
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

// TestSmokeFeedReadsTheLiveRoom is the phase's SMOKE, kept reproducible: it needs this machine's tmux
// seats and omp state root, so it skips unless the two variables are set. It is the only test here that
// touches the real machine - every other test in this package runs against the fake seam.
//
//	WATCH_SMOKE_ROOT=~/.config/4genthub WATCH_SMOKE_ROOM=<room> go test ./internal/clientwatch/ -run Smoke -v
func TestSmokeFeedReadsTheLiveRoom(t *testing.T) {
	root := os.Getenv("WATCH_SMOKE_ROOT")
	room := os.Getenv("WATCH_SMOKE_ROOM")
	if root == "" || room == "" {
		t.Skip("set WATCH_SMOKE_ROOT and WATCH_SMOKE_ROOM to smoke the live view")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := &strings.Builder{}
	if err := runFeed(ctx, shared{room: room, stateRoot: root, back: 2, width: 120, lines: 3, detail: true}, out); err != nil {
		t.Fatalf("feed: %v", err)
	}
	captured := splitLines(out.String())
	t.Logf("feed captured %d line(s) from %s in %s", len(captured), room, root)
	for index, line := range captured {
		if index >= 12 {
			break
		}
		t.Logf("  %s", line)
	}
	if len(captured) == 0 {
		t.Errorf("the feed rendered nothing; the smoke is only meaningful with a live room")
	}
}

// A room `4genteam up` started is a room even when every seat is stopped, and the cloud seat store's layout
// (<root>/<room>/<seat>) is not where the check looks.
func TestTheExistenceCheckReadsTheNativeRoomLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rooms", "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireRoomAndSeats(root, "r", nil); err != nil {
		t.Errorf("a native room with no seats refused: %v", err)
	}
	if err := requireRoomAndSeats(root, "r", []string{"lead"}); err == nil || !strings.Contains(err.Error(), "no such seat: lead in r") {
		t.Errorf("a seat the room does not hold: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "rooms", "r", "seats", "lead"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireRoomAndSeats(root, "r", []string{"lead"}); err != nil {
		t.Errorf("a native seat refused: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "old", "lead"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireRoomAndSeats(root, "old", nil); err == nil || !strings.Contains(err.Error(), "no such room: old") {
		t.Errorf("a room in the seat-store layout counted as a native room: %v", err)
	}
}

// A PANE IS NEVER GIVEN A NAME TO RESOLVE (P1's remainder, the reach inventory). `selfPath` used to fall
// back to the bare string "4genteam" when `os.Executable` failed, and that string went into the pane's
// shell line - so the pane's OWN shell would resolve it on PATH and could start an INSTALLED client rather
// than this one. The fallback is gone: the failure is a refusal that names what could not be read, and a
// pane is only ever sent an absolute path to THIS binary.
func TestAPaneIsNeverGivenABareNameForItsShellToResolve(t *testing.T) {
	prev := executable
	executable = func() (string, error) { return "", errors.New("/proc/self/exe: permission denied") }
	defer func() { executable = prev }()

	command, err := paneCommand("watch --room r")
	if err == nil {
		t.Fatalf("paneCommand = %q, want a refusal: a bare name would run whatever PATH resolves", command)
	}
	if !strings.Contains(err.Error(), "cannot name its own binary") {
		t.Errorf("err = %v, want it to name the read that failed", err)
	}
	if strings.Contains(err.Error(), "exec 4genteam") || command != "" {
		t.Errorf("command = %q err = %v, want no command line at all", command, err)
	}

	executable = func() (string, error) { return "/opt/4genthub/4genteam", nil }
	command, err = paneCommand("watch --room r")
	if err != nil {
		t.Fatalf("paneCommand with a readable self path: %v", err)
	}
	if command != "clear; exec /opt/4genthub/4genteam watch --room r" {
		t.Errorf("command = %q, want the absolute path of THIS binary and the verb", command)
	}
}

// An agy transcript names no model, so the seat's model is the one its room's spec.json declares.
func TestSeatModelOfFallsBackToTheRoomSpec(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rooms", "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{"seats":[{"seat_key":"lead","model":"deepseek/deepseek-flash"},{"seat_key":"reviewer","model":"gemini-3.8-flash-low"}]}`
	if err := os.WriteFile(filepath.Join(root, "rooms", "r", "spec.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(log, []byte(`{"type":"PLANNER_RESPONSE","content":"hi"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := seatModelOf(root, "r", "reviewer", log); got != "gemini-3.8-flash-low" {
		t.Errorf("seatModelOf = %q, want the spec's model", got)
	}
	if got := seatModelOf(root, "r", "nobody", log); got != "" {
		t.Errorf("seatModelOf of an unlisted seat = %q, want empty", got)
	}
}

// TestGridReadsAnAbsentServerAsNoSeats: a freshly created state root has no tmux server, and tmux then
// answers "error connecting to ... (No such file or directory)" rather than "no server running". That is
// the fact "no seats", not a failed read - the same rule clientrestore and ps take - so the grid opens
// with zero seats instead of refusing the verb.
func TestGridReadsAnAbsentServerAsNoSeats(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	withSeatStore(t, "r", "lead")
	root := withStateRoot(t)
	fake.failures["tmux list-sessions -F #{session_name}"] = errors.New("exit status 1")

	code, out, errOut := runVerbT(t, "grid", "--room", "r", "--state-root", root)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "grid: 0 seats") {
		t.Errorf("stdout = %q, want the grid's zero-seat line", out)
	}
}

// TestInputPipeSendsStdinAsOneMessage: a piped multi-line text is ONE message, sent once, with its inner
// newlines kept; blank input sends nothing; a failed send is an error rather than a printed "failed".
func TestInputPipeSendsStdinAsOneMessage(t *testing.T) {
	var delivered []string
	previousSend := sendLine
	sendFails := false
	sendLine = func(_ context.Context, room, seat, text string) error {
		if sendFails {
			return errors.New("the agent refused the turn")
		}
		delivered = append(delivered, room+"/"+seat+": "+text)
		return nil
	}
	t.Cleanup(func() { sendLine = previousSend })

	out := &strings.Builder{}
	if err := runInputPipe(context.Background(), "r", "lead", strings.NewReader("l1\nl2\n\nl4\n"), out); err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if len(delivered) != 1 || delivered[0] != "r/lead: l1\nl2\n\nl4" {
		t.Errorf("delivered = %q, want one message carrying all four lines", delivered)
	}
	if out.String() != "lead > sent\n" {
		t.Errorf("stdout = %q, want one sent line", out.String())
	}

	delivered, out = nil, &strings.Builder{}
	if err := runInputPipe(context.Background(), "r", "lead", strings.NewReader("\n  \n"), out); err != nil || len(delivered) != 0 {
		t.Errorf("blank input: err = %v, delivered = %q, want nothing sent", err, delivered)
	}

	sendFails = true
	if err := runInputPipe(context.Background(), "r", "lead", strings.NewReader("x\n"), &strings.Builder{}); err == nil {
		t.Error("a refused send was not an error")
	}
}

// An agy seat's pane title carries its usage windows; the runtime comes from the spec when the seat has no
// transcript yet, and a seat on another runtime gets no usage text.
func TestAnAgySeatsPaneTitleCarriesItsUsageWindows(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rooms", "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{"seats":[{"seat_key":"lead","runtime":"omp"},{"seat_key":"peak-dev","runtime":"agy","model":"gemini-3.8-flash-low"}]}`
	if err := os.WriteFile(filepath.Join(root, "rooms", "r", "spec.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := seatRuntimeOf(root, "r", "peak-dev"); got != "agy" {
		t.Errorf("seatRuntimeOf(peak-dev) = %q, want agy", got)
	}
	if got := seatRuntimeOf(root, "r", "lead"); got != "omp" {
		t.Errorf("seatRuntimeOf(lead) = %q, want omp", got)
	}

	titles := []string{}
	header := newHeader("peak-dev", 120, 150000, "gemini-3.8-flash-low", func(string) {})
	header.pane = func(title string) { titles = append(titles, title) }
	header.usage = func() string { return "5h 91% left ↻1h33m · week 16% left ↻4d7h" }
	header.draw()
	if len(titles) != 1 || !strings.HasSuffix(titles[0], "ctx …  5h 91% left ↻1h33m · week 16% left ↻4d7h") {
		t.Errorf("title = %v", titles)
	}
}
