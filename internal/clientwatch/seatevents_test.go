package clientwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/seatloop"
)

// The stamp the synthetic logs are written around, and the "now" they are read at.
var logNow = time.Date(2026, 11, 1, 17, 22, 0, 0, time.UTC)

func stamp(ago time.Duration) string { return logNow.Add(-ago).Format(time.RFC3339) }

func writeLog(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func agyCallLine(step int, ago time.Duration, name, args string) string {
	return fmt.Sprintf(`{"step_index":%d,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":%q,"input_tokens":2461,"cache_read_tokens":241864,"output_tokens":101,"content":"I am waiting for the timer to fire.","tool_calls":[{"name":%q,"args":%s}]}`,
		step, stamp(ago), name, args)
}

func agyResultLine(step int, ago time.Duration, status, content string) string {
	return fmt.Sprintf(`{"step_index":%d,"source":"MODEL","type":"GENERIC","status":%q,"created_at":%q,"content":%q}`, step, status, stamp(ago), content)
}

const statusArgs = `{"Action":"\"status\"","TaskId":"\"6d7cd03c/task-249\""}`

// task249Shape is the incident's own shape: a run of status calls, a `schedule` and the timer's system message
// between every few of them, a moving "Last progress" in each answer, and an `lsof` the model tried.
func task249Shape() []string {
	var lines []string
	step := 250
	ago := 160 * time.Second
	add := func(name, args, status, output string) {
		lines = append(lines, agyCallLine(step, ago, name, args), agyResultLine(step+1, ago-time.Second, status, output))
		step += 2
		ago -= 4 * time.Second
	}
	for round := 0; round < 4; round++ {
		add("schedule", fmt.Sprintf(`{"DurationSeconds":"%d","Prompt":"\"Check task-249\""}`, 10+round), "RUNNING",
			"Tool is running as a background task with task id: 6d7cd03c/task-300")
		for i := 0; i < 4; i++ {
			add("manage_task", statusArgs, "DONE", fmt.Sprintf(
				"Created At: 2026-11-01T18:19:32+01:00\nCompleted At: 2026-11-01T18:19:33+01:00\nTask: 6d7cd03c/task-249\nStatus: RUNNING\nLog: file:///x\nLog output:\ncargo-slot: all 2 build slots busy, waiting:\n\nLast progress: %ds ago\n", 30+i))
		}
		lines = append(lines, fmt.Sprintf(`{"step_index":%d,"source":"SYSTEM","type":"SYSTEM_MESSAGE","status":"DONE","created_at":%q,"content":"timer fired"}`, step, stamp(ago)))
		step++
		add("run_command", fmt.Sprintf(`{"CommandLine":"\"lsof /tmp/aa2-build-slots/*\"","WaitMsBeforeAsync":"5000","toolSummary":"\"Check %d\""}`, round), "DONE",
			"Command exited with code 0.\n\nCommand output:\nslot0")
	}
	return lines
}

func detect(t *testing.T, lines []string) (seatloop.Finding, bool) {
	t.Helper()
	return seatloop.Detect(SeatEvents(writeLog(t, lines)), seatloop.Defaults(), logNow, time.Time{})
}

func TestTheTask249ShapeFires(t *testing.T) {
	finding, looping := detect(t, task249Shape())
	if !looping {
		t.Fatal("the incident's shape did not fire")
	}
	if finding.Tool != "manage_task" || !strings.Contains(finding.Label, "status task-249") || finding.Count != 16 {
		t.Errorf("got %+v, want manage_task status task-249 x16", finding)
	}
}

func TestAnAgySeatBlockedInOneLongCommandDoesNotFire(t *testing.T) {
	lines := []string{agyCallLine(10, 2*time.Hour, "run_command", `{"CommandLine":"\"cargo build --release\""}`)}
	if finding, looping := detect(t, lines); looping {
		t.Fatalf("one long command fired: %+v", finding)
	}
	// And the same after a real burst of repeats that ended before the command started.
	var burst []string
	for i := 0; i < 10; i++ {
		burst = append(burst, agyCallLine(i*2, 9*time.Minute, "manage_task", statusArgs), agyResultLine(i*2+1, 9*time.Minute, "DONE", "Task: x\nStatus: RUNNING\n"))
	}
	burst = append(burst, agyCallLine(40, 8*time.Minute, "run_command", `{"CommandLine":"\"cargo build --release\""}`))
	if finding, looping := detect(t, burst); looping {
		t.Fatalf("a seat quiet for eight minutes inside one command fired: %+v", finding)
	}
}

func TestReadingDifferentFilesDoesNotFire(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, agyCallLine(i*2, time.Duration(40-i)*time.Second, "read_file", fmt.Sprintf(`{"Path":"\"/a/file%d.rs\""}`, i)),
			agyResultLine(i*2+1, time.Duration(40-i)*time.Second, "DONE", fmt.Sprintf("Showing lines 1 to 2\n1: fn f%c() {}", 'a'+i%26)))
	}
	if finding, looping := detect(t, lines); looping {
		t.Fatalf("reading different files fired: %+v", finding)
	}
}

func TestTheModelsOwnIntentDoesNotMakeACallDifferent(t *testing.T) {
	var lines []string
	for i := 0; i < 8; i++ {
		lines = append(lines, agyCallLine(i*2, time.Duration(8-i)*time.Second, "run_command",
			fmt.Sprintf(`{"CommandLine":"\"git status\"","toolSummary":"\"look %d\""}`, i)))
	}
	if _, looping := detect(t, lines); !looping {
		t.Fatal("the same command under eight different summaries did not fire")
	}
}

func TestAnOmpSeatFiresAndAnOmpEditResetsIt(t *testing.T) {
	omp := func(index int, ago time.Duration, tool, args string) []string {
		return []string{
			fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[{"type":"toolCall","id":"c%d","name":%q,"arguments":%s}]}}`, stamp(ago), index, tool, args),
			fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"toolResult","toolCallId":"c%d","toolName":%q,"content":[{"type":"text","text":"state: running %d"}]}}`, stamp(ago), index, tool, index),
		}
	}
	var loop, working []string
	for i := 0; i < 10; i++ {
		ago := time.Duration(10-i) * 5 * time.Second
		loop = append(loop, omp(i, ago, "bash", fmt.Sprintf(`{"i":"look %d","command":"4genteam room queue show q1"}`, i))...)
		working = append(working, omp(i*2, ago, "bash", `{"command":"cargo test"}`)...)
		working = append(working, omp(i*2+1, ago, "edit", fmt.Sprintf(`{"path":"a.go","new":"%d"}`, i))...)
	}
	if _, looping := detect(t, loop); !looping {
		t.Error("an omp seat repeating one command did not fire")
	}
	if finding, looping := detect(t, working); looping {
		t.Errorf("an omp seat testing and editing fired: %+v", finding)
	}
}

func TestAClaudeSeatFires(t *testing.T) {
	var lines []string
	for i := 0; i < 8; i++ {
		ago := time.Duration(8-i) * 5 * time.Second
		lines = append(lines,
			fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"gh run view 1"}}]}}`, stamp(ago), i),
			fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"in_progress"}]}}`, stamp(ago), i))
	}
	finding, looping := detect(t, lines)
	if !looping || finding.Rule != "call" {
		t.Fatalf("got %+v, %t", finding, looping)
	}
}

func TestAMessageToTheSeatStartsTheCountOver(t *testing.T) {
	lines := task249Shape()
	lines = append(lines, fmt.Sprintf(`{"step_index":900,"type":"USER_INPUT","status":"DONE","created_at":%q,"content":"<USER_REQUEST>stop</USER_REQUEST>"}`, stamp(time.Second)))
	if finding, looping := detect(t, lines); looping {
		t.Fatalf("it fired over a message that came after the repeats: %+v", finding)
	}
}

func TestThePaneTitleCarriesTheAlert(t *testing.T) {
	header := newHeader("gemini", 1, 100, "", func(string) {})
	header.alert = func() string { return "loop=STOPPED at 17:30Z, repeated x8: bash x" }
	if title := header.title(); !strings.Contains(title, "loop=STOPPED") {
		t.Errorf("the title does not carry the alert: %q", title)
	}
}
