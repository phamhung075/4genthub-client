package clientseat

import (
	"strings"
	"testing"
)

func TestWaitUntilQuietReturnsTrueOnceTheSeatIsIdleAndStill(t *testing.T) {
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "idle", true },
		Since:        func(string, string) float64 { return 99 },
		Sleep:        func(float64) {},
		Clock:        func() float64 { return 0 },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if !waiter.Wait("rig", "s") {
		t.Error("an idle seat still for longer than the quiet window was not reported quiet")
	}
}

func TestWaitUntilQuietGivesUpOnASeatThatNeverStopsRunning(t *testing.T) {
	now := 0.0
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "running", true },
		Since:        func(string, string) float64 { return 99 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Error("a seat that never stops running was reported quiet")
	}
}

func TestWaitUntilQuietDoesNotCountASeatThatJustWrote(t *testing.T) {
	now := 0.0
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "idle", true },
		Since:        func(string, string) float64 { return 2 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Error("a seat that just wrote was reported quiet")
	}
}

// AN UNREADABLE ACTIVITY KEEPS WAITING, and this is the test that fails if the direction is ever flipped back. The
// empty reading is not the answer "not running" - it is the absence of an answer, and the seat may be mid-turn while
// the read simply cannot say. The seat here is perfectly still and the clock runs out, so the activity clause is the
// only thing deciding: proceeding would reseat a live seat.
func TestWaitUntilQuietDoesNotProceedOnAnActivityItCouldNotRead(t *testing.T) {
	now := 0.0
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "", false },
		Since:        func(string, string) float64 { return 999 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Error("an unreadable activity was accepted as quiet: the wait fell through to the stillness clause alone")
	}
}

// AND THE OTHER HALF OF THE SEAM: a reading that HAPPENED but carried no word is still not "not running". The second
// answer is what separates the two - false means nobody could ask (no CLI, the call failed, the seat is not in the
// table), true-with-no-word means the table answered and had nothing to say about this seat. Both keep waiting; the
// difference is named so that a later reader cannot collapse them back into one empty string.
func TestWaitUntilQuietDoesNotProceedOnAWordlessReading(t *testing.T) {
	now := 0.0
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "", true },
		Since:        func(string, string) float64 { return 999 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Error("a reading that carried no word was accepted as quiet")
	}
}

// AND THE ARM THAT WAS WRONG, kept as the test that fails if anyone re-tightens or re-loosens this: a word that is
// not "running" is NOT therefore quiet. `needs_input` means the seat is mid-turn waiting for a person, and the old
// `!= "running"` test released a reseat there - destroying a turn that was about to continue. A stale-looking
// session mtime and a positive reading are both satisfied here, so the word alone decides.
func TestWaitUntilQuietDoesNotProceedOnAWordThatIsNotIdle(t *testing.T) {
	now := 0.0
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "needs_input", true },
		Since:        func(string, string) float64 { return 99 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Error("needs_input released the wait: stillness was allowed to satisfy a destroyer")
	}
}

// AND THE WORD THAT MAY: idle, read positively, with the session still - the only combination that proceeds.
func TestWaitUntilQuietProceedsOnIdleWithinTheQuietWindow(t *testing.T) {
	now := 0.0
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "idle", true },
		Since:        func(string, string) float64 { return 99 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if !waiter.Wait("rig", "s") {
		t.Error("an idle seat whose session has been still was not reported quiet")
	}
}

// AND THE ALIAS AT THE CONSUMER, because the form matters here too: the daemon STORES `idle-at-prompt` and serves
// `idle`, and if the stored form ever arrives at this waiter it must still open - a synonym that neither opens nor
// refuses on purpose is how a gate stays shut forever with nothing said.
func TestWaitUntilQuietProceedsOnTheStoredFormOfIdle(t *testing.T) {
	now := 0.0
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "idle-at-prompt", true },
		Since:        func(string, string) float64 { return 99 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if !waiter.Wait("rig", "s") {
		t.Error("the stored form of idle did not open the wait: the alias is where a gate goes silent")
	}
}

// AND THE SENTENCE THAT TURNS THE NEXT VOCABULARY GROWTH INTO A FIVE-MINUTE FIX: a word nobody recognizes is NAMED,
// once, when the wait gives up - not swallowed into a gate that simply never opens.
func TestWaitUntilQuietNamesTheWordItDidNotRecognize(t *testing.T) {
	now := 0.0
	var said []string
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "drifting", true },
		Since:        func(string, string) float64 { return 999 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		Report:       func(message string) { said = append(said, message) },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Fatal("a word nobody recognizes released the wait")
	}
	if len(said) != 1 {
		t.Fatalf("the wait said %d sentences about an unrecognized word, want exactly one: %v", len(said), said)
	}
	if !strings.Contains(said[0], `"drifting"`) || !strings.Contains(said[0], "not one this client recognizes") {
		t.Errorf("the sentence does not name the word it could not read: %q", said[0])
	}
}

// AND A RECOGNIZED REFUSAL STAYS QUIET: `working` is a word this client knows, so its refusal is deliberate and there
// is nothing to report - the report exists for words nobody has seen, not for seats that are simply busy.
func TestWaitUntilQuietDoesNotReportARecognizedWord(t *testing.T) {
	now := 0.0
	var said []string
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "working", true },
		Since:        func(string, string) float64 { return 999 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		Report:       func(message string) { said = append(said, message) },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Fatal("a working seat released the wait")
	}
	if len(said) != 0 {
		t.Errorf("a recognized word was reported as unrecognized: %v", said)
	}
}

// WHEN THE SENTENCE IS SAID MATTERS AS MUCH AS WHAT IT SAYS. Reported once at the end of the wait, the sentence
// arrived after the full budget with the caller already gone; reported on the first reading that cannot open, it
// arrives inside one poll. The elapsed clock is the assertion: the report must land far below WaitSeconds.
func TestWaitUntilQuietNamesTheWordOnItsFirstReading(t *testing.T) {
	now := 0.0
	var saidAt []float64
	var said []string
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "drifting", true },
		Since:        func(string, string) float64 { return 999 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		Report:       func(message string) { said = append(said, message); saidAt = append(saidAt, now) },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Fatal("a word nobody recognizes released the wait")
	}
	if len(said) != 1 {
		t.Fatalf("want exactly one sentence, not one per poll: %v", said)
	}
	if saidAt[0] > 2*pollSeconds {
		t.Errorf("the word was named %vs in, at the end of a %v wait rather than on the first reading",
			saidAt[0], quietWaitSeconds)
	}
}

// AND SO IS THE OTHER WAY TO A WAIT THAT NEVER OPENS, which is the one this box hits: with no activity read at all
// there is no word to name, so the wait used to say NOTHING for its whole budget and the caller's only evidence was
// silence. Once, on the first reading, with the budget named.
func TestWaitUntilQuietNamesAnUnreadableActivityOnItsFirstReading(t *testing.T) {
	now := 0.0
	var saidAt []float64
	var said []string
	waiter := QuietWaiter{
		Activity:     func(string, string) (string, bool) { return "", false },
		Since:        func(string, string) float64 { return 0 },
		Sleep:        func(seconds float64) { now += seconds },
		Clock:        func() float64 { return now },
		Report:       func(message string) { said = append(said, message); saidAt = append(saidAt, now) },
		QuietSeconds: quietSeconds, WaitSeconds: quietWaitSeconds, PollSeconds: pollSeconds,
	}
	if waiter.Wait("rig", "s") {
		t.Fatal("a seat with nothing readable released the wait")
	}
	if len(said) != 1 {
		t.Fatalf("want exactly one sentence, not one per poll: %v", said)
	}
	if !strings.Contains(said[0], "no activity could be read at all") || !strings.Contains(said[0], quietBudget()) {
		t.Errorf("the sentence does not name the unreadable activity and its budget (%s): %q", quietBudget(), said[0])
	}
	if saidAt[0] > 2*pollSeconds {
		t.Errorf("the unreadable activity was named %vs in, at the end of a %v wait", saidAt[0], quietWaitSeconds)
	}
}

