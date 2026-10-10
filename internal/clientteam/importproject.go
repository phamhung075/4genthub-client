package clientteam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// What import-project reads out of a project: the MCP servers a project registers and its own skill
// tree. Both become module versions, and both are project-root-relative, unlike publish-skills' library
// paths.
const (
	mcpConfig = ".mcp.json"
	skillsDir = ".claude/skills"
	skillFile = "SKILL.md"
	skillKind = "skill"

	kindMCP = "mcp"
)

// A ${ENV_VAR} reference is expanded by the client runtime and is never a stored secret.
var envReferenceRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

var moduleSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// projectRoot is the project the client works on: the directory it is run from.
func projectRoot() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", usageError(fmt.Sprintf("cannot read the working directory: %v", err))
	}
	return root, nil
}

// refuseSecret fails on a credential-shaped literal; a ${ENV_VAR} reference is allowed through, which
// is what lets a project commit an .mcp.json whose Authorization header names the token.
func refuseSecret(label, value string) error {
	stripped := envReferenceRe.ReplaceAllString(value, "")
	if _, redacted := secretscan.Redact(stripped, nil); redacted {
		return usageError(fmt.Sprintf(
			"%s: carries a credential-shaped value; reference the secret as ${ENV_VAR} instead of writing it into the module",
			label))
	}
	return nil
}

// moduleSlug is a name turned into the slug shape the backend accepts, or a usage error naming what it
// started from.
func moduleSlug(name, what string) (string, error) {
	slug := strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if !moduleSlugRe.MatchString(slug) {
		return "", usageError(fmt.Sprintf(
			"%s %q does not yield a module slug matching ^[a-z][a-z0-9-]*$ (got %q)", what, name, slug))
	}
	return slug, nil
}

// mcpModules is one mcp module per .mcp.json server entry, and nothing when there is no config.
func mcpModules(root string) ([]module, error) {
	configPath := filepath.Join(root, mcpConfig)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, usageError(fmt.Sprintf("cannot read %s: %v", configPath, err))
	}
	decoded, err := decodeOrdered(raw)
	if err != nil {
		return nil, usageError(fmt.Sprintf("cannot read %s: %v", configPath, err))
	}
	config, ok := decoded.(*orderedObject)
	if !ok {
		return nil, nil
	}
	rawServers, present := config.get("mcpServers")
	if !present || rawServers == nil {
		return nil, nil
	}
	servers, ok := rawServers.(*orderedObject)
	if !ok {
		return nil, usageError(fmt.Sprintf("%s: mcpServers must be an object", configPath))
	}
	names := append([]string(nil), servers.keys...)
	sort.Strings(names)
	modules := make([]module, 0, len(names))
	for _, name := range names {
		entry, _ := servers.get(name)
		block, err := mcpBlock(name, entry)
		if err != nil {
			return nil, err
		}
		slug, err := moduleSlug(name, "mcp server")
		if err != nil {
			return nil, err
		}
		modules = append(modules, module{slug: slug, kind: kindMCP, content: pyIndented(block)})
	}
	return modules, nil
}

// mcpBlock is one .mcp.json server entry as the backend's mcp block payload. The key order is the
// Python's, because it is part of the content this module is compared on.
func mcpBlock(name string, entry any) (*pyObject, error) {
	server, ok := entry.(*orderedObject)
	if !ok {
		return nil, usageError(fmt.Sprintf("mcp server %q must be an object", name))
	}
	serverType := ""
	if declared, ok := server.get("type"); ok {
		serverType, _ = declared.(string)
	}
	_, hasURL := server.get("url")
	_, hasCommand := server.get("command")
	if serverType != "http" && serverType != "stdio" {
		switch {
		case hasURL && !hasCommand:
			serverType = "http"
		case hasCommand && !hasURL:
			serverType = "stdio"
		default:
			return nil, usageError(fmt.Sprintf(
				"mcp server %q: type must be \"http\" or \"stdio\", or be inferable from url / command", name))
		}
	}
	block := newPyObject().set("name", name).set("type", serverType)
	if serverType == "http" {
		url, _ := server.get("url")
		text, ok := url.(string)
		if !ok || text == "" {
			return nil, usageError(fmt.Sprintf("mcp server %q: an http server needs a url", name))
		}
		block.set("url", text)
	} else {
		command, _ := server.get("command")
		text, ok := command.(string)
		if !ok || text == "" {
			return nil, usageError(fmt.Sprintf("mcp server %q: a stdio server needs a command", name))
		}
		block.set("command", text)
		if raw, present := server.get("args"); present {
			args, err := stringSlice(raw, name, "args")
			if err != nil {
				return nil, err
			}
			if len(args) > 0 {
				block.set("args", args)
			}
		}
	}
	for _, field := range []string{"headers", "env"} {
		raw, present := server.get(field)
		if !present || raw == nil {
			continue
		}
		if object, ok := raw.(*orderedObject); ok && len(object.keys) == 0 {
			continue
		}
		values, ok := raw.(*orderedObject)
		if !ok {
			return nil, usageError(fmt.Sprintf("mcp server %q: %s must be an object of strings", name, field))
		}
		converted, ok := orderedStrings(values)
		if !ok {
			return nil, usageError(fmt.Sprintf("mcp server %q: %s must be an object of strings", name, field))
		}
		if serverType == "http" && field == "env" {
			return nil, usageError(fmt.Sprintf("mcp server %q: an http server takes headers, not env", name))
		}
		if serverType == "stdio" && field == "headers" {
			return nil, usageError(fmt.Sprintf("mcp server %q: a stdio server takes env, not headers", name))
		}
		block.set(field, converted)
	}
	if err := refuseBlockSecrets(name, block); err != nil {
		return nil, err
	}
	return block, nil
}

// stringSlice validates a JSON array of strings.
func stringSlice(raw any, name, field string) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, usageError(fmt.Sprintf("mcp server %q: %s must be an array of strings", name, field))
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, usageError(fmt.Sprintf("mcp server %q: %s must be an array of strings", name, field))
		}
		out = append(out, text)
	}
	return out, nil
}

// refuseBlockSecrets checks every field of a built block that could carry a credential, naming the
// field so the reader knows which one to fix.
func refuseBlockSecrets(name string, block *pyObject) error {
	check := func(field, value string) error {
		return refuseSecret(fmt.Sprintf("mcp server %q %s", name, field), value)
	}
	for _, field := range []string{"url", "command"} {
		if raw, present := block.get(field); present {
			text, _ := raw.(string)
			if err := check(field, text); err != nil {
				return err
			}
		}
	}
	if raw, present := block.get("args"); present {
		items, _ := raw.([]string)
		for index, item := range items {
			if err := check(fmt.Sprintf("args[%d]", index), item); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"headers", "env"} {
		raw, present := block.get(field)
		if !present {
			continue
		}
		values, ok := raw.(*pyObject)
		if !ok {
			continue
		}
		for index, key := range values.keys {
			text, _ := values.values[index].(string)
			if err := check(field+"."+key, text); err != nil {
				return err
			}
		}
	}
	return nil
}

// skillBlockContent is one skill block's content JSON: the SKILL.md text plus its source provenance.
//
// A skill module's content is a BLOCK, never raw markdown: a skill that is not a valid block fails
// visibly at render instead of writing JSON or untraceable text into a seat. sourcePath is relative to
// the root its publisher used, and mirrorPath/mirrorSha256 record the second committed copy of a skill
// that lives on two edges.
func skillBlockContent(content, sourcePath, digest, mirrorPath, mirrorSha256 string) string {
	block := newPyObject().
		set("content", content).
		set("source_path", sourcePath).
		set("sha256", digest)
	if mirrorPath != "" && mirrorSha256 != "" {
		block.set("mirror_path", mirrorPath).set("mirror_sha256", mirrorSha256)
	}
	return pyCompact(block)
}

// skillModules is one skill block per .claude/skills directory: the SKILL.md text plus its provenance.
func skillModules(root string) ([]module, error) {
	directory := filepath.Join(root, skillsDir)
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, usageError(fmt.Sprintf("cannot read %s: %v", directory, err))
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	modules := make([]module, 0, len(names))
	for _, name := range names {
		skillPath := filepath.Join(directory, name, skillFile)
		raw, err := os.ReadFile(skillPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, usageError(fmt.Sprintf("skill %q: %s is missing", name, skillPath))
			}
			return nil, usageError(fmt.Sprintf("cannot read %s: %v", skillPath, err))
		}
		content := string(raw)
		if err := refuseSecret(fmt.Sprintf("skill %q", name), content); err != nil {
			return nil, err
		}
		slug, err := moduleSlug(name, "skill")
		if err != nil {
			return nil, err
		}
		relative := filepath.ToSlash(filepath.Join(skillsDir, name, skillFile))
		modules = append(modules, module{
			slug:    slug,
			kind:    skillKind,
			content: skillBlockContent(content, relative, sha256Hex(raw), "", ""),
		})
	}
	return modules, nil
}

// projectModules is every module the project's local configuration imports, refusing two entries that
// would produce the same slug because the second would silently overwrite the first.
func projectModules(root string) ([]module, error) {
	mcp, err := mcpModules(root)
	if err != nil {
		return nil, err
	}
	skills, err := skillModules(root)
	if err != nil {
		return nil, err
	}
	modules := append(mcp, skills...)
	seen := map[string]int{}
	for _, m := range modules {
		seen[m.slug]++
	}
	duplicates := []string{}
	for slug, count := range seen {
		if count > 1 {
			duplicates = append(duplicates, slug)
		}
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		return nil, usageError(fmt.Sprintf(
			"module slug(s) %s are produced by more than one import entry", strings.Join(duplicates, ", ")))
	}
	return modules, nil
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
