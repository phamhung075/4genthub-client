// The `sync bundle` verb: build a .rigbundle from the snapshot a seat is PINNED to, and say at BUILD
// time what the bundle does not contain.
//
// The pin is the whole point. The bundle is built from the rig root and the output path, but the
// seat's resolved snapshot comes from the lock this client wrote - the same hash a running seat was
// launched from - so the bundle the operator ships and the seat that is running are the same artifact.
//
// A bundle copies `<rig-root>/agents/<agent>/` verbatim, and seatcheck reads a seat's policy from the
// pin that directory is supposed to hold. When the rig root carries no usable pin the artifact still
// passes its own integrity check, and the first sign of trouble is offline-install refusing it later -
// so the build WARNS here, on stderr, and never refuses.
package clientsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"gopkg.in/yaml.v3"
)

const bundleUsage = `usage: 4genteam sync bundle ROOM SEAT --rig-yaml PATH --rig-root DIR [--out-dir DIR]

Build a .rigbundle from the snapshot ROOM/SEAT is pinned to, then print its path. The pinned hash is
the one used, so the bundle and the running seat are the same artifact.
`

// bundleArgs is the parsed command line.
type bundleArgs struct {
	room    string
	seat    string
	rigYAML string
	rigRoot string
	outDir  string
}

// RunBundleVerb is `sync bundle ROOM SEAT --rig-yaml PATH --rig-root DIR [--out-dir DIR]`.
func RunBundleVerb(_ context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			_, _ = io.WriteString(stdout, bundleUsage)
			return clientcmd.ExitOK
		}
	}
	parsed, err := bundleParseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.ExitUsage
	}
	return bundleCreate(parsed, stdout, stderr)
}

// bundleParseArgs is the Python's subparser for `bundle`: two positionals, --rig-yaml and --rig-root
// REQUIRED, and --out-dir optional (default: the current directory).
func bundleParseArgs(args []string) (bundleArgs, error) {
	var parsed bundleArgs
	var positionals []string
	needValue := func(flag string, index int) error {
		if index+1 >= len(args) {
			return usageError(flag + " needs a value")
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--rig-yaml":
			if err := needValue(arg, i); err != nil {
				return parsed, err
			}
			i++
			parsed.rigYAML = args[i]
		case strings.HasPrefix(arg, "--rig-yaml="):
			parsed.rigYAML = strings.TrimPrefix(arg, "--rig-yaml=")
		case arg == "--rig-root":
			if err := needValue(arg, i); err != nil {
				return parsed, err
			}
			i++
			parsed.rigRoot = args[i]
		case strings.HasPrefix(arg, "--rig-root="):
			parsed.rigRoot = strings.TrimPrefix(arg, "--rig-root=")
		case arg == "--out-dir":
			if err := needValue(arg, i); err != nil {
				return parsed, err
			}
			i++
			parsed.outDir = args[i]
		case strings.HasPrefix(arg, "--out-dir="):
			parsed.outDir = strings.TrimPrefix(arg, "--out-dir=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			return parsed, usageError("unknown argument: " + arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 2 {
		return parsed, usageError("missing argument: room, seat")
	}
	parsed.room, parsed.seat = positionals[0], positionals[1]
	if parsed.rigYAML == "" {
		return parsed, usageError("missing argument: --rig-yaml")
	}
	if parsed.rigRoot == "" {
		return parsed, usageError("missing argument: --rig-root")
	}
	return parsed, nil
}

// bundleCreate ports cmd_bundle's body. It reads the pin, packs the bundle (bundlepack.go), and then warns about the seats the rig root leaves unpinned.
func bundleCreate(parsed bundleArgs, stdout, stderr io.Writer) int {
	room, err := ValidateName("room", parsed.room)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.ExitUsage
	}
	seat, err := ValidateName("seat", parsed.seat)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.ExitUsage
	}

	// seatStore directly, not resolveSeatStore: `sync bundle` takes no store flag of its own - its
	// --out-dir is where the bundle is written - so its refusal keeps the shared reason alone rather than
	// naming a flag the verb does not accept.
	store, err := seatStore("")
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.ExitUsage
	}
	seatDir := filepath.Join(store, room, seat)
	lockPath := filepath.Join(seatDir, "pinned.json")
	if !lockPresent(lockPath) {
		fmt.Fprintf(stderr,
			"4genteam sync bundle: no pinned snapshot for %s/%s; run `4genteam sync pull %s %s` first\n",
			room, seat, room, seat)
		return clientcmd.ExitUsage
	}
	lock, err := ReadLock(lockPath)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	pinnedHash, err := ValidateHash(lock.Hash)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.ExitUsage
	}

	outDir := parsed.outDir
	if outDir == "" {
		outDir = "."
	}
	outDir, err = filepath.Abs(outDir)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.ExitRemote
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: %v\n", err)
		return clientcmd.ExitRemote
	}
	bundlePath := filepath.Join(outDir,
		fmt.Sprintf("%s-%s-%s.rigbundle", room, seat, bundleHashPrefix(pinnedHash, 8)))

	// The locked hash - not the cloud's - is what travels in the notes.
	spec := bundleSpec{
		name: room + "-" + seat, version: "1.0.0",
		notes:   "resolved_sha256=" + pinnedHash + " source=4genteam",
		rigYAML: parsed.rigYAML, rigRoot: parsed.rigRoot,
	}
	if err := bundlePack(spec, bundlePath); err != nil {
		fmt.Fprintf(stderr, "4genteam sync bundle: cannot build the bundle: %v\n", err)
		return clientcmd.ExitRemote
	}

	missing := bundleUnpinnedSeats(parsed.rigYAML, parsed.rigRoot)
	if len(missing) > 0 {
		details := make([]string, 0, len(missing))
		for _, entry := range missing {
			details = append(details, fmt.Sprintf("%s (%s)", entry.seat, entry.reason))
		}
		fmt.Fprintf(stderr,
			"warning: %d seat(s) in this bundle have no usable pinned policy, so a seat launched from it "+
				"offline cannot decide or audit and an offline install will refuse it - %s. Build the rig "+
				"root with `4genteam sync rig <room>`, which materializes each seat's policy.json into its "+
				"agent directory.\n",
			len(missing), strings.Join(details, "; "))
	}
	fmt.Fprintln(stdout, bundlePath)
	return clientcmd.ExitOK
}

// bundleUnpinned is one seat the rig root cannot hand a usable pin.
type bundleUnpinned struct {
	seat   string
	reason string
}

// bundleUnpinnedSeats ports unpinned_seats: the seats of the spec whose agent directory holds no usable
// pinned policy. An unreadable spec or root answers NOTHING TO SAY (an empty list) rather than a warning
// about the check itself, which is the Python's rule.
func bundleUnpinnedSeats(rigYAML, rigRoot string) []bundleUnpinned {
	raw, err := os.ReadFile(rigYAML)
	if err != nil {
		return nil
	}
	var spec any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return nil
	}
	root, ok := spec.(map[string]any)
	if !ok {
		return nil
	}
	pods, _ := root["pods"].([]any)

	var missing []bundleUnpinned
	for _, podValue := range pods {
		pod, _ := podValue.(map[string]any)
		members, _ := pod["members"].([]any)
		for _, memberValue := range members {
			member, _ := memberValue.(map[string]any)
			seat, _ := member["id"].(string)
			ref, _ := member["agent_ref"].(string)
			agent := bundleAgentFromRef(ref)
			if seat == "" || agent == "" {
				continue
			}
			if _, err := SafeRelative(agent); err != nil {
				continue
			}
			policy := filepath.Join(rigRoot, "agents", agent, "policy.json")
			info, statErr := os.Stat(policy)
			if statErr != nil || info.IsDir() {
				missing = append(missing, bundleUnpinned{seat, "no policy.json in agents/" + agent})
				continue
			}
			contents, readErr := os.ReadFile(policy)
			if readErr != nil {
				missing = append(missing, bundleUnpinned{seat, "the policy in agents/" + agent + " is unreadable"})
				continue
			}
			var document map[string]any
			if err := json.Unmarshal(contents, &document); err != nil {
				missing = append(missing, bundleUnpinned{seat, "the policy in agents/" + agent + " is unreadable"})
				continue
			}
			// Two seats sharing one seat type share one agent directory in a bundle, so only one of
			// their policies can ride there; the seat that lost is named here.
			if claimed, isString := document["Seat"].(string); isString && claimed != seat {
				missing = append(missing, bundleUnpinned{seat,
					"the policy in agents/" + agent + " belongs to seat '" + claimed + "'"})
			}
		}
	}
	return missing
}

// bundleAgentFromRef is the Python's `ref.partition("local:agents/")[2]`: everything after the FIRST
// occurrence of the prefix, and the empty string when it is absent.
func bundleAgentFromRef(ref string) string {
	const prefix = "local:agents/"
	index := strings.Index(ref, prefix)
	if index < 0 {
		return ""
	}
	return ref[index+len(prefix):]
}

// bundleHashPrefix is the Python's `pinned_hash[:8]`, safe for a hash shorter than eight characters
// (which validate_hash would not normally allow, but the slice must not panic).
func bundleHashPrefix(hash string, length int) string {
	if len(hash) <= length {
		return hash
	}
	return hash[:length]
}
