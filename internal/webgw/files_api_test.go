package webgw

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type fakeFiles struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeFiles) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeFiles) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeFiles) Settings() (SettingsView, error) {
	f.rec("settings")
	return SettingsView{Rows: []SettingRow{{Key: "ui.scrollback_lines", Value: 1}}}, f.err
}
func (f *fakeFiles) SetSetting(key string, v json.RawMessage) (SettingsView, error) {
	f.rec("set " + key + "=" + string(v))
	return SettingsView{}, f.err
}
func (f *fakeFiles) ReadText(t FileTarget) (TextFile, error) {
	f.rec("read " + t.Kind + "/" + t.Name)
	return TextFile{Text: "x", Hash: "h"}, f.err
}
func (f *fakeFiles) WriteText(t FileTarget, text, base string, force bool) (TextFile, error) {
	f.rec("write " + t.Kind + "/" + t.Name)
	return TextFile{Text: text, Hash: "h2"}, f.err
}
func (f *fakeFiles) Migration() ([]StaleFile, error) { f.rec("migration"); return nil, f.err }
func (f *fakeFiles) ResolveMigration(name, choice, text string) ([]StaleFile, error) {
	f.rec("resolve " + name + " " + choice)
	return []StaleFile{}, f.err
}
func (f *fakeFiles) LogTail(name string) (LogText, error) {
	f.rec("log " + name)
	return LogText{Text: "t"}, f.err
}

func newFilesHarness(t *testing.T, rights string, files Files) *wsHarness {
	t.Helper()
	h := newWSHarness(t, func(s *Server) { s.cfg.Files = files })
	h.rights = rights
	return h
}

var fileRoutes = []struct {
	method, path string
	body         any
	standardOK   bool
}{
	{http.MethodGet, "/api/settings", nil, true},
	{http.MethodPatch, "/api/settings", map[string]any{"key": "ui.scrollback_lines", "value": 5}, false},
	{http.MethodGet, "/api/files/templates", nil, false},
	{http.MethodPut, "/api/files/templates", map[string]any{"text": "x", "base_hash": "", "force": false}, false},
	{http.MethodGet, "/api/files/plugins/ssh", nil, false},
	{http.MethodPut, "/api/files/plugins/ssh", map[string]any{"text": "x", "base_hash": "h", "force": false}, false},
	{http.MethodGet, "/api/migration", nil, false},
	{http.MethodPost, "/api/migration/ssh", map[string]any{"choice": "default"}, false},
	{http.MethodGet, "/api/logs/web", nil, false},
}

// AC-2: below full no route writes or returns file or log text; standard
// may read the settings values.
func TestFilesAPI_Rights(t *testing.T) {
	for _, rights := range []string{"read-only", "standard", "full", "no-tab"} {
		ff := &fakeFiles{}
		dial := rights
		if rights == "no-tab" {
			dial = "full"
		}
		h := newFilesHarness(t, dial, ff)
		s := h.login()
		if rights != "no-tab" {
			h.liveTab(s)
		}
		for _, rt := range fileRoutes {
			resp := h.api(rt.method, rt.path, s, rt.body, nil)
			want := http.StatusForbidden
			if rights == "full" || (rights == "standard" && rt.standardOK) {
				want = http.StatusOK
			}
			if resp.StatusCode != want {
				t.Errorf("%s %s %s: %d, want %d", rights, rt.method, rt.path, resp.StatusCode, want)
				continue
			}
			if want == http.StatusForbidden {
				var b map[string]string
				_ = json.NewDecoder(resp.Body).Decode(&b)
				if b["error"] != "needs full rights" {
					t.Errorf("%s %s: body %v", rights, rt.path, b)
				}
			}
		}
		if rights != "full" && rights != "standard" && len(ff.called()) != 0 {
			t.Errorf("%s reached Files: %v", rights, ff.called())
		}
	}
}

func TestFilesAPI_AuthChecks(t *testing.T) {
	ff := &fakeFiles{}
	h := newFilesHarness(t, "full", ff)
	s := h.login()
	h.liveTab(s)
	for _, rt := range fileRoutes {
		if got := h.api(rt.method, rt.path, s, rt.body, func(r *http.Request) { r.Header.Del(APIKeyHeader) }).StatusCode; got != http.StatusUnauthorized {
			t.Errorf("%s %s without key: %d", rt.method, rt.path, got)
		}
		if got := h.api(rt.method, rt.path, s, rt.body, func(r *http.Request) { r.Header.Set("Origin", "http://evil.test") }).StatusCode; got != http.StatusForbidden {
			t.Errorf("%s %s foreign origin: %d", rt.method, rt.path, got)
		}
	}
	if len(ff.called()) != 0 {
		t.Fatalf("a refused request reached Files: %v", ff.called())
	}
}

func TestFilesAPI_NilFilesIs404(t *testing.T) {
	h := newFilesHarness(t, "full", nil)
	s := h.login()
	h.liveTab(s)
	for _, rt := range fileRoutes {
		if got := h.api(rt.method, rt.path, s, rt.body, nil).StatusCode; got != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", rt.method, rt.path, got)
		}
	}
}

func TestFilesAPI_BodyCapsAre413(t *testing.T) {
	ff := &fakeFiles{}
	h := newFilesHarness(t, "full", ff)
	s := h.login()
	h.liveTab(s)
	big := strings.Repeat("a", 2<<20)
	if got := h.api(http.MethodPut, "/api/files/templates", s, map[string]any{"text": big}, nil).StatusCode; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT 2 MiB: %d", got)
	}
	if got := h.api(http.MethodPatch, "/api/settings", s, map[string]any{"key": "k", "value": strings.Repeat("v", 100<<10)}, nil).StatusCode; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("PATCH 100 KiB: %d", got)
	}
	// Under the body cap but over the text cap: 413 too.
	over := strings.Repeat("a", FileTextMax+1)
	if got := h.api(http.MethodPut, "/api/files/templates", s, map[string]any{"text": over}, nil).StatusCode; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT text over cap: %d", got)
	}
	if len(ff.called()) != 0 {
		t.Fatalf("an oversized body reached Files: %v", ff.called())
	}
}

func TestFilesAPI_NamesAndMethods(t *testing.T) {
	ff := &fakeFiles{}
	h := newFilesHarness(t, "full", ff)
	s := h.login()
	h.liveTab(s)
	for _, p := range []string{"/api/files/plugins/A", "/api/files/plugins/a.b", "/api/files/plugins/" + strings.Repeat("a", 65), "/api/files/plugins/", "/api/logs/audit", "/api/migration/A"} {
		method := http.MethodGet
		if strings.HasPrefix(p, "/api/migration/") {
			method = http.MethodPost
		}
		got := h.api(method, p, s, map[string]any{"choice": "default"}, nil).StatusCode
		if got != http.StatusBadRequest && got != http.StatusNotFound {
			t.Errorf("%s: %d", p, got)
		}
	}
	if got := h.api(http.MethodDelete, "/api/settings", s, nil, nil).StatusCode; got != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/settings: %d", got)
	}
	if len(ff.called()) != 0 {
		t.Fatalf("a bad name reached Files: %v", ff.called())
	}
}

func TestFilesAPI_FileErrorsMapToStatus(t *testing.T) {
	cases := []struct {
		err    error
		status int
		text   string
	}{
		{Conflict(TextFile{Text: "theirs", Hash: "h9"}), 409, "changed on disk"},
		{ErrSettingsUnparsable, 409, "settings file does not parse; fix it in an editor"},
		{Invalid("line 3: bad"), 422, "line 3: bad"},
		{ErrTooLarge, 413, "too large to edit in the browser; use the TUI"},
		{ErrNotFound, 404, "not found"},
		{http.ErrBodyNotAllowed, 500, "failed; see web.log"},
	}
	for _, c := range cases {
		ff := &fakeFiles{err: c.err}
		h := newFilesHarness(t, "full", ff)
		s := h.login()
		h.liveTab(s)
		resp := h.api(http.MethodPut, "/api/files/templates", s, map[string]any{"text": "x", "base_hash": "h"}, nil)
		var b struct {
			Error   string    `json:"error"`
			Current *TextFile `json:"current"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&b)
		if resp.StatusCode != c.status || b.Error != c.text {
			t.Errorf("%v: %d %q", c.err, resp.StatusCode, b.Error)
		}
		if c.status == 409 && c.text == "changed on disk" && (b.Current == nil || b.Current.Hash != "h9") {
			t.Errorf("conflict without the current file: %+v", b.Current)
		}
	}
}

func TestPluginNameOK(t *testing.T) {
	for name, want := range map[string]bool{"ssh": true, "claude-code": true, "a_1": true, "": false, "A": false, "a.b": false, "..": false, "a/b": false, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false} {
		if PluginNameOK(name) != want {
			t.Errorf("PluginNameOK(%q) != %v", name, want)
		}
	}
}
