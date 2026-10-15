package clientactivity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The three records merge into one oldest-first timeline, a lifecycle line is not a send, and --problems keeps only
// what was refused or went wrong.
func TestTimelineMergesTheThreeRecordsAndFiltersProblems(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rooms", "r", "seats", "lead", "tool-calls.jsonl"),
		`{"at":"2026-10-15T10:00:00Z","tool":"bash","target":"ls","outcome":"ok","ms":3}`+"\n"+
			`{"at":"2026-10-15T10:00:02Z","tool":"bash","target":"docker ps","outcome":"blocked","rule":"docker","error":"no docker"}`+"\n")
	write(t, filepath.Join(root, "seats", "r", "lead", "audit.jsonl"),
		`{"type":"SessionStarted","payload":{"room":"r"}}`+"\n"+
			`{"At":"2026-10-15T10:00:01Z","From":"lead","To":"dev","Intent":"notice","Allowed":false,"Reason":"no link"}`+"\n")
	write(t, filepath.Join(root, "jev_decisions.log"),
		`{"timestamp":"2026-10-15T10:00:03Z","kind":"decision","tier":"seat","category":"routine","outcome":"escalated-to-lead","summary":"which fix"}`+"\n")

	all := Collect(root, "r")
	if len(all) != 4 {
		t.Fatalf("want 4 events (the lifecycle line is not one), got %d: %+v", len(all), all)
	}
	for i := 1; i < len(all); i++ {
		if all[i].At.Before(all[i-1].At) {
			t.Fatalf("the timeline is not oldest-first: %+v", all)
		}
	}
	problems := filter(all, options{problems: true}, time.Now())
	if len(problems) != 3 {
		t.Fatalf("want the blocked call, the refused send and the escalation, got %+v", problems)
	}
	line := render(problems[0])
	if !strings.Contains(line, "send") || !strings.Contains(line, "refused") || !strings.Contains(line, "lead -> dev (notice)") {
		t.Fatalf("a refused send must read as one line saying what was refused: %q", line)
	}
}
