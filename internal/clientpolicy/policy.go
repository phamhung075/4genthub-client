// Package clientpolicy is the `policy` verb group: per-seat permission policy for omp seats, written as
// each seat's own config.yml.
//
// the rig orchestrator launches an omp seat in exactly one of two postures - yolo, or always-ask which the managed
// runner auto-cancels - so the restrictions live where omp enforces them. This is a guard rail against
// accidents, not a sandbox.
//
// THE ROLE TABLE IS NOT IN THIS BINARY. The Python carried the owner's rig, its seats and their roles as
// a compiled table (SEAT_ROLES); a client that ships somebody's team definition cannot be used by anyone
// else, so the roles come from the rig's own team file, passed with --team, and --rig must name the rig
// that file defines.
package clientpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// ExitDrift is --check's verdict: the policy's own keys are not what this binary would write. It shares
// the number 1 with clientcmd.ExitRemote but is a different thing - nothing remote was involved - so it
// is named here rather than borrowed.
const ExitDrift = 1

// servedRuntime is the one runtime `policy apply` writes for. The document it renders is an omp config
// (`mcp`, `compaction`, `bash.patterns`, `tools.approval`) and the state root it resolves defaults to
// `~/.config/4genthub` (clientenv.DefaultStateRoot) so a seat running
// anything else has no config here to write: that seat's state lives under its OWN runtime's root, which
// this verb never addresses. Named rather than inlined because the refusal and the help both state it.
const servedRuntime = "omp"

// keepRecentTokens is the tokens of the newest conversation kept verbatim at a compaction. omp's default
// is 20000, which holds the post-compaction floor near 42k; 8000 brings it near 30k.
const keepRecentTokens = 8000

// Denied for every seat. Anchored, because allowCompoundCommands makes omp evaluate each command of
// `a && b` on its own, so `git push*` also catches `cd x && git push`.
var commonBashDeny = []string{
	"git push*",
	"git reset --hard*",
	"git clean*",
	"git checkout -- *",
	"git stash drop*",
	"git stash clear*",
	"git commit*--amend*",
	"git add -A*",
	"git add --all*",
	"git add .",
	"rm -rf*",
	"rm -fr*",
	"sudo *",
	"ssh *",
	"scp *",
	"docker *",
	"tmux kill-*",
	"pkill *",
	"killall *",
	"printenv*",
	"cat *deepseek/env*",
	"rig down*",
	"rig up*",
	"rig remove*",
}

// Rig lifecycle belongs to the lead, who restarts seats; the other roles only message and read.
var nonLeadBashDeny = []string{
	"rig launch*",
	"rig seat stop*",
	"rig seat launch*",
	"rig seat clean*",
	"rig seat set-*",
}

// The MCP tools that manage agents, seats and the server itself are the lead's. The task, subtask,
// context, project and branch tools - the context sync - stay open to everyone.
var nonLeadToolDeny = []string{
	"mcp__agenthub_http_manage_agent",
	"mcp__agenthub_http_manage_connection",
	"mcp__agenthub_http_manage_seat",
}

// A reviewer reads and reports; it does not rewrite source (it can still write its verdict files).
var reviewerToolDeny = []string{"edit", "ast_edit"}

// Seat is one seat's entry in the team file. Runtime is the one the seat is launched with (`omp`,
// `claude-code`, ...); the team verb writes it into this file, and this package reads it only to know which
// seats THIS verb can write for. A file that names no runtime is served: that is the shape the Python's own
// table had, and the state root this verb resolves is the omp one, so its seats are the omp room's seats.
type Seat struct {
	Key      string `json:"seat_key"`
	Type     string `json:"seat_type"`
	Thinking string `json:"thinking"`
	Runtime  string `json:"runtime"`
}

// Team is the team file: the room it defines, and its seats with their roles. It is the same shape the
// team verb reads (a room slug and a seat list), so one file serves both.
type Team struct {
	Room struct {
		Slug string `json:"slug"`
	} `json:"room"`
	Seats []Seat `json:"seats"`
}

// ReadTeam reads a team file.
func ReadTeam(path string) (Team, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Team{}, &clientcmd.CodedError{
			Message: fmt.Sprintf("cannot read the team file %s: %v", path, err),
			Code:    clientcmd.ExitUsage,
		}
	}
	var team Team
	if err := json.Unmarshal(raw, &team); err != nil {
		return Team{}, &clientcmd.CodedError{
			Message: fmt.Sprintf("the team file %s is not valid JSON: %v", path, err),
			Code:    clientcmd.ExitUsage,
		}
	}
	return team, nil
}

// RoleFor is seat_roles and the role lookup: an unlisted rig or seat is an error, because there is no
// permissive default.
func RoleFor(team Team, rig, seat string) (Seat, error) {
	if team.Room.Slug != rig {
		return Seat{}, &clientcmd.CodedError{
			Message: fmt.Sprintf("no policy table for rig %q (the team file defines %q)", rig, team.Room.Slug),
			Code:    clientcmd.ExitUsage,
		}
	}
	for _, entry := range team.Seats {
		if entry.Key == seat {
			return entry, nil
		}
	}
	return Seat{}, &clientcmd.CodedError{
		Message: fmt.Sprintf("seat %q has no role in rig %q", seat, rig),
		Code:    clientcmd.ExitUsage,
	}
}

// policyFor is policy_for: the bash patterns to deny and the tools to deny for a role.
func policyFor(role string) ([]string, []string) {
	bash := append([]string{}, commonBashDeny...)
	var tools []string
	if role != "lead" {
		bash = append(bash, nonLeadBashDeny...)
		tools = append(tools, nonLeadToolDeny...)
	}
	if role == "reviewer" {
		tools = append(tools, reviewerToolDeny...)
	}
	return bash, tools
}

// RenderConfig is render_config: the seat's config.yml, where json.Marshal supplies YAML-safe
// double-quoted scalars exactly as the Python's json.dumps did.
//
// The startup call is exempt in EVERY approval mode, and it is the one entry that does not come from a
// role table: the startup file orders `rig whoami` before anything else, so on an always-ask seat an
// unanswered prompt would block the seat on its first call.
func RenderConfig(seat Seat) string {
	bash, tools := policyFor(seat.Type)
	var out strings.Builder
	fmt.Fprintf(&out, "# generated by 4genteam policy for %s (role: %s) - do not edit\n", seat.Key, seat.Type)
	out.WriteString("mcp:\n  startupTimeoutMs: 0\n")
	fmt.Fprintf(&out, "compaction:\n  keepRecentTokens: %d\n", keepRecentTokens)
	if seat.Thinking != "" {
		fmt.Fprintf(&out, "defaultThinkingLevel: %s\n", seat.Thinking)
	}
	out.WriteString("bash:\n  allowCompoundCommands: true\n  patterns:\n")
	fmt.Fprintf(&out, "    - match: %s\n      approval: allow\n", quote("rig whoami*"))
	for _, pattern := range bash {
		fmt.Fprintf(&out, "    - match: %s\n      approval: deny\n", quote(pattern))
	}
	if len(tools) > 0 {
		out.WriteString("tools:\n  approval:\n")
		for _, tool := range tools {
			fmt.Fprintf(&out, "    %s: deny\n", tool)
		}
	}
	return out.String()
}

func quote(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// PolicyKeysMatch is policy_keys_match: whether have carries the policy's own keys exactly as want
// states them. STRUCTURAL rather than byte-equal, because the pipeline MERGES this document into the
// runtime's config.yml, so a byte comparison would call a correctly-policed seat drifty. What must hold
// is exact: every key this package defines, at the value it defines. Keys it does not define are none
// of its business.
func PolicyKeysMatch(want, have string) bool {
	var wanted map[string]any
	if err := yaml.Unmarshal([]byte(want), &wanted); err != nil || wanted == nil {
		return false
	}
	var present map[string]any
	if err := yaml.Unmarshal([]byte(have), &present); err != nil || present == nil {
		return false
	}
	for key, value := range wanted {
		if !reflect.DeepEqual(present[key], value) {
			return false
		}
	}
	return true
}

// ConfigPath is config_path: a seat's own config, under the machine-level state root.
func ConfigPath(stateRoot, rig, seat string) string {
	return filepath.Join(stateRoot, rig+"-"+seat+"@"+rig, "agent", "config.yml")
}

// Commands is what the dispatcher registers from this package.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{policyCommand{}}
}

const (
	policyUsage = "usage: 4genteam policy (show SEAT --rig RIG --team FILE | apply --rig RIG --team FILE [--state-root DIR] [--check])"
	showUsage   = "usage: 4genteam policy show SEAT --rig RIG --team FILE"
	applyUsage  = "usage: 4genteam policy apply --rig RIG --team FILE [--state-root DIR] [--check]"
)

type policyCommand struct{}

func (policyCommand) Name() string    { return "policy" }
func (policyCommand) Summary() string { return "per-seat policy: show, apply" }

func (policyCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "4genteam policy: pick a verb: show, apply")
		return clientcmd.ExitUsage
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(stdout, "%s\n\nPer-seat permission policy. The roles come from the rig's own team file, so there is no\ncompiled role table: --team names it and --rig must be the rig it defines.\n", policyUsage)
		return clientcmd.ExitOK
	}
	switch args[0] {
	case "show":
		return runShow(args[1:], stdout, stderr)
	case "apply":
		return runApply(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "4genteam policy: unknown verb %q; the verbs are show, apply\n", args[0])
	return clientcmd.ExitUsage
}

func runShow(args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		fmt.Fprintf(stdout, "%s\n\nPrint one seat's config.yml, rendered from its role in the team file.\n", showUsage)
		return clientcmd.ExitOK
	}
	values, positional, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam policy show: %v. %s\n", err, showUsage)
		return clientcmd.ExitUsage
	}
	if err := rejectUnknown(values, "rig", "team"); err != nil {
		fmt.Fprintf(stderr, "4genteam policy show: %v. %s\n", err, showUsage)
		return clientcmd.ExitUsage
	}
	if values["rig"] == "" || values["team"] == "" || len(positional) < 1 {
		fmt.Fprintf(stderr, "4genteam policy show: a SEAT, --rig and --team are required. %s\n", showUsage)
		return clientcmd.ExitUsage
	}
	team, err := ReadTeam(values["team"])
	if err != nil {
		return exitFor(stderr, err)
	}
	entry, err := RoleFor(team, values["rig"], positional[0])
	if err != nil {
		return exitFor(stderr, err)
	}
	_, _ = io.WriteString(stdout, RenderConfig(entry))
	return clientcmd.ExitOK
}

func runApply(args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		fmt.Fprintf(stdout, "%s\n\nWrite every %s seat's config.yml from the team file; a seat of another runtime is refused by name, before anything is written. With --check write nothing and exit 1 on drift.\n", applyUsage, servedRuntime)
		return clientcmd.ExitOK
	}
	values, _, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam policy apply: %v. %s\n", err, applyUsage)
		return clientcmd.ExitUsage
	}
	if err := rejectUnknown(values, "rig", "team", "state-root", "check"); err != nil {
		fmt.Fprintf(stderr, "4genteam policy apply: %v. %s\n", err, applyUsage)
		return clientcmd.ExitUsage
	}
	rig := values["rig"]
	teamPath := values["team"]
	stateRoot, resolveErr := clientenv.ResolveStateRoot(values["state-root"])
	if resolveErr != nil {
		fmt.Fprintf(stderr, "4genteam policy apply: %v. %s\n", resolveErr, applyUsage)
		return clientcmd.ExitUsage
	}
	check := values["check"] == "true"
	if rig == "" || teamPath == "" {
		fmt.Fprintf(stderr, "4genteam policy apply: --rig and --team are required. %s\n", applyUsage)
		return clientcmd.ExitUsage
	}
	team, err := ReadTeam(teamPath)
	if err != nil {
		return exitFor(stderr, err)
	}
	if team.Room.Slug != rig {
		return exitFor(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("no policy table for rig %q (the team file defines %q)", rig, team.Room.Slug),
			Code:    clientcmd.ExitUsage,
		})
	}
	// THE WHOLE SET IS DECIDED BEFORE THE FIRST WRITE. A seat of another runtime has no agent directory
	// under THIS root - its state lives under its own runtime's - so without this pass the loop below wrote
	// the omp seats and then stopped at that seat with "never launched?", which it had been: a partial
	// application, a false reason, and nothing saying how much had been written. Measured on a scratch room
	// before the pass existed: one config on disk, exit 2; and with that seat's directory present, an omp
	// config written into a claude-code seat and exit 0, which is worse. Every unserved seat is named, so
	// one run tells a caller the whole set.
	var unserved []Seat
	for _, seat := range team.Seats {
		if seat.Runtime != "" && seat.Runtime != servedRuntime {
			unserved = append(unserved, seat)
		}
	}
	if len(unserved) > 0 {
		for _, seat := range unserved {
			fmt.Fprintf(stderr, "%s: runtime %s is not %s - this verb writes %s configs under the %s state root\n",
				seat.Key, seat.Runtime, servedRuntime, servedRuntime, servedRuntime)
		}
		fmt.Fprintln(stderr, "4genteam policy apply: nothing was written")
		return clientcmd.ExitUsage
	}
	drift := 0
	for _, seat := range team.Seats {
		path := ConfigPath(stateRoot, rig, seat.Key)
		if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
			fmt.Fprintf(stderr, "%s: no agent directory at %s (never launched?)\n", seat.Key, filepath.Dir(path))
			return clientcmd.ExitUsage
		}
		want := RenderConfig(seat)
		have := ""
		if raw, err := os.ReadFile(path); err == nil {
			have = string(raw)
		}
		if PolicyKeysMatch(want, have) {
			fmt.Fprintf(stdout, "%s: ok (%s)\n", seat.Key, seat.Type)
			continue
		}
		drift++
		if check {
			fmt.Fprintf(stdout, "%s: DRIFT (%s)\n", seat.Key, seat.Type)
			continue
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			fmt.Fprintf(stderr, "%s: cannot write %s: %v\n", seat.Key, path, err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "%s: written (%s)\n", seat.Key, seat.Type)
	}
	if check && drift > 0 {
		return ExitDrift
	}
	return clientcmd.ExitOK
}

// parseArgs splits a command line into --name value pairs and bare words. It is hand-rolled because the
// standard flag package STOPS at the first bare word, so `policy show SEAT --rig R --team F` - the
// documented order, and one argparse accepted - would parse no flags at all. `--check` is boolean, so it
// takes no value.
func parseArgs(args []string) (map[string]string, []string, error) {
	values := map[string]string{}
	booleans := map[string]bool{"check": true}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positional = append(positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if booleans[name] {
			if !hasValue {
				value = "true"
			}
			values[name] = value
			continue
		}
		if hasValue {
			values[name] = value
			continue
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("--%s needs a value", name)
		}
		i++
		values[name] = args[i]
	}
	return values, positional, nil
}

// rejectUnknown is the other half of a hand-rolled parse: a flag this verb does not define must be a
// usage error naming it, not a value silently carried and ignored.
func rejectUnknown(values map[string]string, allowed ...string) error {
	known := map[string]bool{}
	for _, name := range allowed {
		known[name] = true
	}
	for name := range values {
		if !known[name] {
			return fmt.Errorf("unknown flag --%s", name)
		}
	}
	return nil
}

func wantsHelp(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help")
}

// exitFor prints a failure and returns its exit code: a usage error keeps 2, anything else is a
// failure to act on this machine.
func exitFor(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "4genteam policy: %v\n", err)
	return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
}
