// 7.5's skills check: the ONE check `up` runs after every seat launch and `doctor` reports on, with the
// five codes the ruling's table names (ai_docs/core-architecture/seat-skills.md section 6).
//
// WHAT CHANGED SINCE THE RULING WAS WRITTEN, and why this file is not the ruling's `sync rig` check. The
// ruling's dated note (2026-10-17) retires the client's delivery: at every seat start the RUNTIME resolves
// the client's tree (`share`, then `rooms/<room>`, then `rooms/<room>/<seat>`, never `client/`) and writes
// it under the seat's own directory, replacing what was there. The client's delivery code and its
// `agent.yaml`/`guide` rendering are deleted with their writers. So this check reads what the runtime
// INSTALLED; it resolves the tree only to know what SHOULD be installed, and it delivers nothing.
//
// THE CODES ARE THE RULING'S, VERBATIM. F1 cwd, F2 foreign, F3 missing, F4 path and F5 stale are decided
// from evidence and never from a guess; a NOTE is this client's own token for the one situation the ruling
// has no code for - an input the CALLER could not obtain - and it NAMES that input instead of showing a
// fabricated F1/F2/F3. A note prints as `seat <seat>: note <detail>`, so a parser reading the ruling's line
// shape cannot mistake it for one of the five codes.
//
// F6 (guide version) and F7 (forbid rules) are NOT here. Their inputs were retired with the delivery:
// no rendered guide and no pinned agent spec exist on this client line (the seat role is a skill), and the
// client-side policy table was deleted so the hub's render is the only source (`4genteam policy render
// --check` is that comparison, and it fetches the render).
//
// WHAT THE EVIDENCE IS, per runtime:
//
//   - omp: `omp skill list --json`, run with the seat's launch environment and cwd (PI_CODING_AGENT_DIR is
//     the variable that names the agent directory omp reads). The AUTHORITATIVE SET is the resolved set
//     UNION the platform's own delivery: a listed skill whose filePath lies outside <agent dir>/skills/ was
//     loaded from a root the runtime did not install into, which is the platform's own delivery and a
//     second legitimate producer, never a finding. F2 is a listed skill in NEITHER set (and its detail
//     names the two sides that disagree when the installed copy is there); F3 is a set skill absent from
//     the list; F4 is a set skill whose filePath is not under <agent dir>/skills/.
//   - any other runtime: a note. The client builds no claude argv and reads no other runtime's loader, so it
//     has no evidence for F2-F4 there, and F5 (the filesystem) is still decided.
//
// F5 is the filesystem for every runtime: the installed copy's content must equal the tree's copy.
package clientlifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientskills"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
)

// runtimeOMP is the only runtime this check has evidence for, spelled as the room spec spells it. Any
// other runtime gets a note: the client has no reader for it, and inventing one would be a second
// mechanism for a job the runtime's own tooling owns.
const runtimeOMP = "omp"

// The codes of the ruling's table, verbatim, plus this client's own token for a note. The code is half of
// the ruling's one line, `seat <seat>: <code> <detail>`.
const (
	codeCwd     = "F1 cwd"
	codeForeign = "F2 foreign"
	codeMissing = "F3 missing"
	codePath    = "F4 path"
	codeStale   = "F5 stale"
	// codeUnobserved is NOT a ruling code: it is this client's name for "the input this code needs was not
	// observable", so a reader is told what is missing rather than shown a fabricated F1/F2/F3. It is the
	// word `note` rather than a code-shaped token, because the line it prints keeps the ruling's shape and
	// a parser reading that shape must not be able to read a note as one of the five codes.
	codeUnobserved = "note"
)

// seatSkillFailure is ONE line of the ruling's output: the seat, the code, and a detail that names what
// the evidence showed (or, for a note, what could not be read).
type seatSkillFailure struct {
	Seat   string
	Code   string
	Detail string
}

// line is the ruling's shape, `seat <seat>: <code> <detail>`.
func (f seatSkillFailure) line() string {
	return fmt.Sprintf("seat %s: %s %s", f.Seat, f.Code, f.Detail)
}

// seatSkillsVerdict is one room's answer: the DIFFERENCES the evidence proved (Failures, the ruling's
// codes) and the inputs the caller could not obtain (Notes, printed as `note`). Only Failures make a run
// fail. Both print in one line shape, so a caller that only reads stderr learns what is wrong and what it
// could not look at.
type seatSkillsVerdict struct {
	Failures []seatSkillFailure
	Notes    []seatSkillFailure
}

// seatSkillInput is ONE seat's check input: the set the runtime was given, and what the caller OBSERVED.
// Cwd is an input rather than a here-derived value because this client reads no running seat's cwd of its
// own; an empty field is "not observed", which the check reports as a note rather than deciding a code from
// a guess.
type seatSkillInput struct {
	// Seat is the seat's name within the room.
	Seat string
	// Runtime is the seat spec's runtime: "omp" is the one this check has evidence for.
	Runtime string
	// AgentDir is the seat's own directory, <roomDir>/seats/<seat>: the runtime installs the tree under
	// its skills/, and PI_CODING_AGENT_DIR names it.
	AgentDir string
	// Skills is the seat's set, as the tree resolves it (clientskills.Tree.Resolve). The installed copy on
	// disk is what F5 compares against these copies.
	Skills []clientskills.Skill
	// Cwd is the seat's working directory, when the caller observed one: a running seat's pane path. F1
	// compares it with Worktree.
	Cwd string
	// Worktree is the worktree F1 expects the seat to be in, derived by clientservices.DerivedWorktrees -
	// the same derivation `up`'s worktrees step creates. An empty one means the derivation could not be
	// made, and F1 cannot be decided.
	Worktree string
	// Env is the seat's launch environment, when the caller has it. A nil Env means the evidence command
	// inherits this process's environment.
	Env []string
}

// checkSeatSkills is the ONE entry point `up` and `doctor` share: it returns the ruling's failures for
// every seat its caller observed, and the notes about inputs the caller could not obtain. Notes are
// deduplicated by detail, because the same missing input is the same fact no matter how many seats needed
// it.
func checkSeatSkills(inputs []seatSkillInput) seatSkillsVerdict {
	verdict := seatSkillsVerdict{}
	seen := map[string]bool{}
	for _, in := range inputs {
		failures, notes := checkOneSeatSkills(in)
		verdict.Failures = append(verdict.Failures, failures...)
		for _, note := range notes {
			if seen[note.Detail] {
				continue
			}
			seen[note.Detail] = true
			verdict.Notes = append(verdict.Notes, note)
		}
	}
	return verdict
}

// reportSeatSkills writes the ruling's one line per failure and per note, so a caller that only reads
// stderr learns both what is wrong and what could not be looked at.
func reportSeatSkills(verdict seatSkillsVerdict, stderr io.Writer) {
	for _, failure := range verdict.Failures {
		fmt.Fprintln(stderr, failure.line())
	}
	for _, note := range verdict.Notes {
		fmt.Fprintln(stderr, note.line())
	}
}

// checkOneSeatSkills decides every code ONE seat's evidence can decide. A code whose input is missing is
// NOT decided: it becomes a note naming the missing input.
func checkOneSeatSkills(in seatSkillInput) ([]seatSkillFailure, []seatSkillFailure) {
	var failures, notes []seatSkillFailure
	fail := func(code, detail string) {
		failures = append(failures, seatSkillFailure{Seat: in.Seat, Code: code, Detail: detail})
	}
	note := func(detail string) {
		notes = append(notes, seatSkillFailure{Seat: in.Seat, Code: codeUnobserved, Detail: detail})
	}

	// F1: the seat's cwd must be the worktree the room's own derivation names. The cwd is observed and the
	// worktree derived, and an absent one of either is a note, never a guessed F1.
	switch {
	case in.Cwd == "" || in.Worktree == "":
		note("the seat's cwd or its worktree was not obtainable, so F1 cwd cannot be decided")
	case in.Cwd != in.Worktree:
		fail(codeCwd, fmt.Sprintf("cwd %s is not its worktree %s", in.Cwd, in.Worktree))
	}

	if in.Runtime != runtimeOMP {
		note(fmt.Sprintf("runtime %q has no skills reader in this client, so F2-F4 cannot be decided",
			in.Runtime))
		checkInstalledCopies(in, filepath.Join(in.AgentDir, clientskills.SkillsDir), fail)
		return failures, notes
	}
	checkOmpSkills(in, fail, note)
	return failures, notes
}

// ompSkillsDocument is the `omp skill list --json` shape: measured on omp 18.6.0, `{"skills":[...],
// "warnings":[]}`, where a skill carries the name it loads under, the SKILL.md it resolves to, and the
// source that delivered it ("native:user", "agents:user").
type ompSkillsDocument struct {
	Skills []ompSkillEntry `json:"skills"`
}

type ompSkillEntry struct {
	Name     string `json:"name"`
	FilePath string `json:"filePath"`
	BaseDir  string `json:"baseDir"`
	Source   string `json:"source"`
}

// ompSkillList is the omp evidence: `omp skill list --json`, run with the seat's launch environment and
// cwd. A seam so a test drives F2/F3/F4 without an omp on the machine, exactly as the fake-binary case
// drives it with one.
var ompSkillList = func(cwd string, env []string) ([]byte, error) {
	command := exec.Command("omp", "skill", "list", "--json")
	if cwd != "" {
		command.Dir = cwd
	}
	if env != nil {
		command.Env = env
	}
	return command.Output()
}

// checkOmpSkills decides F2, F3, F4 and F5 from the omp evidence. F2 and F4 read the list against the
// set; F3 reads the set against the list; F5 compares the installed copy on disk with the tree's copy and
// needs no command at all.
func checkOmpSkills(in seatSkillInput, fail func(code, detail string), note func(detail string)) {
	raw, err := ompSkillList(in.Cwd, in.Env)
	if err != nil {
		note("`omp skill list --json` could not be read: " + err.Error())
	} else {
		var document ompSkillsDocument
		if err := json.Unmarshal(raw, &document); err != nil {
			note("`omp skill list --json` did not answer JSON: " + err.Error())
		} else {
			checkOmpList(in, document.Skills, fail)
		}
	}
	checkInstalledCopies(in, filepath.Join(in.AgentDir, clientskills.SkillsDir), fail)
}

// checkOmpList is F2, F3 and F4 over one listing. The AUTHORITATIVE SET is the resolved set UNION the
// platform's own delivery, because the resolved set is not the only producer: the runtime also loads the
// platform's skills from the roots it installs them into, and a check that knows only this tree reports
// that delivery as foreign. The platform's ROOT is read from the evidence and never hardcoded: an entry
// whose filePath lies outside <agent dir>/skills/ was loaded from a root the runtime did not install into.
//
// A listed skill IN the set whose filePath is outside <agent dir>/skills/ has a wrong PATH. A listed skill
// OUTSIDE the set whose filePath IS under <agent dir>/skills/ is FOREIGN, and its detail names the
// disagreement it sits in: the installed copy is there while the tree does not declare it, so one of the
// two is wrong and the line says which entry it is. A set skill the list does not carry is MISSING.
func checkOmpList(in seatSkillInput, listed []ompSkillEntry, fail func(code, detail string)) {
	installed := filepath.Join(in.AgentDir, clientskills.SkillsDir)
	set := map[string]bool{}
	for _, skill := range in.Skills {
		set[skill.Name] = true
	}
	loaded := map[string]bool{}
	for _, entry := range listed {
		loaded[entry.Name] = true
		if !set[entry.Name] {
			// The platform's own delivery, or an entry no evidence places: only the first is a legitimate
			// second producer, and the filePath is what tells them apart.
			if entry.FilePath != "" && !pathWithin(installed, entry.FilePath) {
				continue
			}
			if entry.FilePath == "" {
				fail(codeForeign, fmt.Sprintf(
					"skill %q from %s is not in the seat's set and the listing gives no path to place it",
					entry.Name, entry.Source))
				continue
			}
			fail(codeForeign, fmt.Sprintf(
				"skill %q is installed at %s but the tree does not declare it", entry.Name, installed))
			continue
		}
		if !pathWithin(installed, entry.FilePath) {
			fail(codePath, fmt.Sprintf("skill %q loads from %s, outside %s", entry.Name, entry.FilePath, installed))
		}
	}
	for _, skill := range in.Skills {
		if !loaded[skill.Name] {
			fail(codeMissing, fmt.Sprintf("skill %q is in the seat's set but omp did not load it", skill.Name))
		}
	}
}

// checkInstalledCopies is F5 for every skill in the set: the installed copy under root must be the tree's
// copy, byte for byte. An edited - or missing - installed copy differs, and that difference is the code.
func checkInstalledCopies(in seatSkillInput, root string, fail func(code, detail string)) {
	// An ABSENT tree is ONE fact about the seat, not one fact per skill. Reporting it as N differences gives
	// N lines that all say the same thing, and none of them tells a reader whether one skill's body was
	// edited or nothing was installed at all. So the absence is named once.
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		fail(codeStale, fmt.Sprintf("the installed tree %s is absent", root))
		return
	}
	for _, skill := range in.Skills {
		if !sameTree(filepath.Join(root, skill.Name), skill.Dir) {
			fail(codeStale, fmt.Sprintf("skill %q differs from the tree's copy at %s", skill.Name, skill.Dir))
		}
	}
}

// sameTree reports whether two directories hold the same files with the same bytes. A directory that does
// not exist hashes to nothing, so a missing installed copy differs from the tree's copy - which is the F5
// it is.
func sameTree(left, right string) bool {
	leftHash, leftErr := treeHash(left)
	rightHash, rightErr := treeHash(right)
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

// treeHash is the content hash of one directory: every entry's path relative to the root, and every
// regular file's bytes, fed to one sha256 in WalkDir's lexical order.
func treeHash(root string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\n", relative)
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// pathWithin reports whether path is inside parent, the rule F4's filePath check obeys. A relative answer
// that climbs out with ".." is outside, and so is a path that only shares a name prefix.
func pathWithin(parent, path string) bool {
	if parent == "" || path == "" {
		return false
	}
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// seatSkillsRow is `doctor`'s seats row carried WITH what produced it: the differences the ruling's codes
// name (Failures) and the inputs this client could not read (Notes). The reasons travel with the row
// because a Health row's field set is 7.2's exactly and has no place for them - and a row that says
// `skills_stale` without naming a seat, a code and a detail is a row a reader cannot act on.
type seatSkillsRow struct {
	Row      clientservices.Health
	Failures []seatSkillFailure
	Notes    []seatSkillFailure
}

// seatLines names what made one seats row what it is: the differences first, then the inputs that could
// not be read. The line shape is the ruling's for both.
func seatLines(row seatSkillsRow) []string {
	lines := make([]string, 0, len(row.Failures)+len(row.Notes))
	for _, failure := range row.Failures {
		lines = append(lines, failure.line())
	}
	for _, note := range row.Notes {
		lines = append(lines, note.line())
	}
	return lines
}

// seatReasonLines is the stderr half of a DOWN row: the differences, which are why it is down. The
// notes are NOT part of it - an input the check could not read is reported whether or not any row is down,
// so a reader can tell "the seats are right" from "the seats could not be looked at".
func seatReasonLines(row seatSkillsRow) []string {
	lines := make([]string, 0, len(row.Failures))
	for _, failure := range row.Failures {
		lines = append(lines, failure.line())
	}
	return lines
}

// seatNoteLines is every seat row's notes, for the stderr surface that prints them unconditionally.
func seatNoteLines(rows []seatSkillsRow) []string {
	lines := []string{}
	for _, row := range rows {
		for _, note := range row.Notes {
			lines = append(lines, note.line())
		}
	}
	return lines
}

// seatsRowFor is `doctor`'s seats-row computation as a seam, beside rowsFor: a case drives the row without
// a runtime or a live room.
var seatsRowFor = seatsRows

// seatsRows computes the `seats` row of every room the report covers: `running`, or `skills_stale` when
// the check found a difference.
func seatsRows(rig, root string) []seatSkillsRow {
	state, _ := clientservices.ReadAgentState()
	rigs := reportedRigs(state.Rigs, rig)
	rows := make([]seatSkillsRow, 0, len(rigs))
	for _, name := range rigs {
		verdict := roomSkills(root, name)
		rowState := clientservices.Running
		if len(verdict.Failures) > 0 {
			rowState = clientservices.SkillsStale
		}
		rows = append(rows, seatSkillsRow{
			Row: clientservices.Health{
				Rig:       name,
				Safeguard: clientservices.Seats,
				State:     rowState,
				AgeS:      clientservices.UnknownAge,
			},
			Failures: verdict.Failures,
			Notes:    verdict.Notes,
		})
	}
	return rows
}

// roomSkills is the check as seatsRows calls it, a seam so a case drives the row's state without a room.
var roomSkills = roomSkillsVerdict

// roomSkillsVerdict is the check for ONE room: build the inputs from the room's spec and the runtime's
// own seat statuses, run the check, and keep the builder's notes - a tree that does not resolve is a note
// about the input, not a failure of a seat.
func roomSkillsVerdict(root, room string) seatSkillsVerdict {
	inputs, notes := seatSkillInputs(root, room)
	verdict := checkSeatSkills(inputs)
	verdict.Notes = append(notes, verdict.Notes...)
	return verdict
}

// SeatCheckLines answers ONE seat's checks for a verb that is not `doctor`: `seat reseat` has just
// restarted the seat, so the questions worth answering are the ones its new process decides - is it in its
// own worktree (F1), did it load the installed skills (F2-F4) and are they the tree's copies (F5). It
// reads the room the same way `doctor`'s row does, so the answer is the one `doctor` gives, about the seat
// the caller named.
//
// The findings are LINES, not a verdict: reseat has no exit code for them and reports them as it reports
// everything else. A seat the runtime does not report answers with that fact rather than an empty list, so
// a reader can tell "nothing to report" from "nothing could be read".
func SeatCheckLines(root, rig, seat string) []string {
	inputs, notes := seatSkillInputs(root, rig)
	var mine []seatSkillInput
	for _, input := range inputs {
		if input.Seat == seat {
			mine = append(mine, input)
		}
	}
	if len(mine) == 0 {
		return failureLines([]seatSkillFailure{{Seat: seat, Code: codeUnobserved,
			Detail: "the restarted seat has no record in room " + rig + ", so its checks cannot be read"}})
	}
	verdict := checkSeatSkills(mine)
	verdict.Notes = append(notes, verdict.Notes...)
	return failureLines(verdict.Failures, verdict.Notes)
}

// failureLines is the ruling's line shape for the findings a caller is about to report.
func failureLines(groups ...[]seatSkillFailure) []string {
	lines := []string{}
	for _, group := range groups {
		for _, finding := range group {
			lines = append(lines, finding.line())
		}
	}
	return lines
}

// roomSeats is the runtime's view of a room's seats, as a seam so a case drives the check without a
// daemon.
var roomSeats = func(rig string) ([]clientruntime.SeatStatus, error) {
	return clientruntime.NewClient("").GetRoomStatus(rig)
}

// roomSpec is the room's own record, the source of its seats' types and of the checkout the tree lives
// in. A seam for the same reason.
var roomSpec = clientteam.LoadLiveRoomSpec

// seatCwd is the seat's cwd as the caller can OBSERVE it: tmux reports the pane's current path, and the
// seat's agent runs in that pane. An absent tmux, an absent session or any failure gives "", which the
// check reports as a note rather than deciding F1 from a guess. It is a seam so a case answers from memory
// instead of a tmux server.
var seatCwd = func(session string) string {
	if session == "" {
		return ""
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return ""
	}
	out, err := exec.Command(tmux, "display-message", "-p", "-t", session+":", "#{pane_current_path}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// skillsTreeRoot is the tree the runtime installs from: the ONE variable `install_seat_skills` reads,
// AGENTHUB_CLIENT_SKILLS, from this process's environment or from the state root's .env. There is no
// second name and no guess: the runtime's install itself does nothing when the variable is unset, so a
// check that invented a root would compare a seat against a tree nobody installed.
var skillsTreeRoot = func(root string) (string, error) {
	if tree := strings.TrimSpace(os.Getenv(clientskills.RootEnv)); tree != "" {
		return tree, nil
	}
	if fromEnv := envFileValue(filepath.Join(root, ".env"), clientskills.RootEnv); fromEnv != "" {
		return fromEnv, nil
	}
	return "", errors.New(clientskills.RootEnv + " is unset, so the runtime installed no tree to compare against")
}

// envFileValue reads one KEY=VALUE line out of the state root's environment file. The client itself never
// needs it - the runtime exports the variables to the seats - but `doctor` may run outside a seat, and the
// runtime reads this same file, so the check reads it the same way rather than asking an operator to
// export what is already written down.
func envFileValue(path, key string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		return value
	}
	return ""
}

// seatSkillInputs is the CALLER's half: one input per seat the runtime reports, carrying the seat's
// resolved set, the seat directory the runtime installs into, the worktree the room's own derivation names
// and the cwd the caller could observe. A failure to read a room-level input is a NOTE about that input,
// never a fabricated per-seat finding.
func seatSkillInputs(root, room string) ([]seatSkillInput, []seatSkillFailure) {
	note := func(detail string) ([]seatSkillInput, []seatSkillFailure) {
		return nil, []seatSkillFailure{{Seat: room, Code: codeUnobserved, Detail: detail}}
	}
	if root == "" {
		var err error
		if root, err = clientenv.ResolveStateRoot(""); err != nil {
			return note(err.Error())
		}
	}
	spec, err := roomSpec(root, room)
	if err != nil {
		return note(err.Error())
	}
	treeRoot, err := skillsTreeRoot(root)
	if err != nil {
		return note(err.Error())
	}
	tree := clientskills.Tree{Root: treeRoot}
	if err := tree.Require(); err != nil {
		return note(err.Error())
	}
	// The room's own directory, read where the caller pointed the report: `--state-root DIR` names the tree
	// the rows are read from, so the check must not report one tree and inspect another.
	roomDir := ""
	if root != "" {
		roomDir = filepath.Join(root, "rooms", room)
	} else if roomDir, err = clientservices.RoomDir(room); err != nil {
		return note(err.Error())
	}
	statuses, err := roomSeats(room)
	if err != nil {
		return note(fmt.Sprintf("the runtime's seat statuses for %s could not be read: %v", room, err))
	}
	seats := make([]clientservices.SeatType, 0, len(spec.Seats))
	for _, seat := range spec.Seats {
		seats = append(seats, clientservices.SeatType{Key: seat.SeatKey, Type: seat.SeatType})
	}
	worktrees := map[string]string{}
	for _, worktree := range clientservices.DerivedWorktrees(roomDir, room, seats) {
		worktrees[worktree.SeatKey] = worktree.Path
	}

	var inputs []seatSkillInput
	var notes []seatSkillFailure
	for _, status := range statuses {
		if status.Seat == "" {
			continue
		}
		skills, err := tree.Resolve(room, status.Seat)
		if err != nil {
			notes = append(notes, seatSkillFailure{Seat: status.Seat, Code: codeUnobserved,
				Detail: "the seat's set could not be resolved: " + err.Error()})
			continue
		}
		agentDir := filepath.Join(roomDir, "seats", status.Seat)
		inputs = append(inputs, seatSkillInput{
			Seat:     status.Seat,
			Runtime:  status.Runtime,
			AgentDir: agentDir,
			Skills:   skills,
			Cwd:      seatCwd(status.SessionName),
			Worktree: worktrees[status.Seat],
			Env:      append(os.Environ(), "PI_CODING_AGENT_DIR="+agentDir),
		})
	}
	return inputs, notes
}
