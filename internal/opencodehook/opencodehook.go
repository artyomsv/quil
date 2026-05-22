// Package opencodehook manages OpenCode's plugin-based session-id tracker for
// Quil.
//
// Quil tracks opencode session-id rotation (new session, /new, fork,
// compaction) by registering a small JS plugin via the OPENCODE_CONFIG_CONTENT
// env var (inline JSON containing an absolute path to our plugin file). The
// plugin lives under $QUIL_HOME/opencodehook/ and writes per-pane session ids
// to $QUIL_HOME/sessions/opencode-<paneID>.id.
//
// This package never writes into ~/.config/opencode/ or any other user-owned
// opencode directory — the plugin is referenced per-spawn via env var.
// OPENCODE_CONFIG_CONTENT is merged with the user's existing config (verified
// against opencode 1.14.x) so user-installed plugins / agents / modes remain
// active inside Quil-spawned opencode panes.
package opencodehook

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed scripts/*
var scripts embed.FS

const pluginScriptName = "quil-session-tracker.js"

// shellUnsafeChars mirror claudehook's set plus an explicit NUL byte: a
// quilDir containing any of these could break out of JSON / shell quoting
// when we embed the path into OPENCODE_CONFIG_CONTENT or any future wrapper.
// json.Marshal would handle most of these correctly on its own, but a NUL
// byte terminates the env var on the C side without warning, and `$` /
// backtick can still confuse downstream tooling that ever shells out. Reject
// up-front rather than try to escape every shell variant.
const shellUnsafeChars = "\"`$\n\r\t\x00"

// ValidateQuilDir reports an error if quilDir contains characters that
// cannot be safely embedded into inline config JSON.
func ValidateQuilDir(quilDir string) error {
	if quilDir == "" {
		return errors.New("opencodehook: empty quilDir")
	}
	if strings.ContainsAny(quilDir, shellUnsafeChars) {
		return fmt.Errorf("opencodehook: quilDir %q contains shell-unsafe characters", quilDir)
	}
	return nil
}

// paneIDRe matches Quil pane IDs (`pane-<short-hex>`) and any pane-id format
// limited to ASCII alphanumerics plus `-` and `_`. Kept in sync with the JS
// plugin's PANE_ID_RE in scripts/quil-session-tracker.js: if either side
// loosens, panes that "should" resume can silently fall back to --continue
// because one validator drops the write while the other accepts the id.
var paneIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// validatePaneID rejects pane ids that could let a caller escape the sessions
// directory. Mirror of the JS plugin's PANE_ID_RE so JS-side and Go-side
// validation stay aligned — see paneIDRe doc comment.
func validatePaneID(paneID string) error {
	if paneID == "" {
		return errors.New("opencodehook: empty paneID")
	}
	if !paneIDRe.MatchString(paneID) {
		return fmt.Errorf("opencodehook: paneID %q contains characters outside [A-Za-z0-9_-] or exceeds 64 bytes", paneID)
	}
	return nil
}

// sessionIDRe matches opencode session ids as produced by opencode 1.14.x
// (e.g., `ses_1b0d89947ffeE92bKkZ4LTBzO2`) and is the Go-side mirror of the
// JS plugin's SESSION_ID_RE. Used by IsValidSessionID so the daemon refuses
// to promote a corrupted or partially-written id file into `--session <id>`
// at restore time.
var sessionIDRe = regexp.MustCompile(`^[0-9a-zA-Z_-]{1,128}$`)

// IsValidSessionID reports whether id matches the shape opencode emits on the
// session bus. Used by the daemon's restore path to filter ids that fail
// shape validation (corrupted file, partial write surviving rename, manual
// edit) before passing them to opencode as `--session <id>`.
func IsValidSessionID(id string) bool {
	return sessionIDRe.MatchString(id)
}

// EnsureScripts writes the embedded plugin file to $quilDir/opencodehook/.
// Idempotent — atomic temp+rename so a concurrent opencode load never reads a
// truncated file. Mirrors claudehook.EnsureScripts.
func EnsureScripts(quilDir string) error {
	if err := ValidateQuilDir(quilDir); err != nil {
		return err
	}
	dir := filepath.Join(quilDir, "opencodehook")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create opencodehook dir: %w", err)
	}
	data, err := scripts.ReadFile("scripts/" + pluginScriptName)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", pluginScriptName, err)
	}
	if err := atomicWrite(filepath.Join(dir, pluginScriptName), data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", pluginScriptName, err)
	}
	return nil
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	// Use CreateTemp for the unique-name guarantee, then immediately Chmod so
	// the final perm bits are set BEFORE we write content. On Unix CreateTemp
	// already opens with 0600 which matches our typical perm, but on Windows
	// the default is umask-derived (0666 & ~umask, usually 0644) so chmod-up-
	// front closes that brief window. Mode 0700 dir ensures even the wider
	// transient mode isn't accessible to other users.
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// cleanup removes the half-written temp on any error. The Remove return
	// value is intentionally discarded: we are already returning an error to
	// the caller, and the dir is 0700 so a leaked temp is owner-only.
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

// ScriptPath returns the absolute path to the plugin file. EnsureScripts must
// have been called first.
func ScriptPath(quilDir string) string {
	return filepath.Join(quilDir, "opencodehook", pluginScriptName)
}

// configContentSchema is the subset of opencode's config.json shape we emit
// inline via OPENCODE_CONFIG_CONTENT. opencode merges this with the user's
// regular config so additional user plugins remain active.
type configContentSchema struct {
	Plugin []string `json:"plugin"`
}

// BuildConfigContent returns the JSON string Quil passes to opencode via the
// OPENCODE_CONFIG_CONTENT env var. The single plugin entry is the absolute
// path returned by ScriptPath. opencode 1.14.x accepts raw absolute paths in
// the plugin array and merges them with the user's other config sources.
//
// The path MUST be absolute — opencode resolves plugin entries against the
// child process's CWD, not the daemon's. With `prompts_cwd = true` (which the
// opencode plugin enables) the child runs in whatever directory the user
// picked, so a relative scriptPath would silently look for the plugin under
// that user-chosen dir and fail to load. Reject up-front so the failure is
// visible at spawn time instead of as a missing-tracking footgun later.
func BuildConfigContent(scriptPath string) (string, error) {
	if scriptPath == "" {
		return "", errors.New("opencodehook: empty scriptPath")
	}
	if !filepath.IsAbs(scriptPath) {
		return "", fmt.Errorf("opencodehook: scriptPath must be absolute, got %q", scriptPath)
	}
	c := configContentSchema{Plugin: []string{scriptPath}}
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal opencode config content: %w", err)
	}
	return string(b), nil
}

// sessionIDFile returns the absolute path to the recorded id for a pane.
// Filename is prefixed with "opencode-" so it cannot collide with
// claudehook's $QUIL_HOME/sessions/<paneID>.id files.
func sessionIDFile(quilDir, paneID string) string {
	return filepath.Join(quilDir, "sessions", "opencode-"+paneID+".id")
}

// ReadPersistedSessionID returns the session id our plugin last wrote for the
// given pane. errors.Is(err, os.ErrNotExist) for "no session event has fired
// yet" so callers can cleanly fall back to --continue.
//
// Symlinks are rejected atomically via O_NOFOLLOW (no Lstat+Open TOCTOU
// window). On Windows O_NOFOLLOW resolves to 0; symlink creation there
// requires elevated privilege so the practical attack surface is narrower.
// The Stat is performed on the resulting file descriptor so the modTime
// reported always corresponds to the bytes returned, even under concurrent
// rotation by the plugin.
func ReadPersistedSessionID(quilDir, paneID string) (id string, modTime time.Time, err error) {
	if quilDir == "" {
		return "", time.Time{}, errors.New("opencodehook: empty quilDir")
	}
	if err := validatePaneID(paneID); err != nil {
		return "", time.Time{}, err
	}
	path := sessionIDFile(quilDir, paneID)
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return "", time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", time.Time{}, err
	}
	const maxIDBytes = 256
	buf, err := io.ReadAll(io.LimitReader(f, maxIDBytes))
	if err != nil {
		return "", info.ModTime(), err
	}
	return strings.TrimSpace(string(buf)), info.ModTime(), nil
}
