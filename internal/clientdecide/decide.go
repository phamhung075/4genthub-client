// Package clientdecide is the `4genteam decide` verb: one step of the seat/lead/owner decision ladder.
//
// THE LADDER RUNS ON THE USER'S CLIENT, with Jev (internal/clientjev) like the LLM on a seat: the
// server is not called and holds no Jev. A seat or lead that must choose between options asks Jev
// here; at or above the confidence threshold on every question the tier decides for itself and the
// answer is printed, otherwise the output names the next tier and carries the recommended option so
// the caller re-asks one tier up (`--tier lead`) or raises it to the owner. A category listed as
// owner-only always answers "escalate to owner" by code, without a Jev call, whatever the confidence.
//
// The owner tier is not this verb, but an escalation to it is raised here (route.go): in a room with a
// director office the question goes to the director's ready.md, otherwise it becomes an owner ask (a rail
// row tagged owner-decision with its questions recorded) that the bridge mirrors to the dashboard.
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
	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
	"github.com/phamhung075/4genthub-client/internal/clientjev"
	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// ExitEscalated is the answer "this tier did not decide": the output is valid and names the next
// tier, so it is neither a success nor a failure of the call.
const ExitEscalated = 5

const usage = `usage: 4genteam decide --tier seat|lead --summary <text> (--questions <json> | --questions-file <path|->) [--category C] [--detail D | --detail-file <path>] [--room R] [--distinct-from-owner-ask WHY]

Ask one step of the decision ladder. Jev is asked from this machine; the JSON outcome goes to stdout.

  --tier            REQUIRED. seat or lead: the tier asking.
  --summary         REQUIRED. One line naming the decision.
  --questions       The questions as a JSON array (id, question, options[id,label,recommended]).
  --questions-file  The same array read from a file, or "-" for stdin. One of the two is REQUIRED.
  --category        Optional. A free token; credentials, spend and $DECISION_OWNER_ONLY_CATEGORIES always go to the owner.
  --detail          Optional. Context for the evaluator.
  --detail-file     The same text read from a file: use it for anything with quotes, newlines or a $, which a shell breaks.
  --room            The room asking. Default: the room of this seat's session ($AGENTHUB_SESSION_NAME), else $AGENTHUB_ROOM.
  --distinct-from-owner-ask WHY
                    Lead tier only. A lead ask whose summary rewords one escalated to the owner in the last 7 days
                    is not sent to Jev: it answers escalate_to owner again (exit 5). Give WHY to state that this is
                    a different question; WHY is written into the log entry.

Configuration, from ~/.config/4genthub/.env or the environment: TYPESAFE_API_KEY (required), TYPESAFE_BASE_URL,
JEV_DECISION_MODEL (jev-1.13.0), JEV_DECISION_THRESHOLD (the confidence a tier needs to decide; the value is the one set in the config), JEV_DECISION_LOG_PATH. Every outcome is appended
to the Jev decision log (~/.config/4genthub/jev_decisions.log), with the room.

An escalation whose next tier is the owner is raised by this verb, once:
  - room with a director office (<room dir>/director/): one QUESTION line is appended to director/ready.md and
    escalate_to is "director"; the director answers there and alone raises an owner ask for what it cannot decide.
  - any other room: an owner ask is created on the rail (to human@<room>, tag owner-decision, with the questions),
    which the bridge mirrors to the dashboard's Decision Requests panel; owner_ask names its qitem id.
  - no room can be named: nothing is raised, and stderr says so (raise it with room queue create).
Exit: 0 this tier decided, 5 escalated to the next tier (see escalate_to), 1 the evaluation could not be recorded, 2 usage.
`

// Commands is what the dispatcher registers from this package: one verb, `decide`.
func Commands() []clientcmd.Command { return []clientcmd.Command{decideCommand{}} }

type decideCommand struct{}

func (decideCommand) Name() string    { return "decide" }
func (decideCommand) Summary() string { return "ask one step of the seat/lead/owner decision ladder" }

type options struct {
	tier, category, summary, detail, detailFile, questions, questionsFile string
	room                                                                  string
	// distinct is --distinct-from-owner-ask: the lead's stated reason that this ask is NOT the one already
	// with the owner. It is written into the log entry, so the override is on the record.
	distinct string
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
	// Room is the room the ask was made for, when one could be named.
	Room string `json:"room,omitempty"`
	// OwnerAsk is the qitem id of the owner ask this escalation raised (rooms without a director).
	OwnerAsk string `json:"owner_ask,omitempty"`
	// Ready is the director's ready.md the QUESTION line was appended to (rooms with a director).
	Ready string `json:"ready,omitempty"`
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
	if opts.detail != "" && opts.detailFile != "" {
		return usageError(stderr, "give --detail or --detail-file, not both")
	}
	if opts.detailFile != "" {
		data, err := os.ReadFile(opts.detailFile)
		if err != nil {
			return usageError(stderr, fmt.Sprintf("cannot read --detail-file: %v", err))
		}
		opts.detail = string(data)
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
	questions, err := clientdecisions.ParseQuestions(raw)
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

	var (
		result  outcome
		picks   map[string]clientjev.Selection
		evalErr error
	)
	distinct := strings.TrimSpace(opts.distinct)
	route := resolveRoute(opts.room, os.Getenv)
	reasked := false
	if open, found := openOwnerAsk(cfg.LogPath, time.Now(), summary); opts.tier == "lead" && found && distinct == "" {
		// The owner (or the director) already holds this question: Jev is not asked, and the answer is theirs again.
		holder := strings.TrimPrefix(open.Outcome, "escalated-to-")
		reasked = true
		result = outcome{Tier: opts.tier, EscalateTo: holder, Questions: make([]questionResult, 0, len(questions)),
			Reason: fmt.Sprintf("already escalated to the %s at %s as %q: only the %s's answer settles it. Wait for that answer; if this is a different question, re-run with --distinct-from-owner-ask \"<why>\"",
				holder, open.Timestamp.UTC().Format(time.RFC3339), open.Summary, holder)}
		for _, q := range questions {
			result.Questions = append(result.Questions, questionResult{QuestionID: q.ID, Recommended: q.Recommended()})
		}
		fmt.Fprintf(stderr, "4genteam decide: %s\n", result.Reason)
	} else {
		result, picks, evalErr = ladder(ctx, cfg, opts.tier, category, summary, detail, questions)
	}
	if !result.Decided && result.EscalateTo == "owner" && !reasked && route.director {
		// A director room: the founder is not asked by a seat; the director is.
		result.EscalateTo = "director"
	}
	result.Room = route.room
	if distinct != "" {
		detail = strings.TrimSpace(detail + "\n\n[distinct-from-owner-ask] " + distinct)
	}
	entry := clientjev.LogEntry{
		Timestamp: time.Now(), Kind: "decision", Summary: summary, Detail: detail, Choices: picks, Questions: questions,
		Tier: opts.tier, Category: category, Outcome: "decided", Room: route.room,
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
	if !result.Decided && !reasked && (result.EscalateTo == "owner" || result.EscalateTo == "director") {
		raise(route, opts.tier, summary, detail, questions, &result, stderr)
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
func ladder(ctx context.Context, cfg clientjev.Config, tier, category, summary, detail string, qs []clientdecisions.Question) (outcome, map[string]clientjev.Selection, error) {
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
		case "--detail-file":
			target = &opts.detailFile
		case "--questions":
			target = &opts.questions
		case "--questions-file":
			target = &opts.questionsFile
		case "--room":
			target = &opts.room
		case "--distinct-from-owner-ask":
			target = &opts.distinct
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
	_, _ = io.WriteString(stderr, "usage: 4genteam decide --tier seat|lead --summary <text> (--questions <json> | --questions-file <path|->) [--category C] [--detail D | --detail-file <path>] [--room R]\n")
	return clientcmd.ExitUsage
}
