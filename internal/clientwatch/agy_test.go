package clientwatch

import (
	"encoding/json"
	"strings"
	"testing"
)

// The samples keep the shapes of real agy transcripts (seats of room aa2-gemini); their content is made up.

// agyPlanner is a PLANNER_RESPONSE record calling one tool, with args as agy logs them.
func agyPlanner(step int, name string, args map[string]string) string {
	encoded, _ := json.Marshal(map[string]any{"step_index": step, "type": "PLANNER_RESPONSE", "status": "DONE",
		"tool_calls": []any{map[string]any{"name": name, "args": args}}})
	return string(encoded)
}

func agyGenericRecord(step int, status, content string) string {
	encoded, _ := json.Marshal(map[string]any{"step_index": step, "type": "GENERIC", "status": status, "content": content})
	return string(encoded)
}

const agyStamps = "Created At: 2026-11-01T17:36:41+01:00\nCompleted At: 2026-11-01T17:36:41+01:00\n"

// An agy call must render byte for byte as the omp call it maps to: one renderer, no copied styling.
func TestAgyCallsRenderAsTheirOmpCall(t *testing.T) {
	cases := []struct {
		name string
		agy  map[string]string
		tool string
		omp  map[string]any
	}{
		{"run_command", map[string]string{"CommandLine": `"git log \\\n  --oneline"`, "WaitMsBeforeAsync": "5000", "toolSummary": `"Check history"`},
			"bash", map[string]any{"i": "Check history", "command": "git log \\\n  --oneline"}},
		{"read_file", map[string]string{"Path": `"/repo/a.go"`}, "read", map[string]any{"path": "/repo/a.go"}},
		{"read_file", map[string]string{"Path": `"/repo/a.go"`, "StartLine": "45", "EndLine": "130", "ByteOffset": "0"},
			"read", map[string]any{"path": "/repo/a.go:45-130"}},
		{"view_file", map[string]string{"AbsolutePath": `"/repo/a.go"`, "StartLine": "3"}, "read", map[string]any{"path": "/repo/a.go:3"}},
		{"write_file", map[string]string{"Path": `"/repo/n.md"`, "Content": `"- a line\n"`, "Append": "true"},
			"write", map[string]any{"path": "/repo/n.md", "content": "- a line\n"}},
		{"edit_file", map[string]string{"Path": `"/repo/a.go"`, "Target": `"x := 1\ny := 2"`, "Replacement": `"x := 1\ny := 3"`},
			"edit", map[string]any{"path": "/repo/a.go", "old_string": "x := 1\ny := 2", "new_string": "x := 1\ny := 3"}},
		{"edit_file", map[string]string{"Path": `"/repo/n.md"`, "Content": `"whole file"`, "ArtifactMetadata": `{"Summary":"s"}`},
			"write", map[string]any{"path": "/repo/n.md", "content": "whole file"}},
		{"search_web", map[string]string{"query": `"xpbd cloth"`}, "web_search", map[string]any{"query": "xpbd cloth"}},
		{"invoke_subagent", map[string]string{"Prompt": `"do it"`, "Role": `"Maintainer"`, "TypeName": `"maintainer"`},
			"task", map[string]any{"i": "maintainer", "prompt": "do it"}},
		{"call_mcp_tool", map[string]string{"ServerName": `"blender"`, "ToolName": `"run"`, "Arguments": `{"code":"print(1)"}`},
			"mcp__blender__run", map[string]any{"code": "print(1)"}},
		{"send_message", map[string]string{"Recipient": `"peer"`, "Message": `"hello"`},
			"send_message", map[string]any{"i": "to peer", "prompt": "hello"}},
		{"manage_task", map[string]string{"Action": `"status"`, "TaskId": `"6d7c-conv/task-67"`},
			"manage_task", map[string]any{"summary": "status task-67"}},
		{"schedule", map[string]string{"DurationSeconds": "20", "Prompt": `"check it"`},
			"schedule", map[string]any{"summary": "in 20s: check it"}},
	}
	for _, c := range cases {
		got := strings.Join(events(agyPlanner(1, c.name, c.agy), 110, false, 25), "\n")
		want := strings.Join(events(toolCall(c.tool, c.omp), 110, false, 25), "\n")
		if got != want || got == "" {
			t.Errorf("%s %v:\n got %q\nwant %q", c.name, c.agy, got, want)
		}
	}
}

func TestAgyCommandShowsRealLines(t *testing.T) {
	out := plain(strings.Join(events(agyPlanner(1, "run_command", map[string]string{"CommandLine": `"a \\\n  --to b"`}), 110, false, 25), "\n"))
	if want := "❯ bash a \\\n      --to b"; !strings.Contains(out, want) {
		t.Errorf("got %q, want it to hold %q", out, want)
	}
}

func TestAgyUnknownToolIsAGenericLine(t *testing.T) {
	out := plain(strings.Join(events(agyPlanner(1, "new_tool", map[string]string{"Name": `"a \"b\""`, "Count": "3", "toolSummary": `"Why"`}), 110, false, 25), "\n"))
	for _, want := range []string{"→ new_tool", "Why", `Name: a "b"`, "Count: 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing %q", out, want)
		}
	}
	if strings.Contains(out, "{") || strings.Contains(out, `\"`) {
		t.Errorf("raw JSON in %q", out)
	}
}

func TestAgyArgumentCutByTheLogStillDecodes(t *testing.T) {
	out := plain(strings.Join(events(agyPlanner(1, "run_command", map[string]string{"CommandLine": "\"say \\\"hi\\\"\\nnext\n<truncated 1646 bytes>"}), 110, false, 25), "\n"))
	for _, want := range []string{`say "hi"`, "next", "<truncated 1646 bytes>"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing %q", out, want)
		}
	}
	if strings.Contains(out, `\"`) {
		t.Errorf("escapes left in %q", out)
	}
}

func TestAgyEnvelopeIsStripped(t *testing.T) {
	cases := []struct{ name, content, want string }{
		{"output", agyStamps + "Command exited with code 0.\n\nCommand output:\nOn branch x\r\nclean\r\n", "On branch x\r\nclean\r\n"},
		{"no output", agyStamps + "Command exited with code 0.\n\nNo output from the command.", ""},
		{"cut output", agyStamps + "The tool's output was truncated due to being too long. You can find the full output in a file at file:///x\n\nHere is a preview with the middle of the output truncated:\nCommand exited with code 0.\n\nCommand output:\nrow", "row"},
		{"output like the envelope", agyStamps + "Command exited with code 0.\n\nCommand output:\nCreated At: mine\n", "Created At: mine\n"},
		{"read", agyStamps + "Showing lines 1-2 of 2\n1: a\n2: b", "1: a\n2: b"},
		{"view", agyStamps + "File Path: `file:///x`\nTotal Lines: 2\nTotal Bytes: 8\nShowing lines 1 to 2\nThe following code has been modified to include a line number before every line, in the format: <n>: <line>.\n1: a\n2: b", "1: a\n2: b"},
		{"background", "Created At: 2026-11-01T17:36:41+01:00\nTool is running as a background task with task id: conv/task-40\nTask Description: make\nsecond line\nTask logs are available at: file:///x\nLog output:\n<truncated 20 lines>\nbuilding\n\nYOU MUST TAKE ONE OF THE FOLLOWING TWO ACTIONS: A) stop\n DO NOTHING ELSE.\n", "background task-40\n<truncated 20 lines>\nbuilding"},
		{"task status", agyStamps + "Task: conv/task-51\nStatus: RUNNING\nLog: file:///x\nLog output:\nbuilding\r\n", "task-51 RUNNING\nbuilding"},
		{"step error", agyStamps + "Encountered error in step execution: task is not running", "task is not running"},
	}
	for _, c := range cases {
		if got := agyUnwrap(c.content).output; got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// resultOf renders a call, then its result, the way the feed does for one seat.
func resultOf(call, result string) string {
	o := renderOptions{width: 110, lines: 25, calls: newCallTracker(), agy: newAgyPairs()}
	eventsWith(call, o)
	return plain(strings.Join(eventsWith(result, o), "\n"))
}

func TestAgyResultsFoldLikeOmp(t *testing.T) {
	read := agyPlanner(1, "read_file", map[string]string{"Path": `"/a.go"`, "StartLine": "1", "EndLine": "2"})
	if got, want := resultOf(read, agyGenericRecord(2, "DONE", agyStamps+"Showing lines 1-2 of 2\n1: a\n2: b")), "← read  /a.go:1-2  · 2 lines"; got != want {
		t.Errorf("read: got %q, want %q", got, want)
	}
	write := agyPlanner(1, "write_file", map[string]string{"Path": `"/a.md"`, "Content": `"x"`})
	if got := resultOf(write, agyGenericRecord(2, "DONE", agyStamps+"Created file.")); got != "" {
		t.Errorf("a write's result should fold away, got %q", got)
	}
	shell := agyPlanner(1, "run_command", map[string]string{"CommandLine": `"ls"`})
	if got, want := resultOf(shell, agyGenericRecord(2, "DONE", agyStamps+"Command exited with code 0.\n\nCommand output:\nout\n")), "← bash  ls out"; got != want {
		t.Errorf("bash: got %q, want %q", got, want)
	}
}

func TestAgyFailureShowsTheExitLikeOmp(t *testing.T) {
	shell := agyPlanner(1, "run_command", map[string]string{"CommandLine": `"make"`})
	content := "Created At: 2026-11-01T17:36:00+01:00\nCompleted At: 2026-11-01T17:36:05+01:00\nCommand exited with code 2.\n\nCommand output:\nboom\n"
	if got, want := resultOf(shell, agyGenericRecord(2, "DONE", content)), "✗ bash  make  5.0s exit 2 boom"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := resultOf(shell, agyGenericRecord(2, "ERROR", agyStamps+"Encountered error in step execution: denied")), "✗ bash  make denied"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := resultOf(shell, agyGenericRecord(2, "RUNNING", agyStamps+"Task: c/t-1\nStatus: RUNNING\nLog: x\nLog output:\n")); strings.Contains(got, "✗") {
		t.Errorf("a running task is no failure: %q", got)
	}
}

func TestAgyQuickCallShowsNoTime(t *testing.T) {
	content := "Created At: 2026-11-01T17:36:00+01:00\nCompleted At: 2026-11-01T17:36:01+01:00\nCommand exited with code 0.\n\nNo output from the command."
	if took := agyUnwrap(content).wallMs; took != 0 {
		t.Errorf("a one-second gap between whole-second stamps showed %vms", took)
	}
}

// A result names no call and an input can come between them, and the replay reads a transcript more than once: the
// filter pass, the replay and a redraw after a resize must all pair the same way.
func TestAgyPairingSurvivesInterleavingAndRereading(t *testing.T) {
	lines := []string{
		agyPlanner(1, "read_file", map[string]string{"Path": `"/a.go"`}),
		`{"step_index":2,"type":"USER_INPUT","status":"DONE","content":"hi"}`,
		agyGenericRecord(3, "DONE", agyStamps+"Showing lines 1-1 of 1\n1: a"),
		agyPlanner(4, "run_command", map[string]string{"CommandLine": `"ls"`}),
		agyGenericRecord(5, "DONE", agyStamps+"Command exited with code 0.\n\nCommand output:\nout"),
		agyPlanner(6, "write_file", map[string]string{"Path": `"/b.md"`, "Content": `"x"`}),
	}
	pairs := newAgyPairs()
	for _, line := range lines {
		eventsWith(line, renderOptions{width: 1, lines: 25, agy: pairs})
	}
	for pass := range 2 {
		o := renderOptions{width: 110, lines: 25, calls: newCallTracker(), agy: pairs}
		// The window starts at the first result, its call scrolled out: the tool is still known.
		got := []string{}
		for _, line := range lines[2:] {
			got = append(got, eventsWith(line, o)...)
		}
		out := plain(strings.Join(got, "\n"))
		for _, want := range []string{"← read  · 1 lines", "❯ bash ls", "← bash  ls out", "✎ write /b.md"} {
			if !strings.Contains(out, want) {
				t.Errorf("pass %d: %q is missing %q", pass, out, want)
			}
		}
	}
	// The write above is still open, so its result pairs with it and not with an earlier step.
	if got := pairs.result(7); got.tool != "write" {
		t.Errorf("the open call was lost: %+v", got)
	}
}

func TestAgyWithoutPairingStillStripsTheEnvelope(t *testing.T) {
	out := plain(strings.Join(events(agyGenericRecord(2, "DONE", agyStamps+"Command exited with code 0.\n\nCommand output:\nout"), 110, false, 25), "\n"))
	if strings.Contains(out, "Created At") || strings.Contains(out, "Command output") || !strings.Contains(out, "out") {
		t.Errorf("got %q", out)
	}
}

func TestAgyRecordTimeIsTheSeats(t *testing.T) {
	at := recordTime(`{"type":"GENERIC","created_at":"2026-10-28T04:40:29Z"}`)
	if at.UTC().Format("2006-01-02T15:04:05") != "2026-10-28T04:40:29" {
		t.Errorf("got %v", at)
	}
}
