package clientskills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The kinds of signal a skill may declare. Each names a TYPED stream fact the census can join a failure
// to - never free text, because free text is how a loop learns noise. The vocabulary is closed: a new kind
// is a new census reader, so an unknown key is refused rather than stored.
const (
	SignalGuard   = "guard"
	SignalSend    = "send"
	SignalJEV     = "jev"
	SignalRowTag  = "row-tag"
	SignalCommand = "command"
)

// SignalKinds is the closed vocabulary, in the order a report prints it.
var SignalKinds = []string{SignalGuard, SignalSend, SignalJEV, SignalRowTag, SignalCommand}

// Signal is one declared fact: what a skill governs, so a failure of that kind is attributable to it.
type Signal struct {
	Kind  string
	Value string
}

// String is the declared form the SKILL.md writes, so a report echoes the file rather than a rephrasing.
func (s Signal) String() string { return s.Kind + ": " + s.Value }

// Signals parses the `signals:` list out of a skill's front matter and returns it sorted by kind then
// value. The list is a mapping in a list item (`- guard: docs-only`), which is the shape the design note
// pins; a bare scalar (`- guard`) is refused because it declares a stream and no value.
//
// NO FRONT MATTER, OR NO `signals:` KEY, IS NOT AN ERROR: it returns nil, which the census reports as
// `no-signal` - a skill nobody can hold to account is a finding about the skill, not a parse failure.
func (s Skill) Signals() ([]Signal, error) {
	body, err := os.ReadFile(filepath.Join(s.Dir, SkillFile))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", filepath.Join(s.Dir, SkillFile), err)
	}
	return parseSignals(body)
}

// frontMatter splits a SKILL.md into its YAML front matter and the rest. A file without a leading `---`
// line has no front matter, which is legal: the loader marks a skill by the FILE, not by the block.
func frontMatter(body []byte) ([]byte, bool) {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, false
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}
	return []byte(rest[:end+1]), true
}

// parseSignals reads the front matter's `signals:` list. Order in the file is NOT preserved (yaml maps are
// unordered), so the result is sorted: a report that reordered itself between runs would be unreadable.
func parseSignals(body []byte) ([]Signal, error) {
	front, ok := frontMatter(body)
	if !ok {
		return nil, nil
	}
	var doc struct {
		Signals []map[string]string `yaml:"signals"`
	}
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return nil, errors.New("front matter is not YAML: " + err.Error())
	}
	known := map[string]bool{}
	for _, kind := range SignalKinds {
		known[kind] = true
	}
	var signals []Signal
	for i, entry := range doc.Signals {
		if len(entry) != 1 {
			return nil, fmt.Errorf("signals[%d] must declare exactly one kind, got %d", i, len(entry))
		}
		for kind, value := range entry {
			if !known[kind] {
				return nil, fmt.Errorf("signals[%d] declares unknown kind %q (known: %s)",
					i, kind, strings.Join(SignalKinds, ", "))
			}
			if strings.TrimSpace(value) != value || value == "" {
				return nil, fmt.Errorf("signals[%d] %s: is blank or carries surrounding space", i, kind)
			}
			signals = append(signals, Signal{Kind: kind, Value: value})
		}
	}
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Kind != signals[j].Kind {
			return signals[i].Kind < signals[j].Kind
		}
		return signals[i].Value < signals[j].Value
	})
	return signals, nil
}
