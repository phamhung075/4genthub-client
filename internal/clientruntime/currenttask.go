package clientruntime

// THE SEAT'S CURRENT TASK (4/8, the 1/8 ruling): `4genteam whoami` asks the hub for the brief of the
// task this seat holds in_progress, so a seat that was compacted, restarted or restored rebuilds from
// the task's own record instead of re-reading its transcript.
//
// THE HUB RESOLVES THE TASK, NOT THIS CLIENT. The call is `manage_task action=resume` with NO
// task_id; the hub reads the seat from the request's own X-Agenthub-Seat header - the same
// `<room>/<seat>` value the rendered MCP block carries - and answers the newest in_progress task
// assigned to it, or `no_task_for_seat`. The seat key is therefore never a parameter here: there is
// no argument through which one seat could ask for another seat's task.
//
// THE THREE ANSWERS ARE DISTINCT, and that is the point of the shape:
//   - a task:        {"id": <uuid>, "resume": <the hub's brief>}
//   - no task:       null (the hub said no_task_for_seat; the seat honestly holds nothing)
//   - a failed read: "unavailable (<reason>)" - NEVER a null, because a null would tell a compacted
//                    seat it holds nothing when the truth is that nothing answered.
//
// NOTHING OF THE BRIEF IS RE-DERIVED HERE. The token budget, the ledger reads and the next_action
// ladder are the hub's; this file transports its answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// hubCallTimeout bounds the one read. It is longer than the client's other calls because the hub
// builds the brief (a ledger read and a context read) inside this request, and shorter than a
// seat's turn: a seat whose hub is wedged must get the unavailable answer and continue.
const hubCallTimeout = 30 * time.Second

// hubClient is the HTTP seam, the same package-variable convention internal/clientfeedback and
// internal/clientevidence use: a test replaces its Transport so the wire is exercised in-process.
var hubClient = &http.Client{Timeout: hubCallTimeout}

// hubMCPPost is the wire call, separate from the answer's reading so a test can assert the URL, the
// headers and the exact JSON-RPC body without a server.
var hubMCPPost = func(ctx context.Context, url, token, seat string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	// The streamable-HTTP MCP transport's own Accept pair: the server may answer JSON or an SSE
	// stream for the same call, and both are read below.
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Agenthub-Seat", seat)
	resp, err := hubClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("the hub answered HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

// currentTaskWire is the JSON-RPC tools/call body: one manage_task resume, no task_id.
func currentTaskWire() []byte {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "manage_task",
			"arguments": map[string]any{"action": "resume"},
		},
	})
	return body
}

// ResolveCurrentTask answers the three shapes above for the given seat identity. It reads the hub
// base and credential from this shell's environment (AGENTHUB_URL, AGENTHUB_TOKEN - the same pair
// every other hub call uses) and the seat pair from the resolved identity.
func ResolveCurrentTask(ctx context.Context, identity WhoamiResponse) any {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(agentHubURLEnv)), "/")
	token := strings.TrimSpace(os.Getenv(agentHubTokenEnv))
	if base == "" {
		return "unavailable (AGENTHUB_URL is unset, so the hub was not asked)"
	}
	if token == "" {
		return "unavailable (AGENTHUB_TOKEN is unset, so the hub was not asked)"
	}
	room := strings.TrimSpace(identity.Identity.RoomName)
	seat := strings.TrimSpace(identity.Identity.MemberId)
	if room == "" || seat == "" {
		return "unavailable (this seat's identity names no room/seat)"
	}

	raw, err := hubMCPPost(ctx, base+"/mcp", token, room+"/"+seat, currentTaskWire())
	if err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	brief, toolErr, err := readCurrentTaskAnswer(raw)
	if err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	if toolErr != "" {
		if strings.Contains(toolErr, "no_task_for_seat") {
			return nil
		}
		return "unavailable (" + toolErr + ")"
	}
	taskSection, ok := brief["task"].(map[string]any)
	if !ok {
		return "unavailable (the hub's brief carries no task section)"
	}
	id, _ := taskSection["id"].(string)
	if id == "" {
		return "unavailable (the hub's brief carries no task id)"
	}
	return map[string]any{"id": id, "resume": brief}
}

// readCurrentTaskAnswer reads the MCP answer: a JSON-RPC envelope (possibly inside an SSE `data:`
// line), whose result content is the tool's own JSON text, whose `brief` is the resume document and
// whose `error.message` is a refusal.
func readCurrentTaskAnswer(raw []byte) (map[string]any, string, error) {
	envelopeBytes := raw
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] != '{' {
		envelopeBytes = sseDataPayload(trimmed)
		if envelopeBytes == nil {
			return nil, "", fmt.Errorf("the hub's answer is neither JSON nor an SSE data payload")
		}
	}
	var envelope struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(envelopeBytes, &envelope); err != nil {
		return nil, "", fmt.Errorf("the hub's answer is not a JSON-RPC document: %w", err)
	}
	if envelope.Error != nil {
		return nil, "", fmt.Errorf("%s", envelope.Error.Message)
	}
	if envelope.Result == nil || len(envelope.Result.Content) == 0 {
		return nil, "", fmt.Errorf("the hub's answer carries no tool content")
	}

	var tool struct {
		Success bool           `json:"success"`
		Brief   map[string]any `json:"brief"`
		Error   any            `json:"error"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &tool); err != nil {
		return nil, "", fmt.Errorf("the hub's tool answer is not JSON: %w", err)
	}
	if tool.Success && tool.Brief != nil {
		return tool.Brief, "", nil
	}
	switch e := tool.Error.(type) {
	case string:
		return nil, e, nil
	case map[string]any:
		if msg, ok := e["message"].(string); ok && msg != "" {
			return nil, msg, nil
		}
	}
	return nil, "", fmt.Errorf("the hub refused the resume without a reason")
}

// sseDataPayload finds the first `data:` line of an SSE stream and returns its bytes. The stream's
// other lines (event:, id:, retry:) carry no payload this call needs.
func sseDataPayload(raw []byte) []byte {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		return bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	}
	return nil
}
