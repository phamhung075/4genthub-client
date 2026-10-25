#!/bin/bash
# build.sh - build 4genteam from this checkout and install it, with the client's env file.
#
#   scripts/build.sh              go vet, go test, build, install ~/.local/bin/4genteam and agenthub-runtime, install .env
#   scripts/build.sh --no-test    skip go test (go vet still runs)
#   scripts/build.sh --force      install the checkout's .env even when it differs from the live one
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
# .env.sample (mode 600) for the user to fill in. A live file that DIFFERS from the checkout's is a
# credential and not a build artifact: the build refuses, names both paths and leaves the live file
# untouched, because a stale checkout copy silently undoes a rotation. --force installs it anyway.
# The file's content is never printed.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
bin_dir=$HOME/.local/bin
env_dir=$HOME/.config/4genthub

# The arguments are read BEFORE anything is built: a --respawn without a rig is a caller error, and it must
# be said in one line rather than after a full build.
no_test=0
forced=0
respawn=""
while [ $# -gt 0 ]; do
  case "$1" in
    --no-test) no_test=1 ;;
    --force) forced=1 ;;
    --respawn)
      if [ $# -lt 2 ] || [ -z "$2" ]; then
        echo "build.sh: --respawn needs a rig name (e.g. --respawn 4genthub-min)" >&2
        exit 2
      fi
      respawn=$2
      shift
      ;;
    *)
      echo "build.sh: unknown argument $1 (usage: build.sh [--no-test] [--force] [--respawn RIG])" >&2
      exit 2
      ;;
  esac
  shift
done

# A live .env that differs from the checkout's is refused BEFORE anything is built: the live file holds a
# credential, and a stale checkout copy would silently undo a rotation. cmp -s prints nothing, the message
# names the two paths and never their content, and --force is the deliberate override.
if [ -f "$root/.env" ] && [ -f "$env_dir/.env" ] && ! cmp -s "$root/.env" "$env_dir/.env"; then
  if [ "$forced" = 0 ]; then
    echo "build.sh: refusing to replace the live credential file: the two .env files differ" >&2
    echo "build.sh:   checkout: $root/.env" >&2
    echo "build.sh:   live:     $env_dir/.env" >&2
    echo "build.sh: reconcile them, or pass --force to install the checkout's copy over the live one" >&2
    exit 1
  fi
  echo "build.sh: --force: installing $root/.env over the differing $env_dir/.env" >&2
fi

cd "$root"
go vet ./...
if [ "$no_test" = 0 ]; then
  go test ./...
fi

# the runtime daemon is a second binary of this client: a change under rust/ reaches the machine only when
# it is built and installed here, or the daemon goes on running the last one that was
#
# CARGO DECIDES "UNCHANGED" BY MTIME, not content: a source whose mtime is older than the last build is
# skipped even when its text changed (a file written with a preserved or skewed timestamp; aa2-rs
# 2026-10-25, src/runner/mod.rs at 10:28 with content newer than the 10:33 binary, so a fix did not run).
# So the build keys on CONTENT: a hash of everything the binary is built from is stamped beside the
# release output, and when it differs from the stamp the crate's own artifacts are cleaned first (its
# dependencies stay built), so the next build compiles the text that is on disk.
runtime_dir=$root/rust/agenthub-runtime
runtime_stamp=$runtime_dir/target/release/.agenthub-runtime.src-sha256
runtime_hash=$(cd "$runtime_dir" && find src assets Cargo.toml Cargo.lock -type f -print0 | LC_ALL=C sort -z |
  xargs -0 sha256sum | sha256sum | cut -d' ' -f1)
if [ "$(cat "$runtime_stamp" 2>/dev/null || true)" != "$runtime_hash" ]; then
  (cd "$runtime_dir" && cargo clean --release --offline --quiet -p agenthub-runtime)
fi
(cd "$runtime_dir" && cargo build --release --offline --quiet)
printf '%s\n' "$runtime_hash" >"$runtime_stamp"

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
