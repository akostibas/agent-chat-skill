// Package channel is the importable core of agent-chat: the on-disk channel
// wire format and the operations a peer needs to speak it.
//
// A channel is a directory on a shared filesystem containing an append-only
// JSONL log, an flock file (log.lock) that serializes writes across processes,
// and a presence/ subdir of heartbeat files. The bytes on disk are the real
// contract — this package and the agent-chat CLI both produce identical output,
// and any program that imports this package speaks the same format as a CLI
// peer sharing the same directory.
//
// # Concurrency
//
// Writes (Append, Leave, ReapStale) take an exclusive flock on log.lock, so a
// separate process writing the same log is coordinated by construction. Reads
// (Read, ReadSince) are lock-free against the append-only log.
//
// # Presence is the caller's job
//
// A peer is "present" only while its presence file stays fresh. There is no
// background daemon: the caller must drive TouchPresence on its own cadence
// (faster than AGENT_CHAT_STALE_SECS) and call ReapStale to retire vanished
// peers. A peer that stops touching its file is reaped as if it had left.
//
// # Stability
//
// This is a SemVer-governed surface. The exported API and Record's JSON output
// are supported; breaking either is a major version bump.
package channel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const defaultStaleSecs = 45
const defaultHeartbeatSecs = 15

// LeaveBodyTimedOut is the body ReapStale posts when it retires a peer whose
// heartbeat went stale. It is part of what peers observe on the wire, exported
// so a stream consumer can tell a reap-induced leave (which a sleep flap
// produces, and which should be debounced) apart from a clean, intentional
// leave. Keep ReapStale and any matcher pinned to this single constant so they
// never drift.
const LeaveBodyTimedOut = "left channel (timed out)"

// HeartbeatSecs returns the effective heartbeat interval in seconds, honoring
// AGENT_CHAT_HEARTBEAT_SECS (default defaultHeartbeatSecs). It is the single
// source of truth for the beat period, so a consumer sizing a window off the
// heartbeat (e.g. a flap-debounce hold) tracks the same env override instead of
// hardcoding a second constant.
func HeartbeatSecs() int { return envInt("AGENT_CHAT_HEARTBEAT_SECS", defaultHeartbeatSecs) }

// Record is a single JSONL entry in the channel log. Field names and omitempty
// rules are the published schema — they must stay byte-identical to what the
// CLI and the original shell scripts' jq schema write. See TestRecordJSONGolden.
type Record struct {
	Ts       string   `json:"ts"`
	Sender   string   `json:"sender"`
	Cwd      string   `json:"cwd,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	Kind     string   `json:"kind"`
	Body     string   `json:"body"`
	Mentions []string `json:"mentions,omitempty"`
	// Round tags a record with a feedback-poll round id. Set only on the
	// poll-open/poll-submit/poll-close kinds (see feedback.go); omitted (and thus
	// invisible in the golden schema) on ordinary join/leave/msg records.
	Round string `json:"round,omitempty"`
}

// Cursor marks a position in the append-only log. The zero value is the start
// of the log. It is an opaque byte offset; pass the Cursor returned by one
// ReadSince into the next.
//
// A long-lived consumer that must survive restarts can persist a Cursor with
// Offset and restore it with CursorAt, so it surfaces each record exactly once
// across process lifetimes.
type Cursor struct {
	off int64
}

// Offset returns the cursor's byte position in the log. Persist this value to
// resume reading later with CursorAt.
func (c Cursor) Offset() int64 { return c.off }

// CursorAt reconstructs a Cursor at a byte offset previously obtained from
// Offset — e.g. to resume after a restart. The offset need not be validated:
// ReadSince self-heals if it points past a shrunken log (the channel was
// deleted and recreated under the same slug), resetting to the start.
func CursorAt(off int64) Cursor { return Cursor{off: off} }

// Channel holds the root dir and slug for one agent-chat channel. Construct it
// with Open.
type Channel struct {
	root string
	slug string
}

// Open returns a Channel for slug under root. It does not create anything on
// disk — a read of a channel that was never joined still reports "no such
// channel". The first write (Append/Leave) creates the channel directory.
//
// root is passed in explicitly rather than read from the environment so the
// library stays testable and the location is visible at the call site; the CLI
// resolves AGENT_CHAT_ROOT and passes it here.
func Open(root, slug string) (*Channel, error) {
	if root == "" {
		return nil, fmt.Errorf("channel: empty root")
	}
	if slug == "" {
		return nil, fmt.Errorf("channel: empty slug")
	}
	return &Channel{root: root, slug: slug}, nil
}

// Slug returns the channel's slug.
func (c *Channel) Slug() string { return c.slug }

// Exists reports whether the channel has been created — i.e. its log file is
// present. A peer can use it to refuse to operate on a channel nobody joined.
func (c *Channel) Exists() bool {
	_, err := os.Stat(c.logPath())
	return err == nil
}

func (c *Channel) dir() string      { return filepath.Join(c.root, c.slug) }
func (c *Channel) logPath() string  { return filepath.Join(c.root, c.slug, "log") }
func (c *Channel) lockPath() string { return filepath.Join(c.root, c.slug, "log.lock") }
func (c *Channel) presDir() string  { return filepath.Join(c.root, c.slug, "presence") }
func (c *Channel) presFile(name string) string {
	return filepath.Join(c.root, c.slug, "presence", name)
}

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

// logEmpty reports whether the channel log has no records yet — the signal that
// a join is creating the channel. Call it under the channel lock so no
// concurrent write can change the answer. A zero-byte (or absent) log means no
// record has been appended.
func (c *Channel) logEmpty() (bool, error) {
	info, err := os.Stat(c.logPath())
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size() == 0, nil
}

// acquireLock grabs an exclusive flock on log.lock, retrying until the lock is
// held or ctx is done. Returns the open file; caller must call releaseLock.
func (c *Channel) acquireLock(ctx context.Context) (*os.File, error) {
	f, err := os.OpenFile(c.lockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK {
			_ = f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("acquire lock: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func releaseLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// appendLocked writes r as a JSON line to the log. The caller must already
// hold the lock.
func (c *Channel) appendLocked(r Record) (err error) {
	f, err := os.OpenFile(c.logPath(), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	// On a write, a Close error can mean the line wasn't flushed — surface it.
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// Append writes r to the channel log under an exclusive lock, creating the
// channel directory if needed. If r.Ts is empty it is stamped with the current
// UTC time. Blocks acquiring the lock until ctx is done.
func (c *Channel) Append(ctx context.Context, r Record) error {
	if err := c.ensureDir(); err != nil {
		return err
	}
	if r.Ts == "" {
		r.Ts = isoNow()
	}
	lockF, err := c.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer releaseLock(lockF)
	return c.appendLocked(r)
}

// Read returns all well-formed records in the channel log. It reports an error
// if the channel does not exist.
func (c *Channel) Read() ([]Record, error) {
	f, err := os.Open(c.logPath())
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("no such channel: %s", c.slug)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
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

// ReadSince returns records appended after cur, plus a Cursor to pass to the
// next call. A polling peer calls it in a loop: each call reads only what is
// new since the last cursor, so two records sharing a one-second timestamp are
// never dropped or double-read the way a timestamp cursor would.
//
// If the log is shorter than cur (the channel was deleted and recreated under
// the same slug — the only mutation the layout permits, since the sweep removes
// a whole channel dir rather than truncating a live log), the cursor resets to
// the start. A missing log yields no records and an unchanged cursor, so a peer
// may poll a channel before anyone has written to it.
//
// Only whole newline-terminated lines are consumed; a partial trailing line
// from a concurrent in-progress write is left for the next call.
func (c *Channel) ReadSince(ctx context.Context, cur Cursor) ([]Record, Cursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, cur, err
	}
	f, err := os.Open(c.logPath())
	if os.IsNotExist(err) {
		return nil, cur, nil
	}
	if err != nil {
		return nil, cur, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, cur, err
	}
	off := cur.off
	if info.Size() < off {
		off = 0 // log shrank: channel was recreated; start over.
	}
	if _, err := f.Seek(off, 0); err != nil {
		return nil, cur, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, cur, err
	}
	// Consume only up to the last complete line.
	nl := bytes.LastIndexByte(data, '\n')
	if nl < 0 {
		return nil, Cursor{off: off}, nil
	}
	complete := data[:nl+1]
	var records []Record
	for _, line := range bytes.Split(complete, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) == nil {
			records = append(records, r)
		}
	}
	return records, Cursor{off: off + int64(len(complete))}, nil
}

// End returns a Cursor at the current end of the log, so a peer can begin
// polling for messages that arrive from now on without replaying history. A
// missing log yields the start cursor.
func (c *Channel) End() (Cursor, error) {
	info, err := os.Stat(c.logPath())
	if os.IsNotExist(err) {
		return Cursor{}, nil
	}
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{off: info.Size()}, nil
}

func isoNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
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
