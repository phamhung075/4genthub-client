// Package clientteam is the `team` verb group: create the room in the cloud and keep its content in
// step with the files on disk.
//
// Four verbs, ported from the Python client's team_setup.py with its test suite as the spec:
//
//	team apply           create modules, room, seats, links and overlays from <TEAM-DIR>/team.json
//	team prune           delete the room's seats that <TEAM-DIR>/team.json does not define
//	team import-project  push a project's .mcp.json servers and .claude/skills as module versions
//	team publish-skills  push a skill library inventory as kind=skill blocks with provenance
//	team drift-check     report stored skill provenance that no longer matches the files on disk
//
// Three departures from the Python, all deliberate and all in the phase report:
//
//   - module content is produced by this package's own Python-compatible JSON emitter (pyjson.go),
//     because the content is compared byte-for-byte with what the cloud stores;
//   - the tests replace the httpClient seam with a fake transport, so no test opens a socket - the
//     Python tests ran a loopback HTTP server instead;
//   - an owner-specific value is never a default: --team, --inventory and (for publish-skills) the
//     library root are required, so a repository name, a rig or an owner's path cannot leak in.
package clientteam

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// Commands is what the dispatcher registers from this package.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{teamCommand{}}
}

type teamCommand struct{}

func (teamCommand) Name() string { return "team" }
func (teamCommand) Summary() string {
	return "create the cloud room: apply, import-project, publish-skills, drift-check"
}

// errHelpShown separates "the caller asked for usage" from a failure: both stop the verb, but usage
// is a success.
var errHelpShown = errors.New("usage shown")

// errDriftFound is drift-check's own exit condition. It carries no message because the findings it
// already printed ARE the message; the dispatcher must not add an "error:" line that says nothing.
var errDriftFound = errors.New("drift found")

const teamVerbs = "apply, prune, import-project, publish-skills, drift-check"

func (teamCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "4genteam team: pick a verb: %s\n", teamVerbs)
		return clientcmd.ExitUsage
	}
	var err error
	switch args[0] {
	case "apply":
		err = runApply(args[1:], stdout, stderr)
	case "prune":
		err = runPrune(args[1:], stdout, stderr)
	case "import-project":
		err = runImportProject(args[1:], stdout, stderr)
	case "publish-skills":
		err = runPublishSkills(args[1:], stdout, stderr)
	case "drift-check":
		err = runDriftCheck(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printTeamUsage(stdout)
		return clientcmd.ExitOK
	default:
		fmt.Fprintf(stderr, "4genteam team: unknown verb %q; the verbs are %s\n", args[0], teamVerbs)
		return clientcmd.ExitUsage
	}
	if err != nil {
		if errors.Is(err, errHelpShown) {
			return clientcmd.ExitOK
		}
		if errors.Is(err, errDriftFound) {
			return ExitDrift
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return clientcmd.CodeOf(err, ExitRemote)
	}
	return clientcmd.ExitOK
}

func printTeamUsage(w io.Writer) {
	fmt.Fprint(w, `usage: 4genteam team <verb> [options]

Create the cloud room a rig runs: its modules, seats, links and overlays.

verbs:
  apply           create modules, room, seats, links, overlays (--team DIR)
  prune           delete the room's seats that team.json does not define (--team DIR)
  import-project  push .mcp.json servers and .claude/skills as module versions
  publish-skills  push the skill library inventory as kind=skill blocks with provenance
  drift-check     report stored skill provenance that no longer matches the files on disk

Every verb answers --help with its own usage.
`)
}

// newVerbFlags is the one place a verb's FlagSet is built: output discarded (the verb prints its own
// usage), so a parse failure produces exactly one message from this package.
func newVerbFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

// parseVerb parses a verb's arguments, mapping --help to errHelpShown and any other parse failure to
// a usage error carrying the flag package's own message.
func parseVerb(flags *flag.FlagSet, args []string, usage func(io.Writer), stdout io.Writer) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return errHelpShown
		}
		return usageError(err.Error())
	}
	if flags.NArg() > 0 {
		return usageError(fmt.Sprintf("unexpected argument %q", flags.Arg(0)))
	}
	return nil
}

// runApply is `team apply`: one plan from the team definition, sent step by step.
func runApply(args []string, stdout, stderr io.Writer) error {
	const usage = `usage: 4genteam team apply --team DIR [--dry-run]

Create the modules, room, seats, links and overlays a team.json defines.

options:
  --team DIR   team definition directory (holds team.json); required
  --dry-run    print the plan, call nothing
`
	flags := newVerbFlags("apply")
	dryRun := flags.Bool("dry-run", false, "print the plan, call nothing")
	teamDir := flags.String("team", "", "team definition directory (holds team.json)")
	if err := parseVerb(flags, args, func(w io.Writer) { fmt.Fprint(w, usage) }, stdout); err != nil {
		return err
	}
	if *teamDir == "" {
		return usageError("team apply: --team DIR is required (the directory that holds team.json)")
	}
	loaded, err := loadTeam(*teamDir)
	if err != nil {
		return err
	}
	// The seat-type seed is skipped only when the cloud already stores every seat type the team uses.
	// Without credentials the stored set is unknown, so the seed stays in the plan - which is the
	// Python's rule, and the reason the doc's dry-run sentence says a dry run still reads the server.
	var stored map[string]bool
	var cloud *cloudClient
	if found, ok := optionalCredentials(); ok {
		cloud = found
		if stored, err = cloud.storedSeatTypeSlugs(); err != nil {
			return err
		}
	}
	plan, err := buildPlan(loaded, stored)
	if err != nil {
		return err
	}
	if *dryRun {
		_, err := runSteps(nil, plan, true, false, stdout, stderr)
		return err
	}
	if cloud == nil {
		if cloud, err = credentials(); err != nil {
			return err
		}
	}
	_, err = runSteps(cloud, plan, false, false, stdout, stderr)
	return err
}

// runImportProject is `team import-project`: the project's own .mcp.json and skill tree.
func runImportProject(args []string, stdout, stderr io.Writer) error {
	const usage = `usage: 4genteam team import-project [--dry-run] [--version X.Y.Z]

Push the current project's .mcp.json servers and .claude/skills as module versions.

options:
  --dry-run        print the plan, call nothing
  --version V      module version to publish (default 1.0.0)
`
	flags := newVerbFlags("import-project")
	dryRun := flags.Bool("dry-run", false, "print the plan, call nothing")
	version := flags.String("version", importVersion, "module version to publish (default 1.0.0)")
	if err := parseVerb(flags, args, func(w io.Writer) { fmt.Fprint(w, usage) }, stdout); err != nil {
		return err
	}
	root, err := projectRoot()
	if err != nil {
		return err
	}
	modules, err := projectModules(root)
	if err != nil {
		return err
	}
	cloud, haveCredentials := optionalCredentials()
	if *dryRun {
		fmt.Fprintf(stdout, "plan: import-project from %s (%d module(s))\n", root, len(modules))
		if haveCredentials {
			return printResolvedPlan(cloud, modules, *version, stdout)
		}
		for _, m := range modules {
			fmt.Fprintln(stdout, unresolvedPlanLine(m, *version))
		}
		return nil
	}
	if !haveCredentials {
		if cloud, err = credentials(); err != nil {
			return err
		}
	}
	resolved, err := cloud.resolveModules(modules, *version)
	if err != nil {
		return err
	}
	counts := countActions(resolved)
	fmt.Fprintf(stdout, "importing %d module(s) from %s\n", len(modules), root)
	if _, err := runSteps(cloud, resolvedSteps(resolved), false, false, stdout, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "import summary: %d pushed, %d new version(s), %d skipped\n",
		counts[actionPush], counts[actionNewVersion], counts[actionSkip])
	return nil
}

// runPublishSkills is `team publish-skills`: a skill library inventory, pushed with provenance.
func runPublishSkills(args []string, stdout, stderr io.Writer) error {
	const usage = `usage: 4genteam team publish-skills --inventory FILE [--dry-run] [--source-root DIR] [--version X.Y.Z]

Push a skill library inventory as kind=skill blocks with provenance.

options:
  --inventory FILE    skill library inventory JSON; required
  --source-root DIR   library checkout the inventory paths are relative to
                      (default: $AGENTHUB_SKILLS_ROOT)
  --dry-run           print the plan, call nothing
  --version V         module version to publish (default 1.0.0)
`
	flags := newVerbFlags("publish-skills")
	dryRun := flags.Bool("dry-run", false, "print the plan, call nothing")
	inventory := flags.String("inventory", "", "skill library inventory JSON")
	sourceRoot := flags.String("source-root", "", "library checkout the inventory paths are relative to")
	version := flags.String("version", publishVersion, "module version to publish (default 1.0.0)")
	if err := parseVerb(flags, args, func(w io.Writer) { fmt.Fprint(w, usage) }, stdout); err != nil {
		return err
	}
	if *inventory == "" {
		return usageError("team publish-skills: --inventory FILE is required (the skill library inventory JSON)")
	}
	return publishSkills(*inventory, *sourceRoot, *version, *dryRun, stdout, stderr)
}

// runDriftCheck is `team drift-check`: read-only, and its own exit code when something drifted.
func runDriftCheck(args []string, stdout, stderr io.Writer) error {
	const usage = `usage: 4genteam team drift-check [--root DIR] [--library-root DIR]

Report stored skill provenance that no longer matches the files on disk. Reads only; writes nothing.

options:
  --root DIR           project root import-project's paths are relative to (default: the current directory)
  --library-root DIR   library checkout publish-skills' paths are relative to
                       (default: $AGENTHUB_SKILLS_ROOT)
`
	flags := newVerbFlags("drift-check")
	root := flags.String("root", "", "project root import-project's paths are relative to")
	libraryRoot := flags.String("library-root", "", "library checkout publish-skills' paths are relative to")
	if err := parseVerb(flags, args, func(w io.Writer) { fmt.Fprint(w, usage) }, stdout); err != nil {
		return err
	}
	code, err := driftCheck(*root, *libraryRoot, stderr)
	if err != nil {
		return err
	}
	if code != ExitOK {
		return errDriftFound
	}
	return nil
}
