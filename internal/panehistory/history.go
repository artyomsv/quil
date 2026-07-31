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
	"unicode"
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

// syntheticTags are the open/close tag pairs Claude Code uses for turns it
// injects that the user never typed. Two confirmed offenders:
//
//   - <task-notification>: when a background Task/subagent finishes, the
//     harness resumes the main loop by submitting that block as a
//     UserPromptSubmit.
//   - <agent-message>: when a subagent reports back to its parent, the report
//     is delivered the same way — as a prompt the user never typed. A single
//     turn may carry several concatenated blocks; the both-ends rule below
//     still matches, since the first opens and the last closes.
//
// Without this filter those machine-generated turns land in input history.
// Each open tag intentionally omits the closing ">" so a future attribute
// (<task-notification version="2">, <agent-message from="x">) still matches.
var syntheticTags = []struct{ open, close string }{
	{"<task-notification", "</task-notification>"},
	{"<agent-message", "</agent-message>"},
}

// IsSyntheticPrompt reports whether text is a harness-injected turn (not real
// user input) that must be excluded from history. A prompt is synthetic only
// when, after trimming surrounding whitespace, it BOTH opens with a known tag
// AND closes with its matching end tag — i.e. it is the whole notification
// block and nothing else. Requiring both ends means a real prompt that merely
// quotes or discusses the tag ("<task-notification is confusing, what is it?")
// is preserved; only a pure, complete block is dropped.
func IsSyntheticPrompt(text string) bool {
	t := strings.TrimSpace(text)
	for _, tag := range syntheticTags {
		if strings.HasPrefix(t, tag.open) && strings.HasSuffix(t, tag.close) {
			return true
		}
	}
	return false
}

// Append writes one entry to the pane's history file. Empty/whitespace text and
// synthetic harness-injected turns (see IsSyntheticPrompt) are skipped (returns
// nil without writing). Oversize text is truncated on a rune boundary with a
// trailing marker. V is forced to the current schema version. O_APPEND keeps
// concurrent hook invocations from clobbering each other.
func Append(quilDir, paneID string, e Entry) error {
	if strings.TrimSpace(e.Text) == "" || IsSyntheticPrompt(e.Text) {
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

// Read returns all real user entries oldest-first, excluding synthetic
// harness-injected turns (see IsSyntheticPrompt) so pre-existing junk written
// before the filter existed never surfaces. A missing file is not an error
// (returns nil, nil). Malformed lines — including a trailing partial line from
// an in-flight concurrent append — are skipped. A file larger than maxReadBytes
// is read from the tail so daemon memory stays bounded (the dropped first line
// is the oldest; Compact normally keeps the file well under the cap).
func Read(quilDir, paneID string) ([]Entry, error) {
	entries, err := readRaw(quilDir, paneID)
	if err != nil {
		return nil, err
	}
	return filterSynthetic(entries), nil
}

// readRaw loads every parseable entry from the pane file, unfiltered. Used by
// Read (which then drops synthetic turns) and Compact (which needs the raw set
// to detect droppable junk even when the file is under the ring cap).
func readRaw(quilDir, paneID string) ([]Entry, error) {
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

// filterSynthetic returns entries with harness-injected turns removed. When
// nothing needs filtering it returns the input slice unchanged — so a nil or
// all-real input allocates nothing and nil stays nil, preserving Read's
// missing-file contract.
func filterSynthetic(entries []Entry) []Entry {
	first := -1
	for i, e := range entries {
		if IsSyntheticPrompt(e.Text) {
			first = i
			break
		}
	}
	if first < 0 {
		return entries // common case: no synthetic entries, no copy
	}
	out := make([]Entry, first, len(entries)-1)
	copy(out, entries[:first])
	for _, e := range entries[first+1:] {
		if !IsSyntheticPrompt(e.Text) {
			out = append(out, e)
		}
	}
	return out
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

// PreviewLine renders text as a SINGLE display line for the history list: every
// run of whitespace (newlines and tabs included) collapses to one space, and
// the result is truncated rune-aware to maxBytes with a trailing "…".
//
// Flattening happens daemon-side rather than in the TUI because the list shows
// exactly one row per entry: a multi-line prompt has to become one line
// somewhere, and doing it before the wire keeps the payload proportional to
// what is actually displayed.
//
// Control characters (C0, DEL and C1) are DROPPED, not substituted. A prompt is
// free text the user may have pasted into, so it can carry an ANSI escape; an
// ESC that reached the list would repaint or reposition inside a fixed-width
// bordered modal that assumes one cell per printed column. Substituting a
// visible placeholder would keep the row honest about length but add noise to
// every row for a case that is always accidental — dropping is the quieter fix,
// and the full text is still available verbatim in the detail view.
func PreviewLine(text string, maxBytes int) string {
	var b strings.Builder
	b.Grow(len(text))
	pendingSpace := false
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = true
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// Drop: C0 controls, DEL, and C1 (U+009B is the CSI introducer).
		default:
			// b.Len() == 0 means nothing printable has been written yet, so a
			// leading run of whitespace emits nothing. A trailing run never
			// emits either — the space is written before the NEXT printable.
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		}
	}
	return truncRunes(b.String(), maxBytes)
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

// Compact rewrites the pane's history file keeping only the last keepLast real
// entries, dropping synthetic harness-injected turns (see IsSyntheticPrompt) in
// the process. It rewrites ONLY when the number of REAL entries exceeds
// keepLast. When real entries are within the cap the file is left untouched —
// even if pre-existing synthetic junk is on disk — because Read-time filtering
// already hides that junk from every caller, and rewriting would race the
// Claude hook's cross-process O_APPEND (a prompt appended between readRaw and
// the rename would be written to the old inode and lost). Gating on the RAW
// line count instead would reopen that race: historical task-notifications can
// push a pane with few real prompts over the cap, triggering a needless rewrite.
// Since Append no longer records synthetic turns, that junk is a finite, non-
// growing residue; it is purged only as part of a genuine over-cap trim of real
// entries. Atomic via temp file + rename.
func Compact(quilDir, paneID string, keepLast int) error {
	if keepLast < 0 {
		keepLast = 0
	}
	raw, err := readRaw(quilDir, paneID)
	if err != nil {
		return err
	}
	entries := filterSynthetic(raw)
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
