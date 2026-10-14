package webgw

import (
	"encoding/json"
	"net/http"
	"regexp"
)

// Files is the gateway's access to this machine's quil files (spec 5d D1,
// D6). cmd/quil implements it; webgw never imports config or tui. Every path
// is built by the implementation: the page names a target, never a path.
type Files interface {
	Settings() (SettingsView, error)
	SetSetting(key string, value json.RawMessage) (SettingsView, error)
	ReadText(t FileTarget) (TextFile, error)
	WriteText(t FileTarget, text, baseHash string, force bool) (TextFile, error)
	Migration() ([]StaleFile, error)
	ResolveMigration(name, choice, text string) ([]StaleFile, error)
	LogTail(name string) (LogText, error)
}

const (
	TargetTemplates = "templates"
	TargetPlugin    = "plugin"
)

// FileTarget names a text file: the templates file, or one plugin file.
type FileTarget struct {
	Kind string
	Name string
}

// FileTextMax caps a file the browser opens or saves (spec 5d §5.3).
const FileTextMax = 256 << 10

var pluginName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// PluginNameOK reports whether name may name a plugin file.
func PluginNameOK(name string) bool { return pluginName.MatchString(name) }

type SettingRow struct {
	Key     string   `json:"key"`
	Group   string   `json:"group"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`
	Choices []string `json:"choices,omitempty"`
	Applies string   `json:"applies"`
	Value   any      `json:"value"`
}

type KeysView struct {
	Preset       string   `json:"preset"`
	Prefix       string   `json:"prefix"`
	Presets      []string `json:"presets"`
	PrefixUsed   bool     `json:"prefix_used"`
	PresetPrefix string   `json:"preset_prefix"`
	// Error is a fixed text when bindings.toml does not read; the Keys tab
	// is then read-only.
	Error string `json:"error,omitempty"`
}

type SettingsView struct {
	Rows []SettingRow `json:"rows"`
	Keys KeysView     `json:"keys"`
}

// TextFile is a file as the editor shows it. Hash is the SHA-256 of the raw
// bytes on disk ("" for a missing file): the page echoes it as base_hash and
// never hashes itself. Default is the shipped templates file (templates only).
type TextFile struct {
	Text    string `json:"text"`
	Hash    string `json:"hash"`
	Exists  bool   `json:"exists"`
	Default string `json:"default,omitempty"`
}

type StaleFile struct {
	Name        string `json:"name"`
	Mine        string `json:"mine"`
	Default     string `json:"default"`
	NeedVersion int    `json:"need_version"`
}

type LogText struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
	Mtime     string `json:"mtime"`
	Missing   bool   `json:"missing"`
}

// FileError is a refusal the page sees: an HTTP status and a fixed text.
// Any other error from Files is logged and answered 500.
type FileError struct {
	Status  int
	Text    string
	Current *TextFile
}

func (e *FileError) Error() string { return e.Text }

var (
	ErrNotFound           = &FileError{Status: http.StatusNotFound, Text: "not found"}
	ErrBadName            = &FileError{Status: http.StatusBadRequest, Text: "not a plugin file"}
	ErrTooLarge           = &FileError{Status: http.StatusRequestEntityTooLarge, Text: "too large to edit in the browser; use the TUI"}
	ErrSettingsUnparsable = &FileError{Status: http.StatusConflict, Text: "settings file does not parse; fix it in an editor"}
)

// Conflict is the 409 for a file changed on disk since the page loaded it.
func Conflict(cur TextFile) error {
	return &FileError{Status: http.StatusConflict, Text: "changed on disk", Current: &cur}
}

// Invalid is the 422 for a value or text its check refused; text is the check's.
func Invalid(text string) error {
	return &FileError{Status: http.StatusUnprocessableEntity, Text: text}
}
