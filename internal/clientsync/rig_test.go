package clientsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// rigspecCloud serves whatever body the case under test wants, and counts the requests so the ordering
// claim (a bad ROOM never reaches the network) is measured rather than assumed.
func rigspecCloud(t *testing.T, body func() any) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if err := json.NewEncoder(w).Encode(body()); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

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

// TestReadRigspecPinsEveryShapeRefusal covers the six checks cmd_rig makes and the name rule it applies
// on top: every one is a message an operator can act on, and the code differs between "you named it
// badly" (usage) and "the cloud answered with something else" (remote).
func TestReadRigspecPinsEveryShapeRefusal(t *testing.T) {
	valid := func() any {
		return map[string]any{"success": true, "rigspec": map[string]any{
			"name": "room1", "yaml": "rig: {}\n",
			"seats": []any{map[string]any{"seat": "seat1", "hash": "h1a2b3c4"}},
		}}
	}
	withSeats := func(seats any) func() any {
		return func() any {
			return map[string]any{"success": true, "rigspec": map[string]any{
				"name": "room1", "yaml": "rig: {}\n", "seats": seats,
			}}
		}
	}

	for _, c := range []struct {
		name     string
		body     func() any
		wantCode int
		wantText string
	}{
		{"a rigspec for a different room", func() any {
			return map[string]any{"success": true, "rigspec": map[string]any{"name": "other", "yaml": "x", "seats": []any{}}}
		}, clientcmd.ExitRemote, "returned a rigspec for a different room"},
		{"no yaml text", func() any {
			return map[string]any{"success": true, "rigspec": map[string]any{"name": "room1", "seats": []any{}}}
		}, clientcmd.ExitRemote, "rigspec has no yaml text"},
		{"no seats key", func() any {
			return map[string]any{"success": true, "rigspec": map[string]any{"name": "room1", "yaml": "x"}}
		}, clientcmd.ExitRemote, "rigspec has no seats"},
		{"an empty seat list", withSeats([]any{}), clientcmd.ExitRemote, "rigspec has no seats"},
		{"an entry that is not an object", withSeats([]any{"seat1"}), clientcmd.ExitRemote, "malformed seat entry"},
		{"an entry whose seat is not a string", withSeats([]any{map[string]any{"seat": 1, "hash": "h1"}}), clientcmd.ExitRemote, "malformed seat entry"},
		{"an entry with no hash", withSeats([]any{map[string]any{"seat": "seat1"}}), clientcmd.ExitRemote, "rigspec seat entry has no hash"},
		{"a seat listed twice", withSeats([]any{
			map[string]any{"seat": "seat1", "hash": "h1"}, map[string]any{"seat": "seat1", "hash": "h2"},
		}), clientcmd.ExitRemote, "rigspec lists seat \"seat1\" twice"},
		{"a seat name the rule refuses", withSeats([]any{map[string]any{"seat": "../etc", "hash": "h1"}}), clientcmd.ExitUsage, "invalid seat name"},
	} {
		t.Run(c.name, func(t *testing.T) {
			server, _ := rigspecCloud(t, c.body)
			_, err := ReadRigspec(context.Background(), server.URL, "tok", "room1")
			if err == nil {
				t.Fatalf("ReadRigspec accepted %s", c.name)
			}
			if !strings.Contains(err.Error(), c.wantText) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantText)
			}
			if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != c.wantCode {
				t.Errorf("code = %d, want %d", code, c.wantCode)
			}
		})
	}

	t.Run("a bad room name never reaches the cloud", func(t *testing.T) {
		server, requests := rigspecCloud(t, valid)
		_, err := ReadRigspec(context.Background(), server.URL, "tok", "../etc")
		if err == nil || clientcmd.CodeOf(err, clientcmd.ExitOK) != clientcmd.ExitUsage {
			t.Fatalf("err = %v, want the name refusal at exit %d", err, clientcmd.ExitUsage)
		}
		if *requests != 0 {
			t.Errorf("the cloud was called %d times for an invalid room name; validation comes first", *requests)
		}
	})

	t.Run("a valid rigspec is read whole, in order", func(t *testing.T) {
		server, _ := rigspecCloud(t, func() any {
			return map[string]any{"success": true, "rigspec": map[string]any{
				"name": "room1", "yaml": "rig: {}\n",
				"seats": []any{
					map[string]any{"seat": "writer", "hash": "h3"},
					map[string]any{"seat": "lead", "hash": "h2"},
				},
			}}
		})
		spec, err := ReadRigspec(context.Background(), server.URL, "tok", "room1")
		if err != nil {
			t.Fatalf("ReadRigspec: %v", err)
		}
		if spec.Name != "room1" || spec.YAML != "rig: {}\n" {
			t.Errorf("spec = %+v", spec)
		}
		if len(spec.Seats) != 2 || spec.Seats[0].Seat != "writer" || spec.Seats[1].Hash != "h2" {
			t.Errorf("seats = %+v, want the cloud's order and hashes", spec.Seats)
		}
	})
}
