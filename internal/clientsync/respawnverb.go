package clientsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const (
	// respawnPollSeconds is how often the dead reading is re-taken while --after-seconds runs out.
	respawnPollSeconds = 5.0
	// defaultRespawnAfterSeconds is that deadline: a just-launched seat reads EXACTLY like a dead one
	// until its runtime hook attaches, so a single reading is not evidence of anything.
	defaultRespawnAfterSeconds = 30.0
	// defaultRespawnReason is what `--reason` defaults to: the audit reason the LAUNCH would record on the
	// seat.fresh_launched event. NOTHING RECORDS IT IN THIS BUILD - the launch has no native verb and this
	// verb refuses in words rather than falling back to `rig` - so the value is still parsed and validated
	// (a caller's command line does not change the day the runtime grows the verb) and then set aside at the
	// one place the launch would have spent it.
	defaultRespawnReason = "agent died outside `rig seat stop` (respawn)"
)

const respawnUsage = `usage: 4genteam sync respawn ROOM SEAT [--after-seconds SECONDS] [--reason TEXT] ` + clientoffpeak.ModeFlagUsage + `

Respawn a seat whose agent died outside ` + "`rig seat stop`" + `. The seat must READ dead for
--after-seconds (default 30s) before the launch leg is reached, because a seat that has just started reads
the same way until its runtime hook attaches.

A respawn is a LAUNCH, so the DeepSeek peak gate applies here: with ` + clientoffpeak.ModeEnv + `
on (the default) a seat whose model is deepseek/ is not launched at peak, and the refusal names when
off-peak resumes. ` + clientoffpeak.FlagAllowPeak + ` skips the check for this run.

THE LAUNCH IS NOT AVAILABLE IN THIS BUILD, SO THIS VERB CANNOT FINISH: the native runtime has no verb that
starts one session fresh, stops it first and records an audit reason, and this verb never falls back to ` + "`rig`" + `.
A seat that holds the dead reading is refused in words - exit 3 (` + "`ExitUnavailable`" + `), the session named,
nothing on stdout - rather than started.

RECORDED AS THE C3 FOLLOW-UP ` + "`sync respawn has no launch`" + `, so it is visible here and not only in a commit:
the readings, the peak gate and the dead-reading wait are all intact and unchanged, and this verb completes
the moment the runtime gains a launch verb that carries a reason. Until then ` + "`4genteam room reseat ROOM SEAT`" + `
is the native door onto the same stop-then-start path - the room's own spec, no ` + "`--fresh`" + `, no ` + "`--reason`" + `.
`

// RunRespawnVerb is `sync respawn`: the deliberate primitive for a seat whose agent died outside
// OpenRig's own stop, since OpenRig has no automatic trigger for that.
//
// IT IS ALSO THE ONE THING THIS VERB CANNOT FINISH. Every reading and every refusal it has always made is
// still here - the seat's own record, the peak gate, the dead reading held for --after-seconds - and the
// LAUNCH at the end of them has no native verb to call, so that leg speaks its refusal instead of running
// `rig` (C3 §4). The decision is unchanged; the step it decided to take is named as unavailable.
//
// THE DEBT IS RECORDED, NOT IMPLIED (C3 follow-up `sync respawn has no launch`): in this build the verb can
// never complete a respawn. That is R1's own price - a launch that did not happen is never ExitOK - and it is
// paid knowingly rather than by wiring RestartSeat into a leg that would then drop the caller's --reason,
// which is the silent skip R1 exists to forbid. The capability returns with a runtime launch verb that takes
// a reason; until then --reason is parsed, validated and set aside at the refusal, and the refusal names the
// native door onto the same path.
func RunRespawnVerb(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			_, _ = io.WriteString(stdout, respawnUsage)
			return clientcmd.ExitOK
		}
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(args)
	// THE REASON IS PARSED AND THEN SET ASIDE, named here rather than hidden in a signature: the one thing
	// that would spend it is the launch, and that leg refuses without a launch verb (see respawn below). The
	// flag keeps needing a value - a caller's command line does not have to change the day the runtime grows
	// the verb, and nothing about it was silently accepted as done.
	room, seat, after, _, err := parseRespawnArgs(rest)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitUsage
	}
	// respawn takes no stdout writer: its last step can only refuse, and a refusal is spoken on stderr, so
	// there is no longer a way for this verb to print a launch report on stdout.
	return respawn(ctx, room, seat, after, allowPeak, stderr)
}

func parseRespawnArgs(args []string) (room, seat string, after float64, reason string, err error) {
	var positionals []string
	after, reason = defaultRespawnAfterSeconds, defaultRespawnReason
	needValue := func(flag string, index int) error {
		if index+1 >= len(args) {
			return fmt.Errorf("%s needs a value", flag)
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--after-seconds":
			if err := needValue(arg, i); err != nil {
				return "", "", 0, "", err
			}
			i++
			after, err = strconv.ParseFloat(args[i], 64)
			if err != nil {
				return "", "", 0, "", fmt.Errorf("invalid --after-seconds: %s", args[i])
			}
		case arg == "--reason":
			if err := needValue(arg, i); err != nil {
				return "", "", 0, "", err
			}
			i++
			reason = args[i]
		case strings.HasPrefix(arg, "--after-seconds="):
			after, err = strconv.ParseFloat(strings.TrimPrefix(arg, "--after-seconds="), 64)
			if err != nil {
				return "", "", 0, "", fmt.Errorf("invalid --after-seconds: %s", arg)
			}
		case strings.HasPrefix(arg, "--reason="):
			reason = strings.TrimPrefix(arg, "--reason=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			return "", "", 0, "", fmt.Errorf("unknown argument: %s", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 2 {
		return "", "", 0, "", errors.New("missing argument: room, seat")
	}
	return positionals[0], positionals[1], after, reason, nil
}

func respawn(ctx context.Context, room, seat string, after float64, allowPeak bool, stderr io.Writer) int {
	room, err := ValidateName("room", room)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitUsage
	}
	seat, err = ValidateName("seat", seat)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitUsage
	}
	deadline := time.Now().Add(time.Duration(after * float64(time.Second)))
	for {
		node, err := seatNode(ctx, room, seat)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
			return clientcmd.ExitRemote
		}
		if node == nil {
			fmt.Fprintf(stderr, "4genteam sync respawn: OpenRig lists no node %s.%s\n", room, seat)
			return clientcmd.ExitUsage
		}
		// THE PEAK GATE, on the seat this verb decides about, and before the dead-reading wait: a respawn is
		// a start, so at peak a DeepSeek seat is not started by default, and a refusal must not make the
		// caller wait --after-seconds first. The seat's own record carries its model.
		//
		// IT STAYS AHEAD OF THE LAUNCH REFUSAL below even though this build cannot launch, because the
		// decision is this verb's own and the seat's model is what it turns on: a caller who asked to respawn
		// a DeepSeek seat gets the billing answer first, and only a seat that passes the gate reaches the
		// step that has no verb. Both refuse; neither is swallowed.
		proceed, message, err := clientoffpeak.Check(allowPeak, []map[string]any{node}, os.Getenv)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
			return clientcmd.ExitUsage
		}
		if !proceed {
			fmt.Fprintf(stderr, "4genteam sync respawn: %s\n", message)
			return clientcmd.ExitUnavailable
		}
		if !agentIsGone(node) {
			fmt.Fprintf(stderr,
				"4genteam sync respawn: %s.%s is not in the dead-agent state (sessionStatus=%s, "+
					"agentActivity=%s); refusing to respawn\n",
				room, seat, jsonText(node["sessionStatus"]), jsonText(node["agentActivity"]))
			return clientcmd.ExitUsage
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Duration(respawnPollSeconds * float64(time.Second)))
	}
	session, err := canonicalSession(ctx, room, seat)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitRemote
	}
	if session == "" {
		session = room + "." + seat
	}
	// THE LAUNCH IS REFUSED IN WORDS, NOT SUBSTITUTED (C3 §4; R1/R4). What this leg IS: `seat launch
	// <session> --fresh --stop --reason R` - ONE SESSION, started FRESH, with the old occupant stopped first
	// and an audit reason recorded. What the native runtime has is StartRoom (a whole room, from a spec) and
	// RestartSeat ({room, seat} -> stop_seat, then start_seat from the room's own spec) and nothing that
	// starts one session - and RestartSeat carries NO REASON AT ALL, so wiring it would drop the operator's
	// reason on the floor while this verb reported a respawn: the silent skip R1 exists to forbid. The step
	// is therefore not taken and it says so, naming the session it could not start; `rig` is not consulted
	// anywhere on this path (R4), and the exit is non-zero so no caller reads a refusal as a respawn (R1).
	fmt.Fprintf(stderr, "4genteam sync respawn: cannot launch %s — the native runtime has no launch verb for "+
		"this, and `rig` is never run in its place, so this build cannot respawn the seat at all; start it "+
		"with `4genteam room reseat %s %s` (native, from the room's spec: no --fresh, no --reason) or retry "+
		"after the runtime gains a launch verb that carries a reason.\n", session, room, seat)
	return clientcmd.ExitUnavailable
}

// seatNode is the ONE seat's record for room.seat, or nil when the room's records do not list it.
//
// The read is the client's own node read (clientruntime.SeatNode) rather than a `rig ps` exec, which is
// what makes its three answers distinguishable here: a room that does not list the seat is nil (nothing to
// act on), and a read that could not be made is the error - an unreadable record is NOT the answer "no
// such seat", and `respawn` must refuse on it rather than decide for a seat it could not read.
func seatNode(ctx context.Context, room, seat string) (map[string]any, error) {
	node, found, err := clientruntime.SeatNode(ctx, room, seat)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return node, nil
}

// agentIsGone is the reading a dead agent leaves: the session is up, the runtime hook is not.
func agentIsGone(node map[string]any) bool {
	if node == nil {
		return true
	}
	status, _ := node["sessionStatus"].(string)
	if status == "stopped" || status == "exited" {
		return false
	}
	activity, _ := node["agentActivity"].(map[string]any)
	state, _ := activity["state"].(string)
	reason, _ := activity["reason"].(string)
	return state == "unknown" && reason == "no_runtime_hook"
}

// canonicalSession is the live canonical session name of a seat, or "" when the room's records do not list
// it. It reads through the same clientruntime.SeatNode the seat's own record comes from - the ONE seat read
// this verb takes twice - so an unreadable record is the error it already wraps rather than an empty name
// that the refusal below would print as a composed room.seat: an operator told to start the seat by hand
// must be told the session that exists, not one this verb invented.
func canonicalSession(ctx context.Context, room, seat string) (string, error) {
	node, found, err := clientruntime.SeatNode(ctx, room, seat)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	name, _ := node["canonicalSessionName"].(string)
	return name, nil
}

// jsonText renders a node field for the refusal, so an operator sees exactly what the reading was.
func jsonText(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}
