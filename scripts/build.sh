#!/bin/bash
# build.sh - build 4genteam from this checkout and install it, with the client's env file.
#
#   scripts/build.sh              go vet, go test, build, install ~/.local/bin/4genteam and agenthub-runtime, install .env
#   scripts/build.sh --no-test    skip go test (go vet still runs)
#   scripts/build.sh --respawn RIG
#                                 all of the above, then bring RIG back on what was just installed:
#                                 `4genteam up RIG --respawn` stops agent and replaces the runtime daemon
#                                 (a running daemon keeps the binary it was started with), starts the room
#                                 from its own spec.json and opens the watch view and the UI. The room's
#                                 record, seat directories and worktrees are left in place. The peak gate
#                                 still applies: an explicit --allow-peak is the caller's, never this script's.
#
# The checkout's .env, when it exists, REPLACES ~/.config/4genthub/.env (mode 600): that is the one
# file the client reads, so the file edited here is the file in effect after a build. A checkout
# without a .env leaves an installed file as it is, and when none is installed yet creates it from
# .env.sample (mode 600) for the user to fill in. The file's content is never printed.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
bin_dir=$HOME/.local/bin
env_dir=$HOME/.config/4genthub

# The arguments are read BEFORE anything is built: a --respawn without a rig is a caller error, and it must
# be said in one line rather than after a full build.
no_test=0
respawn=""
while [ $# -gt 0 ]; do
  case "$1" in
    --no-test) no_test=1 ;;
    --respawn)
      if [ $# -lt 2 ] || [ -z "$2" ]; then
        echo "build.sh: --respawn needs a rig name (e.g. --respawn 4genthub-min)" >&2
        exit 2
      fi
      respawn=$2
      shift
      ;;
    *)
      echo "build.sh: unknown argument $1 (usage: build.sh [--no-test] [--respawn RIG])" >&2
      exit 2
      ;;
  esac
  shift
done

cd "$root"
go vet ./...
if [ "$no_test" = 0 ]; then
  go test ./...
fi

# the runtime daemon is a second binary of this client: a change under rust/ reaches the machine only when
# it is built and installed here, or the daemon goes on running the last one that was
(cd "$root/rust/agenthub-runtime" && cargo build --release --offline --quiet)

# build beside the target and rename: a seat that is running the old binary keeps its inode
mkdir -p "$bin_dir"
go build -o "$bin_dir/.4genteam.new" ./cmd/4genteam
chmod 0755 "$bin_dir/.4genteam.new"
mv -f "$bin_dir/.4genteam.new" "$bin_dir/4genteam"
echo "installed $bin_dir/4genteam ($("$bin_dir/4genteam" version))"
cp "$root/rust/agenthub-runtime/target/release/agenthub-runtime" "$bin_dir/.agenthub-runtime.new"
chmod 0755 "$bin_dir/.agenthub-runtime.new"
mv -f "$bin_dir/.agenthub-runtime.new" "$bin_dir/agenthub-runtime"
echo "installed $bin_dir/agenthub-runtime (restart the daemon to run it)"

if [ -f "$root/.env" ]; then
  mkdir -p "$env_dir"
  chmod 0700 "$env_dir"
  install -m 600 "$root/.env" "$env_dir/.env"
  echo "installed $env_dir/.env from $root/.env"
elif [ -f "$env_dir/.env" ]; then
  echo "no $root/.env: $env_dir/.env left as it is"
else
  mkdir -p "$env_dir"
  chmod 0700 "$env_dir"
  install -m 600 "$root/.env.sample" "$env_dir/.env"
  echo "created $env_dir/.env from .env.sample: set AGENTHUB_TOKEN in it"
fi

# --respawn is the step the install message above names, done for the caller: agent stops, the daemon
# serving this state root is replaced, and `up` starts the room again from the binary just renamed into
# place. Everything else about the room - its record, its seats' directories, their worktrees - is left
# where it is. No --allow-peak is passed: the peak gate is the verb's decision, not this script's.
if [ -n "$respawn" ]; then
  echo "respawning $respawn on the installed binary (agent stops; the runtime daemon is replaced)"
  exec "$bin_dir/4genteam" up "$respawn" --respawn
fi
