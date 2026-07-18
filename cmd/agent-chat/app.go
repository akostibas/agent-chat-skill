package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

const (
	defaultTTLDays = 14
)

// channelRoot resolves the channel root directory, honoring AGENT_CHAT_ROOT and
// falling back to ~/.claude/agent-chat. The library takes the root explicitly;
// reading the environment is the CLI's job.
func channelRoot() string {
	if root := os.Getenv("AGENT_CHAT_ROOT"); root != "" {
		return root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "agent-chat")
}

// openChannel opens the named channel under the resolved root, exiting with a
// diagnostic on error.
func openChannel(slug string) *channel.Channel {
	c, err := channel.Open(channelRoot(), slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}
	return c
}

func agentCwd() string {
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	cwd, _ := os.Getwd()
	return cwd
}

func agentBranch() string {
	out, err := exec.Command("git", "symbolic-ref", "--short", "-q", "HEAD").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscan(v, &n); err == nil && n > 0 {
		return n
	}
	return def
}

// envFloat reads a non-negative float from the environment, falling back to def
// when unset, unparseable, or negative. Zero is a valid value (it is not the
// same as unset) so a rate of "0" can explicitly disable a feature.
func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return def
	}
	return f
}

// sweepOldChannels removes channel dirs whose log is older than TTL_DAYS.
func sweepOldChannels(root string) {
	ttl := envInt("AGENT_CHAT_TTL_DAYS", defaultTTLDays)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -ttl)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		logPath := filepath.Join(root, e.Name(), "log")
		if info, err := os.Stat(logPath); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// checkForUpdate does a throttled, best-effort upstream version check. Errors
// are swallowed — this must never break join/send/stream.
func checkForUpdate(skillDir string) {
	if os.Getenv("AGENT_CHAT_NO_UPDATE_CHECK") != "" {
		return
	}
	vb, err := os.ReadFile(filepath.Join(skillDir, "VERSION"))
	if err != nil {
		return
	}
	current := strings.TrimSpace(string(vb))
	if current == "" {
		return
	}
	ttl := time.Duration(envInt("AGENT_CHAT_UPDATE_TTL_SECS", 86400)) * time.Second
	stamp := filepath.Join(os.TempDir(), "agent-chat-update-check")
	if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < ttl {
		return
	}
	_ = os.WriteFile(stamp, nil, 0600)

	latest, err := latestReleaseTag(updateRepo(), 2*time.Second)
	if err != nil {
		return
	}
	if latest != current {
		fmt.Fprintf(os.Stderr, "agent-chat: a newer release is available (%s → %s). To upgrade: bash %s/update.sh\n",
			current, latest, skillDir)
	}
}
