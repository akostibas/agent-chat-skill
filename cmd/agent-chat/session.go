package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// The session registry maps a Claude Code session to its channel memberships,
// so the delivery hook (see hook.go) can answer "is this session subscribed to
// anything?" with a single stat. One file per session under <root>/sessions/,
// JSONL, one membership per line. join writes it; the hook prunes dead
// channels from it and bumps its mtime on every fire (the mtime doubles as the
// hook's wake-gap clock); a clean leave or SessionEnd removes the entry.
//
// The sessions/ dir lives under the channel root on purpose: it shares the
// root's lifecycle (an ephemeral fleet root takes its registry with it) and
// sweepOldChannels skips it naturally (it has no log file).

// sessionIDRE bounds what we'll embed in a filesystem path. Claude Code
// session ids are UUIDs; anything outside this alphabet is rejected outright.
var sessionIDRE = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,80}$`)

type membership struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// currentSessionID returns the session id Claude Code exports to Bash
// children, or "" when absent or unsafe to use as a filename.
func currentSessionID() string {
	sid := os.Getenv("CLAUDE_CODE_SESSION_ID")
	if !sessionIDRE.MatchString(sid) {
		return ""
	}
	return sid
}

func sessionFile(root, sid string) string {
	return filepath.Join(root, "sessions", sid)
}

func readMemberships(path string) []membership {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []membership
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var m membership
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Slug != "" && m.Name != "" {
			out = append(out, m)
		}
	}
	return out
}

func writeMemberships(path string, ms []membership) error {
	if len(ms) == 0 {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, m := range ms {
		_ = enc.Encode(m)
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

// registerSession records that this session is <name> on <slug>. A re-join of
// the same slug replaces the old entry (the name may have changed). No-op
// without a session id — a peer outside Claude Code just has no registry.
func registerSession(root, slug, name string) {
	sid := currentSessionID()
	if sid == "" {
		return
	}
	path := sessionFile(root, sid)
	ms := readMemberships(path)
	kept := ms[:0]
	for _, m := range ms {
		if m.Slug != slug {
			kept = append(kept, m)
		}
	}
	_ = writeMemberships(path, append(kept, membership{Slug: slug, Name: name}))
}

// deregisterSession drops this session's membership of slug (e.g. on a clean
// leave), removing the file when it was the last one.
func deregisterSession(root, slug string) {
	sid := currentSessionID()
	if sid == "" {
		return
	}
	path := sessionFile(root, sid)
	ms := readMemberships(path)
	kept := ms[:0]
	for _, m := range ms {
		if m.Slug != slug {
			kept = append(kept, m)
		}
	}
	if len(kept) != len(ms) {
		_ = writeMemberships(path, kept)
	}
}
