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
