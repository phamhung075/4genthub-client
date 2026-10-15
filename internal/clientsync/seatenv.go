// What every sync verb shares besides its own command: the seat store and the environment the URL and the
// token are read from, the presence check for a seat's lock file, and the two error helpers the verbs use
// to answer with the Python's exit codes.
//
// It was `statusverb.go` and half of it was the `sync status` verb. That verb is gone with the rigspec
// route it compared against, and what is left is what the surviving verbs were already reaching for.
package clientsync

import (
	"fmt"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// seatStore is the machine's store: clientenv.ResolveSeatStore, the account home's .config/4genthub/seats.
// It is a var so a test can point the store at a scratch tree - the store no longer follows $HOME, so
// t.Setenv("HOME", ...) cannot redirect it, exactly as the Python tests monkeypatch DEFAULT_OUT - and so a
// test can drive the machine that cannot resolve one.
var seatStore = clientenv.ResolveSeatStore

// resolveSeatStore is the sync verbs' resolution: the machine's store, with the escape named in the refusal
// because every verb here that resolves a store takes --out. `sync bundle` is the exception and calls
// seatStore directly: its --out-dir is where the bundle is written, not the store.
func resolveSeatStore(flagValue string) (string, error) {
	store, err := seatStore(flagValue)
	return store, clientenv.SeatStoreRefusal(err, clientenv.SeatStoreOutFlag)
}

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

func lockPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// clientFailureCode is the Python's main: EXIT_USAGE stays EXIT_USAGE, everything else is EXIT_FAILED -
// which is 3, this client's ExitUnavailable.
func clientFailureCode(err error) int {
	if clientcmd.CodeOf(err, clientcmd.ExitUnavailable) == clientcmd.ExitUsage {
		return clientcmd.ExitUsage
	}
	return clientcmd.ExitUnavailable
}

func usageError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: clientcmd.ExitUsage}
}
