package clientheal

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientlifecycle"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

const usage = "usage: 4genteam heal ROOM [--check] [--json] " + clientoffpeak.ModeFlagUsage + " [--state-root DIR] [--socket PATH]"

// peakGate is the DeepSeek peak gate heal asks before it restarts a seat; a var so the suite can date the
// run without waiting for a real peak window. The SOCKET-AWARE one: heal's gate reads the daemon its
// `--socket` resolved, which is the daemon it is about to stop and restart.
var peakGate = clientlifecycle.PeakGateAt

// Commands registers the verb.
func Commands() []clientcmd.Command { return []clientcmd.Command{healCommand{}} }

type healCommand struct{}

func (healCommand) Name() string { return "heal" }
func (healCommand) Summary() string {
	return "detect what stops a room working (seat env, MCP config, down seats, safeguards) and fix it"
}

func (healCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, usage)
		fmt.Fprintln(stdout, "Checks one room: the env file has the keys a seat needs, every seat is running, holds them and has\n"+
			"its MCP config, and every safeguard runs. Each finding is fixed (reseat a seat, `up` for the safeguards)\n"+
			"and checked again; what only the owner can fix is reported with its cause. --check fixes nothing.\n"+
			"A restart is a launch, so the DeepSeek peak gate runs before a seat is reseated: with "+
			clientoffpeak.ModeEnv+" on\n(the default) a room whose seats run a deepseek/ model leaves its down seats to"+
			" the operator until off-peak, as an unfixed finding\nnaming the seat, and "+clientoffpeak.FlagAllowPeak+
			" reseats them now.\n"+
			"Exit 0 when the room is clean or every finding was fixed, 3 when something is left.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("heal", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "report only; fix nothing")
	asJSON := flags.Bool("json", false, "print the findings as JSON")
	socket := flags.String("socket", "", "the runtime daemon's socket")
	// THE ROOT IS READ THROUGH THE ONE PARSER, so the scan below only has to carry the socket: the root is
	// stripped wherever it sits and the runtime's spelling is refused here, with this verb's code.
	rootFlag, args, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam heal: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUsage)
	}
	// THE GATE'S FLAG LEAVES THE LIST BEFORE THE FLAG SET SEES IT: every other dash-word below is routed into a
	// set that refuses what it does not own, so `--allow-peak` read any later would be a usage error instead of
	// the operator's override.
	allowPeak, args := clientoffpeak.ModeFromFlags(args)
	var flagArgs, rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--socket":
			flagArgs = append(flagArgs, args[i])
			if i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		case strings.HasPrefix(args[i], "-"):
			flagArgs = append(flagArgs, args[i])
		default:
			rest = append(rest, args[i])
		}
	}
	if err := flags.Parse(flagArgs); err != nil || len(rest) != 1 {
		fmt.Fprintln(stderr, "4genteam heal: one room name is required. "+usage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam heal: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	room := rest[0]
	client := clientruntime.NewClient(*socket)
	findings, probeErr := Run(room, machineDeps(root, client, !*check, allowPeak, stderr))
	if probeErr != nil {
		// NOTHING IS APPLIED ON A REPORT THAT WAS NOT READ (7.12): the probe is named, this verb exits 3, and
		// the json is not printed - an empty finding list would read as "clean".
		fmt.Fprintf(stderr, "4genteam heal: no safeguard row was decided: %v\n", probeErr)
		return clientcmd.ExitUnavailable
	}
	if *asJSON {
		body, _ := json.MarshalIndent(findings, "", "  ")
		fmt.Fprintln(stdout, string(body))
	} else if len(findings) == 0 {
		fmt.Fprintf(stdout, "%s: clean\n", room)
	}
	left := 0
	for _, f := range findings {
		if !*asJSON {
			fmt.Fprintf(stdout, "%-9s %-12s %s: %s\n", f.Result, f.Check, f.Target, f.Detail)
		}
		if f.Result != "fixed" {
			left++
		}
	}
	if left > 0 {
		return clientcmd.ExitUnavailable
	}
	return clientcmd.ExitOK
}

func machineDeps(root string, client *clientruntime.Client, fix, allowPeak bool, stderr io.Writer) Deps {
	return Deps{
		StateRoot: root,
		Fix:       fix,
		Seats:     client.GetRoomStatus,
		Reseat: func(room, seat string) error {
			// THE GATE BEFORE THE STOP: heal's repair is a stop and a restart, and a restart is a launch that
			// bills at peak. A refusal returns before StopSeat, so the seat is left exactly as the run found it,
			// and the error is what the finding carries - by seat name, as "unfixed", which is the exit-3 path.
			if err := gateRestart(client.SocketPath, room, allowPeak, stderr); err != nil {
				return err
			}
			if err := client.StopSeat(room, seat); err != nil {
				return err
			}
			// NO DOCUMENT: a heal restarts a seat that died, from the room's own record, and the record carries
			// the document every start and reseat wrote into it - so the seat comes back on what it was on. A
			// fresh render is what a RESEAT is for, and that verb fetches one.
			_, err := client.RestartSeat(room, seat, nil)
			return err
		},
		ProcEnv: procEnv,
		Rows: func(room string) ([]clientservices.Health, error) {
			state, _ := clientservices.ReadAgentState()
			return clientservices.Verify(context.Background(), clientservices.Env{Root: root, Rooms: []string{room}, Supervised: state.Safeguards})
		},
		Up: func(room string) error {
			self, err := os.Executable()
			if err != nil {
				return err
			}
			// THE ROOT IS PASSED ON: heal reads its findings against ITS root, so the repair must start the
			// room in that same root - a child up left to resolve its own default would repair a different
			// machine state than the one that was diagnosed.
			out, err := exec.Command(self, "up", room, "--state-root", root).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, lastLine(string(out)))
			}
			return nil
		},
		Cause: func() string {
			lines := clientservices.TailAlerts(root)
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.Contains(lines[i], "refused") || strings.Contains(lines[i], "NOT") {
					return lines[i]
				}
			}
			return ""
		},
	}
}

// gateRestart is the peak gate heal asks before it stops and restarts a seat.
//
// It writes the refusal to the caller's stderr AND returns it as an error: the stderr line is what an
// operator watching the run sees, and the error is what the finding carries - so the seat is named, the
// finding reads "unfixed", and the verb exits 3 rather than reporting a repair it did not make.
func gateRestart(socket, room string, allowPeak bool, stderr io.Writer) error {
	var refusal strings.Builder
	if code := peakGate(socket, room, "heal", allowPeak, io.MultiWriter(stderr, &refusal)); code != clientcmd.ExitOK {
		if text := strings.TrimSpace(refusal.String()); text != "" {
			return errors.New(text)
		}
		return fmt.Errorf("the peak gate refused with exit %d and gave no reason", code)
	}
	return nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return lines[len(lines)-1]
}

// procEnv finds the seat's agent process by the room and seat variables the runtime sets, and answers which
// of the required keys it holds. Only /proc is read, so another OS answers false and the check is skipped.
func procEnv(room, seat string) (map[string]bool, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			continue
		}
		vars := map[string]string{}
		for _, kv := range strings.Split(string(raw), "\x00") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				vars[k] = v
			}
		}
		if vars["AGENTHUB_ROOM"] == room && vars["AGENTHUB_SEAT"] == seat && vars["PI_CODING_AGENT_DIR"] != "" {
			held := map[string]bool{}
			for _, key := range RequiredKeys {
				held[key] = vars[key] != ""
			}
			return held, true
		}
	}
	return nil, false
}
