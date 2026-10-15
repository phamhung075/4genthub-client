# short-output skill for every seat

### Added
- `skills/share/short-output/SKILL.md`: seats think in pointer lines, answer result-first in at most 5 short lines, read only the part of a tool output they need, and change docs once, in the code commit, when the task is done. Complements `token-economy` (documents and messages between seats). The skill keeps negations, numbers, paths, commands and blockers intact.

### Testing
- `go test ./internal/clientskills` passes (link check covers the new file).
