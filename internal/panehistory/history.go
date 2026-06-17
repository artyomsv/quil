// Package panehistory stores and serves per-pane user-input history. One
// JSONL file per pane lives under <quilDir>/history/<paneID>.jsonl. The Claude
// hook subprocess appends entries; the daemon reads, previews, and compacts.
//
// Concurrency: the hook subprocess only ever O_APPENDs single lines and the
// daemon only reads/compacts, so writes never interleave in practice. On Linux
// an O_APPEND write of one line is atomic; on Windows the guarantee is weaker
// for very large lines, but Read tolerates a malformed/partial trailing line
// and Compact's rename is best-effort (a transient Windows sharing-violation is
// logged by the caller and retried on the next read). Treat "concurrent" as
// serial-enough, not lock-free-safe.
package panehistory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxEntryBytes caps a single entry's text — generous enough for a pasted
	// stack trace, bounded so one paste can't bloat the store.
	MaxEntryBytes = 64 * 1024
	// MaxEntries is the ring cap Compact enforces.
	MaxEntries = 200
	// schemaVersion is stamped into every entry's V field.
	schemaVersion = 1
	// truncMarker is appended when Append caps oversize text.
	truncMarker = "…[truncated]"
)

// maxReadBytes bounds how much of a history file Read buffers. A runaway
// producer that never triggered Compact could grow the file without bound; Read
// then loads only the trailing window (the newest entries), keeping daemon
// memory bounded. A var, not a const, so tests can lower it. Default ≈ the
// ring's worst case (MaxEntries × MaxEntryBytes).
var maxReadBytes int64 = MaxEntries * MaxEntryBytes

// Entry is one recorded user input, persisted as a single JSONL line. TsMs
// doubles as the entry's lookup id on the fetch-one-entry IPC path; two
// submissions in the same millisecond would collide, but human prompt cadence
// makes that effectively impossible (a collision just returns the first match).
type Entry struct {
	V         int    `json:"v"`
	TsMs      int64  `json:"ts_ms"`
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text"`
}

// Dir returns the history directory under quilDir.
func Dir(quilDir string) string { return filepath.Join(quilDir, "history") }

// Path returns the per-pane history file path.
func Path(quilDir, paneID string) string { return filepath.Join(Dir(quilDir), paneID+".jsonl") }

// Append writes one entry to the pane's history file. Empty/whitespace text is
// skipped (returns nil without writing). Oversize text is truncated on a rune
// boundary with a trailing marker. V is forced to the current schema version.
// O_APPEND keeps concurrent hook invocations from clobbering each other.
func Append(quilDir, paneID string, e Entry) error {
	if strings.TrimSpace(e.Text) == "" {
		return nil
	}
	e.V = schemaVersion
	e.Text = capText(e.Text, MaxEntryBytes)

	if err := os.MkdirAll(Dir(quilDir), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := Path(quilDir, paneID)
	if err := rejectSymlink(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// capText truncates s to at most maxBytes on a rune boundary, appending a
// marker when truncated. Always returns valid UTF-8.
func capText(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	budget := maxBytes - len(truncMarker)
	if budget < 0 {
		budget = 0
	}
	cut := 0
	for i := range s {
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut] + truncMarker
}

// rejectSymlink returns an error when path is a symlink (a missing file is
// fine). The history dir is owner-only (0o700), but a planted symlink at
// history/<paneID>.jsonl must not redirect a write or a rename. Mirrors the
// symlink guard in internal/persist/notes.go.
func rejectSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("panehistory: refusing to use symlink %q", path)
	}
	return nil
}

// Read returns all entries oldest-first. A missing file is not an error
// (returns nil, nil). Malformed lines — including a trailing partial line from
// an in-flight concurrent append — are skipped. A file larger than maxReadBytes
// is read from the tail so daemon memory stays bounded (the dropped first line
// is the oldest; Compact normally keeps the file well under the cap).
func Read(quilDir, paneID string) ([]Entry, error) {
	path := Path(quilDir, paneID)
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	if fi, serr := f.Stat(); serr == nil && fi.Size() > maxReadBytes {
		if _, serr := f.Seek(fi.Size()-maxReadBytes, io.SeekStart); serr == nil {
			r = bufio.NewReader(f)
			// Drop the now-partial first line so we start on an entry boundary.
			if _, derr := r.ReadBytes('\n'); derr != nil && !errors.Is(derr, io.EOF) {
				return nil, derr
			}
		}
	}
	return readEntries(r)
}

// readEntries parses JSONL Entry lines from r, skipping malformed/partial lines.
func readEntries(r *bufio.Reader) ([]Entry, error) {
	var entries []Entry
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 {
			var e Entry
			if json.Unmarshal(line, &e) == nil { // skip malformed / trailing partial line
				entries = append(entries, e)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return entries, rerr
		}
	}
	return entries, nil
}

// Preview returns up to maxLines logical lines of text, each truncated (rune-
// aware) to maxBytes with a trailing "…". Tabs become four spaces and CRs are
// stripped so the list renders cleanly.
func Preview(text string, maxLines, maxBytes int) []string {
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\t", "    ")
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, truncRunes(ln, maxBytes))
	}
	return out
}

// truncRunes truncates s to at most maxBytes bytes on a rune boundary,
// appending "…" when truncated.
func truncRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const ell = "…"
	budget := maxBytes - len(ell)
	if budget < 0 {
		budget = 0
	}
	cut := 0
	for i := range s {
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut] + ell
}

// Compact rewrites the pane's history file keeping only the last keepLast
// entries. No-op when at or under the limit. Atomic via temp file + rename.
func Compact(quilDir, paneID string, keepLast int) error {
	entries, err := Read(quilDir, paneID)
	if err != nil {
		return err
	}
	if len(entries) <= keepLast {
		return nil
	}
	entries = entries[len(entries)-keepLast:]

	if err := os.MkdirAll(Dir(quilDir), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Dir(quilDir), paneID+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // best-effort cleanup if rename never happens

	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, Path(quilDir, paneID))
}
