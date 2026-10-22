// codex's surface is a FILE, not an environment variable (PROBE-2026-10-21-llm-egress.md: OPENAI_BASE_URL
// measured 0 listener hits, model_providers.<id>.base_url measured 2). So this route is redirected the way
// codex itself reads: one provider entry whose base_url is this listener, plus a PROFILE that selects it.
//
// THE CLIENT OWNS ONE ENTRY AND NEVER THE SELECTION: it copies the fields of the provider the seat already
// selected (the config's own model_provider and its model_providers.<id> section), changes only base_url,
// and writes a [profiles.agenthub-egress] that points at the copy. The global model_provider is read and
// left exactly as it was - a machine-wide rewrite of the owner's provider choice is not this client's to
// make. Until a seat runs under the profile the copy is INERT, so the route is reported
// `unproxied (provider entry present, not selected)`, never as a pass.
//
// THE EDIT IS SURGICAL. The file is scanned as sections and key blocks, only the two sections this client
// owns are replaced, and every other line is carried through byte for byte. A config this code cannot
// read well enough to copy from is a failure that names the file, never a rewrite that guesses.
package clientllmproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	// codexProviderID is the provider entry this client owns in codex's config file. The name carries
	// the client, not the provider, because the entry is a copy of a provider rather than a new one.
	codexProviderID = "agenthub-egress"
	// codexProfileName is the profile that selects it. It is named after the entry so a seat's launch
	// flag can name one word.
	codexProfileName = "agenthub-egress"
	codexConfigName  = "config.toml"
	codexDirName     = ".codex"
)

// codexConfigPath resolves <CODEX_HOME>/config.toml, else ~/.codex/config.toml. CODEX_HOME is a seam:
// tests write into a temp directory rather than the owner's real codex config.
func codexConfigPath(environ func() []string) (string, error) {
	for _, entry := range environ() {
		name, value, found := strings.Cut(entry, "=")
		if found && name == "CODEX_HOME" && value != "" {
			return filepath.Join(value, codexConfigName), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate codex's config file: CODEX_HOME is unset and there is no home directory: %w", err)
	}
	return filepath.Join(home, codexDirName, codexConfigName), nil
}

// ensureCodexProvider makes path point codex at this listener and reports whether it changed the file.
// It copies the provider the config selects, replaces only that copy's base_url with the listener, and
// writes the profile that selects the copy; the second run over an unchanged config returns false and
// leaves the bytes identical.
func ensureCodexProvider(path string, port int) (bool, error) {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		existing = nil
	default:
		return false, fmt.Errorf("cannot read codex's config file %s: %w", path, err)
	}
	doc := parseConfig(string(existing))
	sourceID, ok := doc.rootValue("model_provider")
	if !ok || sourceID == "" {
		return false, fmt.Errorf("%s selects no model_provider, so there is no provider whose fields could be "+
			"copied into [model_providers.%s]; set one (or run codex once) and retry - this client will not "+
			"invent a model or an auth field", path, codexProviderID)
	}
	source, ok := doc.find("model_providers." + sourceID)
	if !ok {
		return false, fmt.Errorf("%s selects model_provider %q but has no [model_providers.%s] section to "+
			"copy from; this client will not invent one", path, sourceID, sourceID)
	}

	doc.setSection("model_providers."+codexProviderID, copyProviderFields(source.body, port))
	doc.setSection("profiles."+codexProfileName, doc.profileWithSelector())
	doc.trailingNewline = true
	rendered := doc.render()
	if rendered == string(existing) {
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(rendered)); err != nil {
		return false, err
	}
	return true, nil
}

// copyProviderFields is the copy rule: every field of the selected provider survives verbatim, and only
// base_url becomes this listener. A provider with no base_url gains one; nothing else is added.
func copyProviderFields(body []string, port int) []string {
	baseURL := `base_url = "http://127.0.0.1:` + strconv.Itoa(port) + `"`
	var out []string
	found := false
	for _, block := range keyBlocks(body) {
		if block.key == "base_url" {
			out = append(out, baseURL)
			found = true
			continue
		}
		out = append(out, trimTrailingBlanks(block.lines)...)
	}
	out = trimTrailingBlanks(out)
	if !found {
		out = append(out, baseURL)
	}
	return out
}

// profileWithSelector is the profile body that selects the owned entry. Any other key the profile already
// carries is kept; only model_provider is set, and it is set to the owned entry, never to a provider.
func (d *configDoc) profileWithSelector() []string {
	selector := `model_provider = "` + codexProviderID + `"`
	var out []string
	found := false
	for _, block := range keyBlocks(d.sectionBody("profiles." + codexProfileName)) {
		if block.key == "model_provider" {
			out = append(out, selector)
			found = true
			continue
		}
		out = append(out, trimTrailingBlanks(block.lines)...)
	}
	out = trimTrailingBlanks(out)
	if !found {
		out = append([]string{selector}, out...)
	}
	return out
}

// codexEntryPresent reports whether this client's provider entry is already in codex's config file. It is
// what Report and CodexStatus need to say "present, not selected" instead of a bare unproxied.
func codexEntryPresent(environ func() []string) bool {
	path, err := codexConfigPath(environ)
	if err != nil {
		return false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, ok := parseConfig(string(content)).find("model_providers." + codexProviderID)
	return ok
}

// configDoc is a config file as ordered sections. The root table is the section with an empty name and no
// header; every other section keeps its raw header line so an untouched table renders byte for byte.
type configDoc struct {
	sections        []configSection
	trailingNewline bool
}

type configSection struct {
	name   string
	header string
	body   []string
}

// parseConfig splits content into sections on table headers. It is deliberately line-based rather than a
// full TOML parse: the client edits two tables and must carry everything else through unchanged, which a
// decode-then-encode round trip cannot promise for comments and spacing.
func parseConfig(content string) *configDoc {
	doc := &configDoc{trailingNewline: strings.HasSuffix(content, "\n")}
	if doc.trailingNewline {
		content = strings.TrimSuffix(content, "\n")
	}
	var lines []string
	if content != "" {
		lines = strings.Split(content, "\n")
	}
	current := configSection{}
	for _, line := range lines {
		if name, ok := tableName(line); ok {
			doc.sections = append(doc.sections, current)
			current = configSection{name: name, header: line}
			continue
		}
		current.body = append(current.body, line)
	}
	doc.sections = append(doc.sections, current)
	return doc
}

// find returns the section named name.
func (d *configDoc) find(name string) (configSection, bool) {
	for _, section := range d.sections {
		if section.name == name {
			return section, true
		}
	}
	return configSection{}, false
}

// sectionBody is a section's body, or nil when the section is absent.
func (d *configDoc) sectionBody(name string) []string {
	section, ok := d.find(name)
	if !ok {
		return nil
	}
	return section.body
}

// rootValue reads one top-level key whose value is a string, which is where model_provider lives.
func (d *configDoc) rootValue(key string) (string, bool) {
	if len(d.sections) == 0 {
		return "", false
	}
	for _, block := range keyBlocks(d.sections[0].body) {
		if block.key != key {
			continue
		}
		return tomlString(block.lines[0]), true
	}
	return "", false
}

// setSection replaces the named section's body (keeping the blank lines that separated it from the next
// table) or appends the section at the end of the file with one blank line before it.
func (d *configDoc) setSection(name string, body []string) {
	header := "[" + name + "]"
	for i := range d.sections {
		if d.sections[i].name != name {
			continue
		}
		replacement := append([]string{}, body...)
		replacement = append(replacement, make([]string, trailingBlanks(d.sections[i].body))...)
		d.sections[i].body = replacement
		d.sections[i].header = header
		return
	}
	if len(d.sections) > 0 {
		last := &d.sections[len(d.sections)-1]
		if len(last.body) > 0 && strings.TrimSpace(last.body[len(last.body)-1]) != "" {
			last.body = append(last.body, "")
		}
	}
	d.sections = append(d.sections, configSection{name: name, header: header, body: body})
}

// render turns the sections back into the file's text, adding the final newline only when the original
// had one (or when this client added a section to a file that had none).
func (d *configDoc) render() string {
	var out []string
	for _, section := range d.sections {
		if section.header != "" {
			out = append(out, section.header)
		}
		out = append(out, section.body...)
	}
	text := strings.Join(out, "\n")
	if d.trailingNewline && text != "" {
		text += "\n"
	}
	return text
}

// trailingBlanks counts the empty lines at the end of body.
func trailingBlanks(body []string) int {
	count := 0
	for i := len(body) - 1; i >= 0; i-- {
		if strings.TrimSpace(body[i]) != "" {
			break
		}
		count++
	}
	return count
}

// trimTrailingBlanks drops the empty lines at the end of body. A blank line before the next table is the
// SECTION's separation, not one of its fields, so copying it would grow by one blank line on every run.
func trimTrailingBlanks(body []string) []string {
	return body[:len(body)-trailingBlanks(body)]
}

// tomlBlock is one key and the lines that belong to it: its own line plus any continuation lines (an
// array or a multi-line string), which are carried through verbatim.
type tomlBlock struct {
	key   string
	lines []string
}

var tomlKeyLine = regexp.MustCompile(
	`^(?:"([A-Za-z0-9_-]+)"|'([A-Za-z0-9_-]+)'|([A-Za-z0-9_-]+))\s*=`,
)

// keyBlocks splits a section body into its key blocks, dropping comments and blank lines that precede the
// first key: they describe the section, not the fields this client copies.
func keyBlocks(body []string) []tomlBlock {
	var blocks []tomlBlock
	for _, line := range body {
		if key, ok := tomlKey(strings.TrimSpace(stripComment(line))); ok {
			blocks = append(blocks, tomlBlock{key: key, lines: []string{line}})
			continue
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1].lines = append(blocks[len(blocks)-1].lines, line)
		}
	}
	return blocks
}

// tomlKey reports the bare key a key line starts with, quoted or not. Provider and profile keys are bare
// in every codex config this client has to copy; a dotted key is not one it needs to interpret.
func tomlKey(code string) (string, bool) {
	match := tomlKeyLine.FindStringSubmatch(code)
	if match == nil {
		return "", false
	}
	for _, group := range match[1:] {
		if group != "" {
			return group, true
		}
	}
	return "", false
}

// tableName reports the canonical name of a table header line, so `[profiles . foo]` and `[profiles.foo]`
// name the same section. Arrays of tables are section boundaries too, which is all this file needs.
func tableName(line string) (string, bool) {
	trimmed := strings.TrimSpace(stripComment(line))
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	if strings.HasPrefix(trimmed, "[[") {
		if !strings.HasSuffix(trimmed, "]]") {
			return "", false
		}
		trimmed = trimmed[2 : len(trimmed)-2]
	} else {
		if !strings.HasSuffix(trimmed, "]") {
			return "", false
		}
		trimmed = trimmed[1 : len(trimmed)-1]
	}
	return canonicalName(trimmed), true
}

// canonicalName removes the whitespace and quotes around the parts of a table name, so the name compared
// is the name TOML means.
func canonicalName(inner string) string {
	parts := strings.Split(inner, ".")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) >= 2 && (part[0] == '"' || part[0] == '\'') && part[len(part)-1] == part[0] {
			part = part[1 : len(part)-1]
		}
		parts[i] = part
	}
	return strings.Join(parts, ".")
}

// tomlString reads the string value from a key line, tolerating a trailing comment.
func tomlString(line string) string {
	_, value, found := strings.Cut(line, "=")
	if !found {
		return ""
	}
	value = strings.TrimSpace(stripComment(value))
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}

// stripComment removes an unquoted # comment: a # inside a quoted string is part of the value.
func stripComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return line[:i]
		}
	}
	return line
}

// writeFileAtomic replaces path with data through a temp file in the same directory, so a failure cannot
// leave a half-written config behind. The existing file's mode is kept; a new file is 0600 because it can
// hold an API key's name and environment variable.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	temp, err := os.CreateTemp(dir, "."+codexConfigName+".*")
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("cannot replace %s: %w", path, err)
	}
	return nil
}
