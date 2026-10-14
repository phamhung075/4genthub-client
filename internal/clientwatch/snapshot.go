package clientwatch

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

// SeatSnapshot reads the seat's newest session log. A seat with no log yields an empty LogPath, and so does an empty root.
func SeatSnapshot(root, room, seat string) Snapshot {
	logPath, runtime := "", ""
	if root != "" { // an unresolved root names no directory; a relative one would read the working tree
		logPath, runtime = seatLog(root, room, seat)
	}
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
