package clientwatch

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const feedPollInterval = time.Second

// runFeed is the read-only stream: one line per tool call and result, coloured by tool kind, tailed
// from every seat's session log. It never writes to a seat.
func runFeed(ctx context.Context, s shared, out io.Writer) error {
	root := s.stateRoot
	seats, err := roomSeats(ctx, root, s.room)
	if err != nil {
		return err
	}
	if len(s.seats) > 0 {
		wanted := map[string]bool{}
		for _, seat := range s.seats {
			wanted[seat] = true
		}
		filtered := []string{}
		for _, seat := range seats {
			if wanted[seat] {
				filtered = append(filtered, seat)
			}
		}
		seats = filtered
	}
	colours := map[string]int{}
	for index, seat := range seats {
		colours[seat] = seatColors[index%seatColorCount]
	}

	// One seat in view (a grid pane): its name and token bar are the top line, not a column.
	var header *Header
	if len(seats) == 1 {
		logPath, _ := seatLog(root, s.room, seats[0])
		header = newHeader(seats[0], colours[seats[0]], compactLimit(), seatModelOf(root, s.room, seats[0], logPath),
			func(line string) { fmt.Fprintln(out, line) })
		if seatRuntimeOf(root, s.room, seats[0]) == "agy" {
			header.usage = agyUsage(root, header.model)
		}
		if paneID := os.Getenv("HERDR_PANE_ID"); paneID != "" {
			header.pane = func(title string) { _, _ = herdr(ctx, "pane", "rename", paneID, title) }
		}
	}

	type position struct {
		path   string
		offset int64
	}
	positions := map[string]position{}

	opts := renderOptions{width: lineWidth(s.width, header != nil), lines: s.lines, detail: s.detail, expand: s.expand,
		only: s.onlyKinds(), errorsOnly: s.errorsOnly, calls: newCallTracker()}
	views := map[string]*seatView{}
	for _, seat := range seats {
		views[seat] = &seatView{}
	}

	show := func(seat, line string) {
		if used := contextTokens(line); header != nil && used != nil {
			header.setTokens(used)
		}
		at := recordTime(line)
		view := views[seat]
		texts := eventsWith(line, opts)
		if header != nil && len(texts) > 0 {
			header.touch(at)
		}
		for _, text := range texts {
			if text == view.last {
				view.repeated++
				continue
			}
			if view.repeated > 0 {
				fmt.Fprintf(out, "%s%s↺ repeated ×%d%s\n", strings.Repeat(" ", 5+1+4), fg(stampColor), view.repeated, reset)
				view.repeated = 0
			}
			view.last = text
			if header != nil {
				// A pane holds one seat: the clock and the meter ride on the rule, so no step line is indented.
				info := func() string {
					return strings.TrimSpace(view.stamp(at) + " " + view.meter(header.tag(), header.percent()))
				}
				if rule := view.ruleBefore(text, opts.width, info); rule != "" {
					fmt.Fprintln(out, rule)
				}
				fmt.Fprintln(out, text)
				continue
			}
			if rule := view.ruleBefore(text, opts.width, nil); rule != "" {
				fmt.Fprintln(out, rule)
			}
			name := fg(colours[seat]) + padRight(seat, 12) + reset
			fmt.Fprintf(out, "%s %s%s\n", view.stamp(at), name, text)
		}
	}

	filterOpts := renderOptions{width: 1, lines: s.lines, detail: s.detail, expand: s.expand, only: opts.only, errorsOnly: s.errorsOnly}
	if header == nil {
		fmt.Fprintf(out, "-- following %d seats; Ctrl-C to stop\n", len(seats))
	}
	for _, seat := range seats {
		logPath, _ := seatLog(root, s.room, seat)
		if logPath == "" {
			continue
		}
		lines := readLines(logPath)
		shown := []string{}
		for _, line := range lines {
			if len(eventsWith(line, filterOpts)) > 0 {
				shown = append(shown, line)
			}
		}
		if limit := s.back * 2; len(shown) > limit {
			shown = shown[len(shown)-limit:]
		}
		if header != nil {
			for index := len(lines) - 1; index >= 0; index-- {
				if tokens := contextTokens(lines[index]); tokens != nil {
					header.setTokens(tokens)
					break
				}
			}
		}
		for _, line := range shown {
			show(seat, line)
		}
		if header != nil {
			header.draw()
		}
		positions[seat] = position{path: logPath, offset: logSize(logPath)}
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		for _, seat := range seats {
			logPath, _ := seatLog(root, s.room, seat)
			if logPath == "" {
				continue
			}
			current := positions[seat]
			if current.path != logPath {
				current = position{path: logPath, offset: 0}
			}
			size := logSize(logPath)
			if size > current.offset {
				for _, line := range splitLines(readFrom(logPath, current.offset)) {
					show(seat, line)
				}
			}
			positions[seat] = position{path: logPath, offset: size}
		}
		if header != nil {
			header.draw()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(feedPollInterval):
		}
	}
}

// padRight is Python's f"{seat:<12}".
func padRight(text string, width int) string {
	length := len([]rune(text))
	if length >= width {
		return text
	}
	return text + fmt.Sprintf("%*s", width-length, "")
}

// recordTime is when the seat logged the record, so the clock on a line is the seat's and a replay is
// not stamped with the minute the viewer started. A record with no readable time is stamped now.
func recordTime(line string) time.Time {
	decoded, _, err := decodeOrderedJSON(line)
	if err == nil {
		if record, ok := decoded.(*orderedJSONObject); ok {
			if value, present := record.get("timestamp"); present {
				if text, ok := value.(string); ok {
					if at, err := time.Parse(time.RFC3339Nano, text); err == nil {
						return at.Local()
					}
				}
			}
		}
	}
	return time.Now()
}
