package clientsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// offlineInstallUsage is what `sync offline-install --help` prints.
const offlineInstallUsage = `usage: 4genteam sync offline-install TARGET [--home DIR]

Install a materialized bundle's pinned policy into the seat store. seatcheck resolves its policy as
<home>/.config/4genthub/seats/<room>/<member>/policy.json and it is deliberately not a flag, so
this is what makes an offline seat's allowed path able to decide and audit. TARGET is the
materialized bundle; HOME defaults to this account's home directory.
`

// rigSpec is only the part of a rig spec this verb reads: the room's name and each member's id and
// agent reference. Everything else in the document is none of its business.
type rigSpec struct {
	Name string `yaml:"name"`
	Pods []struct {
		Members []struct {
			ID       string `yaml:"id"`
			AgentRef string `yaml:"agent_ref"`
		} `yaml:"members"`
	} `yaml:"pods"`
}

// RunOfflineInstallVerb is `sync offline-install`: the bundle's pinned policy becomes the policy the
// seat store at <home>/.config/4genthub/seats holds, which is the only place seatcheck looks.
func RunOfflineInstallVerb(_ context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			_, _ = io.WriteString(stdout, offlineInstallUsage)
			return clientcmd.ExitOK
		}
	}
	target, home, err := parseOfflineInstallArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
		return clientcmd.ExitUsage
	}
	return offlineInstall(target, home, stdout, stderr)
}

func parseOfflineInstallArgs(args []string) (target, home string, err error) {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--home":
			if i+1 >= len(args) {
				return "", "", errors.New("--home needs a value")
			}
			i++
			home = args[i]
		case strings.HasPrefix(arg, "--home="):
			home = strings.TrimPrefix(arg, "--home=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			return "", "", fmt.Errorf("unknown argument: %s", arg)
		default:
			if target != "" {
				return "", "", fmt.Errorf("unknown argument: %s", arg)
			}
			target = arg
		}
	}
	if target == "" {
		return "", "", errors.New("missing argument: target")
	}
	return target, home, nil
}

func offlineInstall(target, home string, stdout, stderr io.Writer) int {
	root, err := filepath.Abs(expandHome(target))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
		return clientcmd.ExitUsage
	}
	spec, code := readRigSpec(filepath.Join(root, "rig.yaml"), stderr)
	if code != clientcmd.ExitOK {
		return code
	}
	rig, err := ValidateName("room", spec.Name)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
		return clientcmd.ExitUsage
	}
	homeDir := home
	if homeDir == "" {
		if homeDir, err = clientenv.RealHome(); err != nil {
			fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
			return clientcmd.ExitUsage
		}
	}
	store := clientenv.SeatStoreUnder(expandHome(homeDir))

	var installed []string
	for _, pod := range spec.Pods {
		for _, member := range pod.Members {
			seat, err := ValidateName("seat", member.ID)
			if err != nil {
				fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
				return clientcmd.ExitUsage
			}
			agent := agentFromRef(member.AgentRef)
			if agent == "" {
				fmt.Fprintf(stderr, "skipped %s/%s: no local agent ref\n", rig, seat)
				continue
			}
			if _, err := SafeRelative(agent); err != nil {
				fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
				return clientcmd.ExitUsage
			}
			agentDir := filepath.Join(root, "agents", agent)
			policy := filepath.Join(agentDir, "policy.json")
			if info, err := os.Stat(policy); err != nil || info.IsDir() {
				fmt.Fprintf(stderr, "skipped %s/%s: no pinned policy in the bundle (%s is missing)\n",
					rig, seat, policy)
				continue
			}
			// Two members that share one seat type share one agent directory in the bundle, so only
			// one of their policies can ride there. Refuse to install a policy that names a
			// DIFFERENT seat, rather than silently handing this seat the wrong links.
			claimed, err := claimedSeat(policy)
			if err != nil {
				fmt.Fprintf(stderr, "4genteam sync offline-install: cannot read %s: %v\n", policy, err)
				return clientcmd.ExitUsage
			}
			if claimed != "" && claimed != seat {
				fmt.Fprintf(stderr,
					"skipped %s/%s: the bundle's %s policy belongs to seat '%s' (members sharing one "+
						"seat type share one agent directory in a bundle; per-seat policy needs distinct "+
						"seat types)\n", rig, seat, agent, claimed)
				continue
			}
			seatDir := filepath.Join(store, rig, seat)
			if err := os.MkdirAll(seatDir, 0o755); err != nil {
				fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
				return clientcmd.ExitUsage
			}
			if err := copyFile(policy, filepath.Join(seatDir, "policy.json")); err != nil {
				fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
				return clientcmd.ExitUsage
			}
			pinned := filepath.Join(agentDir, "pinned.json")
			if info, err := os.Stat(pinned); err == nil && !info.IsDir() {
				if err := copyFile(pinned, filepath.Join(seatDir, "pinned.json")); err != nil {
					fmt.Fprintf(stderr, "4genteam sync offline-install: %v\n", err)
					return clientcmd.ExitUsage
				}
			}
			installed = append(installed, rig+"/"+seat)
		}
	}
	if len(installed) == 0 {
		fmt.Fprintf(stderr,
			"4genteam sync offline-install: no pinned policy found under %s/agents; build the bundle "+
				"from a rig root that carries it (4genteam sync rig / bundle does since 2026-10-05)\n",
			root)
		return clientcmd.ExitUsage
	}
	for _, name := range installed {
		fmt.Fprintf(stdout, "installed %s -> %s\n", name, filepath.Join(store, name))
	}
	return clientcmd.ExitOK
}

// readRigSpec parses the bundle's rig.yaml: a missing file, invalid YAML and a document that is not
// a spec are three different usage errors, and saying which one it was is the whole point.
func readRigSpec(path string, stderr io.Writer) (rigSpec, int) {
	text, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync offline-install: cannot read %s: %v\n", path, err)
		return rigSpec{}, clientcmd.ExitUsage
	}
	var document any
	if err := yaml.Unmarshal(text, &document); err != nil {
		fmt.Fprintf(stderr, "4genteam sync offline-install: %s is not valid YAML: %v\n", path, err)
		return rigSpec{}, clientcmd.ExitUsage
	}
	if _, ok := document.(map[string]any); !ok {
		fmt.Fprintf(stderr, "4genteam sync offline-install: %s is not a rig spec\n", path)
		return rigSpec{}, clientcmd.ExitUsage
	}
	var spec rigSpec
	if err := yaml.Unmarshal(text, &spec); err != nil {
		fmt.Fprintf(stderr, "4genteam sync offline-install: %s is not a rig spec: %v\n", path, err)
		return rigSpec{}, clientcmd.ExitUsage
	}
	return spec, clientcmd.ExitOK
}

// agentFromRef is the agent directory a member's reference names: `local:agents/<dir>` is local,
// anything else is not this bundle's to install.
func agentFromRef(ref string) string {
	const prefix = "local:agents/"
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	return strings.TrimPrefix(ref, prefix)
}

// claimedSeat is the seat a pinned policy says it belongs to, or "" when it does not say.
func claimedSeat(path string) (string, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var document map[string]any
	if err := json.Unmarshal(text, &document); err != nil {
		return "", err
	}
	claimed, _ := document["Seat"].(string)
	return claimed, nil
}

// copyFile copies one file's bytes and its permission bits, which is what shutil.copy2 leaves behind.
func copyFile(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, info.Mode().Perm())
}
