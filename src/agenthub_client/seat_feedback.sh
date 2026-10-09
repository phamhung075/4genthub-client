#!/bin/sh
# Report friction from a seat whose runtime has no MCP.
#
# This is one of the two submission paths of the seat friction channel. The other is the MCP tool
# `submit_feedback`, which a seat on an MCP-capable runtime calls directly. Both reach
# POST /api/v2/openrig/feedback, so the channel has ONE writer contract: this script is an HTTP
# client and never touches storage or the service directly.
#
# Usage:
#   seat_feedback.sh --layer <layer> --text <what happened> [options]
#
#   --layer   REQUIRED. One of: runtime, openrig, cloud, seat-context, workspace, other
#   --text    REQUIRED. What happened, and what you expected instead (1-2000 characters).
#   --room    Optional. Defaults to the rig name from `rig whoami --json`.
#   --seat    Optional. Defaults to the member id from `rig whoami --json`.
#   --session Optional. Defaults to the session name from `rig whoami --json`.
#   --url     Optional. Defaults to the origin of $AGENTHUB_MCP_URL (its /mcp suffix removed),
#             then to $AGENTHUB_PUBLIC_URL. The route path is appended.
#
# Credential: $AGENTHUB_TOKEN, sent as a bearer token. It is never printed.
#
# Exit: 0 stored, 1 the request failed or the answer refused it, 2 usage or a missing value.

set -eu

layer=""
text=""
room=""
seat=""
session=""
base_url="${AGENTHUB_PUBLIC_URL:-}"

usage() {
    sed -n '3,24p' "$0" | sed 's/^# \{0,1\}//'
}

die_usage() {
    printf 'seat_feedback.sh: %s\n' "$1" >&2
    printf 'usage: seat_feedback.sh --layer <layer> --text <text> [--room R] [--seat S] [--session X] [--url BASE]\n' >&2
    exit 2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --layer) layer="${2:-}"; shift 2 ;;
        --text) text="${2:-}"; shift 2 ;;
        --room) room="${2:-}"; shift 2 ;;
        --seat) seat="${2:-}"; shift 2 ;;
        --session) session="${2:-}"; shift 2 ;;
        --url) base_url="${2:-}"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die_usage "unknown argument: $1" ;;
    esac
done

[ -n "$layer" ] || die_usage "--layer is required"
[ -n "$text" ] || die_usage "--text is required"

case "$layer" in
    runtime|openrig|cloud|seat-context|workspace|other) ;;
    *) die_usage "--layer \"$layer\" is not one of runtime, openrig, cloud, seat-context, workspace, other" ;;
esac

if [ -z "$base_url" ] && [ -n "${AGENTHUB_MCP_URL:-}" ]; then
    # The MCP URL is the platform origin plus /mcp; the REST route hangs off the same origin.
    base_url="${AGENTHUB_MCP_URL%/mcp}"
fi

# Identity defaults come from the client itself, which is the only authority on which seat this is.
if [ -z "$room" ] || [ -z "$seat" ] || [ -z "$session" ]; then
    whoami_json="$(rig whoami --json 2>/dev/null || true)"
    if [ -z "$whoami_json" ]; then
        die_usage "cannot read the seat identity (`rig whoami --json` failed); pass --room, --seat and --session"
    fi
    read_identity() {
        printf '%s' "$whoami_json" | python3 -c '
import json, sys
who = json.load(sys.stdin)
identity = who.get("identity") or {}
print(json.dumps({
    "room": identity.get("rigName") or "",
    "seat": identity.get("memberId") or "",
    "session": identity.get("sessionName") or "",
}))
'
    }
    identity="$(read_identity)"
    if [ -z "$room" ]; then
        room="$(printf '%s' "$identity" | python3 -c 'import json,sys; print(json.load(sys.stdin)["room"])')"
    fi
    if [ -z "$seat" ]; then
        seat="$(printf '%s' "$identity" | python3 -c 'import json,sys; print(json.load(sys.stdin)["seat"])')"
    fi
    if [ -z "$session" ]; then
        session="$(printf '%s' "$identity" | python3 -c 'import json,sys; print(json.load(sys.stdin)["session"])')"
    fi
fi

[ -n "$base_url" ] || die_usage "no server URL: set AGENTHUB_MCP_URL or pass --url"
[ -n "${AGENTHUB_TOKEN:-}" ] || die_usage "AGENTHUB_TOKEN is not set, so the report cannot be authenticated"

body="$(python3 -c '
import json, sys
print(json.dumps({
    "room": sys.argv[1],
    "seat": sys.argv[2],
    "session": sys.argv[3],
    "layer": sys.argv[4],
    "text": sys.argv[5],
}))
' "$room" "$seat" "$session" "$layer" "$text")"

url="${base_url%/}/api/v2/openrig/feedback"
answer="$(curl -sS -X POST "$url" \
    -H "Authorization: Bearer ${AGENTHUB_TOKEN}" \
    -H 'Content-Type: application/json' \
    --data-binary "$body" \
    -w '\n%{http_code}')" || {
    printf 'seat_feedback.sh: the request to %s failed\n' "$url" >&2
    exit 1
}

status="$(printf '%s' "$answer" | tail -n 1)"
payload="$(printf '%s' "$answer" | sed '$d')"
printf '%s\n' "$payload"

case "$status" in
    200) exit 0 ;;
    *)
        printf 'seat_feedback.sh: the server answered HTTP %s\n' "$status" >&2
        exit 1
        ;;
esac
