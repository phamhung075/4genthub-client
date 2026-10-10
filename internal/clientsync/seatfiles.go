// The two readers that stand between a server's answer and this machine's filesystem: a snapshot's hash
// becomes a directory name and its file paths become the paths written under it, so both are validated
// before anything is created. A port that drops either writes wherever a server tells it to.
package clientsync

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// PathsPath is SEATS_PATH in agenthub_client/src/agenthub_client/seat_sync.py: one resolved seat, by room and seat.
const PathsPath = "/api/v2/openrig/seats"

// hashPattern is HASH_RE: a hash is a directory name, so it may not contain a separator, a space or
// anything else that would make it mean more than one path component.
var hashPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateHash ports validate_hash. The extra ".." check is not redundant with the pattern (a hash like
// "a..b" matches the pattern) and is what stops a hash that is a relative path segment of its own.
func ValidateHash(value string) (string, error) {
	if !hashPattern.MatchString(value) || strings.Contains(value, "..") {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("server returned an unsafe seat hash: %q", value),
			Code:    clientcmd.ExitUsage,
		}
	}
	return value, nil
}

// SafeRelative ports safe_relative and returns the path's components. EVERY rejection here is one the
// server could send on purpose or by accident, and each would otherwise escape the snapshot directory:
// an absolute path, a leading separator, a BACKSLASH (which is a separator on another platform and a
// legal character on this one), and any component that is empty, "." or ".." - which is what makes
// "a//b", "./x" and ".." unsafe as well as "../evil".
//
// The Python's message uses {path!r}, the repr; %q is Go's nearest equivalent for a string, and the
// difference is in quoting style rather than in what an operator learns.
func SafeRelative(path string) ([]string, error) {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) || strings.Contains(path, `\`) {
		return nil, unsafeFilePath(path)
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, unsafeFilePath(path)
		}
	}
	return parts, nil
}

func unsafeFilePath(path string) error {
	return &clientcmd.CodedError{
		Message: fmt.Sprintf("server returned an unsafe file path: %q", path),
		Code:    clientcmd.ExitUsage,
	}
}

// FileEntry is one file of a resolved snapshot: the components of its path (already validated) and its
// content. The Python pairs a PurePosixPath with the text; components are the same thing with the
// separators kept out of the value, so nothing downstream can re-join them wrongly.
type FileEntry struct {
	Parts   []string
	Content string
}

// ExtractFiles ports extract_files: the `files` list must exist, every entry must be an object with a
// string path and string content, and each path must survive safe_relative. The two refusals are
// EXIT_REMOTE (the server's own answer is malformed) while an unsafe path is EXIT_USAGE - the Python's
// split, and worth keeping: one is "the cloud is wrong", the other is "the cloud is dangerous".
func ExtractFiles(resolved map[string]any) ([]FileEntry, error) {
	raw, ok := resolved["files"].([]any)
	if !ok {
		return nil, &clientcmd.CodedError{Message: "resolved_seat has no files list", Code: clientcmd.ExitRemote}
	}
	entries := make([]FileEntry, 0, len(raw))
	for _, item := range raw {
		file, isObject := item.(map[string]any)
		if !isObject {
			return nil, malformedFileEntry()
		}
		path, pathIsString := file["path"].(string)
		content, contentIsString := file["content"].(string)
		if !pathIsString || !contentIsString {
			return nil, malformedFileEntry()
		}
		parts, err := SafeRelative(path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, FileEntry{Parts: parts, Content: content})
	}
	return entries, nil
}

func malformedFileEntry() error {
	return &clientcmd.CodedError{
		Message: "resolved_seat contains a malformed file entry",
		Code:    clientcmd.ExitRemote,
	}
}
