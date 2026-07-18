// Package update implements Quil's auto-update pipeline: a GitHub release
// checker, a download/verify/stage step, and the small state files shared
// between daemon (writer of state.json) and TUI (writer of notified.json).
// Stdlib-only by design — both binaries import it.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the GitHub API host; tests override Checker.BaseURL.
const DefaultBaseURL = "https://api.github.com"

// latestReleasePath is the repo-specific /releases/latest endpoint.
const latestReleasePath = "/repos/artyomsv/quil/releases/latest"

// requestTimeout bounds every update HTTP request. No retry — callers run
// on a daily ticker, so the next tick is the retry.
const requestTimeout = 10 * time.Second

// maxJSONBody caps the release-JSON response read (defense against a
// hostile/broken endpoint streaming forever).
const maxJSONBody = 1 << 20 // 1 MiB

// Asset is one downloadable file attached to a GitHub release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// Release is the subset of the GitHub release JSON the pipeline needs.
type Release struct {
	TagName string  `json:"tag_name"`
	URL     string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Version returns the tag without the leading "v" ("v1.37.0" → "1.37.0"),
// matching the version GoReleaser embeds in asset names and ldflags.
func (r *Release) Version() string {
	return strings.TrimPrefix(r.TagName, "v")
}

// Checker fetches the latest published release.
type Checker struct {
	BaseURL string       // DefaultBaseURL when empty; tests point it at httptest
	Client  *http.Client // nil → 10 s-timeout default
}

func (c *Checker) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: requestTimeout}
}

// Latest fetches the newest published release. One GET, no retry.
func (c *Checker) Latest(ctx context.Context) (*Release, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+latestReleasePath, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("latest release: unexpected status %s", resp.Status)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release JSON: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release JSON missing tag_name")
	}
	return &rel, nil
}

// AssetName returns the archive name GoReleaser produces for a platform
// (name_template "quil_{{ .Version }}_{{ .Os }}_{{ .Arch }}"; zip on
// windows, tar.gz elsewhere). Version carries no leading "v".
func AssetName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "quil_" + version + "_" + goos + "_" + goarch + ext
}

// FindAssets locates the platform archive and the checksums.txt asset in a
// release. Both must be present — a release without either cannot be
// verified and is not staged.
func FindAssets(rel *Release, goos, goarch string) (archive, checksums *Asset, err error) {
	want := AssetName(rel.Version(), goos, goarch)
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case want:
			archive = &rel.Assets[i]
		case "checksums.txt":
			checksums = &rel.Assets[i]
		}
	}
	if archive == nil {
		return nil, nil, fmt.Errorf("release %s has no asset %q", rel.TagName, want)
	}
	if checksums == nil {
		return nil, nil, fmt.Errorf("release %s has no checksums.txt", rel.TagName)
	}
	return archive, checksums, nil
}
