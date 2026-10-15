package clientskills

// The check this file exists for: every LOCAL link in every shipped skill document resolves against
// the directory that carries the document. `team publish-skills` pushes this tree to the cloud
// catalog and `sync room` delivers it, so a link that does not resolve is delivered to every seat
// that receives the skill. The class is the reviewer's finding of 2026-10-10 (ten dead links in
// `skills/share/agent-browser/SKILL.md`, finding 5 of GATE-49e8d433-post-hoc-six): before the fix
// this test fails on exactly those ten and on nothing else, and after it there are none.
//
// SCOPE - read before extending. CHECKED: every `SKILL.md` in the tree plus the two companion
// documents a skill ships beside it (`VENDORING.md`, `LOCAL-INSIGHTS.md`) - the unit a seat actually
// loads. NOT CHECKED, each for a reason:
//   - fenced code blocks. They carry samples, not links; `skills/share/token-economy/TEMPLATES.md`
//     shows `**[func]([params])**` as an example of the style it warns against.
//   - template documents (`TEMPLATES.md`, `templates/*.md`). Their targets are fill-in placeholders
//     a reader replaces - `skills/share/dogfood/templates/dogfood-report-template.md`'s
//     `screenshots/issue-001-step-1.png`, `skills/share/unused-code-cleanup/TEMPLATES.md`'s
//     `[TYPE1]` - not files the skill promises to ship.
//   - off-site targets. This test never touches the network, so a target with a scheme is not
//     fetched and not judged.
//
// A local link here is a markdown inline link, image or reference definition whose target carries
// no scheme and is not a bare fragment. A `#fragment` is stripped before resolution, and the
// remainder is resolved relative to the directory of the file that carries the link.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// checkedDoc names the documents of a shipped skill that are read as its own prose.
func checkedDoc(name string) bool {
	switch name {
	case "SKILL.md", "VENDORING.md", "LOCAL-INSIGHTS.md":
		return true
	}
	return false
}

var (
	mdInlineLink = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)`)
	mdRefDef     = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*(\S+)`)
	urlScheme    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*:`)
	fenceMarker  = regexp.MustCompile("^\\s*(```|~~~)")
)

type localLink struct {
	file   string // path relative to the repository root
	line   int
	target string
}

// localLinks reads one document and returns the local link targets it carries.
func localLinks(root, path string) []localLink {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	var out []localLink
	fenced := false
	for i, line := range strings.Split(string(raw), "\n") {
		if fenceMarker.MatchString(line) {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, re := range []*regexp.Regexp{mdInlineLink, mdRefDef} {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				target := strings.Trim(m[1], "<>")
				if target == "" || strings.HasPrefix(target, "#") ||
					strings.Contains(target, "://") || urlScheme.MatchString(target) {
					continue
				}
				if local := strings.SplitN(target, "#", 2)[0]; local != "" {
					out = append(out, localLink{file: filepath.ToSlash(rel), line: i + 1, target: local})
				}
			}
		}
	}
	return out
}

// The acceptance: every local link in every shipped SKILL.md - and in the companions beside it -
// resolves against its own directory.
func TestShippedSkillLinksResolve(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("the shipped skills tree is not at %s: this check reads the tree in the repository, not a fixture", root)
	}

	var docs []string
	var skills int
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !checkedDoc(filepath.Base(path)) {
			return nil
		}
		if filepath.Base(path) == "SKILL.md" {
			skills++
		}
		docs = append(docs, path)
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// A walk that found no SKILL.md is checking an empty scope and must say so rather than pass.
	if skills == 0 {
		t.Fatalf("walked %s and found no SKILL.md; the tree moved and this check is reading nothing", root)
	}

	var checked, broken []localLink
	for _, doc := range docs {
		links := localLinks(root, doc)
		checked = append(checked, links...)
		for _, link := range links {
			if _, err := os.Stat(filepath.Join(filepath.Dir(doc), filepath.FromSlash(link.target))); err != nil {
				broken = append(broken, link)
			}
		}
	}

	if len(broken) > 0 {
		sort.Slice(broken, func(i, j int) bool {
			if broken[i].file != broken[j].file {
				return broken[i].file < broken[j].file
			}
			return broken[i].line < broken[j].line
		})
		for _, link := range broken {
			t.Errorf("%s:%d links to %s, which does not exist in that directory", link.file, link.line, link.target)
		}
		t.Fatalf("%d of %d local links in %d shipped skill documents do not resolve",
			len(broken), len(checked), len(docs))
	}
	t.Logf("%d local links across %d shipped skill documents (%d SKILL.md) all resolve",
		len(checked), len(docs), skills)
}
