package clientsync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// TestReadLockMirrorsThePythonBranches pins all three outcomes of agenthub_client/src/agenthub_client/seat_sync.py's read_lock,
// including the two failure ones, because a port that silently drops a refusal is the failure this
// tree keeps finding in the other direction (a refusal that arrives as a success).
func TestReadLockMirrorsThePythonBranches(t *testing.T) {
	dir := t.TempDir()

	t.Run("absent is not an error: the seat was never pulled", func(t *testing.T) {
		lock, err := ReadLock(filepath.Join(dir, "pinned.json"))
		if err != nil {
			t.Fatalf("ReadLock on a missing file returned %v, want no error", err)
		}
		if lock.Hash != "" || lock.Path != "" {
			t.Fatalf("lock = %+v, want the zero value", lock)
		}
	})

	t.Run("a directory is not a file, as the Python's is_file() says", func(t *testing.T) {
		asDir := filepath.Join(dir, "a-directory")
		if err := os.Mkdir(asDir, 0o755); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		lock, err := ReadLock(asDir)
		if err != nil {
			t.Fatalf("ReadLock on a directory returned %v, want no error", err)
		}
		if lock.Hash != "" {
			t.Fatalf("lock = %+v, want the zero value", lock)
		}
	})

	t.Run("a valid lock yields both fields", func(t *testing.T) {
		path := filepath.Join(dir, "valid.json")
		body := `{"hash":"a1b2c3","path":"/tmp/store/lead"}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		lock, err := ReadLock(path)
		if err != nil {
			t.Fatalf("ReadLock: %v", err)
		}
		if lock.Hash != "a1b2c3" || lock.Path != "/tmp/store/lead" {
			t.Fatalf("lock = %+v, want the file's two fields", lock)
		}
	})

	t.Run("invalid JSON exits 2 with Python's message shape", func(t *testing.T) {
		path := filepath.Join(dir, "broken.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := ReadLock(path)
		if err == nil {
			t.Fatal("ReadLock on invalid JSON returned no error")
		}
		if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUsage {
			t.Errorf("code = %d, want %d", code, clientcmd.ExitUsage)
		}
		if want := "cannot read lock file " + path + ":"; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("error = %q, want the Python's prefix %q", err.Error(), want)
		}
	})

	// The malformed check is exactly Python's: a dict whose hash or path is absent or not a string.
	for _, malformed := range []struct {
		name string
		body string
	}{
		{"no hash", `{"path":"/tmp/store/lead"}`},
		{"no path", `{"hash":"a1b2c3"}`},
		{"hash is a number", `{"hash":12,"path":"/tmp/store/lead"}`},
		{"path is null", `{"hash":"a1b2c3","path":null}`},
		{"a JSON list", `["hash","path"]`},
	} {
		t.Run("malformed: "+malformed.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(malformed.name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(malformed.body), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			_, err := ReadLock(path)
			if err == nil {
				t.Fatalf("ReadLock accepted %s, want the malformed refusal", malformed.body)
			}
			if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUsage {
				t.Errorf("code = %d, want %d", code, clientcmd.ExitUsage)
			}
			if _, isCoded := err.(*clientcmd.CodedError); !isCoded {
				t.Fatalf("error %T is not a CodedError, so a caller cannot read its code", err)
			}
		})
	}

	t.Run("an unreadable file exits 2", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: permissions do not deny the read")
		}
		path := filepath.Join(dir, "unreadable.json")
		if err := os.WriteFile(path, []byte(`{"hash":"a1b2c3","path":"/tmp/store/lead"}`), 0o000); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := ReadLock(path)
		var coded *clientcmd.CodedError
		if !errors.As(err, &coded) || coded.Code != clientcmd.ExitUsage {
			t.Fatalf("err = %v, want a CodedError with code %d", err, clientcmd.ExitUsage)
		}
	})
}
