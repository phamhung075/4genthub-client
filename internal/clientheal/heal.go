// Package clientheal is `4genteam heal`: the checks a room needs to be a working environment, each with the
// fix that is safe to apply on its own. It holds nothing project-specific. A room is whatever the runtime
// reports for it, a seat is whatever the room lists, and the state root's `.env` is the one configuration
// file, so the same run serves every team on every project.
//
// A finding is fixed, left (a fix that needs the owner), or clean. A fix is applied, then the check is run
// AGAIN: a finding stays "unfixed" until the second read agrees, so a fix that did nothing is reported as
// failing rather than as done. Values of the env file are never read into a result: only whether a key has one.
package clientheal

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// RequiredKeys are the variables every seat must hold for the agenthub_http MCP server to mount.
var RequiredKeys = []string{"AGENTHUB_URL", "AGENTHUB_TOKEN"}

// Finding is one problem the run saw.
type Finding struct {
	Check  string `json:"check"`
	Target string `json:"target"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`    // what the run does about it; "" when only the owner can
	Result string `json:"result"` // fixed | unfixed | left | would-fix
}

// Deps are the run's seams: the machine reads and the repairs. Tests drive the decisions through fakes.
type Deps struct {
	StateRoot string
	Seats     func(room string) ([]clientruntime.SeatStatus, error)
	Reseat    func(room, seat string) error
	// ProcEnv answers the environment keys that have a non-empty value in the seat's running process, and
	// false when the process cannot be found or this machine cannot read it.
	ProcEnv func(room, seat string) (map[string]bool, bool)
	// Rows is the room's safeguard report. Its error is the process probe's (7.12): a run that could not read
	// it refuses in words rather than healing a report nobody decided.
	Rows  func(room string) ([]clientservices.Health, error)
	Up    func(room string) error
	Cause func() string
	Fix   bool
}

// Run checks one room and returns every finding. An error is the safeguard report's: the process probe
// could not run, so no safeguard finding was decided and no repair built on it is applied (7.12).
func Run(room string, d Deps) ([]Finding, error) {
	var out []Finding
	envKeys, envErr := envFileKeys(filepath.Join(d.StateRoot, ".env"))
	if envErr != nil {
		out = append(out, Finding{Check: "env-file", Target: d.StateRoot, Detail: envErr.Error(), Result: "left"})
	}
	for _, key := range RequiredKeys {
		if envErr == nil && !envKeys[key] {
			out = append(out, Finding{Check: "env-key", Target: key, Detail: "no value in " + filepath.Join(d.StateRoot, ".env"), Result: "left"})
		}
	}
	seats, err := d.Seats(room)
	if err != nil {
		return append(out, Finding{Check: "runtime", Target: room, Detail: err.Error(), Result: "left"}), nil
	}
	for _, seat := range seats {
		out = append(out, checkSeat(room, seat, envKeys, d)...)
	}
	safeguards, err := checkSafeguards(room, d)
	if err != nil {
		return nil, err
	}
	return append(out, safeguards...), nil
}

func checkSeat(room string, seat clientruntime.SeatStatus, envKeys map[string]bool, d Deps) []Finding {
	target := room + "/" + seat.Seat
	if !seatUp(seat) {
		return []Finding{apply(d, Finding{Check: "seat-down", Target: target, Detail: "state " + seat.State, Fix: "reseat"},
			func() error { return d.Reseat(room, seat.Seat) },
			func() bool { return seatsUp(d, room, seat.Seat) })}
	}
	var out []Finding
	var missing []string
	if keys, ok := d.ProcEnv(room, seat.Seat); ok {
		for _, key := range RequiredKeys {
			if envKeys[key] && !keys[key] {
				missing = append(missing, key)
			}
		}
	}
	if len(missing) > 0 {
		out = append(out, apply(d, Finding{Check: "seat-env", Target: target, Detail: "the running seat lacks " + strings.Join(missing, ", "), Fix: "reseat"},
			func() error { return d.Reseat(room, seat.Seat) },
			func() bool {
				keys, ok := d.ProcEnv(room, seat.Seat)
				if !ok {
					return false
				}
				for _, key := range missing {
					if !keys[key] {
						return false
					}
				}
				return true
			}))
	}
	mcp := filepath.Join(clientruntime.SeatDir(d.StateRoot, room, seat.Seat), "mcp.json")
	if _, err := os.Stat(mcp); err != nil {
		out = append(out, apply(d, Finding{Check: "seat-mcp", Target: target, Detail: "no mcp.json in the seat dir", Fix: "reseat"},
			func() error { return d.Reseat(room, seat.Seat) },
			func() bool { _, err := os.Stat(mcp); return err == nil }))
	}
	return out
}

func checkSafeguards(room string, d Deps) ([]Finding, error) {
	rows, err := d.Rows(room)
	if err != nil {
		return nil, err
	}
	down := clientservices.NotRunning(rows)
	if len(down) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(down))
	for _, row := range down {
		names = append(names, row.Safeguard)
	}
	finding := apply(d, Finding{Check: "safeguards", Target: room, Detail: "not running: " + strings.Join(names, ", "), Fix: "up"},
		func() error { return d.Up(room) },
		func() bool {
			// A RE-CHECK THAT COULD NOT READ IS NOT A FIX (7.12); the finding stays unfixed instead.
			fresh, err := d.Rows(room)
			return err == nil && len(clientservices.NotRunning(fresh)) == 0
		})
	if finding.Result == "unfixed" {
		if cause := d.Cause(); cause != "" {
			finding.Detail += "; last cause: " + cause
		}
	}
	return []Finding{finding}, nil
}

// apply runs the fix when the run may, then re-checks. Without --fix the finding says what it would do.
func apply(d Deps, f Finding, fix func() error, healed func() bool) Finding {
	if !d.Fix {
		f.Result = "would-fix"
		return f
	}
	if err := fix(); err != nil {
		f.Result, f.Detail = "unfixed", f.Detail+"; "+f.Fix+" failed: "+err.Error()
		return f
	}
	if healed() {
		f.Result = "fixed"
	} else {
		f.Result = "unfixed"
	}
	return f
}

func seatUp(seat clientruntime.SeatStatus) bool {
	return strings.EqualFold(strings.TrimSpace(seat.State), "running")
}

func seatsUp(d Deps, room, name string) bool {
	seats, err := d.Seats(room)
	if err != nil {
		return false
	}
	for _, seat := range seats {
		if seat.Seat == name {
			return seatUp(seat)
		}
	}
	return false
}

// envFileKeys answers which keys of the env file carry a value. It keeps no value.
func envFileKeys(path string) (map[string]bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	keys := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "export ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			keys[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`) != ""
		}
	}
	return keys, scanner.Err()
}
