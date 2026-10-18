// Package clientjev is the user's client talking to Jev, the TypeSafe AI System One evaluator.
//
// JEV RUNS ON THE USER'S MACHINE, LIKE THE LLM ON A SEAT. The server stores asks and tasks and never
// calls Jev: the key lives in the client's env file (~/.config/4genthub/.env), the call leaves from
// this machine, and every outcome is appended to a log on this machine. Two verbs use it:
// `4genteam decide` (choice questions, the decision ladder) and `4genteam complete` (acceptance
// criteria, the completion gate).
package clientjev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

const (
	defaultBaseURL   = "https://api.typesafe.ai/v1"
	defaultModel     = "jev-1.13.0"
	defaultThreshold = 0.8
	logFileName      = "jev_decisions.log"
)

// builtinOwnerOnly are the categories that always go to the owner, whatever Jev's confidence: its
// confidence measures how concentrated an answer is, not whether the action is safe.
var builtinOwnerOnly = []string{"credentials", "spend"}

// ErrNoKey is the refusal when no TypeSafe key is configured.
var ErrNoKey = errors.New("no TypeSafe key: set TYPESAFE_API_KEY (or JEV_API_KEY / JEV_TOKEN) in ~/.config/4genthub/.env or the environment")

// HTTPClient is the HTTP seam; a test replaces it so no request leaves the process.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// Config is Jev as this client is set up to use it, read from the environment per call.
type Config struct {
	APIKey    string
	BaseURL   string
	Model     string
	Threshold float64
	LogPath   string
	ownerOnly map[string]bool
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// FromEnv reads the configuration. A malformed threshold is an error, never a silent default.
func FromEnv() (Config, error) {
	cfg := Config{
		APIKey:    firstEnv("TYPESAFE_API_KEY", "JEV_API_KEY", "JEV_TOKEN"),
		BaseURL:   strings.TrimRight(firstEnv("TYPESAFE_BASE_URL"), "/"),
		Model:     firstEnv("JEV_DECISION_MODEL"),
		Threshold: defaultThreshold,
		ownerOnly: map[string]bool{},
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if raw := firstEnv("JEV_DECISION_THRESHOLD"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v > 1 {
			return Config{}, fmt.Errorf("JEV_DECISION_THRESHOLD=%q is not a number in (0, 1]", raw)
		}
		cfg.Threshold = v
	}
	path, err := logPath(firstEnv("JEV_DECISION_LOG_PATH"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogPath = path
	for _, name := range builtinOwnerOnly {
		cfg.ownerOnly[name] = true
	}
	for _, name := range strings.Split(os.Getenv("DECISION_OWNER_ONLY_CATEGORIES"), ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			cfg.ownerOnly[name] = true
		}
	}
	return cfg, nil
}

func logPath(configured string) (string, error) {
	if configured != "" && !strings.HasPrefix(configured, "~/") {
		return configured, nil
	}
	home, err := clientenv.RealHome()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the home directory for the Jev log: %v", err)
	}
	if configured != "" {
		return filepath.Join(home, strings.TrimPrefix(configured, "~/")), nil
	}
	return filepath.Join(clientenv.StateSubdir(home, ""), logFileName), nil
}

// OwnerOnly reports whether the category always goes to the owner.
func (c Config) OwnerOnly(category string) bool {
	return c.ownerOnly[strings.ToLower(strings.TrimSpace(category))]
}

// Selection is Jev's pick for one choice question.
type Selection struct {
	OptionID   string  `json:"selected_option_id"`
	Confidence float64 `json:"confidence"`
}

// Verdict is the outcome of a criteria check.
type Verdict string

const (
	Accept    Verdict = "accept"
	Reject    Verdict = "reject"
	Uncertain Verdict = "uncertain"
	// Unjudgeable is the completion gate's refusal of a criterion that names no fact the evidence bundle
	// carries. It is NOT one of the judge's answers - the judge is never asked about such a criterion - and
	// it is not a low score: a claim the gate cannot see and a claim it can see and finds weak are different
	// facts about the work, and they must not arrive as the same number
	// (seats/architect/RULING-closing-gate-register-and-unjudgeable.md, R1/R2).
	Unjudgeable Verdict = "unjudgeable"
)

type answer struct {
	Noul       *float64 `json:"noul"`
	Choice     *string  `json:"choice"`
	Confidence *float64 `json:"confidence"`
}

// call posts one System One request and returns its answers.
func (c Config) call(ctx context.Context, questions map[string]any, state any) (map[string]answer, error) {
	if c.APIKey == "" {
		return nil, ErrNoKey
	}
	payload, err := redactPayload(map[string]any{"model": c.Model, "questions": questions, "state": state})
	if err != nil {
		return nil, fmt.Errorf("cannot encode the request: %w", err)
	}
	// THE OWNER'S COUNTS-ONLY RULE (O9a) is applied above, at the one place the payload is built, so
	// every question this client sends - decide and complete/gate alike - leaves with its file paths
	// and test names replaced. A caller that names one in a criterion cannot bypass it, because the
	// shaping is here rather than in the caller; the local decision log still records the full text
	// (LogEntry.Questions).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/systemone", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("cannot build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the request to TypeSafe failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot read the TypeSafe answer: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("TypeSafe answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Answers map[string]answer `json:"answers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("the TypeSafe answer is not JSON: %w", err)
	}
	return parsed.Answers, nil
}

// SelectChoices asks one choice question per HumanQuestion and returns the pick for each answered one.
func (c Config) SelectChoices(ctx context.Context, summary, detail string, qs []Question) (map[string]Selection, error) {
	questions := make(map[string]any, len(qs))
	for _, q := range qs {
		criteria := make(map[string]string, len(q.Options))
		for _, option := range q.Options {
			criteria[option.ID] = option.Label
		}
		questions[q.ID] = map[string]any{"type": "choice", "instructions": q.Question, "criteria": criteria}
	}
	answers, err := c.call(ctx, questions, map[string]string{"summary": summary, "detail": detail})
	if err != nil {
		return nil, err
	}
	picks := make(map[string]Selection, len(answers))
	for id, a := range answers {
		if a.Choice == nil {
			continue
		}
		pick := Selection{OptionID: *a.Choice}
		if a.Confidence != nil {
			pick.Confidence = *a.Confidence
		}
		picks[id] = pick
	}
	return picks, nil
}

// EvaluateCriteria asks whether each acceptance criterion holds for the evidence. Any answer at or
// below 0.3 rejects, all at or above the configured threshold accept, anything else is uncertain; a missing answer or an
// error is never an accept.
func (c Config) EvaluateCriteria(ctx context.Context, criteria []string, evidence any) (Verdict, string, error) {
	questions := make(map[string]any, len(criteria))
	for i, criterion := range criteria {
		questions[fmt.Sprintf("criterion_%d", i+1)] = map[string]any{"type": "noul", "instructions": criterion}
	}
	state, err := countsState(evidence)
	if err != nil {
		return Uncertain, err.Error(), err
	}
	answers, err := c.call(ctx, questions, state)
	if err != nil {
		return Uncertain, err.Error(), err
	}
	if len(answers) < len(criteria) {
		return Uncertain, fmt.Sprintf("Jev answered %d of %d criteria", len(answers), len(criteria)), nil
	}
	allHigh, anyLow := true, false
	parts := make([]string, 0, len(answers))
	for id, a := range answers {
		value := 0.0
		if a.Noul != nil {
			value = *a.Noul
		}
		parts = append(parts, fmt.Sprintf("%s: %.2f", id, value))
		allHigh = allHigh && value >= c.Threshold
		anyLow = anyLow || value <= 0.3
	}
	sort.Strings(parts)
	reasons := strings.Join(parts, "; ")
	switch {
	case anyLow:
		return Reject, "criteria rejected (" + reasons + ")", nil
	case allHigh:
		return Accept, "criteria verified (" + reasons + ")", nil
	}
	return Uncertain, "marginal criteria confidence (" + reasons + ")", nil
}

// LogEntry is one line of the Jev decision log.
type LogEntry struct {
	Timestamp           time.Time            `json:"timestamp"`
	Kind                string               `json:"kind"`
	Model               string               `json:"model"`
	ConfidenceThreshold float64              `json:"confidence_threshold"`
	Summary             string               `json:"summary"`
	Detail              string               `json:"detail,omitempty"`
	Choices             map[string]Selection `json:"choices,omitempty"`
	// Questions keeps the options each pick was made from, so a logged decision can be audited later.
	Questions []Question `json:"questions,omitempty"`
	Tier      string     `json:"tier,omitempty"`
	Category  string     `json:"category,omitempty"`
	Outcome   string     `json:"outcome"`
	Error     string     `json:"error,omitempty"`
}

// Log appends the entry as one JSON line, so the log is a benchmark of what Jev decided and how.
func (c Config) Log(entry LogEntry) error {
	entry.Timestamp = entry.Timestamp.UTC()
	entry.Model, entry.ConfidenceThreshold = c.Model, c.Threshold
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(c.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.Write(append(line, '\n'))
	return err
}
