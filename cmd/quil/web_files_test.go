package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/webgw"
)

func home(t *testing.T) *webFiles {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := os.MkdirAll(config.PluginsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return newWebFiles(config.Default(), false)
}

func status(err error) int {
	var fe *webgw.FileError
	if errors.As(err, &fe) {
		return fe.Status
	}
	return 0
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// bom is U+FEFF from its code point, never an escape (see utf8BOM).
var bom = string(rune(0xFEFF))

func writeRaw(t *testing.T, p string, b string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWebFiles_SetSettingStoresAndRefuses(t *testing.T) {
	f := home(t)
	v, err := f.SetSetting("ui.scrollback_lines", json.RawMessage(`5000`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := config.Load(config.ConfigPath())
	if got.UI.ScrollbackLines != 5000 {
		t.Fatalf("stored %d", got.UI.ScrollbackLines)
	}
	found := false
	for _, r := range v.Rows {
		if r.Key == "ui.scrollback_lines" {
			found = r.Value == 5000
		}
	}
	if !found {
		t.Fatal("the answer is not the new view")
	}
	if _, err := f.SetSetting("ui.scrollback_lines", json.RawMessage(`0`)); status(err) != 422 {
		t.Fatalf("0: %v", err)
	}
	if _, err := f.SetSetting("listener.tcp", json.RawMessage(`"0.0.0.0:1"`)); status(err) != 422 {
		t.Fatalf("unknown key: %v", err)
	}
}

// Review Focus 3.
func TestWebFiles_WrongValueTypeIs422(t *testing.T) {
	f := home(t)
	for key, raw := range map[string]string{"update.auto": `"true"`, "ui.scrollback_lines": `5.5`, "logging.level": `3`} {
		if _, err := f.SetSetting(key, json.RawMessage(raw)); status(err) != 422 {
			t.Errorf("%s=%s: %v", key, raw, err)
		}
	}
	if _, err := os.Stat(config.ConfigPath()); !os.IsNotExist(err) {
		t.Fatal("a refused value wrote config.toml")
	}
}

func TestWebFiles_UnparsableConfigIsNeverReplaced(t *testing.T) {
	f := home(t)
	writeRaw(t, config.ConfigPath(), "[ui\nbroken")
	if _, err := f.SetSetting("update.auto", json.RawMessage(`true`)); status(err) != 409 {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(config.ConfigPath()); string(b) != "[ui\nbroken" {
		t.Fatal("an unparsable config.toml was replaced")
	}
	if _, err := f.Settings(); status(err) != 409 {
		t.Fatalf("GET on unparsable: %v", err)
	}
}

// Review Focus 2.
func TestWebFiles_ConcurrentPatchesBothLand(t *testing.T) {
	f := home(t)
	var wg sync.WaitGroup
	for _, kv := range [][2]string{{"ui.scrollback_lines", "4321"}, {"ui.mouse_scroll_lines", "7"}} {
		wg.Add(1)
		go func(k, v string) {
			defer wg.Done()
			if _, err := f.SetSetting(k, json.RawMessage(v)); err != nil {
				t.Error(err)
			}
		}(kv[0], kv[1])
	}
	wg.Wait()
	got, _ := config.Load(config.ConfigPath())
	if got.UI.ScrollbackLines != 4321 || got.UI.MouseScrollLines != 7 {
		t.Fatalf("lost a change: %+v", got.UI)
	}
}

func TestWebFiles_KeysPresetAndPrefix(t *testing.T) {
	f := home(t)
	v, err := f.SetSetting("keys.preset", json.RawMessage(`"tmux"`))
	if err != nil || v.Keys.Preset != "tmux" || !v.Keys.PrefixUsed {
		t.Fatalf("%+v %v", v.Keys, err)
	}
	if _, err := f.SetSetting("keys.prefix", json.RawMessage(`"ctrl+a"`)); err != nil {
		t.Fatal(err)
	}
	b, _ := config.LoadBindings()
	if b.Preset != "tmux" || b.Prefix != "ctrl+a" {
		t.Fatalf("bindings %+v", b)
	}
	if _, err := f.SetSetting("keys.preset", json.RawMessage(`"nope"`)); status(err) != 422 {
		t.Fatalf("unknown preset: %v", err)
	}
}

// Review Focus 5.
func TestWebFiles_KeysRefusedOnUnparsableBindings(t *testing.T) {
	f := home(t)
	writeRaw(t, config.BindingsPath(), "preset = ")
	if _, err := f.SetSetting("keys.preset", json.RawMessage(`"tmux"`)); status(err) != 409 {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(config.BindingsPath()); string(b) != "preset = " {
		t.Fatal("an unparsable bindings.toml was replaced")
	}
	v, err := f.Settings()
	if err != nil || v.Keys.Error == "" {
		t.Fatalf("Settings with broken bindings: %+v %v", v.Keys, err)
	}
}

func TestWebFiles_TemplatesReadMissingGivesTheDefault(t *testing.T) {
	f := home(t)
	tf, err := f.ReadText(webgw.FileTarget{Kind: webgw.TargetTemplates})
	if err != nil || tf.Exists || tf.Hash != "" || tf.Text != config.DefaultTemplatesText() || tf.Default != config.DefaultTemplatesText() {
		t.Fatalf("%+v %v", tf, err)
	}
}

// AC-4 and the CRLF/BOM rule: an unchanged CRLF+BOM file saves with no 409
// and is written back as LF without BOM.
func TestWebFiles_TemplatesCRLFAndBOM(t *testing.T) {
	f := home(t)
	src := bom + strings.ReplaceAll(config.DefaultTemplatesText(), "\n", "\r\n")
	writeRaw(t, config.TemplatesPath(), src)
	tg := webgw.FileTarget{Kind: webgw.TargetTemplates}
	tf, err := f.ReadText(tg)
	if err != nil || tf.Hash != sum([]byte(src)) || strings.Contains(tf.Text, "\r") || strings.HasPrefix(tf.Text, bom) {
		t.Fatalf("read %v", err)
	}
	out, err := f.WriteText(tg, tf.Text, tf.Hash, false)
	if err != nil {
		t.Fatalf("unchanged save: %v", err)
	}
	b, _ := os.ReadFile(config.TemplatesPath())
	if strings.Contains(string(b), "\r") || strings.HasPrefix(string(b), bom) || out.Hash != sum(b) {
		t.Fatal("not written back as LF without BOM")
	}
}

func TestWebFiles_ChangedOnDiskAndForce(t *testing.T) {
	f := home(t)
	tg := webgw.FileTarget{Kind: webgw.TargetTemplates}
	writeRaw(t, config.TemplatesPath(), config.DefaultTemplatesText())
	tf, _ := f.ReadText(tg)
	writeRaw(t, config.TemplatesPath(), config.DefaultTemplatesText()+"\n# theirs\n")
	_, err := f.WriteText(tg, tf.Text+"# mine\n", tf.Hash, false)
	var fe *webgw.FileError
	if !errors.As(err, &fe) || fe.Status != 409 || fe.Current == nil || !strings.Contains(fe.Current.Text, "# theirs") {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := f.WriteText(tg, tf.Text+"# mine\n", tf.Hash, true); err != nil {
		t.Fatalf("force: %v", err)
	}
	if b, _ := os.ReadFile(config.TemplatesPath()); !strings.Contains(string(b), "# mine") {
		t.Fatal("force did not overwrite")
	}
}

// AC-5 and Review Focus 4.
func TestWebFiles_EmptyTemplatesRefused(t *testing.T) {
	f := home(t)
	tg := webgw.FileTarget{Kind: webgw.TargetTemplates}
	writeRaw(t, config.TemplatesPath(), config.DefaultTemplatesText())
	tf, _ := f.ReadText(tg)
	for _, text := range []string{"", bom, "[[templates]\nname="} {
		if _, err := f.WriteText(tg, text, tf.Hash, false); status(err) != 422 {
			t.Errorf("%q: %v", text, err)
		}
	}
	if b, _ := os.ReadFile(config.TemplatesPath()); string(b) != config.DefaultTemplatesText() {
		t.Fatal("a refused text reached the disk")
	}
}

func TestWebFiles_TooLargeIsNotRead(t *testing.T) {
	f := home(t)
	writeRaw(t, config.TemplatesPath(), strings.Repeat("#", webgw.FileTextMax+1))
	if _, err := f.ReadText(webgw.FileTarget{Kind: webgw.TargetTemplates}); status(err) != 413 {
		t.Fatalf("err %v", err)
	}
}

// A file the page could never have loaded is never replaced by an ordinary
// save (AC-4); only Overwrite may replace it.
func TestWebFiles_TooLargeIsNeverOverwrittenWithoutForce(t *testing.T) {
	f := home(t)
	big := strings.Repeat("#", webgw.FileTextMax+1)
	writeRaw(t, config.TemplatesPath(), big)
	tg := webgw.FileTarget{Kind: webgw.TargetTemplates}
	if _, err := f.WriteText(tg, config.DefaultTemplatesText(), "", false); status(err) != 413 {
		t.Fatalf("save over a too-large file: %v", err)
	}
	if b, _ := os.ReadFile(config.TemplatesPath()); string(b) != big {
		t.Fatal("the too-large file was replaced without force")
	}
	if _, err := f.WriteText(tg, config.DefaultTemplatesText(), "", true); err != nil {
		t.Fatalf("force: %v", err)
	}
	if b, _ := os.ReadFile(config.TemplatesPath()); string(b) != config.DefaultTemplatesText() {
		t.Fatal("force did not replace the file")
	}
}

func TestWebFiles_PluginReadWriteAndNames(t *testing.T) {
	f := home(t)
	p := filepath.Join(config.PluginsDir(), "ssh.toml")
	writeRaw(t, p, "[plugin]\r\nname = \"ssh\"\r\n")
	tg := webgw.FileTarget{Kind: webgw.TargetPlugin, Name: "ssh"}
	tf, err := f.ReadText(tg)
	if err != nil || strings.Contains(tf.Text, "\r") {
		t.Fatalf("read %+v %v", tf, err)
	}
	if _, err := f.WriteText(tg, "not = [toml", tf.Hash, false); status(err) != 422 {
		t.Fatalf("bad toml: %v", err)
	}
	if _, err := f.WriteText(tg, "[plugin]\nname = \"ssh\"\ndisplay_name = \"S\"\n", tf.Hash, false); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"..", "a/b", "SSH", strings.Repeat("a", 65), ""} {
		if _, err := f.ReadText(webgw.FileTarget{Kind: webgw.TargetPlugin, Name: bad}); status(err) != 400 {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := f.ReadText(webgw.FileTarget{Kind: webgw.TargetPlugin, Name: "nope"}); status(err) != 404 {
		t.Fatalf("missing plugin: %v", err)
	}
}

// Review Focus 1.
func TestWebFiles_PluginNameIsTheFileName(t *testing.T) {
	f := home(t)
	writeRaw(t, filepath.Join(config.PluginsDir(), "my.toml"), "[plugin]\nname = \"ssh2\"\n")
	if _, err := f.ReadText(webgw.FileTarget{Kind: webgw.TargetPlugin, Name: "ssh2"}); status(err) != 404 {
		t.Fatalf("ssh2: %v", err)
	}
}

// AC-6: a link in the plugins dir is refused, read and write.
func TestWebFiles_PluginLinkRefused(t *testing.T) {
	f := home(t)
	target := filepath.Join(t.TempDir(), "outside.toml")
	writeRaw(t, target, "[plugin]\nname = \"x\"\n")
	if err := os.Symlink(target, filepath.Join(config.PluginsDir(), "x.toml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tg := webgw.FileTarget{Kind: webgw.TargetPlugin, Name: "x"}
	if _, err := f.ReadText(tg); status(err) != 400 {
		t.Fatalf("read: %v", err)
	}
	if _, err := f.WriteText(tg, "[plugin]\n", "", true); status(err) != 400 {
		t.Fatalf("write: %v", err)
	}
}

func TestWebFiles_Migration(t *testing.T) {
	f := home(t)
	if _, err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
		t.Fatal(err)
	}
	cc := filepath.Join(config.PluginsDir(), "claude-code.toml")
	writeRaw(t, cc, "[plugin]\nname = \"claude-code\"\n")
	list, err := f.Migration()
	if err != nil || len(list) != 1 || list[0].Name != "claude-code" || list[0].NeedVersion == 0 {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err := f.ResolveMigration("claude-code", "mine", "[plugin]\nname = \"claude-code\"\nschema_version = 0\n"); status(err) != 422 {
		t.Fatalf("lower version accepted: %v", err)
	}
	left, err := f.ResolveMigration("claude-code", "default", "")
	if err != nil || len(left) != 0 {
		t.Fatalf("default: %+v %v", left, err)
	}
}

func TestWebFiles_MigrationGETNeverWrites(t *testing.T) {
	f := home(t)
	if _, err := f.Migration(); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(config.PluginsDir()); len(ents) != 0 {
		t.Fatalf("GET created %d files", len(ents))
	}
}

func TestWebFiles_Logs(t *testing.T) {
	f := home(t)
	if lt, err := f.LogTail("web"); err != nil || !lt.Missing {
		t.Fatalf("missing: %+v %v", lt, err)
	}
	writeRaw(t, filepath.Join(config.QuilDir(), "web.log"), "hello\n")
	if lt, err := f.LogTail("web"); err != nil || lt.Text != "hello\n" || lt.Missing {
		t.Fatalf("web: %+v %v", lt, err)
	}
	if _, err := f.LogTail("audit"); status(err) != 404 {
		t.Fatalf("audit: %v", err)
	}
}

func TestWebFiles_ExtrasReadConfigPerRequest(t *testing.T) {
	f := home(t)
	if f.ClientExtras().ShowDisclaimer != config.Default().UI.ShowDisclaimer {
		t.Fatal("missing file: not the launch copy")
	}
	if _, err := f.SetSetting("ui.show_disclaimer", json.RawMessage(`false`)); err != nil {
		t.Fatal(err)
	}
	ex := f.ClientExtras()
	if ex.ShowDisclaimer || ex.Tip == nil || ex.Tip.Title == "" {
		t.Fatalf("extras %+v", ex)
	}
}
