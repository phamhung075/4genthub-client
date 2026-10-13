package clientoutbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Store handles persistent on-disk storage of the client cursor and outbox intents.
type Store struct {
	mu   sync.Mutex
	path string
	data State
}

// NewStore loads or initializes the store from path.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.Load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load outbox store from %s: %w", path, err)
		}
		s.data = State{Cursor: 0, Intents: []Intent{}}
	}
	return s, nil
}

// Load reads the state from disk.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to parse outbox state: %w", err)
	}
	s.data = state
	if s.data.Intents == nil {
		s.data.Intents = []Intent{}
	}
	return nil
}

// Save writes the state atomically to disk.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory for outbox: %w", err)
	}

	bytes, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal outbox state: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0o644); err != nil {
		return fmt.Errorf("failed to write tmp outbox state: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("failed to atomic replace outbox state: %w", err)
	}
	return nil
}

// Cursor returns the current cursor.
func (s *Store) Cursor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Cursor
}

// SetCursor updates and saves the cursor.
func (s *Store) SetCursor(cursor int64) error {
	s.mu.Lock()
	s.data.Cursor = cursor
	s.mu.Unlock()
	return s.Save()
}

// Intents returns a copy of pending intents.
func (s *Store) Intents() []Intent {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Intent, len(s.data.Intents))
	copy(cp, s.data.Intents)
	return cp
}

// Enqueue adds an intent to the outbox and saves to disk.
func (s *Store) Enqueue(intent Intent) error {
	s.mu.Lock()
	s.data.Intents = append(s.data.Intents, intent)
	s.mu.Unlock()
	return s.Save()
}

// Remove removes the intents matching the given clientEventIDs.
func (s *Store) Remove(clientEventIDs []string) error {
	s.mu.Lock()
	removeMap := make(map[string]bool, len(clientEventIDs))
	for _, id := range clientEventIDs {
		removeMap[id] = true
	}

	remaining := make([]Intent, 0, len(s.data.Intents))
	for _, item := range s.data.Intents {
		if !removeMap[item.ClientEventID] {
			remaining = append(remaining, item)
		}
	}
	s.data.Intents = remaining
	s.mu.Unlock()
	return s.Save()
}

// OrderedForDrain returns pending intents ordered by:
// 1. Facts first (chronological by CreatedAt)
// 2. Decisions second (chronological by CreatedAt)
func (s *Store) OrderedForDrain() []Intent {
	s.mu.Lock()
	defer s.mu.Unlock()

	var facts []Intent
	var decisions []Intent
	var others []Intent

	for _, it := range s.data.Intents {
		if IsFact(it.Kind) {
			facts = append(facts, it)
		} else if IsDecision(it.Kind) {
			decisions = append(decisions, it)
		} else {
			others = append(others, it)
		}
	}

	sort.Slice(facts, func(i, j int) bool {
		return facts[i].CreatedAt.Before(facts[j].CreatedAt)
	})
	sort.Slice(decisions, func(i, j int) bool {
		return decisions[i].CreatedAt.Before(decisions[j].CreatedAt)
	})

	result := make([]Intent, 0, len(s.data.Intents))
	result = append(result, facts...)
	result = append(result, decisions...)
	result = append(result, others...)
	return result
}
