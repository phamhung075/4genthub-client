package clientlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bottom of peak-review's pane in room aa2-rs-peak, 2026-10-29, after the second failed input.
const brokenAgyPane = `● Ran 17 commands, checked 2 tasks (4genteam room queue...)
  ⚠ agent executor error: generating and executing: serializing trajectory: maxMessageTokens must be positive, got 0
  Error ID: d480a155-dd4e-463e-acec-6757ea67d798-42
> From owner session: You went quiet in the middle of a job.
  ⚠ agent executor error: generating and executing: serializing trajectory: maxMessageTokens must be positive, got 0
  Error ID: d480a155-dd4e-463e-acec-6757ea67d798-44
────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────
? for shortcuts                      Gemini 3.8 Flash · low · 1 task(s) · /tasks`

const healthyAgyPane = `● Ran 2 commands
────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────
? for shortcuts                      Gemini 3.8 Flash · low`

// brokenTranscript is an agy transcript shaped like conversation d14d9779: the director's brief, the model's
// work, a supervisor nudge last.
func brokenTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	records := []string{
		`{"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nFrom: human@kernel\n---\nGate P3 and write APPROVE/HOLD.\n---\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: x\n</ADDITIONAL_METADATA>"}`,
		`{"type":"PLANNER_RESPONSE","status":"DONE","input_tokens":2308,"content":"working"}`,
		`{"type":"SYSTEM_MESSAGE","status":"DONE","content":"Task finished"}`,
		`{"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nFrom owner session: You went quiet in the middle of a job.\n</USER_REQUEST>"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type brokenRig struct {
	supervisor *Supervisor
	now        float64
	pane       string
	restarts   int
	briefs     []string
	logged     []string
}

func newBrokenRig(t *testing.T, path string) *brokenRig {
	t.Helper()
	var sent, interrupted []string
	r := &brokenRig{now: 100000, pane: brokenAgyPane}
	r.supervisor = supervisorFor(path, "agy", true, &sent, &interrupted)
	r.supervisor.Now = func() float64 { return r.now }
	r.supervisor.Log = func(text string) { r.logged = append(r.logged, text) }
	r.supervisor.Pane = func(string, string) (string, bool) { return r.pane, true }
	r.supervisor.Recover = func(string, string) error { r.restarts++; return nil }
	r.supervisor.Brief = func(_, _, text string) error { r.briefs = append(r.briefs, text); return nil }
	return r
}

// THE INCIDENT: peak-review printed the executor error twice and was useless until a manual reseat.
func TestAnAgySeatStuckOnTheExecutorErrorIsRestartedAndBriefed(t *testing.T) {
	r := newBrokenRig(t, brokenTranscript(t))
	r.supervisor.Step()
	if r.restarts != 0 {
		t.Fatal("restarted on the first sighting; one pass must confirm it first")
	}
	r.now += agyBrokenConfirm
	r.supervisor.Step()
	if r.restarts != 1 || len(r.briefs) != 1 {
		t.Fatalf("restarts %d, briefs %d, want 1 and 1; log %q", r.restarts, len(r.briefs), r.logged)
	}
	if !strings.Contains(r.briefs[0], "Gate P3 and write APPROVE/HOLD.") {
		t.Errorf("the brief did not carry the last instruction: %q", r.briefs[0])
	}
	if strings.Contains(r.briefs[0], "You went quiet") || strings.Contains(r.briefs[0], "ADDITIONAL_METADATA") {
		t.Errorf("the brief carried a supervisor nudge or agy's wrapper: %q", r.briefs[0])
	}
}

func TestAHealthyAgyPaneIsNeverRestarted(t *testing.T) {
	r := newBrokenRig(t, brokenTranscript(t))
	r.pane = healthyAgyPane
	for range 3 {
		r.supervisor.Step()
		r.now += agyBrokenConfirm
	}
	if r.restarts != 0 {
		t.Fatalf("a healthy seat was restarted %d times", r.restarts)
	}
}

func TestAnErrorThatClearsBeforeTheConfirmIsNotActedOn(t *testing.T) {
	r := newBrokenRig(t, brokenTranscript(t))
	r.supervisor.Step()
	r.pane = healthyAgyPane
	r.now += agyBrokenConfirm
	r.supervisor.Step()
	r.pane = brokenAgyPane
	r.now += 1
	r.supervisor.Step()
	if r.restarts != 0 {
		t.Fatal("an error seen, gone, then seen again was acted on without a fresh confirm")
	}
}

func TestASeatThatBreaksAgainInsideTheCooldownIsLeftForTheLead(t *testing.T) {
	r := newBrokenRig(t, brokenTranscript(t))
	r.supervisor.Step()
	r.now += agyBrokenConfirm
	r.supervisor.Step()
	for range 4 {
		r.now += agyBrokenConfirm
		r.supervisor.Step()
	}
	if r.restarts != 1 {
		t.Fatalf("restarts %d inside the cooldown, want 1", r.restarts)
	}
	r.now += agyRecoverCooldown
	r.supervisor.Step()
	if r.restarts != 2 {
		t.Fatalf("restarts %d after the cooldown, want 2", r.restarts)
	}
}

func TestOnlyTheConversationBreakingErrorIsActedOn(t *testing.T) {
	if PaneShowsAgyBroken("  ⚠ agent executor error: 429 RESOURCE_EXHAUSTED\n>") {
		t.Error("a quota error read as the broken-conversation error; a restart does not cure it")
	}
	if !PaneShowsAgyBroken(brokenAgyPane) {
		t.Error("the measured pane was not recognised")
	}
}

func TestAnOmpSeatIsNotReadForTheAgyError(t *testing.T) {
	var sent, interrupted []string
	supervisor := supervisorFor(brokenTranscript(t), "omp", false, &sent, &interrupted)
	restarts, now := 0, 100000.0
	supervisor.Now = func() float64 { now += agyBrokenConfirm; return now }
	supervisor.Pane = func(string, string) (string, bool) { return brokenAgyPane, true }
	supervisor.Recover = func(string, string) error { restarts++; return nil }
	for range 3 {
		supervisor.Step()
	}
	if restarts != 0 {
		t.Fatal("an omp seat was restarted for an agy pane error")
	}
}
