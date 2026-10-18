package clientevidence

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The count keys are the wire's (and the register's) vocabulary for the numbers a diff carries. One source
// for the spelling: the gate matches a criterion on these, the state sends them, and the doc teaches them.
const (
	KeyFilesChanged = "files_changed"
	KeyLinesChanged = "lines_changed"
	KeyLinesAdded   = "lines_added"
	KeyLinesDeleted = "lines_deleted"
)

// NumstatRow is one `git diff --numstat` line: the path (which never leaves the machine) and its counts.
// Binary is git's "-" for both counts, and such a row has no line counts at all - not zero lines.
type NumstatRow struct {
	Path    string
	Added   int
	Deleted int
	Binary  bool
}

// NumstatStats is the whole numstat: its rows and the two totals the wire states. It is the ONE
// interpretation of the text, so the gate and the wire cannot disagree about the same change.
type NumstatStats struct {
	Rows    []NumstatRow
	Added   int
	Deleted int
}

// ParseNumstat reads `git diff --numstat` lines: added, deleted, path. A binary file prints "-" for both
// counts and carries none. The two error strings are the gate's own (D5 quotes them verbatim), which is why
// the gate's copy of this parser is gone rather than kept beside this one.
func ParseNumstat(raw string) (NumstatStats, error) {
	var stats NumstatStats
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return NumstatStats{}, fmt.Errorf("numstat line %q is not added, deleted, path", line)
		}
		row := NumstatRow{Path: parts[2], Binary: parts[0] == "-" && parts[1] == "-"}
		for i, count := range parts[:2] {
			if count == "-" {
				continue
			}
			n, err := strconv.Atoi(count)
			if err != nil {
				return NumstatStats{}, fmt.Errorf("numstat line %q has a non-numeric count", line)
			}
			if i == 0 {
				row.Added = n
				stats.Added += n
			} else {
				row.Deleted = n
				stats.Deleted += n
			}
		}
		stats.Rows = append(stats.Rows, row)
	}
	return stats, nil
}

// Files is the number of paths the change touches, a binary file included.
func (s NumstatStats) Files() int { return len(s.Rows) }

// Total is the number of lines the change moves.
func (s NumstatStats) Total() int { return s.Added + s.Deleted }

// WireRows is the rows as the wire sends them: largest total first, then added, then deleted, with binary
// rows last. It is a RULE, not the numstat's path order, so the sequence carries no name - and the sort is
// stable, so rows that tie (indistinguishable once the path is dropped) keep a deterministic order.
func (s NumstatStats) WireRows() []NumstatRow {
	rows := make([]NumstatRow, len(s.Rows))
	copy(rows, s.Rows)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Binary != b.Binary {
			return !a.Binary
		}
		if at, bt := a.Added+a.Deleted, b.Added+b.Deleted; at != bt {
			return at > bt
		}
		if a.Added != b.Added {
			return a.Added > b.Added
		}
		return a.Deleted > b.Deleted
	})
	return rows
}
