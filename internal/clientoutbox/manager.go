package clientoutbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// DefaultHTTPClient is the client used for requests.
var DefaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

// Manager orchestrates outbox draining, ledger pulling, and seat wakeups.
type Manager struct {
	Store      *Store
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Runtime    *clientruntime.Client
	OnWake     func(room, seat, text string)
}

// NewManager creates a new ledger outbox manager.
func NewManager(store *Store, baseURL, token string, runtime *clientruntime.Client) *Manager {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8000"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Manager{
		Store:      store,
		BaseURL:    baseURL,
		Token:      token,
		HTTPClient: DefaultHTTPClient,
		Runtime:    runtime,
	}
}

// Drain sends ordered outbox intents to the cloud in one batch.
func (m *Manager) Drain(ctx context.Context) ([]Answer, error) {
	intents := m.Store.OrderedForDrain()
	if len(intents) == 0 {
		return nil, nil
	}

	payload, err := json.Marshal(intents)
	if err != nil {
		return nil, fmt.Errorf("failed to encode intents: %w", err)
	}

	endpoint := m.BaseURL + "/api/v2/ledger/intents"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if m.Token != "" {
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}

	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drain request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("server answered HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var answers []Answer
	if err := json.Unmarshal(bodyBytes, &answers); err != nil {
		return nil, fmt.Errorf("failed to parse server answers: %w", err)
	}

	// Remove processed intents (both accepted and rejected) from store
	var processedIDs []string
	for i, ans := range answers {
		if i < len(intents) {
			intentID := intents[i].ClientEventID
			processedIDs = append(processedIDs, intentID)
			if ans.Rejected {
				// Record/log rejection for seat feedback
				fmt.Printf("[outbox] intent %s (%s) rejected: %s (task_seq: %d)\n",
					intentID, intents[i].Kind, ans.Reason, ans.TaskSeq)
			}
		}
	}

	if err := m.Store.Remove(processedIDs); err != nil {
		return answers, fmt.Errorf("failed to remove drained intents from store: %w", err)
	}

	return answers, nil
}

// Pull fetches new events from the ledger beyond the current cursor.
func (m *Manager) Pull(ctx context.Context, limit int) ([]LedgerEvent, error) {
	if limit <= 0 {
		limit = 100
	}

	cursor := m.Store.Cursor()
	query := url.Values{}
	query.Set("after", fmt.Sprintf("%d", cursor))
	query.Set("limit", fmt.Sprintf("%d", limit))

	endpoint := fmt.Sprintf("%s/api/v2/ledger?%s", m.BaseURL, query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build pull request: %w", err)
	}
	if m.Token != "" {
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}

	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pull request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read ledger response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server answered HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var page LedgerPage
	if err := json.Unmarshal(bodyBytes, &page); err != nil {
		return nil, fmt.Errorf("failed to parse ledger page: %w", err)
	}

	// Advance cursor to the tip or highest user_seq
	maxSeq := cursor
	for _, e := range page.Events {
		if e.UserSeq > maxSeq {
			maxSeq = e.UserSeq
		}
	}
	if page.Tip > maxSeq {
		maxSeq = page.Tip
	}

	if maxSeq > cursor {
		if err := m.Store.SetCursor(maxSeq); err != nil {
			return page.Events, fmt.Errorf("failed to persist new cursor: %w", err)
		}
	}

	// Prefetch resume briefs for active tasks in background (P6)
	seenTasks := make(map[string]bool)
	for _, e := range page.Events {
		if e.TaskID != "" && !seenTasks[e.TaskID] {
			seenTasks[e.TaskID] = true
			go func(tID string) {
				_, _ = m.Prefetch(context.Background(), tID)
			}(e.TaskID)
		}
	}

	return page.Events, nil
}

// WakeSeatsFromEvents examines ledger events and sends wake messages to target seats.
func (m *Manager) WakeSeatsFromEvents(ctx context.Context, events []LedgerEvent) error {
	for _, ev := range events {
		room, seat, wakeText := extractWakeTarget(ev)
		if room == "" || seat == "" || wakeText == "" {
			continue
		}

		if m.OnWake != nil {
			m.OnWake(room, seat, wakeText)
		}

		if m.Runtime != nil {
			_ = m.Runtime.SendTextToSeat(room, seat, wakeText)
		}
	}
	return nil
}

// extractWakeTarget determines if an event should wake a seat and formats the brief.
func extractWakeTarget(ev LedgerEvent) (room, seat, brief string) {
	if ev.Payload == nil {
		return "", "", ""
	}

	// Case 1: Assignee in payload (format: "room/seat" or room + seat keys)
	var targetRoom, targetSeat string
	if rawAssignee, ok := ev.Payload["assignee"].(string); ok && strings.Contains(rawAssignee, "/") {
		parts := strings.SplitN(rawAssignee, "/", 2)
		targetRoom, targetSeat = parts[0], parts[1]
	} else {
		if r, ok := ev.Payload["room"].(string); ok {
			targetRoom = r
		}
		if s, ok := ev.Payload["seat"].(string); ok {
			targetSeat = s
		}
	}

	if targetRoom == "" || targetSeat == "" {
		return "", "", ""
	}

	switch ev.Kind {
	case "assigned":
		brief = fmt.Sprintf("\n[agenthub:wake] Task %s assigned to you. Resume work with: manage_task action=resume task_id=%s\n", ev.TaskID, ev.TaskID)
	case "status_changed":
		newStatus, _ := ev.Payload["status"].(string)
		brief = fmt.Sprintf("\n[agenthub:wake] Task %s moved to %s. Check details: manage_task action=resume task_id=%s\n", ev.TaskID, newStatus, ev.TaskID)
	case "gate_verdict":
		verdict, _ := ev.Payload["verdict"].(string)
		reasons, _ := ev.Payload["reasons"].(string)
		brief = fmt.Sprintf("\n[agenthub:wake] Task %s gate verdict: %s (%s). Please review and proceed.\n", ev.TaskID, verdict, reasons)
	case "context_updated":
		brief = fmt.Sprintf("\n[agenthub:wake] Task %s context updated. Refresh and resume: manage_task action=resume task_id=%s\n", ev.TaskID, ev.TaskID)
	default:
		brief = fmt.Sprintf("\n[agenthub:wake] Task %s event: %s. manage_task action=resume task_id=%s\n", ev.TaskID, ev.Kind, ev.TaskID)
	}

	return targetRoom, targetSeat, brief
}

// Sync runs the full reconnection workflow: drain outbox, pull ledger, and wake seats.
func (m *Manager) Sync(ctx context.Context) ([]Answer, []LedgerEvent, error) {
	answers, err := m.Drain(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("drain failed: %w", err)
	}

	events, err := m.Pull(ctx, 100)
	if err != nil {
		return answers, nil, fmt.Errorf("pull failed: %w", err)
	}

	if err := m.WakeSeatsFromEvents(ctx, events); err != nil {
		return answers, events, fmt.Errorf("wake seats failed: %w", err)
	}

	return answers, events, nil
}

// WorkItem represents a task ready for execution by a seat in a room (O7).
type WorkItem struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Status             string   `json:"status"`
	Assignee           string   `json:"assignee"`
	ClaimedBy          *string  `json:"claimed_by"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Scope              []string `json:"scope"`
}

// FetchWork retrieves open work for a machine from GET /api/v2/machines/{machine}/work.
func (m *Manager) FetchWork(ctx context.Context, machine string) ([]WorkItem, error) {
	endpoint := fmt.Sprintf("%s/api/v2/machines/%s/work", m.BaseURL, machine)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build work request: %w", err)
	}
	if m.Token != "" {
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}

	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch work request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server answered HTTP %d: %s", resp.StatusCode, string(body))
	}

	var work []WorkItem
	if err := json.NewDecoder(resp.Body).Decode(&work); err != nil {
		return nil, fmt.Errorf("failed to parse work response: %w", err)
	}
	return work, nil
}

// WakeSeatsForWork wakes assigned seats for unclaimed open work items (O7).
// Claiming suppresses duplicate wakeups.
func (m *Manager) WakeSeatsForWork(ctx context.Context, machine string, work []WorkItem) error {
	for _, item := range work {
		if item.ClaimedBy != nil && *item.ClaimedBy != "" {
			continue // Claiming suppresses duplicate wakeups!
		}
		parts := strings.SplitN(item.Assignee, "/", 2)
		if len(parts) != 2 {
			continue
		}
		room, seat := parts[0], parts[1]
		wakeText := fmt.Sprintf("\n[agenthub:wake] Work available on task %s (%s). To claim: manage_task action=claim task_id=%s seat=%s/%s\n",
			item.ID, item.Title, item.ID, room, seat)
		if m.OnWake != nil {
			m.OnWake(room, seat, wakeText)
		}
		if m.Runtime != nil {
			_ = m.Runtime.SendTextToSeat(room, seat, wakeText)
		}
	}
	return nil
}
