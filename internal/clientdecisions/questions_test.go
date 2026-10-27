package clientdecisions

import (
	"strings"
	"testing"
)

func TestParseQuestionsLimits(t *testing.T) {
	ok := `[{"id":"q1","question":"?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B"}]}]`
	qs, err := ParseQuestions([]byte(ok))
	if err != nil || len(qs) != 1 || qs[0].Recommended() != "a" {
		t.Fatalf("ok: %v %v", qs, err)
	}
	for name, bad := range map[string]string{
		"not an array":    `{}`,
		"empty":           `[]`,
		"bad id":          `[{"id":"a b","question":"?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]`,
		"one option":      `[{"id":"q","question":"?","options":[{"id":"a","label":"A"}]}]`,
		"two recommended": `[{"id":"q","question":"?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B","recommended":true}]}]`,
		"long label":      `[{"id":"q","question":"?","options":[{"id":"a","label":"` + strings.Repeat("x", 76) + `"},{"id":"b","label":"B"}]}]`,
		"duplicate id":    `[{"id":"q","question":"?","options":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}]`,
	} {
		if _, err := ParseQuestions([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
