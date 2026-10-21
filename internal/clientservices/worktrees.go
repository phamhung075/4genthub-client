// The derived seat worktree model (ai_docs/core-architecture/seat-worktrees.md): one worktree per
// seat, DERIVED from the room's seats rather than written down anywhere, plus the one resolution of
// the room directory and of the team definition the seats come from.
//
// The work is done by internal/clientworktree; this file is the data the three readers share - `up`
// creates the list, `doctor` reports it, and the two `worktree` verbs read it - so no reader can
// disagree about a path.
package clientservices

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
)

const (
	// Worktrees is `doctor`'s row about the seat worktrees this client derives and `up` creates: a
	// room's seats must launch in their own worktrees, so where those worktrees are is a fact about
	// the room rather than about a process. Like OrphanHandoffs it has no pid and no heartbeat to age.
	Worktrees = "worktrees"
	// WorktreesWrong is the worktrees row's one failing state: a DERIVED path exists and is not the
	// worktree the ruling derives - on another branch, not a worktree at all, or missing a submodule.
	// A worktree that is not created YET and a FOREIGN entry are deliberately not failures: `up` creates
	// the first before it launches a seat, and `worktree prune` owns the second.
	WorktreesWrong = "worktrees_wrong"
	// Integration is the room's own worktree: the place a room's seats meet through a merge of their
	// tips, and only the lead writes there.
	Integration = "integration"
	// Skills is `doctor`'s row about the skill census: what every skill a room delivers declares, and
	// whether the typed streams have contradicted it. Like Worktrees it is a fact about the room, not
	// about a process, so it has no pid and no heartbeat to age.
	Skills = "skills"
	// SkillsDemote is the skills row's first failing state: a `trusted` skill a declared signal has
	// contradicted inside its window. The reasons name the skill and the rows that did it.
	SkillsDemote = "skills_demote"
	// SkillsBroken is the skills row's second failing state: a record that cannot be read, or one citing
	// an evidence id that does not resolve. A decision citing something that does not exist is not a
	// decision, so it fails the row whether or not anything was demoted.
	SkillsBroken = "skills_broken"
	// Clone is `doctor`'s row about the SHARED checkout: a change sitting in it belongs to no seat, because
	// a seat commits in its own worktree. 4.1's third measured failure was a generated file and eleven
	// landing-branch files sitting changed there with no seat owning them, and nothing read the checkout at
	// all. Like Worktrees it is about a FACT rather than a process, and it is the MACHINE's row: every
	// room's worktrees are registered in one checkout, so it carries no room.
	Clone = "clone"
	// Hooks is `doctor`'s row about the hooks folder GIT actually runs for the checkout, and whether the
	// client's git guard is installed in it. 4.4's fifth risk is the reason it exists: a hook can be
	// switched off, so the guard's presence has to be read rather than assumed. It is the machine's row for
	// the same reason Clone is.
	Hooks = "hooks"
)

// GuardMarker is the line every hook script of the client's git guard carries: ONE constant, so the
// installer and the `hooks` row cannot disagree about what the guard looks like.
const GuardMarker = "4genteam git guard"

// SkillsRootEnv names the OpenRig checkout: the SAME variable `team publish-skills` reads as its
// library root, so one variable points at the repository whose `scripts/team/<room>/team.json` this
// client uses.
const SkillsRootEnv = "OPENRIG_SKILLS_ROOT"

// SeatType is one seat of a room's definition: the key it is addressed by, and the type that decides
// whether it writes.
type SeatType struct {
	Key  string
	Type string
}

// Worktree is one derived worktree: the seat key it belongs to (`integration` for the room's own),
// where it goes, and the branch it is on. An empty Branch is a detached HEAD.
type Worktree struct {
	SeatKey string
	Path    string
	Branch  string
}

// seatWorktree is the ONE path/branch rule, and DerivedWorktrees is its only caller.
func seatWorktree(roomDir, room, key, seatType string) Worktree {
	worktree := Worktree{SeatKey: key, Path: filepath.Join(roomDir, "wt", key), Branch: room + "/" + key}
	if seatType == "reviewer" {
		// Detached: a reviewer never commits, so it has no branch to be on.
		worktree.Branch = ""
	}
	return worktree
}

// DerivedWorktrees is the ONE list, in the order a report and a start step read it: the seats of the
// room in their own order, then the room's integration worktree when the room has a lead. A seat whose
// key is empty is skipped: it has no address, so it has no path.
func DerivedWorktrees(roomDir, room string, seats []SeatType) []Worktree {
	out := make([]Worktree, 0, len(seats)+1)
	hasLead := false
	for _, seat := range seats {
		if seat.Key == "" {
			continue
		}
		if seat.Type == "lead" {
			hasLead = true
		}
		out = append(out, seatWorktree(roomDir, room, seat.Key, seat.Type))
	}
	if hasLead {
		out = append(out, seatWorktree(roomDir, room, Integration, ""))
	}
	return out
}

// RoomDir is the room's OWN directory, and the ONE resolution `up`, the `worktrees` row, both worktree
// verbs and the overlap scan use: `<root>/rooms/<room>`.
//
// IT NEVER RESOLVES THE ROOT. `--state-root DIR` is resolved ONCE, by the verb that took the flag, and the
// derived paths live under THAT store: a function that re-derived the machine's default would derive
// worktrees under one root while the verb started a daemon on another, silently. An empty root is a
// caller's mistake and is refused rather than turned into a relative path. It is a variable so a test
// drives the verbs over its own store, in the repo's style of a package-level seam rather than an
// interface added for testing.
var RoomDir = func(root, room string) (string, error) {
	if root == "" {
		return "", &clientcmd.CodedError{
			Message: "the room's directory needs the state root the caller resolved, so one verb cannot " +
				"derive under one store and read another",
			Code: clientcmd.ExitUsage,
		}
	}
	if room == "" {
		return "", &clientcmd.CodedError{Message: "the room's directory needs a room name", Code: clientcmd.ExitUsage}
	}
	return filepath.Join(root, "rooms", room), nil
}

// LiveSeats reads the room's OWN record - spec.json, the file the runtime wrote from the definition and
// every start reads - and returns its seats in the room's order. It is the source a verb reads once the
// room exists, so a reading of the room is never a reading of a definition that may since have changed.
func LiveSeats(root, room string) ([]SeatType, error) {
	spec, err := clientteam.LoadLiveRoomSpec(root, room)
	if err != nil {
		return nil, err
	}
	out := make([]SeatType, 0, len(spec.Seats))
	for _, seat := range spec.Seats {
		out = append(out, SeatType{Key: seat.SeatKey, Type: seat.SeatType})
	}
	return out, nil
}

// TeamDir resolves the directory that holds the room's `team.json`: the --team value when the caller
// passed one, else $OPENRIG_SKILLS_ROOT - the OpenRig checkout a room's `scripts/team/<rig>/team.json`
// lives in. A room whose definition is neither is refused by name, with both ways to point at it,
// never derived from nothing.
func TeamDir(flagValue, room string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if root := os.Getenv(SkillsRootEnv); root != "" {
		return filepath.Join(root, "scripts", "team", room), nil
	}
	return "", &clientcmd.CodedError{
		Message: fmt.Sprintf(
			"cannot find the room's team file for room %q: pass --team DIR or set %s",
			room, SkillsRootEnv),
		Code: clientcmd.ExitUsage,
	}
}

// TeamSeats reads the room's team file and returns its seats in the file's own order, which is the
// order DerivedWorktrees keeps. A team file that names no seat is refused by name: an empty list
// derives nothing, and a start step that creates nothing looks exactly like one that worked.
func TeamSeats(teamDir string) ([]SeatType, error) {
	spec, err := clientteam.LoadRoomSpec(teamDir)
	if err != nil {
		return nil, err
	}
	if len(spec.Seats) == 0 {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("the team file in %s names no seat, so the room has no worktree to derive", teamDir),
			Code:    clientcmd.ExitUsage,
		}
	}
	out := make([]SeatType, 0, len(spec.Seats))
	for _, seat := range spec.Seats {
		out = append(out, SeatType{Key: seat.SeatKey, Type: seat.SeatType})
	}
	return out, nil
}
