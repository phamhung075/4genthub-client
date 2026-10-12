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
which rig
which omp || which claude || which codex

# 2. Run the safeguards doctor
4genteam doctor
```

### Expected `doctor` Safeguards Status
- **`rigd`**: running (the always-on client supervisor)
- **`RIG compact`**: running (compaction supervisor loop)
- **`RIG watchdog`**: running (restores dropped seats)
- **`bridge`**: running / syncing to cloud

If `bridge` reports `silent` or `doctor` exits `3`, run:
```bash
4genteam status
4genteam log --lines 30
```

---

## 2. Environment Files Detection & Setup

The client expects configuration in fixed locations under the user's home directory.

### A. The Client's Primary Configuration: `~/.config/4genthub/.env`
Default path: `~/.config/4genthub/.env` (mode `0600`).

Required variables:
```bash
# Cloud Orchestration Connection
AGENTHUB_URL=https://api.4genthub.com       # or your local/staging endpoint
AGENTHUB_TOKEN=tok_...                     # User bearer token from 4genthub web UI

# OpenRig Daemon Connection
OPENRIG_URL=http://127.0.0.1:7433
OPENRIG_PORT=7433
OPENRIG_STATE_ROOT=/home/<user>/.openrig/state/omp

# Skills Directory
OPENRIG_CLIENT_SKILLS=/path/to/4genthub/agenthub_client/skills

# Model Provider Credentials (for omp / headless DeepSeek seats)
DEEPSEEK_API_KEY=sk-...

# Safeguards & Compaction thresholds
COMPACT_LIMIT_TOKENS=150000
COMPACT_WARN_TOKENS=180000
COMPACT_HARD_TOKENS=200000
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

### C. Launch Directory Symlinks: `<store>/<rig>/.env`
When materializing a rig via `4genteam sync rig <room>`, verify that the launch directory has `.env` linked to a file that contains the necessary provider credentials:
```bash
# Check link
ls -la ~/.openrig/agenthub-seats/<rig>/.env
# Verify key presence (never echo the value)
grep -c DEEPSEEK_API_KEY ~/.openrig/agenthub-seats/<rig>/.env   # Must be >= 1
```

---

## 3. Automated Setup Script for New PC

When setting up a fresh PC, run the following sequence:

```bash
#!/usr/bin/env bash
set -euo pipefail

echo "=== 1. Creating Config Directories ==="
mkdir -p ~/.config/4genthub
mkdir -p ~/.config/deepseek
mkdir -p ~/.openrig/agenthub-seats
chmod 700 ~/.config/4genthub ~/.config/deepseek

echo "=== 2. Checking OpenRig Daemon ==="
if [ ! -f ~/.openrig/daemon.json ]; then
    echo "[!] OpenRig daemon is not running. Starting daemon..."
    rig daemon start || true
else
    echo "[✓] OpenRig daemon active (PID $(jq .pid ~/.openrig/daemon.json 2>/dev/null || cat ~/.openrig/daemon.json))"
fi

echo "=== 3. Checking DeepSeek Provider Key ==="
if [ -f ~/.config/deepseek/env ]; then
    cp -n ~/.config/deepseek/env ~/.config/deepseek/.env || true
    echo "[✓] ~/.config/deepseek/env present"
else
    echo "[!] Warning: ~/.config/deepseek/env not found. Create it with DEEPSEEK_API_KEY=..."
fi

echo "=== 4. Checking Global Tmux Environment ==="
if command -v tmux >/dev/null 2>&1; then
    if [ -f ~/.config/deepseek/env ]; then
        set -a; source ~/.config/deepseek/env; set +a
        tmux set-environment -g DEEPSEEK_API_KEY "$DEEPSEEK_API_KEY" 2>/dev/null || true
        echo "[✓] DEEPSEEK_API_KEY set in global tmux environment"
    fi
fi

echo "=== 5. Running Safeguards Doctor ==="
4genteam doctor || echo "[i] Review doctor output above for any silent or missing services."
```

---

## 4. Common Doctor Failures & Remedies

| Symptom | Cause | Remedy |
|---|---|---|
| `[omp-runner] ERROR rpc: No API key found for deepseek` | `DEEPSEEK_API_KEY` missing from tmux environment or launch `.env` | Ensure key is in `~/.config/deepseek/env`, `~/.config/4genthub/.env`, and set in tmux with `tmux set-environment -g DEEPSEEK_API_KEY <key>` |
| `seat <seat> has no skills to deliver` | Rig was not synchronized with client skills tree | Run `OPENRIG_CLIENT_SKILLS=/path/to/skills 4genteam sync rig <room>` |
| `Cannot determine identity. Run inside an OpenRig-managed session` | Running `rig whoami` from host shell instead of inside a seat pane | Use `rig whoami --session <session-name> --json` or run from inside a tmux seat pane |
| `bridge: silent, age unknown` | Client background bridge is stopped or unauthenticated | Start client bridge with `4genteam bridge` or check `AGENTHUB_TOKEN` in `~/.config/4genthub/.env` |
| `Exit 3: at PEAK` | Attempted to start DeepSeek seats during peak pricing | Run `4genteam offpeak` to check window, or use `4genteam up <rig> --allow-peak` if authorized |
