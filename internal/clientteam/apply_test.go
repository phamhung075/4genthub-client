package clientteam

import (
	"path/filepath"
	"strings"
	"testing"
)

// Mirrors tests/test_team_setup.py::test_apply_names_its_team_directory_and_has_no_default.
func TestApplyNamesItsTeamDirectoryAndHasNoDefault(t *testing.T) {
	fake := installFake(t)

	code, _, errOut := runTeam(t, "apply")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "--team") {
		t.Errorf("stderr did not name --team: %q", errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("a usage error sent %d request(s)", len(fake.recorded()))
	}
}

// The gate's first expectation: with credentials set, apply must still READ the stored seat types,
// because that read is what decides whether the seed runs. The Python does it at cmd_apply ->
// stored_seat_type_slugs, and a port that quietly called nothing would change the plan.
func TestApplyReadsStoredSeatTypesWhenCredentialsAreSet(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, out, errOut := runTeam(t, "apply", "--team", teamDir, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	sawSeatTypes := false
	for _, request := range fake.recorded() {
		if request.method == "GET" && request.path == apiPathSeatTypes {
			sawSeatTypes = true
		}
	}
	if !sawSeatTypes {
		t.Error("apply did not GET /seat-types with credentials set; the seed decision needs it")
	}
	if !strings.Contains(out, "plan: seat types (seed)") {
		t.Errorf("the seed should run while the cloud stores no seat types:\n%s", out)
	}
}

// And when the cloud already stores every seat type the team uses, the seed is dropped.
func TestApplySkipsTheSeedWhenEverySeatTypeIsStored(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", apiPathSeatTypes, 200, `{"seat_types": [{"slug": "developer"}]}`)
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, out, errOut := runTeam(t, "apply", "--team", teamDir, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if strings.Contains(out, "seat types (seed)") {
		t.Errorf("the seed ran though every seat type is stored:\n%s", out)
	}
}

// Without credentials the stored set is unknown, so the seed stays in the plan.
func TestApplyWithoutCredentialsStillPlansTheSeed(t *testing.T) {
	installFake(t)
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, out, errOut := runTeam(t, "apply", "--team", teamDir, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"plan: seat types (seed) (POST /api/v2/seat-types/seed)",
		"plan: module demo-module@1.0.0 (PUT /api/v2/modules/demo-module/versions/1.0.0)",
		"plan: room demo (POST /api/v2/rooms)",
		"plan: seat lead (POST /api/v2/rooms/demo/seats)",
		"plan: link lead delegates_to lead (PUT /api/v2/rooms/demo/seats/lead/links)",
		"plan: overlay company (PUT /api/v2/overlay)",
		"plan: overlay seat lead (PUT /api/v2/rooms/demo/seats/lead/overlay)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan missing %q\n%s", want, out)
		}
	}
}

// A missing credential is one message naming the half that is missing.
func TestApplyRefusesWithOneMessageWhenTheTokenIsMissing(t *testing.T) {
	installFake(t)
	t.Setenv("AGENTHUB_URL", "https://cloud.example.test")
	t.Setenv("AGENTHUB_TOKEN", "")
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, _, errOut := runTeam(t, "apply", "--team", teamDir)
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if strings.Count(strings.TrimSpace(errOut), "\n") != 0 {
		t.Errorf("a refusal must be ONE line, got %q", errOut)
	}
	if !strings.Contains(errOut, "AGENTHUB_TOKEN is not set") {
		t.Errorf("stderr did not name the missing half: %q", errOut)
	}
}

// An unreadable team definition is a usage error naming the directory.
func TestApplyRefusesAnUnreadableTeamDirectory(t *testing.T) {
	installFake(t)
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	missing := filepath.Join(t.TempDir(), "absent")

	code, _, errOut := runTeam(t, "apply", "--team", missing)
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, missing) {
		t.Errorf("stderr did not name the directory: %q", errOut)
	}
}

// Every verb answers --help, and team itself does.
func TestTeamAndItsVerbsAnswerHelp(t *testing.T) {
	installFake(t)
	for _, args := range [][]string{
		{"--help"},
		{"apply", "--help"},
		{"import-project", "--help"},
		{"publish-skills", "--help"},
		{"drift-check", "--help"},
	} {
		code, out, errOut := runTeam(t, args...)
		if code != 0 {
			t.Errorf("%v exited %d, stderr %q", args, code, errOut)
		}
		if !strings.Contains(out, "usage: 4genteam team") {
			t.Errorf("%v printed no usage:\n%s", args, out)
		}
	}
}

// An unknown verb names the ones that exist.
func TestTeamRejectsAnUnknownVerb(t *testing.T) {
	installFake(t)
	code, _, errOut := runTeam(t, "frobnicate")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, teamVerbs) {
		t.Errorf("stderr did not list the verbs: %q", errOut)
	}
}

// A seat the cloud already holds is not changed by apply, and the line says so and names the verb that does:
// on 2026-10-28 aa2-rs's lead kept its model after `team apply` printed "seat lead: exists", until the owner
// edited spec.json and ran sync switch by hand.
func TestApplySaysAnExistingSeatWasNotUpdatedAndNamesSyncSwitch(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", apiPathSeatTypes, 200, `{"seat_types": [{"slug": "developer"}]}`)
	fake.override("POST", apiPrefix+"/rooms/demo/seats", 409, `{"detail": "seat already exists"}`)
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, out, errOut := runTeam(t, "apply", "--team", teamDir)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q\n%s", code, errOut, out)
	}
	want := "seat lead: exists - NOT updated"
	if !strings.Contains(out, want) || !strings.Contains(out, "4genteam sync switch demo lead --runtime omp --model <model> --apply set-model") {
		t.Errorf("the exists line does not say the seat was left unchanged and name sync switch:\n%s", out)
	}
}
