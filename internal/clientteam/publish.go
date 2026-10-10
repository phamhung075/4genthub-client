package clientteam

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The inventory's edges and the field names a published skill block carries. Both edges of one skill
// are ONE module: the canonical copy is the source and the plugin copy is its mirror.
const (
	edgeCanonical = "canonical"
	edgePlugin    = "plugin"

	provenanceSource     = "source_path"
	provenanceSHA256     = "sha256"
	provenanceMirrorPath = "mirror_path"
	provenanceMirrorSHA  = "mirror_sha256"

	// libraryRootEnv is the OpenRig checkout publish-skills' and drift-check's library paths are
	// relative to, when the flag is not given.
	libraryRootEnv = "OPENRIG_SKILLS_ROOT"
)

// inventoryRow is one inventory skill line. Either edge may be absent; a canonical row's plugin is its
// mirror, and a plugin-only row is published alone.
type inventoryRow struct {
	Name      string         `json:"name"`
	Canonical *inventoryEdge `json:"canonical"`
	Plugin    *inventoryEdge `json:"plugin"`
}

type inventoryEdge struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type skillInventory struct {
	Skills []inventoryRow `json:"skills"`
}

// loadSkillInventory reads the skill library inventory the publish consumes. The inventory is content
// owned elsewhere; this path only consumes it and never edits it.
func loadSkillInventory(path string) (*skillInventory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, usageError(fmt.Sprintf("cannot read skill inventory from %s: %v", path, err))
	}
	var inventory skillInventory
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return nil, usageError(fmt.Sprintf("cannot read skill inventory from %s: %v", path, err))
	}
	if inventory.Skills == nil {
		return nil, usageError(fmt.Sprintf("%s: inventory must be an object with a skills array", path))
	}
	return &inventory, nil
}

// verifiedSkillText reads one recorded side of a skill and refuses when its bytes do not match the
// recorded digest. side names which copy is being read ("source" or "mirror"), so a failure says which
// half of the pair moved. Both sides come through here: one rule, applied twice.
func verifiedSkillText(name, path, recorded, sourceRoot, side string) ([]byte, error) {
	skillPath := filepath.Join(sourceRoot, filepath.FromSlash(path), skillFile)
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, usageError(fmt.Sprintf(
			"skill %q: cannot read %s %s under --source-root: %v", name, side, skillPath, err))
	}
	digest := sha256Hex(raw)
	if digest != recorded {
		return nil, usageError(fmt.Sprintf(
			"skill %q: %s %s digests %s, but the inventory records %s; the inventory is stale - regenerate it before publishing",
			name, side, skillPath, digest, recorded))
	}
	return raw, nil
}

// skillLibraryModules is one skill block per inventory skill, with both recorded digests verified
// against the files they name.
//
// A digest that is recorded but never read is a side free to rot green, so an unread mirror is a claim
// nothing has checked: publishing bytes the inventory does not describe would write a block whose
// provenance is stale the moment it lands.
func skillLibraryModules(inventory *skillInventory, sourceRoot string) ([]module, error) {
	type edges struct {
		canonical *inventoryEdge
		plugin    *inventoryEdge
	}
	rows := map[string]*edges{}
	order := []string{}
	for _, row := range inventory.Skills {
		if row.Name == "" {
			return nil, usageError("every skill row in the inventory needs a name")
		}
		merged, known := rows[row.Name]
		if !known {
			merged = &edges{}
			rows[row.Name] = merged
			order = append(order, row.Name)
		}
		for _, pair := range []struct {
			edge  string
			value *inventoryEdge
		}{{edgeCanonical, row.Canonical}, {edgePlugin, row.Plugin}} {
			if pair.value == nil {
				continue
			}
			if pair.value.Path == "" || pair.value.SHA256 == "" {
				return nil, usageError(fmt.Sprintf(
					"skill %q: inventory entry %q needs a path and a sha256", row.Name, pair.edge))
			}
			if pair.edge == edgeCanonical {
				merged.canonical = pair.value
			} else {
				merged.plugin = pair.value
			}
		}
	}
	sort.Strings(order)
	modules := make([]module, 0, len(order))
	for _, name := range order {
		row := rows[name]
		primaryEdge, primary := edgeCanonical, row.canonical
		if primary == nil {
			primaryEdge, primary = edgePlugin, row.plugin
		}
		if primary == nil {
			// A row that names an edge the loader does not know carries no path at all; the
			// inventory contract is canonical and/or plugin.
			return nil, usageError(fmt.Sprintf(
				"skill %q: inventory entry needs a path and a sha256", name))
		}
		raw, err := verifiedSkillText(name, primary.Path, primary.SHA256, sourceRoot, "source")
		if err != nil {
			return nil, err
		}
		content := string(raw)
		if err := refuseSecret(fmt.Sprintf("skill %q", name), content); err != nil {
			return nil, err
		}
		mirror := (*inventoryEdge)(nil)
		if primaryEdge == edgeCanonical {
			mirror = row.plugin
		}
		if mirror != nil {
			// Read even though only the source's text is published: the mirror's digest is part of
			// the block's provenance, so an unread mirror is a claim nothing has checked.
			if _, err := verifiedSkillText(name, mirror.Path, mirror.SHA256, sourceRoot, "mirror"); err != nil {
				return nil, err
			}
		}
		slug, err := moduleSlug(name, "skill")
		if err != nil {
			return nil, err
		}
		mirrorPath, mirrorSHA := "", ""
		if mirror != nil {
			mirrorPath = strings.TrimRight(mirror.Path, "/") + "/" + skillFile
			mirrorSHA = mirror.SHA256
		}
		modules = append(modules, module{
			slug: slug,
			kind: skillKind,
			content: skillBlockContent(
				content,
				strings.TrimRight(primary.Path, "/")+"/"+skillFile,
				primary.SHA256,
				mirrorPath,
				mirrorSHA,
			),
		})
	}
	return modules, nil
}

// publishSourceRoot is --source-root, else $OPENRIG_SKILLS_ROOT, else a usage error naming both. The
// checkout the inventory's paths are relative to is required, never guessed: a wrong root would
// publish bytes from the wrong tree.
func publishSourceRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if fromEnv := os.Getenv(libraryRootEnv); fromEnv != "" {
		return fromEnv, nil
	}
	return "", usageError("publish-skills needs the OpenRig checkout the inventory paths are relative to: " +
		"pass --source-root DIR or set " + libraryRootEnv)
}

// publishSkills is `team publish-skills`. Unlike apply and import-project it attempts EVERY block
// rather than stopping at the first refusal: a publish that dies at block 30 of 52 leaves 22
// unattempted and reads as a 30-block inventory.
func publishSkills(inventoryPath, sourceRootFlag, version string, dryRun bool, stdout, stderr io.Writer) error {
	inventory, err := loadSkillInventory(inventoryPath)
	if err != nil {
		return err
	}
	sourceRoot, err := publishSourceRoot(sourceRootFlag)
	if err != nil {
		return err
	}
	modules, err := skillLibraryModules(inventory, sourceRoot)
	if err != nil {
		return err
	}
	cloud, haveCredentials := optionalCredentials()
	if dryRun {
		fmt.Fprintf(stdout, "plan: publish-skills %d skill block(s) from %s\n", len(modules), inventoryPath)
		if haveCredentials {
			return printResolvedPlan(cloud, modules, version, stdout)
		}
		for _, m := range modules {
			fmt.Fprintln(stdout, unresolvedPlanLine(m, version))
		}
		return nil
	}
	if !haveCredentials {
		if cloud, err = credentials(); err != nil {
			return err
		}
	}
	resolved, err := cloud.resolveModules(modules, version)
	if err != nil {
		return err
	}
	counts := countActions(resolved)
	fmt.Fprintf(stdout, "publishing %d skill block(s) from %s\n", len(modules), inventoryPath)
	failures, err := runSteps(cloud, resolvedSteps(resolved), false, true, stdout, stderr)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		pushed := counts[actionPush] + counts[actionNewVersion] - len(failures)
		fmt.Fprintf(stdout, "publish summary: %d of %d block(s) pushed, %d FAILED, %d skipped\n",
			pushed, counts[actionPush]+counts[actionNewVersion], len(failures), counts[actionSkip])
		parts := make([]string, 0, len(failures))
		for _, failure := range failures {
			parts = append(parts, failure.label+": "+failure.reason)
		}
		return remoteError(fmt.Sprintf("%d skill block(s) not published: %s", len(failures), strings.Join(parts, "; ")))
	}
	fmt.Fprintf(stdout, "publish summary: %d pushed, %d new version(s), %d skipped\n",
		counts[actionPush], counts[actionNewVersion], counts[actionSkip])
	return nil
}

// countActions is the per-outcome tally both import-project and publish-skills print.
func countActions(resolved []resolvedEntry) map[string]int {
	counts := map[string]int{}
	for _, entry := range resolved {
		counts[entry.action]++
	}
	return counts
}

// printResolvedPlan is the dry-run body when the server could classify the modules.
func printResolvedPlan(cloud *cloudClient, modules []module, version string, stdout io.Writer) error {
	resolved, err := cloud.resolveModules(modules, version)
	if err != nil {
		return err
	}
	for _, entry := range resolved {
		fmt.Fprintln(stdout, planLine(entry))
	}
	return nil
}
