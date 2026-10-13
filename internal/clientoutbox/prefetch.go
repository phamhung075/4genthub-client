package clientoutbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ResumeBrief is the structured task brief returned by the server and cached locally.
type ResumeBrief struct {
	TaskID    string         `json:"task_id"`
	Brief     map[string]any `json:"brief"`
	FetchedAt time.Time      `json:"fetched_at"`
}

// DefaultCacheDir returns the default cache directory for task briefs and post-compaction packs.
func DefaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "4genthub", "cache", "resumes"), nil
}

// Prefetch fetches the resume brief for a task from the cloud and caches it on disk.
func (m *Manager) Prefetch(ctx context.Context, taskID string) (*ResumeBrief, error) {
	if taskID == "" {
		return nil, fmt.Errorf("taskID cannot be empty")
	}

	endpoint := fmt.Sprintf("%s/api/v2/tasks/%s/resume", m.BaseURL, taskID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build prefetch request: %w", err)
	}
	if m.Token != "" {
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}

	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prefetch request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read resume response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server answered HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var rawBrief map[string]any
	if err := json.Unmarshal(bodyBytes, &rawBrief); err != nil {
		return nil, fmt.Errorf("failed to parse resume brief: %w", err)
	}

	brief := &ResumeBrief{
		TaskID:    taskID,
		Brief:     rawBrief,
		FetchedAt: time.Now().UTC(),
	}

	// Persist to disk cache
	if err := SaveCachedBrief(brief); err != nil {
		// Non-fatal, still return the fetched brief
		fmt.Printf("[prefetch] warning: failed to write cache for task %s: %v\n", taskID, err)
	}

	return brief, nil
}

// SaveCachedBrief persists the brief to the local cache directory.
func SaveCachedBrief(brief *ResumeBrief) error {
	dir, err := DefaultCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(brief, "", "  ")
	if err != nil {
		return err
	}

	target := filepath.Join(dir, brief.TaskID+".json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// GetCachedBrief reads a previously cached resume brief from disk without any network call.
func GetCachedBrief(taskID string) (*ResumeBrief, error) {
	dir, err := DefaultCacheDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, taskID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var brief ResumeBrief
	if err := json.Unmarshal(data, &brief); err != nil {
		return nil, err
	}
	return &brief, nil
}
