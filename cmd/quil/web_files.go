package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/artyomsv/quil/internal/atomicfile"
	"github.com/artyomsv/quil/internal/changelog"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/logtail"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/tui"
	"github.com/artyomsv/quil/internal/webgw"
)

// webFiles is quil web's access to this machine's files (spec 5d). The two
// locks keep the gateway's own reads and writes apart: on Windows a rename
// fails over a file another goroutine holds open. A reader in another
// process (the TUI) is covered by atomicfile's one rename retry.
type webFiles struct {
	launch  config.Config
	connect bool

	settingsMu sync.Mutex // config.toml and bindings.toml
	filesMu    sync.Mutex // templates.toml and plugins/*.toml
	warned     atomic.Bool
}

func newWebFiles(launch config.Config, connect bool) *webFiles {
	return &webFiles{launch: launch, connect: connect}
}

var _ webgw.Files = (*webFiles)(nil)

// loadConfigLocked reads config.toml; a missing or unparsable file is the
// copy loaded at start (an unparsable one is logged once). Caller holds
// settingsMu.
func (f *webFiles) loadConfigLocked() config.Config {
	cfg, err := config.Load(config.ConfigPath())
	if err == nil {
		return cfg
	}
	if !errors.Is(err, fs.ErrNotExist) && f.warned.CompareAndSwap(false, true) {
		log.Printf("config.toml does not parse; using the copy loaded at start: %v", err)
	}
	return f.launch
}

func (f *webFiles) loadConfig() config.Config {
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	return f.loadConfigLocked()
}

// ClientExtras is the config-derived part of /api/client, read per request.
func (f *webFiles) ClientExtras() webgw.ClientExtras {
	cfg := f.loadConfig()
	img := cfg.Sandbox.DefaultImage
	if !f.connect {
		if remembered := tui.LoadSandboxImage(config.SandboxImagePath("")); remembered != "" {
			img = remembered
		}
	}
	f.settingsMu.Lock()
	km := webKeymap(cfg)
	f.settingsMu.Unlock()
	f.filesMu.Lock()
	tpls, terr := webTemplates()
	f.filesMu.Unlock()
	title, items := tui.RandomDisclaimerTip()
	return webgw.ClientExtras{
		SandboxSignIn:  cfg.Sandbox.DefaultSignIn(),
		SandboxImage:   img,
		Keymap:         km,
		Notifications:  webNotify(cfg),
		Templates:      tpls,
		TemplatesError: terr,
		Connect:        f.connect,
		WhatsNew:       webWhatsNew(),
		ShowDisclaimer: cfg.UI.ShowDisclaimer,
		Tip:            &webgw.Tip{Title: title, Items: items},
	}
}

var whatsNewKinds = map[changelog.Kind]string{
	changelog.KindAdded: "added", changelog.KindChanged: "changed", changelog.KindDeprecated: "deprecated",
	changelog.KindRemoved: "removed", changelog.KindFixed: "fixed", changelog.KindSecurity: "security",
}

func webWhatsNew() *webgw.WhatsNew {
	r := changelog.Latest()
	if r == nil {
		return nil
	}
	w := &webgw.WhatsNew{Version: r.Version, Date: r.Date, Items: []webgw.WhatsNewItem{}}
	for _, e := range r.Entries {
		w.Items = append(w.Items, webgw.WhatsNewItem{Kind: whatsNewKinds[e.Kind], Text: e.Text})
	}
	return w
}

// --- settings ---

func (f *webFiles) Settings() (webgw.SettingsView, error) {
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	cfg, err := config.Load(config.ConfigPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return webgw.SettingsView{}, webgw.ErrSettingsUnparsable
	}
	b, berr := config.LoadBindings()
	return settingsView(cfg, b, berr), nil
}

// SetSetting stores one value. config.toml and bindings.toml that do not
// parse are never replaced (spec 5d §5.3).
func (f *webFiles) SetSetting(key string, raw json.RawMessage) (webgw.SettingsView, error) {
	if strings.HasPrefix(key, "keys.") {
		return f.setKey(key, raw)
	}
	s, ok := config.LookupSetting(key)
	if !ok {
		return webgw.SettingsView{}, webgw.Invalid("unknown setting")
	}
	v, err := settingInput(s.Kind, raw)
	if err != nil {
		return webgw.SettingsView{}, webgw.Invalid(err.Error())
	}
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	path := config.ConfigPath()
	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return webgw.SettingsView{}, webgw.ErrSettingsUnparsable
	}
	if err := s.Set(&cfg, v); err != nil {
		return webgw.SettingsView{}, webgw.Invalid(err.Error())
	}
	if err := config.Save(path, cfg); err != nil {
		return webgw.SettingsView{}, err
	}
	b, berr := config.LoadBindings()
	// Built from the Config just saved, never through the locked loader.
	return settingsView(cfg, b, berr), nil
}

func (f *webFiles) setKey(key string, raw json.RawMessage) (webgw.SettingsView, error) {
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return webgw.SettingsView{}, webgw.Invalid("must be text")
	}
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	b, err := config.LoadBindings()
	if err != nil {
		return webgw.SettingsView{}, webgw.ErrSettingsUnparsable
	}
	switch key {
	case "keys.preset":
		err = config.SetKeyPreset(&b, v)
	case "keys.prefix":
		err = config.SetKeyPrefix(&b, v)
	default:
		return webgw.SettingsView{}, webgw.Invalid("unknown setting")
	}
	if err != nil {
		return webgw.SettingsView{}, webgw.Invalid(err.Error())
	}
	if err := config.WriteBindings(b); err != nil {
		return webgw.SettingsView{}, err
	}
	return settingsView(f.loadConfigLocked(), b, nil), nil
}

// settingInput turns a JSON value into the table's string form, refusing a
// value of the wrong JSON type (Review Focus 3).
func settingInput(kind string, raw json.RawMessage) (string, error) {
	switch kind {
	case config.SettingBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return "", errors.New("must be true or false")
		}
		return strconv.FormatBool(b), nil
	case config.SettingInt:
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return "", errors.New("must be a whole number")
		}
		return strconv.Itoa(n), nil
	case config.SettingFloat:
		var x float64
		if err := json.Unmarshal(raw, &x); err != nil {
			return "", errors.New("must be a number")
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	default:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", errors.New("must be text")
		}
		return s, nil
	}
}

func settingsView(cfg config.Config, b config.Bindings, berr error) webgw.SettingsView {
	var rows []webgw.SettingRow
	for _, s := range config.Settings() {
		raw := s.Get(cfg)
		var val any = raw
		switch s.Kind {
		case config.SettingBool:
			val = raw == "true"
		case config.SettingInt:
			if n, err := strconv.Atoi(raw); err == nil {
				val = n
			}
		case config.SettingFloat:
			if x, err := strconv.ParseFloat(raw, 64); err == nil {
				val = x
			}
		}
		rows = append(rows, webgw.SettingRow{Key: s.Key, Group: s.Group, Label: s.Label, Kind: s.Kind, Choices: s.Choices, Applies: s.Applies, Value: val})
	}
	keys := webgw.KeysView{Preset: b.Preset, Prefix: b.Prefix, Presets: keymap.PresetNames(), PrefixUsed: keymap.PresetUsesPrefix(b.Preset)}
	if p, err := keymap.LoadPreset(b.Preset); err == nil {
		keys.PresetPrefix = p.Prefix
	}
	if berr != nil {
		log.Printf("bindings.toml unreadable: %v", berr)
		keys.Error = "bindings.toml does not read; fix it in an editor"
	}
	return webgw.SettingsView{Rows: rows, Keys: keys}
}

// --- text files ---

// targetPath resolves t to a path on this machine. A plugin name passes the
// regex, its file must exist in PluginsDir() as a regular file (never a link)
// and stay inside that folder after cleaning.
func (f *webFiles) targetPath(t webgw.FileTarget) (path string, isPlugin bool, err error) {
	switch t.Kind {
	case webgw.TargetTemplates:
		return config.TemplatesPath(), false, nil
	case webgw.TargetPlugin:
		if !webgw.PluginNameOK(t.Name) {
			return "", true, webgw.ErrBadName
		}
		dir, err := filepath.Abs(config.PluginsDir())
		if err != nil {
			return "", true, err
		}
		p := filepath.Join(dir, t.Name+".toml")
		if filepath.Dir(p) != dir {
			return "", true, webgw.ErrBadName
		}
		li, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return "", true, webgw.ErrNotFound
		}
		if err != nil {
			return "", true, err
		}
		if !li.Mode().IsRegular() {
			return "", true, webgw.ErrBadName
		}
		return p, true, nil
	}
	return "", false, webgw.ErrNotFound
}

// readRaw reads at most FileTextMax bytes through the open handle; nil data
// and no error for a missing file.
func readRaw(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, webgw.ErrBadName
	}
	data, err := io.ReadAll(io.LimitReader(fh, webgw.FileTextMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > webgw.FileTextMax {
		return nil, webgw.ErrTooLarge
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func hashOf(data []byte) string {
	if data == nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// normalize drops a leading BOM and changes line ends as the TUI does: every
// \r for a plugin file (editor.go:113), CRLF only for templates (templates.go:275).
func normalize(text string, isPlugin bool) string {
	text = strings.TrimPrefix(text, "Feff")
	if isPlugin {
		return strings.ReplaceAll(text, "\r", "")
	}
	return strings.ReplaceAll(text, "\r\n", "\n")
}

func (f *webFiles) textFile(data []byte, isPlugin bool) webgw.TextFile {
	tf := webgw.TextFile{Hash: hashOf(data), Exists: data != nil}
	if data == nil && !isPlugin {
		tf.Text = config.DefaultTemplatesText()
	} else {
		tf.Text = normalize(string(data), isPlugin)
	}
	if !isPlugin {
		tf.Default = config.DefaultTemplatesText()
	}
	return tf
}

func (f *webFiles) ReadText(t webgw.FileTarget) (webgw.TextFile, error) {
	f.filesMu.Lock()
	defer f.filesMu.Unlock()
	path, isPlugin, err := f.targetPath(t)
	if err != nil {
		return webgw.TextFile{}, err
	}
	data, err := readRaw(path)
	if err != nil {
		return webgw.TextFile{}, err
	}
	return f.textFile(data, isPlugin), nil
}

func (f *webFiles) WriteText(t webgw.FileTarget, text, baseHash string, force bool) (webgw.TextFile, error) {
	if len(text) > webgw.FileTextMax {
		return webgw.TextFile{}, webgw.ErrTooLarge
	}
	f.filesMu.Lock()
	defer f.filesMu.Unlock()
	path, isPlugin, err := f.targetPath(t)
	if err != nil {
		return webgw.TextFile{}, err
	}
	data, err := readRaw(path)
	if err != nil && !errors.Is(err, webgw.ErrTooLarge) {
		return webgw.TextFile{}, err
	}
	if !force && hashOf(data) != baseHash {
		cur := f.textFile(data, isPlugin)
		return webgw.TextFile{}, webgw.Conflict(cur)
	}
	text = normalize(text, isPlugin)
	if isPlugin {
		var probe map[string]any
		if err := toml.Unmarshal([]byte(text), &probe); err != nil {
			return webgw.TextFile{}, webgw.Invalid("TOML syntax error: " + err.Error())
		}
	} else {
		checked, err := config.CheckTemplatesSource(text)
		if err != nil {
			return webgw.TextFile{}, webgw.Invalid(err.Error())
		}
		text = checked
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return webgw.TextFile{}, err
	}
	if err := atomicfile.Write(path, []byte(text)); err != nil {
		return webgw.TextFile{}, err
	}
	return f.textFile([]byte(text), isPlugin), nil
}

// --- migration ---

func staleFiles(list []plugin.StalePlugin) []webgw.StaleFile {
	out := make([]webgw.StaleFile, 0, len(list))
	for _, sp := range list {
		out = append(out, webgw.StaleFile{
			Name:        strings.TrimSuffix(filepath.Base(sp.FilePath), ".toml"),
			Mine:        normalize(string(sp.UserData), true),
			Default:     string(sp.DefaultData),
			NeedVersion: sp.NeedVersion,
		})
	}
	return out
}

func (f *webFiles) Migration() ([]webgw.StaleFile, error) {
	f.filesMu.Lock()
	defer f.filesMu.Unlock()
	list, err := plugin.StaleDefaults(config.PluginsDir())
	if err != nil {
		return nil, err
	}
	return staleFiles(list), nil
}

// ResolveMigration writes the default, or the user's text when its TOML
// parses and its schema_version reaches the default's (the TUI rule,
// internal/tui/migration.go:132-156). It answers the files still stale.
func (f *webFiles) ResolveMigration(name, choice, text string) ([]webgw.StaleFile, error) {
	if !webgw.PluginNameOK(name) {
		return nil, webgw.ErrBadName
	}
	f.filesMu.Lock()
	defer f.filesMu.Unlock()
	dir := config.PluginsDir()
	list, err := plugin.StaleDefaults(dir)
	if err != nil {
		return nil, err
	}
	var sp *plugin.StalePlugin
	for i := range list {
		if filepath.Base(list[i].FilePath) == name+".toml" {
			sp = &list[i]
		}
	}
	if sp == nil {
		return nil, webgw.ErrNotFound
	}
	if li, err := os.Lstat(sp.FilePath); err != nil || !li.Mode().IsRegular() {
		return nil, webgw.ErrBadName
	}
	var data []byte
	switch choice {
	case "default":
		data = sp.DefaultData
	case "mine":
		text = normalize(text, true)
		var probe map[string]any
		if err := toml.Unmarshal([]byte(text), &probe); err != nil {
			return nil, webgw.Invalid("Invalid TOML: " + err.Error())
		}
		if have := plugin.ParseSchemaVersion([]byte(text)); have < sp.NeedVersion {
			return nil, webgw.Invalid(fmt.Sprintf("schema_version must be >= %d (currently %d)", sp.NeedVersion, have))
		}
		data = []byte(text)
	default:
		return nil, webgw.Invalid("choice must be default or mine")
	}
	if err := atomicfile.Write(sp.FilePath, data); err != nil {
		return nil, err
	}
	log.Printf("migration: saved %s (%s)", sp.FilePath, choice)
	left, err := plugin.StaleDefaults(dir)
	if err != nil {
		return nil, err
	}
	return staleFiles(left), nil
}

// --- logs ---

func (f *webFiles) LogTail(name string) (webgw.LogText, error) {
	if name == "mcp" {
		dir := config.MCPLogDir(f.loadConfig().MCP)
		text, n, err := logtail.JoinDir(dir, logtail.MaxBytes)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && n == 0) {
			return webgw.LogText{Missing: true}, nil
		}
		if err != nil {
			return webgw.LogText{}, err
		}
		return webgw.LogText{Text: text, Size: int64(len(text))}, nil
	}
	file := map[string]string{"client": "quil.log", "daemon": "quild.log", "web": "web.log"}[name]
	if file == "" {
		return webgw.LogText{}, webgw.ErrNotFound
	}
	path := filepath.Join(config.QuilDir(), file)
	li, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return webgw.LogText{Missing: true}, nil
	}
	if err != nil {
		return webgw.LogText{}, err
	}
	if !li.Mode().IsRegular() {
		return webgw.LogText{}, webgw.ErrBadName
	}
	text, err := logtail.Tail(path, logtail.MaxBytes)
	if err != nil {
		return webgw.LogText{}, err
	}
	return webgw.LogText{
		Text:      text,
		Truncated: li.Size() > logtail.MaxBytes,
		Size:      li.Size(),
		Mtime:     li.ModTime().UTC().Format(time.RFC3339),
	}, nil
}
