package clientruntime

// The seat's current task on the wire (currenttask.go): whoami asks the hub's manage_task resume with
// no task_id and the identity's own <room>/<seat>, and the document leads with the answer. The tests
// below pin the three answer shapes apart - a task, NO task, an unanswered hub - because collapsing
// the last two into one would tell a compacted seat it holds nothing when in truth nobody answered.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// defaultHubMCPPost is the real wire call, kept so a test can exercise it against an in-process
// server after another test has replaced the seam.
var defaultHubMCPPost = hubMCPPost

// resumeEnvelope builds the JSON-RPC tools/call answer whose content text is the tool's own JSON.
func resumeEnvelope(toolJSON string) []byte {
	envelope, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": toolJSON}},
		},
	})
	return envelope
}

// stubHub replaces the wire for one test and returns a channel carrying each call it saw.
type hubCall struct {
	url   string
	token string
	seat  string
	body  []byte
}

func stubHub(t *testing.T, answer func() ([]byte, error)) <-chan hubCall {
	t.Helper()
	calls := make(chan hubCall, 4)
	old := hubMCPPost
	hubMCPPost = func(_ context.Context, url, token, seat string, body []byte) ([]byte, error) {
		calls <- hubCall{url: url, token: token, seat: seat, body: body}
		return answer()
	}
	t.Cleanup(func() { hubMCPPost = old })
	return calls
}

func seededSeat(t *testing.T) {
	t.Helper()
	hermeticSeat(t)
	noRuntimeSocket(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")
	t.Setenv(agentHubURLEnv, "https://hub.example")
	t.Setenv(agentHubTokenEnv, "tok")
}

// TestWhoamiLeadsWithTheTaskAndItBrief: the brief leads the document and the human form, and the call
// that produced it named the seat and no task.
func TestWhoamiLeadsWithTheTaskAndItBrief(t *testing.T) {
	seededSeat(t)
	calls := stubHub(t, func() ([]byte, error) {
		return resumeEnvelope(`{"success":true,"brief":{"task":{"id":"t-9","title":"lane"},"acceptance_criteria":["a"],"last_verdict":"fail","next_action":"rerun the test"}}`), nil
	})

	var out, errOut bytes.Buffer
	if code := (whoamiCommand{}).Run(context.Background(), []string{"--json"}, &out, &errOut); code != 0 {
		t.Fatalf("whoami --json exit = %d, stderr = %q", code, errOut.String())
	}

	var call hubCall
	select {
	case call = <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("whoami never asked the hub for the seat's task")
	}
	if call.url != "https://hub.example/mcp" || call.token != "tok" || call.seat != "room-a/go-dev" {
		t.Errorf("call = %s token=%q seat=%q, want the hub base, the token and this seat's own header", call.url, call.token, call.seat)
	}
	var wire struct {
		Method string `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(call.body, &wire); err != nil {
		t.Fatalf("the wire body is not JSON-RPC: %v", err)
	}
	if wire.Method != "tools/call" || wire.Params.Name != "manage_task" || wire.Params.Arguments["action"] != "resume" {
		t.Errorf("wire = %s %s %v, want one manage_task resume", wire.Method, wire.Params.Name, wire.Params.Arguments)
	}
	if _, named := wire.Params.Arguments["task_id"]; named {
		t.Errorf("the resume named a task_id; the hub must resolve it from the header (%v)", wire.Params.Arguments)
	}

	lead := strings.Index(out.String(), `"current_task"`)
	identity := strings.Index(out.String(), `"resolved_by"`)
	if lead < 0 || identity < 0 || lead > identity {
		t.Errorf("current_task is not the document's first key:\n%s", out.String())
	}
	var printed struct {
		CurrentTask map[string]any `json:"current_task"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not the identity document: %v", err)
	}
	if printed.CurrentTask["id"] != "t-9" {
		t.Errorf("current_task.id = %v, want t-9", printed.CurrentTask["id"])
	}
	brief, _ := printed.CurrentTask["resume"].(map[string]any)
	if brief["last_verdict"] != "fail" || brief["next_action"] != "rerun the test" {
		t.Errorf("current_task.resume = %v, want the hub's brief carried whole", printed.CurrentTask["resume"])
	}

	var human, humanErr bytes.Buffer
	if code := (whoamiCommand{}).Run(context.Background(), nil, &human, &humanErr); code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, humanErr.String())
	}
	if !strings.HasPrefix(human.String(), "Task:    t-9\nVerdict: fail\nNext:    rerun the test\n") {
		t.Errorf("human output does not lead with the brief:\n%s", human.String())
	}
}

// TestWhoamiStatesNoTaskAsNull: the hub's no_task_for_seat is the ONE answer that prints null, and
// the human form says so in words.
func TestWhoamiStatesNoTaskAsNull(t *testing.T) {
	seededSeat(t)
	stubHub(t, func() ([]byte, error) {
		return resumeEnvelope(`{"success":false,"error":{"message":"no_task_for_seat: go-dev holds no in_progress task"}}`), nil
	})

	var out, errOut bytes.Buffer
	if code := (whoamiCommand{}).Run(context.Background(), []string{"--json"}, &out, &errOut); code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	var printed struct {
		CurrentTask json.RawMessage `json:"current_task"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not the identity document: %v", err)
	}
	if string(printed.CurrentTask) != "null" {
		t.Errorf("current_task = %s, want an explicit null for a seat that holds nothing", printed.CurrentTask)
	}
	if !strings.Contains(out.String(), `"current_task": null`) {
		t.Errorf("the key was omitted rather than stated:\n%s", out.String())
	}

	var human, humanErr bytes.Buffer
	if code := (whoamiCommand{}).Run(context.Background(), nil, &human, &humanErr); code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, humanErr.String())
	}
	if !strings.Contains(human.String(), "Task:    none in progress on this seat") {
		t.Errorf("human output does not state the empty seat:\n%s", human.String())
	}
}

// TestWhoamiStatesAnUnansweredHubAsUnavailable: a hub that did not answer is a string, never null.
func TestWhoamiStatesAnUnansweredHubAsUnavailable(t *testing.T) {
	seededSeat(t)
	stubHub(t, func() ([]byte, error) {
		return nil, io.ErrUnexpectedEOF
	})

	var out, errOut bytes.Buffer
	if code := (whoamiCommand{}).Run(context.Background(), []string{"--json"}, &out, &errOut); code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	var printed struct {
		CurrentTask *string `json:"current_task"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not the identity document: %v", err)
	}
	if printed.CurrentTask == nil || !strings.HasPrefix(*printed.CurrentTask, "unavailable (") {
		t.Errorf("current_task = %v, want an unavailable string so a compacted seat is not told it holds nothing", printed.CurrentTask)
	}

	// No hub configured at all is the same honest answer, and it is not a network call.
	hermeticSeat(t)
	noRuntimeSocket(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")
	var bare bytes.Buffer
	if code := (whoamiCommand{}).Run(context.Background(), []string{"--json"}, &bare, &errOut); code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(bare.String(), `"current_task": "unavailable (AGENTHUB_URL is unset`) {
		t.Errorf("an unconfigured hub is not stated:\n%s", bare.String())
	}
}

// TestReadCurrentTaskAnswerReadsSSEAndRefusals: the streamable-HTTP transport may answer the same call
// as an SSE stream, and a JSON-RPC error is a refusal, not a brief.
func TestReadCurrentTaskAnswerReadsSSEAndRefusals(t *testing.T) {
	envelope := resumeEnvelope(`{"success":true,"brief":{"task":{"id":"t-1"}}}`)
	stream := append([]byte("event: message\ndata: "), envelope...)
	stream = append(stream, []byte("\n\n")...)

	brief, toolErr, err := readCurrentTaskAnswer(stream)
	if err != nil || toolErr != "" {
		t.Fatalf("SSE answer: brief=%v toolErr=%q err=%v", brief, toolErr, err)
	}
	if task, _ := brief["task"].(map[string]any); task["id"] != "t-1" {
		t.Errorf("SSE brief = %v, want the same brief as a JSON answer", brief)
	}

	_, _, err = readCurrentTaskAnswer([]byte(`{"jsonrpc":"2.0","id":1,"error":{"message":"unauthorized"}}`))
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("JSON-RPC refusal = %v, want its message", err)
	}
}

// TestHubCallCarriesTheSeatHeader: the real wire call - not a stub - sends the seat in
// X-Agenthub-Seat and the token in Authorization, because the hub resolves the task from them.
func TestHubCallCarriesTheSeatHeader(t *testing.T) {
	seededSeat(t)
	type seen struct {
		path, authorization, seat, accept string
		body                              []byte
	}
	requests := make(chan seen, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- seen{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			seat:          r.Header.Get("X-Agenthub-Seat"),
			accept:        r.Header.Get("Accept"),
			body:          body,
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resumeEnvelope(`{"success":true,"brief":{"task":{"id":"t-7"}}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(agentHubURLEnv, server.URL)
	t.Setenv(agentHubTokenEnv, "tok")
	previous := hubMCPPost
	hubMCPPost = defaultHubMCPPost
	t.Cleanup(func() { hubMCPPost = previous })

	identity := WhoamiResponse{}
	identity.Identity.RigName = "room-a"
	identity.Identity.MemberId = "go-dev"
	answer := ResolveCurrentTask(context.Background(), identity)

	var request seen
	select {
	case request = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub was never called")
	}
	if request.path != "/mcp" || request.authorization != "Bearer tok" || request.seat != "room-a/go-dev" {
		t.Errorf("request = %s %q seat=%q, want /mcp, the bearer token and the seat header", request.path, request.authorization, request.seat)
	}
	if !strings.Contains(request.accept, "text/event-stream") {
		t.Errorf("Accept = %q, want the streamable-HTTP pair", request.accept)
	}
	task, _ := answer.(map[string]any)
	if task["id"] != "t-7" {
		t.Errorf("answer = %v, want the task the hub named", answer)
	}
}
