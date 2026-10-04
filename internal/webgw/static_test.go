package webgw

import (
	"net/http/httptest"
	"strings"
	"testing"
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
