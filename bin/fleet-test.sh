#!/usr/bin/env bash
#
# fleet-test.sh — hermetic check of the worker-fleet tooling (issue #17).
#
# spawn-fleet.sh / teardown-fleet.sh / docker-worker.sh orchestrate `docker`,
# which we don't want to actually run here. So we put a FAKE `docker` on PATH
# that records every invocation and keeps a tiny in-memory registry of
# "containers" keyed by their agent-chat-fleet label. That lets us assert the
# exact run/label/clone/teardown contract without pulling an image or starting a
# container. (The real round-trip against a live image is bin/docker-worker-test.sh.)
#
# Exit codes: 0 = pass, 2 = behavior mismatch.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fail() { echo "FAIL: $*" >&2; echo "----- docker.log -----" >&2; cat "$DOCKER_LOG" >&2; exit 2; }

# --- fake docker ------------------------------------------------------------
BINDIR="$TMP/bin"; mkdir -p "$BINDIR"
export DOCKER_LOG="$TMP/docker.log"; : > "$DOCKER_LOG"
export DOCKER_STATE="$TMP/state"; mkdir -p "$DOCKER_STATE"

cat > "$BINDIR/docker" <<'FAKE'
#!/usr/bin/env bash
# Records args; maintains $DOCKER_STATE/<name> -> fleet-label for ps/rm queries.
echo "$*" >> "$DOCKER_LOG"
cmd="${1:-}"; shift || true
case "$cmd" in
  image)  # image inspect <img>
    [[ "${FAKE_DOCKER_IMAGE_MISSING:-0}" == 1 ]] && exit 1
    exit 0 ;;
  run)
    name=""; label=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --name)  name="$2"; shift 2 ;;
        --label) [[ "$2" == agent-chat-fleet=* ]] && label="${2#agent-chat-fleet=}"; shift 2 ;;
        *)       shift ;;
      esac
    done
    [[ -n "$name" ]] && printf '%s\n' "$label" > "$DOCKER_STATE/$name"
    echo "$name"   # `docker run -d` echoes the container id
    exit 0 ;;
  ps)
    want=""; allfilter=0
    for a in "$@"; do
      case "$a" in
        label=agent-chat-fleet=*) want="${a#label=agent-chat-fleet=}" ;;
        label=agent-chat-fleet)   allfilter=1 ;;
      esac
    done
    for f in "$DOCKER_STATE"/*; do
      [[ -e "$f" ]] || continue
      cname="$(basename "$f")"; clabel="$(cat "$f")"
      if [[ -n "$want" ]]; then
        [[ "$clabel" == "$want" ]] && echo "$cname"
      elif [[ "$allfilter" == 1 ]]; then
        echo "$clabel"
      fi
    done
    exit 0 ;;
  rm)  # rm -f <names...>
    for a in "$@"; do
      [[ "$a" == -f ]] && continue
      rm -f "$DOCKER_STATE/$a" 2>/dev/null || true
    done
    exit 0 ;;
  *) exit 0 ;;
esac
FAKE
chmod +x "$BINDIR/docker"

export PATH="$BINDIR:$PATH"
export CLAUDE_CODE_OAUTH_TOKEN="fake-token-for-test"  # token auth path: no Keychain
export AGENT_CHAT_LAUNCH_SETTLE_SECS=0                # don't sleep in the launcher
unset GITHUB_TOKEN GH_TOKEN 2>/dev/null || true

FLEET_ID="testfleet"
ROOT="$TMP/agent-chat-fleet-$FLEET_ID"   # fleet-shaped so teardown will remove it
REPO="https://github.com/example/repo.git"

# === 1. spawn a fleet of 2 =================================================
echo "Spawning a fleet of 2..."
bin/spawn-fleet.sh -n 2 --id "$FLEET_ID" --root "$ROOT" --repo "$REPO" >/dev/null 2>&1 \
  || fail "spawn-fleet exited nonzero"

runs="$(grep -c '^run ' "$DOCKER_LOG" || true)"
[[ "$runs" == 2 ]] || fail "expected 2 'docker run' calls, got $runs"

# Every run must carry the fleet label and a clone of the target repo.
[[ "$(grep -c "label agent-chat-fleet=$FLEET_ID" "$DOCKER_LOG")" == 2 ]] \
  || fail "fleet label not on both run calls"
[[ "$(grep -c "AGENT_CHAT_CLONE_REPO=$REPO" "$DOCKER_LOG")" == 2 ]] \
  || fail "clone repo env not on both run calls"

# Unique container names, mounted at the EPHEMERAL fleet root (not the global one).
grep -q "name agent-chat-fleet-$FLEET_ID-w1" "$DOCKER_LOG" || fail "missing worker w1 container name"
grep -q "name agent-chat-fleet-$FLEET_ID-w2" "$DOCKER_LOG" || fail "missing worker w2 container name"
[[ "$(grep -c -- "-v $ROOT:/channel" "$DOCKER_LOG")" == 2 ]] || fail "channel root not mounted on both"
grep -q "\.claude/agent-chat:/channel" "$DOCKER_LOG" && fail "fleet leaked into the GLOBAL ~/.claude/agent-chat root"
[[ -d "$ROOT" ]] || fail "channel root $ROOT was not created"
[[ -f "$DOCKER_STATE/agent-chat-fleet-$FLEET_ID-w1" && -f "$DOCKER_STATE/agent-chat-fleet-$FLEET_ID-w2" ]] \
  || fail "fake docker did not register both containers"
echo "OK: 2 labeled, uniquely-named workers cloning $REPO onto the ephemeral root."

# === 2. teardown removes exactly that fleet + its root ======================
echo "Tearing the fleet down..."
: > "$DOCKER_LOG"
bin/teardown-fleet.sh "$FLEET_ID" --root "$ROOT" >/dev/null 2>&1 || fail "teardown exited nonzero"

grep -q "^rm -f .*agent-chat-fleet-$FLEET_ID-w1" "$DOCKER_LOG" || fail "teardown did not rm w1"
grep -q "agent-chat-fleet-$FLEET_ID-w2" "$DOCKER_LOG" || fail "teardown did not rm w2"
[[ -e "$DOCKER_STATE/agent-chat-fleet-$FLEET_ID-w1" ]] && fail "w1 still registered after teardown"
[[ -e "$ROOT" ]] && fail "channel root $ROOT not removed by teardown"
echo "OK: teardown removed both containers and the ephemeral root."

# === 3. teardown isolation: another fleet is untouched =====================
echo "Verifying teardown only touches the named fleet..."
OTHER_ID="otherfleet"; OTHER_ROOT="$TMP/agent-chat-fleet-$OTHER_ID"
bin/spawn-fleet.sh -n 1 --id "$OTHER_ID" --root "$OTHER_ROOT" --repo "$REPO" >/dev/null 2>&1 \
  || fail "spawn of second fleet failed"
bin/teardown-fleet.sh "$FLEET_ID" --root "$ROOT" >/dev/null 2>&1 || fail "teardown of (gone) first fleet errored"
[[ -f "$DOCKER_STATE/agent-chat-fleet-$OTHER_ID-w1" ]] || fail "tearing down '$FLEET_ID' wrongly removed '$OTHER_ID'"
bin/teardown-fleet.sh "$OTHER_ID" --root "$OTHER_ROOT" >/dev/null 2>&1 || fail "teardown of second fleet failed"
echo "OK: label-scoped teardown left the other fleet alone."

# === 4. refusal paths ======================================================
echo "Checking refusals..."
if bin/spawn-fleet.sh -n 0   --id r --root "$TMP/r0" --repo "$REPO" >/dev/null 2>&1; then fail "n=0 should be refused"; fi
if bin/spawn-fleet.sh -n 21  --id r --root "$TMP/r2" --repo "$REPO" >/dev/null 2>&1; then fail "n=21 should be refused (cap)"; fi
if FAKE_DOCKER_IMAGE_MISSING=1 bin/spawn-fleet.sh -n 1 --id r --root "$TMP/r3" --repo "$REPO" >/dev/null 2>&1; then
  fail "missing image should be refused"
fi
# Unknown-id teardown is a clean no-op, not an error.
bin/teardown-fleet.sh nonexistentfleet --root "$TMP/none" >/dev/null 2>&1 \
  || fail "teardown of an unknown fleet should exit 0 (idempotent)"
echo "OK: bad count, oversized count, and missing image all refused; unknown teardown is a no-op."

# === 5. docker-worker disallow/clone plumbing ==============================
echo "Checking docker-worker disallowed-tools plumbing..."
: > "$DOCKER_STATE"/* 2>/dev/null || true; rm -f "$DOCKER_STATE"/* 2>/dev/null || true; : > "$DOCKER_LOG"

bin/docker-worker.sh chan --container dwc-default --clone "$REPO" --label "agent-chat-fleet=x" >/dev/null 2>&1 \
  || fail "docker-worker default invocation failed"
grep -q "AGENT_CHAT_DISALLOWED_TOOLS" "$DOCKER_LOG" \
  && fail "default docker-worker must NOT set AGENT_CHAT_DISALLOWED_TOOLS (entrypoint defaults it)"

: > "$DOCKER_LOG"
bin/docker-worker.sh chan --container dwc-optout --disallow "" >/dev/null 2>&1 || fail "docker-worker --disallow '' failed"
grep -q -- "-e AGENT_CHAT_DISALLOWED_TOOLS=" "$DOCKER_LOG" \
  || fail "--disallow '' should pass an explicit empty AGENT_CHAT_DISALLOWED_TOOLS (opt out)"

: > "$DOCKER_LOG"
bin/docker-worker.sh chan --container dwc-custom --disallow "Foo Bar" >/dev/null 2>&1 || fail "docker-worker --disallow custom failed"
grep -q "AGENT_CHAT_DISALLOWED_TOOLS=Foo Bar" "$DOCKER_LOG" || fail "--disallow custom list not passed through"
echo "OK: default omits the env; --disallow '' opts out; --disallow 'list' overrides."

echo
echo "PASS: fleet spawn/teardown contract, isolation, refusals, and disallow plumbing."
