---
name: install
description: Onboarding, environment audit, and configuration doctor for users starting with the 4genthub client on their PC. Detects missing configurations (.env, tokens, credentials, providers), checks daemon and safeguards (doctor), sets up required directory structures, and prepares the PC to run and manage teams cleanly.
---

# Client Install & Environment Doctor Skill

Use this skill when onboarding a new machine, setting up the 4genthub client (`4genteam`) on a user's PC, or when diagnosing missing environment configuration, credential failures, or broken safeguards.

---

## 1. Fast Diagnostic Checklist (`doctor`)

Run the built-in diagnostic and check the system prerequisites:

```bash
# 1. Check client version and binary availability
4genteam version
which 4genteam
which omp || which claude || which codex

# 2. Run the safeguards doctor
4genteam doctor
```

### Expected `doctor` Safeguards Status
- **`agent`**: running (the always-on client supervisor)
- **`ROOM compact`**: running (compaction supervisor loop)
- **`ROOM watchdog`**: running (restores dropped seats)
- **`bridge`**: running / syncing to cloud

If `bridge` reports `silent` or `doctor` exits `3`, run:
```bash
4genteam status <room>       # a room name is required when no default room is set
4genteam log <room>          # one room's log, newest last; `status` already prints its last lines
```

---

## 2. Environment Files Detection & Setup

The client expects configuration in fixed locations under the user's home directory — the **passwd**
home, not `$HOME`. Inside a seat pane `HOME` may point at the seat's own state directory,
so `~/.config/4genthub/.env` resolves to the passwd home.
The client resolves machine paths the same way (`internal/clientenv/env.go`, `RealHome()`). Resolve it
once and use it:

```bash
HOME_REAL="$(getent passwd "$(id -u)" | cut -d: -f6)"
ls -l "$HOME_REAL/.config/4genthub/.env"
```

Through §2 and §3, a `~` in a machine-level path (daemon, config, seat store) means `$HOME_REAL`.

### A. The Client's Primary Configuration: `~/.config/4genthub/.env`
Default path: `~/.config/4genthub/.env` (mode `0600`).

Required variables:
```bash
# Cloud Orchestration Connection
AGENTHUB_URL=https://api.4genthub.com       # or your local/staging endpoint
AGENTHUB_TOKEN=tok_...                     # User bearer token from 4genthub web UI

# Model Provider Credentials (for omp / headless DeepSeek seats)
DEEPSEEK_API_KEY=sk-...

# TypeSafe AI / Jev System One (runs on this machine: 4genteam decide, 4genteam complete)
TYPESAFE_API_KEY=apikey_...
TYPESAFE_BASE_URL=https://api.typesafe.ai/v1

# Safeguards & Compaction thresholds
COMPACT_LIMIT_TOKENS=150000
COMPACT_WARN_TOKENS=200000
COMPACT_HARD_TOKENS=250000
COMPACT_QUIET_SECONDS=15
OFFPEAK_ONLY=true
```

### B. Provider Environments: `~/.config/deepseek/`
For seats using `omp` with `deepseek/deepseek-flash` or `deepseek-chat`:
1. Ensure `~/.config/deepseek/` directory exists (mode `0700`).
2. Provide both files:
   - `~/.config/deepseek/env`
   - `~/.config/deepseek/.env`
   Both containing:
   ```bash
   DEEPSEEK_API_KEY=sk-...
   ```
3. Load the key globally in tmux so new seat sessions inherit it:
   ```bash
   set -a && source ~/.config/deepseek/env && set +a
   tmux set-environment -g DEEPSEEK_API_KEY "$DEEPSEEK_API_KEY"
   ```

### C. Launch Directory Symlinks: `<store>/<room>/.env`
When a room is materialized by the launcher (`4genteam up <room> --team <dir>`), verify that the launch directory has `.env` linked to a file that contains the necessary provider credentials:
```bash
# Check link
ls -la "$HOME_REAL/.config/4genthub/seats/<room>/.env"
# Verify key presence (never echo the value)
grep -c DEEPSEEK_API_KEY "$HOME_REAL/.config/4genthub/seats/<room>/.env"   # Must be >= 1
```

---

## 3. Automated Setup Script for New PC

When setting up a fresh PC, run the following sequence:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Machine-level paths come from the passwd home, not $HOME (a seat pane points HOME at its state dir).
HOME_REAL="$(getent passwd "$(id -u)" | cut -d: -f6)"

echo "=== 1. Creating Config Directories ==="
mkdir -p "$HOME_REAL/.config/4genthub"
mkdir -p "$HOME_REAL/.config/deepseek"
chmod 700 "$HOME_REAL/.config/4genthub" "$HOME_REAL/.config/deepseek"

echo "=== 2. Checking DeepSeek Provider Key ==="
if [ -f "$HOME_REAL/.config/deepseek/env" ]; then
    cp -n "$HOME_REAL/.config/deepseek/env" "$HOME_REAL/.config/deepseek/.env" || true
    echo "[✓] $HOME_REAL/.config/deepseek/env present"
else
    echo "[!] Warning: $HOME_REAL/.config/deepseek/env not found. Create it with DEEPSEEK_API_KEY=..."
fi

echo "=== 3. Checking Global Tmux Environment ==="
if command -v tmux >/dev/null 2>&1; then
    if [ -f "$HOME_REAL/.config/deepseek/env" ]; then
        set -a; source "$HOME_REAL/.config/deepseek/env"; set +a
        tmux set-environment -g DEEPSEEK_API_KEY "$DEEPSEEK_API_KEY" 2>/dev/null || true
        echo "[✓] DEEPSEEK_API_KEY set in global tmux environment"
    fi
fi

echo "=== 4. Running Safeguards Doctor ==="
4genteam doctor || echo "[i] Review doctor output above for any silent or missing services."
```

---

## 4. Common Doctor Failures & Remedies

| Symptom | Cause | Remedy |
|---|---|---|
| `[omp-runner] ERROR rpc: No API key found for deepseek` | `DEEPSEEK_API_KEY` missing from tmux environment or launch `.env` | Ensure key is in `~/.config/deepseek/env`, `~/.config/4genthub/.env`, and set in tmux with `tmux set-environment -g DEEPSEEK_API_KEY <key>` |
| `Cannot determine identity. Run inside an managed seat session` | Running `4genteam whoami` from host shell instead of inside a seat pane | Use `4genteam whoami --session <session-name> --json` or run from inside a tmux seat pane |
| `bridge: silent, age unknown` | Client background bridge is stopped or unauthenticated | Start client bridge with `4genteam bridge` or check `AGENTHUB_TOKEN` in `~/.config/4genthub/.env` |
| `Exit 3: at PEAK` | Attempted to start DeepSeek seats during peak pricing | Run `4genteam offpeak` to check window, or use `4genteam up <room> --allow-peak` if authorized |

---

## Link the client skills into Claude Code

A Claude Code session lists only what is under `~/.claude/skills/`; it does not scan this repository.
Link every `skills/client/*` and `skills/share/*` skill once, and again after adding one (idempotent,
never replaces an existing entry):

```bash
for d in <hub-root>/agenthub_client/skills/client/*/ <hub-root>/agenthub_client/skills/share/*/; do
  n=$(basename "$d"); [ -e "$HOME_REAL/.claude/skills/$n" ] || ln -s "${d%/}" "$HOME_REAL/.claude/skills/$n"
done
```

Seats do not use these links: the runtime installs their skills at each seat start.
