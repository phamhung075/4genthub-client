package clientheal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
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
		Rows: func(string) ([]clientservices.Health, error) {
			if r.down {
				return []clientservices.Health{{Room: "r", Safeguard: "agent", State: "stopped"}}, nil
			}
			return []clientservices.Health{{Room: "r", Safeguard: "agent", State: "running"}}, nil
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

// TestUpRoomRefusesToReExecTheTestBinary: heal's repair starts `up` as a child of this binary, and under
// `go test` this binary is the TEST binary - the child parses no -test.* flag and runs the whole suite again,
// which is the 2026-10-22 fork bomb. The refusal names the re-exec and nothing is spawned.
func TestUpRoomRefusesToReExecTheTestBinary(t *testing.T) {
	root := t.TempDir()
	err := upRoom("r", root)
	if err == nil {
		t.Fatal("upRoom spawned this binary as `up` under `go test`; the child would run the suite again")
	}
	if !strings.Contains(err.Error(), "re-exec") {
		t.Errorf("the refusal is %q, want it to name the re-exec", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("the refused repair still wrote into the root: %v", entries)
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

// healRun is Run for a case that expects a report: a probe error is a failure of the case, and the test
// that drives the refusal calls Run itself.
func healRun(t *testing.T, room string, d Deps) []Finding {
	t.Helper()
	findings, err := Run(room, d)
	if err != nil {
		t.Fatalf("Run(%s) refused: %v", room, err)
	}
	return findings
}

func TestACleanRoomHasNoFindings(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	for _, seat := range []string{"dev", "lead"} {
		r.seats[seat] = "running"
		r.env[seat] = map[string]bool{"AGENTHUB_URL": true, "AGENTHUB_TOKEN": true}
		_ = os.WriteFile(filepath.Join(clientruntime.SeatDir(r.root, "r", seat), "mcp.json"), []byte("{}"), 0o600)
	}
	if got := healRun(t, "r", r.deps(t, true)); len(got) != 0 {
		t.Fatalf("findings on a clean room: %+v", got)
	}
}

func TestASeatMissingTheEnvOrItsMcpConfigIsReseatedAndChecked(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	r.seats["dev"], r.seats["lead"] = "running", "stopped"
	r.env["dev"] = map[string]bool{"AGENTHUB_URL": true} // the token never reached it
	got := results(healRun(t, "r", r.deps(t, true)))
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
	got := results(healRun(t, "r", r.deps(t, true)))
	if got["seat-mcp r/dev"] != "fixed" || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestCheckOnlyChangesNothing(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	r.seats["dev"], r.seats["lead"] = "stopped", "stopped"
	got := results(healRun(t, "r", r.deps(t, false)))
	if got["seat-down r/dev"] != "would-fix" || len(r.reseats) != 0 {
		t.Fatalf("a check-only run acted: %v reseats=%v", got, r.reseats)
	}
}

func TestAMissingKeyIsLeftAndNeverPrinted(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=\nSECRET=hunter2\n")
	for _, seat := range []string{"dev", "lead"} {
		r.seats[seat] = "running"
	}
	findings := healRun(t, "r", r.deps(t, true))
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
	findings := healRun(t, "r", r.deps(t, true))
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

// TestAProbeErrorRefusesTheHeal: the safeguard report is the run's evidence, so a pgrep that could not run
// refuses the whole heal rather than reading as "nothing is down" (7.12). It answers an error and NO
// findings - an empty list is what a clean room looks like, and a repair is never applied on it.
func TestAProbeErrorRefusesTheHeal(t *testing.T) {
	r := newRoom(t, "")
	d := r.deps(t, true)
	d.Rows = func(string) ([]clientservices.Health, error) { return nil, errors.New("pgrep: not found") }

	findings, err := Run("r", d)
	if err == nil {
		t.Fatalf("Run healed a room from a probe that could not run: %v", findings)
	}
	if findings != nil {
		t.Errorf("Run answered %d finding(s) beside its error, want none", len(findings))
	}
	if len(r.reseats) != 0 || r.upCalls != 0 {
		t.Errorf("a refused run still applied repairs: reseats %v up calls %d", r.reseats, r.upCalls)
	}
}

// stubGate replaces the gate heal asks, and puts the real one back when the test ends.
func stubGate(t *testing.T, gate func(socket, name, verb string, allowPeak bool, stderr io.Writer) int) {
	t.Helper()
	real := peakGate
	peakGate = gate
	t.Cleanup(func() { peakGate = real })
}

// TestHealsGateRefusesBeforeTheStop is the order, observed rather than argued: the client is pointed at a
// socket nothing listens on, so a StopSeat that ran would come back as a dial error NAMING that path. The
// refusal that comes back instead is the gate's, and the socket path is absent from it - the stop never ran.
func TestHealsGateRefusesBeforeTheStop(t *testing.T) {
	var asked []string
	stubGate(t, func(socket, name, verb string, allowPeak bool, stderr io.Writer) int {
		asked = append(asked, socket+"|"+name+"|"+verb+"|"+strconv.FormatBool(allowPeak))
		fmt.Fprintf(stderr, "4genteam %s: PEAK right now\n", verb)
		return clientcmd.ExitUnavailable
	})
	socket := filepath.Join(t.TempDir(), "absent.sock")
	deps := machineDeps(t.TempDir(), clientruntime.NewClient(socket), true, false, io.Discard)

	err := deps.Reseat("room-a", "lead")
	if err == nil {
		t.Fatal("a refused reseat answered no error, so the finding would read fixed")
	}
	if !contains(err.Error(), "PEAK right now") {
		t.Errorf("error = %q, want the gate's own refusal", err)
	}
	if contains(err.Error(), socket) {
		t.Errorf("error = %q names the socket: the stop ran before the gate refused", err)
	}
	if len(asked) != 1 || asked[0] != socket+"|room-a|heal|false" {
		t.Errorf("the gate was asked %v, want one room-a/heal at the socket the verb resolved, flag off", asked)
	}
}

// TestTheFlagIsWhatHealsGateSees is the control: with --allow-peak the same call passes the gate and reaches
// the stop, which is what the absent-socket error below proves - the run moved PAST the gate.
func TestTheFlagIsWhatHealsGateSees(t *testing.T) {
	var asked []string
	stubGate(t, func(socket, name, verb string, allowPeak bool, stderr io.Writer) int {
		asked = append(asked, socket+"|"+name+"|"+verb+"|"+strconv.FormatBool(allowPeak))
		return clientcmd.ExitOK
	})
	socket := filepath.Join(t.TempDir(), "absent.sock")
	deps := machineDeps(t.TempDir(), clientruntime.NewClient(socket), true, true, io.Discard)

	err := deps.Reseat("room-a", "lead")
	if err == nil || !contains(err.Error(), socket) {
		t.Fatalf("error = %v, want the stop's dial refusal at %s (the gate passed)", err, socket)
	}
	if len(asked) != 1 || asked[0] != socket+"|room-a|heal|true" {
		t.Errorf("the gate was asked %v, want one room-a/heal at the socket the verb resolved, flag ON", asked)
	}
}

// TestARefusedRestartIsAnUnfixedFindingNamingTheSeat. The verb's exit rule counts every finding that is not
// "fixed", so the refusal has to land as one: the seat's own target, the gate's words in the detail, and the
// result "unfixed" rather than a repaired room reported over a seat left down.
func TestARefusedRestartIsAnUnfixedFindingNamingTheSeat(t *testing.T) {
	r := newRoom(t, "AGENTHUB_URL=u\nAGENTHUB_TOKEN=t\n")
	r.seats["lead"] = "stopped"
	d := r.deps(t, true)
	d.Reseat = func(_, seat string) error {
		r.reseats = append(r.reseats, seat)
		return errors.New("4genteam heal: 1 seat(s) run a DeepSeek model (r.lead: deepseek/deepseek-flash) and DeepSeek bills at PEAK right now")
	}

	findings := healRun(t, "r", d)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want the one stopped seat", findings)
	}
	got := findings[0]
	if got.Check != "seat-down" || got.Target != "r/lead" {
		t.Errorf("finding = %+v, want seat-down on r/lead", got)
	}
	if got.Result != "unfixed" {
		t.Errorf("result = %q, want unfixed: the exit rule counts exactly these", got.Result)
	}
	if !contains(got.Detail, "reseat failed:") || !contains(got.Detail, "PEAK right now") {
		t.Errorf("detail = %q, want the fix named and the gate's refusal carried", got.Detail)
	}
}
