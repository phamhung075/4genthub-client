package clientwatch

import (
	"context"
	"fmt"
	"io"
	"time"
)

const feedPollInterval = time.Second

// runFeed is the read-only stream: one line per tool call and result, coloured by tool kind, tailed
// from every seat's session log. It never writes to a seat.
func runFeed(ctx context.Context, root string, s shared, out io.Writer) error {
	seats, err := rigSeats(ctx, s.rig)
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
		logPath, _ := seatLog(ctx, root, s.rig, seats[0])
		header = newHeader(seats[0], colours[seats[0]], compactLimit(), seatModel(logPath),
			func(line string) { fmt.Fprintln(out, line) })
	}

	type position struct {
		path   string
		offset int64
	}
	positions := map[string]position{}

	show := func(seat, line string) {
		if used := contextTokens(line); header != nil && used != nil {
			header.setTokens(used)
		}
		stamp := time.Now().Format("15:04:05")
		name := fg(colours[seat]) + padRight(seat, 12) + reset
		if header != nil {
			if tag := header.tag(); tag != "" {
				name = tag
			}
		}
		for _, text := range events(line, s.width, s.detail, s.lines) {
			fmt.Fprintf(out, "%s%s%s %s%s\n", fg(stampColor), stamp, reset, name, text)
		}
	}

	if header == nil {
		fmt.Fprintf(out, "-- following %d seats; Ctrl-C to stop\n", len(seats))
	}
	for _, seat := range seats {
		logPath, _ := seatLog(ctx, root, s.rig, seat)
		if logPath == "" {
			continue
		}
		lines := readLines(logPath)
		shown := []string{}
		for _, line := range lines {
			if len(events(line, 1, s.detail, s.lines)) > 0 {
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
			logPath, _ := seatLog(ctx, root, s.rig, seat)
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
