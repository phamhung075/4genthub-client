package clientheal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// room is a fake machine: the seats and what each holds, and the repairs recorded.
type room struct {
	root    string
	seats   map[string]string          // seat -> state
	env     map[string]map[string]bool // seat -> keys held by the running process
	reseats []string
	upCalls int
	down    bool
	upWorks bool
}

func (r *room) deps(t *testing.T, fix bool) Deps {
	return Deps{
		StateRoot: r.root,
		Fix:       fix,
		Seats: func(string) ([]clientruntime.SeatStatus, error) {
			var out []clientruntime.SeatStatus
			for name, state := range r.seats {
				out = append(out, clientruntime.SeatStatus{Seat: name, State: state})
			}
			return out, nil
		},
		Reseat: func(_, seat string) error {
			r.reseats = append(r.reseats, seat)
			r.seats[seat] = "running"
			r.env[seat] = map[string]bool{"AGENTHUB_URL": true, "AGENTHUB_TOKEN": true}
			_ = os.WriteFile(filepath.Join(clientruntime.SeatDir(r.root, "r", seat), "mcp.json"), []byte("{}"), 0o600)
			return nil
		},
		ProcEnv: func(_, seat string) (map[string]bool, bool) { keys, ok := r.env[seat]; return keys, ok },
		Rows: func(string) []clientservices.Health {
			if r.down {
				return []clientservices.Health{{Rig: "r", Safeguard: "agent", State: "stopped"}}
			}
			return []clientservices.Health{{Rig: "r", Safeguard: "agent", State: "running"}}
		},
		Up: func(string) error {
			r.upCalls++
			if !r.upWorks {
				return errors.New("exit 3")
			}
			r.down = false
			return nil
		},
		Cause: func() string { return "the connector refused this credential" },
	}
}

func newRoom(t *testing.T, envFile string) *room {
	root := t.TempDir()
	if envFile != "" {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte(envFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, seat := range []string{"dev", "lead"} {
		if err := os.MkdirAll(clientruntime.SeatDir(root, "r", seat), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &room{root: root, seats: map[string]string{}, env: map[string]map[string]bool{}}
}

func results(findings []Finding) map[string]string {
	out := map[string]string{}
	for _, f := range findings {
		out[f.Check+" "+f.Target] = f.Result
	}
	return out
}

func TestACleanRoomHasNoFindings(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	for _, seat := range []string{"dev", "lead"} {
		r.seats[seat] = "running"
		r.env[seat] = map[string]bool{"AGENTHUB_URL": true, "AGENTHUB_TOKEN": true}
		_ = os.WriteFile(filepath.Join(clientruntime.SeatDir(r.root, "r", seat), "mcp.json"), []byte("{}"), 0o600)
	}
	if got := Run("r", r.deps(t, true)); len(got) != 0 {
		t.Fatalf("findings on a clean room: %+v", got)
	}
}

func TestASeatMissingTheEnvOrItsMcpConfigIsReseatedAndChecked(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	r.seats["dev"], r.seats["lead"] = "running", "stopped"
	r.env["dev"] = map[string]bool{"AGENTHUB_URL": true} // the token never reached it
	got := results(Run("r", r.deps(t, true)))
	for _, key := range []string{"seat-env r/dev", "seat-down r/lead"} {
		if got[key] != "fixed" {
			t.Errorf("%s = %q, want fixed (all: %v)", key, got[key], got)
		}
	}
}

func TestASeatWithoutItsMcpConfigIsReseated(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	r.seats["dev"], r.seats["lead"] = "running", "running"
	for _, seat := range []string{"dev", "lead"} {
		r.env[seat] = map[string]bool{"AGENTHUB_URL": true, "AGENTHUB_TOKEN": true}
	}
	_ = os.WriteFile(filepath.Join(clientruntime.SeatDir(r.root, "r", "lead"), "mcp.json"), []byte("{}"), 0o600)
	got := results(Run("r", r.deps(t, true)))
	if got["seat-mcp r/dev"] != "fixed" || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestCheckOnlyChangesNothing(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	r.seats["dev"], r.seats["lead"] = "stopped", "stopped"
	got := results(Run("r", r.deps(t, false)))
	if got["seat-down r/dev"] != "would-fix" || len(r.reseats) != 0 {
		t.Fatalf("a check-only run acted: %v reseats=%v", got, r.reseats)
	}
}

func TestAMissingKeyIsLeftAndNeverPrinted(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=\nSECRET=hunter2\n")
	for _, seat := range []string{"dev", "lead"} {
		r.seats[seat] = "running"
	}
	findings := Run("r", r.deps(t, true))
	if results(findings)["env-key AGENTHUB_TOKEN"] != "left" {
		t.Fatalf("an empty token must be left for the owner: %+v", findings)
	}
	for _, f := range findings {
		if f.Detail == "hunter2" || f.Target == "hunter2" {
			t.Fatalf("a value leaked: %+v", f)
		}
	}
}

func TestASafeguardThatStaysDownNamesItsCause(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	for _, seat := range []string{"dev", "lead"} {
		r.seats[seat], r.env[seat] = "running", map[string]bool{"AGENTHUB_URL": true, "AGENTHUB_TOKEN": true}
		_ = os.WriteFile(filepath.Join(clientruntime.SeatDir(r.root, "r", seat), "mcp.json"), []byte("{}"), 0o600)
	}
	r.down = true
	findings := Run("r", r.deps(t, true))
	if len(findings) != 1 || findings[0].Result != "unfixed" || r.upCalls != 1 {
		t.Fatalf("findings=%+v up=%d", findings, r.upCalls)
	}
	if want := "last cause: the connector refused this credential"; !contains(findings[0].Detail, want) {
		t.Fatalf("the cause is missing: %q", findings[0].Detail)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
