# The agy seat's hook records the seat's conversation id

### Fixed
- `rust/agenthub-runtime/assets/seat-guard.ts` `recordConversation`: agy opens a new conversation at every seat start, and nothing wrote `<seat dir>/agy-conversation` (it had been written once by hand), so after a respawn `watch feed`, `watch` and `ps` followed the old conversation and the reviewer's pane showed nothing new. The hook payload carries `conversationId`; the seat's own guard hook now keeps the file equal to it.

### Testing
- Fed two payloads with different `conversationId` through `node seat-guard.ts --agy-hook pre`: the file follows the id, and the allow and deny answers are unchanged. `cargo test --offline` passes.
