// The `sync status <room>` verb: the wiring that turns the two halves into the line an operator reads.
package clientsync

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// resolveSeatStore is the ONE resolution the sync verbs use: the --out flag when given, else the
// machine's store under the account home (clientenv.ResolveSeatStore). It is a var so a test can point
// the store at a scratch tree: the store no longer follows $HOME, so t.Setenv("HOME", ...) can no longer
// redirect it, exactly as the Python tests monkeypatch DEFAULT_OUT rather than the environment.
var resolveSeatStore = clientenv.ResolveSeatStore

// RequireEnv ports agenthub_client/src/agenthub_client/seat_sync.py's require_env: an unset or empty variable is a usage error with
// the Python's message ("<name> is not set"), NOT a silent default - which is why the URL and the token
// are read through it rather than with a fallback.
func RequireEnv(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("%s is not set", name),
			Code:    clientcmd.ExitUsage,
		}
	}
	return value, nil
}

// PinnedHashes is pinned_hashes: seat -> the hash its local lock carries, for the seats the cloud listed.
//
// ABSENCE IS THE PYTHON'S None: a seat with no lock file is missing from the map (there is nothing to
// say about it), and that is what the status core renders "not pulled". A lock that EXISTS contributes
// its hash even when that hash is empty, because the Python's `lock["hash"] if lock else None` takes the
// hash whenever the lock object exists - so an empty one reaches the status line as a pin and renders
// BEHIND rather than as a seat nobody ever pulled.
func PinnedHashes(out, room string, order []string) (map[string]string, error) {
	pins := make(map[string]string, len(order))
	for _, seat := range order {
		path := filepath.Join(out, room, seat, "pinned.json")
		if !lockPresent(path) {
			continue
		}
		lock, err := ReadLock(path)
		if err != nil {
			return nil, err
		}
		pins[seat] = lock.Hash
	}
	return pins, nil
}

func lockPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// statusUsage is what `4genteam sync status --help` prints.
const statusUsage = `usage: 4genteam sync status ROOM [--seat SEAT ...] [--out DIR]

Compare each of the room's seats' LOCAL pin with the hash the cloud serves, and report the ones that
are behind. Reading only: nothing is written and nothing is pulled.
`

// RunStatusVerb is `sync status <room>`: the arguments, the two halves, the table and the exit code.
//
// THE ERROR CODE MAPPING IS THE PYTHON MAIN'S, NOT THE HELPER'S, and that is the detail a port gets
// wrong: the helpers raise SyncError carrying EXIT_REMOTE (1), and the client's main() then maps
// anything that is not EXIT_USAGE (2) to EXIT_FAILED (3). So a cloud that cannot be read exits 3 here,
// not 1 - clientcmd.CodeOf would return the helper's own code, which is right for the bridge (it does
// report ExitRemote) and wrong for this verb.

func RunStatusVerb(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, statusUsage)
		return clientcmd.ExitOK
	}
	room, out, wanted, err := parseStatusArgs(args)
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

	order, cloud, err := CloudHashes(ctx, baseURL, token, room)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return clientFailureCode(err)
	}
	if len(wanted) > 0 {
		order, cloud = filterSeats(order, cloud, wanted)
	}
	pins, err := PinnedHashes(out, room, order)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return clientFailureCode(err)
	}
	return RunStatus(stdout, order, cloud, pins)
}

// clientFailureCode is the Python's main: EXIT_USAGE stays EXIT_USAGE, everything else is EXIT_FAILED -
// which is 3, this client's ExitUnavailable.
func clientFailureCode(err error) int {
	if clientcmd.CodeOf(err, clientcmd.ExitUnavailable) == clientcmd.ExitUsage {
		return clientcmd.ExitUsage
	}
	return clientcmd.ExitUnavailable
}

func filterSeats(order []string, cloud map[string]string, wanted []string) ([]string, map[string]string) {
	keep := map[string]bool{}
	for _, seat := range wanted {
		keep[seat] = true
	}
	filtered := make([]string, 0, len(order))
	hashes := make(map[string]string, len(order))
	for _, seat := range order {
		if !keep[seat] {
			continue
		}
		filtered = append(filtered, seat)
		hashes[seat] = cloud[seat]
	}
	return filtered, hashes
}

// parseStatusArgs is the Python's subparser for `status`: a positional room, `--out` with the seat-store
// default, and a repeatable `--seat`. An unknown flag or a missing room is a usage error, as argparse's
// exit 2 is.
func parseStatusArgs(args []string) (room, out string, seats []string, err error) {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--out":
			if i+1 >= len(args) {
				return "", "", nil, usageError("--out needs a value")
			}
			i++
			out = args[i]
		case strings.HasPrefix(arg, "--out="):
			out = strings.TrimPrefix(arg, "--out=")
		case arg == "--seat":
			if i+1 >= len(args) {
				return "", "", nil, usageError("--seat needs a value")
			}
			i++
			seats = append(seats, args[i])
		case strings.HasPrefix(arg, "--seat="):
			seats = append(seats, strings.TrimPrefix(arg, "--seat="))
		case strings.HasPrefix(arg, "-"):
			return "", "", nil, usageError("unknown argument " + arg)
		default:
			if room != "" {
				return "", "", nil, usageError("unexpected argument " + arg + " (one room, then flags)")
			}
			room = arg
		}
	}
	if room == "" {
		return "", "", nil, usageError("a room is required")
	}
	out, err = resolveSeatStore(out)
	if err != nil {
		return "", "", nil, err
	}
	return room, out, seats, nil
}

func usageError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: clientcmd.ExitUsage}
}
