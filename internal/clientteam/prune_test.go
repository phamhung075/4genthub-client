package clientteam

import (
	"strings"
	"testing"
)

const demoSeatsPath = apiPrefix + "/rooms/demo/seats"

func deletesOf(fake *fakeCloud) []string {
	paths := []string{}
	for _, request := range fake.recorded() {
		if request.method == "DELETE" {
			paths = append(paths, request.path)
		}
	}
	return paths
}

func TestPruneDeletesTheRoomSeatsTheTeamDoesNotDefine(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", demoSeatsPath, 200, `{"success": true, "seats": [{"seat_key": "lead"}, {"seat_key": "writer"}, {"seat_key": "web-dev"}]}`)
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, out, errOut := runTeam(t, "prune", "--team", teamDir)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	got := strings.Join(deletesOf(fake), ",")
	want := demoSeatsPath + "/writer," + demoSeatsPath + "/web-dev"
	if got != want {
		t.Errorf("deleted %q, want %q", got, want)
	}
	if !strings.Contains(out, "seat writer: deleted") || strings.Contains(out, "seat lead") {
		t.Errorf("report names the wrong seats:\n%s", out)
	}
}

func TestPruneDryRunListsAndDeletesNothing(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", demoSeatsPath, 200, `{"success": true, "seats": [{"seat_key": "lead"}, {"seat_key": "writer"}]}`)
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, out, errOut := runTeam(t, "prune", "--team", teamDir, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(deletesOf(fake)) != 0 {
		t.Errorf("a dry run deleted %v", deletesOf(fake))
	}
	if !strings.Contains(out, "plan: delete seat writer") {
		t.Errorf("the plan does not name writer:\n%s", out)
	}
}

func TestPruneRefusesAnUnreadableSeatListing(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", demoSeatsPath, 500, `{"detail": "boom"}`)
	teamDir := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	code, _, _ := runTeam(t, "prune", "--team", teamDir)
	if code == 0 {
		t.Error("prune succeeded although the seat listing failed")
	}
	if len(deletesOf(fake)) != 0 {
		t.Errorf("deleted %v without a listing", deletesOf(fake))
	}
}

func TestPruneNamesItsTeamDirectory(t *testing.T) {
	fake := installFake(t)
	code, _, errOut := runTeam(t, "prune")
	if code != ExitUsage || !strings.Contains(errOut, "--team") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if len(fake.recorded()) != 0 {
		t.Errorf("a usage error sent %d request(s)", len(fake.recorded()))
	}
}
