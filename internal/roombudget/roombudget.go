// The room's budget: the ceilings an owner writes, the numbers one seat is spending, and the record that says
// a seat was parked. One package because the same three facts have three readers - `4genteam room limits`
// writes the file, `budget-run` measures and parks, `room status` reads the record - and a second copy of the
// file's shape is how a writer and a reader come to disagree.
package roombudget

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Name is the limits file inside the ROOM's own directory.
	Name = "limits.json"
	// ParkedName is the record a safeguard leaves in the SEAT's own directory. The record is load-bearing - it is
	// what the routes that refuse a parked seat work read - so the REPO's guard names it in its PROTECTED PATHS and
	// refuses every write shape it parses on it (edit path, redirect, `tee`, cp/mv/install/rsync/ln,
	// rm/unlink/truncate/shred, `sed -i`/`perl -i`, `dd of=`). The copy a seat RUNS - the one the runtime binary
	// embeds and writes into <seat dir>/hooks/pre/seat-guard.ts when it prepares the seat - is the asset as of
	// 194343c (blob d18048e5) and refuses NONE of them here, so a seat can still unpark itself by hand until the
	// runtime is rebuilt, installed and the seats restarted. Neither copy sees a path opened from inside a program
	// (`python3 -c`, `node -e`) or spelled through a variable.
	ParkedName = "parked.json"

	// SpendWindow and RateWindow are the two windows the ceilings are counted over, and they are the
	// design's, not the file's: one number per window, so a ceiling cannot mean two things.
	SpendWindow = 24 * time.Hour
	RateWindow  = time.Minute

	// DefaultUSDPerSeat24h and DefaultCallsPerSeatMin are the ceilings `room limits` writes when the caller
	// sets one number and not the other.
	DefaultUSDPerSeat24h   = 25
	DefaultCallsPerSeatMin = 300

	// KindSpend and KindRate name the two ceilings, so a caller can hold a rate breach for a second tick
	// without parsing the sentence that explains it.
	KindSpend = "spend"
	KindRate  = "rate"
)

// Limits is the file. Every limit SHIPS OFF: the zero value is inert, and a room with no file at all is a
// room with nothing to enforce rather than a room with a default nobody chose.
type Limits struct {
	Enabled         bool    `json:"enabled"`
	USDPerSeat24h   float64 `json:"usd_per_seat_24h"`
	CallsPerSeatMin int     `json:"calls_per_seat_min"`
}

// Path is the limits file of one room directory.
func Path(roomDir string) string { return filepath.Join(roomDir, Name) }

// Load reads a room's limits. A MISSING file is not an error: it is the shipped state, everything off, and
// the safeguard must not turn it into a refusal to run.
func Load(roomDir string) (Limits, error) {
	raw, err := os.ReadFile(Path(roomDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Limits{}, nil
		}
		return Limits{}, fmt.Errorf("cannot read %s: %w", Path(roomDir), err)
	}
	var limits Limits
	if err := json.Unmarshal(raw, &limits); err != nil {
		return Limits{}, fmt.Errorf("cannot read %s: %w", Path(roomDir), err)
	}
	return limits, nil
}

// Save writes a room's limits the way this client writes its own state: indented, a trailing newline, mode
// 0600, through a temporary file and a rename, so a reader never sees half a file.
func Save(roomDir string, limits Limits) error {
	raw, err := json.MarshalIndent(limits, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(Path(roomDir), append(raw, '\n'))
}

// Usage is what one seat is spending now, over the two windows.
type Usage struct {
	Cost  float64
	Calls int
}

// Measure reads a seat's own logs: the USD its assistant turns cost inside SpendWindow (the
// `.message.usage.cost.total` of the session transcripts, the field `room report` reads) and the tool calls
// its guard log recorded inside RateWindow. A log that cannot be read measures zero, and zero breaches no
// ceiling: an unreadable log is not evidence of spending.
func Measure(seatDir string, now time.Time) Usage {
	var usage Usage
	spendCutoff := now.Add(-SpendWindow)
	files, err := filepath.Glob(filepath.Join(seatDir, "sessions", "*.jsonl"))
	if err == nil {
		for _, file := range files {
			_ = forEachLine(file, func(raw []byte) {
				var line struct {
					Type      string `json:"type"`
					Timestamp string `json:"timestamp"`
					Message   struct {
						Role  string `json:"role"`
						Usage struct {
							Cost struct {
								Total float64 `json:"total"`
							} `json:"cost"`
						} `json:"usage"`
					} `json:"message"`
				}
				if json.Unmarshal(raw, &line) != nil || line.Type != "message" || line.Message.Role != "assistant" {
					return
				}
				at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
				if err != nil || at.Before(spendCutoff) {
					return
				}
				usage.Cost += line.Message.Usage.Cost.Total
			})
		}
	}
	rateCutoff := now.Add(-RateWindow)
	_ = forEachLine(filepath.Join(seatDir, "tool-calls.jsonl"), func(raw []byte) {
		var call struct {
			At string `json:"at"`
		}
		if json.Unmarshal(raw, &call) != nil {
			return
		}
		if at, err := time.Parse(time.RFC3339Nano, call.At); err == nil && !at.Before(rateCutoff) {
			usage.Calls++
		}
	})
	return usage
}

// Breach is the ceiling this seat is over, as (kind, reason), or ("", "") when it is inside every one of
// them. The ceilings are compared only while the limits are on: a disabled file is a room nobody asked to
// bound, whatever numbers it still carries.
func Breach(limits Limits, usage Usage) (string, string) {
	if !limits.Enabled {
		return "", ""
	}
	if limits.USDPerSeat24h > 0 && usage.Cost > limits.USDPerSeat24h {
		return KindSpend, fmt.Sprintf("spent %.2f USD in the last 24h, over the %.2f USD ceiling", usage.Cost, limits.USDPerSeat24h)
	}
	if limits.CallsPerSeatMin > 0 && usage.Calls > limits.CallsPerSeatMin {
		return KindRate, fmt.Sprintf("made %d tool calls in the last minute, over the %d call ceiling", usage.Calls, limits.CallsPerSeatMin)
	}
	return "", ""
}

// Parked is the record a safeguard leaves in a seat's own directory: which seat, why it was stopped, when,
// and the numbers that stopped it, so the reason outlives the process that found it.
type Parked struct {
	Seat   string  `json:"seat"`
	Reason string  `json:"reason"`
	At     string  `json:"at"`
	Cost   float64 `json:"cost_usd"`
	Calls  int     `json:"calls"`
}

// ParkedPath is the park record of one seat.
func ParkedPath(seatDir string) string { return filepath.Join(seatDir, ParkedName) }

// ReadParked reads a seat's park record. A missing or unreadable record is `false` - a seat that is not
// parked - because a corrupt record must not read as a parked seat.
func ReadParked(seatDir string) (Parked, bool) {
	raw, err := os.ReadFile(ParkedPath(seatDir))
	if err != nil {
		return Parked{}, false
	}
	var parked Parked
	if json.Unmarshal(raw, &parked) != nil {
		return Parked{}, false
	}
	return parked, true
}

// WriteParked records a park the same way the limits are written: 0600, through a temporary file and a rename.
func WriteParked(seatDir string, parked Parked) error {
	raw, err := json.MarshalIndent(parked, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(ParkedPath(seatDir), append(raw, '\n'))
}

// forEachLine calls fn with every non-blank line of a JSONL file; a line that is not JSON is the caller's to
// skip. `room report` carries the same reader for the same reason: a half-written last line is not an error.
func forEachLine(path string, fn func([]byte)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// writeFile is this package's one writer: mode 0600, a temporary file in the same directory, then a rename.
func writeFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
