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

// StateRootMessage is the ONE refusal a machine that cannot resolve the state root gets. It names the
// escape rather than guessing, because a relative path would make the state tree depend on the working
// directory - the dependency this client forbids everywhere.
const StateRootMessage = "the state root cannot be resolved on this machine: pass --state-root DIR"

// configDirName is the ONE directory under the real home's .config where every piece of the client's
// state lives: the env file, the native state root and the seat store.
const configDirName = "4genthub"

// configDir is ~/.config/4genthub under the real home.
func configDir(home string) string {
	return filepath.Join(home, ".config", configDirName)
}

// StateSubdir is the named directory (briefs, logs, state) of the client's state home under home.
func StateSubdir(home, name string) string {
	return filepath.Join(configDir(home), name)
}

// RealHome is the account's home directory from the passwd entry, NOT $HOME. the rig orchestrator launches a seat
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
	return filepath.Join(configDir(home), ".env"), nil
}

// DefaultStateRoot is where the client keeps its native state: ~/.config/4genthub. It is here rather than in a verb package
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
	return configDir(home)
}

// realHome is a var so a test can drive the machine that has no resolvable home: that path refuses
// rather than guesses, so it has to be reachable without breaking the machine running the test.
var realHome = RealHome

// ResolveStateRoot is the ONE resolution every verb uses: the flag when it is given, else this machine's
// own default. It REFUSES when both are empty - a machine with no resolvable home and no flag gets that
// message rather than a path that depends on where the client was run from.
func ResolveStateRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
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
// Two reasons it answers "" and NEVER a relative path: the rig orchestrator launches a seat with HOME pointed at the
// seat's own state directory, so resolving from the environment would make the store a function of which
// seat runs the client; and a relative store would make the tree a function of the working directory, so
// a client run from a checkout could read whatever tree happened to sit under it. Callers refuse with
// SeatStoreMessage instead - ResolveSeatStore is that refusal.
func DefaultSeatStore() string {
	home, err := realHome()
	if err != nil || home == "" {
		return ""
	}
	return SeatStoreUnder(home)
}

// SeatStoreUnder is the seat store of the account whose home is home.
func SeatStoreUnder(home string) string {
	return filepath.Join(configDir(home), "seats")
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

// Set writes ONE key's value into the client's env file, leaves every other byte of the file alone, and
// returns no value a caller could print. That is the whole reason it is here rather than in the verb
// that needed it: the file also holds the account's credential, so a verb must be able to write one
// switch without reading, echoing or rewriting anything else in it.
//
// The assignment replaces the FIRST line that sets the key (an `export ` prefix included) wherever it
// sits, because Load's first-occurrence-wins rule is what makes one of two lines effective; otherwise it
// is appended. A missing file is created, with its directory - 0600 and 0700, because a fresh env file
// is a secret on a shared machine - while an existing file keeps the mode it had, since a mode the
// operator chose is not this function's business.
//
// The write goes through a temporary file in the same directory and a rename, so an interrupted write
// cannot truncate a file whose loss is a credential. A symlink is followed rather than replaced: the
// client's rooms link TO this file, and writing through the link keeps the link.
func Set(path, name, value string) error {
	if !validName(name) {
		return fmt.Errorf("%q is not an environment variable name", name)
	}
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("the value for %s contains a line break, which would corrupt %s", name, path)
	}

	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	existing, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot read %s: %w", target, err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}

	lines := strings.Split(string(existing), "\n")
	assignment := name + "=" + value
	replaced := false
	for i, line := range lines {
		candidate := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if key, _, found := strings.Cut(candidate, "="); found && strings.TrimSpace(key) == name {
			lines[i], replaced = assignment, true
			break
		}
	}
	if !replaced {
		// The assignment goes on its own TERMINATED line. When the file already ends with a newline the
		// final empty element is where it lands; when it does not, the unterminated last line stays as it
		// is and the assignment follows, or the next tool to append here glues its first line onto it.
		if lines[len(lines)-1] != "" {
			lines = append(lines, assignment, "")
		} else {
			lines = append(lines[:len(lines)-1], assignment, "")
		}
	}

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".env-*")
	if err != nil {
		return fmt.Errorf("cannot write beside %s: %w", target, err)
	}
	defer func() {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
	}()
	if _, err := temp.WriteString(strings.Join(lines, "\n")); err != nil {
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("cannot set the mode of %s: %w", target, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	if err := os.Rename(temp.Name(), target); err != nil {
		return fmt.Errorf("cannot replace %s: %w", target, err)
	}
	return nil
}

// validName is the shape an env key may take. Anything else would write a line Load cannot read back.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
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
