// Package clientseat is the `seat` verb group: keep the local seats in step with the cloud.
//
// The cloud owns what a seat is and resolves it into a snapshot named by a hash. This package compares
// the hash each local seat is pinned to with the cloud's current one, adopts the newer snapshot THROUGH
// this client's own `sync room` verb (so the pinning rules stay in one place), and optionally restarts
// the affected seat so it loads it.
//
// Three deliberate departures from the Python, all recorded in the phase report:
//   - the seat store and the rig orchestrator state root are named, not derived from $HOME, because a seat runs
//     with HOME pointed at its own state directory (seat_policy's real_home() exists for the same
//     reason);
//   - adoption spawns this binary rather than a Python interpreter;
//   - `seat sync` therefore needs `sync room`, which is not ported yet, so it refuses loudly until it is.
package clientseat

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

// Exit codes, the Python seat_client's: ok, usage, failed, and behind (a seat whose pin differs).
const (
	// ExitFailed is a sync, relaunch or cloud step that failed. It shares the number 3 with
	// clientcmd.ExitUnavailable but answers a different question, so it is named here.
	ExitFailed = 3
)

const (
	quietSeconds     = 30.0
	quietWaitSeconds = 1500.0
	pollSeconds      = 15.0
)

// SeatHash is one seat and the hash the cloud would serve for it. It is a slice rather than a map
// because the ORDER the cloud lists seats in is the order the Python reported them in, and an operator
// reads that.
type SeatHash struct {
	Seat string
	Hash string
}

// Commands is what the dispatcher registers from this package.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{seatCommand{}, sendCommand{}}
}

type seatCommand struct{}

func (seatCommand) Name() string    { return "seat" }
func (seatCommand) Summary() string { return "status, sync and watch of a room's seats" }

func (seatCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "4genteam seat: pick a verb: status, sync, watch, reseat")
		return clientcmd.ExitUsage
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, "usage: 4genteam seat (status ROOM | sync ROOM | watch ROOM) [--out DIR] [--seat NAME]...\n       4genteam seat reseat RIG --seat NAME [--seat NAME]...")
		fmt.Fprintln(stdout, "\nstatus compares each seat's local pin with the cloud's hash; sync adopts the newer snapshot")
		fmt.Fprintln(stdout, "through this client's own `sync room` and, with --relaunch quiet, restarts what changed; watch")
		fmt.Fprintln(stdout, "repeats sync until you stop it. Every sub-verb answers --help.")
		return clientcmd.ExitOK
	}
	switch args[0] {
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "sync":
		return runSync(args[1:], stdout, stderr)
	case "watch":
		return runWatch(args[1:], stdout, stderr)
	case "reseat":
		return runReseat(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "4genteam seat: unknown verb %q; the verbs are status, sync, watch, reseat\n", args[0])
	return clientcmd.ExitUsage
}

// options is what the three sub-verbs share.
type options struct {
	room     string
	out      string
	seats    []string
	rig      string
	relaunch string
	interval int
}

func (o options) rigName() string {
	if o.rig != "" {
		return o.rig
	}
	return o.room
}

// seatsFiltered is selected(): the cloud's seats, limited to the ones named with --seat.
func seatsFiltered(cloud []SeatHash, wanted []string) []SeatHash {
	if len(wanted) == 0 {
		return cloud
	}
	var out []SeatHash
	for _, entry := range cloud {
		for _, seat := range wanted {
			if seat == entry.Seat {
				out = append(out, entry)
				break
			}
		}
	}
	return out
}

type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

// parseOptions reads a sub-verb's flags. The second result is the code the caller returns when ok is
// false: 0 when the flag was --help (which every sub-verb answers), 2 for a bad or missing argument.
func parseOptions(verb string, args []string, withRig, withInterval bool, stdout, stderr io.Writer) (options, int, bool) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintf(stdout, "usage: 4genteam seat %s ROOM [--out DIR] [--seat NAME]...%s%s\n",
			verb, rigSuffix(withRig), intervalSuffix(withInterval))
		return options{}, clientcmd.ExitOK, false
	}
	flags := flag.NewFlagSet("seat "+verb, flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "where the adopted snapshots and their pins live")
	var seats repeatedFlag
	flags.Var(&seats, "seat", "limit the work to this seat (repeatable)")
	rig := flags.String("room", "", "rig name when it differs from the room")
	relaunch := flags.String("relaunch", "none", "also restart changed seats: none or quiet")
	interval := flags.Int("interval", 60, "seconds between passes")
	if err := flags.Parse(args); err != nil {
		return options{}, clientcmd.ExitUsage, false
	}
	if flags.NArg() < 1 {
		fmt.Fprintf(stderr, "4genteam seat %s: a ROOM is required\n", verb)
		return options{}, clientcmd.ExitUsage, false
	}
	if *relaunch != "none" && *relaunch != "quiet" {
		fmt.Fprintf(stderr, "4genteam seat %s: --relaunch must be none or quiet\n", verb)
		return options{}, clientcmd.ExitUsage, false
	}
	store, err := clientenv.ResolveSeatStore(*out)
	if err != nil {
		// Every `seat` verb takes --out, so the refusal names it: the rule lives in clientenv, which is
		// why the bridge (no such flag) shows the reason alone.
		err = clientenv.SeatStoreRefusal(err, clientenv.SeatStoreOutFlag)
		fmt.Fprintf(stderr, "4genteam seat %s: %v\n", verb, err)
		return options{}, clientcmd.ExitUsage, false
	}
	return options{
		room: flags.Arg(0), out: store, seats: seats,
		rig: *rig, relaunch: *relaunch, interval: *interval,
	}, clientcmd.ExitOK, true
}

func rigSuffix(withRig bool) string {
	if withRig {
		return " [--room RIG] [--relaunch none|quiet]"
	}
	return ""
}

func intervalSuffix(withInterval bool) string {
	if withInterval {
		return " [--interval SECONDS]"
	}
	return ""
}

// source is the two reads seat needs from the world: the cloud's hashes, and one seat's local pin.
type source interface {
	CloudHashes(room string) ([]SeatHash, error)
	ReadPin(out, room, seat string) (string, bool, error)
}

// cloudSource is the real source: the cloud over HTTP and the pins on disk.
type cloudSource struct{}

func (cloudSource) CloudHashes(room string) ([]SeatHash, error) {
	baseURL, err := clientsync.RequireEnv("AGENTHUB_URL")
	if err != nil {
		return nil, err
	}
	token, err := clientsync.RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		return nil, err
	}
	rigspec, err := clientsync.FetchRigspec(context.Background(), baseURL, token, room)
	if err != nil {
		return nil, err
	}
	listed, ok := rigspec["seats"].([]any)
	if !ok {
		return nil, &clientcmd.CodedError{Message: "the rigspec carries no seats", Code: clientcmd.ExitRemote}
	}
	hashes := make([]SeatHash, 0, len(listed))
	for _, entry := range listed {
		row, isObject := entry.(map[string]any)
		if !isObject {
			return nil, &clientcmd.CodedError{Message: "the rigspec carries a malformed seat entry", Code: clientcmd.ExitRemote}
		}
		seat, seatOK := row["seat"].(string)
		hash, hashOK := row["hash"].(string)
		if !seatOK || !hashOK {
			return nil, &clientcmd.CodedError{Message: "the rigspec seat entry has no seat or no hash", Code: clientcmd.ExitRemote}
		}
		hashes = append(hashes, SeatHash{Seat: seat, Hash: hash})
	}
	return hashes, nil
}

func (cloudSource) ReadPin(out, room, seat string) (string, bool, error) {
	lock, err := clientsync.ReadLock(filepath.Join(out, room, seat, "pinned.json"))
	if err != nil {
		return "", false, err
	}
	if lock.Hash == "" {
		return "", false, nil
	}
	return lock.Hash, true, nil
}

// SeatsBehind is seats_behind: the seats whose pin differs from the cloud's hash, or that were never
// pulled. The order is the cloud's.
func SeatsBehind(cloud []SeatHash, pinned map[string]string) []string {
	var behind []string
	for _, entry := range cloud {
		if got, ok := pinned[entry.Seat]; !ok || got != entry.Hash {
			behind = append(behind, entry.Seat)
		}
	}
	return behind
}

func pinnedHashes(src source, out, room string, cloud []SeatHash) (map[string]string, error) {
	pins := map[string]string{}
	for _, entry := range cloud {
		hash, ok, err := src.ReadPin(out, room, entry.Seat)
		if err != nil {
			return nil, err
		}
		if ok {
			pins[entry.Seat] = hash
		}
	}
	return pins, nil
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	parsed, code, ok := parseOptions("status", args, false, false, stdout, stderr)
	if !ok {
		return code
	}
	out := expand(parsed.out)
	src := source(cloudSource{})
	cloud, err := src.CloudHashes(parsed.room)
	if err != nil {
		return report(stderr, err)
	}
	cloud = seatsFiltered(cloud, parsed.seats)
	pins, err := pinnedHashes(src, out, parsed.room, cloud)
	if err != nil {
		return report(stderr, err)
	}
	behind := map[string]bool{}
	for _, seat := range SeatsBehind(cloud, pins) {
		behind[seat] = true
	}
	for _, entry := range cloud {
		pinned, hasPin := pins[entry.Seat]
		state := "in sync"
		if behind[entry.Seat] {
			if hasPin {
				state = "BEHIND"
			} else {
				state = "not pulled"
			}
		}
		fmt.Fprintf(stdout, "%-14s pinned %-12s  cloud %-12s  %s\n",
			entry.Seat, short(pinned), short(entry.Hash), state)
	}
	if len(behind) > 0 {
		return clientcmd.ExitBehind
	}
	return clientcmd.ExitOK
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func report(stderr io.Writer, err error) int {
	var coded *clientcmd.CodedError
	if errors.As(err, &coded) {
		fmt.Fprintf(stderr, "error: %v\n", coded)
		if coded.Code == clientcmd.ExitUsage {
			return clientcmd.ExitUsage
		}
		return ExitFailed
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	return ExitFailed
}

func expand(path string) string {
	if strings.HasPrefix(path, "~") {
		if home, err := clientenv.RealHome(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
