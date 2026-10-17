// Package clientactivity is the `4genteam activity` verb: ONE time-ordered, one-line-per-event reading of what the
// seats of a room did, so a recheck is a single command instead of three files in three formats.
//
// It reads, and never writes, the three records the client keeps:
//
//	TOOL   <state>/rooms/<room>/seats/<seat>/tool-calls.jsonl   every tool call, written by the seat's tool hook
//	SEND   <state>/seats/<room>/<seat>/audit.jsonl              every send decision and its delivery outcome
//	JEV    <state>/jev_decisions.log                            every Jev decision and completion gate (machine-wide)
//
// A line is `<time> <seat> <kind> <outcome> <what>`. The outcome column is the one a recheck scans: `blocked`,
// `refused`, `error` and `escalated` are the lines worth reading; `ok`, `allowed` and `decided` are the rest.
package clientactivity

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

const usage = `usage: 4genteam activity ROOM [--seat S] [--kind tool|send|jev] [--problems] [--since DURATION] [--limit N] [--json]

One timeline of what a room's seats did: their tool calls, their sends and the Jev decisions that concern them.

  --seat      only this seat's tool calls and sends (Jev rows are machine-wide and name no seat, so they stay)
  --kind      only tool calls, only sends, or only Jev rows
  --problems  only what was refused or went wrong: blocked and failed tool calls, refused sends, escalations, rejected and unjudgeable gates
  --since     only the last DURATION (30m, 2h, 24h); the default is everything
  --limit     the newest N lines (default 200)
  --json      one JSON object per line instead of text

Reads ~/.config/4genthub only; nothing is written.
`

// Commands is what the dispatcher registers from this package: one verb, `activity`.
func Commands() []clientcmd.Command { return []clientcmd.Command{activityCommand{}} }

type activityCommand struct{}

func (activityCommand) Name() string { return "activity" }
func (activityCommand) Summary() string {
	return "one timeline of a room's tool calls, sends and Jev decisions"
}

// Event is one line of the timeline.
type Event struct {
	At      time.Time `json:"at"`
	Seat    string    `json:"seat,omitempty"`
	Kind    string    `json:"kind"`
	Outcome string    `json:"outcome"`
	What    string    `json:"what"`
	Detail  string    `json:"detail,omitempty"`
}

// problemOutcomes are the outcomes --problems keeps: what a seat was refused, what failed, and the gate verdicts
// that are not an acceptance. `reject` and `unjudgeable` are both the enforced gate refusing to close the task,
// so an operator looking for why a task did not close must find either.
var problemOutcomes = map[string]bool{
	"blocked": true, "error": true, "refused": true, "escalated": true, "reject": true, "unjudgeable": true, "delivery_failed": true,
}

type options struct {
	room, seat, kind string
	problems, asJSON bool
	since            time.Duration
	limit            int
}

func parse(args []string) (options, error) {
	opts := options{limit: 200}
	for i := 0; i < len(args); i++ {
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", args[i])
			}
			i++
			return args[i], nil
		}
		var err error
		switch arg := args[i]; arg {
		case "--seat":
			opts.seat, err = value()
		case "--kind":
			opts.kind, err = value()
			if err == nil && opts.kind != "tool" && opts.kind != "send" && opts.kind != "jev" {
				err = fmt.Errorf("--kind must be tool, send or jev")
			}
		case "--problems":
			opts.problems = true
		case "--json":
			opts.asJSON = true
		case "--since":
			var text string
			if text, err = value(); err == nil {
				opts.since, err = time.ParseDuration(text)
			}
		case "--limit":
			var text string
			if text, err = value(); err == nil {
				_, err = fmt.Sscanf(text, "%d", &opts.limit)
			}
		default:
			if strings.HasPrefix(arg, "-") || opts.room != "" {
				return opts, fmt.Errorf("unexpected argument %q", arg)
			}
			opts.room = arg
		}
		if err != nil {
			return opts, err
		}
	}
	if opts.room == "" {
		return opts, fmt.Errorf("a room name is required")
	}
	return opts, nil
}

func (activityCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, usage)
		return clientcmd.ExitOK
	}
	opts, err := parse(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam activity: %v\n%s", err, usage)
		return clientcmd.ExitUsage
	}
	root := clientenv.DefaultStateRoot()
	if root == "" {
		_, _ = io.WriteString(stderr, "4genteam activity: "+clientenv.StateRootMessage+"\n")
		return clientcmd.ExitUsage
	}
	events := Collect(root, opts.room)
	events = filter(events, opts, time.Now())
	if len(events) > opts.limit {
		events = events[len(events)-opts.limit:]
	}
	for _, event := range events {
		if opts.asJSON {
			line, _ := json.Marshal(event)
			_, _ = fmt.Fprintln(stdout, string(line))
			continue
		}
		_, _ = fmt.Fprintln(stdout, render(event))
	}
	return clientcmd.ExitOK
}

func filter(events []Event, opts options, now time.Time) []Event {
	out := events[:0:0]
	for _, event := range events {
		switch {
		case opts.kind != "" && event.Kind != opts.kind:
		case opts.seat != "" && event.Seat != opts.seat && event.Kind != "jev":
		case opts.since > 0 && now.Sub(event.At) > opts.since:
		case opts.problems && !problemOutcomes[event.Outcome]:
		default:
			out = append(out, event)
		}
	}
	return out
}

func render(event Event) string {
	seat := event.Seat
	if seat == "" {
		seat = "-"
	}
	line := fmt.Sprintf("%s %-9s %-4s %-9s %s", event.At.Local().Format("01-02 15:04:05"), seat, event.Kind, event.Outcome, event.What)
	if event.Detail != "" {
		line += "  | " + event.Detail
	}
	return line
}

// Collect reads the three records for one room, oldest first. A missing file is an empty record, not an error:
// a seat that has not called a tool yet has nothing to show.
func Collect(root, room string) []Event {
	var events []Event
	seats, _ := filepath.Glob(filepath.Join(root, "rooms", room, "seats", "*", "tool-calls.jsonl"))
	for _, path := range seats {
		seat := filepath.Base(filepath.Dir(path))
		readLines(path, func(raw []byte) {
			var line struct {
				At      time.Time `json:"at"`
				Tool    string    `json:"tool"`
				Target  string    `json:"target"`
				Outcome string    `json:"outcome"`
				Rule    string    `json:"rule"`
				Error   string    `json:"error"`
				Ms      int       `json:"ms"`
			}
			if json.Unmarshal(raw, &line) != nil || line.At.IsZero() {
				return
			}
			what := line.Tool
			if line.Target != "" {
				what += " " + line.Target
			}
			detail := ""
			if line.Rule != "" {
				detail = "rule " + line.Rule + ": " + line.Error
			} else if line.Error != "" {
				detail = line.Error
			}
			events = append(events, Event{At: line.At, Seat: seat, Kind: "tool", Outcome: line.Outcome, What: what, Detail: detail})
		})
	}
	sends, _ := filepath.Glob(filepath.Join(root, "seats", room, "*", "audit.jsonl"))
	for _, path := range sends {
		seat := filepath.Base(filepath.Dir(path))
		readLines(path, func(raw []byte) {
			var line struct {
				At      time.Time
				From    string
				To      string
				Intent  string
				Allowed bool
				Reason  string
				Outcome string
			}
			// Lifecycle lines share the file and carry no From; only a send decision does.
			if json.Unmarshal(raw, &line) != nil || line.From == "" || line.At.IsZero() {
				return
			}
			outcome := line.Outcome
			switch {
			case outcome != "":
			case line.Allowed:
				outcome = "allowed"
			default:
				outcome = "refused"
			}
			events = append(events, Event{At: line.At, Seat: seat, Kind: "send", Outcome: outcome,
				What: fmt.Sprintf("%s -> %s (%s)", line.From, line.To, line.Intent), Detail: line.Reason})
		})
	}
	readLines(filepath.Join(root, "jev_decisions.log"), func(raw []byte) {
		var line struct {
			Timestamp time.Time `json:"timestamp"`
			Kind      string    `json:"kind"`
			Tier      string    `json:"tier"`
			Category  string    `json:"category"`
			Outcome   string    `json:"outcome"`
			Summary   string    `json:"summary"`
		}
		if json.Unmarshal(raw, &line) != nil || line.Timestamp.IsZero() {
			return
		}
		outcome := line.Outcome
		if strings.HasPrefix(outcome, "escalated") {
			outcome = "escalated"
		}
		what := line.Kind
		if line.Tier != "" {
			what += " " + line.Tier
		}
		if line.Category != "" {
			what += "/" + line.Category
		}
		events = append(events, Event{At: line.Timestamp, Kind: "jev", Outcome: outcome, What: what, Detail: line.Summary})
	})
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	return events
}

func readLines(path string, each func([]byte)) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			each(scanner.Bytes())
		}
	}
}
