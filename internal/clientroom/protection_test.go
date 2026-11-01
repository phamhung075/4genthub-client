package clientroom

import (
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// A status case is about the seat rows, so it starts with nothing to say about the machine's process table.
func init() { roomProtection = func(string) string { return "" } }

func TestRoomProtectionSaysWhatARoomRunsWithout(t *testing.T) {
	saved, savedLive := roomProtection, clientservices.Live
	defer func() { roomProtection, clientservices.Live = saved, savedLive }()
	roomProtection = protectionOf
	t.Setenv("AGENTHUB_SEAT_STORE", t.TempDir())
	clientservices.Live = func(string) ([]int, error) { return nil, nil }
	if got := roomProtection("demo"); !strings.Contains(got, "UNPROTECTED") || !strings.Contains(got, "up demo --room-only") {
		t.Errorf("no guard: %q", got)
	}
	clientservices.Live = func(pattern string) ([]int, error) {
		if strings.Contains(pattern, "stuck demo --loops") {
			return []int{7}, nil
		}
		return nil, nil
	}
	if got := roomProtection("demo"); !strings.Contains(got, "loop guard only") {
		t.Errorf("guard alone: %q", got)
	}
}
