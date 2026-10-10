// The pull path: fetch one resolved seat, write its snapshot under a directory named by its hash, and pin
// the seat to that hash. The pin is what the rest of the client trusts, so everything here is either
// idempotent or refuses: a hash directory is never rewritten, and a pin whose directory is gone is a loud
// failure rather than a silent re-fetch.
package clientsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// FetchSeat ports fetch_seat: GET {SEATS_PATH}/{room}/{seat}, then the `success is not True` check and the
// `resolved_seat` shape check, each with the Python's message at EXIT_REMOTE.
func FetchSeat(ctx context.Context, baseURL, token, room, seat string) (map[string]any, error) {
	path := PathsPath + "/" + room + "/" + seat
	body, err := RequestJSON(ctx, "GET", baseURL, token, path, nil)
	if err != nil {
		return nil, err
	}
	if success, isBool := body["success"].(bool); !isBool || !success {
		return nil, remoteFailure("GET", path, "returned an error response")
	}
	resolved, ok := body["resolved_seat"].(map[string]any)
	if !ok {
		return nil, remoteFailure("GET", path, "response has no resolved_seat")
	}
	return resolved, nil
}

// Materialize ports materialize: an EXISTING hash directory is left exactly as it is - that immutability
// is what lets a pull adopt a new snapshot while the previous one survives for whatever is still running
// on it - and a new one is written through a staging directory and renamed into place, so a failure
// halfway through leaves no half-written snapshot under a name anything could trust.
func Materialize(hashDir string, entries []FileEntry) error {
	if info, err := os.Stat(hashDir); err == nil && info.IsDir() {
		return nil
	}
	parent := filepath.Dir(hashDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(hashDir)+".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		destination := filepath.Join(append([]string{staging}, entry.Parts...)...)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			removeAllQuiet(staging)
			return err
		}
		if err := os.WriteFile(destination, []byte(entry.Content), 0o644); err != nil {
			removeAllQuiet(staging)
			return err
		}
	}
	if err := os.Rename(staging, hashDir); err != nil {
		removeAllQuiet(staging)
		return err
	}
	return nil
}

func removeAllQuiet(path string) {
	_ = os.RemoveAll(path)
}

// WriteJSONAtomic ports write_json_atomic: the parent is created, the payload is written to a temporary
// file in that same parent (so the rename cannot cross a filesystem), and it is renamed onto the target.
// A reader therefore sees the old file or the new one and never a partial one - which matters for
// pinned.json, the file the whole client's decisions read.
//
// THE ENCODING IS THE PYTHON'S: indent=2 and sort_keys=True with a trailing newline. Go marshals a map in
// sorted key order, so passing a map gets the same bytes; the lock is written as a map for that reason
// rather than as a struct whose field order would decide it.
func WriteJSONAtomic(path string, payload any) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(parent, "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	return nil
}

// RunPullVerb is `sync pull <room> <seat>`: two positionals, --out with the seat-store default, and
// --update to adopt a newer snapshot instead of only being told about it. On success it prints exactly
// "path:<dir>" - the directory now pinned - which is the line the Python prints and the one a caller
// reads to find the materialized snapshot.
func RunPullVerb(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	room, seat, out, update, err := parsePullArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return clientcmd.ExitUsage
	}
	baseURL, err := RequireEnv("AGENTHUB_URL")
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return clientcmd.ExitUsage
	}
	token, err := RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return clientcmd.ExitUsage
	}
	dir, err := PullSeat(ctx, baseURL, token, room, seat, out, update, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return clientFailureCode(err)
	}
	fmt.Fprintf(stdout, "path:%s\n", dir)
	return clientcmd.ExitOK
}

// parsePullArgs is the Python's subparser for `pull`: TWO positionals (room then seat), --out, --update.
func parsePullArgs(args []string) (room, seat, out string, update bool, err error) {
	out = DefaultSeatStore()
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--out":
			if i+1 >= len(args) {
				return "", "", "", false, usageError("--out needs a value")
			}
			i++
			out = args[i]
		case strings.HasPrefix(arg, "--out="):
			out = strings.TrimPrefix(arg, "--out=")
		case arg == "--update":
			update = true
		case strings.HasPrefix(arg, "-"):
			return "", "", "", false, usageError("unknown argument " + arg)
		default:
			switch {
			case room == "":
				room = arg
			case seat == "":
				seat = arg
			default:
				return "", "", "", false, usageError("unexpected argument " + arg + " (pull takes a room and a seat)")
			}
		}
	}
	if room == "" || seat == "" {
		return "", "", "", false, usageError("pull needs a room and a seat")
	}
	return room, seat, out, update, nil
}

// PullSeat ports pull_seat and returns the directory of the snapshot the seat is pinned to now.
//
// The lock is the pin, and its three outcomes are the point of the function:
//
//	no lock at all            -> write the snapshot, write the lock;
//	a different cloud hash WITHOUT update -> the pinned snapshot must still be there (a missing one is
//	                             EXIT_USAGE and says so, refusing a silent fallback) and the newer hash
//	                             is only ANNOUNCED, on stderr, because adopting it is a separate decision;
//	update                    -> write the new snapshot and move the pin, leaving the previous snapshot
//	                             untouched for whatever is still running on it.
func PullSeat(ctx context.Context, baseURL, token, room, seat, out string, update bool, stderr io.Writer) (string, error) {
	resolved, err := FetchSeat(ctx, baseURL, token, room, seat)
	if err != nil {
		return "", err
	}
	if resolvedRoom, _ := resolved["room"].(string); resolvedRoom != room {
		return "", remoteFailure("GET", PathsPath+"/"+room+"/"+seat, "returned a snapshot for a different room/seat")
	}
	if resolvedSeat, _ := resolved["seat"].(string); resolvedSeat != seat {
		return "", remoteFailure("GET", PathsPath+"/"+room+"/"+seat, "returned a snapshot for a different room/seat")
	}
	hashValue, _ := resolved["hash"].(string)
	fetchedHash, err := ValidateHash(hashValue)
	if err != nil {
		return "", err
	}
	entries, err := ExtractFiles(resolved)
	if err != nil {
		return "", err
	}
	policy, ok := resolved["policy"].(map[string]any)
	if !ok {
		policy = map[string]any{}
	}

	seatDir := filepath.Join(out, room, seat)
	if err := os.MkdirAll(seatDir, 0o755); err != nil {
		return "", err
	}
	lockPath := filepath.Join(seatDir, "pinned.json")

	if lockPresent(lockPath) {
		lock, err := ReadLock(lockPath)
		if err != nil {
			return "", err
		}
		if !update && fetchedHash != lock.Hash {
			pinnedHash, err := ValidateHash(lock.Hash)
			if err != nil {
				return "", err
			}
			pinnedDir := filepath.Join(seatDir, pinnedHash)
			if info, statErr := os.Stat(pinnedDir); statErr != nil || !info.IsDir() {
				return "", &clientcmd.CodedError{
					Message: fmt.Sprintf(
						"pinned seat directory is missing: %s (refusing silent fallback; fix the store or remove %s)",
						pinnedDir, lockPath),
					Code: clientcmd.ExitUsage,
				}
			}
			fmt.Fprintf(stderr, "newer snapshot available: %s (run with --update to adopt)\n", fetchedHash)
			return pinnedDir, nil
		}
	}

	hashDir := filepath.Join(seatDir, fetchedHash)
	if err := Materialize(hashDir, entries); err != nil {
		return "", err
	}
	if err := WriteJSONAtomic(filepath.Join(seatDir, "policy.json"), policy); err != nil {
		return "", err
	}
	// The lock is written when there was none, when the hash moved, or when update was asked for - which
	// is the Python's condition, and it keeps a re-pull of the same hash from rewriting the file.
	writeLock := true
	if lockPresent(lockPath) {
		if lock, err := ReadLock(lockPath); err == nil {
			writeLock = lock.Hash != fetchedHash || update
		}
	}
	if writeLock {
		if err := WriteJSONAtomic(lockPath, map[string]any{"hash": fetchedHash, "path": hashDir}); err != nil {
			return "", err
		}
	}
	return hashDir, nil
}
