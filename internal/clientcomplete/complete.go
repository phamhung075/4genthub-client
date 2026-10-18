// Package clientcomplete is the `4genteam complete` verb: the client's door to completing a task.
//
// THE COMPLETION GATE RUNS ON THE USER'S CLIENT, with Jev (internal/clientjev) like the LLM on a seat.
// The server completes a task when it is asked to and holds no Jev: this verb collects the evidence
// (internal/clientevidence), reads the task's acceptance criteria, runs the gate locally, and only then
// calls the server - evidence first, completion second.
//
// The gate is a SHADOW by default: the verdict is printed and logged and the completion goes on. With
// VALIDATION_GATE_ENFORCEMENT=true a reject or an uncertain verdict stops the verb before any server
// write, so the task stays open.
package clientcomplete

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientevidence"
	"github.com/phamhung075/4genthub-client/internal/clientjev"
)

// ExitGateRefused is the enforced gate saying no: the verdict is on stdout and the server was not written.
const ExitGateRefused = 6

const (
	enforcementEnv = "VALIDATION_GATE_ENFORCEMENT"
	taskPath       = "/api/v2/tasks/%s"
	completePath   = "/api/v2/tasks/%s/complete"
)

// completeClient is the HTTP seam to the server; a test replaces it so no request leaves the process.
var completeClient = &http.Client{Timeout: 60 * time.Second}

const usage = `usage: 4genteam complete --task <id> --base <sha> --summary <text> --test "<cmd>" [--notes <text>] [--url BASE]

Complete a task after the completion gate passes. The gate runs on this machine: the evidence (diff numstat,
SHAs, test run) is collected like 4genteam evidence, checked deterministically (D3 tests, D4 keep-out paths,
D5 diff size), then the task's acceptance criteria are put to Jev. A task with no acceptance criteria is uncertain, never
accepted: the checks alone are not a verdict. The server is not asked to judge.

  --task     REQUIRED. The task id.
  --base     REQUIRED. The commit the change is measured from.
  --summary  REQUIRED. The completion summary stored on the task.
  --test     The test command (split in-process, no shell). Without it the deterministic check D3 fails.
  --notes    Optional testing notes stored on the task.
  --url      Optional. Defaults to $AGENTHUB_URL.

Credential: $AGENTHUB_TOKEN, sent as a bearer token to the server; $TYPESAFE_API_KEY goes to TypeSafe only.
Shadow mode (default): the verdict goes to stderr and the completion goes on. With VALIDATION_GATE_ENFORCEMENT=true
(env file or environment) a reject or uncertain verdict stops before any server write and prints the verdict.
Every verdict is appended to the Jev decision log.
Exit: 0 completed, 1 a request failed or was refused, 2 usage, 5 completed but the test command exited non-zero,
      6 the enforced gate refused.
`

// Commands is what the dispatcher registers from this package: one verb, `complete`.
func Commands() []clientcmd.Command { return []clientcmd.Command{completeCommand{}} }

type completeCommand struct{}

func (completeCommand) Name() string    { return "complete" }
func (completeCommand) Summary() string { return "complete a task after the local Jev gate" }

type options struct {
	task, base, summary, test, notes, url string
}

func parse(args []string) (options, bool, error) {
	var opts options
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag == "-h" || flag == "--help" {
			return opts, true, nil
		}
		var target *string
		switch flag {
		case "--task":
			target = &opts.task
		case "--base":
			target = &opts.base
		case "--summary":
			target = &opts.summary
		case "--test":
			target = &opts.test
		case "--notes":
			target = &opts.notes
		case "--url":
			target = &opts.url
		default:
			return opts, false, fmt.Errorf("unknown argument: %s", flag)
		}
		if i+1 >= len(args) {
			return opts, false, fmt.Errorf("%s needs a value", flag)
		}
		i++
		*target = args[i]
	}
	return opts, false, nil
}

func (completeCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, help, err := parse(args)
	if help {
		_, _ = io.WriteString(stdout, usage)
		return clientcmd.ExitOK
	}
	if err != nil {
		return usageError(stderr, err.Error())
	}
	for _, required := range []struct{ flag, value string }{{"--task", opts.task}, {"--base", opts.base}, {"--summary", opts.summary}} {
		if strings.TrimSpace(required.value) == "" {
			return usageError(stderr, required.flag+" is required")
		}
	}
	baseURL := opts.url
	if baseURL == "" {
		baseURL = os.Getenv("AGENTHUB_URL")
	}
	if baseURL == "" {
		return usageError(stderr, "no server URL: set AGENTHUB_URL or pass --url")
	}
	token := os.Getenv("AGENTHUB_TOKEN")
	if token == "" {
		return usageError(stderr, "AGENTHUB_TOKEN is not set, so the completion cannot be authenticated")
	}
	cfg, err := clientjev.FromEnv()
	if err != nil {
		return usageError(stderr, err.Error())
	}

	body, failure := clientevidence.Collect(ctx, opts.base, opts.test)
	if failure != nil {
		if failure.Usage {
			return usageError(stderr, failure.Message)
		}
		fmt.Fprintf(stderr, "4genteam complete: %s\n", failure.Message)
		return clientcmd.ExitRemote
	}
	state, err := readTask(ctx, baseURL, token, opts.task)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam complete: %v\n", err)
		return clientcmd.ExitRemote
	}

	verdict := evaluate(ctx, cfg, state.Criteria, body)
	for _, warning := range verdict.Warnings {
		fmt.Fprintf(stderr, "4genteam complete: warning: %s\n", warning)
	}
	encoded, _ := json.Marshal(verdict)
	enforced := strings.EqualFold(strings.TrimSpace(os.Getenv(enforcementEnv)), "true")
	entry := clientjev.LogEntry{Timestamp: time.Now(), Kind: "gate", Summary: "task " + opts.task, Detail: verdict.Reasons, Outcome: string(verdict.Verdict)}
	if err := cfg.Log(entry); err != nil {
		fmt.Fprintf(stderr, "4genteam complete: cannot append to the Jev log %s: %v\n", cfg.LogPath, err)
		return clientcmd.ExitRemote
	}
	if verdict.Verdict != clientjev.Accept && enforced {
		_, _ = stdout.Write(append(encoded, '\n'))
		fmt.Fprintf(stderr, "4genteam complete: the gate says %s (%s); the task was not completed\n", verdict.Verdict, verdict.Reasons)
		return ExitGateRefused
	}
	mode := "shadow"
	if enforced {
		mode = "enforced"
	}
	fmt.Fprintf(stderr, "4genteam complete: gate (%s) %s: %s\n", mode, verdict.Verdict, verdict.Reasons)

	stored := clientevidence.Post(ctx, baseURL, token, opts.task, body, io.Discard, stderr)
	switch {
	case stored.Exit == clientcmd.ExitOK || stored.Exit == 5:
		// stored, with or without a failing test run: the completion may proceed.
	case stored.HTTPStatus == http.StatusConflict:
		// The evidence for this run's own bundle is ALREADY on the hub (a retry after a failed
		// completion). Refusing here would make the retry impossible, and the completion gate above
		// judged this run's local bundle, of which the stored submission is the hub's record.
		fmt.Fprintf(stderr, "4genteam complete: warning: head_sha %s already has evidence; completing on the stored submission\n", body.HeadSHA)
		stored.Exit = clientcmd.ExitOK
	default:
		fmt.Fprintf(stderr, "4genteam complete: the task was not completed: the evidence was not stored; task %s is unchanged\n", opts.task)
		return stored.Exit
	}
	if code := postComplete(ctx, baseURL, token, opts, stdout, stderr); code != clientcmd.ExitOK {
		// The completion POST's own failure (status and hub body) is printed by postComplete. What it
		// cannot know is the task's state: the POST is not idempotent from here, so read it back once
		// and name it - "the task was not completed" is the enforced gate's own vocabulary.
		after, rerr := readTask(ctx, baseURL, token, opts.task)
		switch {
		case rerr != nil:
			fmt.Fprintf(stderr, "4genteam complete: the task was not completed: the completion request failed and the state could not be read: %v\n", rerr)
		case after.Status == "done":
			fmt.Fprintf(stderr, "4genteam complete: the completion request failed, but task %s reads done: it completed\n", opts.task)
		default:
			fmt.Fprintf(stderr, "4genteam complete: the task was not completed: the completion request failed and task %s reads %s\n", opts.task, after.Status)
		}
		return code
	}
	return stored.Exit
}

// taskState is what ONE read of the task answers, because the verb needs two facts from the same
// row: the criteria the gate judges, and - only when the completion request failed - the status it
// left behind. One reader, so the two cannot disagree about the envelope.
type taskState struct {
	Status   string
	Criteria []string
}

// readTask reads the task's status and acceptance criteria from the server.
func readTask(ctx context.Context, baseURL, token, taskID string) (taskState, error) {
	route := strings.TrimRight(baseURL, "/") + fmt.Sprintf(taskPath, url.PathEscape(taskID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, route, nil)
	if err != nil {
		return taskState{}, fmt.Errorf("the request to %s failed: %v", route, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := completeClient.Do(req)
	if err != nil {
		return taskState{}, fmt.Errorf("the request to %s failed: %v", route, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return taskState{}, fmt.Errorf("reading the task: the server answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	var parsed struct {
		Task struct {
			Status             string   `json:"status"`
			AcceptanceCriteria []string `json:"acceptance_criteria"`
		} `json:"task"`
	}
	if err := json.Unmarshal(answer, &parsed); err != nil {
		return taskState{}, fmt.Errorf("the task answer is not JSON: %v", err)
	}
	return taskState{Status: parsed.Task.Status, Criteria: parsed.Task.AcceptanceCriteria}, nil
}

// postComplete asks the server to complete the task; its answer goes to stdout.
func postComplete(ctx context.Context, baseURL, token string, opts options, stdout, stderr io.Writer) int {
	query := url.Values{"completion_summary": {opts.summary}}
	if opts.notes != "" {
		query.Set("testing_notes", opts.notes)
	}
	route := strings.TrimRight(baseURL, "/") + fmt.Sprintf(completePath, url.PathEscape(opts.task)) + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route, bytes.NewReader(nil))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam complete: the request failed: %v\n", err)
		return clientcmd.ExitRemote
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := completeClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam complete: the completion request failed: %v\n", err)
		return clientcmd.ExitRemote
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(stderr, "4genteam complete: the server answered HTTP %d: %s\n", resp.StatusCode, strings.TrimSpace(string(answer)))
		return clientcmd.ExitRemote
	}
	_, _ = stdout.Write(answer)
	_, _ = io.WriteString(stdout, "\n")
	return clientcmd.ExitOK
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "4genteam complete: %s\n", message)
	_, _ = io.WriteString(stderr, "usage: 4genteam complete --task <id> --base <sha> --summary <text> --test \"<cmd>\" [--notes <text>] [--url BASE]\n")
	return clientcmd.ExitUsage
}
