// The pinned-snapshot reader. A seat's snapshot is pinned by hash; the comparison against the hash
// the cloud would serve now lived in the removed `sync status` verb, and the readers that are left
// here - `sync pull` and `sync bundle` - are what resolve the pin.
package clientsync

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// Lock is the pinned.json a seat is pinned by: the hash of the snapshot it was pulled to, and the path
// that snapshot was materialized into. BOTH ARE REQUIRED STRINGS, which is the whole of the retired
// Python's malformed check - read_lock validates `hash` and `path` and nothing else, so a lock with a
// number for either is malformed there and here.
type Lock struct {
	Hash string `json:"hash"`
	Path string `json:"path"`
}

// ReadLock ports agenthub_client/src/agenthub_client/seat_sync.py's read_lock, and its three outcomes are all load-bearing - the
// two failure branches are exactly the kind a port drops silently:
//
//	no file (or a directory)      -> the zero Lock and no error: the seat was never pulled, which
//	                                 `status` renders as "not pulled". A DIRECTORY counts as absent
//	                                 because the Python's is_file() is false for one, and a stat that
//	                                 fails for any other reason is treated the same way, as is_file()
//	                                 swallows it.
//	unreadable, or invalid JSON   -> exit 2 with the Python's message shape ("cannot read lock file
//	                                 <path>: <cause>"). The cause is the Go error's own text, which is
//	                                 the one place the wording cannot match Python's verbatim.
//	hash or path absent or not a string -> exit 2 with the Python's "lock file <path> is malformed".
//
// The Python raises these as SyncError with EXIT_USAGE, so they arrive here as CodedError; callers read
// the code with clientcmd.CodeOf rather than deciding it again.
func ReadLock(path string) (Lock, error) {
	var lock Lock
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return lock, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return lock, lockUnreadable(path, err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return lock, lockUnreadable(path, err)
	}
	hash, hashIsString := probe["hash"].(string)
	materializedPath, pathIsString := probe["path"].(string)
	if !hashIsString || !pathIsString {
		return lock, &clientcmd.CodedError{
			Message: fmt.Sprintf("lock file %s is malformed", path),
			Code:    clientcmd.ExitUsage,
		}
	}
	return Lock{Hash: hash, Path: materializedPath}, nil
}

func lockUnreadable(path string, cause error) error {
	return &clientcmd.CodedError{
		Message: fmt.Sprintf("cannot read lock file %s: %v", path, cause),
		Code:    clientcmd.ExitUsage,
	}
}
