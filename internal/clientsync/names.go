// The name rule every seat and room name obeys before anything is written, which is what lets a verb
// refuse with nothing half-applied.
package clientsync

import (
	"fmt"
	"regexp"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// namePattern is NAME_RE: a room or seat name becomes a directory name, so it may not contain a
// separator, a space or anything else that would make it mean more than one path component.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidateName ports validate_name. The kind ("room", "seat") is in the message because the same rule
// is applied to both and an operator needs to know which name was refused.
func ValidateName(kind, value string) (string, error) {
	if !namePattern.MatchString(value) {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("invalid %s name: %q (expected [a-zA-Z0-9][a-zA-Z0-9_-]*)", kind, value),
			Code:    clientcmd.ExitUsage,
		}
	}
	return value, nil
}
