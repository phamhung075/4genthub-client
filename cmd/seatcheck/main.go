// Command seatcheck scans observed command lines for sends that never met the audited path.
//
// The audited send path is `4genteam send` (`internal/clientseat`): one process builds the sending seat's
// policy at send time, decides, writes the decision line, delivers, and writes the outcome line, so both
// halves of a send's audit belong to the sender. This binary used to carry a `send` verb of its own that
// checked a pinned policy and delivered the message; that verb is DELETED, not deprecation-stubbed, and
// what is left here - `audit-scan` - only reads.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/commpolicy"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, topUsage)
		return 2
	}
	switch args[0] {
	case "audit-scan":
		return runAuditScan(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		// Help is help: the flag package answers -h itself inside audit-scan, and the guard's own
		// usage must be reachable the same way. It goes to STDOUT with exit 0, because 2 is this
		// command's usage code - a help request returning it is indistinguishable from a caller's
		// mistake to anything reading the exit status.
		fmt.Fprintln(stdout, topUsage)
		return 0
	default:
		// `send` lands here and that is the point: it is an UNKNOWN subcommand now, not a stub that
		// still refuses for its own reason. The refusal is therefore the one every unknown
		// subcommand gets, and it NAMES the surviving route - `4genteam send` - which is what a seat
		// typing the deleted spelling needs to read.
		fmt.Fprintf(stderr, "seatcheck: unknown subcommand %q\n%s\n", args[0], topUsage)
		return 2
	}
}

// topUsage is the guard's own usage line: to STDERR with exit 2 when no subcommand was given (that is a
// mistake), to STDOUT with exit 0 when help was asked for. It names the one surviving subcommand, and it
// names the route a seat sends through instead, because the `send` verb this command used to carry is gone:
// a seat that types the old spelling is pointed at the audited path rather than left guessing.
const topUsage = "usage: seatcheck audit-scan ...\n" +
	"to message another seat, run `4genteam send` (seatcheck does not send)"

// helpOrUsageError turns one flag set's answer into an exit code.
//
// -h/--help is a REQUEST for help: flag.ContinueOnError makes the flag package answer it itself with
// flag.ErrHelp, and the usage then goes to STDOUT with exit 0 - 2 is this command's usage code, so a help
// request returning it cannot be told apart from a mistyped flag. EVERY OTHER parse failure is that mistake:
// the usage on stderr with exit 2, exactly as before.
func helpOrUsageError(err error, usage string, stdout, stderr io.Writer) int {
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

// auditScanUsage is what `seatcheck audit-scan` prints, on the same rule as helpOrUsageError.
const auditScanUsage = "usage: seatcheck audit-scan --known <wrapper names comma separated> --file <path to a text file of observed command lines, one per line>"

func runAuditScan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit-scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	known := fs.String("known", "", "comma separated wrapper command names")
	file := fs.String("file", "", "file of observed command lines, one per line")
	// Silenced because the stream depends on the ANSWER rather than on the flag set: see helpOrUsageError.
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		return helpOrUsageError(err, auditScanUsage, stdout, stderr)
	}
	if *file == "" {
		fmt.Fprintln(stderr, "seatcheck audit-scan: --file is required")
		return 2
	}

	var names []string
	for _, name := range strings.Split(*known, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "seatcheck audit-scan: read file: %v\n", err)
		return 2
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if commpolicy.BypassSuspected(names, line) {
			fmt.Fprintf(stdout, "bypass-suspected: %s\n", line)
			found = true
		}
	}
	if found {
		return 4
	}
	return 0
}
