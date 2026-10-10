// Package clientenv is the client's ONE environment file, and the machine-level defaults that go with
// it: where the file is, where the state root is, and the limit rule.
//
// The Python client loaded a .env from its own checkout (paths.py: load_dotenv(CLIENT_DIR / ".env")).
// A compiled binary has no checkout, so the file lives at one fixed place under the REAL user's home
// and a variable already present in the process environment always wins - which is what makes an
// operator's `export` override the file rather than the other way round.
package clientenv

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// StateRootEnv names the override for the omp state root - the second clause of the resolution.
const StateRootEnv = "OPENRIG_STATE_ROOT"

// StateRootMessage is the ONE refusal a machine that cannot resolve the state root gets. It names both
// escapes rather than guessing, because a relative path would make the state tree depend on the working
// directory - the dependency this client forbids everywhere.
const StateRootMessage = "the state root cannot be resolved on this machine: pass --state-root DIR or set " + StateRootEnv

// RealHome is the account's home directory from the passwd entry, NOT $HOME. OpenRig launches a seat
// with HOME pointed at the seat's own state directory, so resolving from the environment would make a
// machine-level path a function of which seat happens to run the client - the doubled path seat_policy's
// real_home() exists to avoid.
func RealHome() (string, error) {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir, nil
	}
	return os.UserHomeDir()
}

// DefaultPath is where the client's one env file lives.
func DefaultPath() (string, error) {
	home, err := RealHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "4genthub", ".env"), nil
}

// DefaultStateRoot is where OpenRig keeps its per-seat state. It is here rather than in a verb package
// because two verb groups default to it, and because it is a MACHINE-level location: resolved from the
// real home, it is the same inside a seat and out.
//
// A machine whose real home cannot be resolved answers "" and NEVER a relative path: a relative root
// would make the state tree a function of the working directory, so a client run from a checkout could
// read whatever tree happened to sit under it. Callers refuse with StateRootMessage instead -
// ResolveStateRoot is that refusal, and is what a verb with the root as an option uses.
func DefaultStateRoot() string {
	home, err := realHome()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".openrig", "state", "omp")
}

// realHome is a var so a test can drive the machine that has no resolvable home: that path refuses
// rather than guesses, so it has to be reachable without breaking the machine running the test.
var realHome = RealHome

// ResolveStateRoot is the ONE resolution every verb uses: the flag when it is given, else
// $OPENRIG_STATE_ROOT, else this machine's own default. It REFUSES when all three are empty, naming both
// escapes - a machine with no resolvable home and no override gets that message rather than a path that
// depends on where the client was run from.
//
// The clauses live here and not per verb because a verb that resolved its own root and then told the
// user to set $OPENRIG_STATE_ROOT while ignoring it reads the wrong tree silently.
func ResolveStateRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if fromEnv := os.Getenv(StateRootEnv); fromEnv != "" {
		return fromEnv, nil
	}
	if root := DefaultStateRoot(); root != "" {
		return root, nil
	}
	return "", &clientcmd.CodedError{Message: StateRootMessage, Code: clientcmd.ExitUsage}
}

// SeatStoreMessage is the ONE refusal a machine that cannot resolve the seat store gets. It names the
// reason rather than guessing, because a relative path would make the store depend on the working
// directory - the dependency this client forbids everywhere.
const SeatStoreMessage = "the seat store cannot be resolved on this machine: the account home directory is not readable"

// SeatStoreOutFlag is the escape a verb can offer in that refusal when it really accepts one: --out DIR
// carries the store. It is NOT part of SeatStoreMessage, because two callers have no such flag - the
// bridge resolves no store flag at all, and `sync bundle --out-dir` is where the bundle is written - and a
// verb that advertises a flag it does not accept is worse than one that says less.
const SeatStoreOutFlag = "--out DIR"

// SeatStoreRefusal is the refusal with the escape named, and the ONE rule behind it: a verb that takes the
// flag passes SeatStoreOutFlag, a verb that does not passes "" and keeps the reason alone. The composition
// lives here so both forms are testable without a machine that cannot resolve a home; the error keeps its
// code, so the exit status does not depend on which form a verb shows.
func SeatStoreRefusal(err error, outFlag string) error {
	if err == nil || outFlag == "" {
		return err
	}
	var coded *clientcmd.CodedError
	if errors.As(err, &coded) {
		return &clientcmd.CodedError{Message: coded.Message + ", or pass " + outFlag, Code: coded.Code}
	}
	return fmt.Errorf("%w, or pass %s", err, outFlag)
}

// DefaultSeatStore is where this client keeps the adopted snapshots and their pins. It lives here rather
// than in a verb package because several verbs default to it, and because it is a MACHINE-level location:
// resolved from the real home, it is the same inside a seat and out.
//
// Two reasons it answers "" and NEVER a relative path: OpenRig launches a seat with HOME pointed at the
// seat's own state directory, so resolving from the environment would make the store a function of which
// seat runs the client; and a relative store would make the tree a function of the working directory, so
// a client run from a checkout could read whatever tree happened to sit under it. Callers refuse with
// SeatStoreMessage instead - ResolveSeatStore is that refusal.
func DefaultSeatStore() string {
	home, err := realHome()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".openrig", "agenthub-seats")
}

// ResolveSeatStore is the ONE resolution every verb uses: the flag when it is given, else this machine's
// own default. It REFUSES when both are empty, naming the reason - a machine with no resolvable home and
// no flag gets that message rather than a path that depends on where the client was run from.
func ResolveSeatStore(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if store := DefaultSeatStore(); store != "" {
		return store, nil
	}
	return "", &clientcmd.CodedError{Message: SeatStoreMessage, Code: clientcmd.ExitUsage}
}

// Load reads KEY=VALUE lines from path and sets each variable that is NOT already in the environment.
// An existing value wins; a missing file is not an error, because the client is usable with the
// environment alone. Blank lines and # comments are skipped, and a value may be quoted.
func Load(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "export "))
		value = unquote(strings.TrimSpace(value))
		if name == "" {
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		_ = os.Setenv(name, value)
	}
	return scanner.Err()
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}

// Limit is the client's limit rule, ported from paths.env_limit: an integer from the environment where
// a BLANK value means the documented default rather than a parse failure - a copied .env that blanked
// a limit is a normal thing to find - while a value that is present and not a number is a typo and
// fails loudly, naming the variable and what it held.
func Limit(name string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s=%q is not a whole number", name, raw),
			Code:    clientcmd.ExitUsage,
		}
	}
	return value, nil
}
