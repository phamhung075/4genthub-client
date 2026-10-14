package clientlifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

// withDocumentedLimits pins the three limits and restores them, so the suite does not move with the
// operator's env file - the Python suite's the_documented_limits fixture does the same thing.
func withDocumentedLimits(t *testing.T) {
	t.Helper()
	// Every Step test goes through this helper, so the heartbeat seam is installed here too: a Step that
	// beats must not write into the machine's own log directory.
	stubHeartbeat(t)
	compact, warn, hard := seatlog.CompactLimit, seatlog.WarnLimit, seatlog.HardLimit
	t.Cleanup(func() { seatlog.CompactLimit, seatlog.WarnLimit, seatlog.HardLimit = compact, warn, hard })
	seatlog.CompactLimit, seatlog.WarnLimit, seatlog.HardLimit = 150_000, 250_000, 300_000
}

// seatLogFile writes the one-record session log the passes read, with a chosen age.
func seatLogFile(t *testing.T, tokens int, ageSeconds float64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	record, err := json.Marshal(map[string]any{
		"message": map[string]any{"usage": map[string]any{"totalTokens": tokens}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(record, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Duration(ageSeconds * float64(time.Second)))
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

// supervisorFor is one pass over one seat, with every action recorded instead of taken.
func supervisorFor(path, runtime string, working bool, sent *[]string, interrupted *[]string) *Supervisor {
	supervisor := &Supervisor{Rig: "r", Quiet: 180, State: map[string]*seatState{}}
	supervisor.Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	supervisor.Seats = func(string) []string { return []string{"lead"} }
	supervisor.SeatLog = func(string, string) (string, string) { return path, runtime }
	supervisor.Idle = func(string, string) bool { return !working }
	supervisor.Say = func(_, _, text string, raw bool) {
		if raw {
			*sent = append(*sent, "/compact RAW")
			return
		}
		*sent = append(*sent, text)
	}
	supervisor.RPCCompact = func(string, string) string { *sent = append(*sent, "RPC"); return "sent" }
	supervisor.Abort = func(string, string) { *sent = append(*sent, "ABORT") }
	supervisor.Interrupt = func(_, seat string) { *interrupted = append(*interrupted, seat) }
	supervisor.Log = func(string) {}
	// The off-peak gate is OFF for every other test in this file: they are about the compaction logic,
	// so the resume path must behave exactly as it did before the gate existed. The gate's own tests set
	// both seams themselves.
	supervisor.Nodes = func(string) []map[string]any { return nil }
	supervisor.Getenv = func(key string) string {
		if key == clientoffpeak.ModeEnv {
			return clientoffpeak.ValueOff
		}
		return ""
	}
	return supervisor
}

func firstFive(sent []string) []string {
	out := make([]string, 0, len(sent))
	for _, text := range sent {
		if len(text) > 5 {
			text = text[:5]
		}
		out = append(out, text)
	}
	return out
}

func TestASeatUnderTheLimitIsLeftAlone(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, seatlog.CompactLimit-1, 999), "omp", false, &sent, &interrupted)
	supervisor.Step()
	if len(sent) != 0 {
		t.Errorf("a seat under the limit was acted on: %v", sent)
	}
}

func TestASeatPastTheLimitStillWorkingIsToldOnceAndNotCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	path := seatLogFile(t, 200_000, 5)
	supervisor := supervisorFor(path, "omp", true, &sent, &interrupted)
	supervisor.Step()
	if len(sent) != 1 || !strings.Contains(sent[0], "Context limit") || !strings.Contains(sent[0], "Do not type /compact") {
		t.Fatalf("first pass sent %v, want the one notice", sent)
	}
	sent = nil
	supervisor.Step()
	if len(sent) != 0 {
		t.Errorf("the notice was repeated: %v", sent)
	}
}

func TestAQuietSeatPastTheLimitIsCompactedAfterTheNotice(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 200_000, 600), "omp", false, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,/comp" {
		t.Errorf("sent %v, want the notice then the raw /compact", got)
	}
}

func TestASeatThatDroppedBelowTheLimitIsToldAgainNextTime(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 200_000, 5), "omp", false, &sent, &interrupted)
	supervisor.Step()
	supervisor.SeatLog = func(string, string) (string, string) {
		return seatLogFile(t, 40_000, 5), "omp"
	}
	supervisor.Step()
	if supervisor.State["lead"].told {
		t.Error("a seat that dropped below the limit is still marked as told")
	}
}

func TestAnOmpSeatPastTheWarnLimitIsCompactedNowWithoutAnAbort(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "omp", true, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,VERY ,RPC" {
		t.Errorf("sent %v, want notice, warning then the RPC compact", got)
	}
}

func TestAnOmpSeatAtTheHardLimitIsAbortedThenCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 320_000, 5), "omp", true, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,VERY ,ABORT,RPC" {
		t.Errorf("sent %v, want the abort before the RPC compact", got)
	}
}

func TestAnIdleSeatOfAnotherRuntimePastTheWarnLimitIsSentCompactWithoutWaitingForQuiet(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "claude-code", false, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,VERY ,/comp" {
		t.Errorf("sent %v, want notice, warning then /compact", got)
	}
}

func TestAWorkingSeatOfAnotherRuntimePastTheWarnLimitWaitsThenIsInterruptedAtTheHardLimit(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "claude-code", true, &sent, &interrupted)
	supervisor.Step()
	if strings.Contains(strings.Join(sent, "|"), "/compact RAW") || len(interrupted) != 0 {
		t.Fatalf("a working seat past the warn limit was acted on early: sent %v, interrupted %v", sent, interrupted)
	}
	hard := seatLogFile(t, 320_000, 5)
	supervisor.SeatLog = func(string, string) (string, string) { return hard, "claude-code" }
	supervisor.Step()
	supervisor.Step()
	if len(interrupted) != 1 || interrupted[0] != "lead" {
		t.Errorf("interrupted = %v, want exactly one interrupt of lead (one per gap)", interrupted)
	}
}

// THE PURE HALF: what the read's word means, and what a caller can SAY about it. A recognized word is a deliberate
// answer - the seat is busy - and has no sentence; a word nobody knows, an absent field, and an empty word are the
// three ways a gate goes silent, and each says which one it was.
func TestIdleFromActivityWordNamesWhatItCouldNotRead(t *testing.T) {
	cases := []struct {
		name    string
		word    string
		present bool
		idle    bool
		want    string
	}{
		{"idle", "idle", true, true, ""},
		{"the stored form releases too", "idle-at-prompt", true, true, ""},
		{"a busy seat is deliberate and silent", "working", true, false, ""},
		{"needs_input is busy too", "needs_input", true, false, ""},
		{"a word nobody knows is named", "drifting", true, false, "drifting"},
		{"no agentActivity field at all", "", false, false, "no agentActivity"},
		{"a field that carried no word", "", true, false, "was empty"},
	}
	for _, c := range cases {
		idle, why := idleFromActivityWord(c.word, c.present)
		if idle != c.idle {
			t.Errorf("%s: idle=%v, want %v", c.name, idle, c.idle)
		}
		if c.want == "" {
			if why != "" {
				t.Errorf("%s: said %q, want nothing (a deliberate refusal is not a failure to read)", c.name, why)
			}
			continue
		}
		if !strings.Contains(why, c.want) {
			t.Errorf("%s: said %q, which does not name %q", c.name, why, c.want)
		}
	}
}

// AND THE SENTENCE LANDS WHERE THE FALSE IS ACTED ON - the hard-limit branch, which interrupts and returns every
// pass, so the quiet-triggered compact below it never fires. Without this line a read that answered nothing reads
// exactly like a seat that is simply working: a supervisor that never compacts, saying nothing.
func TestASeatThatCouldNotBeReadSaysWhyItWasNotCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted, logged []string
	hard := seatLogFile(t, 320_000, 5)
	supervisor := supervisorFor(hard, "claude-code", true, &sent, &interrupted)
	supervisor.Log = func(text string) { logged = append(logged, text) }
	supervisor.IdleWhy = func(string, string) string {
		return "the node table carries no agentActivity for this seat, so nothing can say whether it is idle"
	}
	supervisor.Step()
	supervisor.Step()
	if len(interrupted) != 1 {
		t.Fatalf("interrupted = %v, want exactly one", interrupted)
	}
	said := strings.Join(logged, "|")
	if !strings.Contains(said, "not compacting") || !strings.Contains(said, "no agentActivity") {
		t.Errorf("the pass acted on a false without saying why: %v", logged)
	}

	// AND A BUSY SEAT STAYS QUIET: a deliberate refusal carries no sentence, so the acting path says nothing about it.
	var quiet []string
	busy := supervisorFor(hard, "claude-code", true, &sent, &interrupted)
	busy.Log = func(text string) { quiet = append(quiet, text) }
	busy.IdleWhy = func(string, string) string { return "" }
	busy.Step()
	busy.Step()
	if strings.Contains(strings.Join(quiet, "|"), "not compacting") {
		t.Errorf("a deliberate refusal was reported as a failure to read: %v", quiet)
	}
}

func TestASeatPastTheWarnLimitIsWarnedOnce(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "omp", false, &sent, &interrupted)
	supervisor.Step()
	if !strings.Contains(strings.Join(sent, "|"), "VERY IMPORTANT") {
		t.Fatalf("no warning on the first pass: %v", sent)
	}
	sent = nil
	supervisor.Step()
	if strings.Contains(strings.Join(sent, "|"), "VERY IMPORTANT") {
		t.Errorf("the warning was repeated: %v", sent)
	}
}

// TestAWitnessedCompactionTellsTheSeatToResume covers the path the nine behaviours above miss: a
// compaction the supervisor can SEE. The witness is a new `"type":"compaction"` record, and the seat is
// then told to carry on without the owner typing continue.
func TestAWitnessedCompactionTellsTheSeatToResume(t *testing.T) {
	withDocumentedLimits(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// The compaction record is both the witness and the new reading: 40k is under the soft limit, so
	// this pass is about the witness alone.
	if err := os.WriteFile(path, []byte(`{"type":"compaction","tokensAfter":40000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sent, interrupted, logged []string
	supervisor := supervisorFor(path, "omp", false, &sent, &interrupted)
	supervisor.Log = func(text string) { logged = append(logged, text) }
	supervisor.State["lead"] = &seatState{before: 300_000, hasBefore: true, count: 0, sent: supervisor.Now() - 200}

	supervisor.Step()

	if len(sent) != 1 || sent[0] != resumeText {
		t.Errorf("sent = %v, want exactly the resume text", sent)
	}
	joined := strings.Join(logged, "|")
	if !strings.Contains(joined, "WITNESSED") {
		t.Errorf("the witnessed branch was not reported: %v", logged)
	}
	if !strings.Contains(joined, "told to resume") {
		t.Errorf("the resume was not logged: %v", logged)
	}
}

// TestAnUnwitnessedCompactionIsReportedAndRetried covers the other half: the compaction never appeared
// and the context did not drop, so the supervisor says so and tries again instead of assuming success.
func TestAnUnwitnessedCompactionIsReportedAndRetried(t *testing.T) {
	withDocumentedLimits(t)
	// A reading above the soft limit with NO compaction record and no drop: the send is unwitnessed.
	path := seatLogFile(t, 200_000, 5)
	var sent, interrupted, logged []string
	supervisor := supervisorFor(path, "omp", false, &sent, &interrupted)
	supervisor.Log = func(text string) { logged = append(logged, text) }
	supervisor.State["lead"] = &seatState{before: 200_000, hasBefore: true, count: 0, sent: supervisor.Now() - 200}

	supervisor.Step()

	for _, text := range sent {
		if text == resumeText {
			t.Error("an unwitnessed compaction told the seat to resume")
		}
	}
	if !strings.Contains(strings.Join(logged, "|"), "NOT witnessed") {
		t.Errorf("the unwitnessed branch was not reported: %v", logged)
	}
	if supervisor.State["lead"].hasBefore {
		t.Error("the unwitnessed branch left the pending witness in place")
	}
}

// gatedSupervisor is one pass on the resume path with a WITNESSED compaction, and the off-peak gate's
// two inputs wired to this suite's seams: the node reading, and the environment (which is where both
// the setting and the UTC clock pin live - clientoffpeak reads them through this one function).
// `mode` is what OFFPEAK_ONLY answers and `pin` what OFFPEAK_UTC answers.
func gatedSupervisor(t *testing.T, mode, pin string, nodes []map[string]any) (*Supervisor, *[]string, *[]string) {
	t.Helper()
	withDocumentedLimits(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// The compaction record is both the witness and the new reading: 40k is under the soft limit, so the
	// pass is about the resume alone.
	if err := os.WriteFile(path, []byte(`{"type":"compaction","tokensAfter":40000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sent, interrupted, logged []string
	supervisor := supervisorFor(path, "omp", false, &sent, &interrupted)
	supervisor.Log = func(text string) { logged = append(logged, text) }
	supervisor.Nodes = func(string) []map[string]any { return nodes }
	supervisor.Getenv = func(key string) string {
		switch key {
		case clientoffpeak.ModeEnv:
			return mode
		case clientoffpeak.PinEnv:
			return pin
		}
		// The holiday file stays empty, so the machine's own cannot change what these tests assert.
		return ""
	}
	supervisor.State["lead"] = &seatState{before: 300_000, hasBefore: true, count: 0, sent: supervisor.Now() - 200}
	return supervisor, &sent, &logged
}

// TestAPeakResumeIsHeldByTheOffPeakGate: the witnessed compaction is an automatic wake, so at peak the
// resume text must NOT go out and the supervisor's own log must record WHY.
func TestAPeakResumeIsHeldByTheOffPeakGate(t *testing.T) {
	supervisor, sent, logged := gatedSupervisor(t, "", "2026-10-09 02:00", deepSeekRoomNode())
	supervisor.Step()
	if len(*sent) != 0 {
		t.Fatalf("the resume was sent at peak: %v", *sent)
	}
	joined := strings.Join(*logged, "|")
	if !strings.Contains(joined, "PEAK") {
		t.Errorf("a held resume was not logged with its reason: %v", *logged)
	}
}

// TestAnOffPeakResumeIsSentExactlyAsBefore: the same witnessed compaction off-peak sends the resume.
func TestAnOffPeakResumeIsSentExactlyAsBefore(t *testing.T) {
	supervisor, sent, logged := gatedSupervisor(t, "", "2026-10-09 05:00", deepSeekRoomNode())
	supervisor.Step()
	if len(*sent) != 1 || (*sent)[0] != resumeText {
		t.Errorf("sent = %v, want exactly the resume text", *sent)
	}
	if !strings.Contains(strings.Join(*logged, "|"), "told to resume") {
		t.Errorf("the resume was not logged: %v", *logged)
	}
}

// TestTheResumeIsSentWhenTheGateIsOff: OFFPEAK_ONLY=0 is no check at all - not even the clock is read -
// so a peak instant cannot hold the resume back. That is the behaviour this path had before the gate.
func TestTheResumeIsSentWhenTheGateIsOff(t *testing.T) {
	supervisor, sent, _ := gatedSupervisor(t, clientoffpeak.ValueOff, "2026-10-09 02:00", deepSeekRoomNode())
	supervisor.Step()
	if len(*sent) != 1 || (*sent)[0] != resumeText {
		t.Errorf("sent = %v, want exactly the resume text while the gate is off", *sent)
	}
}

// THE PROPERTY, PINNED THE WAY IT HAS TO BE PINNED: this FAILS IF AN ENVELOPE APPEARS rather than recording that
// none is there today. rigAbort's text is the runner's keystroke, so the assertion is on the EXACT string - a
// "From NAME:" prefix would arrive as a MESSAGE where the runner needs "/abort", which is the difference between
// aborting a turn and sending a seat a note about aborting one.
func TestRigAbortSendsTheBareKeystrokeWithNoEnvelope(t *testing.T) {
	previous := runtimeSendText
	t.Cleanup(func() { runtimeSendText = previous })

	var gotSession, gotText string
	runtimeSendText = func(session, text string) error {
		gotSession, gotText = session, text
		return nil
	}

	rigAbort("room1", "alpha")

	if gotSession != "room1-alpha@room1" {
		t.Errorf("session = %q, want the room's canonical session name room1-alpha@room1", gotSession)
	}
	if gotText != "/abort" {
		t.Errorf("barrier text = %q, want the bare /abort: an envelope or any other text arrives as a MESSAGE, "+
			"not the keystroke the omp runner aborts a turn on", gotText)
	}
}

// rigSay's two shapes, pinned the same way: a plain message carries the "From" envelope the CLI used to write, and a
// raw one is the exact text - a /compact wrapped in an envelope arrives as prose the runner cannot act on.
func TestRigSayEnvelopesAPlainMessageAndSendsTheRawOneBare(t *testing.T) {
	previous := runtimeSendText
	t.Cleanup(func() { runtimeSendText = previous })

	var sessions, texts []string
	runtimeSendText = func(session, text string) error {
		sessions, texts = append(sessions, session), append(texts, text)
		return nil
	}

	rigSay("room1", "alpha", "context was compacted", false)
	rigSay("room1", "alpha", "/compact", true)

	if len(texts) != 2 {
		t.Fatalf("sends = %d, want 2", len(texts))
	}
	for i, session := range sessions {
		if session != "room1-alpha@room1" {
			t.Errorf("send %d session = %q, want the room's canonical session name room1-alpha@room1", i, session)
		}
	}
	if want := "From " + saySender + ": context was compacted"; texts[0] != want {
		t.Errorf("plain message = %q, want %q: the runtime's send types exactly the bytes it is given, so the caller "+
			"writes the envelope the CLI used to add", texts[0], want)
	}
	if texts[1] != "/compact" {
		t.Errorf("raw message = %q, want the bare /compact: an envelope arrives as a MESSAGE where the runner needs "+
			"the command", texts[1])
	}
}

// THE ABSENCE, pinned the way the seat-send conversion pinned its own: with no `rig` on PATH the send must still reach
// the runtime. A fallback to the rig tool would return early and silently, which this test cannot see unless it
// empties PATH.
func TestRigSayNeverShellsOutToTheRigTool(t *testing.T) {
	previous := runtimeSendText
	t.Cleanup(func() { runtimeSendText = previous })

	called := false
	runtimeSendText = func(session, text string) error { called = true; return nil }
	t.Setenv("PATH", t.TempDir())

	rigSay("room1", "alpha", "hello", false)
	if !called {
		t.Fatal("rigSay did not reach the runtime with no `rig` on PATH: it shelled out to the rig tool instead")
	}
}
