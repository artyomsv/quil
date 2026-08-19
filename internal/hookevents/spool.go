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

// removeFn is the unlink implementation used by Init. Overridable in tests to
// exercise the truncate fallback, which is otherwise only reachable on Windows
// when another process holds the file open without FILE_SHARE_DELETE — a state
// the Linux CI image cannot produce.
//
// Any test that stubs this MUST NOT call t.Parallel(). Its Init siblings do, and
// they are safe only because Go pauses parallel tests until the sequential phase
// finishes — so a stub installed by a serial test is never live while they run.
// Adding t.Parallel() to the stubbing test would race the package var, and the
// detector will not report it, because the two sets never overlap in a passing
// run. See TestSpool_Init_TruncatesWhenRemoveFails.
var removeFn = os.Remove

// openFile is a seam so the idle-tick test can count how many spool files a
// Tick actually opens.
//
// Same constraint as removeFn: any test that stubs this MUST NOT call
// t.Parallel(). See TestSpool_Tick_DoesNotOpenFilesWithNothingNew.
var openFile = os.Open

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
// start, Init REMOVES them so we do not replay stale events from a
// previous session (notifications are inherently ephemeral).
//
// Removing rather than truncating is load-bearing for cost, not tidiness.
// Tick walks every .jsonl in the directory and pays open+stat+close on each,
// so a zero-byte husk left for a pane that no longer exists costs syscalls on
// every 200 ms tick for the life of the daemon — and truncating meant the set
// only ever grew, across every restart, for as long as the install existed.
type Spool struct {
	dir string

	mu             sync.Mutex
	offsets        map[string]int64 // paneID → byte offset already consumed
	parseErrCounts map[string]uint64 // paneID → malformed-line counter for log sampling
}

// NewSpool returns a Spool reading from dir. Use Init to discard stale files
// on daemon startup; Tick on each poll; Cleanup on pane destroy.
func NewSpool(dir string) *Spool {
	return &Spool{
		dir:            dir,
		offsets:        make(map[string]int64),
		parseErrCounts: make(map[string]uint64),
	}
}

// Init prepares the spool directory: creates it if absent, then REMOVES every
// existing *.jsonl file so a fresh daemon never replays events from a previous
// run. Safe to call multiple times.
//
// Discard-on-start trades off durability for predictability: a hook that fired
// between daemon-stop and daemon-start is lost, but the alternative — replaying
// potentially-stale events that no longer represent live state — is worse for a
// notification surface.
//
// Unlinking rather than truncating is what bounds the directory. Tick walks
// every entry here five times a second and pays open+stat+close on each, so a
// zero-byte husk left for a pane that no longer exists costs syscalls for the
// life of the daemon — and truncating meant the set only ever grew, across
// every restart. Where the unlink fails (Windows refuses it while another
// process holds the file open without FILE_SHARE_DELETE) it falls back to the
// old truncate: unlinking is the optimisation, zeroing is the guarantee.
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
		// Containment check, mirroring Cleanup's. os.ReadDir yields base names
		// only and never "." or "..", so no escape is constructible here today
		// — this is symmetry, not a patched hole. It earns its place because
		// the OPERATION changed: this loop used to truncate and now unlinks,
		// and the guard set around it did not move with the blast radius.
		// readPaneFile and Cleanup both carry a check; the path that deletes
		// should not be the one without one.
		cleaned := filepath.Clean(path)
		if !strings.HasPrefix(cleaned, filepath.Clean(s.dir)+string(filepath.Separator)) {
			logger.Warn("hookevents: refusing to remove spool outside the dir: %q", cleaned)
			continue
		}
		if err := removeFn(cleaned); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Unlinking is the optimisation; zeroing is the guarantee. A file
			// we cannot delete must still not replay a previous session's
			// events, so fall back to what Init did before.
			logger.Warn("hookevents: remove stale spool %q: %v — truncating instead", cleaned, err)
			if err := os.Truncate(cleaned, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.Warn("hookevents: truncate spool %q: %v", cleaned, err)
			}
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

		// Skip the open entirely when the file has not grown. ReadDir already
		// carries the size on Windows (FindFirstFile fills it, so Info() costs
		// no extra syscall); on Linux this trades open+fstat+close for one
		// lstat. The idle case is the common one — at 5 Hz across a large
		// workspace it was the daemon's dominant syscall source.
		//
		// Refused in three cases, each load-bearing:
		//   - Info() errored: fall THROUGH to the full read. A failing stat must
		//     not silently stop a pane's events draining.
		//   - The pane has no recorded offset yet: a brand-new (or empty) file
		//     has size 0 and maps to offset 0, so a bare size==off test would
		//     skip it forever, including the moment it first gains content.
		//   - The file is at or past rotationThreshold: rotation runs from
		//     readPaneFile's idle branch, so skipping there strands the file to
		//     grow without bound — the exact failure this shortcut is near.
		if info, err := e.Info(); err == nil {
			s.mu.Lock()
			off := s.offsets[paneID]
			s.mu.Unlock()
			size := info.Size()
			// The map's zero value carries this for a pane never seen before,
			// so no separate "is it known" test is needed: an untracked file
			// has off == 0, which matches only when it is also empty — and an
			// empty file has nothing to read and nothing to rotate. The moment
			// it gains a byte, size stops matching and the read below runs.
			//
			// External truncation (size < off) also falls through to the read,
			// where readPaneFile restarts from zero.
			if size == off && size < rotationThreshold {
				continue
			}
		}

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

	f, err := openFile(path)
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
