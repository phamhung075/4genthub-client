package clientskills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RecordFile is the decision record a skill carries beside its SKILL.md. It travels with the skill
// because the runtime copies the whole skill directory at every seat start (install_seat_skills, in
// rust/agenthub-runtime/src/supervisor/mod.rs), so a record cannot drift from the skill it judges.
//
// THE RECORD HOLDS DECISIONS, NEVER COUNTERS. Nothing in this client can observe which skill a model
// loaded, so a hit counter would be a number nobody could fill; the only facts stored are the ones a
// person or a gate decided and cited.
const RecordFile = "record.json"

// The two states a record can hold, and the three shapes of a change. They are OpenSpace's vocabulary
// trimmed to what this repo can actually fill: PROVISIONAL/TRUSTED wholesale, and FIX/DERIVED/CAPTURED as
// the record's `change` field rather than a revision DAG.
const (
	StateProvisional = "provisional"
	StateTrusted     = "trusted"

	ChangeFix      = "fix"
	ChangeDerived  = "derived"
	ChangeCaptured = "captured"
)

// Record is one skill's decision record, as `record.json` stores it.
//
// An ABSENT file is not an error and not a zero value: it is `provisional` with no `since`, which is what a
// skill the census has never had an opinion about looks like (see DefaultRecord).
type Record struct {
	State    string   `json:"state"`
	Since    string   `json:"since"`
	Change   string   `json:"change"`
	Evidence []string `json:"evidence"`
	Risk     string   `json:"risk"`
}

// DefaultRecord is what an absent record.json means: provisional, with no commit that set it. `since` is
// empty rather than guessed - the commit that introduced the skill is a READ of history, and that read
// belongs to the census windows, not to the type.
func DefaultRecord() Record {
	return Record{State: StateProvisional}
}

// sincePattern is a committed object name: full or abbreviated hex. A `since` that is not a commit is a
// record nothing can window on, so it is refused where it is written and where it is read.
var sincePattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// skillExists is the loader's own marker test, repeated here for the writer: a record is written only
// beside a SKILL.md, so a mistyped directory cannot grow a record of its own.
func skillExists(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, SkillFile))
	return err == nil && info.Mode().IsRegular()
}

// Validate refuses a record the census could not act on: an unknown state or change, a `since` that is not
// a hex object name, a blank or whitespace-carrying evidence id. It does NOT require a `risk` line or
// evidence: an honest record of a decision taken before the loop existed has neither, and inventing them
// here would write words nobody decided.
func (r Record) Validate() error {
	switch r.State {
	case StateProvisional, StateTrusted:
	default:
		return fmt.Errorf("record state %q is neither %q nor %q", r.State, StateProvisional, StateTrusted)
	}
	if r.Change != "" {
		switch r.Change {
		case ChangeFix, ChangeDerived, ChangeCaptured:
		default:
			return fmt.Errorf("record change %q is none of %q, %q, %q",
				r.Change, ChangeFix, ChangeDerived, ChangeCaptured)
		}
	}
	if r.Since != "" && !sincePattern.MatchString(r.Since) {
		return fmt.Errorf("record since %q is not a hex commit name", r.Since)
	}
	for i, id := range r.Evidence {
		if strings.TrimSpace(id) != id || id == "" {
			return fmt.Errorf("record evidence[%d] is blank or carries surrounding space: %q", i, id)
		}
	}
	return nil
}

// ReadRecord reads the record beside a skill's SKILL.md. The bool reports whether the file was there:
// absent means DefaultRecord and no error, because a skill with no record is the normal state of every
// skill until the first proposal. A present but unreadable or invalid record IS an error - a record the
// census cannot trust must not be silently replaced by the default.
func ReadRecord(dir string) (Record, bool, error) {
	path := filepath.Join(dir, RecordFile)
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultRecord(), false, nil
		}
		return Record{}, false, fmt.Errorf("cannot read %s: %w", path, err)
	}
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(body))
	// A misspelled key is a decision that silently did not happen, so it is refused instead of ignored.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, true, fmt.Errorf("%s is not a record: %w", path, err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, true, fmt.Errorf("%s: %w", path, err)
	}
	return record, true, nil
}

// WriteRecord writes the record beside a skill's SKILL.md, indented and with a trailing newline so a
// proposal is a readable diff. It refuses an invalid record and writes nothing else - no directory is
// created, because the skill directory must already exist for a record about it to mean anything.
func WriteRecord(dir string, record Record) error {
	if record.State == "" {
		record.State = StateProvisional
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if !skillExists(dir) {
		return usageError(dir + " holds no " + SkillFile + ", so it is not a skill to record")
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	path := filepath.Join(dir, RecordFile)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}
