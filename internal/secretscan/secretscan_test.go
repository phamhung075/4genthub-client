package secretscan

import (
	"encoding/json"
	"os"
	"testing"
)

func TestContainsMatchesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/scan_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name      string `json:"name"`
			Text      string `json:"text"`
			HasSecret bool   `json:"has_secret"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fixture.Cases {
		if got := Contains(c.Text); got != c.HasSecret {
			t.Errorf("%s: Contains = %v, want %v", c.Name, got, c.HasSecret)
		}
	}
}
