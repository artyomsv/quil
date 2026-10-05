package webgw

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/instances"
)

func newAPIHarness(t *testing.T, rights string) (*wsHarness, string) {
	t.Helper()
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0o700); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, plugins, "e2e-ssh.toml", testPluginTOML)
	inst := filepath.Join(dir, "instances.json")
	h := newWSHarness(t, func(s *Server) {
		s.cfg.PluginsDir = plugins
		s.cfg.InstancesPath = inst
		s.cfg.ClientExtras = func() ClientExtras { return ClientExtras{SandboxSignIn: "browser", SandboxImage: "img:1"} }
		s.catalog = newCatalog(plugins)
	})
	h.rights = rights
	return h, inst
}

func (h *wsHarness) api(method, path string, s session, body any, mut func(*http.Request)) *http.Response {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rdr)
	req.Header.Set("Cookie", SessionCookie+"="+s.cookie)
	req.Header.Set(APIKeyHeader, s.key)
	if method != http.MethodGet {
		req.Header.Set("Origin", h.origin())
		req.Header.Set("Content-Type", "application/json")
	}
	if mut != nil {
		mut(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// liveTab opens one tab so the session holds daemon rights.
func (h *wsHarness) liveTab(s session) {
	h.t.Helper()
	h.open(testCtx(h.t), s, "")
}

func sshBody(name string) map[string]any {
	return map[string]any{"plugin": "e2e-ssh", "name": name, "fields": map[string]string{"name": name, "host": "h"}}
}

func TestAPI_Checks(t *testing.T) {
	h, _ := newAPIHarness(t, "full")
	s := h.login()
	h.liveTab(s)
	other := h.login() // a second live session: its key must not open the first's
	cases := []struct {
		name   string
		method string
		mut    func(*http.Request)
		want   int
	}{
		{"get ok without origin", http.MethodGet, nil, 200},
		{"get with this origin", http.MethodGet, func(r *http.Request) { r.Header.Set("Origin", h.origin()) }, 200},
		{"get with a foreign origin", http.MethodGet, func(r *http.Request) { r.Header.Set("Origin", "http://evil.test") }, 403},
		{"cross-site fetch metadata", http.MethodGet, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"same-site is not same-origin", http.MethodGet, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, 403},
		{"same-origin fetch metadata", http.MethodGet, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, 200},
		{"typed into the address bar", http.MethodGet, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }, 200},
		{"no key", http.MethodGet, func(r *http.Request) { r.Header.Del(APIKeyHeader) }, 401},
		{"wrong key", http.MethodGet, func(r *http.Request) { r.Header.Set(APIKeyHeader, s.key+"x") }, 401},
		{"another session's key", http.MethodGet, func(r *http.Request) { r.Header.Set(APIKeyHeader, other.key) }, 401},
		{"no cookie", http.MethodGet, func(r *http.Request) { r.Header.Del("Cookie") }, 401},
		{"foreign host", http.MethodGet, func(r *http.Request) { r.Host = "evil.test" }, 403},
		{"post ok", http.MethodPost, nil, 201},
		{"post without origin", http.MethodPost, func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"post with a foreign origin", http.MethodPost, func(r *http.Request) { r.Header.Set("Origin", "http://evil.test") }, 403},
		{"post as a form", http.MethodPost, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"post without key", http.MethodPost, func(r *http.Request) { r.Header.Del(APIKeyHeader) }, 401},
		// The exact Origin does not excuse a cross-site fetch: both are checked.
		{"post with this origin but cross-site fetch metadata", http.MethodPost, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"post same-site", http.MethodPost, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := "/api/client"
			var body any
			if c.method == http.MethodPost {
				path = "/api/instances"
				body = sshBody("x")
			}
			if got := h.api(c.method, path, s, body, c.mut).StatusCode; got != c.want {
				t.Fatalf("status %d, want %d", got, c.want)
			}
		})
	}
	if got := h.api(http.MethodPost, "/api/client", s, nil, nil).StatusCode; got != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/client: %d, want 405", got)
	}
	if got := h.api(http.MethodGet, "/api/instances", s, nil, nil).StatusCode; got != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/instances: %d, want 405", got)
	}
}

func TestAPI_ClientReturnsCatalogInstancesAndRights(t *testing.T) {
	h, inst := newAPIHarness(t, "standard")
	if err := instances.Save(inst, instances.Store{"e2e-ssh": {{ID: "i1", Name: "box", Fields: map[string]string{"host": "h"}}}}); err != nil {
		t.Fatal(err)
	}
	s := h.login()
	h.liveTab(s)
	resp := h.api(http.MethodGet, "/api/client", s, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var ci ClientInfo
	if err := json.NewDecoder(resp.Body).Decode(&ci); err != nil {
		t.Fatal(err)
	}
	if ci.Rights != "standard" || ci.Sandbox.SignInDefault != "browser" || ci.Sandbox.ImageDefault != "img:1" {
		t.Fatalf("rights/sandbox = %q %+v", ci.Rights, ci.Sandbox)
	}
	if len(ci.Instances["e2e-ssh"]) != 1 || ci.Instances["e2e-ssh"][0].ID != "i1" {
		t.Fatalf("instances = %+v", ci.Instances)
	}
	if findDef(ci.Plugins, "e2e-ssh") == nil || findDef(ci.Plugins, "terminal") == nil {
		t.Fatalf("plugins = %+v", ci.Plugins)
	}
	if len(ci.Categories) == 0 || ci.Categories[0].Key != "terminal" {
		t.Fatalf("categories = %+v", ci.Categories)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("api answers must not be cached")
	}
}

// With no tab open the session holds no daemon rights; the client info says
// so ("") rather than inventing a level.
func TestAPI_ClientWithoutATabHasNoRights(t *testing.T) {
	h, _ := newAPIHarness(t, "full")
	s := h.login()
	resp := h.api(http.MethodGet, "/api/client", s, nil, nil)
	var ci ClientInfo
	if err := json.NewDecoder(resp.Body).Decode(&ci); err != nil {
		t.Fatal(err)
	}
	if ci.Rights != "" {
		t.Fatalf("rights %q, want none", ci.Rights)
	}
}

func TestAPI_InstancesCreateEditDelete(t *testing.T) {
	h, inst := newAPIHarness(t, "standard") // standard may save instances (spec §4.2)
	s := h.login()
	h.liveTab(s)
	resp := h.api(http.MethodPost, "/api/instances", s, map[string]any{
		"plugin": "e2e-ssh", "name": "box", "fields": map[string]string{"name": "box", "host": "h", "user": "u"},
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	var made instances.Saved
	_ = json.NewDecoder(resp.Body).Decode(&made)
	if len(made.ID) != 8 {
		t.Fatalf("id %q, want 8 hex chars", made.ID)
	}
	if got := h.api(http.MethodPut, "/api/instances", s, map[string]any{
		"plugin": "e2e-ssh", "id": made.ID, "name": "box2", "fields": map[string]string{"name": "box2", "host": "h2"},
	}, nil).StatusCode; got != http.StatusOK {
		t.Fatalf("edit: %d", got)
	}
	st, _ := instances.Load(inst)
	if st["e2e-ssh"][0].Fields["host"] != "h2" || st["e2e-ssh"][0].Name != "box2" {
		t.Fatalf("edit not stored: %+v", st)
	}
	if got := h.api(http.MethodPut, "/api/instances", s, map[string]any{
		"plugin": "e2e-ssh", "id": "nope", "name": "x", "fields": map[string]string{"name": "x", "host": "h"},
	}, nil).StatusCode; got != http.StatusNotFound {
		t.Fatalf("edit of an unknown id: %d, want 404", got)
	}
	if got := h.api(http.MethodDelete, "/api/instances?plugin=e2e-ssh&id="+made.ID, s, nil, nil).StatusCode; got != http.StatusNoContent {
		t.Fatalf("delete: %d", got)
	}
	st, _ = instances.Load(inst)
	if len(st["e2e-ssh"]) != 0 {
		t.Fatalf("not deleted: %+v", st)
	}
	if got := h.api(http.MethodDelete, "/api/instances?plugin=e2e-ssh&id="+made.ID, s, nil, nil).StatusCode; got != http.StatusNotFound {
		t.Fatalf("second delete: %d, want 404", got)
	}
}

func TestAPI_InstancesRefusals(t *testing.T) {
	h, inst := newAPIHarness(t, "read-only")
	if err := os.WriteFile(inst, []byte(`{"e2e-ssh":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(inst)
	s := h.login()
	h.liveTab(s)
	if got := h.api(http.MethodPost, "/api/instances", s, sshBody("x"), nil).StatusCode; got != http.StatusForbidden {
		t.Fatalf("read-only write: %d, want 403", got)
	}
	if got := h.api(http.MethodDelete, "/api/instances?plugin=e2e-ssh&id=x", s, nil, nil).StatusCode; got != http.StatusForbidden {
		t.Fatalf("read-only delete: %d, want 403", got)
	}
	after, _ := os.ReadFile(inst)
	if !bytes.Equal(before, after) {
		t.Fatal("a refused write changed the file")
	}

	h2, inst2 := newAPIHarness(t, "full")
	s2 := h2.login() // no tab open: no daemon rights held for this session
	if got := h2.api(http.MethodPost, "/api/instances", s2, sshBody("x"), nil).StatusCode; got != http.StatusForbidden {
		t.Fatalf("no live tab: %d, want 403", got)
	}
	h2.liveTab(s2)
	for name, b := range map[string]any{
		"unknown plugin":   map[string]any{"plugin": "nope", "name": "x", "fields": map[string]string{}},
		"plugin w/o form":  map[string]any{"plugin": "terminal", "name": "x", "fields": map[string]string{}},
		"unknown field":    map[string]any{"plugin": "e2e-ssh", "name": "x", "fields": map[string]string{"evil": "1"}},
		"long value":       map[string]any{"plugin": "e2e-ssh", "name": "x", "fields": map[string]string{"host": strings.Repeat("h", 2000)}},
		"no name":          map[string]any{"plugin": "e2e-ssh", "name": "", "fields": map[string]string{"host": "h"}},
		"required missing": map[string]any{"plugin": "e2e-ssh", "name": "x", "fields": map[string]string{"name": "x"}},
		"required blank":   map[string]any{"plugin": "e2e-ssh", "name": "x", "fields": map[string]string{"name": "x", "host": "  "}},
		"edit without id":  map[string]any{"plugin": "e2e-ssh", "name": "x", "fields": map[string]string{"host": "h"}, "put": true},
	} {
		method := http.MethodPost
		if m, ok := b.(map[string]any); ok && m["put"] == true {
			method = http.MethodPut
			delete(m, "put")
		}
		if got := h2.api(method, "/api/instances", s2, b, nil).StatusCode; got != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, got)
		}
	}
	if _, err := os.Stat(inst2); !os.IsNotExist(err) {
		t.Fatalf("a refused write created the file: %v", err)
	}
}

// A revoked token closes the daemon connection; the tab goes, and with it
// the rights the session held. Writes are refused from then on.
func TestAPI_InstancesRefusedAfterTheDaemonClosed(t *testing.T) {
	h, _ := newAPIHarness(t, "full")
	s := h.login()
	h.liveTab(s)
	if got := h.api(http.MethodPost, "/api/instances", s, sshBody("a"), nil).StatusCode; got != http.StatusCreated {
		t.Fatalf("before the close: %d", got)
	}
	_ = h.daemon(0).Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := h.api(http.MethodPost, "/api/instances", s, sshBody("b"), nil).StatusCode
		if got == http.StatusForbidden {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %d after the daemon closed, want 403", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The lowest level among the session's tabs is what it holds.
func TestAPI_SessionRightsIsTheLowestTab(t *testing.T) {
	h, _ := newAPIHarness(t, "full")
	s := h.login()
	h.liveTab(s)
	h.mu.Lock()
	h.rights = "read-only"
	h.mu.Unlock()
	h.liveTab(s)
	if got := h.s.sessionRights(s.cookie); got != "read-only" {
		t.Fatalf("rights %q, want read-only", got)
	}
	if got := h.api(http.MethodPost, "/api/instances", s, sshBody("x"), nil).StatusCode; got != http.StatusForbidden {
		t.Fatalf("write: %d, want 403", got)
	}
}

func TestAPI_InstanceWritesAreSerialized(t *testing.T) {
	h, inst := newAPIHarness(t, "full")
	s := h.login()
	h.liveTab(s)
	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := json.Marshal(sshBody("n"))
			req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/instances", bytes.NewReader(b))
			req.Header.Set("Cookie", SessionCookie+"="+s.cookie)
			req.Header.Set(APIKeyHeader, s.key)
			req.Header.Set("Origin", h.origin())
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				codes <- 0
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusCreated {
			t.Fatalf("a write answered %d", c)
		}
	}
	st, err := instances.Load(inst)
	if err != nil || len(st["e2e-ssh"]) != 20 {
		t.Fatalf("got %d instances (%v), want 20: a lost update means writes were not serialized", len(st["e2e-ssh"]), err)
	}
}

func TestAPI_MalformedFileIsNeverOverwritten(t *testing.T) {
	h, inst := newAPIHarness(t, "full")
	if err := os.WriteFile(inst, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := h.login()
	h.liveTab(s)
	resp := h.api(http.MethodPost, "/api/instances", s, sshBody("n"), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	if b, _ := os.ReadFile(inst); string(b) != "{broken" {
		t.Fatal("the malformed file was replaced")
	}
	// The list still answers, empty: a reader never fails on a bad file.
	var ci ClientInfo
	if err := json.NewDecoder(h.api(http.MethodGet, "/api/client", s, nil, nil).Body).Decode(&ci); err != nil {
		t.Fatal(err)
	}
	if len(ci.Instances) != 0 {
		t.Fatalf("instances = %+v", ci.Instances)
	}
}
