package clientteam

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The one reason token per finding, in the digest-check style: a stale digest, a source path with no
// file under --root, an older stored block with no provenance, and a canonical/mirror pair that
// diverged (a finding about the two copies, not about the stored-vs-source pair).
const (
	driftDigest       = "digest"
	driftMissing      = "missing-source"
	driftNoProvenance = "no-provenance"
	driftMirror       = "mirror"

	driftHeader = "stored skill provenance drifted from disk; nothing was written."
)

// provenance is what a stored skill block records about where its text came from.
type provenance struct {
	sourcePath string
	sha256     string
	mirrorPath string
	mirrorSHA  string
	hasMirror  bool
}

// storedProvenance is the provenance a stored skill block records, or nil when it carries none.
//
// An older block published before provenance carries raw skill text, or JSON without both source_path
// and sha256; that is the no-provenance category, reported apart from a digest mismatch.
func storedProvenance(stored storedModule) *provenance {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stored.content), &parsed); err != nil {
		return nil
	}
	sourcePath := provenancePath(parsed[provenanceSource])
	sha := provenancePath(parsed[provenanceSHA256])
	if sourcePath == "" || sha == "" {
		return nil
	}
	mirrorPath := provenancePath(parsed[provenanceMirrorPath])
	mirrorSHA := provenancePath(parsed[provenanceMirrorSHA])
	return &provenance{
		sourcePath: sourcePath,
		sha256:     sha,
		mirrorPath: mirrorPath,
		mirrorSHA:  mirrorSHA,
		hasMirror:  mirrorPath != "" && mirrorSHA != "",
	}
}

// provenancePath: a recorded path/digest is provenance only when it is a non-empty string.
func provenancePath(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// finding is one drift line's data.
type finding struct {
	slug       string
	reason     string
	version    string
	sourcePath string
	stored     string
	computed   string
	root       string
	roots      []string
}

// pairFinding is the finding for one recorded path/digest pair, or nil when it matches a file under a
// root. The path is tried under each root in order and the first file found decides the answer, so
// with no match it is the missing-source finding for every root that was tried.
func pairFinding(slug, path, stored string, roots []string, mirror bool) *finding {
	for _, root := range roots {
		target := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Stat(target)
		if err != nil || info.IsDir() {
			continue
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			continue
		}
		computed := sha256Hex(raw)
		if computed == stored {
			return nil
		}
		reason := driftDigest
		if mirror {
			reason = driftMirror
		}
		return &finding{
			slug: slug, reason: reason, sourcePath: path,
			stored: stored, computed: computed, root: root,
		}
	}
	return &finding{
		slug: slug, reason: driftMissing, sourcePath: path, stored: stored,
		roots: append([]string(nil), roots...),
	}
}

// driftFindings is one finding per stored digest pair that drifted or lost its file, plus the
// no-provenance blocks. Read-only: it hashes each recorded path under the roots it may belong to and
// compares. The primary pair is always checked; a block that also records a mirror pair has that pair
// checked too, independently.
func driftFindings(blocks []storedModule, roots []string) []finding {
	findings := []finding{}
	for _, block := range blocks {
		recorded := storedProvenance(block)
		if recorded == nil {
			findings = append(findings, finding{
				slug: block.slug, reason: driftNoProvenance, version: block.version,
			})
			continue
		}
		if primary := pairFinding(block.slug, recorded.sourcePath, recorded.sha256, roots, false); primary != nil {
			findings = append(findings, *primary)
		}
		if recorded.hasMirror {
			if mirror := pairFinding(block.slug, recorded.mirrorPath, recorded.mirrorSHA, roots, true); mirror != nil {
				findings = append(findings, *mirror)
			}
		}
	}
	return findings
}

// driftLine is one finding as the digest check prints them: indented "<key>: <path> (<reason>)" plus
// the detail. A resolved path names the root it matched under; a missing one names every root tried.
func driftLine(item finding) string {
	if item.reason == driftNoProvenance {
		return fmt.Sprintf("  %s: stored %s (%s) carries no source_path/sha256",
			item.slug, item.version, item.reason)
	}
	if item.reason == driftMissing {
		return fmt.Sprintf("  %s: %s (%s) no file under %s",
			item.slug, item.sourcePath, item.reason, strings.Join(item.roots, " or "))
	}
	return fmt.Sprintf("  %s: %s (%s @ %s) stored %s computed %s",
		item.slug, item.sourcePath, item.reason, item.root, item.stored, item.computed)
}

// driftRoots is the roots a recorded path may be relative to, in resolution order: --root (default the
// project directory) holds import-project's paths; --library-root (default $OPENRIG_SKILLS_ROOT) holds
// publish-skills' library paths.
func driftRoots(rootFlag, libraryFlag string) ([]string, error) {
	root := rootFlag
	if root == "" {
		cwd, err := projectRoot()
		if err != nil {
			return nil, err
		}
		root = cwd
	}
	roots := []string{root}
	library := libraryFlag
	if library == "" {
		library = os.Getenv(libraryRootEnv)
	}
	if library != "" && !containsPath(roots, library) {
		roots = append(roots, library)
	}
	return roots, nil
}

func containsPath(roots []string, candidate string) bool {
	cleaned := filepath.Clean(candidate)
	for _, root := range roots {
		if filepath.Clean(root) == cleaned {
			return true
		}
	}
	return false
}

// driftCheck reports stored skill provenance against disk; it reads only and writes nothing. It
// returns ExitDrift when there is at least one finding, ExitOK on a clean tree.
func driftCheck(rootFlag, libraryFlag string, stderr io.Writer) (int, error) {
	roots, err := driftRoots(rootFlag, libraryFlag)
	if err != nil {
		return 0, err
	}
	cloud, err := credentials()
	if err != nil {
		return 0, err
	}
	summaries, err := cloud.listModuleVersions()
	if err != nil {
		return 0, err
	}
	blocks, err := cloud.storedSkillBlocks(summaries)
	if err != nil {
		return 0, err
	}
	findings := driftFindings(blocks, roots)
	if len(findings) == 0 {
		return ExitOK, nil
	}
	fmt.Fprintln(stderr, driftHeader)
	for _, item := range findings {
		fmt.Fprintln(stderr, driftLine(item))
	}
	return ExitDrift, nil
}
