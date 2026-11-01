// Package seatloop holds what the loop guard knows without a room: what a repeat is, how many make a loop,
// and the record of what was done about it. The guard itself is `4genteam stuck ROOM --loops`
// (clientlifecycle/stuckloop.go); the session-log reading is the feed's own (clientwatch.SeatEvents); and
// `room status`, `ps` and the grid's pane title read the record through this package.
//
// THE INCIDENT (room aa2-gemini, seat gemini, agy, 2026-11-01 17:19-17:22Z): the seat waited on a build slot by
// calling `manage_task status task-249` 20 times in three minutes, with a `schedule` or an `lsof` between every
// few calls, so no "N in a row" ever passed six. The rule is therefore a count inside a window, reset by a
// write, and not a run of identical neighbours. The result text moved too ("Last progress: 3m10s ago"), which
// is why a result is compared with its digits collapsed.
package seatloop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// The defaults, each overridable in the client's env file the way COMPACT_* are.
const (
	// DefaultRepeat is N. 8 identical calls with no write between: measured against every agy transcript and
	// omp session on this machine before it was chosen (docs/WATCH.md, "Seat loop guard").
	DefaultRepeat = 8
	// DefaultWindow bounds how old a counted call may be.
	DefaultWindow = 10 * time.Minute
	// DefaultCooldown is the least time between two actions on one seat, so the guard cannot loop itself.
	DefaultCooldown = 2 * time.Minute
	// DefaultFresh is how recent the newest repeat must be. A seat that repeated itself and has now been inside
	// one long command for longer than this has stopped repeating, and is not looping.
	DefaultFresh = 3 * time.Minute
	// outputKeep is how much of a result is compared.
	outputKeep = 4096
)

// Config is one run's thresholds. A Repeat of 0 turns the guard's actions off.
type Config struct {
	Repeat   int
	Window   time.Duration
	Cooldown time.Duration
	Fresh    time.Duration
}

// On says whether the guard acts at all.
func (c Config) On() bool { return c.Repeat > 0 }

// Defaults is the configuration with nothing set.
func Defaults() Config {
	return Config{Repeat: DefaultRepeat, Window: DefaultWindow, Cooldown: DefaultCooldown, Fresh: DefaultFresh}
}

// ConfigFromEnv reads LOOP_REPEAT_LIMIT, LOOP_WINDOW_SECONDS and LOOP_COOLDOWN_SECONDS. A blank value is the
// default, and a value that is not a whole number is refused by name, as COMPACT_* are.
func ConfigFromEnv() (Config, error) {
	config := Defaults()
	repeat, err := clientenv.Limit("LOOP_REPEAT_LIMIT", DefaultRepeat)
	if err != nil {
		return config, err
	}
	window, err := clientenv.Limit("LOOP_WINDOW_SECONDS", int(DefaultWindow/time.Second))
	if err != nil {
		return config, err
	}
	cooldown, err := clientenv.Limit("LOOP_COOLDOWN_SECONDS", int(DefaultCooldown/time.Second))
	if err != nil {
		return config, err
	}
	config.Repeat = repeat
	config.Window = time.Duration(window) * time.Second
	config.Cooldown = time.Duration(cooldown) * time.Second
	return config, nil
}

// ---- detection -------------------------------------------------------------

// Kind is what one record of a seat's log was.
type Kind int

const (
	// Call is a tool call.
	Call Kind = iota
	// Result is a tool's answer.
	Result
	// Input is a message that reached the seat: the situation changed, so the count starts over.
	Input
)

// Event is one record of a seat's log, in the same shape for every runtime.
type Event struct {
	Kind Kind
	Tool string
	// Args identifies the call: every argument except the free-text intent, which changes while the call does not.
	Args string
	// Label is the call as a person reads it: its argument values, for messages and alerts.
	Label string
	// ID ties a result to its call; empty on a runtime that names neither.
	ID string
	// Output and Failed are a result's.
	Output string
	Failed bool
	At     time.Time
}

// Finding is the repeat that makes a seat a looping seat.
type Finding struct {
	// Rule is "call" (the same call and arguments) or "result" (the same answer, in a row).
	Rule  string
	Tool  string
	Label string
	Count int
	Last  time.Time
}

// Line is the finding in one line, for a message or a status row.
func (f Finding) Line() string {
	text := strings.TrimSpace(f.Tool + " " + f.Label)
	if len(text) > 100 {
		text = strings.TrimRight(text[:100], " ") + "..."
	}
	return text
}

// writingTools are the tools whose success is progress. A name is compared in lower case, and a tool this list
// misses counts as not writing, which at worst steers a seat that was rewriting a file with an unknown tool.
var writingTools = regexp.MustCompile(`(^|_)(write|edit|multiedit|patch|replace|create_file)(_|$)|^(write|edit|apply_patch|ast_edit)`)

// shellWrites are the commands that change something, on a tool that runs a shell.
var shellWrites = regexp.MustCompile(`(^|[;&|\s=])(git\s+(commit|push|merge|rebase|cherry-pick|apply|am)\b|sed\s+-i|tee\s|mv\s|cp\s|rm\s|touch\s|mkdir\s)|[^0-9&>]\s*>>?\s*[^&\s/]`)

// shellTools are the tools whose arguments are a command line.
var shellTools = map[string]bool{"bash": true, "shell": true, "run_command": true}

func writes(tool, args string) bool {
	name := strings.ToLower(tool)
	if shellTools[name] {
		return shellWrites.MatchString(args)
	}
	return writingTools.MatchString(name)
}

var digits = regexp.MustCompile(`[0-9]+`)

// normalOutput is a result as it is compared: without the numbers that move on every read (a clock, a byte
// count, "3m10s ago"), and with its whitespace collapsed. An empty answer identifies nothing, so it never counts:
// a grep that finds nothing in eight different files is not a loop.
func normalOutput(output string) string {
	if len(output) > outputKeep {
		output = output[:outputKeep]
	}
	return strings.Join(strings.Fields(digits.ReplaceAllString(output, "#")), " ")
}

type repeat struct {
	tool, label string
	count       int
	last        time.Time
}

// Detect reads one seat's events, oldest first, and answers the repeat that makes it a looping seat.
//
// THE COUNT runs over the window and starts over at any progress: an Input, or a write that succeeded and had not
// been made before (an identical write repeated is itself a repeat, and is no progress). Events at or before
// since are not read: since is when the guard last acted, so each rung of the ladder is earned by repeats that
// came after the rung before it, whatever the runtime records about the message.
//
// A SEAT THAT IS NOT MAKING CALLS IS NOT LOOPING: it logs nothing, so the repeat ages past Fresh. That is the seat
// blocked in one long command. Different work is different arguments, so reading different files never adds up.
func Detect(events []Event, config Config, now, since time.Time) (Finding, bool) {
	if !config.On() {
		return Finding{}, false
	}
	calls := map[string]*repeat{}
	written := map[string]bool{}
	type open struct{ id, key string }
	var pending []open
	resultKey, resultLabel, resultTool := "", "", ""
	resultCount := 0
	var resultLast time.Time
	var best Finding
	found := false
	consider := func(candidate Finding) {
		if candidate.Count < config.Repeat || now.Sub(candidate.Last) > config.Fresh {
			return
		}
		if !found || candidate.Count > best.Count {
			best, found = candidate, true
		}
	}
	reset := func() {
		calls = map[string]*repeat{}
		resultKey, resultCount = "", 0
		best, found = Finding{}, false
	}
	for _, event := range events {
		if now.Sub(event.At) > config.Window || !event.At.After(since) {
			continue
		}
		switch event.Kind {
		case Input:
			reset()
		case Call:
			key := event.Tool + "\x00" + event.Args
			pending = append(pending, open{id: event.ID, key: key})
			entry := calls[key]
			if entry == nil {
				entry = &repeat{tool: event.Tool, label: event.Label}
				calls[key] = entry
			}
			entry.count++
			entry.last = event.At
		case Result:
			key := ""
			for index, call := range pending {
				if event.ID == "" || call.id == event.ID {
					key = call.key
					pending = append(pending[:index], pending[index+1:]...)
					break
				}
			}
			tool, args, _ := strings.Cut(key, "\x00")
			if key != "" && !event.Failed && writes(tool, args) && !written[key] {
				written[key] = true
				reset()
				continue
			}
			output := normalOutput(event.Output)
			if output == "" {
				resultKey, resultCount = "", 0
				continue
			}
			answer := tool + "\x00" + output
			if answer != resultKey {
				resultKey, resultCount, resultTool, resultLabel = answer, 0, tool, firstLine(output)
			}
			resultCount++
			resultLast = event.At
		}
	}
	for _, entry := range calls {
		consider(Finding{Rule: "call", Tool: entry.tool, Label: entry.label, Count: entry.count, Last: entry.last})
	}
	consider(Finding{Rule: "result", Tool: resultTool, Label: "answering: " + resultLabel, Count: resultCount, Last: resultLast})
	return best, found
}

func firstLine(text string) string {
	if len(text) > 80 {
		return text[:80]
	}
	return text
}

// ---- the record ------------------------------------------------------------

// The rungs of the ladder.
const (
	Steered     = 1
	Interrupted = 2
	Halted      = 3
)

// Record is what was last done about one seat.
type Record struct {
	Rung  int       `json:"rung"`
	At    time.Time `json:"at"`
	Call  string    `json:"call"`
	Count int       `json:"count"`
	// Stopped is true once the runtime accepted the stop, and Raised once the owner's row was written.
	Stopped bool `json:"stopped,omitempty"`
	Raised  bool `json:"raised,omitempty"`
}

// Status is the record as a status row reads it.
func (r Record) Status() string {
	word := "steered"
	switch {
	case r.Rung == Interrupted:
		word = "interrupted"
	case r.Rung >= Halted && r.Stopped:
		word = "STOPPED"
	case r.Rung >= Halted:
		word = "stop-refused"
	}
	return fmt.Sprintf("loop=%s at %s, repeated x%d: %s", word, r.At.UTC().Format("15:04Z"), r.Count, r.Call)
}

// Path is the guard's record for one room, inside the room's own directory.
func Path(root, room string) string { return filepath.Join(root, "rooms", room, "loop-guard.json") }

// Load reads a room's records. A file that is absent or unreadable is no records: the cost is an alert not shown
// until the next rung, and a status verb must not fail over it.
func Load(root, room string) map[string]Record {
	records := map[string]Record{}
	raw, err := os.ReadFile(Path(root, room))
	if err != nil {
		return records
	}
	if json.Unmarshal(raw, &records) != nil || records == nil {
		return map[string]Record{}
	}
	return records
}

// Save writes a room's records through a rename, so a reader never sees half a file. No records removes it.
func Save(root, room string, records map[string]Record) error {
	path := Path(root, room)
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".loop-guard-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Rooms are the rooms that hold a record, sorted.
func Rooms(root string) []string {
	matches, err := filepath.Glob(filepath.Join(root, "rooms", "*", "loop-guard.json"))
	if err != nil {
		return nil
	}
	rooms := make([]string, 0, len(matches))
	for _, match := range matches {
		rooms = append(rooms, filepath.Base(filepath.Dir(match)))
	}
	sort.Strings(rooms)
	return rooms
}
