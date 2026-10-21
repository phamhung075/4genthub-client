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
  case "$name" in *TOKEN* | *KEY* | *SECRET* | *PASSWORD* | *PASSWD* | *CREDENTIAL*) clean_env+=(-u "$name") ;; esac
done < <(compgen -e)

# The peak gate refuses `up` anyway; checking first saves a build that could not be used.
if [ ${#peak[@]} -eq 0 ] && "$bin" offpeak 2>/dev/null | head -1 | grep -q '^PEAK'; then
  "$bin" offpeak | head -2 >&2
  echo "respawn.sh: it is PEAK; rerun off-peak, or pass --allow-peak" >&2
  exit 3
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
"${clean_env[@]}" "$bin" up "$room" --respawn --state-root "$state_root" "${peak[@]}"

# `up --respawn` re-execs itself in a new session and returns at once, so a seat that calls it survives
# its own restart (clientlifecycle/respawn.go). Wait for that detached `up` - argv[1] is `up` and its
# environment carries FOURGENTEAM_UP_RESPAWN_DETACHED=1. The agent it starts inherits the same marker and
# never exits, so the marker alone is not the test.
detached_up() {
  local p
  for p in $(pgrep -x 4genteam || true); do
    [ "$(tr '\0' '\n' <"/proc/$p/cmdline" 2>/dev/null | sed -n 2p)" = up ] || continue
    tr '\0' '\n' <"/proc/$p/environ" 2>/dev/null | grep -qx 'FOURGENTEAM_UP_RESPAWN_DETACHED=1' && echo "$p"
  done
  return 0
}
waited=0
while [ -n "$(detached_up)" ]; do
  [ "$waited" -lt 900 ] || { echo "respawn.sh: the detached up is still running after 900s; see $log" >&2; exit 1; }
  [ $((waited % 30)) -ne 0 ] || echo "waiting for the detached up (${waited}s; seats prove themselves, a cold cache takes longer)"
  sleep 5
  waited=$((waited + 5))
done
# The detached run truncates its log (O_TRUNC), so after the wait the whole file is this run's.
grep -vE ': kept$|^(state dir|socket): ' "$log" 2>/dev/null || true

if [ "$reseat" -eq 1 ]; then
  step "reseat"
  while read -r session _; do
    seat=${session#"$prefix"}
    printf '%s: ' "$seat"
    "${clean_env[@]}" "$bin" room reseat "$room" "$seat" --state-root "$state_root" "${peak[@]}" 2>&1 | tail -1
  done < <(seat_sessions)
fi

step "watch"
"$bin" watch --room "$room" 2>&1 | tail -1

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
echo "respawned $room: $count fresh seat(s) in $(($(date +%s) - started))s"
