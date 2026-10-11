// THE RULE TABLE, one case per rule 4.4 names, plus the two the ruling adds: a push refuses when EITHER
// signal says seat (fail closed), and an author this client cannot read is not a refusal.
package clientguard

import (
	"strings"
	"testing"
)

func seatFacts(hook string) Facts {
	return Facts{
		Hook:        hook,
		Session:     Session{Room: "room", Seat: "writer"},
		HasSession:  true,
		Worktree:    "/checkout/wt/writer",
		MainRoot:    "/checkout",
		Linked:      true,
		AuthorName:  "room/writer",
		AuthorEmail: "writer@room.example",
		SeatName:    "room/writer",
		SeatEmail:   "writer@room.example",
	}
}

func ownerFacts(hook string) Facts {
	return Facts{Hook: hook, Worktree: "/checkout", MainRoot: "/checkout"}
}

func refusals(t *testing.T, f Facts) []string {
	t.Helper()
	return Decide(f).Refusals
}

// A push from a seat is refused, and the refusal names the route out rather than only the rule (4.4's second
// risk: a refusal without a human present is a stall).
func TestAPushFromASessionIsRefusedAndNamesTheRouteOut(t *testing.T) {
	lines := refusals(t, seatFacts(HookPrePush))
	if len(lines) != 1 {
		t.Fatalf("refusals = %v, want one", lines)
	}
	for _, want := range []string{"room-writer@room", "seats do not push", "the lead", "the owner"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("refusal = %q, want it to name %q", lines[0], want)
		}
	}
}

// Fail closed: a runtime that sets no session name still cannot push, because its working tree is a linked
// worktree and only a checkout without either signal may push.
func TestAPushFromALinkedWorktreeIsRefusedEvenWithNoSession(t *testing.T) {
	f := seatFacts(HookPrePush)
	f.HasSession, f.Session = false, Session{}

	lines := refusals(t, f)
	if len(lines) != 1 || !strings.Contains(lines[0], "linked worktree") {
		t.Fatalf("refusals = %v, want one naming the worktree", lines)
	}
}

// Fail closed in the other direction too: a seat's shell in the checkout itself is still a seat.
func TestAPushFromTheCheckoutUnderASessionIsRefused(t *testing.T) {
	f := ownerFacts(HookPrePush)
	f.HasSession, f.Session = true, Session{Room: "room", Seat: "writer"}

	if lines := refusals(t, f); len(lines) != 1 {
		t.Fatalf("refusals = %v, want one: a session in the checkout is still a seat", lines)
	}
}

// The owner pushes from the checkout, with no session and no linked worktree: nothing is refused.
func TestAPushFromTheCheckoutWithNoSessionIsNotRefused(t *testing.T) {
	verdict := Decide(ownerFacts(HookPrePush))
	if verdict.Refused() {
		t.Fatalf("refusals = %v, want none for the owner's checkout", verdict.Refusals)
	}
}

// A commit made in the shared checkout is refused and names the worktree to commit from.
func TestACommitInTheSharedCheckoutIsRefused(t *testing.T) {
	f := ownerFacts(HookPreCommit)
	f.Worktree = "/checkout/wt/writer"

	lines := refusals(t, f)
	if len(lines) != 1 {
		t.Fatalf("refusals = %v, want one", lines)
	}
	if !strings.Contains(lines[0], "/checkout") || !strings.Contains(lines[0], "/checkout/wt/writer") {
		t.Errorf("refusal = %q, want it to name both the checkout and the worktree", lines[0])
	}
}

// The seat's own commit in its own worktree is not refused, and neither is a foreign author this client
// could not read an identity for: refusing on a value nobody read would refuse the honest commit.
func TestACommitByTheSeatInItsWorktreeIsNotRefused(t *testing.T) {
	if lines := refusals(t, seatFacts(HookPreCommit)); len(lines) != 0 {
		t.Fatalf("refusals = %v, want none", lines)
	}
	f := seatFacts(HookPreCommit)
	f.SeatName, f.SeatEmail = "", ""
	if lines := refusals(t, f); len(lines) != 0 {
		t.Fatalf("refusals = %v, want none when the seat's identity could not be read", lines)
	}
}

// A commit under another identity is refused, and both identities are named so the reader can see the gap.
func TestACommitUnderAForeignAuthorIsRefused(t *testing.T) {
	f := seatFacts(HookPreCommit)
	f.AuthorName, f.AuthorEmail = "someone-else", "someone@example.test"

	lines := refusals(t, f)
	if len(lines) != 1 {
		t.Fatalf("refusals = %v, want one", lines)
	}
	for _, want := range []string{"someone-else", "someone@example.test", "room/writer", "writer@room.example"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("refusal = %q, want it to name %q", lines[0], want)
		}
	}
}

// The secret scan WARNS in both hooks and blocks neither (4.4, 4.1).
func TestASecretInTheChangeWarnsAndBlocksNothing(t *testing.T) {
	// A fact set the OTHER rules leave alone, so the warning is the only finding and this stays the secret
	// scan's case: the commit is the seat's own in its worktree, the push is the owner's from the checkout.
	cases := map[string]Facts{
		HookPreCommit: seatFacts(HookPreCommit),
		HookPrePush:   ownerFacts(HookPrePush),
	}
	for hook, f := range cases {
		f.Change = `token = "ghp_0123456789012345678901234567890123"`

		verdict := Decide(f)
		if verdict.Refused() {
			t.Errorf("%s: refusals = %v, want none: the secret scan warns", hook, verdict.Refusals)
		}
		if len(verdict.Warnings) != 1 || !strings.Contains(verdict.Warnings[0], "WARNS") {
			t.Errorf("%s: warnings = %v, want the warning", hook, verdict.Warnings)
		}
	}
}

// An empty change is no finding: there is nothing to scan, and "nothing to scan" must not read as a warning.
func TestAnEmptyChangeIsNoWarning(t *testing.T) {
	f := seatFacts(HookPreCommit)
	f.Change = "   \n"
	if verdict := Decide(f); len(verdict.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", verdict.Warnings)
	}
}

// The session name is the daemon's own shape, or it is not a seat's.
func TestSessionFromEnvReadsTheDaemonsOwnShape(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  Session
		ok    bool
	}{
		{"a session", "room-writer@room", Session{Room: "room", Seat: "writer"}, true},
		{"a seat whose name holds a dash", "room-go-dev@room", Session{Room: "room", Seat: "go-dev"}, true},
		{"absent", "", Session{}, false},
		{"no room at the end", "room-writer@", Session{}, false},
		{"no separator", "room-writer", Session{}, false},
		{"another room's prefix", "otherroom-writer@room", Session{}, false},
		{"a space", "room-wri ter@room", Session{}, false},
	}
	for _, c := range cases {
		got, ok := SessionFromEnv(func(string) string { return c.value })
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// The line the environment carries is what the verdict quotes back at a reader.
func TestSessionLineIsTheEnvironmentsOwnShape(t *testing.T) {
	if got := (Session{Room: "room", Seat: "go-dev"}).Line(); got != "room-go-dev@room" {
		t.Fatalf("Line() = %q", got)
	}
}
