package clientjev

import (
	"strings"
	"testing"
)

func TestFromEnvDefaultsAndOwnerOnly(t *testing.T) {
	for _, name := range []string{"TYPESAFE_API_KEY", "JEV_API_KEY", "JEV_TOKEN", "TYPESAFE_BASE_URL", "JEV_DECISION_MODEL", "JEV_DECISION_THRESHOLD"} {
		t.Setenv(name, "")
	}
	t.Setenv("JEV_DECISION_LOG_PATH", "/tmp/x/jev.log")
	t.Setenv("DECISION_OWNER_ONLY_CATEGORIES", " Omp_Cap , ,deploy")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "jev-1.13.0" || cfg.Threshold != 0.8 || cfg.BaseURL != "https://api.typesafe.ai/v1" || cfg.LogPath != "/tmp/x/jev.log" {
		t.Errorf("defaults = %+v", cfg)
	}
	for _, name := range []string{"credentials", "SPEND", "omp_cap", "deploy"} {
		if !cfg.OwnerOnly(name) {
			t.Errorf("%s should be owner-only", name)
		}
	}
	if cfg.OwnerOnly("general") || cfg.OwnerOnly("") {
		t.Error("general must not be owner-only")
	}
}

func TestFromEnvRefusesABadThreshold(t *testing.T) {
	for _, bad := range []string{"high", "0", "1.5", "-1"} {
		t.Setenv("JEV_DECISION_THRESHOLD", bad)
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "JEV_DECISION_THRESHOLD") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestParseQuestionsLimits(t *testing.T) {
	ok := `[{"id":"q1","question":"?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B"}]}]`
	qs, err := ParseQuestions([]byte(ok))
	if err != nil || len(qs) != 1 || qs[0].Recommended() != "a" {
		t.Fatalf("ok: %v %v", qs, err)
	}
	for name, bad := range map[string]string{
		"not an array":    `{}`,
		"empty":           `[]`,
		"bad id":          `[{"id":"a b","question":"?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]`,
		"one option":      `[{"id":"q","question":"?","options":[{"id":"a","label":"A"}]}]`,
		"two recommended": `[{"id":"q","question":"?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B","recommended":true}]}]`,
		"long label":      `[{"id":"q","question":"?","options":[{"id":"a","label":"` + strings.Repeat("x", 76) + `"},{"id":"b","label":"B"}]}]`,
		"duplicate id":    `[{"id":"q","question":"?","options":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}]`,
	} {
		if _, err := ParseQuestions([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
