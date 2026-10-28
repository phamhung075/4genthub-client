package clientwatch

import (
	"io"
	"time"

	"github.com/phamhung075/4genthub-client/internal/agyusage"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// agyUsage is the usage text on an agy seat's pane bottom row: the 5h and weekly windows of the agy sign-in for the
// seat's model group, read by internal/agyusage from one cache file under the state root and refreshed by at most
// one `agy -p /usage` per AGY_USAGE_REFRESH_SECONDS (default 300, floor 60) on this machine. The probe spends no
// model turn; a window agy does not report is shown as unknown.
func agyUsage(root, model string) func() string {
	seconds, err := clientenv.Limit("AGY_USAGE_REFRESH_SECONDS", int(agyusage.DefaultEvery/time.Second))
	if err != nil {
		seconds = int(agyusage.DefaultEvery / time.Second)
	}
	watcher := agyusage.NewWatcher(root, time.Duration(seconds)*time.Second)
	return func() string { return "agy limits · " + agyusage.Line(watcher.Reading(), model, time.Now()) }
}

// footLine keeps one line pinned under the stream without a scroll region (a region would keep scrolled lines
// out of the scrollback): the line is written with no newline, and the next write first returns to column 0 and
// erases it, so every event lands where the footer was and the footer is drawn again below it.
type footLine struct {
	w     io.Writer
	text  func() string
	shown bool
	last  string
}

const eraseLine = "\r\033[2K"

func (f *footLine) Write(p []byte) (int, error) {
	f.clear()
	return f.w.Write(p)
}

func (f *footLine) clear() {
	if f.shown {
		_, _ = io.WriteString(f.w, eraseLine)
		f.shown = false
	}
}

// draw writes the footer when it is not on screen or its text changed, cut to the pane width so it never wraps
// (a wrapped footer would leave its first row behind when it is erased). A nil footLine draws nothing.
func (f *footLine) draw(width int) {
	if f == nil {
		return
	}
	text := fitColumns(f.text(), width-1)
	if f.shown && text == f.last {
		return
	}
	f.clear()
	_, _ = io.WriteString(f.w, fg(stampColor)+text+reset)
	f.shown, f.last = true, text
}

func fitColumns(text string, width int) string {
	runes := []rune(text)
	if width < 1 || len(runes) <= width {
		return text
	}
	return string(runes[:width-1]) + "…"
}
