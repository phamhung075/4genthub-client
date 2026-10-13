package clientoutbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPrefetchAndCache(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tmpDir)

	storePath := filepath.Join(tmpDir, "outbox.json")
	store, _ := NewStore(storePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/tasks/task-abc/resume" {
			data := map[string]any{
				"task_id": "task-abc",
				"brief": map[string]any{
					"criteria": []any{"clean build", "tests pass"},
					"blockers": []any{},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(data)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	mgr := NewManager(store, server.URL, "test-token", nil)

	// Prefetch the task brief
	brief, err := mgr.Prefetch(context.Background(), "task-abc")
	if err != nil {
		t.Fatalf("Prefetch failed: %v", err)
	}

	if brief.TaskID != "task-abc" {
		t.Errorf("Expected task-abc, got %s", brief.TaskID)
	}

	// Read from disk cache without network
	cached, err := GetCachedBrief("task-abc")
	if err != nil {
		t.Fatalf("GetCachedBrief failed: %v", err)
	}

	if cached.TaskID != "task-abc" {
		t.Errorf("Expected cached task-abc, got %s", cached.TaskID)
	}
}
