package clientjev

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The limits of a structured question: 1-4 questions, each with 2-4 options. They are the server's
// own (agenthub_go/fastmcp/room_decisions/domain/questions), because an ask that passes here and is
// then raised to the owner must pass there.
const (
	maxQuestions    = 4
	minOptions      = 2
	maxOptions      = 4
	maxOptionLabel  = 75
	maxQuestionText = 500
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

const idRule = `id must be 1-40 letters, digits, "-" or "_".`

// Option is one choice of a question.
type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Recommended bool   `json:"recommended,omitempty"`
}

// Question is one question of an ask.
type Question struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}

// Recommended is the id of the option the asker marked, or "".
func (q Question) Recommended() string {
	for _, option := range q.Options {
		if option.Recommended {
			return option.ID
		}
	}
	return ""
}

// ParseQuestions validates a questions array. A malformed value is refused whole: one bad question
// rejects the ask rather than being dropped from it.
func ParseQuestions(value []byte) ([]Question, error) {
	var raw []struct {
		ID       *string `json:"id"`
		Question *string `json:"question"`
		Options  *[]struct {
			ID          *string `json:"id"`
			Label       *string `json:"label"`
			Recommended *bool   `json:"recommended"`
		} `json:"options"`
	}
	if err := json.Unmarshal(value, &raw); err != nil {
		return nil, fmt.Errorf("the questions must be an array of questions")
	}
	if len(raw) < 1 || len(raw) > maxQuestions {
		return nil, fmt.Errorf("ask 1 to %d questions (got %d)", maxQuestions, len(raw))
	}
	seen := map[string]bool{}
	out := make([]Question, 0, len(raw))
	for i, q := range raw {
		where := fmt.Sprintf("question %d", i+1)
		if q.ID == nil || !idPattern.MatchString(*q.ID) {
			return nil, fmt.Errorf("%s: %s", where, idRule)
		}
		if seen[*q.ID] {
			return nil, fmt.Errorf("%s: duplicate question id %q", where, *q.ID)
		}
		seen[*q.ID] = true
		if q.Question == nil || strings.TrimSpace(*q.Question) == "" {
			return nil, fmt.Errorf("%s: question text is required", where)
		}
		if utf8.RuneCountInString(*q.Question) > maxQuestionText {
			return nil, fmt.Errorf("%s: question text is over %d characters", where, maxQuestionText)
		}
		if q.Options == nil || len(*q.Options) < minOptions || len(*q.Options) > maxOptions {
			got := 0
			if q.Options != nil {
				got = len(*q.Options)
			}
			return nil, fmt.Errorf("%s: give %d to %d options (got %d)", where, minOptions, maxOptions, got)
		}
		optionIDs := map[string]bool{}
		options := make([]Option, 0, len(*q.Options))
		recommended := 0
		for j, o := range *q.Options {
			optionWhere := fmt.Sprintf("%s option %d", where, j+1)
			if o.ID == nil || !idPattern.MatchString(*o.ID) {
				return nil, fmt.Errorf("%s: %s", optionWhere, idRule)
			}
			if optionIDs[*o.ID] {
				return nil, fmt.Errorf("%s: duplicate option id %q", optionWhere, *o.ID)
			}
			optionIDs[*o.ID] = true
			if o.Label == nil || strings.TrimSpace(*o.Label) == "" {
				return nil, fmt.Errorf("%s: label is required", optionWhere)
			}
			if utf8.RuneCountInString(*o.Label) > maxOptionLabel {
				return nil, fmt.Errorf("%s: label is over %d characters", optionWhere, maxOptionLabel)
			}
			option := Option{ID: *o.ID, Label: *o.Label}
			if o.Recommended != nil && *o.Recommended {
				recommended++
				option.Recommended = true
			}
			options = append(options, option)
		}
		if recommended > 1 {
			return nil, fmt.Errorf("%s: mark at most one option as recommended", where)
		}
		out = append(out, Question{ID: *q.ID, Question: *q.Question, Options: options})
	}
	return out, nil
}
