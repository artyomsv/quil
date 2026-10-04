package webgw

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// CI's Go job builds with only the placeholder, so the no-UI path is the one
// it exercises: "/" must explain, and every other path must 404 rather than
// list the embed directory.
func TestStaticHandler_WithoutUI(t *testing.T) {
	if HasUI() {
		t.Skip("this binary has the web UI built in")
	}
	rec := httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "no web UI") {
		t.Fatalf("/ = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	StaticHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/.keep", nil))
	if rec.Code != 404 {
		t.Fatalf("/.keep = %d, want 404", rec.Code)
	}
}

// With the UI built in, a directory is never listed: /assets/ answers 404
// while the files in it are served.
func TestFilesOnly_DirectoriesAre404(t *testing.T) {
	h := filesOnly(fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	})
	for path, want := range map[string]int{
		"/":              200,
		"/assets/":       404,
		"/assets":        404,
		"/assets/./":     404,
		"/assets/app.js": 200,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s = %d, want %d", path, rec.Code, want)
		}
		if want == 404 && strings.Contains(rec.Body.String(), "app.js") {
			t.Errorf("%s listed the directory: %q", path, rec.Body.String())
		}
	}
}
