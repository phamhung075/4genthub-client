package clientheal

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

const usage = "usage: 4genteam heal ROOM [--check] [--json] [--state-dir DIR] [--socket PATH]"

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
			"Exit 0 when the room is clean or every finding was fixed, 3 when something is left.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("heal", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "report only; fix nothing")
	asJSON := flags.Bool("json", false, "print the findings as JSON")
	stateDir := flags.String("state-dir", "", "the state root: the flag, else ~/.config/4genthub")
	socket := flags.String("socket", "", "the runtime daemon's socket")
	var flagArgs, rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--state-dir" || args[i] == "--socket":
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
	root, err := clientenv.ResolveStateRoot(*stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam heal: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	room := rest[0]
	client := clientruntime.NewClient(*socket)
	findings := Run(room, machineDeps(root, client, !*check))
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

func machineDeps(root string, client *clientruntime.Client, fix bool) Deps {
	return Deps{
		StateRoot: root,
		Fix:       fix,
		Seats:     client.GetRoomStatus,
		Reseat: func(room, seat string) error {
			if err := client.StopSeat(room, seat); err != nil {
				return err
			}
			_, err := client.RestartSeat(room, seat)
			return err
		},
		ProcEnv: procEnv,
		Rows: func(room string) []clientservices.Health {
			state, _ := clientservices.ReadAgentState()
			return clientservices.Verify(context.Background(), clientservices.Env{Rigs: []string{room}, Supervised: state.Safeguards})
		},
		Up: func(room string) error {
			self, err := os.Executable()
			if err != nil {
				return err
			}
			out, err := exec.Command(self, "up", room).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, lastLine(string(out)))
			}
			return nil
		},
		Cause: func() string {
			lines := clientservices.TailAlerts()
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.Contains(lines[i], "refused") || strings.Contains(lines[i], "NOT") {
					return lines[i]
				}
			}
			return ""
		},
	}
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
