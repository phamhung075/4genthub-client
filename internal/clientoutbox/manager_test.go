package clientoutbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistenceAndOrdering(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "outbox.json")

	store, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	t0 := time.Now()
	// Enqueue a decision first
	baseSeq := 5
	err = store.Enqueue(Intent{
		ClientEventID: "dec-1",
		TaskID:        "task-1",
		Kind:          "claimed",
		BaseSeq:       &baseSeq,
		Payload:       map[string]any{"seat": "dev"},
		CreatedAt:     t0.Add(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Enqueue decision failed: %v", err)
	}

	// Enqueue an earlier fact
	err = store.Enqueue(Intent{
		ClientEventID: "fact-1",
		TaskID:        "task-1",
		Kind:          "progress",
		Payload:       map[string]any{"note": "working"},
		CreatedAt:     t0,
	})
	if err != nil {
		t.Fatalf("Enqueue fact failed: %v", err)
	}

	// Verify Ordering: Facts must come first!
	ordered := store.OrderedForDrain()
	if len(ordered) != 2 {
		t.Fatalf("Expected 2 intents, got %d", len(ordered))
	}
	if ordered[0].ClientEventID != "fact-1" {
		t.Errorf("Expected fact-1 to come first, got %s", ordered[0].ClientEventID)
	}
	if ordered[1].ClientEventID != "dec-1" {
		t.Errorf("Expected dec-1 to come second, got %s", ordered[1].ClientEventID)
	}

	// Set cursor
	if err := store.SetCursor(42); err != nil {
		t.Fatalf("SetCursor failed: %v", err)
	}

	// Reload from disk (simulating restart)
	store2, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("Reloading store failed: %v", err)
	}

	if store2.Cursor() != 42 {
		t.Errorf("Cursor did not survive restart: expected 42, got %d", store2.Cursor())
	}
	if len(store2.Intents()) != 2 {
		t.Errorf("Intents did not survive restart: expected 2, got %d", len(store2.Intents()))
	}
}

func TestManagerDrainAndPull(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "outbox.json")
	store, _ := NewStore(storePath)

	_ = store.Enqueue(Intent{
		ClientEventID: "evt-uuid-1",
		TaskID:        "task-100",
		Kind:          "progress",
		Payload:       map[string]any{"content": "done"},
		CreatedAt:     time.Now(),
	})

	// Mock server handling /api/v2/ledger/intents and /api/v2/ledger
	drainCalled := 0
	pullCalled := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/ledger/intents":
			drainCalled++
			body, _ := io.ReadAll(r.Body)
			var intents []Intent
			_ = json.Unmarshal(body, &intents)

			answers := []Answer{
				{
					Accepted: true,
					UserSeq:  10,
					Seq:      1,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(answers)

		case "/api/v2/ledger":
			pullCalled++
			after := r.URL.Query().Get("after")
			if after != "0" {
				t.Errorf("Expected after=0, got %s", after)
			}

			page := LedgerPage{
				Success: true,
				Count:   1,
				After:   0,
				Tip:     15,
				Events: []LedgerEvent{
					{
						ID:        "ev-1",
						TaskID:    "task-200",
						Seq:       2,
						UserSeq:   15,
						Kind:      "assigned",
						ActorKind: "human",
						ActorID:   "user-1",
						Payload:   map[string]any{"assignee": "4genthub-min/lead"},
						CreatedAt: time.Now().Format(time.RFC3339),
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(page)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mgr := NewManager(store, server.URL, "test-token", nil)
	var wokenRoom, wokenSeat, wokenText string
	mgr.OnWake = func(r, s, text string) {
		wokenRoom = r
		wokenSeat = s
		wokenText = text
	}

	answers, events, err := mgr.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if drainCalled != 1 {
		t.Errorf("Expected drain to be called once, got %d", drainCalled)
	}
	if len(answers) != 1 || !answers[0].Accepted {
		t.Errorf("Unexpected answers: %+v", answers)
	}
	if len(store.Intents()) != 0 {
		t.Errorf("Outbox was not cleared after successful drain: %d left", len(store.Intents()))
	}

	if pullCalled != 1 {
		t.Errorf("Expected pull to be called once, got %d", pullCalled)
	}
	if len(events) != 1 || events[0].TaskID != "task-200" {
		t.Errorf("Unexpected pulled events: %+v", events)
	}
	if store.Cursor() != 15 {
		t.Errorf("Cursor was not updated to 15, got %d", store.Cursor())
	}

	// Verify seat wakeup
	if wokenRoom != "4genthub-min" || wokenSeat != "lead" {
		t.Errorf("Unexpected wake target: %s/%s", wokenRoom, wokenSeat)
	}
	if wokenText == "" {
		t.Errorf("Expected wake text, got empty string")
	}

	// Second sync should not drain anything because outbox is empty
	answers2, err := mgr.Drain(context.Background())
	if err != nil {
		t.Fatalf("Second drain failed: %v", err)
	}
	if len(answers2) != 0 {
		t.Errorf("Expected 0 answers on empty drain, got %d", len(answers2))
	}
}

func TestManagerRejection(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "outbox.json")
	store, _ := NewStore(storePath)

	_ = store.Enqueue(Intent{
		ClientEventID: "evt-stale-1",
		TaskID:        "task-300",
		Kind:          "claimed",
		Payload:       map[string]any{"seat": "go-dev"},
		CreatedAt:     time.Now(),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answers := []Answer{
			{
				Rejected: true,
				Reason:   "stale_base_seq",
				TaskSeq:  5,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answers)
	}))
	defer server.Close()

	mgr := NewManager(store, server.URL, "test-token", nil)
	answers, err := mgr.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain failed: %v", err)
	}

	if len(answers) != 1 || !answers[0].Rejected {
		t.Errorf("Expected rejected answer, got: %+v", answers)
	}
	if len(store.Intents()) != 0 {
		t.Errorf("Rejected intent should be removed from outbox: %d left", len(store.Intents()))
	}
}
