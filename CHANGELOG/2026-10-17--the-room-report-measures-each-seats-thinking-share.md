# The room report measures each seat's thinking share

## Added

- `internal/clientroom/report.go`: `4genteam room report ROOM --thinking` prints the per-seat thinking baseline for the window: assistant turns, turns whose content holds a `thinking` block, the provider's own `usage.reasoningTokens` summed against `usage.output`, and both shares (turn % and token %). `--json` carries the same fields unconditionally as `thinking_turns`, `thinking_tokens`, `thinking_turn_pct`, `thinking_token_pct`. The section is printed and never gated.
  - The token count is the provider's reasoning count, not a count of thinking text: a redacted block carries no text. A turn is a thinking turn by its content, so a reasoning count without a block moves the token share and not the turn share.
- `internal/clientroom/report_test.go`: `TestThinkingShareIsMeasuredPerSeatFromTheTranscript` pins a fixture transcript with 3 turns / 2 thinking / 100 thinking tokens / 200 output tokens - 50.0% token share and 66.7% turn share. The end-to-end daemon test now also asserts that `--thinking` prints the section (33.3% for the fixture's 1-of-3 thinking turn) and that `--json` carries `thinking_turns`.

## Measured

- `4genteam room report 4genthub-min --since 1h --thinking` at 2026-10-17T13:44:52Z on the live room, six seat directories (architect, fe-dev, go-dev, lead, other, reviewer), three with turns in the window: ROOM 64 turns, 46 thinking turns (71.9%), 11,577 thinking tokens of 29,408 output tokens (39.4%); go-dev 39 / 31 (79.5%) / 8,623 of 19,869 (43.4%); lead 25 / 15 (60.0%) / 2,954 of 9,539 (31.0%).

## Tested

- RED first: `go test ./internal/clientroom -run TestThinkingShareIsMeasuredPerSeatFromTheTranscript` failed with `turns/thinking turns/thinking tokens/output tokens = 3 0 0 200` before the reader summed the fields, then passed.
- `go test ./internal/clientroom`: ok. `go build ./...`: ok.
