// The `skills` doctor row: the census of what a room's skills declare and what the typed streams have done
// since each record was set. It exists because the census is only worth having if something fails on it -
// a report nobody reads is not a check (design note S4).
//
// IT READS AND REPORTS, and it repairs nothing: the same rule as the worktrees row. A demote is fixed by a
// `skills propose` and a gate, and a broken record by the commit that names the right evidence; a doctor
// row that "fixed" either would be a second writer for the skill tree.
//
// THE TWO FAILURES ARE THE CENSUS'S OWN, not a new vocabulary: `demote` (a trusted skill a declared signal
// contradicted) and `broken-record` (a record that cannot be read, or one citing evidence that does not
// resolve). `promotable` is a DETAIL - a skill ready for its gate is not a defect in the room - and a
// `no-signal` skill is reported as what it is rather than failed, because a skill nobody armed is a finding
// about the skill, not a fault of the client.
package clientlifecycle

import (
	"fmt"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientskills"
)

// skillsRow is one room's census row: the safeguard row plus the skills that decided it, so the reasons and
// the state cannot disagree about which skill failed.
type skillsRow struct {
	Row     clientservices.Health
	Demoted []clientskills.Row
	Broken  []clientskills.Row
	// Skills is every row the census answered, in its own order, so the report can print ONE verdict per
	// skill: the row's state is the headline, and the per-skill lines are what the headline is made of.
	Skills []clientskills.Row
	// Notes are the streams or inputs the census could not read. They ride beside the row because a census
	// that read nothing must not print as a census that found nothing.
	Notes []string
	// Note is the one reason the row has no census at all (no tree, no state root): the row then says what
	// it could not look at instead of reporting a room it never measured.
	Note string
}

// skillsRowFor is the skills-row computation as a seam, beside rowsFor, worktreesRowFor and seatsRowFor: a
// case drives the row without a skill tree, a rail or a git repository on the machine.
var skillsRowFor = skillsRows

// censusFor is the census itself as a seam, for the same reason: a case drives a demote, a broken record and
// a promotable from rows it wrote down, so the row's rules are tested rather than the census's own fixtures.
var censusFor = clientskills.CensusRoom

// skillsRows computes the `skills` row of every room the report covers - agent.json's list, the same one the
// worktrees and safeguard rows read, so the row families cannot disagree about which rooms exist.
func skillsRows(room, root string) []skillsRow {
	state, _ := clientservices.ReadAgentState()
	rooms := reportedRooms(state.Rooms, room)
	rows := make([]skillsRow, 0, len(rooms))
	for _, name := range rooms {
		rows = append(rows, skillsRowOf(name, root))
	}
	return rows
}

// skillsRowOf is one room's row. An input it cannot read becomes the row's note, and the note is printed
// beside the row: the same rule the worktrees row keeps, where the state answers only what was measured and
// the note says what could not be.
func skillsRowOf(name, root string) skillsRow {
	row := skillsRow{Row: clientservices.Health{
		Room:      name,
		Safeguard: clientservices.Skills,
		State:     clientservices.Running,
		AgeS:      clientservices.UnknownAge,
	}}
	unobserved := func(err error) skillsRow {
		row.Note = clientservices.Skills + ": not observed: " + err.Error()
		return row
	}
	stateRoot := root
	if stateRoot == "" {
		var err error
		if stateRoot, err = clientenv.ResolveStateRoot(""); err != nil {
			return unobserved(err)
		}
	}
	// The tree is the ONE variable the runtime installs from, read exactly as the seats row reads it: a
	// census over a tree the runtime never installed would judge skills no seat receives.
	treeRoot, err := skillsTreeRoot(stateRoot)
	if err != nil {
		return unobserved(err)
	}
	if err := (clientskills.Tree{Root: treeRoot}).Require(); err != nil {
		return unobserved(err)
	}
	rows, notes, err := censusFor(stateRoot, name, treeRoot)
	if err != nil {
		return unobserved(err)
	}
	row.Notes = notes
	row.Skills = rows
	for _, skill := range rows {
		switch skill.Verdict {
		case clientskills.VerdictDemote:
			row.Demoted = append(row.Demoted, skill)
		case clientskills.VerdictBrokenRecord:
			row.Broken = append(row.Broken, skill)
		}
	}
	// A demote wins the state: it is the loop acting on a skill the room trusted, and the broken records
	// are still in the reasons beside it.
	switch {
	case len(row.Demoted) > 0:
		row.Row.State = clientservices.SkillsDemote
	case len(row.Broken) > 0:
		row.Row.State = clientservices.SkillsBroken
	}
	return row
}

// skillsLines is the row's whole answer as the report prints it: the reasons first, then the details.
func skillsLines(row skillsRow) []string {
	return append(skillsReasonLines(row), skillsDetailLines(row)...)
}

// skillsReasonLines are the lines that say WHY the row is down: one per demoted skill, naming the skill, the
// signal that fired and the cited rows that fired it, and one per broken record, naming the skill and the
// evidence id that does not resolve.
func skillsReasonLines(row skillsRow) []string {
	var lines []string
	for _, skill := range row.Demoted {
		lines = append(lines, fmt.Sprintf("%s: %s %s demote: %s occurred %d time(s) since %s%s",
			clientservices.Skills, row.Row.Room, skillLabel(skill), strings.Join(skill.Signals, ","),
			skill.Occurrences, skillWindow(skill), refList(skill.Refs)))
	}
	for _, skill := range row.Broken {
		lines = append(lines, fmt.Sprintf("%s: %s %s broken-record: %s",
			clientservices.Skills, row.Row.Room, skillLabel(skill), brokenReason(skill)))
	}
	return lines
}

// skillsDetailLines are the lines a healthy row still prints: ONE line per skill with the verdict the census
// gave it, plus its counts, and the streams the census could not read. None of it is a failure - and a skill
// with no signals is printed as `no-signal` rather than left out, because that is the finding a reader needs
// to arm it. A demoted or broken skill is not repeated here: its reason line already names it.
func skillsDetailLines(row skillsRow) []string {
	var lines []string
	for _, skill := range row.Skills {
		switch skill.Verdict {
		case clientskills.VerdictDemote, clientskills.VerdictBrokenRecord:
			continue
		}
		line := fmt.Sprintf("%s: %s %s %s (signals %d)",
			clientservices.Skills, row.Row.Room, skillLabel(skill), skill.Verdict, len(skill.Signals))
		if skill.Occurrences > 0 || skill.Commands > 0 || skill.Landed > 0 {
			line += fmt.Sprintf(", occ %d cmd %d landed %d", skill.Occurrences, skill.Commands, skill.Landed)
		}
		if skill.Window != "" {
			line += ", since " + skill.Window
		}
		if len(skill.Unread) > 0 {
			line += ", unread " + strings.Join(skill.Unread, ",")
		}
		lines = append(lines, line)
	}
	for _, note := range row.Notes {
		lines = append(lines, clientservices.Skills+": "+row.Row.Room+" not observed: "+note)
	}
	if row.Note != "" {
		lines = append(lines, row.Note)
	}
	return lines
}

// skillLabel is the skill as a reader names it: the level that delivers it and its name, because two levels
// can hold a skill of the same name.
func skillLabel(skill clientskills.Row) string {
	if skill.Level == "" {
		return skill.Name
	}
	return skill.Level + "/" + skill.Name
}

// skillWindow is the commit the census measured from, or a word for "there was none": a row without a
// window is a fact the reader must see, not an empty gap.
func skillWindow(skill clientskills.Row) string {
	if skill.Window == "" {
		return "no window"
	}
	return skill.Window
}

// refList names the rows or commits that made a signal fire, after a separator so a report line reads as one
// sentence. The cited refs are what an operator opens to see the failure for themself.
func refList(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return " (" + strings.Join(refs, ",") + ")"
}

// brokenReason says which part of a record could not be trusted: the ids that did not resolve when the
// record named some, and the fault the reader hit when it did not.
func brokenReason(skill clientskills.Row) string {
	if len(skill.Broken) > 0 {
		return strings.Join(skill.Broken, ",") + " does not resolve"
	}
	if skill.Fault != "" {
		return skill.Fault
	}
	return "the record could not be read"
}
