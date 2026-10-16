// The budget a room runs under: the ceilings the room's safeguard enforces, written by the owner and read by
// `budget-run`. The file is the room's own - rooms/<room>/limits.json - and a SEAT may not raise what bounds it:
// THIS VERB refuses a raise, or `--off`, asked for by a seat, naming $AGENTHUB_SESSION_NAME (raisesTheCeiling;
// the usage carries the matrix). The SEAT GUARD is the second layer, and it is at TWO STATES: the repo's asset
// (rust/agenthub-runtime/assets/seat-guard.ts, blob 2e978d1 at main) refuses every write shape it parses on this
// file, while the copy a seat actually RUNS is the one the RUNTIME BINARY embeds and writes into
// <seat dir>/hooks/pre/seat-guard.ts when it prepares the seat (supervisor/mod.rs:272,641) - the installed binary
// still embeds the asset as of 194343c (blob d18048e5), which refuses only an edit tool's path, a redirect, `tee`
// and `cp` here, so `sed -i`, `rm`, `truncate` and `dd of=` pass until the runtime is rebuilt, installed and the
// seats restarted; a RESEAT re-installs that same older copy, because it comes from the same binary. Measured
// 2026-10-16 by driving all three copies; both share one residual: a path opened from INSIDE a program
// (`python3 -c "open(...)"`, `node -e "writeFileSync(...)"`) or spelled through a variable.
//
// The marker is read as a PAIR (os.LookupEnv, not Getenv), so a marker that is SET counts as a seat even when it
// is EMPTY: `AGENTHUB_SESSION_NAME= 4genteam ...` is refused like any other seat's raise, where it used to write
// 999 - measured 2026-10-16 at d964b8b. It is still a SPEED BUMP, not a wall, and ABSENCE is what gets past it:
// with 25/300 in force, `--usd 777` exits 3 and leaves the file alone, while `env --unset=AGENTHUB_SESSION_NAME`
// writes 888 and a variable holding the name writes 889; a program that cleans its own environment and execs the
// verb writes 999. The guard's hook-bypass rule refuses only the two literal spellings, `env -u
// AGENTHUB_SESSION_NAME` and `unset AGENTHUB_SESSION_NAME`. So the refusal stops the honest route, not a
// determined one; what bounds a seat regardless of who wrote the file is the safeguard itself (`budget-run`), not
// this verb.
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
and OFF is how a room says it has none.

A SEAT MAY NOT RAISE A CEILING OR TURN THE CEILINGS OFF: this verb refuses that, naming $AGENTHUB_SESSION_NAME,
because a ceiling exists to stop the seat it is measured against. A seat may still LOWER one or turn them ON, and
the OWNER raises one by running this with $AGENTHUB_SESSION_NAME unset. The FILE itself is the SEAT GUARD's second
layer, and its two copies differ: the REPO's asset refuses an edit tool's path, a redirect, tee, cp/mv/install/
rsync/ln, rm/unlink/truncate/shred, sed -i/perl -i and dd of= on this file, while the copy a seat RUNS - the one
the runtime binary embeds, written into the seat's hooks/pre directory when it prepares the seat - is the asset as
of 194343c and refuses only an edit path, a redirect, tee and cp. So sed -i, rm, truncate and dd of= still pass
here until the runtime is rebuilt, installed and the seats restarted; a reseat re-installs the same older copy.
Neither copy sees a path opened from inside a program (python3 -c, node -e) or spelled through a variable.

THE MARKER IS READ AS A PAIR: the OWNER is the caller with NO marker, and a marker that is SET is a seat even when
its value is EMPTY - "AGENTHUB_SESSION_NAME= 4genteam room limits ROOM --usd 999" is refused exactly like the
unemptied spelling (measured 2026-10-16 at d964b8b, where the empty spelling wrote 999). It is still a SPEED BUMP,
NOT A WALL, and ABSENCE is what gets past it: "env --unset=AGENTHUB_SESSION_NAME", a variable holding the name, or
a program that cleans its own environment and execs the verb writes the raise, and the guard refuses only the
literal "env -u AGENTHUB_SESSION_NAME" and "unset AGENTHUB_SESSION_NAME".`

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
	// A marker that is SET counts as a seat even when its value is EMPTY: the shell hands a seat
	// `AGENTHUB_SESSION_NAME= 4genteam room limits <room> --usd 999` for three characters, and measured
	// 2026-10-16 at d964b8b that raise was WRITTEN (999, exit 0) while the same seat's set-value attempt was
	// refused, because os.Getenv cannot tell an empty marker from an absent one. The OWNER is the caller with NO
	// marker; every other caller is a seat.
	if caller, marked := os.LookupEnv(sessionEnv); marked && (hasUSD || hasCalls || off) {
		name := strings.TrimSpace(caller)
		if name == "" {
			name = "a seat whose $" + sessionEnv + " is empty"
		}
		if reason := raisesTheCeiling(limits, hasUSD, usd, hasCalls, calls, off); reason != "" {
			fmt.Fprintf(stderr, "4genteam room limits: %s does not raise the ceiling that bounds it - %s. A seat may LOWER a "+
				"ceiling or turn it ON; the OWNER raises one, running this with $%s unset (a number the file does not carry yet is "+
				"not a ceiling, so writing that one is not a raise).\n", name, reason, sessionEnv)
			return clientcmd.ExitRoomRefused
		}
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

// raisesTheCeiling names the raise a request asks for, or "" when it tightens (or changes nothing). A SEAT runs
// through this, because the seat a ceiling exists to stop is the caller that must not lift it: measured on the
// real binary before this rule, `room limits --usd 999` from a seat wrote 999 and `--off` disabled the ceilings.
// The comparison is against the numbers the FILE carries, never the defaults: a number the file does not carry
// is not a ceiling yet, so writing that one raises nothing, and a number that cannot be read is left to the
// usage error below rather than judged here.
//
// The marker ($AGENTHUB_SESSION_NAME) is the same one the rail and the git hooks take a seat's identity from,
// and it is a SPEED BUMP, not a wall: a seat that unsets it claims to be the owner. Closing that is the
// destructive-write work (parked with the owner), not this verb's.
func raisesTheCeiling(limits roombudget.Limits, hasUSD bool, usd string, hasCalls bool, calls string, off bool) string {
	if off && limits.Enabled {
		return "turning the ceilings OFF lifts them all at once"
	}
	if hasUSD {
		if value, err := ceiling("usd", usd); err == nil && limits.USDPerSeat24h > 0 && value > limits.USDPerSeat24h {
			return fmt.Sprintf("--usd %s is above the %.2f USD/24h in force", strings.TrimSpace(usd), limits.USDPerSeat24h)
		}
	}
	if hasCalls {
		if value, err := ceiling("calls", calls); err == nil && limits.CallsPerSeatMin > 0 && value > float64(limits.CallsPerSeatMin) {
			return fmt.Sprintf("--calls %s is above the %d calls/min in force", strings.TrimSpace(calls), limits.CallsPerSeatMin)
		}
	}
	return ""
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
