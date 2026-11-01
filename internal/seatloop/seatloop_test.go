package seatloop

import (
	"fmt"
	"testing"
	"time"
)

var at = time.Date(2026, 11, 1, 17, 30, 0, 0, time.UTC)

func call(ago time.Duration, tool, args string) Event {
	return Event{Kind: Call, Tool: tool, Args: args, Label: args, At: at.Add(-ago)}
}

func result(ago time.Duration, output string, failed bool) Event {
	return Event{Kind: Result, Output: output, Failed: failed, At: at.Add(-ago)}
}

func TestTheSameCallCountsAcrossWorkBetweenIt(t *testing.T) {
	var events []Event
	for i := 0; i < 8; i++ {
		events = append(events, call(time.Duration(8-i)*time.Minute/4, "manage_task", "status task-249"),
			result(time.Duration(8-i)*time.Minute/4, fmt.Sprintf("RUNNING %ds ago", i), false),
			call(time.Duration(8-i)*time.Minute/4, "schedule", fmt.Sprintf("in %ds", i)),
			result(time.Duration(8-i)*time.Minute/4, "", false))
	}
	finding, looping := Detect(events, Defaults(), at, time.Time{})
	if !looping || finding.Rule != "call" || finding.Count != 8 || finding.Tool != "manage_task" {
		t.Fatalf("got %+v, %t", finding, looping)
	}
}

func TestSevenRepeatsAreNotALoop(t *testing.T) {
	var events []Event
	for i := 0; i < 7; i++ {
		events = append(events, call(time.Minute, "bash", "git status"))
	}
	if finding, looping := Detect(events, Defaults(), at, time.Time{}); looping {
		t.Fatalf("fired on seven: %+v", finding)
	}
}

func TestANewWriteStartsTheCountOverAndARepeatedWriteDoesNot(t *testing.T) {
	var fresh, same []Event
	for i := 0; i < 12; i++ {
		ago := time.Duration(12-i) * 10 * time.Second
		fresh = append(fresh, call(ago, "bash", "cargo test"), result(ago, "FAILED", false),
			call(ago, "edit", fmt.Sprintf("change %d", i)), result(ago, "ok", false))
		same = append(same, call(ago, "edit", "the same change"), result(ago, "ok", false))
	}
	if finding, looping := Detect(fresh, Defaults(), at, time.Time{}); looping {
		t.Errorf("a test and a new edit each time is work, and it fired: %+v", finding)
	}
	if _, looping := Detect(same, Defaults(), at, time.Time{}); !looping {
		t.Error("the same write twelve times is no progress, and it did not fire")
	}
}

func TestAFailedWriteIsNotProgress(t *testing.T) {
	var events []Event
	for i := 0; i < 8; i++ {
		ago := time.Duration(8-i) * 10 * time.Second
		events = append(events, call(ago, "bash", "git status"), result(ago, "clean", false),
			call(ago, "write", fmt.Sprintf("attempt %d", i)), result(ago, "refused", true))
	}
	if _, looping := Detect(events, Defaults(), at, time.Time{}); !looping {
		t.Error("a write that keeps failing moved nothing, and it did not fire")
	}
}

func TestTheSameAnswerFiresOnlyInARowAndNeverWhenEmpty(t *testing.T) {
	var refused, empty, broken []Event
	for i := 0; i < 8; i++ {
		ago := time.Duration(8-i) * 10 * time.Second
		refused = append(refused, call(ago, "bash", fmt.Sprintf("try %d", i)), result(ago, "permission denied", true))
		empty = append(empty, call(ago, "bash", fmt.Sprintf("grep x file%d", i)), result(ago, "", false))
		broken = append(broken, call(ago, "bash", fmt.Sprintf("try %d", i)), result(ago, fmt.Sprintf("different %c", 'a'+i), false))
	}
	if finding, looping := Detect(refused, Defaults(), at, time.Time{}); !looping || finding.Rule != "result" {
		t.Errorf("the same refusal to eight commands: %+v, %t", finding, looping)
	}
	if finding, looping := Detect(empty, Defaults(), at, time.Time{}); looping {
		t.Errorf("an empty answer identifies nothing, and it fired: %+v", finding)
	}
	if finding, looping := Detect(broken, Defaults(), at, time.Time{}); looping {
		t.Errorf("different answers fired: %+v", finding)
	}
}

func TestARepeatOlderThanFreshIsASeatThatStopped(t *testing.T) {
	var events []Event
	for i := 0; i < 10; i++ {
		events = append(events, call(DefaultFresh+time.Minute, "bash", "git status"))
	}
	if finding, looping := Detect(events, Defaults(), at, time.Time{}); looping {
		t.Fatalf("the newest repeat is older than Fresh, and it fired: %+v", finding)
	}
}

func TestAnInputAndTheGuardsOwnActionStartTheCountOver(t *testing.T) {
	var events []Event
	for i := 0; i < 8; i++ {
		events = append(events, call(time.Minute, "bash", "git status"))
	}
	withInput := append(append([]Event{}, events...), Event{Kind: Input, At: at.Add(-30 * time.Second)})
	if _, looping := Detect(withInput, Defaults(), at, time.Time{}); looping {
		t.Error("a message reached the seat after the repeats, and it still fired")
	}
	if _, looping := Detect(events, Defaults(), at, at.Add(-30*time.Second)); looping {
		t.Error("the repeats came before the guard's last action, and they were counted again")
	}
}

func TestZeroTurnsItOffAndABadValueIsRefusedByName(t *testing.T) {
	var events []Event
	for i := 0; i < 20; i++ {
		events = append(events, call(time.Minute, "bash", "git status"))
	}
	t.Setenv("LOOP_REPEAT_LIMIT", "0")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, looping := Detect(events, config, at, time.Time{}); looping {
		t.Error("LOOP_REPEAT_LIMIT=0 still fired")
	}
	t.Setenv("LOOP_REPEAT_LIMIT", "many")
	if _, err := ConfigFromEnv(); err == nil {
		t.Error("a value that is no number was accepted")
	}
}

func TestTheRecordOutlivesThePassAndNoRecordsLeaveNoFile(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, "demo", map[string]Record{"s": {Rung: Halted, At: at, Call: "bash x", Count: 8, Stopped: true}}); err != nil {
		t.Fatal(err)
	}
	if got := Load(root, "demo")["s"]; !got.Stopped || got.Rung != Halted {
		t.Fatalf("read back %+v", got)
	}
	if rooms := Rooms(root); len(rooms) != 1 || rooms[0] != "demo" {
		t.Fatalf("rooms %v", rooms)
	}
	if err := Save(root, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if rooms := Rooms(root); len(rooms) != 0 {
		t.Fatalf("no records left a file: %v", rooms)
	}
}
