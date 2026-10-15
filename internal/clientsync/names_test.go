package clientsync

import (
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func TestValidateNameMirrorsThePythonRule(t *testing.T) {
	for _, good := range []string{"room1", "a-b_c", "1x", "A", "a" + strings.Repeat("b", 40)} {
		if got, err := ValidateName("room", good); err != nil || got != good {
			t.Errorf("ValidateName(%q) = %q, %v; want it accepted", good, got, err)
		}
	}
	for _, bad := range []string{"", "-leading", ".dot", "a b", "../etc", "a/b", "_leading", "a\nb"} {
		_, err := ValidateName("seat", bad)
		if err == nil {
			t.Errorf("ValidateName(%q) accepted it", bad)
			continue
		}
		if !strings.Contains(err.Error(), "invalid seat name") || !strings.Contains(err.Error(), "expected [a-zA-Z0-9][a-zA-Z0-9_-]*") {
			t.Errorf("ValidateName(%q) = %q, want the Python's message", bad, err.Error())
		}
		if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUsage {
			t.Errorf("ValidateName(%q) code = %d, want %d", bad, code, clientcmd.ExitUsage)
		}
	}
}
