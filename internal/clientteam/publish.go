package clientteam

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// GeneratedFrom is the provenance line the inventory carries, naming the OpenRig revision its
	// digests were taken from. Nothing about the published bytes used to be checked against it.
	GeneratedFrom string         `json:"generated_from"`
	Skills        []inventoryRow `json:"skills"`
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
	if err := verifyInventoryNamesTheSourceRevision(inventoryPath, inventory, sourceRoot); err != nil {
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

// revisionInGeneratedFrom finds the revision in a provenance line. The inventory writes
// "<checkout> @ HEAD <revision>, re-verified <date> (<the committed edges>)", and the revision is the
// only hex run of any length in it, so the match does not depend on the surrounding words.
var revisionInGeneratedFrom = regexp.MustCompile(`[0-9a-f]{7,40}`)

// inventoryRevision is the revision the inventory's generated_from names, or "" when the inventory
// pins nothing. A provenance line that names no revision at all is refused rather than read as an
// absent pin: a pin that cannot be recomputed is what the format was changed to avoid on 2026-10-09.
func inventoryRevision(inventory *skillInventory) (string, error) {
	named := strings.TrimSpace(inventory.GeneratedFrom)
	if named == "" {
		return "", nil
	}
	revision := revisionInGeneratedFrom.FindString(named)
	if revision == "" {
		return "", usageError(fmt.Sprintf(
			"the inventory's generated_from names no revision: %q; it must name the OpenRig revision its digests were taken from",
			named))
	}
	return revision, nil
}

// revisionDigests digests the named revision's own bytes for every recorded path, keyed by the
// inventory-relative path of the file. git archive writes committed objects, so these bytes come from
// the revision and not from the checkout the operator happens to hold.
func revisionDigests(sourceRoot, revision string, paths []string) (map[string]string, error) {
	args := append([]string{"-C", sourceRoot, "archive", "--format=tar", revision, "--"}, paths...)
	command := exec.Command("git", args...)
	var archive, failure bytes.Buffer
	command.Stdout, command.Stderr = &archive, &failure
	if err := command.Run(); err != nil {
		return nil, usageError(fmt.Sprintf(
			"cannot read revision %s from %s: git archive failed: %v: %s",
			revision, sourceRoot, err, strings.TrimSpace(failure.String())))
	}
	digests := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(archive.Bytes()))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return digests, nil
		}
		if err != nil {
			return nil, usageError(fmt.Sprintf(
				"cannot read revision %s from %s: unreadable archive: %v", revision, sourceRoot, err))
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			return nil, usageError(fmt.Sprintf(
				"cannot read revision %s from %s: unreadable entry %s: %v", revision, sourceRoot, header.Name, err))
		}
		digests[filepath.ToSlash(header.Name)] = sha256Hex(body)
	}
}

// verifyInventoryNamesTheSourceRevision refuses an inventory whose generated_from names a revision
// that does not describe every digest it records, and refuses one that carries no pin at all.
//
// The digests themselves are verified against the operator's checkout, so on their own they say only
// that the inventory and that checkout agree: an inventory regenerated from a MOVED checkout keeps an
// old pin and publishes bytes the pin does not describe, which is silent. Materialising the named
// revision is what turns the pin into a claim - the same revision the recorded digests are supposed to
// describe is read back from git and digested here, and a mismatch names the skill, the path and both
// digests. The two pins that cannot be checked are refused rather than trusted: one that names no
// revision to recompute, and one that carries no generated_from at all - a nameless inventory is worse
// than a stale one, because nothing downstream can detect the divergence.
func verifyInventoryNamesTheSourceRevision(inventoryPath string, inventory *skillInventory, sourceRoot string) error {
	revision, err := inventoryRevision(inventory)
	if err != nil {
		return err
	}
	if revision == "" {
		return usageError(fmt.Sprintf(
			"%s: the inventory carries no generated_from, so no revision is named for the digests it records and nothing can check them against the bytes that name them; regenerate the inventory from the revision its digests were taken from",
			inventoryPath))
	}
	directories := map[string]bool{}
	for _, row := range inventory.Skills {
		for _, edge := range []*inventoryEdge{row.Canonical, row.Plugin} {
			if edge == nil || strings.TrimSpace(edge.Path) == "" {
				continue
			}
			directories[strings.TrimRight(edge.Path, "/")] = true
		}
	}
	paths := make([]string, 0, len(directories))
	for directory := range directories {
		paths = append(paths, directory)
	}
	sort.Strings(paths)
	atRevision, err := revisionDigests(sourceRoot, revision, paths)
	if err != nil {
		return err
	}
	for _, row := range inventory.Skills {
		// A row without a canonical edge is published from its plugin copy, so that copy is the
		// SOURCE and the naming has to follow the same rule the reader uses.
		primary := edgeCanonical
		if row.Canonical == nil {
			primary = edgePlugin
		}
		for _, side := range []struct {
			edge string
			rec  *inventoryEdge
		}{
			{edgeCanonical, row.Canonical},
			{edgePlugin, row.Plugin},
		} {
			if side.rec == nil || strings.TrimSpace(side.rec.Path) == "" || strings.TrimSpace(side.rec.SHA256) == "" {
				continue
			}
			which := "mirror"
			if side.edge == primary {
				which = "source"
			}
			name := strings.TrimRight(side.rec.Path, "/") + "/" + skillFile
			digest, present := atRevision[name]
			if !present {
				return usageError(fmt.Sprintf(
					"skill %q: the inventory records %s, which is not in revision %s of %s; the inventory's generated_from names a revision that does not describe it - regenerate the inventory from that revision",
					row.Name, name, revision, sourceRoot))
			}
			if digest != side.rec.SHA256 {
				return usageError(fmt.Sprintf(
					"skill %q: %s %s digests %s in revision %s, but the inventory records %s; the inventory's generated_from names %s but does not describe it - regenerate the inventory from that revision",
					row.Name, which, name, digest, revision, side.rec.SHA256, revision))
			}
		}
	}
	return nil
}
