package clientguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ONLY THE DIRECTOR MOVES MAIN, through git itself. aa2-rs's lead merged four branches into main on
// 2026-10-28 06:16 although the room had a director. The real client is installed as the reference-transaction
// hook, beside a reference-transaction hook the repository already had (aa2-rs keeps its own there), and the
// cases are git commands with and without a seat's environment.
func TestTheReferenceTransactionHookKeepsMainForTheDirector(t *testing.T) {
	r := newSeatRepo(t)
	hooks := filepath.Join(r.checkout, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	ownLog := filepath.Join(t.TempDir(), "own.log")
	own := "#!/bin/sh\ncat >/dev/null\necho \"$1\" >> " + ownLog + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(hooks, HookReferenceTransaction), []byte(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(r.checkout, r.bin, &strings.Builder{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooks, HookReferenceTransaction+BackupSuffix)); err != nil {
		t.Fatalf("the repository's own reference-transaction hook was not kept aside: %v", err)
	}

	room := filepath.Join(t.TempDir(), "rooms", "demo")
	if err := os.MkdirAll(filepath.Join(room, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	seat := func(name string) []string {
		return append(envWithoutSession(), CanonicalSessionEnv+"=agenthub--demo--"+name, LanesRootEnv+"="+filepath.Join(room, "wt"))
	}
	commit := func(dir string, env []string, msg string) (string, error) {
		return r.try(dir, env, "-c", "user.name=room/writer", "-c", "user.email=writer@room.example", "commit", "-q", "--allow-empty", "-m", msg)
	}
	land := func(env []string) (string, error) {
		return r.try(r.checkout, env, "merge", "-q", "--ff-only", "seat-work")
	}

	// The seat's own branch, in its linked worktree, moves freely; with no director the lead lands it on main.
	if out, err := commit(r.worktree, seat("writer"), "seat work 1"); err != nil {
		t.Fatalf("a seat's commit on its own branch was refused: %v\n%s", err, out)
	}
	if out, err := land(seat("lead")); err != nil {
		t.Fatalf("a room without a director refused the lead's merge to main: %v\n%s", err, out)
	}

	if err := os.MkdirAll(filepath.Join(room, "director"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := commit(r.worktree, seat("writer"), "seat work 2"); err != nil {
		t.Fatalf("a seat's commit on its own branch was refused in a director room: %v\n%s", err, out)
	}
	out, err := land(seat("lead"))
	if err == nil || !strings.Contains(out, "only the director updates main") || !strings.Contains(out, "ready.md") {
		t.Fatalf("the lead's merge to main in a director room was not refused: %v\n%s", err, out)
	}
	if out, err := r.try(r.checkout, seat("lead"), "update-ref", "refs/heads/main", "seat-work"); err == nil {
		t.Fatalf("update-ref of main from a seat was not refused:\n%s", out)
	}
	// A shell that is not a seat's - the director's, the founder's - moves main.
	if out, err := land(nil); err != nil {
		t.Fatalf("a non-seat shell was refused on main: %v\n%s", err, out)
	}
	// The hook the repository had still ran, for every state.
	logged, _ := os.ReadFile(ownLog)
	if !strings.Contains(string(logged), "prepared") || !strings.Contains(string(logged), "committed") {
		t.Errorf("the replaced reference-transaction hook did not run:\n%s", logged)
	}

	// The push decision reads the same declaration: the lead of a director room does not push.
	facts := Facts{Hook: HookPrePush, Session: Session{Room: "demo", Seat: "lead"}, HasSession: true, DirectorRoom: room}
	if v := Decide(facts); !v.Refused() || !strings.Contains(strings.Join(v.Refusals, " "), "ready.md") {
		t.Errorf("the lead of a director room may push: %+v", v)
	}
	facts.DirectorRoom = ""
	if v := Decide(facts); v.Refused() {
		t.Errorf("the lead of a room without a director was refused a push: %+v", v)
	}
}
