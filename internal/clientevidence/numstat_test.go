package clientevidence

import (
	"strings"
	"testing"
)

// The numstat is read in ONE place now: the gate's D5 and the judge's state both call this parser, so its
// rows, totals and binary handling are what "how many files / how many lines" means on both sides.
func TestParseNumstatRowsTotalsAndBinary(t *testing.T) {
	stats, err := ParseNumstat("3\t1\ta.go\n20\t0\tb.md\n-\t-\tlogo.png\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []NumstatRow{
		{Path: "a.go", Added: 3, Deleted: 1},
		{Path: "b.md", Added: 20},
		{Path: "logo.png", Binary: true},
	}
	if len(stats.Rows) != len(want) {
		t.Fatalf("rows = %+v, want %+v", stats.Rows, want)
	}
	for i := range want {
		if stats.Rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, stats.Rows[i], want[i])
		}
	}
	if stats.Files() != 3 || stats.Added != 23 || stats.Deleted != 1 || stats.Total() != 24 {
		t.Errorf("files %d, added %d, deleted %d, total %d: want 3 / 23 / 1 / 24 (a binary row has no counts, "+
			"not zero lines)", stats.Files(), stats.Added, stats.Deleted, stats.Total())
	}
	empty, err := ParseNumstat("")
	if err != nil || empty.Files() != 0 || empty.Total() != 0 {
		t.Errorf("an empty numstat = %+v, %v: want empty and no error", empty, err)
	}
}

// D5 quotes these two strings verbatim in its rejection, so the shared parser must not reword them.
func TestParseNumstatKeepsTheGatesErrorStrings(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"garbage\n", `numstat line "garbage" is not added, deleted, path`},
		{"x\t1\ta.go\n", `numstat line "x\t1\ta.go" has a non-numeric count`},
	} {
		_, err := ParseNumstat(tc.raw)
		if err == nil || err.Error() != tc.want {
			t.Errorf("ParseNumstat(%q) error = %v, want %q", tc.raw, err, tc.want)
		}
	}
}

// The wire's row order is a RULE, not the numstat's path order, so the sequence names no file: largest total
// first, then added, then deleted, binary last, and rows that tie keep their numstat order (stable).
func TestWireRowsOrderIsARuleNotThePathOrder(t *testing.T) {
	stats, err := ParseNumstat("1\t0\tsmall.go\n5\t5\ttie-b.go\n-\t-\tz.png\n5\t5\ttie-a.go\n0\t9\tdeleted.go\n")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range stats.WireRows() {
		got = append(got, row.Path)
	}
	want := []string{"tie-b.go", "tie-a.go", "deleted.go", "small.go", "z.png"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("WireRows = %v, want %v", got, want)
	}
	if stats.Rows[0].Path != "small.go" {
		t.Errorf("WireRows reordered the stats' own rows: %v", stats.Rows)
	}
}
