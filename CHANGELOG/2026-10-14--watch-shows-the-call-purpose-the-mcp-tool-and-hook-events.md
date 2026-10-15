## watch shows the call's purpose, the MCP tool and the runtime's own events

### Changed
- `internal/clientwatch/render.go`: a tool call shows the main argument's value only, with no `key=` and no `(+other, keys)`; the seat's stated purpose (`i`) heads the call in dim italic and the argument follows below it. A result is headed by the tool that answered, and `wrapContent` keeps `toolName` and `isError` for it.
- A read or write on an `xd://mcp__<server>_<tool>` device renders as `→ mcp <server_tool>` with the request as its body.
- Records the runtime injects (`custom_message`, `ttsr_injection`, `custom` `session_exit`) render as orange `HOOK`, `RULE` and `EXIT` blocks, notices cut to three lines and stripped of their system tags.
- `docs/WATCH.md`: the mark table describes the new output.

### Tested
`go vet ./...` and `go test ./...` pass; new tests in `internal/clientwatch/render_test.go` cover the argument-only call, the purpose header, the MCP device call, the result's tool name and the three hook marks.
