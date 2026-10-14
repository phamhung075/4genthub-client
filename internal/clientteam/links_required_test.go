package clientteam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A room is created with its edges: more than one seat and no links is refused at load, so the dry run
// and the apply both stop before anything is sent.
func TestLoadTeamRefusesARoomOfSeveralSeatsWithNoLinks(t *testing.T) {
	for _, c := range []struct {
		name    string
		seats   string
		links   string
		refused bool
	}{
		{"two seats, no links", `[{"seat_key":"lead"},{"seat_key":"dev"}]`, `[]`, true},
		{"two seats, one link", `[{"seat_key":"lead"},{"seat_key":"dev"}]`, `[{"from":"lead","kind":"delegates_to","to":"dev"}]`, false},
		{"one seat needs no link", `[{"seat_key":"lead"}]`, `[]`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			directory := t.TempDir()
			body := `{"room":{"slug":"demo"},"seats":` + c.seats + `,"links":` + c.links + `,"seat_overlays":{}}`
			if err := os.WriteFile(filepath.Join(directory, "team.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := loadTeam(directory)
			if c.refused != (err != nil) {
				t.Fatalf("refused=%v, err=%v", c.refused, err)
			}
			if c.refused && !strings.Contains(err.Error(), "needs its links") {
				t.Fatalf("the refusal must name the missing links: %v", err)
			}
		})
	}
}
