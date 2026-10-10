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

// The machine default is still the third clause, and it is the real home's tree - never a relative path.
func TestAResolvableHomeGivesTheMachineDefault(t *testing.T) {
	restore := realHome
	realHome = func() (string, error) { return "/home/someone", nil }
	defer func() { realHome = restore }()
	t.Setenv(StateRootEnv, "")

	if root, err := ResolveStateRoot(""); err != nil || root != "/home/someone/.openrig/state/omp" {
		t.Errorf("ResolveStateRoot = %q, %v; want the machine default", root, err)
	}
}

// A machine with no resolvable home REFUSES the seat store instead of answering a path relative to the
// working directory: a relative store would make the tree a function of where the client was run from.
func TestAnUnresolvableHomeRefusesTheSeatStore(t *testing.T) {
	restore := realHome
	realHome = func() (string, error) { return "", errors.New("no home") }
	defer func() { realHome = restore }()

	if store := DefaultSeatStore(); store != "" {
		t.Errorf("DefaultSeatStore = %q, want empty: a relative path is never returned", store)
	}
	err := func() error { _, err := ResolveSeatStore(""); return err }()
	if code := clientcmd.CodeOf(err, clientcmd.ExitRemote); code != clientcmd.ExitUsage {
		t.Errorf("the refusal exits %d, want %d", code, clientcmd.ExitUsage)
	}
	if err == nil || !strings.Contains(err.Error(), SeatStoreMessage) {
		t.Errorf("the refusal does not carry SeatStoreMessage: %v", err)
	}
}

// A resolvable home gives the seat store under the account home's tree - never a relative path.
func TestAResolvableHomeGivesTheSeatStore(t *testing.T) {
	restore := realHome
	realHome = func() (string, error) { return "/home/someone", nil }
	defer func() { realHome = restore }()

	if store, err := ResolveSeatStore(""); err != nil || store != "/home/someone/.openrig/agenthub-seats" {
		t.Errorf("ResolveSeatStore = %q, %v; want the machine default", store, err)
	}
}

// The refusal names the escape only where the verb really accepts one. The shared reason must stay
// flag-free - the bridge and `sync bundle` show it as it is - and SeatStoreOutFlag is what a verb with
// --out shows, with the code preserved so the exit status does not depend on the form.
func TestTheSeatStoreRefusalNamesTheEscapeOnlyWhenTheVerbHasOne(t *testing.T) {
	reason := &clientcmd.CodedError{Message: SeatStoreMessage, Code: clientcmd.ExitUsage}

	if strings.Contains(SeatStoreMessage, "--out") {
		t.Errorf("the shared reason advertises a flag two callers do not accept: %q", SeatStoreMessage)
	}
	if alone := SeatStoreRefusal(reason, ""); alone == nil || alone.Error() != SeatStoreMessage {
		t.Errorf("with no flag the refusal is %v, want the reason alone", alone)
	}
	withFlag := SeatStoreRefusal(reason, SeatStoreOutFlag)
	if withFlag == nil || !strings.Contains(withFlag.Error(), SeatStoreOutFlag) {
		t.Fatalf("the refusal does not name the flag the verb accepts: %v", withFlag)
	}
	if !strings.Contains(withFlag.Error(), SeatStoreMessage) {
		t.Errorf("the refusal dropped the reason: %v", withFlag)
	}
	if code := clientcmd.CodeOf(withFlag, clientcmd.ExitRemote); code != clientcmd.ExitUsage {
		t.Errorf("the refusal exits %d, want %d", code, clientcmd.ExitUsage)
	}
	if SeatStoreRefusal(nil, SeatStoreOutFlag) != nil {
		t.Error("a nil error grew an escape")
	}
}
