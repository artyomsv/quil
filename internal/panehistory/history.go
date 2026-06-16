// Package panehistory stores and serves per-pane user-input history. One
// JSONL file per pane lives under <quilDir>/history/<paneID>.jsonl. The Claude
// hook subprocess appends entries; the daemon reads, previews, and compacts.
package panehistory

import (
	"bufio"
	"encoding/json"
	"errors"
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

// Entry is one recorded user input, persisted as a single JSONL line.
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
	f, err := os.OpenFile(Path(quilDir, paneID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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

// Read returns all entries oldest-first. A missing file is not an error
// (returns nil, nil). Malformed lines — including a trailing partial line from
// an in-flight concurrent append — are skipped.
func Read(quilDir, paneID string) ([]Entry, error) {
	f, err := os.Open(Path(quilDir, paneID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	r := bufio.NewReader(f)
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
