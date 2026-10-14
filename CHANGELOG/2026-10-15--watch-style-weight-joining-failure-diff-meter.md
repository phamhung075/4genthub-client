# Watch style: rule weight, joined calls, red after failure, aligned bodies, calmer meter

### Changed
- `internal/clientwatch/view.go`, `feed.go`: the rule before `~ think` and `◂ in` is thin and dim (`┄`); a call that directly follows the reasoning that led to it gets no rule; the rule after a failed step is red; continuation lines (diff bodies, multi-line text) align under the step glyph instead of the left edge; the context meter shows when it moves five points, not ten.
- `docs/WATCH.md` describes the rules.

### Testing
- `view_test.go` `TestRuleWeightJoiningAndFailure`; `go test ./internal/clientwatch` passes.
