package clientenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSetsMissingNamesAndLeavesTheEnvironmentWinning(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# a comment\n\ngent_from_file=file\nGENTEAM_FROM_ENV=file\nQUOTED=\"spaced value\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GENTEAM_FROM_ENV", "environment")
	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("gent_from_file"); got != "file" {
		t.Errorf("a name in the file was not set: %q", got)
	}
	if got := os.Getenv("GENTEAM_FROM_ENV"); got != "environment" {
		t.Errorf("the file overrode the environment: %q", got)
	}
	if got := os.Getenv("QUOTED"); got != "spaced value" {
		t.Errorf("a quoted value was not unquoted: %q", got)
	}
}

func TestLoadOfAMissingFileIsNotAnError(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Errorf("a missing env file was an error: %v", err)
	}
}

func TestLimitBlankMeansTheDefault(t *testing.T) {
	t.Setenv("GENTEAM_TEST_LIMIT", "")
	got, err := Limit("GENTEAM_TEST_LIMIT", 15)
	if err != nil || got != 15 {
		t.Errorf("blank limit = %d, %v, want the default 15", got, err)
	}
}

func TestLimitReadsAValue(t *testing.T) {
	t.Setenv("GENTEAM_TEST_LIMIT", "250")
	got, err := Limit("GENTEAM_TEST_LIMIT", 15)
	if err != nil || got != 250 {
		t.Errorf("limit = %d, %v, want 250", got, err)
	}
}

func TestLimitRefusesATypoAndNamesIt(t *testing.T) {
	t.Setenv("GENTEAM_TEST_LIMIT", "soon")
	if _, err := Limit("GENTEAM_TEST_LIMIT", 15); err == nil {
		t.Fatal("a non-numeric limit was accepted")
	} else if !strings.Contains(err.Error(), "GENTEAM_TEST_LIMIT") {
		t.Errorf("the refusal did not name the variable: %v", err)
	}
}
