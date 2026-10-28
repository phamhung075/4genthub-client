// The installer's own behaviour: what it writes, what it refuses to overwrite, and that the guard's marker is
// in every hook it writes (the `hooks` row of `doctor` is what turns a missing marker into a line a reader
// can see).
package clientguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo is a real checkout with one commit, so the installer writes into a hooks folder git itself names.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	r := &gitRepo{t: t, dir: t.TempDir()}
	r.git("init", "-b", "main")
	r.git("config", "user.name", "room/writer")
	r.git("config", "user.email", "writer@room.example")
	if err := os.WriteFile(filepath.Join(r.dir, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git("add", "file.txt")
	r.git("commit", "-m", "one")
	return r
}

// git runs one git command in the fixture, with the test's own environment.
func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	return r.gitIn(r.dir, args...)
}

// gitIn runs one git command in a directory of the fixture (its worktree, or the fixture itself).
func (r *gitRepo) gitIn(dir string, args ...string) string {
	r.t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = os.Environ()
	out, err := command.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func (r *gitRepo) hooks() string {
	r.t.Helper()
	folder, err := HooksFolder(r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	return folder
}

func (r *gitRepo) read(name string) string {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(r.hooks(), name))
	if err != nil {
		r.t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(body)
}

// The guard lands in the hooks folder git resolves, its files carry the marker and the guard's own path, and
// no placeholder is left behind.
func TestInstallWritesBothHooksIntoGitsOwnFolder(t *testing.T) {
	r := newGitRepo(t)
	var out strings.Builder

	installed, err := Install(r.dir, "/opt/4genteam", &out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(installed) != len(guardHooks) {
		t.Fatalf("installed = %+v, want every guard hook %v", installed, guardHooks)
	}
	for _, name := range guardHooks {
		body := r.read(name)
		if !strings.Contains(body, GuardMarker) {
			t.Errorf("%s does not carry the guard's marker, so `doctor` would call the guard absent:\n%s", name, body)
		}
		if !strings.Contains(body, "/opt/4genteam") {
			t.Errorf("%s does not name the client it must run:\n%s", name, body)
		}
		for _, placeholder := range []string{guardBinPlaceholder, prevPlaceholder} {
			if strings.Contains(body, placeholder) {
				t.Errorf("%s still holds the placeholder %s", name, placeholder)
			}
		}
		info, err := os.Stat(filepath.Join(r.hooks(), name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable: %v", name, info.Mode())
		}
	}
}

// A hook the guard did NOT write is moved aside and still runs: this room's checkout already has a pre-commit
// from the pre-commit framework, and losing it would break the owner's own machinery.
func TestInstallMovesAForeignHookAsideAndStillRunsIt(t *testing.T) {
	r := newGitRepo(t)
	foreign := "#!/bin/sh\n# the framework's own hook\nexit 0\n"
	path := filepath.Join(r.hooks(), HookPreCommit)
	if err := os.WriteFile(path, []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(r.dir, "/opt/4genteam", &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	backup := path + BackupSuffix
	body, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("the hook the guard replaced was not kept: %v", err)
	}
	if string(body) != foreign {
		t.Errorf("backup holds %q, want the hook it replaced verbatim", body)
	}
	if !strings.Contains(r.read(HookPreCommit), backup) {
		t.Errorf("the guard's hook does not run the hook it replaced:\n%s", r.read(HookPreCommit))
	}
}

// Installing twice is safe: the guard replaces its OWN hook, keeps ONE backup, and does not fail because a
// backup is already there.
func TestInstallTwiceKeepsOneBackup(t *testing.T) {
	r := newGitRepo(t)
	path := filepath.Join(r.hooks(), HookPreCommit)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(r.dir, "/opt/4genteam", &strings.Builder{}); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if _, err := Install(r.dir, "/opt/4genteam", &strings.Builder{}); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if !strings.Contains(r.read(HookPreCommit), path+BackupSuffix) {
		t.Errorf("the second install dropped the hook it should still run:\n%s", r.read(HookPreCommit))
	}
	entries, err := os.ReadDir(r.hooks())
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), BackupSuffix) {
			backups++
		}
	}
	if backups != 1 {
		t.Errorf("backups = %d, want exactly one", backups)
	}
}

// Two tools claiming one backup name would lose one of them: the installer refuses instead.
func TestInstallRefusesWhenABackupAlreadyExists(t *testing.T) {
	r := newGitRepo(t)
	path := filepath.Join(r.hooks(), HookPreCommit)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+BackupSuffix, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Install(r.dir, "/opt/4genteam", &strings.Builder{})
	if err == nil {
		t.Fatal("Install overwrote a backup: want a refusal")
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("err = %v, want it to name the backup", err)
	}
}
