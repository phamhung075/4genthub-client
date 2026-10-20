---
name: offpeak-gate
description: DeepSeek peak billing through the 4genthub client — read the schedule with `4genteam offpeak`, understand the exit-3 refusal from `up`, `compact`, `continue`, `watchdog` and `sync respawn`, and change the OFFPEAK_ONLY setting with `offpeak set`. Use when a room's start is refused "at PEAK", when an operator asks whether it is off-peak now, or when a DeepSeek room must be started inside a peak window.
---

# The DeepSeek peak gate

DeepSeek bills at half rate outside its peak windows. The client will not quietly start or resume a
DeepSeek seat inside one: it refuses, and the refusal names the seat, its model and when off-peak
resumes. That is the whole feature — nothing is broken, and no workaround is needed beyond the two the
message itself names.

## The schedule

| Window | Rate |
|---|---|
| 01:00-04:00 UTC and 06:00-10:00 UTC, Monday-Friday | PEAK (full rate) |
| every other hour, all of Saturday and Sunday, and any date in `DEEPSEEK_HOLIDAYS` | off-peak (half rate) |

`4genteam offpeak` prints the state and its exit code IS the answer: **0 off-peak, 1 peak**. So a
script or an agent gates itself with `4genteam offpeak --quiet && do-the-work` rather than by parsing
text. `--json` gives `state`, `utc`, `weekday`, `holiday`, `reason` and `next` as one object.

## Read and change the setting

```bash
4genteam offpeak status     # the setting, where it came from, the state now, and what the gate does
4genteam offpeak set off    # writes OFFPEAK_ONLY=0 (allow starts at any hour) into the env file
4genteam offpeak set on     # writes OFFPEAK_ONLY=1 (refuse at peak - the default)
```

`status` reads the setting from the flag, else `OFFPEAK_ONLY`, else the default (on). `set` writes ONE
line into the client's env file — `~/.config/4genthub/.env` — and prints nothing else from that file,
which also holds the account's credential.

**Never read that file, and never paste any of it into a message, a task or a log.** `offpeak set` is
the only way to change the setting: do not edit the file with an editor, `sed` or a shell redirect, and
do not `export` a token while you are there.

## The refusal, and what to do about it

`4genteam up ROOM`, `4genteam compact ROOM`, `4genteam continue ROOM`, `4genteam watchdog ROOM` and
`4genteam sync respawn ROOM SEAT` are the five verbs that start or resume seats, so each runs the gate
first. `continue` restores a stopped room and briefs its seats, and `watchdog` restores one whose tmux
sessions vanished. The refusal looks like this and exits **3**:

```
4genteam up: 1 seat(s) run a DeepSeek model (room-alpha.dev: deepseek/deepseek-flash) and DeepSeek
bills at PEAK right now - PEAK: 01:00-04:00 UTC (Mon-Fri), next: 04:00 UTC (off-peak resumes).
Refusing to start or resume them at the peak rate: pass --allow-peak (or set OFFPEAK_ONLY=0) to run
now, or wait and the gate opens by itself
```

The three legitimate responses, in the order to prefer them:

1. **Wait.** Off-peak arrives at the instant the message names; the gate then opens by itself with no
   action. If the work can wait, say so and stop — this costs nothing and is the default for a reason.
2. **`--allow-peak`** on that one command: run now, at the peak rate, without changing anything for
   later. Use it when the operator asked for the work now and accepted the price.
3. **`4genteam offpeak set off`**: allow DeepSeek starts at peak from now on. Use it only when the
   operator wants that as the machine's standing policy; say that you are changing a standing setting.

`--allow-peak` may sit before or after the verb's own arguments, and it is the only flag this gate
adds: each verb keeps the arguments of its own usage (`continue` also takes a brief file or
`--no-brief`; `watchdog` takes `--check`, `--watch` and `--self-test`; `sync respawn` takes
`--after-seconds` and `--reason`), and an argument a verb does not know is refused by name.

## What is NOT a refusal

- A room with no `deepseek/` seat passes, at any hour. The gate judges the `deepseek/` provider prefix
  on each seat's own record, not the room's name.
- A room whose seats cannot be read (the daemon is down, the room was never materialized) is **not**
  refused either: the verb's own error says what is actually wrong, and the two are different faults.
- A malformed `OFFPEAK_ONLY` (`OFFPEAK_ONLY=ture`) or `OFFPEAK_UTC` is a caller error, exit **2**,
  naming the variable. Fix the value; do not retry the command unchanged.

Report a refusal as what it is — the client refusing to spend at the peak rate — and never as a build
failure, a missing credential or a broken room.
