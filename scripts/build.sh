#!/bin/bash
# build.sh - build 4genteam from this checkout and install it, with the client's env file.
#
#   scripts/build.sh              go vet, go test, build, install ~/.local/bin/4genteam, install .env
#   scripts/build.sh --no-test    skip go test (go vet still runs)
#
# The checkout's .env, when it exists, REPLACES ~/.config/4genthub/.env (mode 600): that is the one
# file the client reads, so the file edited here is the file in effect after a build. A checkout
# without a .env leaves an installed file as it is, and when none is installed yet creates it from
# .env.sample (mode 600) for the user to fill in. The file's content is never printed.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
bin_dir=$HOME/.local/bin
env_dir=$HOME/.config/4genthub

cd "$root"
go vet ./...
if [ "${1:-}" != "--no-test" ]; then
  go test ./...
fi

# build beside the target and rename: a seat that is running the old binary keeps its inode
mkdir -p "$bin_dir"
go build -o "$bin_dir/.4genteam.new" ./cmd/4genteam
chmod 0755 "$bin_dir/.4genteam.new"
mv -f "$bin_dir/.4genteam.new" "$bin_dir/4genteam"
echo "installed $bin_dir/4genteam ($("$bin_dir/4genteam" version))"

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
