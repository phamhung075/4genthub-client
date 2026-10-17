package clientteam

import (
	"os"
	"path/filepath"
	"testing"
)

// The thinking level a seat runs at rides its model SELECTOR: `deepseek/deepseek-flash:low`. omp's
// resolver reads the `:level` suffix (off|minimal|low|medium|high|xhigh|max|auto|inherit), and the
// runtime's runner passes this field through as `--model` - so anything here that parsed, trimmed or
// validated the selector would silently drop a seat's level and leave it at the model's default.
//
// WHY A TEST AND NOT JUST A DOC: the level is data from team.json to argv with no code of its own, so
// the only place the property can regress is this copy. Measured on the initial 5/8 authoring: the
// suffix reaches the harness - `omp -p --model 'deepseek/deepseek-flash:low'` writes
// {"type":"thinking_level_change","thinkingLevel":"low"} into its transcript.
func TestLoadRoomSpecCarriesTheThinkingLevelSuffixVerbatim(t *testing.T) {
	directory := t.TempDir()
	body := `{"room":{"slug":"demo"},"seats":[` +
		`{"seat_key":"go-dev","seat_type":"developer","runtime":"omp","model":"deepseek/deepseek-flash:low"},` +
		`{"seat_key":"architect","seat_type":"architect","runtime":"omp","model":"deepseek/deepseek-flash:high"}` +
		`],"links":[{"from":"go-dev","kind":"collaborates_with","to":"architect"}],"seat_overlays":{}}`
	if err := os.WriteFile(filepath.Join(directory, "team.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadRoomSpec(directory)
	if err != nil {
		t.Fatalf("LoadRoomSpec: %v", err)
	}
	want := map[string]string{
		"go-dev":    "deepseek/deepseek-flash:low",
		"architect": "deepseek/deepseek-flash:high",
	}
	if len(spec.Seats) != len(want) {
		t.Fatalf("seats = %d, want %d", len(spec.Seats), len(want))
	}
	for _, seat := range spec.Seats {
		selector, ok := want[seat.SeatKey]
		if !ok {
			t.Errorf("unexpected seat %q", seat.SeatKey)
			continue
		}
		if seat.Model == nil {
			t.Errorf("%s: model = nil, want %q", seat.SeatKey, selector)
			continue
		}
		if *seat.Model != selector {
			t.Errorf("%s: model = %q, want %q verbatim - the level suffix is the seat's thinking level",
				seat.SeatKey, *seat.Model, selector)
		}
	}
}
