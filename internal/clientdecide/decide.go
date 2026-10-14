// Package clientdecide is the `4genteam decide` verb: one step of the seat/lead/owner decision ladder.
//
// A seat or lead that must choose between options posts the question to the backend's stateless
// evaluate route. The backend asks Jev; at or above its confidence threshold the tier decides for
// itself and the answer comes back, otherwise the response names the next tier and carries the
// recommended option so the caller re-asks one tier up (`--tier lead`) or raises it to the owner.
// A category the backend configures as owner-only (DECISION_OWNER_ONLY_CATEGORIES) always answers
// "escalate to owner", whatever the confidence. The client holds no policy: it posts and relays.
//
// The owner tier is not this verb: a question that reaches the owner becomes a row the owner answers
// on the dashboard, raised through the rig's own ask path.
package clientdecide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// evaluatePath is the backend's ladder route.
const evaluatePath = "/api/v2/openrig/decisions/evaluate"

// ExitEscalated is the answer "this tier did not decide": the response is valid and names the next
// tier, so it is neither a success nor a failure of the call.
const ExitEscalated = 5

var decideClient = &http.Client{Timeout: 60 * time.Second}

const usage = `usage: 4genteam decide --tier seat|lead --summary <text> (--questions <json> | --questions-file <path|->) [--category C] [--detail D] [--url BASE]

Ask one step of the decision ladder. The backend evaluates the question; the JSON response goes to stdout.

  --tier            REQUIRED. seat or lead: the tier asking. The owner answers on the dashboard.
  --summary         REQUIRED. One line naming the decision.
  --questions       The questions as a JSON array (id, question, options[id,label,recommended]).
  --questions-file  The same array read from a file, or "-" for stdin. One of the two is REQUIRED.
  --category        Optional. A free token; categories the backend lists as owner-only go to the owner.
  --detail          Optional. Context for the evaluator.
  --url             Optional. Defaults to $AGENTHUB_URL.

Credential: $AGENTHUB_TOKEN, sent as a bearer token. It is never printed.
Exit: 0 this tier decided, 5 escalated to the next tier (see escalate_to), 1 the request failed or was refused, 2 usage.
`

// Commands is what the dispatcher registers from this package: one verb, `decide`.
func Commands() []clientcmd.Command { return []clientcmd.Command{decideCommand{}} }

type decideCommand struct{}

func (decideCommand) Name() string    { return "decide" }
func (decideCommand) Summary() string { return "ask one step of the seat/lead/owner decision ladder" }

type options struct {
	tier, category, summary, detail, questions, questionsFile, url string
}

type evaluateBody struct {
	Tier      string          `json:"tier"`
	Category  string          `json:"category,omitempty"`
	Summary   string          `json:"summary"`
	Detail    string          `json:"detail,omitempty"`
	Questions json.RawMessage `json:"questions"`
}

func (decideCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, help, err := parse(args)
	if help {
		_, _ = io.WriteString(stdout, usage)
		return clientcmd.ExitOK
	}
	if err != nil {
		return usageError(stderr, err.Error())
	}
	if opts.tier != "seat" && opts.tier != "lead" {
		return usageError(stderr, "--tier must be seat or lead")
	}
	if opts.summary == "" {
		return usageError(stderr, "--summary is required")
	}
	raw, err := readQuestions(opts)
	if err != nil {
		return usageError(stderr, err.Error())
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
		return usageError(stderr, "AGENTHUB_TOKEN is not set, so the question cannot be authenticated")
	}
	return post(ctx, baseURL, token, evaluateBody{Tier: opts.tier, Category: opts.category, Summary: opts.summary, Detail: opts.detail, Questions: raw}, stdout, stderr)
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
		case "--tier":
			target = &opts.tier
		case "--category":
			target = &opts.category
		case "--summary":
			target = &opts.summary
		case "--detail":
			target = &opts.detail
		case "--questions":
			target = &opts.questions
		case "--questions-file":
			target = &opts.questionsFile
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

// readQuestions returns the questions array, checked to be JSON before any request is made.
func readQuestions(opts options) (json.RawMessage, error) {
	if (opts.questions == "") == (opts.questionsFile == "") {
		return nil, fmt.Errorf("give exactly one of --questions or --questions-file")
	}
	text := opts.questions
	if opts.questionsFile == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("cannot read stdin: %v", err)
		}
		text = string(data)
	} else if opts.questionsFile != "" {
		data, err := os.ReadFile(opts.questionsFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read --questions-file: %v", err)
		}
		text = string(data)
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("the questions are not valid JSON")
	}
	return json.RawMessage(strings.TrimSpace(text)), nil
}

func post(ctx context.Context, baseURL, token string, body evaluateBody, stdout, stderr io.Writer) int {
	url := strings.TrimRight(baseURL, "/") + evaluatePath
	payload, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam decide: cannot encode the question: %v\n", err)
		return clientcmd.ExitRemote
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam decide: the request to %s failed: %v\n", url, err)
		return clientcmd.ExitRemote
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := decideClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam decide: the request to %s failed: %v\n", url, err)
		return clientcmd.ExitRemote
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(resp.Body)
	_, _ = stdout.Write(answer)
	_, _ = io.WriteString(stdout, "\n")
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(stderr, "4genteam decide: the server answered HTTP %d\n", resp.StatusCode)
		return clientcmd.ExitRemote
	}
	var outcome struct {
		Decided bool `json:"decided"`
	}
	if err := json.Unmarshal(answer, &outcome); err != nil {
		fmt.Fprintf(stderr, "4genteam decide: the answer is not JSON: %v\n", err)
		return clientcmd.ExitRemote
	}
	if !outcome.Decided {
		return ExitEscalated
	}
	return clientcmd.ExitOK
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "4genteam decide: %s\n", message)
	_, _ = io.WriteString(stderr, "usage: 4genteam decide --tier seat|lead --summary <text> (--questions <json> | --questions-file <path|->) [--category C] [--detail D] [--url BASE]\n")
	return clientcmd.ExitUsage
}
