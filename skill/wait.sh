#!/usr/bin/env bash
# Monitor-free subscription: blocks until a peer message arrives, prints it,
# exits. Re-run in the background after every exit. See SKILL.md.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/agent-chat" wait "$@"
