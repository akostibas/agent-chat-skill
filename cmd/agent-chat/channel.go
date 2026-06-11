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
	"syscall"
	"time"
)

const (
	defaultHeartbeatSecs = 15
	defaultStaleSecs     = 45
	defaultTTLDays       = 14
)

// Record is a single JSONL entry in the channel log. Field names and omitempty
// rules must stay in sync with the shell scripts' jq schema.
type Record struct {
	Ts       string   `json:"ts"`
	Sender   string   `json:"sender"`
	Cwd      string   `json:"cwd,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	Kind     string   `json:"kind"`
	Body     string   `json:"body"`
	Mentions []string `json:"mentions,omitempty"`
}

// Channel holds the slug and root dir for one agent-chat channel.
type Channel struct {
	root string
	slug string
}

func newChannel(slug string) *Channel {
	root := os.Getenv("AGENT_CHAT_ROOT")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".claude", "agent-chat")
	}
	return &Channel{root: root, slug: slug}
}

func (c *Channel) dir() string                 { return filepath.Join(c.root, c.slug) }
func (c *Channel) logPath() string             { return filepath.Join(c.root, c.slug, "log") }
func (c *Channel) lockPath() string            { return filepath.Join(c.root, c.slug, "log.lock") }
func (c *Channel) presDir() string             { return filepath.Join(c.root, c.slug, "presence") }
func (c *Channel) presFile(name string) string { return filepath.Join(c.root, c.slug, "presence", name) }

func (c *Channel) ensureDir() error {
	if err := os.MkdirAll(c.dir(), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(c.logPath(), os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	return f.Close()
}

// acquireLock grabs an exclusive flock on log.lock, retrying until timeout.
// Returns the open file; caller must call releaseLock.
func (c *Channel) acquireLock(timeout time.Duration) (*os.File, error) {
	f, err := os.OpenFile(c.lockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("could not acquire lock within %v", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func releaseLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}

// appendRecord appends r as a JSON line to the log. Must be called under lock.
func (c *Channel) appendRecord(r Record) error {
	f, err := os.OpenFile(c.logPath(), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// readLog reads all well-formed records from the channel log.
func (c *Channel) readLog() ([]Record, error) {
	f, err := os.Open(c.logPath())
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("no such channel: %s", c.slug)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var records []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) == nil {
			records = append(records, r)
		}
	}
	return records, sc.Err()
}

// members returns names of all agents with a presence file.
func (c *Channel) members() []string {
	entries, err := os.ReadDir(c.presDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// touchPresence refreshes this agent's heartbeat file.
func (c *Channel) touchPresence(name string) {
	_ = os.MkdirAll(c.presDir(), 0755)
	pf := c.presFile(name)
	now := time.Now()
	if os.Chtimes(pf, now, now) != nil {
		// File doesn't exist yet — create it.
		if f, err := os.OpenFile(pf, os.O_CREATE|os.O_RDWR, 0644); err == nil {
			f.Close()
		}
	}
}

// removePresence removes this agent's presence file.
func (c *Channel) removePresence(name string) {
	_ = os.Remove(c.presFile(name))
}

// reapStalePeers emits a leave on behalf of any peer whose heartbeat has
// expired. The reap is claimed under lock by removing the presence file first,
// so concurrent reapers can't double-post.
func (c *Channel) reapStalePeers(me string) {
	staleSecs := envInt("AGENT_CHAT_STALE_SECS", defaultStaleSecs)
	entries, err := os.ReadDir(c.presDir())
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		name := e.Name()
		if name == me {
			continue
		}
		pf := c.presFile(name)
		info, err := os.Stat(pf)
		if err != nil || now.Sub(info.ModTime()) <= time.Duration(staleSecs)*time.Second {
			continue
		}
		// Claim the reap under lock.
		lockF, err := c.acquireLock(2 * time.Second)
		if err != nil {
			continue
		}
		func() {
			defer releaseLock(lockF)
			info2, err := os.Stat(pf)
			if err != nil {
				return // already reaped
			}
			if now.Sub(info2.ModTime()) <= time.Duration(staleSecs)*time.Second {
				return // peer refreshed
			}
			if os.Remove(pf) != nil {
				return // another reaper claimed it
			}
			_ = c.appendRecord(Record{
				Ts:     isoNow(),
				Sender: name,
				Kind:   "leave",
				Body:   "left channel (timed out)",
			})
		}()
	}
}

// emitLeaveEvent posts a leave record under lock. Best-effort: errors ignored.
func (c *Channel) emitLeaveEvent(name, body string) {
	lockF, err := c.acquireLock(2 * time.Second)
	if err != nil {
		return
	}
	defer releaseLock(lockF)
	_ = c.appendRecord(Record{
		Ts:     isoNow(),
		Sender: name,
		Kind:   "leave",
		Body:   body,
	})
}

// extractMentions finds @token mentions in body using the same boundary rules
// as the shell scripts: not preceded or followed by [a-zA-Z0-9_-].
func extractMentions(body string) []string {
	var mentions []string
	seen := map[string]bool{}
	b := []byte(body)
	for i := 0; i < len(b); i++ {
		if b[i] != '@' {
			continue
		}
		if i > 0 && isIdentByte(b[i-1]) {
			continue
		}
		j := i + 1
		for j < len(b) && isIdentByte(b[j]) {
			j++
		}
		token := string(b[i+1 : j])
		if len(token) == 0 || len(token) > 40 {
			continue
		}
		if j < len(b) && isIdentByte(b[j]) {
			continue
		}
		if !seen[token] {
			seen[token] = true
			mentions = append(mentions, token)
		}
	}
	return mentions
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_' || c == '-'
}

// filterMentions retains only mention tokens that name a present channel member.
func filterMentions(mentions, members []string) []string {
	mset := make(map[string]bool, len(members))
	for _, m := range members {
		mset[m] = true
	}
	var out []string
	for _, m := range mentions {
		if mset[m] {
			out = append(out, m)
		}
	}
	return out
}

func isoNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
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

	repo := os.Getenv("AGENT_CHAT_REPO")
	if repo == "" {
		repo = "akostibas/agent-chat-skill"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/" + repo + "/releases/latest") //nolint:noctx
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var result struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return
	}
	if result.TagName != "" && result.TagName != current {
		fmt.Fprintf(os.Stderr, "agent-chat: a newer release is available (%s → %s). To upgrade: bash %s/update.sh\n",
			current, result.TagName, skillDir)
	}
}
