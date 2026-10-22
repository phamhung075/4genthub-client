package clientfacts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// gitlinkName is the one submodule this workspace pins: the hub records agenthub_client as a gitlink, and the
// commit it records is the pin every session start reads.
const gitlinkName = "agenthub_client"

// The sources are function values for one reason: a test pins the WHOLE block without a daemon, a hub, a
// network or `gh`. Only repoFacts and pinsFacts run git, against the fixture the test builds.
var (
	queueSource   = queueFacts
	offpeakSource = offpeakFacts
	ciSource      = ciFacts
	taskSource    = taskFacts
)

// git runs one git command in dir and returns stdout. A failure carries git's own first stderr line, so the
// reason in the block says what git said rather than "exit status 128".
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		message := firstLine(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return string(out), nil
}

// firstLine is one line of an error, whitespace collapsed: a source's reason is a field, not a log.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			return line
		}
	}
	return ""
}

// repoFacts reads the worktree's git state. `base` is the ref the ahead/behind counts are measured against
// (main by default): the fact the lead's git hook warns about, and the one a seat used to build with
// `git rev-list --count` and a merge-base probe.
func repoFacts(dir, base string) Repo {
	root, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repo{State: "unavailable", Reason: err.Error(), DirtyPaths: []string{}}
	}
	repo := Repo{State: "ok", Root: strings.TrimSpace(root), Base: base, DirtyPaths: []string{}}
	if head, err := git(repo.Root, "rev-parse", "HEAD"); err == nil {
		repo.Head = strings.TrimSpace(head)
	} else {
		repo.State, repo.Reason = "unavailable", err.Error()
		return repo
	}
	if short, err := git(repo.Root, "rev-parse", "--short", "HEAD"); err == nil {
		repo.HeadShort = strings.TrimSpace(short)
	}
	if branch, err := git(repo.Root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		repo.Branch = strings.TrimSpace(branch)
		repo.Detached = repo.Branch == "HEAD"
	}
	repo.Dirty, repo.DirtyPaths = dirtyPaths(repo.Root)
	repo.BaseState = "ok"
	if sha, err := git(repo.Root, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err == nil {
		repo.BaseSha = strings.TrimSpace(sha)
	} else {
		repo.BaseState = "missing"
	}
	if repo.BaseState == "ok" {
		// --left-right --count A...B prints "<in A not B>\t<in B not A>": left is how far this HEAD is BEHIND
		// the base, right how far it is AHEAD. The order is git's own and lives here rather than at a caller.
		if counts, err := git(repo.Root, "rev-list", "--left-right", "--count", base+"...HEAD"); err == nil {
			fields := strings.Fields(counts)
			if len(fields) == 2 {
				repo.BehindBase, _ = strconv.Atoi(fields[0])
				repo.AheadOfBase, _ = strconv.Atoi(fields[1])
			}
		}
	}
	return repo
}

// dirtyPaths is `git status --porcelain` parsed once: the count and the paths. A rename's destination is the
// path a reader wants (`old -> new`), and an untracked file counts like a modified one, which is what
// `--short --branch` showed a seat before it re-ran `status` without the flag.
func dirtyPaths(root string) (int, []string) {
	out, err := git(root, "status", "--porcelain")
	if err != nil {
		return 0, []string{}
	}
	paths := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		path := strings.TrimPrefix(line, line[:minInt(3, len(line))])
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+len(" -> "):]
		}
		paths = append(paths, strings.TrimSpace(path))
	}
	return len(paths), paths
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// pinsFacts is the pin-vs-worktree fact. The pinning repo is either named (--pins-repo) or found: this repo
// itself when its tree carries the gitlink at ref (a seat whose worktree IS the hub), else the PARENT of this
// repo's main worktree (a seat whose worktree is the client submodule). Discovery is verified, not guessed.
//
// The pin is compared with the HEAD of the repo that HOLDS its objects: a hub records the client sha and the
// client checkout holds the commit, so the comparison runs in that checkout and Head reports its HEAD. Read in
// the repo that records the pin, the compare is "Invalid symmetric difference" - the sha is not an object
// there - and that is why the fact answered unavailable to every seat in a hub worktree.
func pinsFacts(dir, head, pinsRepo, pinsRef string) Pins {
	pins := Pins{Ref: pinsRef, Head: head}
	if head == "" {
		pins.State, pins.Reason = "unavailable", "the repo HEAD could not be read, so there is no head to compare"
		return pins
	}
	repo := strings.TrimSpace(pinsRepo)
	if repo == "" {
		found, err := discoverPinsRepo(dir, pinsRef)
		if err != nil {
			pins.State, pins.Reason = "unavailable", err.Error()
			return pins
		}
		repo = found
	}
	pins.Repo = repo
	pin, err := pinAt(repo, pinsRef)
	if err != nil {
		pins.State, pins.Reason = "unavailable", err.Error()
		return pins
	}
	pins.Pin = pin
	home, err := pinHome(dir, repo, pin)
	if err != nil {
		pins.State, pins.Reason = "unavailable", err.Error()
		return pins
	}
	if !samePath(home, dir) {
		// The caller is the repo that RECORDS the pin, not the one that holds it: the pin measures the
		// submodule checkout, so that checkout's HEAD is the head this fact reports.
		if resolved, err := git(home, "rev-parse", "HEAD"); err == nil {
			pins.Head = strings.TrimSpace(resolved)
		} else {
			pins.State, pins.Reason = "unavailable", err.Error()
			return pins
		}
	}
	// Same rule as repoFacts: A...B prints the commits in A that B lacks (pin-only = behind) first, then the
	// commits in B that A lacks (HEAD-only = ahead). Measured on the fixture, not assumed.
	if counts, err := git(home, "rev-list", "--left-right", "--count", pins.Pin+"..."+pins.Head); err == nil {
		fields := strings.Fields(counts)
		if len(fields) == 2 {
			pins.Behind, _ = strconv.Atoi(fields[0])
			pins.Ahead, _ = strconv.Atoi(fields[1])
		}
	} else {
		pins.State, pins.Reason = "unavailable", err.Error()
		return pins
	}
	switch {
	case pins.Pin == pins.Head:
		pins.State = "same"
	case pins.Ahead > 0 && pins.Behind == 0:
		pins.State = "ahead"
	case pins.Behind > 0 && pins.Ahead == 0:
		pins.State = "behind"
	default:
		pins.State = "diverged"
	}
	return pins
}

// pinHome is the repo that holds the pin's commit: this repo when it resolves the sha (the caller is the
// submodule checkout, or the pin is a commit the caller already carries), else the pinning repo's checkout of
// the gitlink. A sha that neither holds is not a fact this block can report, and the reason names the
// submodule checkout that was read, so the reader knows which one to fetch.
func pinHome(dir, pinsRepo, pin string) (string, error) {
	if _, err := git(dir, "rev-parse", "--verify", "--quiet", pin+"^{commit}"); err == nil {
		return dir, nil
	}
	sub := filepath.Join(pinsRepo, gitlinkName)
	if _, err := git(sub, "rev-parse", "--verify", "--quiet", pin+"^{commit}"); err == nil {
		return sub, nil
	}
	return "", fmt.Errorf("the pin %s is not an object in %s or in its %s submodule checkout: fetch %s in %s, or check the pin",
		pin, pinsRepo, gitlinkName, gitlinkName, sub)
}

// discoverPinsRepo finds the repo that pins this one, or says why it cannot. Two verified shapes, in order:
// THIS repo when its own tree at ref carries the gitlink (a seat whose worktree is the hub - the workspace's
// parent is a directory of repos, not a worktree), else the parent of this repo's main worktree (a seat whose
// worktree is the client submodule, whose parent is the hub).
func discoverPinsRepo(dir, ref string) (string, error) {
	if root, err := git(dir, "rev-parse", "--show-toplevel"); err == nil {
		if _, err := pinAt(strings.TrimSpace(root), ref); err == nil {
			return strings.TrimSpace(root), nil
		}
	}
	out, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("the pinning repo could not be discovered: %v", err)
	}
	mainWorktree := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			mainWorktree = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			break
		}
	}
	if mainWorktree == "" {
		return "", fmt.Errorf("the pinning repo could not be discovered: git named no main worktree")
	}
	parent := filepath.Dir(mainWorktree)
	toplevel, err := git(parent, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("the pinning repo could not be discovered: %s is not a git worktree; name one with --pins-repo", parent)
	}
	if !samePath(strings.TrimSpace(toplevel), parent) {
		return "", fmt.Errorf("the pinning repo could not be discovered: %s is inside %s; name one with --pins-repo", parent, strings.TrimSpace(toplevel))
	}
	return parent, nil
}

// pinAt is the gitlink `ls-tree` reports for gitlinkName at ref: "<mode> commit <sha>\t<name>". The mode
// must be 160000 - a plain tree at that name is not a pin, and saying it was would be a wrong fact.
func pinAt(repo, ref string) (string, error) {
	out, err := git(repo, "ls-tree", ref, "--", gitlinkName)
	if err != nil {
		return "", fmt.Errorf("the pin at %s could not be read: %v", ref, err)
	}
	fields := strings.Fields(out)
	if len(fields) < 3 || fields[0] != "160000" {
		return "", fmt.Errorf("the pin at %s could not be read: %s holds no %s gitlink", ref, repo, gitlinkName)
	}
	return fields[2], nil
}

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && filepath.Clean(resolvedA) == filepath.Clean(resolvedB)
}

// queueFacts counts the room rail from the daemon's own answer. It asks ONCE and counts in-process, and it
// does NOT send RequesterSession: that field makes the daemon answer "no change since" to an identical
// repeat inside five minutes, which would break a verb whose whole job is to answer a read.
func queueFacts(room, session, socket string) Queue {
	if room == "" {
		return Queue{State: "unavailable", Reason: "no room: pass --room or set AGENTHUB_ROOM"}
	}
	rows, err := clientruntime.NewClient(socket).QueueList(room, clientruntime.QueueListFilter{})
	if err != nil {
		return Queue{State: "unavailable", Reason: err.Error(), Room: room}
	}
	return countQueue(room, session, rows)
}

// countQueue is the pure count the test pins: the daemon's rows in, the rail's facts out.
func countQueue(room, session string, rows []clientruntime.QueueRow) Queue {
	facts := Queue{State: "ok", Room: room, Rows: len(rows)}
	newestTs := ""
	newest := func(row clientruntime.QueueRow) {
		if row.TsCreated > newestTs || (row.TsCreated == newestTs && row.QitemID > facts.NewestMineID) {
			newestTs, facts.NewestMineID, facts.NewestMineSummary = row.TsCreated, row.QitemID, row.Summary
		}
	}
	for _, row := range rows {
		switch row.State {
		case "pending":
			facts.Pending++
		case "in-progress":
			facts.InProgress++
		case "blocked":
			facts.Blocked++
		case "handed-off":
			facts.HandedOff++
		case "done":
			facts.Done++
		default:
			facts.Other++
		}
		if session == "" || row.DestinationSession != session {
			continue
		}
		switch row.State {
		case "pending":
			facts.MinePending++
			newest(row)
		case "in-progress":
			facts.MineInProgress++
			newest(row)
		case "blocked":
			facts.MineBlocked++
			newest(row)
		}
	}
	return facts
}

// offpeakFacts reads the schedule through the package that owns it, so this verb cannot drift into a second
// opinion about when DeepSeek bills at half rate.
func offpeakFacts() Offpeak {
	verdict, err := clientoffpeak.Now()
	if err != nil {
		return Offpeak{State: "unavailable", Reason: err.Error()}
	}
	return Offpeak{
		State:   strings.ToLower(verdict.State()),
		Reason:  verdict.Reason,
		Peak:    verdict.Peak,
		Stamp:   verdict.Stamp(),
		Next:    verdict.Next,
		Holiday: verdict.Holiday,
	}
}

// ciFacts is the newest run `gh run list` reports for the branch, with the run's head sha so the reader can
// tell a run on this tip from an older one. `gh` absent, unauthenticated, or a repo with no runs is
// unavailable with the reason gh or its answer gave - never a green that was not observed.
func ciFacts(dir, branch string) CI {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return CI{State: "unavailable", Reason: "gh is not on PATH, so no CI run can be read"}
	}
	args := []string{"run", "list", "--limit", "1", "--json", "status,conclusion,headSha,workflowName,databaseId,url"}
	if branch != "" && branch != "HEAD" {
		args = append(args, "--branch", branch)
	}
	cmd := exec.Command(gh, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		reason := firstLine(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return CI{State: "unavailable", Reason: reason}
	}
	var runs []struct {
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HeadSha      string `json:"headSha"`
		WorkflowName string `json:"workflowName"`
		DatabaseID   int64  `json:"databaseId"`
		URL          string `json:"url"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return CI{State: "unavailable", Reason: "gh's answer is not JSON: " + err.Error()}
	}
	if len(runs) == 0 {
		return CI{State: "unavailable", Reason: "gh reports no workflow run for this branch"}
	}
	run := runs[0]
	return CI{
		State:      "ok",
		Head:       run.HeadSha,
		Status:     run.Status,
		Conclusion: run.Conclusion,
		Workflow:   run.WorkflowName,
		RunID:      run.DatabaseID,
		URL:        run.URL,
	}
}

// factsClient is the HTTP seam for the task read. A test replaces it so no request leaves the process.
var factsClient = &http.Client{Timeout: 20 * time.Second}

const taskPath = "/api/v2/tasks/%s"

// taskFacts reads a task's acceptance record. Without --task the block says so instead of naming some other
// task; a missing server or token is the same kind of answer, because none of the three is a fact about the
// task.
func taskFacts(ctx context.Context, taskID, baseURL, token string) Task {
	empty := make([]string, 0)
	if strings.TrimSpace(taskID) == "" {
		return Task{State: "unavailable", Reason: "no task: pass --task", Criteria: empty}
	}
	if strings.TrimSpace(baseURL) == "" {
		return Task{State: "unavailable", Reason: "AGENTHUB_URL is not set, so no task can be read", ID: taskID, Criteria: empty}
	}
	if strings.TrimSpace(token) == "" {
		return Task{State: "unavailable", Reason: "AGENTHUB_TOKEN is not set, so the task read cannot be authenticated", ID: taskID, Criteria: empty}
	}
	route := strings.TrimRight(baseURL, "/") + fmt.Sprintf(taskPath, url.PathEscape(taskID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, route, nil)
	if err != nil {
		return Task{State: "unavailable", Reason: "the request failed: " + err.Error(), ID: taskID, Criteria: empty}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := factsClient.Do(req)
	if err != nil {
		return Task{State: "unavailable", Reason: "the request failed: " + err.Error(), ID: taskID, Criteria: empty}
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Task{State: "unavailable", Reason: fmt.Sprintf("the server answered HTTP %d: %s", resp.StatusCode, firstLine(string(answer))), ID: taskID, Criteria: empty}
	}
	var parsed struct {
		Task struct {
			Status             string   `json:"status"`
			Priority           string   `json:"priority"`
			AcceptanceCriteria []string `json:"acceptance_criteria"`
		} `json:"task"`
	}
	if err := json.Unmarshal(answer, &parsed); err != nil {
		return Task{State: "unavailable", Reason: "the task answer is not JSON: " + err.Error(), ID: taskID, Criteria: empty}
	}
	criteria := parsed.Task.AcceptanceCriteria
	if criteria == nil {
		criteria = empty
	}
	return Task{
		State:         "ok",
		ID:            taskID,
		Status:        parsed.Task.Status,
		Priority:      parsed.Task.Priority,
		Criteria:      criteria,
		CriteriaCount: len(criteria),
	}
}
