package clientrestore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// teeLog is the shell pair's log(): every line is appended to the verb's own log file AND printed, so a
// foreground run is readable and a redirected one is still capturable. `up`'s supervisor logs are
// separate files under the client's state root; these two verbs write
// ~/.config/4genthub/logs/rig-continue.log and rig-watchdog.log.
type teeLog struct {
	out  io.Writer
	file *os.File
}

// openLog creates the log directory and opens the verb's log for appending.
func openLog(name string, out io.Writer) (*teeLog, error) {
	dir, err := logDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &teeLog{out: out, file: file}, nil
}

// printf stamps the line the way `date -Is` did, so a log written by the client and a log written by the
// shell it replaced read as one file rather than two dialects.
func (l *teeLog) printf(format string, args ...any) {
	if l == nil {
		return
	}
	line := now().Format("2006-01-02T15:04:05-07:00") + " " + fmt.Sprintf(format, args...)
	if l.file != nil {
		_, _ = io.WriteString(l.file, line+"\n")
	}
	if l.out != nil {
		_, _ = io.WriteString(l.out, line+"\n")
	}
}

func (l *teeLog) close() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
}
