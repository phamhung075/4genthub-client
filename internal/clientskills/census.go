package clientskills

import (
	"path/filepath"
	"sort"
)

// The three verdicts S1 decides. They are the whole of the slice: `promotable` and `demote` need the
// census windows and the typed streams (S2), and a verdict this client cannot justify is worse than none.
const (
	// VerdictHold is a skill with declared signals and a record nothing contradicts.
	VerdictHold = "hold"
	// VerdictNoSignal is a skill that declares no signals: nobody can hold it to account, so the report
	// says so rather than skipping it.
	VerdictNoSignal = "no-signal"
	// VerdictBrokenRecord is a skill whose record cannot be read, or whose evidence ids do not resolve.
	// It blocks promotion: a decision citing something that does not exist is not a decision.
	VerdictBrokenRecord = "broken-record"
)

// Resolution is what one evidence check established. Checked is false when the checker could not look (the
// queue daemon is down, the tree is not in a git repository): an id it could not look up is NOT reported as
// broken, because "not found" and "not looked for" are different facts.
type Resolution struct {
	Checked bool
	Found   bool
	Reason  string
}

// Resolver looks one evidence id up. It is a seam: the verb resolves queue rows and commit SHAs, and the
// tests answer from memory instead of a daemon and a repository.
type Resolver func(id string) Resolution

// RoomSkill is one skill a room delivers: the skill itself plus the level that delivers it, which is
// "share", "room", or "seat:<seat>".
type RoomSkill struct {
	Skill
	Level string
}

// Row is one skill's census line.
type Row struct {
	Room     string   `json:"room"`
	Name     string   `json:"name"`
	Level    string   `json:"level"`
	Path     string   `json:"path"`
	State    string   `json:"state"`
	Since    string   `json:"since"`
	Change   string   `json:"change"`
	Risk     string   `json:"risk"`
	Record   bool     `json:"record"`
	Signals  []string `json:"signals"`
	Evidence []string `json:"evidence"`
	Broken   []string `json:"broken"`
	Verdict  string   `json:"verdict"`
	// Fault is the one-line reason a record or a SKILL.md could not be read; empty otherwise.
	Fault string `json:"fault,omitempty"`
}

// RoomSkills lists EVERY skill a room delivers: share/, the room's room-level skills, and the skills
// inside each of that room's seat folders. It is the tree-wide reading the census needs, where
// Tree.Resolve answers the narrower question - one seat's installed set.
//
// A name delivered by two levels is fine HERE and reported as two rows with their own levels: the refusal
// Tree.Resolve makes is about one seat's installation, where two copies would collide on disk. Two seats
// both holding a skill of the same name is normal (go-dev-role lives in the go-dev folder only, but two
// seats could each carry a `review` skill) and is what Level exists to separate.
func (t Tree) RoomSkills(room string) ([]RoomSkill, error) {
	if err := validateName("room", room); err != nil {
		return nil, err
	}
	if err := t.Require(); err != nil {
		return nil, err
	}
	var skills []RoomSkill

	shareDir := filepath.Join(t.Root, ShareDir)
	shareNames, err := subdirs(shareDir)
	if err != nil {
		return nil, err
	}
	for _, name := range shareNames {
		dir := filepath.Join(shareDir, name)
		if !isSkill(dir) {
			return nil, notASkill(dir)
		}
		skills = append(skills, RoomSkill{Skill: Skill{Name: name, Dir: dir}, Level: ShareDir})
	}

	roomDir := filepath.Join(t.Root, RoomsDir, room)
	roomNames, err := subdirsIfPresent(roomDir)
	if err != nil {
		return nil, err
	}
	var seats []string
	for _, name := range roomNames {
		dir := filepath.Join(roomDir, name)
		if isSkill(dir) {
			skills = append(skills, RoomSkill{Skill: Skill{Name: name, Dir: dir}, Level: "room"})
			continue
		}
		seats = append(seats, name)
	}
	for _, seat := range seats {
		seatDir := filepath.Join(roomDir, seat)
		names, err := subdirs(seatDir)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			dir := filepath.Join(seatDir, name)
			if !isSkill(dir) {
				return nil, notASkill(dir)
			}
			skills = append(skills, RoomSkill{Skill: Skill{Name: name, Dir: dir}, Level: "seat:" + seat})
		}
	}

	sort.Slice(skills, func(i, j int) bool {
		if rank := levelRank(skills[i].Level) - levelRank(skills[j].Level); rank != 0 {
			return rank < 0
		}
		if skills[i].Level != skills[j].Level {
			return skills[i].Level < skills[j].Level
		}
		return skills[i].Name < skills[j].Name
	})
	return skills, nil
}

// levelRank orders the report the way a reader expects the tree: every room's share/, then this room's own
// skills, then its seat folders. Alphabetical order would put "room" between "seat:..." and "share".
func levelRank(level string) int {
	switch {
	case level == ShareDir:
		return 0
	case level == "room":
		return 1
	default:
		return 2
	}
}

// Census reads every skill of a room and decides the S1 verdict for each. It reads ONLY the tree: the
// record beside each SKILL.md, the `signals:` list in its front matter, and whatever the resolver can say
// about the record's evidence ids. Nothing here reads a commit window or a failure stream.
//
// A resolver may be nil, which means "this caller cannot look ids up at all"; no id is then broken and each
// row still reports what it cites. The verb always passes one.
func (t Tree) Census(room string, resolve Resolver) ([]Row, error) {
	skills, err := t.RoomSkills(room)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(skills))
	for _, skill := range skills {
		rows = append(rows, censusRow(room, skill, resolve))
	}
	return rows, nil
}

func censusRow(room string, skill RoomSkill, resolve Resolver) Row {
	row := Row{
		Room:     room,
		Name:     skill.Name,
		Level:    skill.Level,
		Path:     skill.Dir,
		Evidence: []string{},
		Broken:   []string{},
	}

	record, present, err := ReadRecord(skill.Dir)
	row.Record = present
	if err != nil {
		row.State = StateProvisional
		row.Fault = err.Error()
		row.Verdict = VerdictBrokenRecord
	} else {
		row.State = record.State
		row.Since = record.Since
		row.Change = record.Change
		row.Risk = record.Risk
		row.Evidence = record.Evidence
		if row.Evidence == nil {
			row.Evidence = []string{}
		}
	}

	if resolve != nil {
		for _, id := range row.Evidence {
			res := resolve(id)
			if res.Checked && !res.Found {
				row.Broken = append(row.Broken, id)
			}
		}
	}
	if len(row.Broken) > 0 {
		row.Verdict = VerdictBrokenRecord
	}

	signals, err := skill.Signals()
	if err != nil {
		if row.Fault == "" {
			row.Fault = err.Error()
		}
		row.Verdict = VerdictBrokenRecord
		return row
	}
	row.Signals = make([]string, 0, len(signals))
	for _, signal := range signals {
		row.Signals = append(row.Signals, signal.String())
	}
	if row.Verdict != "" {
		return row
	}
	if len(signals) == 0 {
		row.Verdict = VerdictNoSignal
		return row
	}
	row.Verdict = VerdictHold
	return row
}

// Counts is the room's tally by verdict and by state, so a report's first line is a measurement.
type Counts struct {
	Skills      int `json:"skills"`
	Hold        int `json:"hold"`
	NoSignal    int `json:"no-signal"`
	Broken      int `json:"broken-record"`
	Trusted     int `json:"trusted"`
	Provisional int `json:"provisional"`
}

// Count tallies rows.
func Count(rows []Row) Counts {
	var counts Counts
	for _, row := range rows {
		counts.Skills++
		switch row.Verdict {
		case VerdictHold:
			counts.Hold++
		case VerdictNoSignal:
			counts.NoSignal++
		case VerdictBrokenRecord:
			counts.Broken++
		}
		switch row.State {
		case StateTrusted:
			counts.Trusted++
		default:
			counts.Provisional++
		}
	}
	return counts
}
