// Package clientskills is the client's OWN skill tree: the skills a seat receives from the client rather
// than from the cloud. Four kinds live under one root, and ONE rule tells the two shapes apart - a
// directory holding SKILL.md IS a skill, a directory without one is a SEAT FOLDER:
//
//	<root>/share/<skill>/SKILL.md                 every room
//	<root>/rooms/<room>/<skill>/SKILL.md          every seat of that room
//	<root>/rooms/<room>/<seat>/<skill>/SKILL.md   that seat only
//	<root>/client/<skill>/SKILL.md                the LLM running the client, NOT a seat's skill set
//
// A seat's set is share + its room's room-level skills + its own seat-level skills, in that order, so a
// more specific directory replaces a shared skill of the same name. Nothing else is ever read: not
// another room, not another seat of this room, and not client/. That isolation is the point of the
// layout, and the test that names it is the reason this package exists.
package clientskills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

const (
	// SkillFile makes a directory a skill. It is also the loader's only marker: without it a directory
	// under rooms/<room>/ is a seat folder.
	SkillFile = "SKILL.md"
	// ShareDir holds the skills EVERY room receives.
	ShareDir = "share"
	// RoomsDir holds the per-room trees: rooms/<room>/<skill> and rooms/<room>/<seat>/<skill>.
	RoomsDir = "rooms"
	// ClientDir holds the skills for the LLM running the client. They are not a seat's skill set and
	// are never part of a resolution.
	ClientDir = "client"
	// RootEnv names the tree when no flag does. It is deliberately NOT AGENTHUB_SKILLS_ROOT: that
	// variable is the library checkout `team publish-skills` reads, a different tree with a different
	// shape, and one name for two trees is how the wrong bytes get delivered.
	RootEnv = "AGENTHUB_CLIENT_SKILLS"
	// SkillsDir is a seat's skills path inside its agent directory - the directory the seat's own agent
	// spec points its resources.skills[].path at ("skills/<id>").
	SkillsDir = "skills"
)

// namePattern is NAME_RE, the rule a room or seat name obeys once it becomes a directory name (the same
// rule clientsync.ValidateName applies). It is repeated rather than imported so this package never
// depends on the verb package that calls it - a room name from an argument must not be able to walk out
// of the tree with "..".
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Tree is the client's skill tree at a known root: a directory holding share/, rooms/ and (for the LLM
// running the client) client/. The zero value is not usable.
type Tree struct {
	Root string
}

// Skill is one skill a seat receives: the name it installs under (its directory) and the directory its
// bytes come from - the most specific of the copies that share that name.
type Skill struct {
	Name string
	Dir  string
}

// Locate finds the tree: --skills-root when the caller passed one, else $AGENTHUB_CLIENT_SKILLS, else
// skills/ beside this executable ("the client directory"), which is where a user who has only the
// binary is told to put the tree. It does not check that a tree is there; Require does that, so a
// caller which only needs the path can ask for it.
func Locate(flagValue string) (Tree, error) {
	if flagValue != "" {
		return Tree{Root: flagValue}, nil
	}
	if fromEnv := os.Getenv(RootEnv); fromEnv != "" {
		return Tree{Root: fromEnv}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return Tree{}, usageError("cannot find the client's skill tree: " + err.Error() +
			"; pass --skills-root DIR or set " + RootEnv)
	}
	return Tree{Root: filepath.Join(filepath.Dir(exe), "skills")}, nil
}

// Require refuses a root that is not the tree. share/ is the check rather than rooms/: every room
// receives share/, while a room with no room-level skills is normal - so a directory holding share/ is
// the tree, and a wrong path is refused by name instead of quietly delivering nothing.
func (t Tree) Require() error {
	if t.Root == "" {
		return usageError("the client's skill tree has no root; pass --skills-root DIR or set " + RootEnv)
	}
	info, err := os.Stat(filepath.Join(t.Root, ShareDir))
	if err != nil || !info.IsDir() {
		return usageError(fmt.Sprintf(
			"the client's skill tree is not at %s (no %s/ directory); pass --skills-root DIR or set %s",
			t.Root, ShareDir, RootEnv))
	}
	return nil
}

// Resolve returns the skills one seat receives, sorted by name: share/, then the room's room-level
// skills, then that seat's own seat-level skills. A missing rooms/<room>/ is NOT an error - a room with
// no room-specific skills still receives share/ - and an entry under rooms/<room>/ that holds SKILL.md
// is a room-level skill while one that does not is a SEAT FOLDER, descended into for that seat alone.
//
// The two malformed shapes are refused by name rather than skipped: a directory in share/ that is not a
// skill, and a directory inside a seat folder that is not a skill. Skipping either would deliver a room
// a set that silently differs from the tree an operator is looking at. A plain FILE at these levels is
// ignored: the rule is about directories, and a README beside the skills is not one.
func (t Tree) Resolve(room, seat string) ([]Skill, error) {
	if err := validateName("room", room); err != nil {
		return nil, err
	}
	if err := validateName("seat", seat); err != nil {
		return nil, err
	}
	if err := t.Require(); err != nil {
		return nil, err
	}

	chosen := map[string]Skill{}

	shareDir := filepath.Join(t.Root, ShareDir)
	shareNames, err := subdirs(shareDir)
	if err != nil {
		return nil, err
	}
	for _, name := range shareNames {
		dir := filepath.Join(shareDir, name)
		if !isSkill(dir) {
			return nil, notASkill(dir)
		}
		chosen[name] = Skill{Name: name, Dir: dir}
	}

	// rooms/<room>/ holds both shapes, and the marker decides which one each entry is. TWO passes, so a
	// more specific source always wins a name clash regardless of directory order: the room-level skills
	// first, then this seat's own folder. An entry that is a seat folder for ANOTHER seat is never read.
	roomDir := filepath.Join(t.Root, RoomsDir, room)
	roomNames, err := subdirsIfPresent(roomDir)
	if err != nil {
		return nil, err
	}
	for _, name := range roomNames {
		dir := filepath.Join(roomDir, name)
		if isSkill(dir) {
			chosen[name] = Skill{Name: name, Dir: dir}
		}
	}
	for _, name := range roomNames {
		if name != seat {
			continue
		}
		dir := filepath.Join(roomDir, name)
		if isSkill(dir) {
			continue // a room-level skill that happens to carry this seat's name
		}
		seatNames, err := subdirs(dir)
		if err != nil {
			return nil, err
		}
		for _, skillName := range seatNames {
			skillDir := filepath.Join(dir, skillName)
			if !isSkill(skillDir) {
				return nil, notASkill(skillDir)
			}
			chosen[skillName] = Skill{Name: skillName, Dir: skillDir}
		}
	}

	names := make([]string, 0, len(chosen))
	for name := range chosen {
		names = append(names, name)
	}
	sort.Strings(names)
	skills := make([]Skill, 0, len(names))
	for _, name := range names {
		skills = append(skills, chosen[name])
	}
	return skills, nil
}

// subdirs returns the names of dir's subdirectories, sorted. A plain file is ignored: the layout's rule
// is about directories, so a README beside the skills is not a fault.
func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, usageError("cannot read the skill tree directory " + dir + ": " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// subdirsIfPresent is subdirs for a directory that may not exist, which is the normal state of
// rooms/<room>/ for a room with no room-level skills.
func subdirsIfPresent(dir string) ([]string, error) {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, usageError("cannot read the skill tree directory " + dir + ": " + err.Error())
	}
	return subdirs(dir)
}

// isSkill is the loader's one rule: a directory holding SKILL.md is a skill.
func isSkill(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, SkillFile))
	return err == nil && info.Mode().IsRegular()
}

func validateName(kind, value string) error {
	if !namePattern.MatchString(value) {
		return usageError(fmt.Sprintf("invalid %s name: %q (expected [a-zA-Z0-9][a-zA-Z0-9_-]*)", kind, value))
	}
	return nil
}

func notASkill(dir string) error {
	return usageError(fmt.Sprintf("%s holds no %s, so it is neither a skill nor a seat folder", dir, SkillFile))
}

func usageError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: clientcmd.ExitUsage}
}
