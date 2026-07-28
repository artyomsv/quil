package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// releaseFixture mirrors the real GitHub /releases/latest response shape
// with an obviously-synthetic version (never a plausible real release).
const releaseFixture = `{
  "tag_name": "v0.0.2",
  "html_url": "https://github.com/artyomsv/quil/releases/tag/v0.0.2",
  "assets": [
    {"name": "checksums.txt", "browser_download_url": "https://example.invalid/checksums.txt", "size": 300},
    {"name": "quil_0.0.2_windows_amd64.zip", "browser_download_url": "https://example.invalid/win.zip", "size": 1000},
    {"name": "quil_0.0.2_linux_amd64.tar.gz", "browser_download_url": "https://example.invalid/linux.tgz", "size": 900}
  ]
}`

func TestChecker_Latest_ParsesRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/artyomsv/quil/releases/latest" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(releaseFixture))
	}))
	defer srv.Close()

	c := &Checker{BaseURL: srv.URL}
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.TagName != "v0.0.2" {
		t.Errorf("TagName = %q, want v0.0.2", rel.TagName)
	}
	if rel.Version() != "0.0.2" {
		t.Errorf("Version() = %q, want 0.0.2", rel.Version())
	}
	if len(rel.Assets) != 3 {
		t.Fatalf("len(Assets) = %d, want 3", len(rel.Assets))
	}
}

func TestChecker_Latest_Non200_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // rate-limited
	}))
	defer srv.Close()
	if _, err := (&Checker{BaseURL: srv.URL}).Latest(context.Background()); err == nil {
		t.Fatal("Latest on 403 = nil error, want error")
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"windows", "amd64", "quil_0.0.2_windows_amd64.zip"},
		{"linux", "amd64", "quil_0.0.2_linux_amd64.tar.gz"},
		{"linux", "arm64", "quil_0.0.2_linux_arm64.tar.gz"},
		{"darwin", "arm64", "quil_0.0.2_darwin_arm64.tar.gz"},
	}
	for _, tc := range cases {
		if got := AssetName("0.0.2", tc.goos, tc.goarch); got != tc.want {
			t.Errorf("AssetName(0.0.2, %s, %s) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestFindAssets(t *testing.T) {
	rel := &Release{TagName: "v0.0.2", Assets: []Asset{
		{Name: "checksums.txt"},
		{Name: "quil_0.0.2_windows_amd64.zip"},
	}}
	archive, sums, err := FindAssets(rel, "windows", "amd64")
	if err != nil {
		t.Fatalf("FindAssets: %v", err)
	}
	if archive.Name != "quil_0.0.2_windows_amd64.zip" || sums.Name != "checksums.txt" {
		t.Errorf("got archive=%q sums=%q", archive.Name, sums.Name)
	}
	if _, _, err := FindAssets(rel, "linux", "amd64"); err == nil {
		t.Error("FindAssets for missing platform = nil error, want error")
	}
}

// The tag becomes a URL path segment, so it is validated before the request is
// built. Rejecting rather than escaping: no real tag contains these, and a
// permissive reading would let a caller redirect the request.
func TestChecker_Release_RejectsUnsafeTags(t *testing.T) {
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()

	tags := []string{
		"1.0.0/../../../etc",
		"../1.0.0",
		"1.0.0?foo=bar",
		"1.0.0#frag",
		"1.0.0%2f",
		`1.0.0\evil`,
	}
	c := &Checker{BaseURL: srv.URL}
	for _, tag := range tags {
		t.Run(tag, func(t *testing.T) {
			if _, err := c.Release(context.Background(), tag); err == nil {
				t.Errorf("Release(%q) error = nil, want rejection", tag)
			}
		})
	}
	if len(requested) != 0 {
		t.Errorf("a rejected tag still reached the network: %v", requested)
	}
}

func TestChecker_Release_ByTag(t *testing.T) {
	tests := []struct {
		name, tag, wantPath string
	}{
		{"bare version", "1.43.1", "/repos/artyomsv/quil/releases/tags/v1.43.1"},
		{"already prefixed", "v1.43.1", "/repos/artyomsv/quil/releases/tags/v1.43.1"},
		// Empty means latest — the endpoint Latest() uses.
		{"empty means latest", "", "/repos/artyomsv/quil/releases/latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Write([]byte(`{"tag_name":"v1.43.1"}`))
			}))
			defer srv.Close()

			rel, err := (&Checker{BaseURL: srv.URL}).Release(context.Background(), tt.tag)
			if err != nil {
				t.Fatalf("Release error = %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("requested %q, want %q", gotPath, tt.wantPath)
			}
			if rel.Version() != "1.43.1" {
				t.Errorf("Version = %q", rel.Version())
			}
		})
	}
}
