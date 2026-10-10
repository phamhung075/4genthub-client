// The `sync switch` verb: change the LLM of a seat. The CLOUD records the new occupant first, and
// OpenRig applies it where it can - which is not always, and the verb says so rather than pretending.
//
// THE CLOUD CHANGE IS THE AUTHORITY, so it is recorded even when OpenRig cannot be reached: a missing
// rig or a seat absent from `rig ps` still exits 0 with `applied=none`, because the operator's intent
// has been recorded and only the local half is outstanding.
//
// A RUNTIME CHANGE CANNOT BE APPLIED IN PLACE. OpenRig has no runtime-swap primitive, so a changed
// runtime prints the manual steps and touches no rig at all; only a model change is applied, and only
// `--apply restart` stops and relaunches the seat fresh.
package clientsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

const switchUsage = `usage: 4genteam sync switch ROOM SEAT [--runtime R] [--model M] [--apply none|set-model|restart] [--reason TEXT]

Change the LLM of one seat. The cloud records the occupant; OpenRig applies it when it can. At least
one of --runtime or --model is required. A runtime change cannot be applied in place, so it prints the
manual steps instead of touching a rig.
`

// switchRuntimes is the Python's RUNTIMES.
var switchRuntimes = []string{"claude-code", "codex", "agy", "omp"}

// switchApplyModes is the Python's APPLY_MODES.
var switchApplyModes = []string{"none", "set-model", "restart"}

// switchModelPattern is MODEL_RE.
var switchModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// errSwitchRigNotFound is the ONE sentinel for "no rig on PATH", so the verb can tell the Python's
// FileNotFoundError from a genuine `rig ps` failure.
var errSwitchRigNotFound = errors.New("`rig` executable not found on PATH")

// switchRigAnswer is one `rig` invocation: its stdout, and the failure when it had one.
type switchRigAnswer struct {
	stdout string
	err    error
}

// switchRunRig is the one place this file starts `rig`, with an ARGUMENT LIST (the first element is the
// program). A var so a test drives the verb without a rig.
var switchRunRig = func(argv ...string) switchRigAnswer {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return switchRigAnswer{err: errSwitchRigNotFound}
	}
	cmd := exec.Command(path, argv[1:]...)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	err = cmd.Run()
	return switchRigAnswer{stdout: stdout.String(), err: err}
}

// switchArgs is the parsed command line. runtimeSet/modelSet distinguish "the flag was absent" from
// "the flag was given an empty value", which is exactly the difference the Python's None default makes.
type switchArgs struct {
	room       string
	seat       string
	runtime    string
	runtimeSet bool
	model      string
	modelSet   bool
	apply      string
	applySet   bool
	reason     string
}

// RunSwitchVerb is `sync switch ROOM SEAT [--runtime R] [--model M] [--apply MODE] [--reason TEXT]`.
func RunSwitchVerb(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			_, _ = io.WriteString(stdout, switchUsage)
			return clientcmd.ExitOK
		}
	}
	parsed, err := switchParseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync switch: %v\n", err)
		return clientcmd.ExitUsage
	}
	return switchExecute(ctx, parsed, stdout, stderr)
}

// switchParseArgs is the Python's subparser for `switch`: two positionals, --runtime, --model, --apply
// (a choice) and --reason.
func switchParseArgs(args []string) (switchArgs, error) {
	var parsed switchArgs
	var positionals []string
	needValue := func(flag string, index int) error {
		if index+1 >= len(args) {
			return usageError("argument " + flag + ": expected one argument")
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--runtime":
			if err := needValue(arg, i); err != nil {
				return parsed, err
			}
			i++
			parsed.runtime, parsed.runtimeSet = args[i], true
		case strings.HasPrefix(arg, "--runtime="):
			parsed.runtime, parsed.runtimeSet = strings.TrimPrefix(arg, "--runtime="), true
		case arg == "--model":
			if err := needValue(arg, i); err != nil {
				return parsed, err
			}
			i++
			parsed.model, parsed.modelSet = args[i], true
		case strings.HasPrefix(arg, "--model="):
			parsed.model, parsed.modelSet = strings.TrimPrefix(arg, "--model="), true
		case arg == "--apply":
			if err := needValue(arg, i); err != nil {
				return parsed, err
			}
			i++
			parsed.apply, parsed.applySet = args[i], true
		case strings.HasPrefix(arg, "--apply="):
			parsed.apply, parsed.applySet = strings.TrimPrefix(arg, "--apply="), true
		case arg == "--reason":
			if err := needValue(arg, i); err != nil {
				return parsed, err
			}
			i++
			parsed.reason = args[i]
		case strings.HasPrefix(arg, "--reason="):
			parsed.reason = strings.TrimPrefix(arg, "--reason=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			return parsed, usageError("unrecognized arguments: " + arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 2 {
		return parsed, usageError("the following arguments are required: room, seat")
	}
	parsed.room, parsed.seat = positionals[0], positionals[1]
	if parsed.applySet {
		if err := switchValidateChoice("--apply", parsed.apply, switchApplyModes, nil); err != nil {
			return parsed, err
		}
	}
	return parsed, nil
}

// switchExecute ports cmd_switch: validate, read the current occupant, PUT the new one, then decide
// whether OpenRig can apply it and how.
func switchExecute(ctx context.Context, parsed switchArgs, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "4genteam sync switch: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitRemote)
	}

	room, err := ValidateName("room", parsed.room)
	if err != nil {
		return fail(err)
	}
	seat, err := ValidateName("seat", parsed.seat)
	if err != nil {
		return fail(err)
	}
	if !parsed.runtimeSet && !parsed.modelSet {
		return fail(usageError("at least one of --runtime or --model is required"))
	}
	if parsed.runtimeSet {
		if err := switchValidateChoice("runtime", parsed.runtime, switchRuntimes, nil); err != nil {
			return fail(err)
		}
	}
	if parsed.modelSet {
		if err := switchValidateChoice("model", parsed.model, nil, switchModelPattern); err != nil {
			return fail(err)
		}
	}

	baseURL, err := RequireEnv("AGENTHUB_URL")
	if err != nil {
		return fail(err)
	}
	token, err := RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		return fail(err)
	}

	current, err := switchCurrentOccupant(ctx, baseURL, token, room, seat)
	if err != nil {
		return fail(err)
	}
	currentRuntime, _ := current["runtime"].(string)
	currentModel, _ := current["model"].(string)
	runtime := currentRuntime
	if parsed.runtimeSet {
		runtime = parsed.runtime
	}
	model := currentModel
	if parsed.modelSet {
		model = parsed.model
	}

	path := RoomsPath + "/" + room + "/seats/" + seat + "/occupant"
	body, err := RequestJSON(ctx, "PUT", baseURL, token, path,
		map[string]any{"runtime": runtime, "model": model})
	if err != nil {
		return fail(err)
	}
	if success, isBool := body["success"].(bool); !isBool || !success {
		return fail(remoteFailure("PUT", path, "returned an error response"))
	}

	runtimeChanged := runtime != currentRuntime
	modelChanged := model != currentModel
	reason := parsed.reason
	if reason == "" {
		// The Python stamps the product name here; this tree has no product label to stamp, so the
		// generic room/seat pair is the audit reason.
		reason = fmt.Sprintf("switch %s/%s", room, seat)
	}

	mode := ""
	switch {
	case parsed.applySet:
		mode = parsed.apply
	case runtimeChanged:
		mode = "manual"
	case modelChanged:
		mode = "set-model"
	default:
		mode = "none"
	}

	applied := "none"
	if mode != "none" && runtimeChanged {
		applied = "manual"
		fmt.Fprintf(stderr,
			"runtime changed: OpenRig cannot switch a runtime in place. Run:\n"+
				"  4genteam sync rig %s --update\n"+
				"  rig down %s\n"+
				"  rig up <path of the rig.yaml printed by the first command>\n",
			room, room)
	} else if mode != "none" {
		modelForRig := ""
		if modelChanged {
			modelForRig = model
		}
		applied, err = switchApplyToRig(room, seat, modelForRig, mode == "restart", reason, stderr)
		if err != nil {
			return fail(err)
		}
	}
	fmt.Fprintf(stdout, "switched:%s/%s runtime=%s model=%s applied=%s\n", room, seat, runtime, model, applied)
	return clientcmd.ExitOK
}

// switchCurrentOccupant ports current_occupant: GET {ROOMS_PATH}/{room}/seats, then the success/seats
// shape check and the seat_key lookup, each with its own message.
func switchCurrentOccupant(ctx context.Context, baseURL, token, room, seat string) (map[string]any, error) {
	path := RoomsPath + "/" + room + "/seats"
	body, err := RequestJSON(ctx, "GET", baseURL, token, path, nil)
	if err != nil {
		return nil, err
	}
	success, isBool := body["success"].(bool)
	seats, isList := body["seats"].([]any)
	if !isBool || !success || !isList {
		return nil, remoteFailure("GET", path, "returned an error response")
	}
	for _, entry := range seats {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if key, _ := row["seat_key"].(string); key == seat {
			return row, nil
		}
	}
	return nil, &clientcmd.CodedError{
		Message: fmt.Sprintf("seat %s/%s not found in the cloud", room, seat),
		Code:    clientcmd.ExitRemote,
	}
}

// switchApplyToRig ports apply_to_rig: the model goes through `rig seat set-model`, `--apply restart`
// also stops and relaunches fresh, and a missing rig or an absent seat is a NOTE rather than a failure.
func switchApplyToRig(room, seat, model string, restart bool, reason string, stderr io.Writer) (string, error) {
	session, err := switchCanonicalSession(room, seat)
	if errors.Is(err, errSwitchRigNotFound) {
		fmt.Fprintln(stderr, "note: `rig` not found on PATH; the cloud change is recorded only")
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	if session == "" {
		fmt.Fprintf(stderr, "note: seat %s/%s is not in `rig ps`; the cloud change is recorded only\n", room, seat)
		return "none", nil
	}

	var commands [][]string
	if model != "" {
		commands = append(commands,
			[]string{"rig", "seat", "set-model", session, "--model", model, "--reason", reason})
	}
	if restart {
		fmt.Fprintf(stderr, "warning: restarting %s interrupts it and loses its live context\n", session)
		commands = append(commands, []string{"rig", "seat", "stop", session, "--reason", reason})
		commands = append(commands, []string{"rig", "seat", "launch", session, "--fresh", "--reason", reason})
	}
	for _, command := range commands {
		answer := switchRunRig(command...)
		if answer.err == nil {
			continue
		}
		if errors.Is(answer.err, errSwitchRigNotFound) {
			return "", &clientcmd.CodedError{
				Message: "`rig` executable not found on PATH",
				Code:    clientcmd.ExitRemote,
			}
		}
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("`rig seat %s` failed with exit code %d", command[2], switchExitCode(answer.err)),
			Code:    clientcmd.ExitRemote,
		}
	}
	if restart {
		return "restart", nil
	}
	if len(commands) > 0 {
		return "set-model", nil
	}
	return "none", nil
}

// switchCanonicalSession ports canonical_session: the live canonical session name of a seat, or "" when
// OpenRig does not list it. A missing rig is the sentinel; a non-zero `rig ps` and invalid JSON are
// reported with the Python's messages.
func switchCanonicalSession(room, seat string) (string, error) {
	answer := switchRunRig("rig", "ps", "--json", "--nodes", "--rig", room)
	if answer.err != nil {
		if errors.Is(answer.err, errSwitchRigNotFound) {
			return "", errSwitchRigNotFound
		}
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("rig ps failed with exit code %d", switchExitCode(answer.err)),
			Code:    clientcmd.ExitRemote,
		}
	}
	nodes, err := parseRigNodes(answer.stdout)
	if err != nil {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("rig ps returned invalid JSON: %v", err),
			Code:    clientcmd.ExitRemote,
		}
	}
	for _, node := range nodes {
		name, _ := node["canonicalSessionName"].(string)
		named, _ := node["rigName"].(string)
		if named == "" {
			named = room
		}
		if name != "" && named == room && logicalSeat(node) == seat {
			return name, nil
		}
	}
	return "", nil
}

// switchValidateChoice ports validate_choice: the allowed-values check, and the pattern check when one
// is given. The message names the kind, the value and what was expected.
func switchValidateChoice(kind, value string, allowed []string, pattern *regexp.Regexp) error {
	if allowed != nil && !switchContains(allowed, value) {
		return usageError(fmt.Sprintf("invalid %s: %q (expected one of %s)", kind, value, strings.Join(allowed, ", ")))
	}
	if pattern != nil && !pattern.MatchString(value) {
		return usageError(fmt.Sprintf("invalid %s: %q (expected %s)", kind, value, pattern.String()))
	}
	return nil
}

func switchContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// switchExitCode is the exit code a failed command carried, or 1 when the failure says nothing about one.
func switchExitCode(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}
