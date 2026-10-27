package clientroom

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
)

// An owner ask is ONE shape: tag owner-decision, addressed to human@<room>, with the questions the
// dashboard's Decision Requests panel offers. The create records the questions beside the room once the
// daemon wrote the row, and refuses every other shape before a frame is sent.

const ownerQuestions = `[{"id":"exit_rule","question":"Which Phase 1 exit rule?",` +
	`"options":[{"id":"short","label":"Short gate","recommended":true},{"id":"full","label":"Full gate"}]}]`

func writeQuestions(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ask.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOwnerAskCreateRecordsItsQuestionsBesideTheRoom(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	daemon := newFakeDaemon(t, map[string]string{
		"QueueCreate": okData(queueRowFrame("qitem-ask", "pending", "normal", "Phase 1 exit rule")),
	})
	code, stdout, stderr := runRoom(t, daemon.args("queue", "create", testRoom,
		"--to", "human@"+testRoom, "--summary", "Phase 1 exit rule", "--body", schemaBody,
		"--tag", "owner-decision", "--questions-file", writeQuestions(t, ownerQuestions))...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "questions recorded") {
		t.Errorf("stdout = %q, want the record named", stdout)
	}
	records, err := clientdecisions.List(daemon.stateDir, testRoom)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %v, %v; want the one ask", records, err)
	}
	if records[0].QitemID != "qitem-ask" || len(records[0].Questions) != 1 || records[0].Questions[0].Options[1].Label != "Full gate" {
		t.Fatalf("record = %+v", records[0])
	}
}

func TestOwnerAskShapeIsRefusedBeforeTheDaemon(t *testing.T) {
	t.Setenv(sessionEnv, testSeat)
	questions := writeQuestions(t, ownerQuestions)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"addressed to a seat": {[]string{"--to", "lead@" + testRoom, "--tag", "owner-decision", "--questions-file", questions}, "goes to human@" + testRoom},
		"no questions":        {[]string{"--to", "human@" + testRoom, "--tag", "owner-decision"}, "needs --questions-file"},
		"questions on a seat row": {[]string{"--to", "reviewer@" + testRoom, "--tag", "gate", "--questions-file", questions},
			"belongs to an owner ask"},
		"malformed questions": {[]string{"--to", "human@" + testRoom, "--tag", "owner-decision", "--questions-file",
			writeQuestions(t, `[{"id":"q","question":"?","options":[{"id":"a","label":"A"}]}]`)}, "give 2 to 4 options"},
	} {
		daemon := newFakeDaemon(t, map[string]string{})
		args := append([]string{"create", testRoom, "--summary", "Phase 1 exit rule", "--body", schemaBody}, tc.args...)
		code, _, stderr := runRoom(t, daemon.args("queue", args...)...)
		if code != clientcmd.ExitRoomRefused || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d stderr %q, want exit %d naming %q", name, code, stderr, clientcmd.ExitRoomRefused, tc.want)
		}
		assertRequests(t, daemon)
	}
}
