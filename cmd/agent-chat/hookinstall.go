package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// hookEvents are the Claude Code hook events the delivery hook registers for:
// PostToolUse is the mid-task delivery path (the point of issue #59),
// UserPromptSubmit catches a session up at turn start after sitting idle, and
// SessionEnd gives a hook-subscribed session the clean leave a stream posts on
// signal.
var hookEvents = []string{"PostToolUse", "UserPromptSubmit", "SessionEnd"}

// hookCmdMarker identifies our entry inside a settings hooks array, both for
// idempotent re-install and for join's "is the hook installed?" probe. It is
// part of the generated command below — change them together.
const hookCmdMarker = `exec "$BIN" hook`

// cmdHookInstall merges the delivery hook into Claude Code settings
// (~/.claude/settings.json by default; --settings overrides, mainly for
// tests). Idempotent: an existing agent-chat entry has its command updated in
// place (the binary may have moved); other hooks and unknown settings keys are
// preserved. The generated command guards on the binary being executable, so
// an uninstalled or half-removed skill degrades to a silent no-op instead of a
// console error on every tool call.
func cmdHookInstall(args []string) {
	fs := newFlagSet("hook install", "[--settings <path>]")
	settingsPath := fs.String("settings", "", "settings file to merge into (default ~/.claude/settings.json)")
	wantPositional(fs, parse(fs, args), 0)
	path := *settingsPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-chat: hook install: %v\n", err)
			os.Exit(1)
		}
		path = filepath.Join(home, ".claude", "settings.json")
	}

	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			fmt.Fprintf(os.Stderr, "agent-chat: hook install: %s is not valid JSON (%v) — fix it or remove it, then re-run\n", path, err)
			os.Exit(1)
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "agent-chat: hook install: %v\n", err)
		os.Exit(1)
	}

	cmd, err := hookCommand()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: hook install: %v\n", err)
		os.Exit(1)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	changed := false
	for _, event := range hookEvents {
		if ensureHookEntry(hooks, event, cmd) {
			changed = true
		}
	}
	settings["hooks"] = hooks

	if !changed {
		fmt.Printf("agent-chat hook already registered in %s\n", path)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: hook install: %v\n", err)
		os.Exit(1)
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: hook install: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: hook install: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Registered agent-chat delivery hook in %s (%s)\n", path, strings.Join(hookEvents, ", "))
	fmt.Printf("Messages on joined channels now arrive automatically; restart running sessions to pick it up.\n")
}

// hookCommand builds the sh command registered in settings. The binary path is
// the installed location of THIS binary, with $HOME left symbolic so the
// settings file survives a home-directory move and stays dotfiles-portable.
func hookCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	display := exe
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, exe); err == nil && !strings.HasPrefix(rel, "..") {
			display = "$HOME/" + filepath.ToSlash(rel)
		}
	}
	return fmt.Sprintf(`BIN="%s"; [ -x "$BIN" ] || exit 0; %s`, display, hookCmdMarker), nil
}

// ensureHookEntry makes hooks[event] contain exactly one agent-chat command
// entry, updating it in place when present. Returns whether it wrote anything.
// The settings shape is hooks.<event> = [{matcher?, hooks: [{type, command}]}].
func ensureHookEntry(hooks map[string]any, event, cmd string) bool {
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			entry, _ := h.(map[string]any)
			if s, _ := entry["command"].(string); strings.Contains(s, hookCmdMarker) {
				if s == cmd {
					return false
				}
				entry["command"] = cmd
				return true
			}
		}
	}
	hooks[event] = append(groups, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 10}},
	})
	return true
}

// hookInstalled reports whether the delivery hook is registered anywhere this
// session's harness would load it: user settings, or the project's checked-in /
// local settings (the project root is where Claude Code resolves them — the
// live test in #59 was a project-level install, which the first cut missed).
// join uses it to decide whether to print the "you're already subscribed"
// story or the Monitor/wait instructions.
func hookInstalled() bool {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".claude", "settings.json"))
	}
	if root := agentCwd(); root != "" {
		paths = append(paths,
			filepath.Join(root, ".claude", "settings.json"),
			filepath.Join(root, ".claude", "settings.local.json"))
	}
	return slices.ContainsFunc(paths, settingsHasHook)
}

// settingsHasHook reports whether the settings file at path registers our
// PostToolUse delivery command. It must parse rather than grep: the marker
// contains quotes, which the JSON on disk escapes.
func settingsHasHook(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var settings map[string]any
	if json.Unmarshal(data, &settings) != nil {
		return false
	}
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["PostToolUse"].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			entry, _ := h.(map[string]any)
			if s, _ := entry["command"].(string); strings.Contains(s, hookCmdMarker) {
				return true
			}
		}
	}
	return false
}
