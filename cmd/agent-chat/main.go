package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "join":
		cmdJoin(os.Args[2:])
	case "send":
		cmdSend(os.Args[2:])
	case "history":
		cmdHistory(os.Args[2:])
	case "wait":
		cmdWait(os.Args[2:])
	case "hook":
		cmdHook(os.Args[2:])
	case "leave":
		cmdLeave(os.Args[2:])
	case "update":
		cmdUpdate(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "agent-chat: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: agent-chat <command> [args]

Commands:
  join    <slug> --as <name>
  send    <slug> --as <name>          (body on stdin)
  history <slug> [--since <iso8601>]
  wait    <slug> <name>               (idle doorbell: block, hold presence, exit on traffic)
  hook [install]                      (Claude Code hook: deliver new messages / register in settings)
  leave   <slug> --as <name>          (depart deliberately: post the leave, stop deliveries)
  update  [--yes]                     (upgrade the installed skill to the latest release)`)
}

// selfDir returns the directory containing this binary, used to locate shim
// scripts next to it. Falls back to empty string on error.
func selfDir() string {
	if d := os.Getenv("AGENT_CHAT_SKILL_DIR"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	return filepath.Dir(resolved)
}
