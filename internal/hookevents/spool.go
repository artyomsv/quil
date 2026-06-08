package hookevents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/artyomsv/quil/internal/logger"
)

// Spool is a per-pane JSONL file reader. The daemon polls Tick on a 200 ms
// ticker; each call reads any new bytes appended since the previous read
// from every <paneID>.jsonl file under the spool directory, parses one
// Payload per complete (newline-terminated) line, and returns them in
// arrival order across all files.
//
// Partial trailing lines (write in flight at the time of read) are NOT
// consumed — Tick remembers the offset of the last complete \n and resumes
// from there next call. This is the defense against the documented race
// between O_APPEND hook writes and the daemon's stat-then-read.
//
// On daemon shutdown, the spool files persist on disk; on next daemon
// start, Init truncates them so we do not replay stale events from a
// previous session (notifications are inherently ephemeral).
type Spool struct {
	dir string

	mu      sync.Mutex
	offsets map[string]int64 // paneID → byte offset already consumed
}

// NewSpool returns a Spool reading from dir. Use Init to truncate stale
// files on daemon startup; Tick on each poll; Cleanup on pane destroy.
func NewSpool(dir string) *Spool {
	return &Spool{
		dir:     dir,
		offsets: make(map[string]int64),
	}
}

// Init prepares the spool directory: creates it if absent, truncates every
// existing *.jsonl file to size 0 so a fresh daemon never replays events
// from a previous run. Safe to call multiple times.
//
// Truncate-on-start trades off durability for predictability: a hook that
// fired between daemon-stop and daemon-start would be lost, but the
// alternative — replaying potentially-stale events that no longer
// represent live state — is worse for a notification surface.
func (s *Spool) Init() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := filepath.Join(s.dir, name)
		if err := os.Truncate(path, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warn("hookevents: truncate spool %q: %v", path, err)
		}
	}
	s.mu.Lock()
	s.offsets = make(map[string]int64)
	s.mu.Unlock()
	return nil
}

// Tick scans the spool directory for new bytes appended since the last
// call, parses every complete line as a Payload, and returns the
// successfully-decoded payloads in arrival order per file (across files
// the order follows directory enumeration, which is not guaranteed —
// downstream coalescing keys by (paneID, hook_event) so ordering across
// panes does not affect correctness).
//
// Decoded but invalid payloads (failed Validate) are dropped with a warn
// log; the spool offset advances past them so they don't get re-parsed.
func (s *Spool) Tick() []Payload {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("hookevents: read spool dir %q: %v", s.dir, err)
		}
		return nil
	}

	var out []Payload
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		paneID := strings.TrimSuffix(name, ".jsonl")
		payloads := s.readPaneFile(paneID, filepath.Join(s.dir, name))
		out = append(out, payloads...)
	}
	return out
}

func (s *Spool) readPaneFile(paneID, path string) []Payload {
	s.mu.Lock()
	off := s.offsets[paneID]
	s.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("hookevents: open spool %q: %v", path, err)
		}
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		logger.Warn("hookevents: stat spool %q: %v", path, err)
		return nil
	}
	size := info.Size()
	if size == off {
		return nil // nothing new
	}
	if size < off {
		// File was truncated externally (e.g. test harness or a future
		// rotation). Restart from the beginning.
		off = 0
	}

	if _, err := f.Seek(off, io.SeekStart); err != nil {
		logger.Warn("hookevents: seek spool %q: %v", path, err)
		return nil
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		logger.Warn("hookevents: read spool %q: %v", path, err)
		return nil
	}

	// Find the last complete line (ending in \n). Everything past that is a
	// partial trailing write that we must not consume; it will be picked up
	// on the next Tick.
	lastNL := bytes.LastIndexByte(buf, '\n')
	if lastNL < 0 {
		return nil // no complete line yet
	}
	consumed := off + int64(lastNL) + 1
	s.mu.Lock()
	s.offsets[paneID] = consumed
	s.mu.Unlock()

	complete := buf[:lastNL+1]
	return parsePayloads(complete)
}

// parsePayloads decodes a buffer of newline-delimited JSON lines, dropping
// malformed lines with a warn log and returning the valid ones.
func parsePayloads(buf []byte) []Payload {
	var out []Payload
	for _, line := range bytes.Split(buf, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > MaxTotalBytes {
			logger.Warn("hookevents: payload exceeds %d-byte cap (%d bytes), dropping", MaxTotalBytes, len(line))
			continue
		}
		var p Payload
		if err := json.Unmarshal(line, &p); err != nil {
			logger.Warn("hookevents: parse payload: %v", err)
			continue
		}
		if err := p.Validate(); err != nil {
			logger.Warn("hookevents: invalid payload from pane=%s src=%s hook_event=%s: %v",
				p.PaneID, p.Source, p.HookEvent, err)
			continue
		}
		out = append(out, p)
	}
	return out
}

// Cleanup removes the spool file for a destroyed pane and forgets its
// offset. Idempotent; safe to call for panes that never had a spool file.
func (s *Spool) Cleanup(paneID string) {
	s.mu.Lock()
	delete(s.offsets, paneID)
	s.mu.Unlock()

	path := filepath.Join(s.dir, paneID+".jsonl")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("hookevents: cleanup spool %q: %v", path, err)
	}
}
