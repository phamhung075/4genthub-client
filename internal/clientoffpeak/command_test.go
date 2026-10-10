package clientoffpeak

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runOffpeak(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := offpeakCommand{}.Run(context.Background(), nil, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestEveryFormOfThisVerbAnswersHelp - the mission's rule, and the reason the dispatcher answers help
// before the platform matrix: a verb's own usage has to be readable on any machine.
func TestEveryFormOfThisVerbAnswersHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"set", "--help"}, {"status", "--help"}} {
		code, out, _ := runOffpeak(t, args...)
		if code != 0 {
			t.Errorf("%v exited %d, want 0", args, code)
		}
		if !strings.Contains(out, "usage: 4genteam offpeak") {
			t.Errorf("%v printed %q, want the usage", args, out)
		}
	}
}

func TestAnUnknownArgumentIsNamedAsACallerError(t *testing.T) {
	code, out, errOut := runOffpeak(t, "--wat")
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--wat") {
		t.Errorf("stderr %q does not name the argument", errOut)
	}
	if out != "" {
		t.Errorf("stdout %q, want nothing", out)
	}
}

// TestTheBareVerbIsTheGateAndItsExitCodeIsTheAnswer: 0 off-peak, 1 peak - the contract the shell gate
// held, which is what makes `offpeak --quiet && run-the-work` work.
func TestTheBareVerbIsTheGateAndItsExitCodeIsTheAnswer(t *testing.T) {
	t.Setenv(HolidaysEnv, "")
	for _, c := range []struct {
		pin  string
		want int
	}{
		{"2026-10-09 00:30", 0},
		{"2026-10-09 02:00", ExitPeak},
	} {
		t.Setenv(PinEnv, c.pin)
		code, out, _ := runOffpeak(t)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d", c.pin, code, c.want)
		}
		if out == "" {
			t.Errorf("%s: printed nothing, want the human line", c.pin)
		}
	}
}

// TestStatusShowsTheSettingItsSourceAndTheGate: the one line an operator reads to answer "may this
// machine start DeepSeek seats right now, and why".
func TestStatusShowsTheSettingItsSourceAndTheGate(t *testing.T) {
	t.Setenv(PinEnv, "2026-10-09 02:00")

	t.Setenv(ModeEnv, "")
	code, out, errOut := runOffpeak(t, "status")
	if code != 0 {
		t.Fatalf("exit %d (stderr %q)", code, errOut)
	}
	for _, want := range []string{
		"setting  on  (from the default (" + ModeEnv + " is not set))",
		"now      PEAK  02:00 UTC (Fri)  PEAK: 01:00-04:00 UTC (Mon-Fri)",
		"next     04:00 UTC (off-peak resumes)",
		"gate     armed - a start or resume of a seat on a deepseek/ model is refused right now; it opens at 04:00 UTC (off-peak resumes)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status printed\n%s\nwithout %q", out, want)
		}
	}

	t.Setenv(ModeEnv, "0")
	if _, out, _ = runOffpeak(t, "status"); !strings.Contains(out, "setting  off  (from "+ModeEnv+"=0") {
		t.Errorf("status did not show the environment's own setting:\n%s", out)
	}
	if !strings.Contains(out, "gate     off - this client starts or resumes a seat on a deepseek/ model at any hour") {
		t.Errorf("status did not say the gate is off:\n%s", out)
	}

	t.Setenv(ModeEnv, "ture")
	if code, _, errOut := runOffpeak(t, "status"); code != 2 || !strings.Contains(errOut, ModeEnv) {
		t.Errorf("exit %d, stderr %q; a typo in the setting must fail loudly", code, errOut)
	}

	if code, _, _ := runOffpeak(t, "status", "extra"); code != 2 {
		t.Errorf("status with an argument exited %d, want 2", code)
	}
}

// TestSetWritesOneLineAndPrintsNoOtherValue. The env file carries the account's credential, so the verb
// must say what it wrote and NOTHING else it read - the failing shape of this test is a message that
// echoes the file.
func TestSetWritesOneLineAndPrintsNoOtherValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := "# the client's env\nAGENTHUB_TOKEN=super-secret-value\nAGENTHUB_URL=https://example.invalid\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreEnvPath := envPath
	envPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { envPath = restoreEnvPath })

	code, out, errOut := runOffpeak(t, "set", "off")
	if code != 0 {
		t.Fatalf("exit %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "offpeak-only is off") || !strings.Contains(out, ModeEnv+"="+ValueOff) {
		t.Errorf("stdout %q does not report what was written", out)
	}
	for _, secret := range []string{"super-secret-value", "example.invalid"} {
		if strings.Contains(out+errOut, secret) {
			t.Errorf("the verb printed another value from the env file (%q): %q", secret, out+errOut)
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := original + ModeEnv + "=" + ValueOff + "\n"
	if string(content) != want {
		t.Errorf("file =\n%q\nwant\n%q", content, want)
	}

	// The switch flips the line it already wrote rather than adding a second one.
	if code, _, errOut := runOffpeak(t, "set", "on"); code != 0 {
		t.Fatalf("set on exited %d (stderr %q)", code, errOut)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want = original + ModeEnv + "=" + ValueOn + "\n"
	if string(content) != want {
		t.Errorf("after `set on`, file =\n%q\nwant\n%q", content, want)
	}
}

// TestSetReplacesTheFirstAssignmentAndLeavesTheRestAlone: Load reads the FIRST assignment of a key and
// ignores later ones, so the first is the effective line and the one to write. Nothing else is touched.
func TestSetReplacesTheFirstAssignmentAndLeavesTheRestAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := ModeEnv + "=1\nKEEP=me\n" + ModeEnv + "=0\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreEnvPath := envPath
	envPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { envPath = restoreEnvPath })

	if code, _, errOut := runOffpeak(t, "set", "off"); code != 0 {
		t.Fatalf("exit %d (stderr %q)", code, errOut)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := ModeEnv + "=" + ValueOff + "\nKEEP=me\n" + ModeEnv + "=0\n"
	if string(content) != want {
		t.Errorf("file =\n%q\nwant\n%q", content, want)
	}
}

func TestSetRefusesSomethingThatIsNotOnOrOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	restoreEnvPath := envPath
	envPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { envPath = restoreEnvPath })

	for _, args := range [][]string{{"set"}, {"set", "maybe"}, {"set", "on", "off"}} {
		code, _, errOut := runOffpeak(t, args...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2", args, code)
		}
		if !strings.Contains(errOut, "on or off") {
			t.Errorf("%v: stderr %q does not name the accepted values", args, errOut)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a refused set wrote %s anyway", path)
	}
}

// TestSetWarnsWhenTheEnvironmentOutranksTheFile: clientenv.Load gives an existing variable precedence, so
// a shell that exports the setting keeps its own answer - and the operator has to be told, or the file
// they just wrote looks ignored.
func TestSetWarnsWhenTheEnvironmentOutranksTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	restoreEnvPath := envPath
	envPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { envPath = restoreEnvPath })
	t.Setenv(ModeEnv, ValueOn)

	code, _, errOut := runOffpeak(t, "set", "off")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut, ModeEnv) || !strings.Contains(errOut, "wins") {
		t.Errorf("stderr %q does not say the environment outranks the file", errOut)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != ModeEnv+"="+ValueOff+"\n" {
		t.Errorf("file = %q, want the line written anyway", content)
	}
}
