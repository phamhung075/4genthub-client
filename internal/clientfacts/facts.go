// Package clientfacts is `4genteam facts`: ONE block of the facts a seat otherwise re-derives, command by
// command, before it can decide anything.
//
// WHY IT EXISTS. Measured over the room's transcripts on 2026-10-17 (20,536 bash calls): the worktree's git
// state appeared in 6,600+ commands (status 1,507, log 2,491, show 1,572, diff 1,065, rev-parse 687,
// merge-base 601, branch 423), the rail 5,316, pin-vs-worktree compares 340, `go test` 1,561, the offpeak
// schedule 96 and a task's acceptance record 90. Every one of those is a computation with one right answer,
// and a seat spent turns rebuilding it in a chain instead of reading it.
//
// THE BLOCK IS A SNAPSHOT, and every source says when it could not be read: a block field carries
// `"state":"unavailable"` and a one-line `reason` rather than a zero that would read as a measurement. The
// verb still exits 0 in that case, because the block - including what is missing - is the answer.
//
// THE FIELD NAMES ARE THE CONTRACT (facts_test.go pins every key and value against a fixture repo). A new
// fact is a new field; a field is not renamed or dropped, because a seat's transcript and a recheck script
// both read this JSON.
package clientfacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// Schema is the block's own version. A consumer checks it before reading; a change to the field set bumps it.
const Schema = "4genteam.facts.v1"

const usage = `usage: 4genteam facts [--room ROOM] [--seat SEAT] [--session NAME] [--task ID]
                    [--repo DIR] [--base REF] [--pins-repo DIR] [--pins-ref REF]
                    [--url BASE] [--socket PATH]

Print ONE JSON block of the facts a seat re-derives by hand today: the worktree's git state, the pin the
pinning repo holds against this HEAD, the room rail's rows, the offpeak schedule, the newest CI run for the
current branch, and a task's acceptance record. The verb writes nothing and never guesses: a source it
cannot read prints state "unavailable" with a one-line reason, and the exit is still 0.

Defaults: room, seat and session come from AGENTHUB_ROOM, AGENTHUB_SEAT and AGENTHUB_SESSION_NAME (the
session is derived from room+seat when the pair is known); --repo is the working directory and --base main;
the pinning repo is found as the parent of this repo's MAIN worktree, and --url defaults to $AGENTHUB_URL.
The task block needs --task; the queue block needs a room; CI needs gh on PATH.

Exit: 0 the block printed (whatever its sources said), 1 the block could not be written, 2 usage.
`

// Block is the whole answer. Every field is always present.
type Block struct {
	Schema  string  `json:"schema"`
	Room    string  `json:"room"`
	Seat    string  `json:"seat"`
	Session string  `json:"session"`
	Repo    Repo    `json:"repo"`
	Pins    Pins    `json:"pins"`
	Queue   Queue   `json:"queue"`
	Offpeak Offpeak `json:"offpeak"`
	CI      CI      `json:"ci"`
	Task    Task    `json:"task"`
}

// Repo is the worktree's git state: what `git status --short --branch`, `git rev-parse`, `git log` and
// `git merge-base` chains were assembled to answer.
type Repo struct {
	State       string   `json:"state"` // ok | unavailable
	Reason      string   `json:"reason"`
	Root        string   `json:"root"`
	Branch      string   `json:"branch"`
	Detached    bool     `json:"detached"`
	Head        string   `json:"head"`
	HeadShort   string   `json:"head_short"`
	Dirty       int      `json:"dirty"`
	DirtyPaths  []string `json:"dirty_paths"`
	Base        string   `json:"base"`
	BaseSha     string   `json:"base_sha"`
	BaseState   string   `json:"base_state"` // ok | missing
	AheadOfBase int      `json:"ahead_of_base"`
	BehindBase  int      `json:"behind_base"`
}

// Pins is the pin-vs-worktree fact: the gitlink the pinning repo holds at Ref, against this repo's HEAD.
// `state` is same, ahead (HEAD carries commits the pin does not), behind, diverged, or unavailable.
type Pins struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Pin    string `json:"pin"`
	Head   string `json:"head"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
}

// Queue is the room rail, counted from the daemon's own answer. "Mine" narrows to the rows addressed to
// this seat's session; the newest active one is named so a seat does not list rows to find it.
type Queue struct {
	State             string `json:"state"` // ok | unavailable
	Reason            string `json:"reason"`
	Room              string `json:"room"`
	Rows              int    `json:"rows"`
	Pending           int    `json:"pending"`
	InProgress        int    `json:"in_progress"`
	Blocked           int    `json:"blocked"`
	HandedOff         int    `json:"handed_off"`
	Done              int    `json:"done"`
	Other             int    `json:"other"`
	MinePending       int    `json:"mine_pending"`
	MineInProgress    int    `json:"mine_in_progress"`
	MineBlocked       int    `json:"mine_blocked"`
	NewestMineID      string `json:"newest_mine_id"`
	NewestMineSummary string `json:"newest_mine_summary"`
}

// Offpeak is the billing schedule's answer for this instant, in the verb's own words (peak / off-peak).
type Offpeak struct {
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Peak    bool   `json:"peak"`
	Stamp   string `json:"stamp"`
	Next    string `json:"next"`
	Holiday bool   `json:"holiday"`
}

// CI is the newest run `gh run list` reports for this branch. Head names the run's commit, so a reader can
// tell a run on this tip from one on an older commit; the comparison is left to the reader on purpose.
type CI struct {
	State      string `json:"state"` // ok | unavailable
	Reason     string `json:"reason"`
	Head       string `json:"head"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Workflow   string `json:"workflow"`
	RunID      int64  `json:"run_id"`
	URL        string `json:"url"`
}

// Task is a task's acceptance record: its state and the criteria a completion gate judges. The criteria are
// carried whole rather than counted, because a seat re-reads them before it starts.
type Task struct {
	State         string   `json:"state"` // ok | unavailable
	Reason        string   `json:"reason"`
	ID            string   `json:"id"`
	Status        string   `json:"status"`
	Priority      string   `json:"priority"`
	Criteria      []string `json:"criteria"`
	CriteriaCount int      `json:"criteria_count"`
}

type command struct{}

// Commands is what the dispatcher registers from this package: one top-level verb.
func Commands() []clientcmd.Command { return []clientcmd.Command{command{}} }

func (command) Name() string { return "facts" }
func (command) Summary() string {
	return "one JSON block of git, pin, queue, offpeak, CI and task facts (a seat reads it instead of re-deriving)"
}

func (command) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, usage)
		return clientcmd.ExitOK
	}
	opts, err := parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam facts: %v\n%s\n", err, usage)
		return clientcmd.ExitUsage
	}
	block := snapshot(ctx, opts)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(block); err != nil {
		fmt.Fprintf(stderr, "4genteam facts: the block could not be written: %v\n", err)
		return clientcmd.ExitRemote
	}
	return clientcmd.ExitOK
}

type options struct {
	room, seat, session string
	task, repo, base    string
	pinsRepo, pinsRef   string
	url, socket         string
}

// parse reads the verb's flags. A flag that is not in the list is a caller error naming it, the client's
// rule everywhere; an argument that is not a flag is refused too, because this verb takes no positionals.
func parse(args []string) (options, error) {
	opts := options{repo: ".", base: "main", pinsRef: "main"}
	targets := map[string]*string{
		"--room":      &opts.room,
		"--seat":      &opts.seat,
		"--session":   &opts.session,
		"--task":      &opts.task,
		"--repo":      &opts.repo,
		"--base":      &opts.base,
		"--pins-repo": &opts.pinsRepo,
		"--pins-ref":  &opts.pinsRef,
		"--url":       &opts.url,
		"--socket":    &opts.socket,
	}
	i := 0
	for i < len(args) {
		flag := args[i]
		target, known := targets[flag]
		if !known {
			if strings.HasPrefix(flag, "-") {
				return opts, fmt.Errorf("unknown argument: %s", flag)
			}
			return opts, fmt.Errorf("it takes no positional arguments, and got %q", flag)
		}
		if i+1 >= len(args) {
			return opts, fmt.Errorf("%s needs a value", flag)
		}
		*target = args[i+1]
		i += 2
	}
	if strings.TrimSpace(opts.repo) == "" {
		return opts, fmt.Errorf("--repo must not be empty")
	}
	if strings.TrimSpace(opts.base) == "" {
		return opts, fmt.Errorf("--base must not be empty")
	}
	if strings.TrimSpace(opts.pinsRef) == "" {
		return opts, fmt.Errorf("--pins-ref must not be empty")
	}
	return opts, nil
}

// snapshot assembles the block. The sources are package-level function values so a test replaces them:
// only repoFacts runs git, and only offpeakFacts reads the clock, so a fixture test can pin everything else.
func snapshot(ctx context.Context, opts options) Block {
	env := func(name string) string { return strings.TrimSpace(os.Getenv(name)) }
	room, seat, session := env("AGENTHUB_ROOM"), env("AGENTHUB_SEAT"), env("AGENTHUB_SESSION_NAME")
	if opts.room != "" {
		room = opts.room
	}
	if opts.seat != "" {
		seat = opts.seat
	}
	if opts.session != "" {
		session = opts.session
	}
	if session == "" && room != "" && seat != "" {
		session = clientruntime.SeatSessionName(room, seat)
	}

	repo := repoFacts(opts.repo, opts.base)
	dir := opts.repo
	if repo.State == "ok" {
		dir = repo.Root
	}
	pins := pinsFacts(dir, repo.Head, opts.pinsRepo, opts.pinsRef)
	queue := queueSource(room, session, opts.socket)
	offpeak := offpeakSource()
	ci := ciSource(dir, repo.Branch)
	baseURL := opts.url
	if baseURL == "" {
		baseURL = env("AGENTHUB_URL")
	}
	task := taskSource(ctx, opts.task, baseURL, env("AGENTHUB_TOKEN"))

	return Block{
		Schema:  Schema,
		Room:    room,
		Seat:    seat,
		Session: session,
		Repo:    repo,
		Pins:    pins,
		Queue:   queue,
		Offpeak: offpeak,
		CI:      ci,
		Task:    task,
	}
}
