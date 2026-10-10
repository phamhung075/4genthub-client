package clientwatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	code, out, errOut := runVerbT(t, "input", "--rig", "r", "--seat", "lead")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(fake.callsOf("rig")) != 0 {
		t.Errorf("the verb sent without any input: %v", fake.callsOf("rig"))
	}

	sends := 0
	stdin := strings.NewReader("hello there\n/hide\n")
	stdout := &strings.Builder{}
	if err := runInputLoop(context.Background(), "r", "lead", stdin, stdout); err != nil {
		t.Fatalf("input loop: %v", err)
	}
	for _, call := range fake.callsOf("rig") {
		sends++
		if got := strings.Join(call, " "); got != "rig send r-lead@r hello there" {
			t.Errorf("sent %q", got)
		}
	}
	if sends != 1 {
		t.Errorf("sends = %d, want 1 (the /hide line must not be sent)", sends)
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

// Mirrors the grid's own line.
func TestGridPrintsTheSeatAndColumnCount(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.seatsFromTmux("r", []string{"lead", "go-dev"})
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

// The state root comes from a flag or the environment, never a hard-coded owner path.
func TestAMachineWithoutTheStateRootRefusesByName(t *testing.T) {
	newFakeExec(t)
	t.Setenv(stateRootEnv, "")
	code, _, errOut := runVerbT(t, "feed", "--rig", "r")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if !strings.Contains(errOut, "state root") || !strings.Contains(errOut, stateRootEnv) {
		t.Errorf("the refusal did not name the state root and its variable: %q", errOut)
	}

	missing := filepath.Join(t.TempDir(), "absent")
	code, _, errOut = runVerbT(t, "feed", "--rig", "r", "--state-root", missing)
	if code != 3 || !strings.Contains(errOut, missing) {
		t.Errorf("a missing root named %q at exit %d, want the path and 3", errOut, code)
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
	_ = fake
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
