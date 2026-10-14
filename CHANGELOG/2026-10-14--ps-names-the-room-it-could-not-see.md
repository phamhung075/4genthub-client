## ps names the room it could not see, instead of an empty list with nothing to read

### What changed

`ps <room> --json` now says on stderr why it printed an empty list when the room matched nothing while tmux sessions
DID exist: `no seats for rig <room> on the server this process would use (N session(s) there, none named <room>-*)`.
An empty list and a verb that cannot see the room read identically on stdout, and that identity is the difference
F-A was reported over - a live room whose inventory came back empty with nothing to distinguish it from a blind verb.

The silence cases are pinned with the reason, because the reason belongs to exactly one case: no room was named, the
server held no sessions at all (`Run` already names that), or the room's seats are right there in the list.

### Fail-before / pass-after, measured

With a fake `tmux` on `PATH` reporting one session for another room, `ps leg-solo --nodes --json`:

- before this change: stdout `[]`, **stderr empty** - the ambiguity.
- after: stdout `[]`, stderr names the rig, the server and the session count.

Against this rig's own live room the verb is unchanged: five seats, empty stderr.

### The state-root half stays owed, and the reason is a runtime one

**CORRECTED 2026-10-14, after the architect's ruling (ARCH-RULING-u6-socket-selection-explicit-S-2026-10-14.md) and
a direct re-measurement.** The shas this corrects, named because a commit message cannot be edited without a
history rewrite: **`3f07cc2`** is where this entry was first committed with the wrong sentence, and **`28a93d1`**'s
message is where the reading came from — a reverted attempt whose "measured failing" conclusion was inherited by a
later author and then reproduced by me instead of re-tested. The offending paragraph said tmux "ignores TMUX_TMPDIR"
on this box. That is FALSE.** Measured
both ways on tmux 3.4 here:

- the base directory EXISTS: `mkdir -p /tmp/p4 && TMUX_TMPDIR=/tmp/p4 tmux -L p ls` → the error names
  `/tmp/p4/tmux-1000/p`, and tmux creates `/tmp/p4/tmux-1000` itself with mode `drwx------`. The variable IS honoured.
- the base directory is ABSENT: the same command with `/tmp/p4-absent` → the error names
  **`/tmp/tmux-1000/p2`**, the HOST DEFAULT server, and the scratch directory is never created. tmux falls back
  **silently**.

My probe's scratch directory was never created, so what it measured was the fallback, not the variable. The same
reading is why the earlier attempt's state-root knob "had no effect": the chosen base did not exist. `-S` is the only
form that cannot fall back - an absent socket is a hard error, as `tmux -S /tmp/absent/default ls` shows.

What this leaves owed is therefore narrower and better founded: `own_tmux_root` (main.rs:100-108) does create its root
and set the variable, so the runtime's private server IS created today; what lacks `-S` is **every tmux call**
(`pty/mod.rs:19-21` starts plain tmux). The architect's ruling is per-ROOM explicit sockets -
`-S <base state dir>/rooms/<room>/run/tmux-<uid>/default` on every call, root created 0700 before the room's first
session - and a `ps --tmux-socket` flag was refused because the runtime needs that path itself and a forgotten flag
reads the host server. That is the unit that makes the state-root half of F-A deliverable, and it is not built here.

