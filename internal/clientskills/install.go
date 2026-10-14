// The delivery half: the files a seat receives, and the declaration that makes the rig orchestrator project them.
// Both halves are needed. "Resources are the available pool. They are NOT automatically delivered to
// agents - profiles select them via uses" (the agent spec reference), so a skill copied into the seat's
// skills path without a declaration is inert, and a declaration without the files points at nothing.
package clientskills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Install copies the skills one seat receives into the seat's skills path, <agentDir>/skills/<name>/,
// and returns their names, sorted. Every file under a skill directory is copied - SKILL.md and whatever
// sits beside it - and an existing file is overwritten: this tree is the source of truth for the skills
// the client owns, so a second run converges on it rather than diverging from it.
//
// It writes files and nothing else. Declare is the other half, kept separate because the agent spec
// belongs to the caller that materialized the agent directory.
func Install(tree Tree, room, seat, agentDir string) ([]string, error) {
	if agentDir == "" {
		return nil, usageError("install: no agent directory to install the skills into")
	}
	skills, err := tree.Resolve(room, seat)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(agentDir, SkillsDir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, usageError("cannot create the seat's skills path " + target + ": " + err.Error())
	}
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		if err := copyTree(skill.Dir, filepath.Join(target, skill.Name)); err != nil {
			return nil, err
		}
		names = append(names, skill.Name)
	}
	return names, nil
}

// copyTree copies one skill directory. Directories are created and regular files are written; anything
// else - a symlink, a socket, a device - is refused by name rather than copied, because following a link
// out of the tree would deliver bytes the operator never put in it.
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return usageError("cannot read the skill directory " + src + ": " + err.Error())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return usageError("cannot create " + dst + ": " + err.Error())
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			return usageError(fmt.Sprintf("%s is not a regular file, so it was not installed", from))
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return usageError("cannot read " + from + ": " + err.Error())
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			return usageError("cannot write " + to + ": " + err.Error())
		}
	}
	return nil
}

// Declare adds the installed ids to an agent spec: each becomes a resources.skills entry (its id, and
// the path this client installs it at, "skills/<id>"), and each is added to EVERY profile's uses.skills,
// because a skill is delivered only when a profile selects it. It returns whether the file changed, and
// leaves the file untouched when everything is already declared.
//
// A spec with no profiles is refused: nothing would select the skills, and inventing a profile would be
// this client deciding a seat's posture. The rewrite re-encodes the whole document (comments are not
// preserved), which is what the Python client's own spec merges did.
func Declare(specPath string, ids []string) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	text, err := os.ReadFile(specPath)
	if err != nil {
		return false, usageError("cannot read the agent spec " + specPath + ": " + err.Error())
	}
	var document yaml.Node
	if err := yaml.Unmarshal(text, &document); err != nil {
		return false, usageError("the agent spec " + specPath + " is not valid YAML: " + err.Error())
	}
	root := documentRoot(&document)
	if root == nil {
		return false, usageError("the agent spec " + specPath + " is not a YAML mapping")
	}
	profiles := lookup(root, "profiles")
	if profiles == nil || profiles.Kind != yaml.MappingNode {
		return false, usageError("the agent spec " + specPath +
			" has no profiles block, so nothing would select the skills it declares")
	}

	changed := false
	resources, err := ensureMapping(root, "resources")
	if err != nil {
		return false, err
	}
	pool, err := ensureSequence(resources, "skills")
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if !poolHasSkill(pool, id) {
			pool.Content = append(pool.Content, skillEntry(id))
			changed = true
		}
	}
	for index := 0; index+1 < len(profiles.Content); index += 2 {
		profile := profiles.Content[index+1]
		if profile.Kind != yaml.MappingNode {
			continue
		}
		uses, err := ensureMapping(profile, "uses")
		if err != nil {
			return false, err
		}
		selected, err := ensureSequence(uses, "skills")
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if !sequenceHasScalar(selected, id) {
				selected.Content = append(selected.Content, stringNode(id))
				changed = true
			}
		}
	}
	if !changed {
		return false, nil
	}
	encoded, err := encodeSpec(&document)
	if err != nil {
		return false, err
	}
	if err := writeAtomic(specPath, encoded); err != nil {
		return false, err
	}
	return true, nil
}

// documentRoot returns a document node's root mapping, or nil when the document is not one.
func documentRoot(document *yaml.Node) *yaml.Node {
	node := document
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

// lookup returns the value a mapping holds for key, or nil when the key is absent.
func lookup(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

// ensureMapping returns the mapping under key, creating an empty one when the key is absent or holds no
// value at all ("resources:" with nothing under it). A key holding something else is refused: this
// client edits an agent spec, it does not reinterpret one.
func ensureMapping(parent *yaml.Node, key string) (*yaml.Node, error) {
	child := lookup(parent, key)
	if child == nil {
		created := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		parent.Content = append(parent.Content, stringNode(key), created)
		return created, nil
	}
	if child.Kind == yaml.MappingNode {
		return child, nil
	}
	if isNull(child) {
		*child = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		return child, nil
	}
	return nil, usageError(fmt.Sprintf("the agent spec's %s is not a block, so skills cannot be declared in it", key))
}

// ensureSequence is ensureMapping for a list.
func ensureSequence(parent *yaml.Node, key string) (*yaml.Node, error) {
	child := lookup(parent, key)
	if child == nil {
		created := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		parent.Content = append(parent.Content, stringNode(key), created)
		return created, nil
	}
	if child.Kind == yaml.SequenceNode {
		return child, nil
	}
	if isNull(child) {
		*child = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		return child, nil
	}
	return nil, usageError(fmt.Sprintf("the agent spec's %s is not a list, so skills cannot be declared in it", key))
}

func isNull(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && (node.Tag == "!!null" || node.Value == "")
}

// poolHasSkill reports whether the resources.skills pool already declares id.
func poolHasSkill(pool *yaml.Node, id string) bool {
	for _, entry := range pool.Content {
		if declared := lookup(entry, "id"); declared != nil && declared.Value == id {
			return true
		}
	}
	return false
}

// sequenceHasScalar reports whether a list already holds value as one of its scalars.
func sequenceHasScalar(sequence *yaml.Node, value string) bool {
	for _, entry := range sequence.Content {
		if entry.Kind == yaml.ScalarNode && entry.Value == value {
			return true
		}
	}
	return false
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// skillEntry is one resources.skills entry: the id, and the path inside the agent directory this client
// installs it at.
func skillEntry(id string) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		stringNode("id"), stringNode(id),
		stringNode("path"), stringNode(SkillsDir + "/" + id),
	}}
}

// encodeSpec renders the document with the two-space indent the agent specs in this tree use.
func encodeSpec(document *yaml.Node) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, usageError("cannot encode the agent spec: " + err.Error())
	}
	if err := encoder.Close(); err != nil {
		return nil, usageError("cannot encode the agent spec: " + err.Error())
	}
	return buffer.Bytes(), nil
}

// writeAtomic writes through a temporary file in the same directory and renames it into place, so a
// reader sees the old spec or the new one and never a half-written one - the rule the seat pin follows
// too, and the reason a spec is never observed truncated by a seat that boots while it is written.
func writeAtomic(path string, data []byte) error {
	handle, err := os.CreateTemp(filepath.Dir(path), ".agent-spec.*")
	if err != nil {
		return usageError("cannot write the agent spec " + path + ": " + err.Error())
	}
	name := handle.Name()
	remove := func() { _ = os.Remove(name) }
	if _, err := handle.Write(data); err != nil {
		_ = handle.Close()
		remove()
		return usageError("cannot write the agent spec " + path + ": " + err.Error())
	}
	if err := handle.Close(); err != nil {
		remove()
		return usageError("cannot write the agent spec " + path + ": " + err.Error())
	}
	if err := os.Chmod(name, 0o644); err != nil {
		remove()
		return usageError("cannot write the agent spec " + path + ": " + err.Error())
	}
	if err := os.Rename(name, path); err != nil {
		remove()
		return usageError("cannot write the agent spec " + path + ": " + err.Error())
	}
	return nil
}
