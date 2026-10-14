package clientsync

import (
	"fmt"
	"io"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// SeatsBehind returns, in the order the cloud listed them, the seats whose local pin differs from the
// cloud's hash or is missing entirely. The exit code that goes with it is clientcmd.ExitBehind (4,
// measured from the retired Python seat client's EXIT_BEHIND - it was 1 in the first draft here).
//
// PORTED FROM the retired Python seat client's seats_behind, whose test is the parity spec:
//
//	cloud  = lead:h2, go-dev:h1, writer:h3
//	pinned = lead:h1, go-dev:h1, writer:None   ->  [lead, writer]
//
// The order argument carries the cloud's listing order, because a Go map has none and the Python
// returns list(cloud) - so the parity is in the ORDER as well as the membership.
func SeatsBehind(order []string, cloud map[string]string, pinned map[string]string) []string {
	var behind []string
	for _, seat := range order {
		hash, ok := cloud[seat]
		if !ok {
			continue
		}
		if pin, pinned := pinned[seat]; !pinned || pin != hash {
			behind = append(behind, seat)
		}
	}
	return behind
}

// StatusState is the word the status line carries for one seat.
//
// THE "not pulled" CASE IS ABSENCE, NOT AN EMPTY PIN, and that is the Python's own distinction:
//
//	state = "in sync" if seat not in behind else ("not pulled" if pinned is None else "BEHIND")
//
// An empty-string pin is what a lock file carrying `"hash": ""` yields, and the Python renders that as
// BEHIND - only a MISSING entry is None. An earlier version of this function folded the two together,
// which read a malformed-but-valid lock as a seat that was never pulled.
func StatusState(seat string, cloud map[string]string, pinned map[string]string) string {
	pin, present := pinned[seat]
	switch {
	case present && pin == cloud[seat]:
		return "in sync"
	case !present:
		return "not pulled"
	default:
		return "BEHIND"
	}
}

// FormatStatus renders the status table in the shape the Python client prints:
//
//	{seat:<14} pinned {pin[:12]:<12}  cloud {hash[:12]:<12}  {state}
//
// A missing pin shows as "None" in that column, because that is what str(None)[:12] produces - the
// parity is the rendered line, not only the word beside it.
func FormatStatus(w io.Writer, order []string, cloud map[string]string, pinned map[string]string) {
	behind := map[string]bool{}
	for _, seat := range SeatsBehind(order, cloud, pinned) {
		behind[seat] = true
	}
	for _, seat := range order {
		hash, ok := cloud[seat]
		if !ok {
			continue
		}
		pinDisplay := "None"
		if pin, present := pinned[seat]; present && pin != "" {
			pinDisplay = pin
		}
		state := StatusState(seat, cloud, pinned)
		if !behind[seat] {
			state = "in sync"
		}
		fmt.Fprintf(w, "%-14s pinned %-12s  cloud %-12s  %s\n",
			seat, truncate(pinDisplay, 12), truncate(hash, 12), state)
	}
}

// RunStatus is the `sync status` verb: it renders the table and exits behind-or-ok. The cloud fetch and
// the pin read are the caller's, so this function is the shape of the verb rather than its wiring.
func RunStatus(w io.Writer, order []string, cloud map[string]string, pinned map[string]string) int {
	FormatStatus(w, order, cloud, pinned)
	if len(SeatsBehind(order, cloud, pinned)) > 0 {
		return clientcmd.ExitBehind
	}
	return clientcmd.ExitOK
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
