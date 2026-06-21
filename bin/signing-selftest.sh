#!/usr/bin/env bash
# signing-selftest.sh — verify the entrypoint's SSH commit-signing setup.
#
# Sources the REAL signing helpers out of docker/entrypoint.sh (so this can't
# drift from what ships) and exercises the bring-your-own-key path end to end:
# stage a provided key, configure git, make a commit, and verify the signature.
# Also asserts the "no signing" default leaves git untouched.
#
# Runs anywhere git + ssh-keygen exist — on the host for fast feedback, and
# INSIDE the built worker image (see bin/docker-worker-test.sh wiring) where it
# additionally proves openssh-client/ssh-keygen are present in the runtime.
#
# Exit: 0 = pass, 1 = setup/precondition failure, 2 = behavior mismatch.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENTRYPOINT="$repo_root/docker/entrypoint.sh"
[[ -f "$ENTRYPOINT" ]] || { echo "FAIL: $ENTRYPOINT not found" >&2; exit 1; }
command -v ssh-keygen >/dev/null || { echo "FAIL: ssh-keygen missing (need openssh-client)" >&2; exit 1; }
command -v git        >/dev/null || { echo "FAIL: git missing" >&2; exit 1; }

WORK="$(mktemp -d -t signing-selftest.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
export HOME="$WORK/home"          # so `git config --global` lands in our sandbox
mkdir -p "$HOME"

# Stand-ins the sourced helpers expect.
log() { printf '  %s\n' "$*"; }
SIGN_KEY="$HOME/.ssh/agent-chat-signing"
SIGN_TITLE="signing-selftest"   # consumed by the sourced helpers, not this file
export SIGN_KEY SIGN_TITLE
git config --global user.name  "Self Test"
git config --global user.email "selftest@example.com"

# Pull the three signing functions out of the entrypoint verbatim and source
# them. Boundaries are stable string markers; if the entrypoint is restructured
# this fails loudly, which is the point.
awk '/^if \[\[ -n "\$\{GIT_SIGNING_KEY_FILE/{exit} /^configure_ssh_signing\(\) \{/{f=1} f{print}' \
  "$ENTRYPOINT" > "$WORK/signing-funcs.sh"
grep -q 'configure_ssh_signing' "$WORK/signing-funcs.sh" || { echo "FAIL: could not extract signing funcs" >&2; exit 1; }
# shellcheck source=/dev/null
source "$WORK/signing-funcs.sh"

fails=0
# Run "$@" as a command; pass on exit 0, else record a failure.
check() { local d="$1"; shift; if "$@"; then printf 'ok   %s\n' "$d"; else printf 'FAIL %s\n' "$d" >&2; fails=$((fails+1)); fi; }
has_ssh_sig()   { git -C "$repo" cat-file commit HEAD | grep -q "BEGIN SSH SIGNATURE"; }
gpgsign_unset() { [[ -z "$(git config --global commit.gpgsign || true)" ]]; }

echo "=== BYOK: provided key -> verifiable signed commit ==="
# A "provided" key, as a deploy would mount in.
ssh-keygen -t ed25519 -N '' -C provided -f "$WORK/provided" >/dev/null
# Replicate the entrypoint's BYOK staging, then call its real configure fn.
install -d -m 700 "$HOME/.ssh"
install -m 600 "$WORK/provided" "$SIGN_KEY"
ssh-keygen -y -f "$SIGN_KEY" > "$SIGN_KEY.pub"
configure_ssh_signing

check "gpg.format is ssh"      test "$(git config --global gpg.format)"      = ssh
check "commit.gpgsign is true" test "$(git config --global commit.gpgsign)"  = true
check "user.signingkey is set" test "$(git config --global user.signingkey)" = "$SIGN_KEY"

repo="$WORK/repo"; mkdir -p "$repo"; ( cd "$repo" && git init -q && git commit -q --allow-empty -m "signed" )
check "commit carries an SSH signature" has_ssh_sig

# Full verification: trust our own key for this committer, then verify-commit.
printf '%s namespaces="git" %s\n' "selftest@example.com" "$(cat "$SIGN_KEY.pub")" > "$WORK/allowed_signers"
git config --global gpg.ssh.allowedSignersFile "$WORK/allowed_signers"
check "git verify-commit passes" git -C "$repo" verify-commit HEAD

echo "=== default: no key, signing stays off ==="
# Fresh sandbox HOME; with neither env var the entrypoint never configures
# signing, so a clean global config must lack commit.gpgsign.
export HOME="$WORK/home2"; mkdir -p "$HOME"
git config --global user.email "selftest@example.com"
check "commit.gpgsign unset by default" gpgsign_unset

if (( fails )); then echo; echo "RESULT: $fails check(s) failed"; exit 2; fi
echo; echo "RESULT: all checks passed"
