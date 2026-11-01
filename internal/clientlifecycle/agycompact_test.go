package clientlifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

const (
	agyReading    = `{"type":"PLANNER_RESPONSE","status":"DONE","input_tokens":%d,"cache_read_tokens":0,"output_tokens":1}`
	agyCheckpoint = `{"type":"CHECKPOINT","status":"DONE","content":"# Resuming from a compaction"}`
)

type triggerFixture struct {
	t       *testing.T
	trigger *compactTrigger
	dir     string
	path    string
	now     float64
	spawned int
}

func newTriggerFixture(t *testing.T, tokens int) *triggerFixture {
	t.Helper()
	root := t.TempDir()
	f := &triggerFixture{t: t, dir: clientruntime.SeatDir(root, "room", "seat"), now: 1000}
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.path = filepath.Join(root, "transcript.jsonl")
	f.write(tokens)
	f.trigger = &compactTrigger{Root: root, Room: "room", Seat: "seat", Now: func() float64 { return f.now },
		Spawn: func(string, string, string) error { f.spawned++; return nil }}
	limit := seatlog.CompactLimit
	seatlog.CompactLimit = 100
	t.Cleanup(func() { seatlog.CompactLimit = limit })
	return f
}

func (f *triggerFixture) write(tokens int) {
	f.t.Helper()
	line := strings.Replace(agyReading, "%d", itoa(tokens), 1)
	if err := os.WriteFile(f.path, []byte(line+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func (f *triggerFixture) payload() agyHookPayload {
	return agyHookPayload{ConversationID: "conv", TranscriptPath: f.path}
}

func TestUnderTheLimitTheHookAddsNothingAndWritesNoClaim(t *testing.T) {
	f := newTriggerFixture(t, 50)
	if got := f.trigger.preInvocation(f.payload()); got != "{}" {
		t.Fatalf("answer = %s", got)
	}
	if got := f.trigger.stop(f.payload()); got != "{}" || f.spawned != 0 {
		t.Fatalf("stop = %s, spawned %d", got, f.spawned)
	}
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("a claim was written under the limit")
	}
}

func TestOverTheLimitTheHookTellsThenTerminatesThenTheStopSendsOnce(t *testing.T) {
	f := newTriggerFixture(t, 500)
	first := f.trigger.preInvocation(f.payload())
	if !strings.Contains(first, "ephemeralMessage") || !strings.Contains(first, "End your turn NOW") {
		t.Fatalf("first answer = %s", first)
	}
	if second := f.trigger.preInvocation(f.payload()); second != `{"terminationBehavior":"terminate"}` {
		t.Fatalf("second answer = %s", second)
	}
	if f.spawned != 0 {
		t.Fatal("/compact was arranged to go out before the turn ended")
	}
	f.trigger.stop(f.payload())
	f.trigger.stop(f.payload())
	if f.spawned != 1 {
		t.Fatalf("the sender started %d times, want once", f.spawned)
	}
	// The /compact is on its way: a further model call is left alone, or the compact's own turn would be stopped.
	if got := f.trigger.preInvocation(f.payload()); got != "{}" {
		t.Fatalf("answer after the send = %s", got)
	}
}

func TestAStopPastTheLimitWithNoToolCallStillCompacts(t *testing.T) {
	f := newTriggerFixture(t, 500)
	f.trigger.stop(f.payload())
	if f.spawned != 1 {
		t.Fatalf("spawned = %d", f.spawned)
	}
}

func TestAStopThatCannotCompactLeavesTheSeatAlone(t *testing.T) {
	f := newTriggerFixture(t, 500)
	f.trigger.stop(agyHookPayload{TerminationReason: "ERROR", TranscriptPath: f.path})
	if f.spawned != 0 {
		t.Fatal("a broken conversation: the sender started")
	}
}

// aa2-gemini, 2026-11-01: a Stop with a background grep running sent nothing, agy fires no second Stop until that work
// ends, and the seat sat at its prompt holding the claim. A /compact typed at such a prompt runs (measured on agy 1.4.12).
func TestAStopWithBackgroundWorkStillSends(t *testing.T) {
	f := newTriggerFixture(t, 500)
	f.trigger.preInvocation(f.payload())
	busy := false
	payload := f.payload()
	payload.FullyIdle = &busy
	f.trigger.stop(payload)
	if f.spawned != 1 {
		t.Fatalf("spawned = %d", f.spawned)
	}
}

// The second half of the same incident: a start that fails must not leave a claim that says it was sent.
func TestASenderThatCannotStartReleasesTheClaim(t *testing.T) {
	f := newTriggerFixture(t, 500)
	f.trigger.Spawn = func(string, string, string) error { return errors.New("no fork") }
	f.trigger.stop(f.payload())
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("the claim outlived a sender that never started")
	}
}

// aa2-gemini, 2026-11-01: reseated under a claim, the new conversation's first message was terminated by the old claim,
// and its Stop typed /compact over it: "Nothing to compact yet".
func TestANewConversationIsNotHeldToTheOldOnesClaim(t *testing.T) {
	f := newTriggerFixture(t, 500)
	f.trigger.preInvocation(agyHookPayload{ConversationID: "old", TranscriptPath: f.path})
	f.write(50) // the new conversation's own transcript, under the limit
	fresh := agyHookPayload{ConversationID: "new", TranscriptPath: f.path}
	if answer := f.trigger.preInvocation(fresh); answer != "{}" {
		t.Fatalf("the first message of the new conversation was answered %s", answer)
	}
	f.trigger.stop(fresh)
	if f.spawned != 0 {
		t.Fatal("a /compact was sent to a conversation with nothing to compact")
	}
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("the old conversation's claim survived")
	}
}

func TestAConversationIsReadFromItsTranscriptPath(t *testing.T) {
	if got := conversationOf("/home/x/.gemini/antigravity-cli/brain/abc-1/.system_generated/logs/transcript.jsonl"); got != "abc-1" {
		t.Fatalf("got %q", got)
	}
	if got := conversationOf("/tmp/transcript.jsonl"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestAStopMarksTheSeatAtItsPromptUntilTheNextModelCall(t *testing.T) {
	f := newTriggerFixture(t, 50)
	path := "/home/x/brain/conv/.system_generated/logs/transcript.jsonl"
	f.trigger.stop(f.payload())
	if !agyAtPrompt(f.dir, path) {
		t.Fatal("a Stop did not mark the seat at its prompt")
	}
	if agyAtPrompt(f.dir, "/home/x/brain/other/.system_generated/logs/transcript.jsonl") {
		t.Fatal("another conversation read as at its prompt")
	}
	f.trigger.preInvocation(f.payload())
	if agyAtPrompt(f.dir, path) {
		t.Fatal("a running turn read as at its prompt")
	}
}

func TestTheClaimStopsBothSidesFromCompacting(t *testing.T) {
	f := newTriggerFixture(t, 500)
	f.trigger.preInvocation(f.payload())
	if supervisorClaim(f.dir, 500, 0, f.now) {
		t.Fatal("the supervisor took a claim the hook holds")
	}
	if !hookHoldsClaim(f.dir, f.now) {
		t.Fatal("the supervisor is not told the hook holds the seat")
	}

	f = newTriggerFixture(t, 500)
	if !supervisorClaim(f.dir, 500, 0, f.now) {
		t.Fatal("the supervisor could not take a free claim")
	}
	if hookHoldsClaim(f.dir, f.now) {
		t.Fatal("the supervisor's claim reads as the hook's")
	}
	if got := f.trigger.preInvocation(f.payload()); got != "{}" {
		t.Fatalf("the hook answered %s under the supervisor's claim", got)
	}
	f.trigger.stop(f.payload())
	if f.spawned != 0 {
		t.Fatal("the hook sent under the supervisor's claim")
	}
}

func TestAClaimPastTheWindowIsFreeForEitherSide(t *testing.T) {
	f := newTriggerFixture(t, 500)
	f.trigger.preInvocation(f.payload())
	f.now += witnessWindow + 1
	if hookHoldsClaim(f.dir, f.now) {
		t.Fatal("a dead sender's claim still holds the seat")
	}
	if !supervisorClaim(f.dir, 500, 0, f.now) {
		t.Fatal("the backstop could not take an expired claim")
	}
}

func TestAStaleLockIsTakenOver(t *testing.T) {
	f := newTriggerFixture(t, 500)
	lock := filepath.Join(f.dir, compactClaimFile+".lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * claimLockStale)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if !supervisorClaim(f.dir, 500, 0, f.now) {
		t.Fatal("a dead process's lock blocked the claim")
	}
}

func TestReadingsBeforeACheckpointAreNotTheContext(t *testing.T) {
	f := newTriggerFixture(t, 500)
	raw, _ := os.ReadFile(f.path)
	if err := os.WriteFile(f.path, append(raw, []byte(agyCheckpoint+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, has := lastContext(f.path); has {
		t.Fatal("the size before the compaction was read as the context after it")
	}
	if got := f.trigger.preInvocation(f.payload()); got != "{}" {
		t.Fatalf("the resume's first model call arranged a second compact: %s", got)
	}
}

func TestTheHookMarksItsConversationLive(t *testing.T) {
	f := newTriggerFixture(t, 50)
	if compactHookLive(f.dir) {
		t.Fatal("live before any hook ran")
	}
	f.trigger.preInvocation(f.payload())
	if compactHookLive(f.dir) {
		t.Fatal("live with no conversation on record")
	}
	if err := os.WriteFile(filepath.Join(f.dir, "agy-conversation"), []byte("conv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !compactHookLive(f.dir) {
		t.Fatal("the conversation that ran the hook does not read live")
	}
	if err := os.WriteFile(filepath.Join(f.dir, "agy-conversation"), []byte("newer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if compactHookLive(f.dir) {
		t.Fatal("a newer conversation that has run no hook reads live")
	}
}

func TestOutsideASeatTheHookAnswersEmpty(t *testing.T) {
	t.Setenv("AGENTHUB_ROOM", "")
	t.Setenv("AGENTHUB_SEAT", "")
	var stderr bytes.Buffer
	if got := compactHookAnswer("pre-invocation", strings.NewReader("not json"), &stderr); got != "{}" {
		t.Fatalf("answer = %s", got)
	}
}

// senderFixture has a claim the Stop already marked sent, and a transcript the fake /compact writes a CHECKPOINT into.
func senderFixture(t *testing.T, tokens int) (*triggerFixture, *compactSender, *[]string) {
	t.Helper()
	f := newTriggerFixture(t, tokens)
	f.trigger.stop(f.payload())
	var said []string
	clock := f.now
	sender := &compactSender{Root: f.trigger.Root, Room: "room", Seat: "seat",
		Sleep: func(d time.Duration) { clock += d.Seconds() },
		Now:   func() float64 { return clock },
		Say: func(_, _, text string, raw bool) error {
			said = append(said, text)
			if raw {
				file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				defer func() { _ = file.Close() }()
				_, err = file.WriteString(agyCheckpoint + "\n")
				return err
			}
			return nil
		},
		Nodes:  func(string) ([]map[string]any, bool, error) { return nil, false, nil },
		Getenv: func(string) string { return "" },
	}
	return f, sender, &said
}

func TestTheSenderTypesCompactOnceThenResumesAndReleasesTheClaim(t *testing.T) {
	f, sender, said := senderFixture(t, 500)
	sender.run()
	if len(*said) != 2 || (*said)[0] != "/compact" || (*said)[1] != resumeText {
		t.Fatalf("said = %q", *said)
	}
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("the claim outlived the compact it was for")
	}
}

func TestAnUnwitnessedCompactSendsNoResume(t *testing.T) {
	_, sender, said := senderFixture(t, 500)
	real := sender.Say
	sender.Say = func(room, seat, text string, raw bool) error {
		if raw {
			*said = append(*said, text)
			return nil // the seat took it and no CHECKPOINT ever came
		}
		return real(room, seat, text, raw)
	}
	sender.run()
	if len(*said) != 1 {
		t.Fatalf("said = %q", *said)
	}
}

// A claim always ends: neither a /compact nobody witnessed nor one that never reached the seat may keep holding it.
func TestASenderThatFailsReleasesTheClaim(t *testing.T) {
	for name, say := range map[string]error{"not witnessed": nil, "not delivered": errors.New("no pane")} {
		f, sender, _ := senderFixture(t, 500)
		failure := say
		sender.Say = func(string, string, string, bool) error { return failure }
		sender.run()
		if _, ok := readClaim(f.dir); ok {
			t.Fatalf("%s: the claim outlived the sender", name)
		}
	}
}

func TestASenderDoesNotTypeIntoAConversationTheClaimIsNotFor(t *testing.T) {
	f, sender, said := senderFixture(t, 500)
	if err := os.WriteFile(filepath.Join(f.dir, compactHookMarkFile), []byte("restarted"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender.run()
	if len(*said) != 0 {
		t.Fatalf("said = %q", *said)
	}
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("the claim of a gone conversation survived")
	}
}

func TestAResumeTheGateCannotReadIsHeldNotSent(t *testing.T) {
	_, sender, said := senderFixture(t, 500)
	failures := 0
	sender.Nodes = func(string) ([]map[string]any, bool, error) {
		failures++
		if failures < 3 {
			return nil, false, errors.New("no daemon")
		}
		return nil, false, nil
	}
	sender.run()
	if failures != 3 || len(*said) != 2 {
		t.Fatalf("asked the gate %d times, said %q", failures, *said)
	}
}

func TestASenderWithNoArrangedCompactTypesNothing(t *testing.T) {
	f, sender, said := senderFixture(t, 500)
	if err := os.Remove(filepath.Join(f.dir, compactClaimFile)); err != nil {
		t.Fatal(err)
	}
	sender.run()
	if len(*said) != 0 {
		t.Fatalf("said = %q", *said)
	}
}

// The supervisor, wired to the real claim: one pass past the limit with the hook holding the seat does nothing at all,
// and the same pass with the hook's claim expired compacts it.
func TestTheSupervisorLeavesASeatTheHookHolds(t *testing.T) {
	f := newTriggerFixture(t, 500)
	var said []string
	supervisor := &Supervisor{Room: "room", Quiet: 0, Out: &bytes.Buffer{}, State: map[string]*seatState{},
		StateRoot: t.TempDir(),
		Now:       func() float64 { return f.now },
		Seats:     func(string) []string { return []string{"seat"} },
		SeatLog:   func(string, string) (string, string) { return f.path, "agy" },
		Idle:      func(string, string) bool { return true },
		Say:       func(_, _, text string, _ bool) error { said = append(said, text); return nil },
		Log:       func(string) {},
		ClaimCompact: func(_ string, used, count int, now float64) bool {
			return supervisorClaim(f.dir, used, count, now)
		},
		HookHolds: func(_ string, now float64) bool { return hookHoldsClaim(f.dir, now) },
		HookLive:  func(string) bool { return true },
	}
	f.now = wallClock() // the quiet test compares this clock with the transcript's real mtime
	f.trigger.preInvocation(f.payload())
	supervisor.Step()
	if len(said) != 0 {
		t.Fatalf("the supervisor said %q under the hook's claim", said)
	}
	f.now += witnessWindow + 1
	supervisor.Step()
	if len(said) != 1 || said[0] != "/compact" {
		t.Fatalf("past the window the backstop said %q", said)
	}
	claim, ok := readClaim(f.dir)
	if !ok || claim.By != claimBySupervisor {
		t.Fatalf("the supervisor's /compact left claim %+v", claim)
	}
}

// claimSupervisor is a supervisor with the claim path wired to the fixture's seat.
func claimSupervisor(f *triggerFixture, idle *bool, said *[]string, logged *[]string) *Supervisor {
	return &Supervisor{Room: "room", Quiet: 0, Out: &bytes.Buffer{}, State: map[string]*seatState{},
		StateRoot: f.t.TempDir(),
		Now:       func() float64 { return f.now },
		Seats:     func(string) []string { return []string{"seat"} },
		SeatLog:   func(string, string) (string, string) { return f.path, "agy" },
		Idle:      func(string, string) bool { return *idle },
		Say:       func(_, _, text string, _ bool) error { *said = append(*said, text); return nil },
		Log:       func(text string) { *logged = append(*logged, text) },
		ClaimCompact: func(_ string, used, count int, now float64) bool {
			return supervisorClaim(f.dir, used, count, now)
		},
		HookHolds: func(_ string, now float64) bool { return hookHoldsClaim(f.dir, now) },
		HookLive:  func(string) bool { return true },
		ClaimDir:  func(string) string { return f.dir },
	}
}

// The backstop does not wait out the window for a seat at its prompt: a claim nobody is sending is taken over, once the
// Stop hook has had its moment, and a Stop arriving after that types nothing.
func TestTheSupervisorTakesOverAClaimOnASeatAtItsPrompt(t *testing.T) {
	f := newTriggerFixture(t, 500)
	idle, said, logged := true, []string{}, []string{}
	supervisor := claimSupervisor(f, &idle, &said, &logged)
	f.now = wallClock()
	f.trigger.preInvocation(f.payload())
	supervisor.Step()
	if len(said) != 0 {
		t.Fatalf("no grace for the Stop hook: said %q", said)
	}
	f.now += claimTakeoverGrace + 1
	if err := os.Chtimes(f.path, time.Unix(int64(f.now)-20, 0), time.Unix(int64(f.now)-20, 0)); err != nil {
		t.Fatal(err)
	}
	supervisor.Step()
	if len(said) != 1 || said[0] != "/compact" {
		t.Fatalf("said %q, logged %q", said, logged)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "took it over") {
		t.Fatalf("logged %q", logged)
	}
	f.trigger.stop(f.payload())
	if f.spawned != 0 {
		t.Fatal("a Stop after the takeover started a second /compact")
	}
}

// A seat that cannot be proved idle is not typed into, and its claim still ends at the window.
func TestAClaimPastItsWindowIsReleasedOnABusySeat(t *testing.T) {
	f := newTriggerFixture(t, 500)
	idle, said, logged := false, []string{}, []string{}
	supervisor := claimSupervisor(f, &idle, &said, &logged)
	f.now = wallClock()
	f.trigger.preInvocation(f.payload())
	supervisor.Step()
	if _, ok := readClaim(f.dir); !ok {
		t.Fatal("a fresh claim was released")
	}
	f.now += witnessWindow + 1
	supervisor.Step()
	if len(said) != 0 {
		t.Fatalf("said %q", said)
	}
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("the claim outlived its window")
	}
}

// Compacted some other way (an operator's /compact): nothing is typed on top of it.
func TestAClaimOnACompactedSeatIsReleased(t *testing.T) {
	f := newTriggerFixture(t, 500)
	idle, said, logged := true, []string{}, []string{}
	supervisor := claimSupervisor(f, &idle, &said, &logged)
	f.now = wallClock()
	f.trigger.preInvocation(f.payload())
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(agyCheckpoint + "\n")
	_ = file.Close()
	f.now += claimTakeoverGrace + 30
	supervisor.Step()
	if len(said) != 0 {
		t.Fatalf("said %q", said)
	}
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("the claim outlived the compaction")
	}
}

// agy-conversation names the new conversation only at the first tool call, so a reseated seat reads the old transcript,
// past the limit and final-answered: a /compact on that reading lands on the fresh prompt.
func TestATranscriptOlderThanTheSeatIsNotCompacted(t *testing.T) {
	f := newTriggerFixture(t, 500)
	idle, said, logged := true, []string{}, []string{}
	supervisor := claimSupervisor(f, &idle, &said, &logged)
	f.now = wallClock() + 60
	supervisor.Started = func(string, string) float64 { return f.now - 5 }
	supervisor.Step()
	if len(said) != 0 {
		t.Fatalf("said %q to a seat younger than its transcript", said)
	}
}

// A live hook means no notice either: a notice typed mid-turn is queued and starts a turn in front of the /compact.
func TestTheSupervisorSendsNoNoticeToASeatWithALiveHook(t *testing.T) {
	f := newTriggerFixture(t, 500)
	var said []string
	supervisor := &Supervisor{Room: "room", Quiet: 3600, Out: &bytes.Buffer{}, State: map[string]*seatState{},
		StateRoot: t.TempDir(),
		Now:       func() float64 { return f.now },
		Seats:     func(string) []string { return []string{"seat"} },
		SeatLog:   func(string, string) (string, string) { return f.path, "agy" },
		Idle:      func(string, string) bool { return false },
		Say:       func(_, _, text string, _ bool) error { said = append(said, text); return nil },
		Log:       func(string) {},
		HookLive:  func(string) bool { return true },
	}
	supervisor.Step()
	if len(said) != 0 {
		t.Fatalf("said %q", said)
	}
}

func mcpExchange(t *testing.T, server *compactMCP, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := server.serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var answers []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var answer map[string]any
		if err := json.Unmarshal([]byte(line), &answer); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		answers = append(answers, answer)
	}
	return answers
}

func mcpText(t *testing.T, answer map[string]any) string {
	t.Helper()
	result := answer["result"].(map[string]any)
	return result["content"].([]any)[0].(map[string]any)["text"].(string)
}

func TestTheMCPServerSpeaksTheProtocol(t *testing.T) {
	f := newTriggerFixture(t, 50)
	server := &compactMCP{Trigger: f.trigger, Log: func(string, string, string) (string, string) { return f.path, "agy" }}
	answers := mcpExchange(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"no/such"}`,
		`not json`,
	)
	if len(answers) != 4 {
		t.Fatalf("a notification was answered, or a request was not: %v", answers)
	}
	if got := answers[0]["result"].(map[string]any)["protocolVersion"]; got != "2024-11-05" {
		t.Fatalf("protocol = %v", got)
	}
	tools := answers[1]["result"].(map[string]any)["tools"].([]any)
	names := []string{tools[0].(map[string]any)["name"].(string), tools[1].(map[string]any)["name"].(string)}
	if names[0] != "compact_me" || names[1] != "context_usage" {
		t.Fatalf("tools = %v", names)
	}
	if answers[2]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method = %v", answers[2])
	}
	if answers[3]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("bad line = %v", answers[3])
	}
}

func TestContextUsageAddsTheInstructionOnlyPastTheLimit(t *testing.T) {
	f := newTriggerFixture(t, 50)
	server := &compactMCP{Trigger: f.trigger, Log: func(string, string, string) (string, string) { return f.path, "agy" }}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"context_usage"}}`
	if text := mcpText(t, mcpExchange(t, server, call)[0]); strings.Contains(text, "End your turn") {
		t.Fatalf("under the limit: %s", text)
	}
	if _, ok := readClaim(f.dir); ok {
		t.Fatal("a claim under the limit")
	}
	f.write(500)
	if text := mcpText(t, mcpExchange(t, server, call)[0]); !strings.Contains(text, "End your turn") {
		t.Fatalf("past the limit: %s", text)
	}
	if _, ok := readClaim(f.dir); !ok {
		t.Fatal("no claim past the limit")
	}
}

func TestCompactMeArrangesUnderTheLimitAndHoldsTheModelToIt(t *testing.T) {
	f := newTriggerFixture(t, 50)
	server := &compactMCP{Trigger: f.trigger, Log: func(string, string, string) (string, string) { return f.path, "agy" }}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"compact_me"}}`
	if text := mcpText(t, mcpExchange(t, server, call)[0]); !strings.Contains(text, "End your turn") {
		t.Fatalf("compact_me said %s", text)
	}
	// A second call changes nothing: the one claim stands.
	mcpExchange(t, server, call)
	if first := f.trigger.preInvocation(f.payload()); !strings.Contains(first, "ephemeralMessage") {
		t.Fatalf("next model call = %s", first)
	}
	if second := f.trigger.preInvocation(f.payload()); !strings.Contains(second, "terminate") {
		t.Fatalf("the model after that = %s", second)
	}
	f.trigger.stop(f.payload())
	if f.spawned != 1 {
		t.Fatalf("spawned = %d", f.spawned)
	}
	if text := mcpText(t, mcpExchange(t, server, call)[0]); !strings.Contains(text, "already running") {
		t.Fatalf("compact_me after the send said %s", text)
	}
}

func TestOutsideASeatTheMCPToolsRefuse(t *testing.T) {
	server := &compactMCP{Trigger: &compactTrigger{}, Log: func(string, string, string) (string, string) { return "", "" }}
	answer := mcpExchange(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"compact_me"}}`)[0]
	if answer["result"].(map[string]any)["isError"] != true {
		t.Fatalf("answer = %v", answer)
	}
}
