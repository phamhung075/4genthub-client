package clientdecisions

import (
	"strings"
	"testing"
)

func TestRecordsRoundTripAndRemove(t *testing.T) {
	root := t.TempDir()
	questions, err := ParseQuestions([]byte(`[{"id":"q","question":"Ship?","options":[{"id":"y","label":"Yes"},{"id":"n","label":"No"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(root, Record{QitemID: "qitem-1", Room: "r", Questions: questions, DecisionID: "d-1"}); err != nil {
		t.Fatal(err)
	}
	records, err := List(root, "r")
	if err != nil || len(records) != 1 || records[0].DecisionID != "d-1" || records[0].Questions[0].Options[0].Label != "Yes" {
		t.Fatalf("records = %+v, %v", records, err)
	}
	if err := Remove(root, "r", "qitem-1"); err != nil {
		t.Fatal(err)
	}
	if records, _ := List(root, "r"); len(records) != 0 {
		t.Fatalf("records after remove = %+v", records)
	}
	if err := Write(root, Record{QitemID: "../x", Room: "r"}); err == nil {
		t.Fatal("a row id naming another directory was accepted")
	}
}

func TestAnswerNoteNamesTheChosenLabels(t *testing.T) {
	questions, _ := ParseQuestions([]byte(`[{"id":"q","question":"Ship?","options":[{"id":"y","label":"Yes, today"},{"id":"n","label":"No"}]}]`))
	note := AnswerNote(questions, map[string]string{"q": "y"})
	if !strings.Contains(note, "Ship? -> Yes, today [y]") {
		t.Fatalf("note = %q", note)
	}
}
