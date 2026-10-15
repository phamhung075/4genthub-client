// Package clientseat is the `seat` verb group: the local side of a room's seats - reseating a seat, and
// the quiet wait that reseat and the runtime's own paths share.
//
// WHAT IS NOT HERE ANY MORE. `seat status`, `seat sync` and `seat watch` compared each local seat's pin
// with the hash the cloud serves, and the route that served that hash
// (`GET /api/v2/rooms/{room}/rigspec`) was deleted server-side (cbc51bae). The client's fetch went with
// it, so the three verbs are gone rather than kept to print an error - a verb that can only fail is a
// deprecation shim, and this repo's rule is a clean break. The operator's sentence is in the CHANGELOG
// entry this change carries.
package clientseat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// repeatedFlag is `--seat NAME` more than once: reseat takes one per seat it restarts.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

// ExitFailed is a reseat step that failed. It shares the number 3 with clientcmd.ExitUnavailable but
// answers a different question, so it is named here.
const ExitFailed = 3

const (
	quietSeconds     = 30.0
	quietWaitSeconds = 1500.0
	pollSeconds      = 15.0
)

// Commands is what the dispatcher registers from this package.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{seatCommand{}, sendCommand{}}
}

type seatCommand struct{}

func (seatCommand) Name() string    { return "seat" }
func (seatCommand) Summary() string { return "reseat a room's seats" }

func (seatCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "4genteam seat: pick a verb: reseat")
		return clientcmd.ExitUsage
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, "usage: 4genteam seat reseat RIG --seat NAME [--seat NAME]...")
		fmt.Fprintln(stdout, "\nreseat waits for each named seat to go quiet and restarts it, so the seat comes back")
		fmt.Fprintln(stdout, "on what it is pinned to. The wait's rules are shared with the runtime's own path.")
		return clientcmd.ExitOK
	}
	switch args[0] {
	case "reseat":
		return runReseat(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "4genteam seat: unknown verb %q; the verbs are reseat\n", args[0])
	return clientcmd.ExitUsage
}

// report turns a failure into the exit code the Python's main used: a usage error stays 2, anything else
// is this package's ExitFailed.
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
