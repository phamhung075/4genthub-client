package clientsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// switchPut is one recorded occupant PUT: its path, its decoded body and the Authorization header.
type switchPut struct {
	path string
	body map[string]any
	auth string
}

// switchCloud is the fake cloud: the seats list a GET answers, the status a PUT answers, and every PUT
// it recorded. It stands in for the Python tests' SeatServer.
type switchCloud struct {
	occupants []map[string]any
	putStatus int
	puts      []switchPut
}

func (c *switchCloud) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == RoomsPath+"/room1/seats" {
				switchWriteJSON(w, http.StatusOK, map[string]any{"success": true, "seats": c.occupants})
				return
			}
			http.NotFound(w, r)
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			c.puts = append(c.puts, switchPut{r.URL.Path, body, r.Header.Get("Authorization")})
			status := c.putStatus
			if status == 0 {
				status = http.StatusOK
			}
			if status == http.StatusOK {
				switchWriteJSON(w, status, map[string]any{"success": true, "seat": map[string]any{}})
				return
			}
			switchWriteJSON(w, status, map[string]any{"success": false, "error": "no"})
		}
	}
}

func switchWriteJSON(w http.ResponseWriter, status int, body any) {
	encoded, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// switchSeatStub answers the verb's two runtime calls - set-model and the fresh restart - and records each in
// order as {step, room, seat[, model]}. failure scripts the runtime's answer to every call, so a test drives
// the absent-daemon, refused and failed branches without a daemon.
type switchSeatStub struct {
	failure error
	calls   [][]string
}

func (s *switchSeatStub) setModel(room, seat, model string) error {
	s.calls = append(s.calls, []string{"set-model", room, seat, model})
	return s.failure
}

func (s *switchSeatStub) restartFresh(room, seat string) error {
	s.calls = append(s.calls, []string{"restart-fresh", room, seat})
	return s.failure
}

func switchStubSeats(t *testing.T, stub *switchSeatStub) {
	t.Helper()
	originalModel, originalRestart := switchSetModel, switchRestartFresh
	switchSetModel, switchRestartFresh = stub.setModel, stub.restartFresh
	t.Cleanup(func() { switchSetModel, switchRestartFresh = originalModel, originalRestart })
}

func switchOccupant(runtime, model string) map[string]any {
	return map[string]any{"seat_key": "seat1", "runtime": runtime, "model": model}
}

// switchRun stands up the fake cloud, points the env at it and runs the verb.
func switchRun(t *testing.T, cloud *switchCloud, argv []string) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(cloud.handler())
	t.Cleanup(server.Close)
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "test-token")
	var stdout, stderr bytes.Buffer
	code := RunSwitchVerb(context.Background(), argv, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// test_switch_model_puts_body_keeps_runtime_and_sets_model.
func TestSwitchModelPutsBodyKeepsRuntimeAndSetsModel(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--model", "new-model", "--reason", "why"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut)
	}
	if len(cloud.puts) != 1 {
		t.Fatalf("put %d times, want 1", len(cloud.puts))
	}
	put := cloud.puts[0]
	if put.path != "/api/v2/openrig/rooms/room1/seats/seat1/occupant" {
		t.Errorf("PUT %s, want the occupant path", put.path)
	}
	if put.auth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the bearer token", put.auth)
	}
	if want := map[string]any{"runtime": "claude-code", "model": "new-model"}; !reflect.DeepEqual(put.body, want) {
		t.Errorf("body = %v, want %v", put.body, want)
	}
	want := [][]string{{"set-model", "room1", "seat1", "new-model"}}
	if got := stub.calls; !reflect.DeepEqual(got, want) {
		t.Errorf("runtime calls = %v, want %v", got, want)
	}
	if !strings.Contains(errOut, "applies at the seat's next start") {
		t.Errorf("stderr = %q, want the note that a model change waits for the next start", errOut)
	}
	if want := "switched:room1/seat1 runtime=claude-code model=new-model applied=set-model\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if strings.Contains(out+errOut, "test-token") {
		t.Errorf("the token leaked into the output:\n%s\n%s", out, errOut)
	}
}

// test_switch_accepts_the_agy_runtime: agy is a legal runtime, and a runtime change is manual.
func TestSwitchAcceptsTheAgyRuntime(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, out, _ := switchRun(t, cloud, []string{"room1", "seat1", "--runtime", "agy"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if want := map[string]any{"runtime": "agy", "model": "old-model"}; !reflect.DeepEqual(cloud.puts[0].body, want) {
		t.Errorf("body = %v, want %v", cloud.puts[0].body, want)
	}
	if want := "switched:room1/seat1 runtime=agy model=old-model applied=manual\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

// test_switch_runtime_only_keeps_current_model_and_prints_manual_steps.
// The port's name for this pin said "prints manual steps"; the steps are gone (they were the rig orchestrator
// CLI's, and following them took the machine's safeguards down), so the name moved with the text.
func TestSwitchRuntimeOnlyKeepsCurrentModelAndPrintsTheLimit(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--runtime", "codex"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if want := map[string]any{"runtime": "codex", "model": "old-model"}; !reflect.DeepEqual(cloud.puts[0].body, want) {
		t.Errorf("body = %v, want %v", cloud.puts[0].body, want)
	}
	if len(stub.calls) != 0 {
		t.Errorf("a runtime change touched a seat: %v", stub.calls)
	}
	if strings.Contains(errOut, "rig down") || strings.Contains(errOut, "rig up") {
		t.Errorf("stderr = %q, want no rig CLI lifecycle verb in the notice", errOut)
	}
	if !strings.Contains(errOut, "4genteam sync room room1 --update") {
		t.Errorf("stderr = %q, want the client's own read of the room's definition", errOut)
	}
	if want := "switched:room1/seat1 runtime=codex model=old-model applied=manual\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

// test_switch_explicit_apply_none_with_runtime_change_skips_manual.
func TestSwitchExplicitApplyNoneWithRuntimeChangeSkipsManual(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--runtime", "codex", "--apply", "none"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if len(stub.calls) != 0 {
		t.Errorf("--apply none touched a seat: %v", stub.calls)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing: --apply none skips the manual steps too", errOut)
	}
	if !strings.HasSuffix(out, "applied=none\n") {
		t.Errorf("stdout = %q, want it to end applied=none", out)
	}
}

// test_switch_restart_sets_the_model_then_restarts_fresh_with_warning.
func TestSwitchRestartSetsTheModelThenRestartsFreshWithWarning(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud,
		[]string{"room1", "seat1", "--model", "new-model", "--apply", "restart", "--reason", "r"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	want := [][]string{
		{"set-model", "room1", "seat1", "new-model"},
		{"restart-fresh", "room1", "seat1"},
	}
	if got := stub.calls; !reflect.DeepEqual(got, want) {
		t.Errorf("runtime calls = %v, want %v", got, want)
	}
	if !strings.Contains(errOut, "warning") || !strings.Contains(errOut, "loses its live context") {
		t.Errorf("stderr = %q, want the restart warning", errOut)
	}
	if !strings.HasSuffix(out, "applied=restart\n") {
		t.Errorf("stdout = %q, want it to end applied=restart", out)
	}
}

// test_switch_runtime_change_prints_manual_steps_and_skips_rig — the port's name; the steps it printed
// are gone because following them took the machine's safeguards down, so this pin now asserts the limit.
func TestSwitchRuntimeChangePrintsTheLimitAndSkipsTheSeat(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--runtime", "codex", "--model", "m2"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if len(stub.calls) != 0 {
		t.Errorf("a runtime change touched a seat: %v", stub.calls)
	}
	for _, want := range []string{"4genteam sync room room1 --update", "4genteam seat reseat", "owner's call"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not say %q:\n%s", want, errOut)
		}
	}
	if want := "switched:room1/seat1 runtime=codex model=m2 applied=manual\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

// THE PIN THE 06:0xZ INCIDENT EARNED. The runtime-change notice used to print the rig CLI's own
// three-line sequence. Its middle line was measured on this box to stop the MACHINE's safeguards (agent
// reaped with its children, for every rig) and its last line could not run at all, so a reader who
// followed it took every rig's supervisors down and could not start the room again from it. A reader
// cannot tell the two vocabularies apart by name - `down` and `stop` read alike - so the notice may
// name no verb from the lead's machine-wide list (room-wide rule, 2026-10-14 06:08Z), and, since the
// cutover removes the tool, no rig CLI verb at all.
func TestSwitchRuntimeChangePrintsNoMachineWideVerb(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	_, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--runtime", "codex", "--model", "m2"})
	printed := out + errOut

	machineWide := []struct{ needle, why string }{
		{"rig down", "measured: a machine-wide safeguard stop - agent reaped with its children, for every rig"},
		{"rig up", "measured: fails with `--team DIR is required`, so the sequence cannot be finished"},
		{"4genteam stop", "a machine-wide stop of agent and its children"},
		{"rig launch", "a seat launch: lifecycle stays in the lead's hands"},
		{"seat launch", "the same launch, spelled the client's own way"},
		{"tmux kill-server", "it takes every pane on the machine with it"},
		{"tmux kill-session", "the same, narrower"},
	}
	for _, f := range machineWide {
		if strings.Contains(printed, f.needle) {
			t.Errorf("the runtime-change notice prints %q (%s):\n%s", f.needle, f.why, printed)
		}
	}

	for _, needle := range []string{"rig seat", "rig remove", "rig daemon", "rig bundle", "rig export", "rig ps", "rig queue"} {
		if strings.Contains(printed, needle) {
			t.Errorf("the runtime-change notice prints the rig CLI verb %q, which a reader cannot run after the cutover:\n%s", needle, printed)
		}
	}
}

// test_switch_apply_none_never_touches_a_seat.
func TestSwitchApplyNoneNeverTouchesASeat(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, out, _ := switchRun(t, cloud, []string{"room1", "seat1", "--model", "new-model", "--apply", "none"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if len(stub.calls) != 0 {
		t.Errorf("--apply none touched a seat: %v", stub.calls)
	}
	if !strings.HasSuffix(out, "applied=none\n") {
		t.Errorf("stdout = %q, want it to end applied=none", out)
	}
}

// TestSwitchAbsentRuntimeRecordsCloudAndExitsZero: the cloud change is the authority and is recorded first,
// and a machine with no runtime daemon is a NOTE - the verb answers applied=none and exits 0.
func TestSwitchAbsentRuntimeRecordsCloudAndExitsZero(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{failure: errSwitchRuntimeAbsent}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--model", "new-model"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d: the cloud change is recorded anyway", code, clientcmd.ExitOK)
	}
	if len(cloud.puts) != 1 {
		t.Errorf("put %d times, want 1", len(cloud.puts))
	}
	if !strings.Contains(errOut, "the agenthub runtime is not running") {
		t.Errorf("stderr = %q, want the note about the runtime", errOut)
	}
	if !strings.HasSuffix(out, "applied=none\n") {
		t.Errorf("stdout = %q, want it to end applied=none", out)
	}
}

// test_switch_seat_unknown_to_the_runtime_exits_0: a seat the runtime's room does not list is recorded in
// the cloud only, and nothing after the refused call runs.
func TestSwitchSeatUnknownToTheRuntimeExitsZero(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{failure: &clientruntime.DaemonRefused{Reason: "seat seat1 not found in room room1"}}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--model", "new-model", "--apply", "restart"})

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if !strings.Contains(errOut, "not in the runtime's records") {
		t.Errorf("stderr = %q, want the note that the runtime's records do not list the seat", errOut)
	}
	if len(stub.calls) != 1 {
		t.Errorf("runtime calls = %v, want only the set-model that was refused", stub.calls)
	}
	if !strings.HasSuffix(out, "applied=none\n") {
		t.Errorf("stdout = %q, want it to end applied=none", out)
	}
}

// test_switch_runtime_failure_exits_1: a call the runtime refused for another reason is a remote failure
// that names the step, and prints nothing to stdout.
func TestSwitchRuntimeFailureExitsOne(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{failure: &clientruntime.DaemonRefused{Reason: "cannot write room spec: disk full"}}
	switchStubSeats(t, stub)

	code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--model", "new-model"})

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "set-model of room1/seat1 failed") || !strings.Contains(errOut, "disk full") {
		t.Errorf("stderr = %q, want the step and the runtime's reason", errOut)
	}
}

// test_switch_put_error_exits_1, for both a 404 and a 409.
func TestSwitchPutErrorExitsOne(t *testing.T) {
	for _, status := range []int{404, 409} {
		t.Run(fmt.Sprintf("http_%d", status), func(t *testing.T) {
			cloud := &switchCloud{
				occupants: []map[string]any{switchOccupant("claude-code", "old-model")},
				putStatus: status,
			}
			stub := &switchSeatStub{}
			switchStubSeats(t, stub)

			code, out, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--model", "new-model"})

			if code != clientcmd.ExitRemote {
				t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if !strings.Contains(errOut, fmt.Sprintf("HTTP %d", status)) {
				t.Errorf("stderr = %q, want HTTP %d", errOut, status)
			}
			if got := stub.calls; len(got) != 0 {
				t.Errorf("rig was called after a failed PUT: %v", got)
			}
			if strings.Contains(errOut, "test-token") {
				t.Errorf("the token leaked: %q", errOut)
			}
		})
	}
}

// test_switch_unknown_seat_in_cloud_exits_1: the cloud never heard of the seat, so nothing is PUT.
func TestSwitchUnknownSeatInCloudExitsOne(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, _, errOut := switchRun(t, cloud, []string{"room1", "seat1", "--model", "new-model"})

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
	}
	if !strings.Contains(errOut, "not found in the cloud") {
		t.Errorf("stderr = %q, want the missing seat named", errOut)
	}
	if len(cloud.puts) != 0 {
		t.Errorf("put %d times for an unknown seat, want 0", len(cloud.puts))
	}
}

// test_switch_usage_errors_exit_2: six argument errors, each a usage error with nothing sent and no rig.
func TestSwitchUsageErrorsExitTwo(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		message string
	}{
		{"no_runtime_or_model", []string{"room1", "seat1"}, "at least one of"},
		{"invalid_runtime", []string{"room1", "seat1", "--runtime", "gpt"}, "invalid runtime"},
		{"invalid_model_dash", []string{"room1", "seat1", "--model=-bad"}, "invalid model"},
		{"invalid_model_empty", []string{"room1", "seat1", "--model", ""}, "invalid model"},
		{"invalid_room", []string{"../x", "seat1", "--model", "m"}, "invalid room name"},
		{"invalid_seat", []string{"room1", "se.at", "--model", "m"}, "invalid seat name"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
			stub := &switchSeatStub{}
			switchStubSeats(t, stub)

			code, out, errOut := switchRun(t, cloud, testCase.argv)

			if code != clientcmd.ExitUsage {
				t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if !strings.Contains(errOut, testCase.message) {
				t.Errorf("stderr = %q, want it to say %q", errOut, testCase.message)
			}
			if len(cloud.puts) != 0 || len(stub.calls) != 0 {
				t.Errorf("a usage error reached the cloud (%v) or rig (%v)", cloud.puts, stub.calls)
			}
		})
	}
}

// test_switch_invalid_apply_choice_exits_2.
func TestSwitchInvalidApplyChoiceExitsTwo(t *testing.T) {
	cloud := &switchCloud{occupants: []map[string]any{switchOccupant("claude-code", "old-model")}}
	stub := &switchSeatStub{}
	switchStubSeats(t, stub)

	code, _, _ := switchRun(t, cloud, []string{"room1", "seat1", "--model", "m", "--apply", "bogus"})

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
}
