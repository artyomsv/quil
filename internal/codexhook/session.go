package codexhook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// HookEnv carries the per-invocation context the hook needs, sourced from the
// QUIL_* environment the daemon sets on a codex pane at spawn. Codex hands its
// own process environment to hook commands (shell_environment_policy defaults
// to inherit = all), so these arrive exactly as they do for Claude.
type HookEnv struct {
	PaneID        string // QUIL_PANE_ID — empty means "invoked outside Quil" (no-op)
	QuilDir       string // QUIL_HOOK_HOME (QUIL_HOME fallback) — root for sessions/ and events/
	Mode          string // QUIL_HOOK_MODE: "default" | "verbose" | "off"
	RecordHistory bool   // QUIL_RECORD_HISTORY=1 — append full prompts to the history store
}

// validatePaneID rejects pane ids that could escape the sessions directory or
// forge a log line. Same invariant as claudehook's: the daemon only ever sets
// a validated uuid-hex id, but this package keeps its own guard so a future
// or hostile caller cannot read or write arbitrary files.
func validatePaneID(paneID string) error {
	if paneID == "" {
		return errors.New("codexhook: empty paneID")
	}
	if strings.ContainsAny(paneID, `/\`) || strings.Contains(paneID, "..") {
		return fmt.Errorf("codexhook: paneID %q contains path separators or parent traversal", paneID)
	}
	for _, r := range paneID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("codexhook: paneID %q contains a control character", paneID)
		}
	}
	return nil
}

// sessionIDRe is the canonical UUID shape codex mints (UUIDv7). The value ends
// up as the operand of `codex resume`, so it is validated rather than trusted;
// a flag-shaped or partial token is refused outright.
var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsValidSessionID reports whether id has the shape codex mints.
func IsValidSessionID(id string) bool {
	return sessionIDRe.MatchString(id)
}

// sessionIDFile returns the record path. The "codex-" prefix keeps it disjoint
// from claudehook's <paneID>.id by construction, the way opencodehook's is.
func sessionIDFile(quilDir, paneID string) string {
	return filepath.Join(quilDir, "sessions", "codex-"+paneID+".id")
}

// SessionRecord is what the hook persists for a pane: the live codex session
// id and the absolute path of its rollout file (empty when SessionStart carried
// none — "unknown", never "missing").
type SessionRecord struct {
	ID             string
	TranscriptPath string
	ModTime        time.Time
}

const (
	// maxIDBytes bounds the id line; a uuid is 36 bytes and anything past
	// this is a corrupt file whose value would reach `codex resume` argv.
	maxIDBytes = 256
	// maxRecordBytes caps a record read: one uuid line plus one path line,
	// sized for a long Windows path.
	maxRecordBytes = 8 << 10
)

// parseSessionRecord splits the two-line record, trimming PER LINE — a
// whole-file TrimSpace would glue both lines into one argv token. An over-long
// id line yields an EMPTY id rather than a truncated one.
func parseSessionRecord(body string) SessionRecord {
	var rec SessionRecord
	lines := strings.SplitN(body, "\n", 3)
	if id := strings.TrimSpace(lines[0]); len(id) <= maxIDBytes {
		rec.ID = id
	}
	if len(lines) > 1 {
		rec.TranscriptPath = strings.TrimSpace(lines[1])
	}
	return rec
}

// ReadPersistedSession returns the record the hook last wrote for paneID. A
// missing file satisfies errors.Is(err, os.ErrNotExist) so callers can tell
// "no SessionStart yet" from a corrupt file.
//
// Symlinks are refused via O_NOFOLLOW where the platform has it, and the
// opened file must be regular; the Stat runs on the open descriptor so the
// ModTime always belongs to the bytes returned.
func ReadPersistedSession(quilDir, paneID string) (SessionRecord, error) {
	if quilDir == "" {
		return SessionRecord{}, errors.New("codexhook: empty quilDir")
	}
	if err := validatePaneID(paneID); err != nil {
		return SessionRecord{}, err
	}
	path := sessionIDFile(quilDir, paneID)
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return SessionRecord{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return SessionRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return SessionRecord{}, fmt.Errorf("codexhook: %s is not a regular file", path)
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxRecordBytes))
	if err != nil {
		return SessionRecord{ModTime: info.ModTime()}, err
	}
	rec := parseSessionRecord(string(buf))
	rec.ModTime = info.ModTime()
	return rec, nil
}

// ReadPersistedSessionID is the id-only accessor, mirroring the other hook
// packages.
func ReadPersistedSessionID(quilDir, paneID string) (string, time.Time, error) {
	rec, err := ReadPersistedSession(quilDir, paneID)
	return rec.ID, rec.ModTime, err
}

// writeSessionFile validates and atomically writes the record. A non-uuid id
// is logged (by length only) and NOT written; a transcript path with a newline
// is dropped — it would forge a second record line — while the id is still
// recorded, so a hostile value costs the path and never the id.
func writeSessionFile(env HookEnv, sessionID, transcriptPath string) error {
	if sessionID == "" {
		hookLog(env.QuilDir, env.PaneID, "no session_id extracted from stdin")
		return nil
	}
	if !IsValidSessionID(sessionID) {
		hookLog(env.QuilDir, env.PaneID, fmt.Sprintf("session_id rejected as non-uuid (len=%d)", len(sessionID)))
		return nil
	}
	sessionsDir := filepath.Join(env.QuilDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		hookLog(env.QuilDir, env.PaneID, "mkdir sessions dir failed: "+err.Error())
		return err
	}
	body := sessionID + "\n"
	if transcriptPath != "" && !strings.ContainsAny(transcriptPath, "\r\n") {
		body += transcriptPath + "\n"
	}
	if err := atomicWrite(sessionIDFile(env.QuilDir, env.PaneID), []byte(body), 0o600); err != nil {
		hookLog(env.QuilDir, env.PaneID, "write session file failed: "+err.Error())
		return err
	}
	return nil
}

// atomicWrite writes via a temp file in the same directory and a rename, so a
// concurrent reader never sees a half-written record. The temp is removed on
// any error; the directory is 0700, so a leaked temp is owner-only.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// hookLog appends a best-effort breadcrumb to $QuilDir/codexhook/hook.log.
// Never returns an error — a failure to log must not surface to codex.
func hookLog(quilDir, paneID, msg string) {
	logDir := filepath.Join(quilDir, "codexhook")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "hook.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pane=%s %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), paneID, msg)
}
