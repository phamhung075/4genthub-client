package clientwatch

import (
	"context"
)

// Snapshot is what one seat's newest session log says about it, for callers outside this package
// (`4genteam ps`). It reuses seatLog, seatModel and contextTokens so a seat has ONE answer to "which
// log, which runtime, which model, how full" wherever it is asked.
type Snapshot struct {
	LogPath       string
	Runtime       string
	Model         string
	ContextTokens int
	CompactLimit  int
}

// SeatSnapshot reads the seat's newest session log. A seat with no log yields an empty LogPath; the
// omp state root is optional, so a machine without one still resolves agy and Claude Code seats.
func SeatSnapshot(ctx context.Context, rig, seat string) Snapshot {
	root, _ := resolveStateRoot("")
	logPath, runtime := seatLog(ctx, root, rig, seat)
	snap := Snapshot{LogPath: logPath, Runtime: runtime, CompactLimit: compactLimit()}
	if logPath == "" {
		snap.Runtime = ""
		return snap
	}
	snap.Model = seatModel(logPath)
	lines := readLines(logPath)
	for i := len(lines) - 1; i >= 0; i-- {
		if tokens := contextTokens(lines[i]); tokens != nil {
			snap.ContextTokens = *tokens
			break
		}
	}
	return snap
}
