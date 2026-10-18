package clientrestore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// teeLog is the shell pair's log(): every line is appended to the verb's own log file AND printed, so a
// foreground run is readable and a redirected one is still capturable. `up`'s supervisor logs are
// separate files under the client's state root; these two verbs write
// ~/.config/4genthub/logs/rig-continue.log and rig-watchdog.log.
type teeLog struct {
	out  io.Writer
	file *os.File
}

// openLog creates the log directory and opens the verb's log for appending, under the root its caller
// resolved - so a verb pointed at another tree writes its log there and nothing under this machine's.
func openLog(name, root string, out io.Writer) (*teeLog, error) {
	dir := logDir(root)
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
//
// The line goes through the known-values redaction before it is written, because this file is a place a seat
// can read: the ruling's sentinel clause requires that a value which reached the process through its
// ENVIRONMENT appears in neither the log nor the event, and a step that fails with a child's output, an argv or
// its own environment in the message must not put that value on disk. Only KNOWN values are replaced, so an
// ordinary line is unchanged and this file still reads as it did.
func (l *teeLog) printf(format string, args ...any) {
	if l == nil {
		return
	}
	line := now().Format("2006-01-02T15:04:05-07:00") + " " + fmt.Sprintf(format, args...)
	line, _ = secretscan.RedactKnownValues(line, redactionKnownValues())
	if l.file != nil {
		_, _ = io.WriteString(l.file, line+"\n")
	}
	if l.out != nil {
		_, _ = io.WriteString(l.out, line+"\n")
	}
}

// redactionKnownValues is a seam - the same one clientsync's gate keeps, and for the same reason: the
// environment is the one input a test cannot set behind a function that captured it already.
var redactionKnownValues = func() map[string]string {
	return secretscan.KnownValuesFromEnv(os.Environ())
}

func (l *teeLog) close() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
}
