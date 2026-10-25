#!/bin/bash
# respawn.sh - rebuild the client, then bring a room back on FRESH seat processes, from the owner's shell.
#
#   scripts/respawn.sh ROOM              build (no go test), stop agent, close the room's seat sessions,
#                                        `up ROOM --respawn`, reseat every seat, reopen the watch view, verify
#   scripts/respawn.sh ROOM --test       the same, with go test in the build
#   scripts/respawn.sh ROOM --no-build   skip the build: respawn on the binaries already installed
#   scripts/respawn.sh ROOM --no-reseat  skip the reseat (the seats are already new processes)
#   scripts/respawn.sh ROOM --allow-peak pass the peak override to up and reseat; never the default
#   scripts/respawn.sh ROOM --state-root DIR   the state root (default ~/.config/4genthub)
#
# Why `build.sh --respawn` is not enough: `up --respawn` replaces the runtime daemon, but a seat's process
# survives in its tmux session and `up` adopts it, so the seat keeps the environment it started with - a
# rotated key in .env never reaches it, and a reseat restarts the session inside that same process. This
# script closes the room's seat sessions first, so every seat is a new process started by the new daemon.
#
# The env file is the only source of secrets: a variable already exported in the calling shell wins over
# the env file, so every exported name that looks like a credential (TOKEN, KEY, SECRET, PASSWORD,
# CREDENTIAL) is unset for `up` and `reseat`. No value is read or printed.
#
# Owner shell only. Run from a seat it would close that seat's own session, so it refuses there.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
bin=$HOME/.local/bin/4genteam

room=""
build=1
test_flag=--no-test
reseat=1
peak=()
state_root=$HOME/.config/4genthub
while [ $# -gt 0 ]; do
  case "$1" in
    --test) test_flag="" ;;
    --no-build) build=0 ;;
    --no-reseat) reseat=0 ;;
    --allow-peak) peak=(--allow-peak) ;;
    --state-root)
      [ $# -ge 2 ] && [ -n "$2" ] || { echo "respawn.sh: --state-root needs a directory" >&2; exit 2; }
      state_root=$2
      shift
      ;;
    -*)
      echo "respawn.sh: unknown argument $1 (usage: respawn.sh ROOM [--test] [--no-build] [--no-reseat] [--allow-peak] [--state-root DIR])" >&2
      exit 2
      ;;
    *)
      [ -z "$room" ] || { echo "respawn.sh: one room only (got $room and $1)" >&2; exit 2; }
      room=$1
      ;;
  esac
  shift
done
[ -n "$room" ] || { echo "respawn.sh: a room is required (e.g. respawn.sh 4genthub-min)" >&2; exit 2; }

if [ -n "${AGENTHUB_SEAT:-}" ]; then
  echo "respawn.sh: refusing inside seat $AGENTHUB_SEAT: closing the room's sessions would close this one" >&2
  exit 2
fi

# `4genteam stop` is MACHINE-WIDE (agent and its children for every room), so a mistyped room would stop
# supervision everywhere, close nothing, and let `up` fail on the unknown room. The room's own record is
# the test, and it fails closed: no record, no stop.
[ -f "$state_root/rooms/$room/spec.json" ] || {
  echo "respawn.sh: no room record at $state_root/rooms/$room/spec.json; a stop is machine-wide, so a mistyped room would stop every room" >&2
  exit 2
}

started=$(date +%s)
step() { printf '\n== %s (+%ss)\n' "$1" "$(($(date +%s) - started))"; }

# The room's own tmux server, the way the client addresses it (clientenv.TmuxArgv): TMUX unset, TMUX_TMPDIR
# pinned to <state-root>/tmux. Session names are agenthub--<room>--<seat>.
room_tmux() { env -u TMUX TMUX_TMPDIR="$state_root/tmux" tmux "$@"; }
prefix="agenthub--$room--"
seat_sessions() { room_tmux ls -F '#{session_name} #{session_created}' 2>/dev/null | grep "^$prefix" || true; }

# Every exported credential-shaped name, as `env -u NAME` pairs, so the env file is what the client reads.
clean_env=(env)
while read -r name; do
  case "$name" in
    COMPACT_HARD_TOKENS | COMPACT_LIMIT_TOKENS | COMPACT_WARN_TOKENS) continue ;;
    *TOKEN* | *KEY* | *SECRET* | *PASSWORD* | *PASSWD* | *CREDENTIAL*) clean_env+=(-u "$name") ;;
  esac
done < <(compgen -e)

# The peak gate refuses `up` anyway; checking first saves a build, a stop and the session closes that
# could not be used. `4genteam offpeak` ANSWERS with its status - exit 0 off-peak, 1 at peak - so the
# status is read directly: a pipeline takes the rightmost non-zero status under pipefail and would
# answer 1 at peak whatever the output said.
if [ ${#peak[@]} -eq 0 ]; then
  offpeak_rc=0
  offpeak_out=$("$bin" offpeak 2>&1) || offpeak_rc=$?
  case $offpeak_rc in
    0) ;;
    1)
      printf '%s\n' "$offpeak_out" >&2
      echo "respawn.sh: it is PEAK; rerun off-peak, or pass --allow-peak" >&2
      exit 3
      ;;
    *)
      printf '%s\n' "$offpeak_out" >&2
      echo "respawn.sh: 4genteam offpeak answered $offpeak_rc, so the peak gate cannot be read" >&2
      exit 1
      ;;
  esac
fi

if [ "$build" -eq 1 ]; then
  step "build ($root)"
  "$root/scripts/build.sh" $test_flag
fi

step "stop agent"
"$bin" stop "$room"

step "close the seat sessions of $room"
closed=0
while read -r session _; do
  [ -n "$session" ] || continue
  room_tmux kill-session -t "$session" && echo "closed $session" && closed=$((closed + 1))
done < <(seat_sessions)
echo "$closed session(s) closed"

step "up $room --respawn (secrets from the env file only)"
log=$state_root/up-respawn.log
up_rc=0
"${clean_env[@]}" "$bin" up "$room" --respawn --state-root "$state_root" "${peak[@]}" || up_rc=$?
# A failed up still gets the log printed and the verify run below: the failure is what those two exist to
# explain, so neither may be skipped by set -e exiting at this line.
if [ "$up_rc" -ne 0 ]; then
  echo "respawn.sh: up $room --respawn exited $up_rc" >&2
fi

# `up --respawn` re-execs itself in a new session and returns at once, so a seat that calls it survives
# its own restart (clientlifecycle/respawn.go). Wait for that detached `up` - argv[1] is `up`, argv[2] is
# THIS room (respawnArgv writes `up ROOM ... --respawn`), and its environment carries
# FOURGENTEAM_UP_RESPAWN_DETACHED=1. The agent it starts inherits the same marker and never exits, so the
# marker alone is not the test; and another room's detached up writes the same per-state-root log, so the
# room is what tells this run's restart from a stranger's.
detached_up() {
  local p argv
  for p in $(pgrep -x 4genteam || true); do
    argv=$(tr '\0' '\n' <"/proc/$p/cmdline" 2>/dev/null) || continue
    [ "$(printf '%s\n' "$argv" | sed -n 2p)" = up ] || continue
    [ "$(printf '%s\n' "$argv" | sed -n 3p)" = "$room" ] || continue
    tr '\0' '\n' <"/proc/$p/environ" 2>/dev/null | grep -qx 'FOURGENTEAM_UP_RESPAWN_DETACHED=1' && echo "$p"
  done
  return 0
}
waited=0
while [ "$up_rc" -eq 0 ] && [ -n "$(detached_up)" ]; do
  [ "$waited" -lt 900 ] || { echo "respawn.sh: the detached up is still running after 900s; see $log" >&2; exit 1; }
  [ $((waited % 30)) -ne 0 ] || echo "waiting for the detached up (${waited}s; seats prove themselves, a cold cache takes longer)"
  sleep 5
  waited=$((waited + 5))
done
# The detached run truncates its log (O_TRUNC), so after the wait the whole file is this run's.
if [ -f "$log" ]; then
  grep -vE ': kept$|^(state dir|socket): ' "$log" 2>/dev/null || true
else
  echo "respawn.sh: no $log was written" >&2
fi

if [ "$reseat" -eq 1 ] && [ "$up_rc" -eq 0 ]; then
  step "reseat"
  while read -r session _; do
    seat=${session#"$prefix"}
    printf '%s: ' "$seat"
    "${clean_env[@]}" "$bin" room reseat "$room" "$seat" --state-root "$state_root" "${peak[@]}" 2>&1 | tail -1
  done < <(seat_sessions)
fi

if [ "$up_rc" -eq 0 ]; then
  step "watch"
  "$bin" watch --room "$room" 2>&1 | tail -1
fi

# Verify: every seat session was created by this run, so no seat kept an old process or an old environment.
step "verify"
stale=0
count=0
while read -r session created; do
  [ -n "$session" ] || continue
  count=$((count + 1))
  if [ "$created" -lt "$started" ]; then
    echo "STALE ${session#"$prefix"}: session predates this run"
    stale=$((stale + 1))
  fi
done < <(seat_sessions)
"$bin" ps "$room" --nodes 2>&1 | grep -E "^(ROOM|$room)" || true
# Every seat runner is exec'd from the DAEMON's executable, so a daemon left on the socket from before the
# build serves the old code under fresh sessions (aa2-rs, 2026-10-25). Each runner of this room must run
# the installed file: a renamed-over binary reads "<path> (deleted)", another path names itself.
installed=$(readlink -f "$HOME/.local/bin/agenthub-runtime" 2>/dev/null || true)
old_exe=0
for p in $(pgrep -f "^[^ ]*agenthub-runtime --state-dir [^ ]+ runner $room/" || true); do
  exe=$(readlink "/proc/$p/exe" 2>/dev/null) || continue
  if [ "$exe" != "$installed" ]; then
    echo "OLD BINARY runner pid $p: runs $exe, not $installed"
    old_exe=$((old_exe + 1))
  fi
done
if [ "$count" -eq 0 ]; then
  echo "respawn.sh: no seat session of $room is running" >&2
  exit 1
fi
if [ "$count" -lt "$closed" ]; then
  echo "respawn.sh: $closed seat session(s) were closed and only $count came back; see $log" >&2
  exit 1
fi
if [ "$stale" -gt 0 ]; then
  echo "respawn.sh: $stale of $count seat(s) still run a process from before this run" >&2
  exit 1
fi
if [ "$old_exe" -gt 0 ]; then
  echo "respawn.sh: $old_exe runner(s) of $room run an old agenthub-runtime; a daemon from before the build still serves the socket - stop it by pid and rerun with --no-build" >&2
  exit 1
fi
[ "$up_rc" -eq 0 ] || { echo "respawn.sh: up failed (exit $up_rc); see $log" >&2; exit 1; }
echo "respawned $room: $count fresh seat(s) in $(($(date +%s) - started))s"
