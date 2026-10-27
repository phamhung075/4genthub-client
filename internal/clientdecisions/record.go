// Package clientdecisions keeps the structured questions of an owner ask beside the room that asked it,
// and turns the owner's dashboard answer into the note the asking seat reads.
//
// WHY A RECORD BESIDE THE ROW. An owner ask is a queue row (tag owner-decision, addressed to
// human@<room>), and the dashboard's Decision Requests panel answers structured questions: 1-4 questions
// of 2-4 options each, clicked by id. The rail's row has a 600-character body and no questions field, so
// the questions ride this record, written by `room queue create --questions-file` once the row exists and
// read by the bridge, which mirrors the ask to the hub, pulls the owner's answer and applies it to the row.
// The record is the bridge's progress too: the hub's decision id once mirrored, and Applied once the
// answer closed the row, so a restarted bridge neither asks twice nor applies twice.
//
// The record is removed once the hub's row is closed (applied, or superseded because the rail row closed
// another way). One file per ask: <state-root>/rooms/<room>/decisions/<qitem>.json.
package clientdecisions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Tag is the rail tag of an owner ask, the runtime's own word (queue/mod.rs OWNER_DECISION_TAG).
const Tag = "owner-decision"

// HumanPrefix addresses a room's human on the rail: human@<room>.
const HumanPrefix = "human@"

// Human is the session that addresses, and answers for, one room's owner.
func Human(room string) string { return HumanPrefix + room }

// Record is one owner ask's questions and the bridge's progress on it.
type Record struct {
	QitemID   string     `json:"qitem_id"`
	Room      string     `json:"room"`
	Questions []Question `json:"questions"`
	// DecisionID is the hub's id for the mirrored ask; empty until the hub accepted it.
	DecisionID string `json:"decision_id,omitempty"`
	// Refused is the hub's answer to an ask it will never accept (for example a room the hub does not
	// know). The bridge stops asking and the row stays a notification only.
	Refused string `json:"refused,omitempty"`
	// Applied is set once the owner's answer closed the rail row; only the hub's ack is then owed.
	Applied bool `json:"applied,omitempty"`
}

// Dir is the directory of one room's records.
func Dir(root, room string) string {
	return filepath.Join(root, "rooms", room, "decisions")
}

func path(root, room, qitem string) (string, error) {
	if qitem == "" || strings.ContainsAny(qitem, `/\`) || qitem == "." || qitem == ".." {
		return "", fmt.Errorf("invalid queue row id %q", qitem)
	}
	return filepath.Join(Dir(root, room), qitem+".json"), nil
}

// Write stores a record through a temporary file and a rename, so a reader sees the whole old record or
// the whole new one.
func Write(root string, record Record) error {
	target, err := path(root, record.Room, record.QitemID)
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, append(payload, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}

// List reads one room's records, ordered by row id (which is creation order). A room with no directory
// has no records. A file that is not a record is an error naming it: a record the bridge cannot read is
// an ask the owner cannot answer, and that must be seen rather than skipped.
func List(root, room string) ([]Record, error) {
	entries, err := os.ReadDir(Dir(root, room))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(Dir(root, room), name))
		if err != nil {
			return nil, err
		}
		var record Record
		if err := json.Unmarshal(raw, &record); err != nil || record.QitemID == "" {
			return nil, fmt.Errorf("%s is not a decision record", filepath.Join(Dir(root, room), name))
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QitemID < out[j].QitemID })
	return out, nil
}

// Remove deletes one record; a record already gone is not an error.
func Remove(root, room, qitem string) error {
	target, err := path(root, room, qitem)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// AnswerNote renders the owner's answers as the note the asking seat receives: each question with the
// label of the chosen option, in the ask's own question order. answers maps question id to option id,
// the hub's shape; an option id the ask does not carry is shown as the id, never dropped.
func AnswerNote(questions []Question, answers map[string]string) string {
	parts := make([]string, 0, len(questions))
	for _, question := range questions {
		chosen, ok := answers[question.ID]
		if !ok {
			continue
		}
		label := chosen
		for _, option := range question.Options {
			if option.ID == chosen {
				label = option.Label
				break
			}
		}
		parts = append(parts, fmt.Sprintf("%s -> %s [%s]", question.Question, label, chosen))
	}
	return "Owner's answer (dashboard): " + strings.Join(parts, "; ") + ". Act on it, then close this row."
}
