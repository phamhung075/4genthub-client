// Package clientdecide is the `4genteam decide` verb: one step of the seat/lead/owner decision ladder.
//
// THE LADDER RUNS ON THE USER'S CLIENT, with Jev (internal/clientjev) like the LLM on a seat: the
// server is not called and holds no Jev. A seat or lead that must choose between options asks Jev
// here; at or above the confidence threshold on every question the tier decides for itself and the
// answer is printed, otherwise the output names the next tier and carries the recommended option so
// the caller re-asks one tier up (`--tier lead`) or raises it to the owner. A category listed as
// owner-only always answers "escalate to owner" by code, without a Jev call, whatever the confidence.
//
// The owner tier is not this verb: a question that reaches the owner becomes a row the owner answers
// on the dashboard, raised through the rig's own ask path.
package clientdecide

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientjev"
	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// ExitEscalated is the answer "this tier did not decide": the output is valid and names the next
// tier, so it is neither a success nor a failure of the call.
const ExitEscalated = 5

const usage = `usage: 4genteam decide --tier seat|lead --summary <text> (--questions <json> | --questions-file <path|->) [--category C] [--detail D]

Ask one step of the decision ladder. Jev is asked from this machine; the JSON outcome goes to stdout.

  --tier            REQUIRED. seat or lead: the tier asking. The owner answers on the dashboard.
  --summary         REQUIRED. One line naming the decision.
  --questions       The questions as a JSON array (id, question, options[id,label,recommended]).
  --questions-file  The same array read from a file, or "-" for stdin. One of the two is REQUIRED.
  --category        Optional. A free token; credentials, spend and $DECISION_OWNER_ONLY_CATEGORIES always go to the owner.
  --detail          Optional. Context for the evaluator.

Configuration, from ~/.config/4genthub/.env or the environment: TYPESAFE_API_KEY (required), TYPESAFE_BASE_URL,
JEV_DECISION_MODEL (jev-1.13.0), JEV_DECISION_THRESHOLD (0.8), JEV_DECISION_LOG_PATH. Every outcome is appended
to the Jev decision log (~/.config/4genthub/jev_decisions.log). No server call is made.
Exit: 0 this tier decided, 5 escalated to the next tier (see escalate_to), 1 the evaluation could not be recorded, 2 usage.
`

// Commands is what the dispatcher registers from this package: one verb, `decide`.
func Commands() []clientcmd.Command { return []clientcmd.Command{decideCommand{}} }

type decideCommand struct{}

func (decideCommand) Name() string    { return "decide" }
func (decideCommand) Summary() string { return "ask one step of the seat/lead/owner decision ladder" }

type options struct {
	tier, category, summary, detail, questions, questionsFile string
}

// questionResult is what one question came to: Jev's pick and confidence, and the option the asker
// recommended (empty when none was marked).
type questionResult struct {
	QuestionID  string  `json:"question_id"`
	OptionID    string  `json:"option_id,omitempty"`
	Confidence  float64 `json:"confidence"`
	Recommended string  `json:"recommended,omitempty"`
}

// outcome is one step of the ladder. Decided means this tier answers and Answers holds one option per
// question. Otherwise EscalateTo is the next tier and Reason says why.
type outcome struct {
	Tier       string            `json:"tier"`
	Decided    bool              `json:"decided"`
	Answers    map[string]string `json:"answers,omitempty"`
	EscalateTo string            `json:"escalate_to,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Questions  []questionResult  `json:"questions"`
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
	summary, detail := strings.TrimSpace(opts.summary), strings.TrimSpace(opts.detail)
	if summary == "" {
		return usageError(stderr, "--summary is required")
	}
	for _, field := range []struct{ name, value string }{{"summary", summary}, {"detail", detail}} {
		if secretscan.Contains(field.value) {
			return usageError(stderr, field.name+" contains what looks like a credential")
		}
	}
	raw, err := readQuestions(opts)
	if err != nil {
		return usageError(stderr, err.Error())
	}
	questions, err := clientjev.ParseQuestions(raw)
	if err != nil {
		return usageError(stderr, err.Error())
	}
	cfg, err := clientjev.FromEnv()
	if err != nil {
		return usageError(stderr, err.Error())
	}
	category := strings.ToLower(strings.TrimSpace(opts.category))
	if !cfg.OwnerOnly(category) && cfg.APIKey == "" {
		return usageError(stderr, clientjev.ErrNoKey.Error())
	}

	result, picks, evalErr := ladder(ctx, cfg, opts.tier, category, summary, detail, questions)
	entry := clientjev.LogEntry{
		Timestamp: time.Now(), Kind: "decision", Summary: summary, Detail: detail, Choices: picks,
		Tier: opts.tier, Category: category, Outcome: "decided",
	}
	if !result.Decided {
		entry.Outcome = "escalated-to-" + result.EscalateTo
	}
	if evalErr != nil {
		entry.Error = evalErr.Error()
	}
	if err := cfg.Log(entry); err != nil {
		fmt.Fprintf(stderr, "4genteam decide: cannot append to the Jev log %s: %v\n", cfg.LogPath, err)
		return clientcmd.ExitRemote
	}
	encoded, _ := json.Marshal(result)
	_, _ = stdout.Write(append(encoded, '\n'))
	if !result.Decided {
		return ExitEscalated
	}
	return clientcmd.ExitOK
}

// ladder runs one step. The next tier of a seat is the lead, of a lead the owner; an owner-only
// category skips to the owner from any tier. Any question Jev did not answer, or answered below the
// threshold, or an evaluator error, escalates the whole ask: a silently shortened decision is one
// nobody agreed to.
func ladder(ctx context.Context, cfg clientjev.Config, tier, category, summary, detail string, qs []clientjev.Question) (outcome, map[string]clientjev.Selection, error) {
	next := "owner"
	if tier == "seat" {
		next = "lead"
	}
	result := outcome{Tier: tier, Questions: make([]questionResult, 0, len(qs))}
	for _, q := range qs {
		result.Questions = append(result.Questions, questionResult{QuestionID: q.ID, Recommended: q.Recommended()})
	}
	if cfg.OwnerOnly(category) {
		result.EscalateTo, result.Reason = "owner", "owner-only category: "+category
		return result, nil, nil
	}
	picks, err := cfg.SelectChoices(ctx, summary, detail, qs)
	result.Decided = err == nil
	result.Answers = map[string]string{}
	for i, q := range qs {
		pick, found := picks[q.ID]
		result.Questions[i].OptionID, result.Questions[i].Confidence = pick.OptionID, pick.Confidence
		if !found || pick.OptionID == "" || pick.Confidence < cfg.Threshold {
			result.Decided = false
			continue
		}
		result.Answers[q.ID] = pick.OptionID
	}
	if result.Decided {
		return result, picks, nil
	}
	result.Answers = nil
	result.EscalateTo = next
	result.Reason = fmt.Sprintf("confidence below %.2f", cfg.Threshold)
	if err != nil {
		result.Reason = "evaluator error: " + err.Error()
	}
	return result, picks, err
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

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "4genteam decide: %s\n", message)
	_, _ = io.WriteString(stderr, "usage: 4genteam decide --tier seat|lead --summary <text> (--questions <json> | --questions-file <path|->) [--category C] [--detail D]\n")
	return clientcmd.ExitUsage
}
