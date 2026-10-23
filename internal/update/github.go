// Package update implements Quil's auto-update pipeline: a GitHub release
// checker, a download/verify/stage step, and the small state files shared
// between daemon (writer of state.json) and TUI (writer of notified.json).
// Stdlib-only by design — both binaries import it.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the GitHub API host; tests override Checker.BaseURL.
const DefaultBaseURL = "https://api.github.com"

// DefaultRepo is Quil's own "owner/name"; an empty Checker.Repo means this
// one, so every existing call site keeps checking Quil's releases.
const DefaultRepo = "artyomsv/quil"

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

// ErrReleaseNotFound: Release was asked for a tag the repo does not have
// (GitHub answered 404). Typed so a caller can name the tag rather than
// parse a status line.
var ErrReleaseNotFound = errors.New("release not found")

// Checker fetches the latest published release.
type Checker struct {
	BaseURL string       // DefaultBaseURL when empty; tests point it at httptest
	Repo    string       // "owner/name"; DefaultRepo when empty
	Client  *http.Client // nil → 10 s-timeout default
}

func (c *Checker) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: requestTimeout}
}

// repoPath returns "/repos/<owner>/<name>" for the checker's repo, refusing
// anything that is not exactly two plain path segments. A plugin TOML is
// where a non-default repo comes from, and the string becomes part of the
// URL — the same reasoning as the tag check in Release.
func (c *Checker) repoPath() (string, error) {
	repo := c.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.ContainsAny(repo, `\?#% `) ||
		strings.Contains(name, "/") || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", fmt.Errorf("invalid release repo %q (want owner/name)", repo)
	}
	return "/repos/" + repo, nil
}

// Latest fetches the newest published release. One GET, no retry.
func (c *Checker) Latest(ctx context.Context) (*Release, error) {
	return c.Release(ctx, "")
}

// Release fetches one published release by tag, or the newest when tag is
// empty. The tag may be given with or without its leading "v".
//
// Fetching by tag exists for remote provisioning: installing "latest" on the
// far side when this TUI is not itself latest would simply invert the version
// mismatch it was called to fix.
func (c *Checker) Release(ctx context.Context, tag string) (*Release, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}

	repo, err := c.repoPath()
	if err != nil {
		return nil, err
	}
	path := repo + "/releases/latest"
	if tag != "" {
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		// The tag becomes a URL path segment. Anything that could escape it —
		// a slash, a traversal, a query or fragment introducer — is rejected
		// rather than escaped, because no real tag contains them and a
		// permissive reading here would let a caller redirect the request.
		if strings.ContainsAny(tag, "/\\?#%") || strings.Contains(tag, "..") {
			return nil, fmt.Errorf("invalid release tag %q", tag)
		}
		path = repo + "/releases/tags/" + tag
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && tag != "" {
		// Only the by-tag path: a 404 there means THIS tag does not exist,
		// which `quild plugin-update` must report as such and never answer
		// by falling back to latest. The latest path keeps its text.
		return nil, fmt.Errorf("%w: tag %s", ErrReleaseNotFound, tag)
	}
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
	if archive = findAsset(rel, want); archive == nil {
		return nil, nil, fmt.Errorf("release %s has no asset %q", rel.TagName, want)
	}
	if checksums = findAsset(rel, ChecksumsName); checksums == nil {
		return nil, nil, fmt.Errorf("release %s has no %s", rel.TagName, ChecksumsName)
	}
	return archive, checksums, nil
}

// ChecksumsName is the one combined checksums file Quil's own release ships.
// Other projects name theirs differently (one per OS, say), which is why
// FetchVerified takes the name as a parameter.
const ChecksumsName = "checksums.txt"

// findAsset returns the release asset with exactly this name, or nil.
func findAsset(rel *Release, name string) *Asset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}
