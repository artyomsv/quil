package hookevents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/artyomsv/quil/internal/logger"
)

// utf8BOM is the UTF-8 byte-order mark. Windows PowerShell 5.1's
// `Add-Content -Encoding UTF8` (the claude hook producer on Windows) prepends
// it when it CREATES a spool file, so the first JSONL line per pane — always
// the start edge (UserPromptSubmit) — would carry it. Go's encoding/json does
// not skip a leading BOM, so the reader strips it (see parseAndValidate).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// rotationThreshold is the per-pane spool size at which we truncate after a
// fully-drained read. The watcher only ever advances; without rotation a
// long-running pane's spool file grows linearly with hook-event count and
// can hit hundreds of MB over a multi-hour Claude session. 16 MiB is much
// larger than any realistic per-pane backlog and stays well clear of
// filesystem inode size guards.
const rotationThreshold = 16 * 1024 * 1024

// parseWarnSampleRate controls how often a per-pane producer error gets
// logged at WARN. A misbehaving producer (malformed lines in a loop) would
// otherwise spam quild.log at 200 ms ticks; sampling at 1 in N keeps the
// diagnostic visible without drowning the rest of the log.
const parseWarnSampleRate = 50

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

	mu             sync.Mutex
	offsets        map[string]int64 // paneID → byte offset already consumed
	parseErrCounts map[string]uint64 // paneID → malformed-line counter for log sampling
}

// NewSpool returns a Spool reading from dir. Use Init to truncate stale
// files on daemon startup; Tick on each poll; Cleanup on pane destroy.
func NewSpool(dir string) *Spool {
	return &Spool{
		dir:            dir,
		offsets:        make(map[string]int64),
		parseErrCounts: make(map[string]uint64),
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
		return fmt.Errorf("hookevents: create spool dir %q: %w", s.dir, err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("hookevents: read spool dir %q: %w", s.dir, err)
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
	s.parseErrCounts = make(map[string]uint64)
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
	// Reject unsafe paneIDs at the read path too — symmetric with the
	// Cleanup guard. A filename like "../evil.jsonl" in the spool dir
	// would otherwise drive arbitrary file reads via os.Open.
	if !safePaneID(paneID) {
		logger.Warn("hookevents: rejected read for unsafe filename-derived paneID %q", paneID)
		return nil
	}

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
		// Nothing new. Take this opportunity to rotate the file if it has
		// grown beyond the threshold and we have nothing in flight. Doing
		// it on an idle tick keeps the truncate off the hot read path.
		if size >= rotationThreshold {
			s.rotate(paneID, path)
		}
		return nil
	}
	if size < off {
		// File was truncated externally (test harness, prior rotation,
		// disk-full recovery). Restart from the beginning.
		off = 0
	}

	if _, err := f.Seek(off, 0); err != nil {
		logger.Warn("hookevents: seek spool %q: %v", path, err)
		return nil
	}

	// bufio.Reader.ReadBytes('\n') lets us distinguish complete lines
	// (returned with the trailing \n) from a partial trailing line
	// (returned WITHOUT \n at io.EOF). The partial trailing line MUST NOT
	// advance the offset — it'll be picked up on the next tick once the
	// producer's pending write finishes.
	//
	// Per-line size cap: ReadBytes will happily allocate an unbounded
	// buffer if the producer writes a multi-MB single line. We guard by
	// checking the returned slice's length and dropping anything over
	// MaxTotalBytes+1 with a warn. The advance still applies so we don't
	// spin on it.
	br := bufio.NewReader(f)
	var out []Payload
	consumed := off
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			// Complete line — advance offset past it regardless of
			// whether validation accepts it.
			consumed += int64(len(line))
			trimmed := line[:len(line)-1]
			if len(trimmed) == 0 || isWhitespaceLine(trimmed) {
				if err != nil {
					break
				}
				continue
			}
			if p, ok := s.parseAndValidate(paneID, trimmed); ok {
				out = append(out, p)
			}
		}
		if err != nil {
			// io.EOF with len(line) > 0 means we hit a partial trailing
			// line — leave the offset short of it so the next tick
			// picks it up. io.EOF with len(line) == 0 means we read the
			// last complete line above; offset already advanced.
			break
		}
	}

	s.mu.Lock()
	s.offsets[paneID] = consumed
	s.mu.Unlock()
	return out
}

// parseAndValidate decodes one JSONL line, validates it, and enforces the
// filename↔Payload paneID match that closes the cross-pane spoof. Returns
// (payload, true) when the line passes all checks; otherwise drops the
// line with a rate-limited warn.
func (s *Spool) parseAndValidate(filenamePaneID string, line []byte) (Payload, bool) {
	if len(line) > MaxTotalBytes {
		s.sampledParseWarn(filenamePaneID, fmt.Sprintf("payload exceeds %d-byte cap (%d bytes)", MaxTotalBytes, len(line)))
		return Payload{}, false
	}
	// Strip a leading UTF-8 BOM before decoding. A BOM-writing producer (the
	// Windows PowerShell claude hook — see utf8BOM) would otherwise make the
	// first line per pane fail json.Unmarshal and be silently dropped, losing
	// the start edge that drives the work-in-progress indicator.
	line = bytes.TrimPrefix(line, utf8BOM)
	var p Payload
	if err := json.Unmarshal(line, &p); err != nil {
		// Log only the byte size — never the err.Error() which may include
		// fragments of the raw line. Producer content can carry user
		// prompt previews or secrets.
		s.sampledParseWarn(filenamePaneID, fmt.Sprintf("unmarshal failed (line len %d)", len(line)))
		return Payload{}, false
	}
	if err := p.Validate(); err != nil {
		s.sampledParseWarn(filenamePaneID, fmt.Sprintf("invalid payload (hook_event=%q src=%q): %v", p.HookEvent, p.Source, err))
		return Payload{}, false
	}
	// Cross-pane spoofing defense: refuse to accept a payload that
	// claims to belong to a different pane than the file it was written
	// to. Without this a plugin running in pane A could forge events
	// attributed to pane B (e.g. "Permission required" cards aimed at a
	// pane the user is not currently looking at). The hook scripts set
	// pane_id from $QUIL_PANE_ID which the daemon controls — a mismatch
	// indicates either a bug or an attempt at attribution forgery.
	if p.PaneID != filenamePaneID {
		s.sampledParseWarn(filenamePaneID, fmt.Sprintf("paneID mismatch: filename=%q payload=%q", filenamePaneID, p.PaneID))
		return Payload{}, false
	}
	return p, true
}

// sampledParseWarn logs a parse failure at WARN, but only 1 of every
// parseWarnSampleRate occurrences per pane. A misbehaving producer (e.g.
// truncated lines in a loop) would otherwise floodlight quild.log.
func (s *Spool) sampledParseWarn(paneID, msg string) {
	s.mu.Lock()
	s.parseErrCounts[paneID]++
	n := s.parseErrCounts[paneID]
	s.mu.Unlock()
	if n%parseWarnSampleRate == 1 {
		logger.Warn("hookevents: pane=%s parse drop (sampled 1/%d): %s", paneID, parseWarnSampleRate, msg)
	}
}

// rotate truncates a fully-drained spool file and resets its offset. Caller
// is responsible for ensuring the file was just observed to have no
// unconsumed bytes (size == offset). Failures land in the hook log.
func (s *Spool) rotate(paneID, path string) {
	if err := os.Truncate(path, 0); err != nil {
		logger.Warn("hookevents: rotate spool %q: %v", path, err)
		return
	}
	s.mu.Lock()
	s.offsets[paneID] = 0
	s.mu.Unlock()
}

// isWhitespaceLine reports whether a slice is all spaces / tabs / nothing.
func isWhitespaceLine(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}

// Cleanup removes the spool file for a destroyed pane and forgets its
// offset. Idempotent; safe to call for panes that never had a spool file.
//
// Defensive against path traversal: the caller is expected to validate
// paneID upstream (the daemon's IPC handlers use isValidHexID) but we
// reject characters that could escape the spool dir as a second line of
// defense. A paneID of "../etc/passwd" would otherwise let an attacker
// who reached the IPC surface unlink arbitrary *.jsonl files under the
// daemon user.
func (s *Spool) Cleanup(paneID string) {
	if !safePaneID(paneID) {
		logger.Warn("hookevents: rejected cleanup for unsafe paneID %q", paneID)
		return
	}

	s.mu.Lock()
	delete(s.offsets, paneID)
	delete(s.parseErrCounts, paneID)
	s.mu.Unlock()

	path := filepath.Join(s.dir, paneID+".jsonl")
	// Belt-and-suspenders: ensure the cleaned path lives strictly under
	// s.dir even after filepath.Join's lexical processing. A future change
	// to safePaneID that lets `..` slip through would still be caught here.
	cleanedPath := filepath.Clean(path)
	cleanedDir := filepath.Clean(s.dir)
	if !strings.HasPrefix(cleanedPath, cleanedDir+string(filepath.Separator)) {
		logger.Warn("hookevents: rejected cleanup escaping spool dir: %q", cleanedPath)
		return
	}
	if err := os.Remove(cleanedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("hookevents: cleanup spool %q: %v", cleanedPath, err)
	}
}

// safePaneID rejects pane ids that could escape the spool directory via
// path-separator or parent-traversal segments. Matches the trust shape the
// daemon uses for its own pane id allocation (uuid-derived hex), but does
// NOT enforce the exact format — that lives in the daemon's isValidHexID
// check at the IPC ingress. Here we just refuse anything that could turn
// filepath.Join into a writable arbitrary path.
func safePaneID(id string) bool {
	if id == "" {
		return false
	}
	if strings.ContainsAny(id, `/\`+"\x00") {
		return false
	}
	if id == "." || id == ".." {
		return false
	}
	if strings.Contains(id, "..") {
		return false
	}
	return true
}
