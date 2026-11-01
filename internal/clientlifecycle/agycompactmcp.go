// `4genteam mcp compact`: the MCP server an agy seat loads beside agenthub_http and deepseek, over stdio, newline-framed
// JSON-RPC 2.0, with no dependency. Every tool call goes through compactTrigger.arrange, the one function the hook uses.
package clientlifecycle

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

const (
	mcpUsage           = "usage: 4genteam mcp compact"
	mcpDefaultProtocol = "2025-03-26"
	mcpMaxLine         = 16 << 20
)

type mcpCommand struct{}

func (mcpCommand) Name() string { return "mcp" }
func (mcpCommand) Summary() string {
	return "MCP servers the client serves over stdio to its seats (compact: compact_me, context_usage)"
}

func (mcpCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, mcpUsage,
			"compact serves two tools to the seat in this environment (AGENTHUB_ROOM, AGENTHUB_SEAT): compact_me arranges\n"+
				"the seat's /compact whatever its size, and context_usage reads its context. Either answer, past\n"+
				"COMPACT_LIMIT_TOKENS, also tells the model to end its turn. See docs/WATCH.md.")
		return clientcmd.ExitOK
	}
	if len(args) != 1 || args[0] != "compact" {
		fmt.Fprintln(stderr, mcpUsage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot("")
	if err == nil {
		err = seatlog.ApplyLimits()
	}
	if err != nil {
		fmt.Fprintf(stderr, "4genteam mcp: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	server := &compactMCP{
		Trigger: &compactTrigger{Root: root, Room: os.Getenv("AGENTHUB_ROOM"), Seat: os.Getenv("AGENTHUB_SEAT"),
			Now: wallClock, Spawn: spawnCompactSender},
		Log: seatlog.SeatLog,
	}
	if err := server.serve(os.Stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "4genteam mcp: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	return clientcmd.ExitOK
}

// compactMCP is the server; Log is the seam that names the seat's transcript.
type compactMCP struct {
	Trigger *compactTrigger
	Log     func(root, room, seat string) (string, string)
}

type mcpRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (m *compactMCP) serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), mcpMaxLine)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var request mcpRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err := encoder.Encode(mcpError(json.RawMessage("null"), -32700, "not JSON")); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			continue // a notification is answered by nothing
		}
		if err := encoder.Encode(m.answer(request)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func mcpError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

func mcpResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func (m *compactMCP) answer(request mcpRequest) map[string]any {
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := params.ProtocolVersion
		if version == "" {
			version = mcpDefaultProtocol
		}
		return mcpResult(request.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "4genteam-compact", "version": "1"},
		})
	case "ping":
		return mcpResult(request.ID, map[string]any{})
	case "tools/list":
		noArguments := map[string]any{"type": "object", "properties": map[string]any{}}
		return mcpResult(request.ID, map[string]any{"tools": []any{
			map[string]any{"name": "compact_me", "inputSchema": noArguments,
				"description": "Ask the client to compact this session. Call it at a safe point, then end your turn: " +
					"the client types /compact once you are at your prompt, then tells you to resume."},
			map[string]any{"name": "context_usage", "inputSchema": noArguments,
				"description": "Read how many tokens this session's context holds, against the client's compaction limit."},
		}})
	case "tools/call":
		var params struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(request.Params, &params)
		text, failed := m.call(params.Name)
		return mcpResult(request.ID, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
			"isError": failed,
		})
	}
	return mcpError(request.ID, -32601, "no method "+request.Method)
}

// call runs one tool: its own result, plus the instruction arrange answers past the limit.
func (m *compactMCP) call(name string) (string, bool) {
	trigger := m.Trigger
	if trigger.Room == "" || trigger.Seat == "" {
		return "this session is not a seat of a room (AGENTHUB_ROOM and AGENTHUB_SEAT are not set)", true
	}
	path, _ := m.Log(trigger.Root, trigger.Room, trigger.Seat)
	if path == "" {
		return "this seat's transcript could not be found", true
	}
	switch name {
	case "compact_me":
		tell := trigger.arrange(path, true)
		if tell == "" {
			return "A compact of this session is already running: end your turn now.", false
		}
		return tell, false
	case "context_usage":
		used, _ := lastContext(path)
		text := fmt.Sprintf("context: %s tokens, compaction limit %s", seatlog.Kilo(used), seatlog.Kilo(seatlog.CompactLimit))
		if tell := trigger.arrange(path, false); tell != "" {
			text += "\n" + tell
		}
		return text, false
	}
	return "no tool " + name, true
}
