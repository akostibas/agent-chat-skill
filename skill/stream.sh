#!/usr/bin/env bash
# COMPAT: remove after 2026-09-01 — thin shim delegating to agent-chat binary.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/agent-chat" stream "$@"
