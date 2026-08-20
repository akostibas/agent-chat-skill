package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const skillName = "agent-chat"

// updateRepo returns the GitHub repo to update from, honoring the same
// AGENT_CHAT_REPO override the daily nudge check uses.
func updateRepo() string {
	if repo := os.Getenv("AGENT_CHAT_REPO"); repo != "" {
		return repo
	}
	return "akostibas/agent-chat-skill"
}

// latestReleaseTag fetches the latest release tag for repo from the GitHub API.
func latestReleaseTag(repo string, timeout time.Duration) (string, error) {
	api := os.Getenv("AGENT_CHAT_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, api+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach GitHub (offline or rate-limited): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var result struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.TagName == "" {
		return "", fmt.Errorf("could not determine the latest release tag")
	}
	return result.TagName, nil
}

// cmdUpdate upgrades the installed skill to the latest GitHub release: clone
// the tag into a temp dir, `make install SKILL_DIR=<here>`, remove the
// checkout. `make install` rm -rf's the skill dir first, so the running
// binary's inode is unlinked rather than truncated — POSIX keeps it readable
// for this process and any live streams, making self-replacement safe.
func cmdUpdate(args []string) {
	fs := newFlagSet("update", "[--yes]")
	yes := fs.Bool("yes", false, "upgrade without confirming")
	fs.BoolVar(yes, "y", false, "shorthand for --yes")
	wantPositional(fs, parse(fs, args), 0)
	assumeYes := *yes

	for _, t := range []struct{ name, hint string }{
		{"git", "install git and retry"},
		{"go", "install Go (https://go.dev/dl/) and retry"},
		{"make", "install make and retry"},
	} {
		if _, err := exec.LookPath(t.name); err != nil {
			dieUpdate("%s not found — %s", t.name, t.hint)
		}
	}

	skillDir := selfDir()
	if skillDir == "" {
		dieUpdate("cannot resolve the skill install directory")
	}

	current := ""
	if b, err := os.ReadFile(filepath.Join(skillDir, "VERSION")); err == nil {
		current = strings.TrimSpace(string(b))
	}
	shown := current
	if shown == "" {
		shown = "unknown"
	}

	latest, err := latestReleaseTag(updateRepo(), 10*time.Second)
	if err != nil {
		dieUpdate("%v", err)
	}

	fmt.Printf("Skill dir: %s\n", skillDir)
	fmt.Printf("Current:   %s\n", shown)
	fmt.Printf("Latest:    %s\n", latest)

	if current != "" && current == latest {
		fmt.Println("Already up to date.")
		return
	}

	if !assumeYes {
		// ModeCharDevice is an imperfect TTY test (/dev/null matches too), so
		// a read error/EOF falls back to the non-interactive hint.
		if stdinIsTTY() {
			fmt.Printf("Upgrade %s to %s? [y/N] ", skillName, latest)
			reply, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if r := strings.TrimSpace(reply); err == nil && (r == "y" || r == "Y") {
				// confirmed; fall through to the upgrade
			} else if err != nil {
				fmt.Println()
				fmt.Println("Re-run with --yes to upgrade (reinstalls the skill to the location above).")
				return
			} else {
				fmt.Println("Aborted.")
				return
			}
		} else {
			fmt.Println()
			fmt.Println("Re-run with --yes to upgrade (reinstalls the skill to the location above).")
			return
		}
	}

	tmp, err := os.MkdirTemp("", skillName+"-update.")
	if err != nil {
		dieUpdate("%v", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	fmt.Printf("Cloning %s into a temp checkout...\n", latest)
	clone := exec.Command("git", "clone", "--quiet", "--depth", "1", "--branch", latest,
		"https://github.com/"+updateRepo()+".git", tmp)
	clone.Stdout, clone.Stderr = os.Stdout, os.Stderr
	if err := clone.Run(); err != nil {
		dieUpdate("git clone of %s failed", latest)
	}

	fmt.Printf("Installing (make install SKILL_DIR=%s)...\n", skillDir)
	install := exec.Command("make", "-C", tmp, "install", "SKILL_DIR="+skillDir)
	install.Stdout, install.Stderr = os.Stdout, os.Stderr
	if err := install.Run(); err != nil {
		dieUpdate("make install failed")
	}

	if current == "" {
		current = "?"
	}
	fmt.Printf("Upgraded %s %s → %s.\n", skillName, current, latest)
}

func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func dieUpdate(format string, a ...any) {
	fmt.Fprintf(os.Stderr, skillName+" update: "+format+"\n", a...)
	os.Exit(1)
}
