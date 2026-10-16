// The budget a room runs under: the ceilings the room's safeguard enforces, written by the owner and read by
// `budget-run`. The file is the room's own - rooms/<room>/limits.json - and the guard refuses a seat a write
// to it, so an owner changes it here rather than a seat changing it under itself.
package clientroom

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

const limitsUsage = `usage: 4genteam room limits ROOM [--usd N] [--calls N] [--on|--off] [--state-dir DIR] [--socket PATH]

Reads or writes <state-dir>/rooms/ROOM/limits.json, the file the room's budget safeguard (` + "`4genteam budget-run --room ROOM`" + `)
enforces. With none of --usd/--calls/--on/--off it prints the limits in force and writes nothing; with any of them
it writes the file, keeping the numbers it does not name.

The limits SHIP OFF: while they are off the safeguard measures nothing, whatever numbers the file carries.
  --usd N    park a seat that spends more than N USD in 24h
  --calls N  park a seat that makes more than N tool calls in a minute, held for two passes
  --on       turn the ceilings on
  --off      turn them off without changing the numbers

A number that is not named keeps the value the file already carries, and a file that does not exist yet starts at
25 USD/24h and 300 calls/min - the ceilings this room agreed. Zero is refused: a ceiling of zero is not a ceiling,
and OFF is how a room says it has none.`

func runLimits(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, limitsUsage)
		return clientcmd.ExitOK
	}
	opts, rest, err := clientruntime.SplitFlags(args, queueFlagSpecs(
		clientruntime.FlagSpec{Name: "usd", Value: true},
		clientruntime.FlagSpec{Name: "calls", Value: true},
		clientruntime.FlagSpec{Name: "on"},
		clientruntime.FlagSpec{Name: "off"},
	))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room limits: %v. %s\n", err, limitsUsage)
		return clientcmd.ExitRoomRefused
	}
	pairArgs := append(append(make([]string, 0, len(rest)+4), rest...), "--state-dir", opts["state-dir"], "--socket", opts["socket"])
	inv, code, ok := parseInvocation("limits", pairArgs, 1, stdout, stderr)
	if !ok {
		return code
	}
	roomDir := filepath.Join(inv.stateDir, "rooms", inv.room)
	if info, err := os.Stat(roomDir); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "4genteam room limits: no room directory at %s\n", roomDir)
		return clientcmd.ExitRoomRefused
	}
	_, on := opts["on"]
	_, off := opts["off"]
	if on && off {
		fmt.Fprintf(stderr, "4genteam room limits: --on and --off are two answers to one question. %s\n", limitsUsage)
		return clientcmd.ExitRoomRefused
	}
	usd, hasUSD := opts["usd"]
	calls, hasCalls := opts["calls"]
	limits, err := roombudget.Load(roomDir)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room limits: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	if hasUSD || hasCalls || on || off {
		if limits.USDPerSeat24h == 0 {
			limits.USDPerSeat24h = roombudget.DefaultUSDPerSeat24h
		}
		if limits.CallsPerSeatMin == 0 {
			limits.CallsPerSeatMin = roombudget.DefaultCallsPerSeatMin
		}
		if hasUSD {
			value, err := ceiling("usd", usd)
			if err != nil {
				fmt.Fprintf(stderr, "4genteam room limits: %v. %s\n", err, limitsUsage)
				return clientcmd.ExitRoomRefused
			}
			limits.USDPerSeat24h = value
		}
		if hasCalls {
			value, err := ceiling("calls", calls)
			if err != nil {
				fmt.Fprintf(stderr, "4genteam room limits: %v. %s\n", err, limitsUsage)
				return clientcmd.ExitRoomRefused
			}
			limits.CallsPerSeatMin = int(value)
		}
		if on {
			limits.Enabled = true
		}
		if off {
			limits.Enabled = false
		}
		if err := roombudget.Save(roomDir, limits); err != nil {
			fmt.Fprintf(stderr, "4genteam room limits: %v\n", err)
			return clientcmd.ExitRoomRefused
		}
	}
	inv.printPair(stdout)
	printLimits(stdout, roomDir, limits)
	return clientcmd.ExitOK
}

// ceiling reads one ceiling: a positive number, refused by name otherwise. A ceiling of zero is not a ceiling -
// it would park every seat on its first call - so zero is a usage error and not a way to say "no limit".
func ceiling(name, text string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("--%s takes a positive number, not %q", name, text)
	}
	return value, nil
}

// printLimits is the one rendering of the limits in force: the file they live in, whether they are on, and the
// two ceilings with the window each is counted over.
func printLimits(out io.Writer, roomDir string, limits roombudget.Limits) {
	state := "off - nothing is measured"
	if limits.Enabled {
		state = "on"
	}
	fmt.Fprintf(out, "limits: %s\nstate: %s\nper seat: %.2f USD/24h, %d calls/min\n",
		roombudget.Path(roomDir), state, limits.USDPerSeat24h, limits.CallsPerSeatMin)
}
