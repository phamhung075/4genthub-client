package clientworktree

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// The fixtures here are REAL repositories and REAL worktrees, built in t.TempDir() the way
// internal/clientteam/publish_test.go builds its library: the ruling is about what git does, so a case
// that stubbed git could not tell a worktree from a directory that happens to look like one.

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("SKIPPED, NOT PASSED: no git on PATH, so the fixture repository cannot be built and this " +
			"case did NOT run")
	}
}

// gitIn runs git with the fixture identity and the machine's own git configuration bypassed, exactly as
// publish_test.go's helper does, so no case can depend on the machine.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return out
}

// gitTry is gitIn for the calls a case EXPECTS to fail - the fetch a never-pushed pin cannot serve, for
// instance - so a failure is a result rather than a test failure.
func gitTry(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.test",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	raw, err := command.CombinedOutput()
	return strings.TrimSpace(string(raw)), err
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// stubLinkProviderKey records the directories the provider key would be linked into, and writes
// nothing: LinkProviderKey reads this machine's key file, which a test cannot supply.
func stubLinkProviderKey(t *testing.T) *[]string {
	t.Helper()
	var linked []string
	original := linkProviderKey
	linkProviderKey = func(dir string) error {
		linked = append(linked, dir)
		return nil
	}
	t.Cleanup(func() { linkProviderKey = original })
	return &linked
}

// seatFixture is a main checkout with the client submodule, a room whose team file names the given
// seats, and the two seams the verbs need.
type seatFixture struct {
	t     *testing.T
	main  string // the checkout whose worktrees these are
	store string // the seat store the room lives in
	room  string // <store>/<room>
	name  string // the room's name, the directory's base
	seats []clientservices.SeatType
	pins  map[string]string // submodule name → the gitlink the parent records
}

// newSeatFixture builds the whole fixture. The submodule SOURCE IS DELETED after the clone, so the pin
// the parent records exists nowhere else on the machine: that is the ruling's "a pin that was never
// pushed", made real rather than asserted, and it is why `submodule update --init` cannot work here.
func newSeatFixture(t *testing.T, seats []clientservices.SeatType) *seatFixture {
	t.Helper()
	requireGit(t)
	fixture := &seatFixture{t: t, name: "fixture-room", seats: seats, pins: map[string]string{}}

	sources := filepath.Join(t.TempDir(), "sources")
	source := filepath.Join(sources, clientSubmodule)
	mkdirAll(t, source)
	gitIn(t, source, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(source, "PAYLOAD.md"), "the "+clientSubmodule+" payload\n")
	gitIn(t, source, "add", "PAYLOAD.md")
	gitIn(t, source, "commit", "-q", "-m", "the "+clientSubmodule+" payload")

	fixture.main = filepath.Join(t.TempDir(), "main")
	mkdirAll(t, fixture.main)
	gitIn(t, fixture.main, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(fixture.main, "README.md"), "the main checkout\n")
	gitIn(t, fixture.main, "add", "README.md")
	gitIn(t, fixture.main, "commit", "-q", "-m", "the main checkout")
	// protocol.file.allow is needed because the URL is a local path: this room never pushes, and a
	// fixture cannot either.
	gitIn(t, fixture.main, "-c", "protocol.file.allow=always",
		"submodule", "add", "-q", source, clientSubmodule)
	gitIn(t, fixture.main, "commit", "-q", "-m", "the client submodule")
	fixture.pins[clientSubmodule] = gitIn(t, fixture.main, "rev-parse", "HEAD:"+clientSubmodule)
	if err := os.RemoveAll(sources); err != nil {
		t.Fatalf("remove the submodule sources: %v", err)
	}

	skillsRoot := t.TempDir()
	teamDir := filepath.Join(skillsRoot, "scripts", "team", fixture.name)
	mkdirAll(t, teamDir)
	writeFile(t, filepath.Join(teamDir, "team.json"), teamJSON(t, fixture.name, seats))
	t.Setenv(clientservices.SkillsRootEnv, skillsRoot)

	fixture.store = t.TempDir()
	fixture.room = filepath.Join(fixture.store, "rooms", fixture.name)
	// The room's OWN record, which the verbs read: spec.json is what a room that exists is, and the team
	// file above is only what creates it.
	writeFile(t, filepath.Join(fixture.room, "spec.json"), roomSpecJSON(t, fixture.name, seats))
	return fixture
}

// roomSpecJSON writes the record the runtime stores for a started room: the seats are the room's.
func roomSpecJSON(t *testing.T, room string, seats []clientservices.SeatType) string {
	t.Helper()
	seatList := make([]map[string]string, 0, len(seats))
	for _, seat := range seats {
		seatList = append(seatList, map[string]string{"seat_key": seat.Key, "seat_type": seat.Type})
	}
	raw, err := json.MarshalIndent(map[string]any{"room": room, "seats": seatList}, "", "  ")
	if err != nil {
		t.Fatalf("marshal the room record: %v", err)
	}
	return string(raw) + "\n"
}

// teamJSON writes the room's team file in the shape clientpolicy.ReadTeam reads.
func teamJSON(t *testing.T, room string, seats []clientservices.SeatType) string {
	t.Helper()
	seatList := make([]map[string]string, 0, len(seats))
	for _, seat := range seats {
		seatList = append(seatList, map[string]string{"seat_key": seat.Key, "seat_type": seat.Type})
	}
	team := map[string]any{
		"room":  map[string]string{"slug": room},
		"seats": seatList,
	}
	// A room of several seats needs its links: clientteam reads the file through LoadRoomSpec, whose rule
	// is that no seat of a many-seat room can message another without one.
	if len(seats) > 1 {
		team["links"] = []map[string]string{
			{"from": seats[0].Key, "kind": "delegates_to", "to": seats[1].Key},
		}
	}
	raw, err := json.MarshalIndent(team, "", "  ")
	if err != nil {
		t.Fatalf("marshal the team file: %v", err)
	}
	return string(raw) + "\n"
}

func (f *seatFixture) derived() []clientservices.Worktree {
	return clientservices.DerivedWorktrees(f.room, f.name, f.seats)
}

// listRaw is the fixture's own reading of `git worktree list`, kept as git's own text so a case can
// compare two runs byte for byte.
func (f *seatFixture) listRaw() string {
	return gitIn(f.t, f.main, "worktree", "list", "--porcelain")
}

// ensure runs the start step over the fixture's own room.
func (f *seatFixture) ensure(out *strings.Builder) error {
	return Ensure(f.main, f.room, f.name, f.seats, out)
}

// entry answers the registered entry for a path, from the package's own reader.
func (f *seatFixture) entry(t *testing.T, path string) (Entry, bool) {
	t.Helper()
	entries, err := Read(f.main)
	if err != nil {
		t.Fatalf("read the worktrees: %v", err)
	}
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == filepath.Clean(path) {
			return entry, true
		}
	}
	return Entry{}, false
}

// removeTmpWorktree undoes a case's own /tmp worktree: it is registered in a fixture repository inside
// t.TempDir(), so the case must take it out of the listing AND off the disk.
func removeTmpWorktree(t *testing.T, main, path string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = gitTry(t, main, "worktree", "unlock", path)
		_, _ = gitTry(t, main, "worktree", "remove", "--force", path)
		_ = os.RemoveAll(path)
		_, _ = gitTry(t, main, "worktree", "prune")
	})
}

// fixedTime is one timestamp reading, for the "changes nothing" case.
func fixedTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}
