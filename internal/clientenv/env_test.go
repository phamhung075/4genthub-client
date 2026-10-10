package clientenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
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

// A machine with no resolvable home REFUSES the state root instead of answering a path relative to the
// working directory: a relative root would make the state tree a function of where the client was run
// from, which is the dependency the mission forbids. The refusal names BOTH escapes.
func TestAnUnresolvableHomeRefusesTheStateRoot(t *testing.T) {
	restore := realHome
	realHome = func() (string, error) { return "", errors.New("no home") }
	defer func() { realHome = restore }()
	t.Setenv(StateRootEnv, "")

	if root := DefaultStateRoot(); root != "" {
		t.Errorf("DefaultStateRoot = %q, want empty: a relative path is never returned", root)
	}
	_, err := ResolveStateRoot("")
	if err == nil {
		t.Fatal("an unresolvable state root was accepted")
	}
	if code := clientcmd.CodeOf(err, clientcmd.ExitRemote); code != clientcmd.ExitUsage {
		t.Errorf("the refusal exits %d, want %d", code, clientcmd.ExitUsage)
	}
	for _, want := range []string{"--state-root", StateRootEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
}

// The flag and the variable are checked BEFORE the machine's own default, so a named root is used even
// where the home cannot be resolved.
func TestTheStateRootPrefersTheFlagThenTheEnvironment(t *testing.T) {
	restore := realHome
	realHome = func() (string, error) { return "", errors.New("no home") }
	defer func() { realHome = restore }()
	t.Setenv(StateRootEnv, "/from/environment")

	if root, err := ResolveStateRoot("/from/flag"); err != nil || root != "/from/flag" {
		t.Errorf("with the flag set: %q, %v; want /from/flag", root, err)
	}
	if root, err := ResolveStateRoot(""); err != nil || root != "/from/environment" {
		t.Errorf("with only the variable set: %q, %v; want /from/environment", root, err)
	}
}
