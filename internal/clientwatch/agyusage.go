package clientwatch

import (
	"time"

	"github.com/phamhung075/4genthub-client/internal/agyusage"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// agyUsage is the usage text of an agy seat's pane title: the 5h and weekly windows of the agy sign-in for the
// seat's model group, read by internal/agyusage from one cache file under the state root and refreshed by at most
// one `agy -p /usage` per AGY_USAGE_REFRESH_SECONDS (default 300, floor 60) on this machine. The probe spends no
// model turn; a window agy does not report is shown as unknown.
func agyUsage(root, model string) func() string {
	seconds, err := clientenv.Limit("AGY_USAGE_REFRESH_SECONDS", int(agyusage.DefaultEvery/time.Second))
	if err != nil {
		seconds = int(agyusage.DefaultEvery / time.Second)
	}
	watcher := agyusage.NewWatcher(root, time.Duration(seconds)*time.Second)
	return func() string { return agyusage.Line(watcher.Reading(), model, time.Now()) }
}
