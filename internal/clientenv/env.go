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
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

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
func DefaultStateRoot() string {
	home, err := RealHome()
	if err != nil {
		return filepath.Join(".openrig", "state", "omp")
	}
	return filepath.Join(home, ".openrig", "state", "omp")
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
