#!/bin/bash
# Usage: send.sh <slug> --as <name>    (message body on stdin)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/_common.sh"

[[ $# -ge 1 ]] || die "usage: send.sh <slug> --as <name>  (body on stdin)"
SLUG="$1"; shift
NAME=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --as) NAME="${2:-}"; shift 2 ;;
    *) die "unknown arg: $1" ;;
  esac
done

[[ -n "$NAME" ]] || die "missing --as <name>"
validate_ident slug "$SLUG"
validate_ident name "$NAME"

BODY="$(cat)"
[[ -n "$BODY" ]] || die "empty message body (provide on stdin)"

ensure_channel "$SLUG"
LOG="$(channel_log "$SLUG")"
LOCK="$(channel_lock "$SLUG")"

acquire_lock "$LOCK"
trap 'release_lock "$LOCK"' EXIT

# Roster of currently-present members, as a JSON array. @mentions are resolved
# against it: only tokens naming a present member count as addressing. An
# unrecognized @token (e.g. a package name like "@vercel/otel", or a typo'd
# name) leaves the mentions array empty, so the message broadcasts rather than
# being silently narrowed to a peer who doesn't exist.
MEMBERS_JSON="$(channel_members "$SLUG" | jq -Rsc 'split("\n") | map(select(length > 0))')"

jq -nc \
  --arg ts "$(iso_now)" \
  --arg sender "$NAME" \
  --arg cwd "$(agent_cwd)" \
  --arg branch "$(agent_branch)" \
  --arg body "$BODY" \
  --argjson members "$MEMBERS_JSON" \
  '{ts:$ts, sender:$sender, cwd:$cwd, branch:$branch, kind:"msg", body:$body,
    mentions: ([$body | scan("(?<![a-zA-Z0-9_-])@([a-zA-Z0-9_-]{1,40})(?![a-zA-Z0-9_-])") | .[0]]
                | unique | map(select(. as $t | ($members | index($t)) != null)))}' \
  >> "$LOG"

release_lock "$LOCK"
trap - EXIT

# Sending is a natural moment to reap: a non-streaming agent (one that only
# ever sends) still helps clear out peers whose sessions were SIGKILLed.
reap_stale_peers "$SLUG" "$NAME"

echo "sent ($(wc -c <<<"$BODY" | tr -d ' ') bytes) to '$SLUG' as '$NAME'"
