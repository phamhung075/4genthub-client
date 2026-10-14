// The `sync switch` verb: change the LLM of a seat. The CLOUD records the new occupant first, and
// the agenthub runtime applies it where it can - which is not always, and the verb says so rather than
// pretending.
//
// THE CLOUD CHANGE IS THE AUTHORITY, so it is recorded even when the runtime cannot be reached: no
// runtime daemon or a seat the runtime's room does not list still exits 0 with `applied=none`, because
// the operator's intent has been recorded and only the local half is outstanding.
//
// A RUNTIME CHANGE CANNOT BE APPLIED IN PLACE. A changed runtime prints the manual steps and touches
// no seat at all; only a model change is applied. The model is written to the room's own spec and
// takes effect at the seat's next start; `--apply restart` makes that start now, with the seat's saved
// agent sessions dropped.
package clientsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const switchUsage = `usage: 4genteam sync switch ROOM SEAT [--runtime R] [--model M] [--apply none|set-model|restart] [--reason TEXT]

Change the LLM of one seat. The cloud records the occupant; the agenthub runtime applies it when it can.
At least one of --runtime or --model is required. A runtime change cannot be applied in place, so it
prints the manual steps instead of touching a seat. --apply set-model records the model for the seat's
next start; --apply restart also restarts the seat now with a fresh agent context.
`

// switchRuntimes is the Python's RUNTIMES.
var switchRuntimes = []string{"claude-code", "codex", "agy", "omp"}

// switchApplyModes is the Python's APPLY_MODES.
var switchApplyModes = []string{"none", "set-model", "restart"}

// switchModelPattern is MODEL_RE.
var switchModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// errSwitchRuntimeAbsent is the ONE sentinel for "no runtime daemon answers on its socket", so the verb can
// tell a machine that hosts no seats from a runtime that answered and refused.
var errSwitchRuntimeAbsent = errors.New("the agenthub runtime is not running")

// switchSetModel and switchRestartFresh are the two places this file asks the runtime to touch a seat. Vars
// so a test drives the verb without a daemon.
var switchSetModel = func(room, seat, model string) error {
	client := clientruntime.NewClient("")
	if !client.Reachable() {
		return errSwitchRuntimeAbsent
	}
	return client.SetSeatModel(room, seat, model)
}

var switchRestartFresh = func(room, seat string) error {
	client := clientruntime.NewClient("")
	if !client.Reachable() {
		return errSwitchRuntimeAbsent
	}
	_, err := client.RestartSeatFresh(room, seat)
	return err
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
			return usageError(flag + " needs a value")
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
			return parsed, usageError("unknown argument: " + arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 2 {
		return parsed, usageError("missing argument: room, seat")
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
		// NOTHING IN THIS BRANCH IS A PROCEDURE ANY MORE. The three lines it printed were the OpenRig
		// CLI's own sequence; the middle one was measured on this box to stop the MACHINE's safeguards
		// rather than the room's (rigd reaped with its children, for every rig), and the last could not
		// run at all (`--team DIR is required`), so a reader who followed it took the supervisors down
		// and could not start the room again from it. A reader cannot tell those two vocabularies apart
		// by name, so the branch names only what this client itself owns and documents, and prints no
		// OpenRig CLI verb. It runs neither: both write, and a restart is peak-gated. Whether a restart
		// carries a changed RUNTIME is not established here, and saying so IS the finding - the printed
		// text is pinned by TestSwitchRuntimeChangePrintsNoMachineWideVerb.
		fmt.Fprintf(stderr,
			"runtime changed: OpenRig cannot switch a runtime in place, and this client prints no "+
				"sequence that completes the switch (it has none that can be run):\n"+
				"  4genteam sync rig %s --update writes the room's definition and each seat's pinned "+
				"snapshot.\n"+
				"  4genteam seat reseat <rig> --seat <name> stops and restarts a seat so it loads the "+
				"configuration it now has.\n"+
				"  Neither is run here (both write; a restart is peak-gated for a deepseek seat), and "+
				"whether a restart carries a changed RUNTIME is not established. The switch is the "+
				"owner's call.\n",
			room)
	} else if mode != "none" {
		modelForRig := ""
		if modelChanged {
			modelForRig = model
		}
		applied, err = switchApplyToRuntime(room, seat, modelForRig, mode == "restart", stderr)
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

// switchApplyToRuntime applies the change through the runtime: the model is written to the room's spec,
// `--apply restart` also restarts the seat fresh, and a seat the runtime's room does not list is a NOTE
// rather than a failure. The runtime keys a seat by room and seat key, so no session name is read first.
func switchApplyToRuntime(room, seat, model string, restart bool, stderr io.Writer) (string, error) {
	if model != "" {
		if applied, done, err := switchSeatStep("set-model", switchSetModel(room, seat, model), room, seat, stderr); done {
			return applied, err
		}
	}
	if restart {
		fmt.Fprintf(stderr, "warning: restarting %s/%s interrupts it and loses its live context\n", room, seat)
		if applied, done, err := switchSeatStep("restart", switchRestartFresh(room, seat), room, seat, stderr); done {
			return applied, err
		}
		return "restart", nil
	}
	if model != "" {
		fmt.Fprintf(stderr, "note: the model is recorded for %s/%s and applies at the seat's next start\n", room, seat)
		return "set-model", nil
	}
	return "none", nil
}

// switchSeatStep reads one runtime call's outcome. done is true when the apply must stop here, with the
// applied value and error to return; a nil failure continues.
func switchSeatStep(step string, err error, room, seat string, stderr io.Writer) (applied string, done bool, outErr error) {
	if err == nil {
		return "", false, nil
	}
	// THIS VERB'S CONTRACT: the cloud change is the authority and has already been recorded, so a machine
	// whose runtime is down, or whose room does not list this seat, is a NOTE - the local half is what is
	// outstanding, not the operator's intent.
	if errors.Is(err, errSwitchRuntimeAbsent) {
		fmt.Fprintln(stderr, "note: the agenthub runtime is not running; the cloud change is recorded only")
		return "none", true, nil
	}
	var refused *clientruntime.DaemonRefused
	if errors.As(err, &refused) && (strings.Contains(refused.Reason, "not found in room") || strings.Contains(refused.Reason, "cannot read room spec")) {
		fmt.Fprintf(stderr, "note: seat %s/%s is not in the runtime's records; the cloud change is recorded only\n", room, seat)
		return "none", true, nil
	}
	return "", true, &clientcmd.CodedError{
		Message: fmt.Sprintf("the runtime's %s of %s/%s failed: %v", step, room, seat, err),
		Code:    clientcmd.ExitRemote,
	}
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
