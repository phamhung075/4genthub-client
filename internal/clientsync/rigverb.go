// The `sync rig` verb: materialize a whole room in one command.
//
// PORTED FROM agenthub_client/src/agenthub_client/seat_sync.py's cmd_rig and the helpers it uses
// (pull_seat, swap_dir, materialize_agent, launch_spec, link_provider_key, omp_agent_dir,
// apply_policy, rig_members, session_name, launch_state_root, refuse_a_seat_that_reads_another_root,
// require_env, require_checker). The room is fetched as a rigspec, every listed seat is pulled with
// the same pinning rules as `pull`, and the rig directory is built in a staging directory and swapped
// in only after every seat is pinned - so a failed run leaves a previous rig directory untouched.
//
// TWO OWNER-SPECIFIC DEFAULTS OF THE PYTHON DO NOT SURVIVE, because a client that ships them cannot
// be used by anyone else:
//
//   - the per-room `.env` link is KEPT, but its source is the client's one env file
//     (internal/clientenv.DefaultPath()) and the trigger is any member whose model is written
//     `provider/model` (contains a "/"), not the Python's own model prefix and private key path;
//   - the per-seat policy table (the Python's compiled SEAT_ROLES) is not shipped. rigPolicyTable is
//     the seam a governed rig is supplied through; with no table a rig is left alone silently, which
//     is exactly what the Python does for a rig outside its table.
//
// THE CLIENT'S OWN SKILL TREE IS DELIVERED BY THIS VERB. `sync rig` is the step that writes each
// seat's agent directory, so it is also the step that gives a seat the skills this client owns: the
// tree is found at --skills-root, else $OPENRIG_CLIENT_SKILLS, else skills/ beside this binary, and a
// tree that is not there is REFUSED by name rather than skipped, because delivering it is the reason
// this verb reads one. For each seat internal/clientskills resolves the skills that seat receives
// (share/, then its room's room-level skills, then its own seat-level ones, and never another room's,
// another seat's or client/'s), installs them into <agent dir>/skills/, and declares the ids in the
// seat's agent spec - OpenRig loads a skill only when a profile selects it, so the copy alone is inert.
//
// WHAT IS REUSED RATHER THAN RESTATED: ReadRigspec and ValidateName (rig.go), PullSeat (pull.go),
// SafeRelative and ValidateHash (seatfiles.go), RequireEnv and DefaultSeatStore (statusverb.go),
// CheckerBinary/CheckerName/resolveChecker/describeFound/checkerLink and placeAgent/removePath
// (installcheckerverb.go), copyFile (offlineinstallverb.go). The YAML merge helper has no equivalent
// in this package (clientpolicy renders but does not merge), so it lives here, unexported and
// rig-prefixed.
package clientsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientskills"
)

const (
	// rigSeatPermissionPolicy is SEAT_PERMISSION_POLICY: the launch posture every materialized seat
	// gets. It resolves to a full-bypass launch, without which omp's always-ask floor cancels
	// approvals headlessly and the seat parks.
	rigSeatPermissionPolicy = "builtin:yolo"

	// The rendered omp documents inside a seat's snapshot, and the names omp reads them from in the
	// seat's own agent directory. The first and third install verbatim; the second is merged at the
	// key level because config.yml is omp's own file and may already hold other settings.
	rigOMPMCPRenderPath     = "runtime/omp-mcp.json"
	rigOMPMCPInstallName    = ".mcp.json"
	rigOMPConfigRenderPath  = "runtime/omp-config.yml"
	rigOMPConfigInstallName = "config.yml"
	rigAgentsMDRenderPath   = "AGENTS.md"
	rigAgentsMDInstallName  = "AGENTS.md"
)

// rigUsage is what `4genteam sync rig --help` prints.
const rigUsage = `usage: 4genteam sync rig ROOM [--out DIR] [--update] [--state-root DIR] [--skills-root DIR]

Materialize a whole room: fetch the room's rigspec, pull every listed seat with the same pinning
rules as ` + "`sync pull`" + `, then build:

  <out>/<room>/rig/rig.yaml       the server's spec with each member's cwd and permission policy set
  <out>/<room>/rig/agents/<seat>  a copy of that seat's pinned snapshot, with its pinned policy, the
                                  skills this client owns for that seat, and the spec declaring them
  <out>/<room>/.env               a link to the client's one env file, for a member with a
                                  provider/model model

The skills tree is at --skills-root, else $OPENRIG_CLIENT_SKILLS, else skills/ beside this binary, and
a tree that is not there is refused by name: each seat receives share/, then its room's room-level
skills, then its own seat-level skills, and never another room's, another seat's or client/'s.

The rig directory is built in a staging directory and swapped in only after every seat is pinned, so
a failed run leaves a previous rig directory untouched, and everything the build did not write is
carried over and named on stderr. Print only ` + "`rig:<path to rig.yaml>`" + ` on stdout.
`

// rigAgentSpecFile is the agent spec in a seat's agent directory: the document that lists the skills a
// profile selects, and therefore the file the skill delivery edits. A skill OpenRig is not told about
// is not loaded, which is why the delivery declares what it copies.
const rigAgentSpecFile = "agent.yaml"

// rigInstall is one resolved omp render write: the rendered file, its destination, and the mode
// ("verbatim" or "merge").
type rigInstall struct {
	rendered    string
	destination string
	mode        string
}

// rigGap is a seat whose render carried only half of the omp MCP pair.
type rigGap struct {
	seat    string
	missing string
}

// rigMember is one (pod id, member id) pair of a rig spec, in spec order.
type rigMember struct {
	Pod    string
	Member string
}

// --- seams, so a test drives the verb without a rig, a tmux server, a live state root or a key -----

// rigRequireChecker ports require_checker: the bare name must resolve to the store's own binary. A
// var so a test stubs the requirement, with rigCheckChecker split out so the test that wants the real
// check can put it back.
var rigRequireChecker = rigCheckChecker

// rigCheckChecker is the real require_checker.
func rigCheckChecker(out string, note io.Writer) error {
	if resolveChecker(out, note) {
		return nil
	}
	link, err := checkerLink()
	if err != nil {
		link = CheckerName
	}
	return &clientcmd.CodedError{
		Message: fmt.Sprintf(
			"%s does not resolve to %s on PATH (found: %s); run `4genteam sync install-checker` and "+
				"put %s on PATH (%s)",
			CheckerName, CheckerBinary(out), describeFound(), filepath.Dir(link), pathLimit),
		Code: clientcmd.ExitUsage,
	}
}

// rigDefaultStateRoot is the omp state root the Python called OMP_STATE_ROOT. A var so a test points
// it at a scratch tree instead of writing under the operator's live state root.
var rigDefaultStateRoot = clientenv.DefaultStateRoot

// rigEnvSource is the ONE env file a provider/model room links to. The Python linked a private
// product key path; here the source is the client's single env file.
var rigEnvSource = func() (string, error) { return clientenv.DefaultPath() }

// rigProcRoot is the process table launch roots are read from. A var so a test points it at a scratch
// tree instead of this box's /proc.
var rigProcRoot = "/proc"

// rigPolicyTable resolves a room's per-seat policy documents: a member id -> a rendered config.yml
// document, plus whether the table governs this room at all. The Go client ships NO role table (the
// Python's SEAT_ROLES named one owner's team), so the default answers "not governed" and a governed
// rig is supplied through this seam - which is why an ungoverned rig is left alone silently, exactly
// as the Python leaves a rig outside its table.
var rigPolicyTable = func(room string) (map[string]string, bool) { return nil, false }

// rigLaunchStateRoot is launch_state_root: the --state-root a running seat was launched with, or ""
// when that seat does not run.
//
// The Python reads the runner's own /proc/<pid>/cmdline. That table exists on linux only, so on any
// other platform this answers "nothing found" (and never refuses a run it cannot inspect) - the same
// answer as an unreadable /proc, which is also "nothing found" rather than a refusal.
var rigLaunchStateRoot = func(session string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return rigProcLaunchStateRoot(session)
}

// rigProcLaunchStateRoot is the /proc half of launch_state_root, split out so its own test can drive
// it against a scratch process table (rigProcRoot) without the platform guard's stub in the way.
func rigProcLaunchStateRoot(session string) string {
	entries, err := os.ReadDir(rigProcRoot)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(rigProcRoot, name, "cmdline"))
		if err != nil {
			continue
		}
		var args []string
		for _, part := range bytes.Split(raw, []byte{0}) {
			if len(part) > 0 {
				args = append(args, string(part))
			}
		}
		if !rigHasArg(args, "--session-name") || !rigHasArg(args, "--state-root") {
			continue
		}
		named := rigArgIndex(args, "--session-name") + 1
		if named >= len(args) || args[named] != session {
			continue
		}
		root := rigArgIndex(args, "--state-root") + 1
		if root < len(args) {
			return args[root]
		}
	}
	return ""
}

// rigMaterializeAgent is materialize_agent, a var so a test can drop a file into the live rig
// directory at the moment the seats are materialized (the window swap_dir's derivation closes).
var rigMaterializeAgent = func(source, seatDir, target string) error {
	removePath(target)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := rigCopyTree(source, target); err != nil {
		return err
	}
	// policy.json is the file seatcheck reads at runtime and pinned.json is the pin, so both travel
	// next to the rendered files a bundle will copy (G4 fix).
	for _, name := range []string{"policy.json", "pinned.json"} {
		candidate := filepath.Join(seatDir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if err := copyFile(candidate, filepath.Join(target, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// rigDeliverSkills gives one seat the skills this client owns, in the seat's freshly materialized
// agent directory. It returns false - and writes nothing - for a seat that receives skills but has no
// agent spec to declare them in, because a copy OpenRig is not told about is dead weight in the seat's
// tree; the caller names that seat. A seat the tree resolves nothing for is not a failure: it simply
// receives nothing, and no empty skills/ directory is left in its agent directory.
func rigDeliverSkills(tree clientskills.Tree, room, seat, agentDir string) (bool, error) {
	// Resolve once here, and again inside Install: a few directory reads, and the price of leaving a
	// seat with nothing to receive untouched. Install and Declare are the package's, not re-implemented.
	skills, err := tree.Resolve(room, seat)
	if err != nil {
		return false, err
	}
	if len(skills) == 0 {
		return true, nil
	}
	spec := filepath.Join(agentDir, rigAgentSpecFile)
	if info, err := os.Stat(spec); err != nil || info.IsDir() {
		return false, nil
	}
	ids, err := clientskills.Install(tree, room, seat, agentDir)
	if err != nil {
		return false, err
	}
	if _, err := clientskills.Declare(spec, ids); err != nil {
		return false, err
	}
	return true, nil
}

// --- the verb ---------------------------------------------------------------------------------------

// RunRigVerb is `sync rig ROOM`: validate, fetch the rigspec, pull every seat, build the rig in a
// staging directory and swap it in, then install the render's omp documents and the per-seat policy.
// The build also delivers the skills this client owns, which is why the tree is resolved before the
// first fetch.
func RunRigVerb(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			_, _ = io.WriteString(stdout, rigUsage)
			return clientcmd.ExitOK
		}
	}
	parsed, err := rigParseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync rig: %v\n", err)
		return clientcmd.ExitUsage
	}
	if err := rigRun(ctx, parsed, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
	}
	return clientcmd.ExitOK
}

// rigArgs is the parsed `rig` subparser.
type rigArgs struct {
	room          string
	hasRoom       bool
	out           string
	stateRoot     string
	haveStateRoot bool
	skillsRoot    string
	update        bool
}

// rigParseArgs is the Python's subparser for `rig`: a positional room, `--out`, `--update` and
// `--state-root`. An unknown flag or a missing room is a usage error, as argparse's exit 2 is.
func rigParseArgs(args []string) (rigArgs, error) {
	parsed := rigArgs{out: DefaultSeatStore()}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--out":
			if i+1 >= len(args) {
				return parsed, errors.New("--out needs a value")
			}
			i++
			parsed.out = args[i]
		case strings.HasPrefix(arg, "--out="):
			parsed.out = strings.TrimPrefix(arg, "--out=")
		case arg == "--update":
			parsed.update = true
		case arg == "--state-root":
			if i+1 >= len(args) {
				return parsed, errors.New("--state-root needs a value")
			}
			i++
			parsed.stateRoot = args[i]
			parsed.haveStateRoot = true
		case strings.HasPrefix(arg, "--state-root="):
			parsed.stateRoot = strings.TrimPrefix(arg, "--state-root=")
			parsed.haveStateRoot = true
		case arg == "--skills-root":
			if i+1 >= len(args) {
				return parsed, errors.New("--skills-root needs a value")
			}
			i++
			parsed.skillsRoot = args[i]
		case strings.HasPrefix(arg, "--skills-root="):
			parsed.skillsRoot = strings.TrimPrefix(arg, "--skills-root=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			return parsed, fmt.Errorf("unknown argument: %s", arg)
		default:
			// An EMPTY positional is still a positional: argparse hands it to validate_name, which
			// refuses it as an invalid room name rather than as a missing argument.
			if parsed.hasRoom {
				return parsed, fmt.Errorf("unknown argument: %s", arg)
			}
			parsed.room = arg
			parsed.hasRoom = true
		}
	}
	if !parsed.hasRoom {
		return parsed, errors.New("missing argument: room")
	}
	return parsed, nil
}

// rigRun is cmd_rig after argument parsing. Every refusal it raises carries its own exit code, so the
// caller reads it with clientcmd.CodeOf rather than deciding again.
func rigRun(ctx context.Context, args rigArgs, stdout, stderr io.Writer) error {
	room, err := ValidateName("room", args.room)
	if err != nil {
		return err
	}
	// The skills this client owns are delivered by this verb, so the tree is resolved BEFORE a single
	// fetch or write: a tree that is not there is the caller's error (exit 2) and must not cost a pull.
	tree, err := clientskills.Locate(args.skillsRoot)
	if err != nil {
		return err
	}
	if err := tree.Require(); err != nil {
		return err
	}
	out, err := filepath.Abs(expandHome(args.out))
	if err != nil {
		return &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
	}
	if err := rigRequireChecker(out, stderr); err != nil {
		return err
	}
	baseURL, err := RequireEnv("AGENTHUB_URL")
	if err != nil {
		return err
	}
	token, err := RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		return err
	}

	spec, err := ReadRigspec(ctx, baseURL, token, room)
	if err != nil {
		return err
	}
	stateRoot := rigDefaultStateRoot()
	if args.haveStateRoot {
		stateRoot = expandHome(args.stateRoot)
	}
	if stateRoot == "" {
		// No flag and no resolvable home: refuse rather than materialize a rig whose state tree sits
		// relative to wherever this was run from.
		return &clientcmd.CodedError{Message: clientenv.StateRootMessage, Code: clientcmd.ExitUsage}
	}
	if err := rigRefuseAnotherRoot(room, spec.YAML, stateRoot); err != nil {
		return err
	}

	pinned := make(map[string]string, len(spec.Seats))
	for _, seat := range spec.Seats {
		dir, err := PullSeat(ctx, baseURL, token, room, seat.Seat, out, args.update, stderr)
		if err != nil {
			return &clientcmd.CodedError{
				Message: fmt.Sprintf("seat %s could not be pulled: %v", seat.Seat, err),
				Code:    clientcmd.ExitRemote,
			}
		}
		pinned[seat.Seat] = dir
	}

	// Resolve every omp install BEFORE the build writes anything: this pass has no side effects, so a
	// refusal later in the run leaves nothing half-applied behind it.
	installs, gaps, err := rigOMPRenderInstalls(pinned, room, spec.YAML, stateRoot)
	if err != nil {
		return err
	}

	roomDir := filepath.Join(out, room)
	if err := os.MkdirAll(roomDir, 0o755); err != nil {
		return err
	}
	rigDir := filepath.Join(roomDir, "rig")
	staging, err := os.MkdirTemp(roomDir, ".rig.")
	if err != nil {
		return err
	}
	kept, undelivered, err := rigBuild(staging, rigDir, spec.YAML, roomDir, out, room, spec.Seats, pinned, tree)
	if err != nil {
		removeAllQuiet(staging)
		return err
	}

	for _, seat := range undelivered {
		fmt.Fprintf(stderr,
			"warning: %s carries no %s, so %s/%s received none of the skills this client owns: a "+
				"skill reaches a seat only when its agent spec declares it, and this seat's render "+
				"carries no spec to declare one in.\n",
			filepath.Join(rigDir, "agents", seat), rigAgentSpecFile, room, seat)
	}

	if len(kept) > 0 {
		fmt.Fprintf(stderr, "kept %d file(s) the build did not create in %s: %s\n",
			len(kept), rigDir, strings.Join(kept, ", "))
	}
	for _, install := range installs {
		if err := os.MkdirAll(filepath.Dir(install.destination), 0o755); err != nil {
			return err
		}
		rendered, err := os.ReadFile(install.rendered)
		if err != nil {
			return err
		}
		var changed bool
		if install.mode == "verbatim" {
			changed, err = rigWriteBytesIfChanged(rendered, install.destination)
		} else {
			changed, err = rigMergeConfigKey(rendered, install.destination)
		}
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintf(stderr, "installed %s\n", install.destination)
		}
	}
	for _, gap := range gaps {
		fmt.Fprintf(stderr,
			"warning: %s/%s rendered no %s, so that seat gets HALF of its omp MCP setup - a document "+
				"without the startup setting mounts servers omp may not wait for, and the setting "+
				"without a document waits for servers the seat has none of. A render that predates "+
				"the setting produces the first case.\n", room, gap.seat, gap.missing)
	}

	if documents, governed := rigPolicyTable(room); governed {
		members, err := rigMembers(spec.YAML)
		if err != nil {
			return err
		}
		for _, member := range members {
			document, listed := documents[member.Member]
			if !listed {
				fmt.Fprintf(stderr,
					"warning: %s/%s has no role in seat_policy, so no per-seat policy was delivered. "+
						"That table is the single definition of the rules and an unlisted seat has no "+
						"default, so this seat is running unpoliced.\n", room, member.Member)
				continue
			}
			agentDir := rigOMPAgentDir(rigSessionName(member.Pod, member.Member, room), stateRoot)
			if info, statErr := os.Stat(agentDir); statErr != nil || !info.IsDir() {
				fmt.Fprintf(stderr,
					"warning: no omp agent directory at %s, so %s/%s did not get the per-seat policy. "+
						"The directory appears once the seat has launched under omp, so run the client "+
						"again after that; a seat on another runtime never has one.\n",
					agentDir, room, member.Member)
				continue
			}
			changed, err := rigApplyPolicy(document, filepath.Join(agentDir, rigOMPConfigInstallName))
			if err != nil {
				return err
			}
			if changed {
				fmt.Fprintf(stderr, "applied the per-seat policy %s\n",
					filepath.Join(agentDir, rigOMPConfigInstallName))
			}
		}
	}

	fmt.Fprintf(stdout, "rig:%s\n", filepath.Join(rigDir, "rig.yaml"))
	return nil
}

// rigBuild writes the launch spec, materializes each seat's agent directory - with the skills this
// client owns installed and declared in that seat's spec - and swaps the staging directory onto the
// live rig directory. The caller removes the staging directory if it fails. The seats it could not
// deliver skills to are returned rather than printed: the caller knows the post-swap path to name.
func rigBuild(staging, rigDir, yamlText, roomDir, out, room string, seats []RigspecSeat, pinned map[string]string, tree clientskills.Tree) ([]string, []string, error) {
	specText, err := rigLaunchSpec(yamlText, roomDir)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(staging, "rig.yaml"), []byte(specText), 0o644); err != nil {
		return nil, nil, err
	}
	agents := filepath.Join(staging, "agents")
	if err := os.Mkdir(agents, 0o755); err != nil {
		return nil, nil, err
	}
	var undelivered []string
	for _, seat := range seats {
		agentDir := filepath.Join(agents, seat.Seat)
		if err := rigMaterializeAgent(pinned[seat.Seat],
			filepath.Join(out, room, seat.Seat), agentDir); err != nil {
			return nil, nil, err
		}
		delivered, err := rigDeliverSkills(tree, room, seat.Seat, agentDir)
		if err != nil {
			return nil, nil, err
		}
		if !delivered {
			undelivered = append(undelivered, seat.Seat)
		}
	}
	kept, err := rigSwapDir(staging, rigDir)
	return kept, undelivered, err
}

// --- spec reading and the launch spec -----------------------------------------------------------------

// rigSpecDocument parses a rig spec into its generic document. A spec that cannot be read raises the
// Python's usage error, which is what both rig_members and launch_spec rely on.
func rigSpecDocument(yamlText string) (map[string]any, error) {
	var document any
	if err := yaml.Unmarshal([]byte(yamlText), &document); err != nil {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("cannot read the rig spec: %v", err),
			Code:    clientcmd.ExitUsage,
		}
	}
	spec, ok := document.(map[string]any)
	if !ok {
		return nil, &clientcmd.CodedError{Message: "cannot read the rig spec", Code: clientcmd.ExitUsage}
	}
	return spec, nil
}

// rigMembers is rig_members: the (pod id, member id) pairs of a rig spec, in spec order.
func rigMembers(yamlText string) ([]rigMember, error) {
	spec, err := rigSpecDocument(yamlText)
	if err != nil {
		return nil, err
	}
	var pairs []rigMember
	for _, podAny := range rigAnyList(spec["pods"]) {
		pod, _ := podAny.(map[string]any)
		podID := rigYAMLString(pod["id"])
		for _, memberAny := range rigAnyList(pod["members"]) {
			member, _ := memberAny.(map[string]any)
			memberID := rigYAMLString(member["id"])
			if podID != "" && memberID != "" {
				pairs = append(pairs, rigMember{Pod: podID, Member: memberID})
			}
		}
	}
	return pairs, nil
}

// rigLaunchSpec is launch_spec: the server's spec with every member's cwd set to the room directory
// (which no build replaces) and the one permission policy set, plus the room's `.env` link.
func rigLaunchSpec(yamlText, roomDir string) (string, error) {
	spec, err := rigSpecDocument(yamlText)
	if err != nil {
		return "", err
	}
	var members []map[string]any
	for _, podAny := range rigAnyList(spec["pods"]) {
		pod, _ := podAny.(map[string]any)
		for _, memberAny := range rigAnyList(pod["members"]) {
			if member, ok := memberAny.(map[string]any); ok {
				members = append(members, member)
			}
		}
	}
	providerSeat := false
	for _, member := range members {
		member["cwd"] = roomDir
		member["permission_policy"] = rigSeatPermissionPolicy
		if strings.Contains(rigYAMLString(member["model"]), "/") {
			providerSeat = true
		}
	}
	if providerSeat {
		if err := rigLinkProviderKey(roomDir); err != nil {
			return "", err
		}
	}
	encoded, err := yaml.Marshal(spec)
	if err != nil {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("cannot write the rig spec: %v", err),
			Code:    clientcmd.ExitUsage,
		}
	}
	return string(encoded), nil
}

// rigLinkProviderKey is link_provider_key with the client's own seam: the room's `.env` links to the
// ONE env file for a room that has a provider/model seat. A regular file at that path is not ours and
// is REFUSED and named; a link of ours aimed at a key file that moved is retargeted.
func rigLinkProviderKey(roomDir string) error {
	source, err := rigEnvSource()
	if err != nil {
		return &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
	}
	if info, statErr := os.Stat(source); statErr != nil || info.IsDir() {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf(
				"the room has a provider/model seat and %s does not exist: omp reads the key from the "+
					"launch directory's .env", source),
			Code: clientcmd.ExitUsage,
		}
	}
	link := filepath.Join(roomDir, ".env")
	info, err := os.Lstat(link)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		if linked, statErr := os.Stat(link); statErr == nil {
			if srcInfo, srcErr := os.Stat(source); srcErr == nil && os.SameFile(linked, srcInfo) {
				return nil
			}
		}
		// Our own link, aimed at a key file that moved: retargeting is what the link is for.
		if err := os.Remove(link); err != nil {
			return &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
		}
	case err == nil:
		return &clientcmd.CodedError{
			Message: fmt.Sprintf(
				"%s already exists and is not a symlink: refusing to replace a file this client did "+
					"not create. Move it aside, or point it at %s, to use the one key.", link, source),
			Code: clientcmd.ExitUsage,
		}
	}
	if err := os.Symlink(source, link); err != nil {
		return &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
	}
	return nil
}

// --- the state-root guard -----------------------------------------------------------------------------

// rigRefuseAnotherRoot is refuse_a_seat_that_reads_another_root: a seat that is running under another
// omp state root than the one chosen is refused BEFORE anything is written, naming both roots and the
// flag, because writing the render where the seat's omp does not read it is a silent misplacement.
func rigRefuseAnotherRoot(room, yamlText, stateRoot string) error {
	members, err := rigMembers(yamlText)
	if err != nil {
		return err
	}
	for _, member := range members {
		session := rigSessionName(member.Pod, member.Member, room)
		running := rigLaunchStateRoot(session)
		if running == "" || filepath.Clean(running) == filepath.Clean(stateRoot) {
			continue
		}
		return &clientcmd.CodedError{
			Message: fmt.Sprintf(
				"%s/%s is running under the omp state root %s, but this run would install its render "+
					"under %s. Nothing was written. Pass --state-root %s to follow the launch, or stop "+
					"and relaunch the seat under %s.",
				room, member.Member, running, stateRoot, running, stateRoot),
			Code: clientcmd.ExitUsage,
		}
	}
	return nil
}

// rigSessionName is session_name: the session name the runner gives a member, `<pod>-<member>@<rig>`.
func rigSessionName(podID, member, room string) string {
	return podID + "-" + member + "@" + room
}

// rigOMPAgentDir is omp_agent_dir: where omp reads one seat's own MCP config.
func rigOMPAgentDir(session, stateRoot string) string {
	return filepath.Join(stateRoot, session, "agent")
}

// --- the render installs ------------------------------------------------------------------------------

// rigOMPRenderInstalls is omp_render_installs: resolve (rendered, destination, mode) for every seat's
// render, plus the half-renders. It has NO side effects, which is why it is separate from the writing
// loop: a refusal later in the run leaves no file and no empty agent directory behind.
func rigOMPRenderInstalls(snapshots map[string]string, room, yamlText, stateRoot string) ([]rigInstall, []rigGap, error) {
	bySeat := make(map[string]map[string]string)
	for seat, snapshot := range snapshots {
		present := make(map[string]string)
		for mode, relative := range map[string]string{
			"verbatim": rigOMPMCPRenderPath,
			"merge":    rigOMPConfigRenderPath,
			"agents":   rigAgentsMDRenderPath,
		} {
			candidate := filepath.Join(snapshot, relative)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				present[mode] = candidate
			}
		}
		if len(present) > 0 {
			bySeat[seat] = present
		}
	}
	if len(bySeat) == 0 {
		return nil, nil, nil
	}
	members, err := rigMembers(yamlText)
	if err != nil {
		return nil, nil, err
	}
	var installs []rigInstall
	var gaps []rigGap
	for _, member := range members {
		present, ok := bySeat[member.Member]
		if !ok {
			continue
		}
		agentDir := rigOMPAgentDir(rigSessionName(member.Pod, member.Member, room), stateRoot)
		if rendered, ok := present["verbatim"]; ok {
			installs = append(installs, rigInstall{rendered, filepath.Join(agentDir, rigOMPMCPInstallName), "verbatim"})
		}
		if rendered, ok := present["merge"]; ok {
			installs = append(installs, rigInstall{rendered, filepath.Join(agentDir, rigOMPConfigInstallName), "merge"})
		}
		if rendered, ok := present["agents"]; ok {
			// Independent of the MCP pair: a seat with guides and no mcp block gets only this one.
			installs = append(installs, rigInstall{rendered, filepath.Join(agentDir, rigAgentsMDInstallName), "verbatim"})
		}
		_, hasVerbatim := present["verbatim"]
		_, hasMerge := present["merge"]
		if hasVerbatim != hasMerge {
			missing := rigOMPConfigRenderPath
			if hasMerge {
				missing = rigOMPMCPRenderPath
			}
			gaps = append(gaps, rigGap{member.Member, missing})
		}
	}
	return installs, gaps, nil
}

// --- the config mergers --------------------------------------------------------------------------------

// rigMergeConfig is _merged_config: existing with the rendered keys set on top; mappings merge, leaves
// overwrite. owned names the top-level keys the rendered document owns WHOLE - those are set rather
// than merged, so a list or map the document no longer carries cannot linger.
func rigMergeConfig(rendered, existing map[string]any, owned ...string) map[string]any {
	ownedSet := make(map[string]bool, len(owned))
	for _, key := range owned {
		ownedSet[key] = true
	}
	out := make(map[string]any, len(existing)+len(rendered))
	for key, value := range existing {
		out[key] = value
	}
	for key, value := range rendered {
		if ownedSet[key] {
			out[key] = value
			continue
		}
		if renderedMap, ok := value.(map[string]any); ok {
			if existingMap, ok := out[key].(map[string]any); ok {
				out[key] = rigMergeConfig(renderedMap, existingMap)
				continue
			}
		}
		out[key] = value
	}
	return out
}

// rigMergeConfigKey is merge_config_key: set the rendered keys in target (omp's own config.yml),
// preserving every other key. True when the file changed. Idempotence is semantic - when the target
// already carries every rendered key at the rendered value, nothing is written and no mtime is
// touched, so a rebuild is not a diff. A target that does not exist is written with the render's own
// bytes.
func rigMergeConfigKey(fragment []byte, target string) (bool, error) {
	var renderedDoc any
	if err := yaml.Unmarshal(fragment, &renderedDoc); err != nil {
		return false, &clientcmd.CodedError{
			Message: fmt.Sprintf("the rendered omp config is not readable YAML: %v", err),
			Code:    clientcmd.ExitUsage,
		}
	}
	rendered, ok := renderedDoc.(map[string]any)
	if !ok {
		return false, &clientcmd.CodedError{
			Message: "the rendered omp config is not a mapping", Code: clientcmd.ExitUsage,
		}
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		if err := os.WriteFile(target, fragment, 0o644); err != nil {
			return false, err
		}
		return true, nil
	}
	existing, err := rigReadMapping(target, "refusing to set the rendered keys in",
		"This merge must not clobber a file it cannot read.")
	if err != nil {
		return false, err
	}
	merged := rigMergeConfig(rendered, existing)
	if reflect.DeepEqual(merged, existing) {
		return false, nil
	}
	encoded, err := yaml.Marshal(merged)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(target, encoded, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// rigApplyPolicy is apply_policy: apply the per-seat policy document to target, the runtime's own
// config.yml. THE POLICY OWNS ITS NAMESPACES: `bash` and `tools` are set whole, because a deny the
// policy dropped must not linger. `mcp` is SHARED, so it merges key by key. When the file holds
// nothing but policy keys the policy's own bytes are written verbatim, keeping its header comment.
func rigApplyPolicy(document, target string) (bool, error) {
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		if err := os.WriteFile(target, []byte(document), 0o644); err != nil {
			return false, err
		}
		return true, nil
	}
	text, err := os.ReadFile(target)
	if err != nil {
		return false, err
	}
	var renderedDoc, existingDoc any
	if err := yaml.Unmarshal([]byte(document), &renderedDoc); err != nil {
		return false, &clientcmd.CodedError{
			Message: fmt.Sprintf("refusing to apply the per-seat policy in %s: it is not readable YAML (%v).", target, err),
			Code:    clientcmd.ExitUsage,
		}
	}
	if err := yaml.Unmarshal(text, &existingDoc); err != nil {
		return false, &clientcmd.CodedError{
			Message: fmt.Sprintf("refusing to apply the per-seat policy in %s: it is not readable YAML (%v).", target, err),
			Code:    clientcmd.ExitUsage,
		}
	}
	rendered, renderedOK := renderedDoc.(map[string]any)
	existing, existingOK := existingDoc.(map[string]any)
	if existingDoc == nil {
		existing, existingOK = map[string]any{}, true
	}
	if !renderedOK || !existingOK {
		return false, &clientcmd.CodedError{
			Message: fmt.Sprintf("refusing to apply the per-seat policy in %s: its top level is not a mapping", target),
			Code:    clientcmd.ExitUsage,
		}
	}
	if rigKeySubset(existing, rendered) {
		if string(text) == document {
			return false, nil
		}
		if err := os.WriteFile(target, []byte(document), 0o644); err != nil {
			return false, err
		}
		return true, nil
	}
	merged := rigMergeConfig(rendered, existing, "bash", "tools")
	if reflect.DeepEqual(merged, existing) {
		return false, nil
	}
	encoded, err := yaml.Marshal(merged)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(target, encoded, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// rigReadMapping reads a YAML mapping from target, refusing anything that is not one. The refusal is
// assembled from the caller's two sentences so both merge_config_key and apply_policy keep the
// Python's own wording.
func rigReadMapping(target, lead, tail string) (map[string]any, error) {
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s %s: it is not readable YAML (%v). %s", lead, target, err, tail),
			Code:    clientcmd.ExitUsage,
		}
	}
	var document any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s %s: it is not readable YAML (%v). %s", lead, target, err, tail),
			Code:    clientcmd.ExitUsage,
		}
	}
	if document == nil {
		return map[string]any{}, nil
	}
	mapping, ok := document.(map[string]any)
	if !ok {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s %s: its top level is not a mapping", lead, target),
			Code:    clientcmd.ExitUsage,
		}
	}
	return mapping, nil
}

// rigWriteBytesIfChanged is write_bytes_if_changed: write only when the file differs. The render is
// deterministic, so a rebuild that produces the same document leaves the file (and its mtime) alone.
func rigWriteBytesIfChanged(data []byte, target string) (bool, error) {
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, data) {
			return false, nil
		}
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// --- the staging-and-swap build -----------------------------------------------------------------------

// rigSwapDir is swap_dir: move staging onto target, restoring target if the move fails. Everything the
// caller did not write is carried over, and WHAT THE CALLER WROTE is derived HERE, from the two
// directories: entries of the old target the new one lacks are moved back in, and their names are
// returned. A name the new directory already has is left alone. Deriving this at swap time closes the
// window between the read and the swap, so a file that lands WHILE the seats are being materialized
// survives too.
func rigSwapDir(staging, target string) ([]string, error) {
	var backup string
	if _, err := os.Lstat(target); err == nil {
		dir, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+".old.")
		if err != nil {
			return nil, err
		}
		if err := os.Remove(dir); err != nil {
			return nil, err
		}
		backup = dir
		if err := os.Rename(target, backup); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return nil, err
	}
	if backup == "" {
		return nil, nil
	}
	written := map[string]bool{}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		written[entry.Name()] = true
	}
	older, err := os.ReadDir(backup)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(older))
	for _, entry := range older {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	var kept []string
	for _, name := range names {
		if written[name] {
			continue
		}
		if err := os.Rename(filepath.Join(backup, name), filepath.Join(target, name)); err != nil {
			return nil, err
		}
		kept = append(kept, name)
	}
	removeAllQuiet(backup)
	return kept, nil
}

// rigCopyTree copies a directory tree. It follows symlinks (shutil.copytree's symlinks=False), and the
// snapshots it copies are plain files, so no link is recreated as a link.
func rigCopyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		return copyFile(path, destination)
	})
}

// --- small helpers ------------------------------------------------------------------------------------

// rigAnyList is the Python's `value or []`: a nil or non-list value is the empty list.
func rigAnyList(value any) []any {
	list, _ := value.([]any)
	return list
}

// rigYAMLString is the Python's `str(value or "")` for the id fields: nil is "", a string is itself,
// and any other scalar is rendered the way fmt does. Specs name these fields as strings in practice.
func rigYAMLString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func rigHasArg(args []string, want string) bool { return rigArgIndex(args, want) >= 0 }

func rigArgIndex(args []string, want string) int {
	for index, arg := range args {
		if arg == want {
			return index
		}
	}
	return -1
}

// rigKeySubset is set(existing) <= set(rendered): every key the file carries is one the document
// defines.
func rigKeySubset(existing, rendered map[string]any) bool {
	for key := range existing {
		if _, ok := rendered[key]; !ok {
			return false
		}
	}
	return true
}
