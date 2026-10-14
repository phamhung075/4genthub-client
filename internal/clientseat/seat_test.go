package clientseat

import (
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func TestASeatIsBehindWhenItsPinDiffersOrIsMissing(t *testing.T) {
	cloud := []SeatHash{{"lead", "h2"}, {"go-dev", "h1"}, {"writer", "h3"}}
	pinned := map[string]string{"lead": "h1", "go-dev": "h1"}
	got := SeatsBehind(cloud, pinned)
	if strings.Join(got, ",") != "lead,writer" {
		t.Errorf("behind = %v, want [lead writer] (a differing pin and a missing one)", got)
	}
}

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

// fakeSource stands in for the cloud and the pins on disk: a fixed cloud, and pins held in a map.
type fakeSource struct {
	cloud []SeatHash
	pins  map[string]string
}

func (f *fakeSource) CloudHashes(string) ([]SeatHash, error) { return f.cloud, nil }

func (f *fakeSource) ReadPin(_, _, seat string) (string, bool, error) {
	hash, ok := f.pins[seat]
	return hash, ok, nil
}

func testOptions(over func(*options)) options {
	parsed := options{room: "room", out: "/nonexistent", relaunch: "none"}
	if over != nil {
		over(&parsed)
	}
	return parsed
}

// testDeps records the adopt command and the relaunches; onRun lets a test simulate the pin moving.
func testDeps(src source, runs *[]string, relaunched *[]string, waitQuiet func(rig, seat string) bool, onRun func()) *syncDeps {
	return &syncDeps{
		src: src,
		runSyncRig: func(room, out string) error {
			*runs = append(*runs, strings.Join(SyncRigArgs("4genteam", room, out), " "))
			if onRun != nil {
				onRun()
			}
			return nil
		},
		waitQuiet: waitQuiet,
		relaunch: func(rig, seat string) error {
			*relaunched = append(*relaunched, seat)
			return nil
		},
		logf: func(string, ...any) {},
	}
}

func TestSyncDoesNothingWhenEverySeatIsInSync(t *testing.T) {
	var runs, relaunched []string
	src := &fakeSource{cloud: []SeatHash{{"lead", "h1"}}, pins: map[string]string{"lead": "h1"}}
	code, err := syncOnce(testOptions(nil), testDeps(src, &runs, &relaunched, nil, nil))
	if err != nil || code != clientcmd.ExitOK {
		t.Fatalf("code = %d, err = %v, want ok", code, err)
	}
	if len(runs) != 0 {
		t.Errorf("a synced room ran the adopt step: %v", runs)
	}
}

func TestSyncAdoptsThroughTheSyncCommandAndLeavesARunningSeatAlone(t *testing.T) {
	var runs, relaunched []string
	src := &fakeSource{cloud: []SeatHash{{"lead", "h2"}}, pins: map[string]string{"lead": "h1"}}
	deps := testDeps(src, &runs, &relaunched, nil, func() { src.pins["lead"] = "h2" })
	code, err := syncOnce(testOptions(nil), deps)
	if err != nil || code != clientcmd.ExitOK {
		t.Fatalf("code = %d, err = %v, want ok", code, err)
	}
	if len(runs) != 1 || !strings.Contains(runs[0], "rig") || !strings.Contains(runs[0], "--update") {
		t.Errorf("the adopt step was %v, want one `sync rig ... --update`", runs)
	}
	if len(relaunched) != 0 {
		t.Errorf("--relaunch none restarted %v", relaunched)
	}
}

func TestSyncFailsLoudlyWhenThePinDidNotMoveToTheCloudHash(t *testing.T) {
	var runs, relaunched []string
	src := &fakeSource{cloud: []SeatHash{{"lead", "h2"}}, pins: map[string]string{"lead": "h1"}}
	_, err := syncOnce(testOptions(nil), testDeps(src, &runs, &relaunched, nil, nil))
	if err == nil || !strings.Contains(err.Error(), "pin did not move") {
		t.Fatalf("err = %v, want the pin-did-not-move failure", err)
	}
}

func TestRelaunchQuietRestartsOnlyTheSeatsThatChangedAndAreQuiet(t *testing.T) {
	var runs, relaunched []string
	src := &fakeSource{
		cloud: []SeatHash{{"lead", "h2"}, {"go-dev", "h1"}, {"writer", "h9"}},
		pins:  map[string]string{"lead": "h1", "go-dev": "h1", "writer": "h1"},
	}
	deps := testDeps(src, &runs, &relaunched,
		func(_, seat string) bool { return seat == "lead" },
		func() { src.pins["lead"] = "h2"; src.pins["writer"] = "h9" })
	code, err := syncOnce(testOptions(func(o *options) { o.relaunch = "quiet" }), deps)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if code != ExitFailed {
		t.Errorf("code = %d, want %d when a changed seat never went quiet", code, ExitFailed)
	}
	if strings.Join(relaunched, ",") != "lead" {
		t.Errorf("relaunched = %v, want only the seat that went quiet", relaunched)
	}
}

func TestTheSeatFilterLimitsTheWork(t *testing.T) {
	var runs, relaunched []string
	src := &fakeSource{
		cloud: []SeatHash{{"lead", "h2"}, {"writer", "h9"}},
		pins:  map[string]string{"lead": "h1", "writer": "h1"},
	}
	deps := testDeps(src, &runs, &relaunched, nil, func() { src.pins["lead"] = "h2" })
	code, err := syncOnce(testOptions(func(o *options) { o.seats = []string{"lead"} }), deps)
	if err != nil || code != clientcmd.ExitOK {
		t.Fatalf("code = %d, err = %v, want ok", code, err)
	}
	if len(runs) != 1 {
		t.Errorf("runs = %v, want one adopt step for the filtered seat", runs)
	}
}
