package clientteam

import (
	"net/url"
	"strings"
	"testing"
)

// ensure creates nothing when a project of that name exists, and says which one it found.
func TestProjectEnsureFindsAnExistingProjectByName(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", projectsPath, 200, `{"success": true, "projects": [{"id": "p-1", "name": "other"}, {"id": "p-2", "name": "aa2-rs"}]}`)

	code, out, errOut := runTeam(t, "project", "ensure", "--name", "aa2-rs")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "exists: aa2-rs id=p-2") {
		t.Errorf("stdout %q does not name the existing project", out)
	}
	for _, request := range fake.recorded() {
		if request.method == "POST" {
			t.Errorf("ensure posted over an existing project: %+v", request)
		}
	}
}

// ensure posts the form the route reads and prints the id the cloud answered.
func TestProjectEnsureCreatesAMissingProjectWithTheRoutesForm(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", projectsPath, 200, `{"success": true, "projects": []}`)
	fake.override("POST", projectsPath, 200, `{"success": true, "project": {"id": "p-9", "name": "aa2-rs"}}`)

	code, out, errOut := runTeam(t, "project", "ensure", "--name", "aa2-rs", "--description", "room aa2-rs")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "created: aa2-rs id=p-9") {
		t.Errorf("stdout %q does not name the created project", out)
	}
	var posted []recordedRequest
	for _, request := range fake.recorded() {
		if request.method == "POST" {
			posted = append(posted, request)
		}
	}
	if len(posted) != 1 {
		t.Fatalf("want one POST, got %+v", posted)
	}
	form, err := url.ParseQuery(posted[0].body)
	if err != nil || form.Get("name") != "aa2-rs" || form.Get("description") != "room aa2-rs" {
		t.Errorf("the POST body is not the route's form: %q", posted[0].body)
	}
}

// --dry-run reads the list and creates nothing.
func TestProjectEnsureDryRunCreatesNothing(t *testing.T) {
	fake := installFake(t)
	withCredentials(t, fake)
	fake.override("GET", projectsPath, 200, `{"success": true, "projects": []}`)

	code, out, _ := runTeam(t, "project", "ensure", "--name", "aa2-rs", "--dry-run")
	if code != 0 || !strings.Contains(out, "would create: aa2-rs") {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	for _, request := range fake.recorded() {
		if request.method == "POST" {
			t.Errorf("a dry run posted: %+v", request)
		}
	}
}

func TestProjectEnsureWithoutANameIsAUsageError(t *testing.T) {
	installFake(t)
	code, _, errOut := runTeam(t, "project", "ensure")
	if code != ExitUsage || !strings.Contains(errOut, "--name NAME is required") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}
