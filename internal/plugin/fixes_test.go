package plugin

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// [[error_handlers.fixes]] and the [update] table. Loading goes through the
// registry the way the daemon loads ~/.quil/plugins: a file that fails
// validation is logged and skipped, so Registry.Get returns nil for it. The
// refusal tests check that AND the error text from loadPluginTOML, because
// "skipped" alone cannot tell a refusal from a typo in the fixture's name.

// fixesPluginHead is the part of every fixture that is not under test.
const fixesPluginHead = `
[plugin]
name = "fixme"
display_name = "Fix Me"
category = "tools"

[command]
cmd = "stripe"
args = ["listen"]

[persistence]
strategy = "rerun"
`

// stripeUpdateTable is the spec's [update] table. assets and checksums are
// written as sub-tables rather than the spec's multi-line inline tables: a
// newline inside an inline table is TOML 1.1, which BurntSushi/toml accepts
// only behind an opt-in.
const stripeUpdateTable = `
[update]
notice = 'newer version of the Stripe CLI is available, please update to: v?(\d+\.\d+\.\d+)'
github = "stripe/stripe-cli"
binary = "stripe"

[update.assets]
windows_amd64 = "stripe_{version}_windows_x86_64.zip"
darwin_amd64 = "stripe_{version}_mac-os_x86_64.tar.gz"
darwin_arm64 = "stripe_{version}_mac-os_arm64.tar.gz"
linux_amd64 = "stripe_{version}_linux_x86_64.tar.gz"
linux_arm64 = "stripe_{version}_linux_arm64.tar.gz"

[update.checksums]
windows = "stripe-windows-checksums.txt"
darwin = "stripe-mac-checksums.txt"
linux = "stripe-linux-checksums.txt"

[[update.managers]]
name = "brew"
match = '/Cellar/|/homebrew/|/linuxbrew/'
run = ["brew", "upgrade", "stripe/stripe-cli/stripe"]

[[update.managers]]
name = "scoop"
match = '(?i)\\scoop\\'
run = ["scoop", "update", "stripe"]

[[update.managers]]
name = "winget"
match = '(?i)\\Microsoft\\WinGet\\(Packages|Links)\\'
run = ["winget", "upgrade", "--id", "Stripe.StripeCli"]
`

// loadFixme loads body through a fresh registry and returns the "fixme"
// plugin, nil when the registry skipped it.
func loadFixme(t *testing.T, body string) *PanePlugin {
	t.Helper()
	path := writePluginTOML(t, body)
	r := NewRegistry()
	if err := r.LoadFromDir(filepath.Dir(path)); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	return r.Get("fixme")
}

// assertRefused checks that body fails plugin load for a reason naming
// wantInText, and that the registry therefore does not carry the plugin.
func assertRefused(t *testing.T, body, wantInText string) {
	t.Helper()
	path := writePluginTOML(t, body)
	_, err := loadPluginTOML(path)
	if err == nil {
		t.Fatalf("loadPluginTOML accepted a plugin that must be refused (%s)", wantInText)
	}
	if !strings.Contains(err.Error(), wantInText) {
		t.Errorf("refusal = %q, want it to mention %q", err, wantInText)
	}
	r := NewRegistry()
	if err := r.LoadFromDir(filepath.Dir(path)); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if r.Get("fixme") != nil {
		t.Error("registry loaded a plugin loadPluginTOML refused")
	}
}

func TestLoadFixes_ParsesNestedTables(t *testing.T) {
	p := loadFixme(t, fixesPluginHead+`
[[error_handlers]]
pattern = 'api_key_expired|not logged in'
title = "Stripe login expired"
message = "Log in again to continue."
action = "dialog"

  [[error_handlers.fixes]]
  name = "login"
  label = "Log in"
  run = ["stripe", "login"]

  [[error_handlers.fixes]]
  name = "login-interactive"
  run = ["stripe", "login", "--interactive"]
  then = "stay"
`)
	if p == nil {
		t.Fatal("plugin with fixes not loaded")
	}
	if len(p.ErrorHandlers) != 1 {
		t.Fatalf("ErrorHandlers = %d, want 1", len(p.ErrorHandlers))
	}
	fixes := p.ErrorHandlers[0].Fixes
	if len(fixes) != 2 {
		t.Fatalf("Fixes = %d, want 2", len(fixes))
	}
	if fixes[0].Name != "login" || fixes[0].Label != "Log in" {
		t.Errorf("Fixes[0] = %+v, want name login, label Log in", fixes[0])
	}
	if !slices.Equal(fixes[0].Run, []string{"stripe", "login"}) {
		t.Errorf("Fixes[0].Run = %v, want [stripe login]", fixes[0].Run)
	}
	if fixes[0].Then != FixThenRestart {
		t.Errorf("Fixes[0].Then = %q, want the %q default when then is omitted", fixes[0].Then, FixThenRestart)
	}
	if fixes[1].Then != FixThenStay {
		t.Errorf("Fixes[1].Then = %q, want %q", fixes[1].Then, FixThenStay)
	}
	// A fix with no label is still a button; it reads as its name.
	if fixes[1].Label != "login-interactive" {
		t.Errorf("Fixes[1].Label = %q, want the name as the fallback label", fixes[1].Label)
	}
	// The handler's own pattern still compiles beside its fixes.
	if MatchErrors(p, "FATAL api_key_expired") == nil {
		t.Error("handler with fixes no longer matches its pattern")
	}
}

func TestLoadFixes_ReservedUpdateNameRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[[error_handlers]]
pattern = 'outdated'
action = "dialog"

  [[error_handlers.fixes]]
  name = "update"
  label = "Update"
  run = ["stripe", "update"]
`, "reserved")
}

func TestLoadFixes_EmptyRunRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[[error_handlers]]
pattern = 'not logged in'
action = "dialog"

  [[error_handlers.fixes]]
  name = "login"
  label = "Log in"
  run = []
`, "run")
}

func TestLoadFixes_DuplicateNameRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[[error_handlers]]
pattern = 'not logged in'
action = "dialog"

  [[error_handlers.fixes]]
  name = "login"
  run = ["stripe", "login"]

[[error_handlers]]
pattern = 'api_key_expired'
action = "dialog"

  [[error_handlers.fixes]]
  name = "login"
  run = ["stripe", "login", "--interactive"]
`, "duplicate fix name")
}

func TestLoadFixes_BadThenRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[[error_handlers]]
pattern = 'not logged in'
action = "dialog"

  [[error_handlers.fixes]]
  name = "login"
  run = ["stripe", "login"]
  then = "later"
`, "then")
}

// A plugin without the new tables loads exactly as before: no fixes, no
// update offer. This is every shipped plugin but stripe.
func TestLoadFixes_AbsentTablesAreNil(t *testing.T) {
	p := loadFixme(t, fixesPluginHead+`
[[error_handlers]]
pattern = 'not logged in'
action = "dialog"
`)
	if p == nil {
		t.Fatal("plain plugin not loaded")
	}
	if p.ErrorHandlers[0].Fixes != nil {
		t.Errorf("Fixes = %v, want nil without [[error_handlers.fixes]]", p.ErrorHandlers[0].Fixes)
	}
	if p.Update != nil {
		t.Errorf("Update = %+v, want nil without an [update] table", p.Update)
	}
	if _, ok := MatchUpdateNotice(p, "please update to: v1.55.1"); ok {
		t.Error("a plugin with no [update] table matched an update notice")
	}
}

func TestLoadUpdate_ParsesStripeShape(t *testing.T) {
	p := loadFixme(t, fixesPluginHead+stripeUpdateTable)
	if p == nil {
		t.Fatal("plugin with [update] not loaded")
	}
	u := p.Update
	if u == nil {
		t.Fatal("Update = nil, want the parsed table")
	}
	if u.GitHub != "stripe/stripe-cli" || u.Binary != "stripe" {
		t.Errorf("GitHub/Binary = %q/%q, want stripe/stripe-cli and stripe", u.GitHub, u.Binary)
	}
	if got := u.Assets["windows_amd64"]; got != "stripe_{version}_windows_x86_64.zip" {
		t.Errorf("Assets[windows_amd64] = %q, want stripe_{version}_windows_x86_64.zip", got)
	}
	if len(u.Assets) != 5 {
		t.Errorf("Assets = %d entries, want 5", len(u.Assets))
	}
	if got := u.Checksums["darwin"]; got != "stripe-mac-checksums.txt" {
		t.Errorf("Checksums[darwin] = %q, want stripe-mac-checksums.txt", got)
	}
	if len(u.Managers) != 3 {
		t.Fatalf("Managers = %d, want 3", len(u.Managers))
	}
	for i, want := range []string{"brew", "scoop", "winget"} {
		if u.Managers[i].Name != want {
			t.Errorf("Managers[%d].Name = %q, want %q (file order)", i, u.Managers[i].Name, want)
		}
		if u.Managers[i].Compiled() == nil {
			t.Errorf("Managers[%d] (%s) match did not compile", i, want)
		}
	}
	if !slices.Equal(u.Managers[2].Run, []string{"winget", "upgrade", "--id", "Stripe.StripeCli"}) {
		t.Errorf("Managers[2].Run = %v", u.Managers[2].Run)
	}
	if u.NoticeRe() == nil {
		t.Fatal("notice did not compile")
	}
	// The scoop match is a regex over a Windows path: two TOML backslashes
	// in a literal string are two regex backslashes, one escaped backslash.
	if !u.Managers[1].Compiled().MatchString(`C:\Users\me\scoop\shims\stripe.exe`) {
		t.Error("scoop manager does not match a scoop shim path")
	}
	if u.Managers[0].Compiled().MatchString(`C:\Users\me\scoop\shims\stripe.exe`) {
		t.Error("brew manager matched a scoop path")
	}
}

func TestLoadUpdate_BadNoticeRegexRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[update]
notice = 'please update to: v?([0-9'
`, "notice")
}

func TestLoadUpdate_NoticeWithoutCaptureRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[update]
notice = 'please update to: v?\d+\.\d+\.\d+'
`, "capture group")
}

func TestLoadUpdate_BadManagerMatchRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[update]
notice = 'please update to: v?(\d+\.\d+\.\d+)'

[[update.managers]]
name = "brew"
match = '(/Cellar/'
run = ["brew", "upgrade", "stripe"]
`, "brew")
}

// github, binary, assets and checksums describe ONE download; a table naming
// some of them would load and then fail when the user pressed Update.
func TestLoadUpdate_PartialDownloadFieldsRefused(t *testing.T) {
	assertRefused(t, fixesPluginHead+`
[update]
notice = 'please update to: v?(\d+\.\d+\.\d+)'
github = "stripe/stripe-cli"
binary = "stripe"
`, "assets")
}

// offer_update promises the [update] table's button on the handler's dialog;
// without the table the dialog could not keep it, so the load is refused —
// and with it the flag is carried through.
func TestLoadOfferUpdate_NeedsUpdateTable(t *testing.T) {
	handler := `
[[error_handlers]]
pattern = 'unknown flag: --all-snapshot'
title = "Too old"
message = "Update installs the latest one."
action = "dialog"
offer_update = true
`
	assertRefused(t, fixesPluginHead+handler, "offer_update")
	// A managers-only [update] names no repository to ask for the latest
	// release, so it cannot keep the promise either.
	assertRefused(t, fixesPluginHead+handler+`
[update]
notice = 'please update to: v?(\d+\.\d+\.\d+)'

[[update.managers]]
name = "brew"
match = '/Cellar/'
run = ["brew", "upgrade", "stripe"]
`, "offer_update")

	p, err := loadPluginTOML(writePluginTOML(t, fixesPluginHead+handler+stripeUpdateTable))
	if err != nil {
		t.Fatalf("offer_update with [update] refused: %v", err)
	}
	if len(p.ErrorHandlers) != 1 || !p.ErrorHandlers[0].OfferUpdate {
		t.Fatalf("handlers = %+v, want one with OfferUpdate", p.ErrorHandlers)
	}
	// Absent, the flag is false: a plain handler offers nothing extra.
	p, err = loadPluginTOML(writePluginTOML(t, fixesPluginHead+strings.Replace(handler, "offer_update = true\n", "", 1)+stripeUpdateTable))
	if err != nil {
		t.Fatalf("plain handler refused: %v", err)
	}
	if p.ErrorHandlers[0].OfferUpdate {
		t.Fatal("OfferUpdate defaulted to true")
	}
}

// Managers alone are a complete [update]: a tool that is only ever installed
// through a package manager has no download to describe.
func TestLoadUpdate_ManagersOnlyIsComplete(t *testing.T) {
	p := loadFixme(t, fixesPluginHead+`
[update]
notice = 'please update to: v?(\d+\.\d+\.\d+)'

[[update.managers]]
name = "brew"
match = '/Cellar/'
run = ["brew", "upgrade", "stripe"]
`)
	if p == nil || p.Update == nil {
		t.Fatal("managers-only [update] was refused")
	}
	if p.Update.Assets != nil || p.Update.Checksums != nil {
		t.Errorf("Assets/Checksums = %v/%v, want nil when absent", p.Update.Assets, p.Update.Checksums)
	}
}

func TestMatchErrors_ReturnsAllInOrder(t *testing.T) {
	p := &PanePlugin{
		ErrorHandlers: []ErrorHandler{
			{Pattern: `api_key_expired`, Title: "Login expired", Action: "dialog"},
			{Pattern: `Host key verification failed`, Title: "Unknown host", Action: "dialog"},
			{Pattern: `Expired API Key`, Title: "Key expired", Action: "log"},
		},
	}
	compilePatterns(p)

	line := `FATAL Expired API Key provided, "code": "api_key_expired"`
	matched := MatchErrors(p, line)
	if len(matched) != 2 {
		t.Fatalf("MatchErrors = %d handlers, want 2", len(matched))
	}
	if matched[0].Title != "Login expired" || matched[1].Title != "Key expired" {
		t.Errorf("MatchErrors order = [%s, %s], want file order [Login expired, Key expired]",
			matched[0].Title, matched[1].Title)
	}
	// The pointers are into the plugin, not copies: the daemon reads Fixes
	// off them.
	if matched[0] != &p.ErrorHandlers[0] {
		t.Error("MatchErrors returned a copy rather than the plugin's handler")
	}
	if got := MatchErrors(p, "all good"); got != nil {
		t.Errorf("MatchErrors on a clean line = %v, want nil", got)
	}
	if got := MatchErrors(nil, line); got != nil {
		t.Errorf("MatchErrors(nil) = %v, want nil", got)
	}
}

func TestMatchUpdateNotice_CapturesVersion(t *testing.T) {
	p := loadFixme(t, fixesPluginHead+stripeUpdateTable)
	if p == nil {
		t.Fatal("plugin with [update] not loaded")
	}
	v, ok := MatchUpdateNotice(p, "A newer version of the Stripe CLI is available, please update to: v1.55.1")
	if !ok || v != "1.55.1" {
		t.Errorf("MatchUpdateNotice = %q, %v; want 1.55.1, true", v, ok)
	}
	if v, ok := MatchUpdateNotice(p, "Ready! You are using Stripe API Version [2024-06-20]"); ok {
		t.Errorf("MatchUpdateNotice matched a ready line with %q", v)
	}
	if _, ok := MatchUpdateNotice(nil, "please update to: v1.55.1"); ok {
		t.Error("MatchUpdateNotice(nil) reported a match")
	}
}

func TestFindFix_SearchesEveryHandler(t *testing.T) {
	p := loadFixme(t, fixesPluginHead+`
[[error_handlers]]
pattern = 'not logged in'
action = "dialog"

  [[error_handlers.fixes]]
  name = "login"
  run = ["stripe", "login"]

[[error_handlers]]
pattern = 'config missing'
action = "dialog"

  [[error_handlers.fixes]]
  name = "config"
  run = ["stripe", "config", "--list"]
  then = "stay"
`)
	if p == nil {
		t.Fatal("plugin not loaded")
	}
	f, ok := p.FindFix("config")
	if !ok || f == nil {
		t.Fatal("FindFix did not reach the second handler's fix")
	}
	if f.Then != FixThenStay || !slices.Equal(f.Run, []string{"stripe", "config", "--list"}) {
		t.Errorf("FindFix(config) = %+v", *f)
	}
	if f != &p.ErrorHandlers[1].Fixes[0] {
		t.Error("FindFix returned a copy rather than the plugin's fix")
	}
	if _, ok := p.FindFix(FixNameUpdate); ok {
		t.Error("FindFix found a handler fix under the reserved update name")
	}
	if _, ok := p.FindFix("nope"); ok {
		t.Error("FindFix found a fix that does not exist")
	}
	var none *PanePlugin
	if _, ok := none.FindFix("login"); ok {
		t.Error("FindFix on a nil plugin reported a fix")
	}
}
