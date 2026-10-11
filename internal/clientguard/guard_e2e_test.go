// THE ACCEPTANCE TEST: push impossible from a seat, whatever its runtime, and the guard stays out of the
// owner's way. It runs real git against a real bare remote, with the guard installed by the installer and
// run by the real client binary, because the thing being proven is a git hook refusing a git command - a
// stub would prove that a stub was called.
//
// The seat signal is the LINKED WORKTREE, not the session variable: every runtime's seat works in one (item
// 1's own finding), so the refusal cannot be undone by a runtime that sets no environment.
package clientguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seatRepo is a checkout, a linked worktree of it, and a bare remote both can push to.
type seatRepo struct {
	t        *testing.T
	remote   string
	checkout string
	worktree string
	bin      string
}

func newSeatRepo(t *testing.T) *seatRepo {
	t.Helper()
	base := t.TempDir()
	r := &seatRepo{t: t, remote: filepath.Join(base, "remote.git"),
		checkout: filepath.Join(base, "checkout"), worktree: filepath.Join(base, "wt", "writer")}
	r.bin = buildClient(t, base)

	r.git(base, "init", "--bare", "-b", "main", r.remote)
	if err := os.MkdirAll(r.checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(r.checkout, "init", "-b", "main")
	r.git(r.checkout, "config", "user.name", "room/writer")
	r.git(r.checkout, "config", "user.email", "writer@room.example")
	if err := os.WriteFile(filepath.Join(r.checkout, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.checkout, "add", "file.txt")
	r.git(r.checkout, "commit", "-m", "one")
	r.git(r.checkout, "remote", "add", "origin", r.remote)
	r.git(r.checkout, "push", "origin", "main")
	r.git(r.checkout, "worktree", "add", "-b", "seat-work", r.worktree)
	return r
}

// buildClient builds the real client once, so the installed hook runs the client the way production does.
func buildClient(t *testing.T, into string) string {
	t.Helper()
	bin := filepath.Join(into, "4genteam")
	command := exec.Command("go", "build", "-o", bin, "./cmd/4genteam")
	command.Dir = "../.."
	command.Env = os.Environ()
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cannot build the client: %v\n%s", err, out)
	}
	return bin
}

func (r *seatRepo) git(dir string, args ...string) string {
	r.t.Helper()
	out, err := r.try(dir, nil, args...)
	if err != nil {
		r.t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// try runs one git command and answers its combined output either way, so a test can assert on a REFUSAL.
// env is the environment git gets: nil means the test's own, with the daemon's session variable removed, so
// a case that wants a seat says so by setting it and nothing depends on where the suite is run.
func (r *seatRepo) try(dir string, env []string, args ...string) (string, error) {
	r.t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if env == nil {
		env = envWithoutSession()
	}
	command.Env = env
	out, err := command.CombinedOutput()
	return string(out), err
}

func envWithoutSession() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, SessionEnv+"=") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func withSession(env []string, value string) []string {
	return append(env, SessionEnv+"="+value)
}

// A seat's push is refused by the hook git runs, and the message names the guard and the route out.
func TestASeatsPushIsRefusedWithNoSessionSetAtAll(t *testing.T) {
	r := newSeatRepo(t)
	if _, err := Install(r.checkout, r.bin, &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.worktree, "file.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.worktree, "commit", "-am", "two")

	out, err := r.try(r.worktree, nil, "push", "origin", "seat-work")
	if err == nil {
		t.Fatalf("the seat's push succeeded; the guard did not refuse it:\n%s", out)
	}
	if !strings.Contains(out, "git guard") || !strings.Contains(out, "seats do not push") {
		t.Errorf("push refused for the wrong reason:\n%s", out)
	}
	// Nothing reached the remote.
	if branches := r.git(r.remote, "branch", "--list"); strings.Contains(branches, "seat-work") {
		t.Errorf("the refused push still landed: %s", branches)
	}
}

// The owner's push from the checkout is not refused: the guard is a seat's guard.
func TestTheOwnersPushFromTheCheckoutGoesThrough(t *testing.T) {
	r := newSeatRepo(t)
	if _, err := Install(r.checkout, r.bin, &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.checkout, "file.txt"), []byte("owner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.checkout, "commit", "-am", "owner")

	if out, err := r.try(r.checkout, nil, "push", "origin", "main"); err != nil {
		t.Fatalf("the owner's push was refused: %v\n%s", err, out)
	}
}

// A SEAT's commit in the shared checkout is refused by git's own pre-commit hook; the same file committed in
// the seat's worktree is not. The owner's own commit in the checkout is allowed (the owner-push case above
// commits there with no session).
func TestAGitCommitInTheSharedCheckoutIsRefused(t *testing.T) {
	r := newSeatRepo(t)
	if _, err := Install(r.checkout, r.bin, &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.checkout, "new.txt"), []byte("here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.checkout, "add", "new.txt")

	seatEnv := withSession(envWithoutSession(), "room-writer@room")
	out, err := r.try(r.checkout, seatEnv, "commit", "-m", "in the shared checkout")
	if err == nil {
		t.Fatalf("a seat's commit in the shared checkout succeeded:\n%s", out)
	}
	if !strings.Contains(out, "shared checkout") {
		t.Errorf("commit refused for the wrong reason:\n%s", out)
	}

	// The same content in the seat's own worktree commits fine.
	seatPath := filepath.Join(r.worktree, "new.txt")
	if err := os.WriteFile(seatPath, []byte("here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.worktree, "add", "new.txt")
	if out, err := r.try(r.worktree, nil, "commit", "-m", "in the seat's worktree"); err != nil {
		t.Fatalf("the seat's own commit was refused: %v\n%s", err, out)
	}
}

// A commit under an identity override is refused, and the identity the repository resolves for the seat is
// what it compares against.
func TestACommitUnderAnOverriddenAuthorIsRefused(t *testing.T) {
	r := newSeatRepo(t)
	if _, err := Install(r.checkout, r.bin, &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.worktree, "file.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.worktree, "add", "file.txt")

	env := append(envWithoutSession(), "GIT_AUTHOR_NAME=someone-else", "GIT_AUTHOR_EMAIL=someone@example.test")
	out, err := r.try(r.worktree, env, "commit", "-m", "under another name")
	if err == nil {
		t.Fatalf("a commit under another author succeeded:\n%s", out)
	}
	if !strings.Contains(out, "someone-else") || !strings.Contains(out, "room/writer") {
		t.Errorf("commit refused for the wrong reason:\n%s", out)
	}
}

// The secret scan WARNS on a commit and blocks nothing: the commit lands and the warning is in what the hook
// printed (4.4, 4.1).
func TestASecretInACommitWarnsWithoutBlockingIt(t *testing.T) {
	r := newSeatRepo(t)
	if _, err := Install(r.checkout, r.bin, &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	secret := "token = \"ghp_0123456789012345678901234567890123\"\n"
	if err := os.WriteFile(filepath.Join(r.worktree, "creds.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.worktree, "add", "creds.txt")

	out, err := r.try(r.worktree, nil, "commit", "-m", "with a secret")
	if err != nil {
		t.Fatalf("the warning blocked the commit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARNS") {
		t.Errorf("the commit printed no warning:\n%s", out)
	}
}

// A push fails CLOSED: with the client the hook would run gone, the push is refused rather than let through.
func TestAPushIsRefusedWhenTheClientTheHookRunsIsGone(t *testing.T) {
	r := newSeatRepo(t)
	if _, err := Install(r.checkout, r.bin, &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.Remove(r.bin); err != nil {
		t.Fatal(err)
	}
	out, err := r.try(r.worktree, nil, "push", "origin", "main")
	if err == nil {
		t.Fatalf("the push went through with no guard to check it:\n%s", out)
	}
	if !strings.Contains(out, "cannot check this push") {
		t.Errorf("push refused for the wrong reason:\n%s", out)
	}
}

// THE PRECONDITION FOR ANY LIVE INSTALL: a checkout that already runs its own hooks keeps them. This room's
// shared repo carries a pre-commit.com hook, so the question is not academic - the guard must chain to it,
// both must run, a refusal must still come before their hook, and uninstalling must put the originals back
// exactly as they were. Nothing here touches a live checkout: the fixture is a temporary repository.
func TestTheGuardChainsToTheHooksAlreadyThereAndUninstallPutsThemBack(t *testing.T) {
	r := newSeatRepo(t)
	hooks := filepath.Join(r.checkout, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "own-hooks.log")
	own := map[string]string{}
	for _, name := range []string{HookPreCommit, HookPrePush} {
		body := "#!/bin/sh\necho " + name + " ran >> " + log + "\nexit 0\n"
		own[name] = body
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	installed, err := Install(r.checkout, r.bin, &strings.Builder{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, hook := range installed {
		if hook.Replaced == "" {
			t.Errorf("%s: the hook already there was not kept", hook.Name)
		}
	}

	// A commit the guard allows runs BOTH: the guard's decision and then the hook that was already there.
	if err := os.WriteFile(filepath.Join(r.worktree, "file.txt"), []byte("chained\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.worktree, "add", "file.txt")
	if out, err := r.try(r.worktree, nil, "commit", "-m", "chained"); err != nil {
		t.Fatalf("the seat's own commit was refused: %v\n%s", err, out)
	}
	if lines := readLog(t, log); !strings.Contains(lines, HookPreCommit+" ran") {
		t.Errorf("the hook that was already there did not run: %q", lines)
	}

	// A commit the guard REFUSES never reaches that hook: the guard's line is first in pre-commit, so its
	// refusal cannot be bypassed by a later hook, and the repo's own hook is not run for it.
	before := readLog(t, log)
	if err := os.WriteFile(filepath.Join(r.checkout, "new.txt"), []byte("here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.checkout, "add", "new.txt")
	seatEnv := withSession(envWithoutSession(), "room-writer@room")
	if out, err := r.try(r.checkout, seatEnv, "commit", "-m", "in the shared checkout"); err == nil {
		t.Fatalf("a seat's commit in the shared checkout succeeded:\n%s", out)
	}
	if after := readLog(t, log); after != before {
		t.Errorf("the repo's own hook ran for a commit the guard refused:\nbefore %q\nafter  %q", before, after)
	}

	// Uninstall puts the originals back, byte for byte, and leaves nothing of the guard's behind.
	removed, err := Uninstall(r.checkout, &strings.Builder{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d hook(s), want both", len(removed))
	}
	for _, hook := range removed {
		if hook.Restored == "" {
			t.Errorf("%s: the hook the guard replaced was not put back", hook.Name)
		}
		body, err := os.ReadFile(filepath.Join(hooks, hook.Name))
		if err != nil {
			t.Fatalf("reading %s back: %v", hook.Name, err)
		}
		if string(body) != own[hook.Name] {
			t.Errorf("%s after uninstall:\n%q\nwant the original\n%q", hook.Name, body, own[hook.Name])
		}
		if _, err := os.Stat(filepath.Join(hooks, hook.Name+BackupSuffix)); err == nil {
			t.Errorf("%s: the backup is still there after uninstall", hook.Name)
		}
	}

	// A hook that is not the guard's is left alone, and uninstalling again says so without touching it.
	if err := os.WriteFile(filepath.Join(hooks, HookPreCommit), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	again, err := Uninstall(r.checkout, &strings.Builder{})
	if err != nil {
		t.Fatalf("Uninstall (second): %v", err)
	}
	if len(again) != 0 {
		t.Errorf("uninstall removed %d hook(s) it did not write", len(again))
	}
	if body, err := os.ReadFile(filepath.Join(hooks, HookPreCommit)); err != nil || string(body) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("a hook the guard did not write was changed: %q (%v)", body, err)
	}
}

// readLog answers what a hook of the repo's own recorded, or "" when it never ran.
func readLog(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(body)
}
