# Seat guard: the .env rule matches a .env file name, not any text containing `.env`

### Fixed
- `rust/agenthub-runtime/assets/seat-guard.ts` `env-secret`: the rule matched `\.env\b` anywhere in a command, so `grep import.meta.env.VITE ... | head` and `.env.sample` were blocked (fe-dev, 18:30Z). It now matches `.env` or `.env.<suffix>` only as a path segment and lets the templates `.env.sample`, `.env.example` and `.env.template` through. `cat .env`, `head x/.env.local` and `source ./.env` still block.

### Testing
- The pattern was checked on six commands (three blocked, three allowed) with bun. Live on a seat after its next start, when the runtime rewrites the hook.
