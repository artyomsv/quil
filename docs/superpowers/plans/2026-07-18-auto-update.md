# Auto-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Quil detects new GitHub releases, shows "new version available" in the TUI (status bar + About + once-per-version startup dialog), auto-downloads and stages the update in the background, and applies it at the next `quil` launch with one confirmation.

**Architecture:** Daemon owns check + stage (daily goroutine, new stdlib-only `internal/update/` package, manifest-written-last atomicity). Update state rides the existing `workspace_state` broadcast under an optional `update` key. Apply happens at `quil` launch (or About → Update now): verify → prompt → rename-aside binary swap → respawn; the existing version gate (`restartDaemonForUpgrade`) finishes the daemon restart, auto-confirmed via `QUIL_UPDATE_RESTART=1`.

**Tech Stack:** Go 1.25 stdlib only (`net/http`, `crypto/sha256`, `archive/zip`, `archive/tar`, `compress/gzip`). No new dependencies. Build/test via `./scripts/dev.sh` (Docker; host has no Go).

**Spec:** `docs/superpowers/specs/2026-07-17-auto-update-design.md`

## Global Constraints

- Release assets are named `quil_<version>_<goos>_<goarch>.tar.gz` (`.zip` on windows) + `checksums.txt` (sha256), per `.goreleaser.yml` `name_template`. Version in the asset name has NO leading `v`; the release tag DOES (`v1.37.0`).
- `internal/update/` must be stdlib-only (both binaries import it; no new go.mod entries).
- Dev builds (`!version.IsRelease()`) skip check, stage, and apply entirely.
- Daemon is the sole writer of `$QUIL_HOME/update/state.json`; the TUI is the sole writer of `$QUIL_HOME/update/notified.json`.
- All file writes are atomic temp+rename (idiom of `internal/persist/`).
- No blocking work on an IPC dispatch goroutine (pane-input-pipeline lesson): the on-demand stage handler must spawn a worker goroutine.
- Commit messages: imperative, conventional-commit style, NO AI attribution / Co-Authored-By trailers.
- Run tests via `./scripts/dev.sh test` (whole tree — the container runs `go test ./...`; there is no per-package wrapper). `./scripts/dev.sh vet` before each commit.
- Test fixtures must use obviously-synthetic versions (`0.0.1`, `0.0.2`, tag `v0.0.2`) — never versions that could be mistaken for real releases.

---

### Task 1: Config `[update]` section + path helpers

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (append)

**Interfaces:**
- Produces: `Config.Update UpdateConfig{Check, Auto bool}` (both default `true`); `config.UpdateDir()`, `config.UpdateStagingRoot()`, `config.UpdateStagingDir(version)`, `config.UpdateStatePath()`, `config.UpdateNotifiedPath()`.

- [ ] **Step 1: Write the failing test** (append to `internal/config/config_test.go`)

```go
func TestDefault_UpdateSection(t *testing.T) {
	cfg := Default()
	if !cfg.Update.Check {
		t.Error("Update.Check default = false, want true")
	}
	if !cfg.Update.Auto {
		t.Error("Update.Auto default = false, want true")
	}
}

func TestLoad_MissingUpdateSection_KeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ntheme = \"default\"\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Update.Check || !cfg.Update.Auto {
		t.Errorf("missing [update] section: Check=%v Auto=%v, want true/true", cfg.Update.Check, cfg.Update.Auto)
	}
}

func TestUpdatePaths_UnderQuilDir(t *testing.T) {
	t.Setenv("QUIL_HOME", filepath.Join(t.TempDir(), "qh"))
	root := QuilDir()
	if got, want := UpdateDir(), filepath.Join(root, "update"); got != want {
		t.Errorf("UpdateDir = %q, want %q", got, want)
	}
	if got, want := UpdateStagingDir("1.2.3"), filepath.Join(root, "update", "staged", "1.2.3"); got != want {
		t.Errorf("UpdateStagingDir = %q, want %q", got, want)
	}
	if got, want := UpdateStatePath(), filepath.Join(root, "update", "state.json"); got != want {
		t.Errorf("UpdateStatePath = %q, want %q", got, want)
	}
	if got, want := UpdateNotifiedPath(), filepath.Join(root, "update", "notified.json"); got != want {
		t.Errorf("UpdateNotifiedPath = %q, want %q", got, want)
	}
}
```

(Check the file's existing imports — it very likely already imports `os`/`filepath`; add if missing.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `cfg.Update undefined`, `UpdateDir undefined`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`:

Add to the `Config` struct (after the `Notification` field):

```go
	Update       UpdateConfig       `toml:"update"`
```

Add the type near `NotificationConfig`:

```go
// UpdateConfig controls the auto-update pipeline. Check gates the daily
// GitHub release check (one unauthenticated GET to api.github.com); Auto
// gates background download + staging of a newer release. auto = false
// degrades to notify-only. Dev builds (version.IsRelease() == false) skip
// the pipeline regardless of these settings.
type UpdateConfig struct {
	Check bool `toml:"check"`
	Auto  bool `toml:"auto"`
}
```

Add to `Default()` (after the `Notification:` literal):

```go
		Update: UpdateConfig{
			Check: true,
			Auto:  true,
		},
```

Add path helpers at the bottom of the file, after `SessionsDir()`:

```go
// UpdateDir returns the root directory of the auto-update pipeline:
// staged binaries, the daemon-owned state.json, and the TUI-owned
// notified.json all live under it.
func UpdateDir() string {
	return filepath.Join(QuilDir(), "update")
}

// UpdateStagingRoot returns the directory that holds one subdirectory per
// staged release version.
func UpdateStagingRoot() string {
	return filepath.Join(UpdateDir(), "staged")
}

// UpdateStagingDir returns the directory a given release version is staged
// into. The stager writes manifest.json into it LAST — its presence is the
// atomic "staging complete" marker.
func UpdateStagingDir(version string) string {
	return filepath.Join(UpdateStagingRoot(), version)
}

// UpdateStatePath is the daemon-owned check/stage status file. The TUI
// never writes it (single-writer-per-file rule).
func UpdateStatePath() string {
	return filepath.Join(UpdateDir(), "state.json")
}

// UpdateNotifiedPath is the TUI-owned once-per-version startup-dialog
// marker. The daemon never writes it.
func UpdateNotifiedPath() string {
	return filepath.Join(UpdateDir(), "notified.json")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — expected PASS. Then `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add [update] section and update path helpers"
```

---

### Task 2: `internal/update` — GitHub release checker

**Files:**
- Create: `internal/update/github.go`
- Test: `internal/update/github_test.go`

**Interfaces:**
- Produces: `update.Release{TagName, URL string; Assets []Asset}` with `(*Release).Version() string`; `update.Asset{Name, DownloadURL string; Size int64}`; `update.Checker{BaseURL string; Client *http.Client}` with `(*Checker).Latest(ctx) (*Release, error)`; `update.AssetName(version, goos, goarch string) string`; `update.FindAssets(rel *Release, goos, goarch string) (archive, checksums *Asset, err error)`.

- [ ] **Step 1: Write the failing tests** (`internal/update/github_test.go`)

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — package `internal/update` does not exist / undefined symbols.

- [ ] **Step 3: Implement** (`internal/update/github.go`)

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — expected PASS. `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/update/github.go internal/update/github_test.go
git commit -m "feat(update): GitHub latest-release checker and asset selection"
```

---

### Task 3: `internal/update` — stager (download, verify, extract, manifest)

**Files:**
- Create: `internal/update/stage.go`
- Test: `internal/update/stage_test.go`

**Interfaces:**
- Consumes: `Release`, `Asset`, `FindAssets` from Task 2.
- Produces: `update.Manifest{Version string; Files map[string]string; StagedAt string}`; `update.Stager{Root, GOOS, GOARCH string; Client *http.Client}` with `(*Stager).Stage(ctx, rel) error`; `update.BinaryNames(goos string) []string`; `update.FindStaged(root string) (*Manifest, string, error)`; `update.VerifyStaged(dir string, m *Manifest) error`; `update.PruneStaged(root, keep string) `.
- `Root` is `config.UpdateDir()`; staged dir layout is `<Root>/staged/<version>/{quil[.exe], quild[.exe], manifest.json}`.

- [ ] **Step 1: Write the failing tests** (`internal/update/stage_test.go`)

```go
package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// buildZip returns an in-memory zip holding the given files.
func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// stageFixture serves a release archive + checksums over httptest and
// returns the Release pointing at it.
func stageFixture(t *testing.T, tamperChecksum bool) (*Release, *httptest.Server) {
	t.Helper()
	archive := buildZip(t, map[string][]byte{
		"quil.exe":  []byte("fake-quil-binary"),
		"quild.exe": []byte("fake-quild-binary"),
		"LICENSE":   []byte("mit"),
	})
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	if tamperChecksum {
		hexSum = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	name := "quil_0.0.2_windows_amd64.zip"
	checksums := fmt.Sprintf("%s  %s\n", hexSum, name)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(checksums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rel := &Release{
		TagName: "v0.0.2",
		URL:     "https://example.invalid/rel",
		Assets: []Asset{
			{Name: name, DownloadURL: srv.URL + "/archive", Size: int64(len(archive))},
			{Name: "checksums.txt", DownloadURL: srv.URL + "/sums"},
		},
	}
	return rel, srv
}

func TestStage_HappyPath_WritesBinariesAndManifest(t *testing.T) {
	rel, _ := stageFixture(t, false)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}
	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	dir := filepath.Join(root, "staged", "0.0.2")
	for _, name := range []string{"quil.exe", "quild.exe", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("staged file %s missing: %v", name, err)
		}
	}
	man, gotDir, err := FindStaged(root)
	if err != nil || man == nil {
		t.Fatalf("FindStaged: man=%v err=%v", man, err)
	}
	if man.Version != "0.0.2" || gotDir != dir {
		t.Errorf("FindStaged = (%q, %q), want (0.0.2, %q)", man.Version, gotDir, dir)
	}
	if err := VerifyStaged(dir, man); err != nil {
		t.Errorf("VerifyStaged on fresh stage: %v", err)
	}
}

func TestStage_BadChecksum_NoManifest(t *testing.T) {
	rel, _ := stageFixture(t, true)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}
	if err := s.Stage(context.Background(), rel); err == nil {
		t.Fatal("Stage with tampered checksum = nil error, want error")
	}
	if _, err := os.Stat(filepath.Join(root, "staged", "0.0.2", "manifest.json")); !os.IsNotExist(err) {
		t.Errorf("manifest exists after failed stage (err=%v) — atomicity broken", err)
	}
	if man, _, _ := FindStaged(root); man != nil {
		t.Errorf("FindStaged after failed stage = %+v, want nil", man)
	}
}

func TestVerifyStaged_DetectsCorruption(t *testing.T) {
	rel, _ := stageFixture(t, false)
	root := t.TempDir()
	s := &Stager{Root: root, GOOS: "windows", GOARCH: "amd64"}
	if err := s.Stage(context.Background(), rel); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	man, dir, _ := FindStaged(root)
	if err := os.WriteFile(filepath.Join(dir, "quil.exe"), []byte("corrupted"), 0700); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	if err := VerifyStaged(dir, man); err == nil {
		t.Error("VerifyStaged on corrupted file = nil error, want error")
	}
}

func TestPruneStaged_KeepsOnlyGiven(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"0.0.1", "0.0.2"} {
		dir := filepath.Join(root, "staged", v)
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"`+v+`"}`), 0600)
	}
	PruneStaged(root, "0.0.2")
	if _, err := os.Stat(filepath.Join(root, "staged", "0.0.1")); !os.IsNotExist(err) {
		t.Error("0.0.1 not pruned")
	}
	if _, err := os.Stat(filepath.Join(root, "staged", "0.0.2")); err != nil {
		t.Errorf("0.0.2 was pruned: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `Stager`, `FindStaged`, `VerifyStaged`, `PruneStaged` undefined.

- [ ] **Step 3: Implement** (`internal/update/stage.go`)

```go
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/version"
)

// maxArchiveSize caps the release-archive download. Current archives are
// ~15 MB; 200 MB leaves generous headroom while bounding a hostile stream.
const maxArchiveSize = 200 << 20

// manifestName is the atomic "staging complete" marker inside a staged dir.
const manifestName = "manifest.json"

// BinaryNames returns the executable names inside the release archive for
// a platform.
func BinaryNames(goos string) []string {
	if goos == "windows" {
		return []string{"quil.exe", "quild.exe"}
	}
	return []string{"quil", "quild"}
}

// Manifest records a completed staging: which files landed and their
// hashes. It is written LAST — its presence marks the stage as complete;
// its absence means "nothing staged" regardless of other files present.
type Manifest struct {
	Version  string            `json:"version"`
	Files    map[string]string `json:"files"` // base name → sha256 hex
	StagedAt string            `json:"staged_at"` // RFC3339
}

// Stager downloads, verifies, and extracts a release into
// <Root>/staged/<version>/.
type Stager struct {
	Root   string // config.UpdateDir()
	GOOS   string // runtime.GOOS at prod call sites
	GOARCH string
	Client *http.Client // nil → 10 s-timeout default (per request)
}

func (s *Stager) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	// Downloads are bigger than the release-JSON GET; give them longer.
	return &http.Client{Timeout: 5 * time.Minute}
}

// Stage downloads the platform archive, verifies its sha256 against
// checksums.txt BEFORE extraction, extracts the quil/quild binaries, and
// writes manifest.json last. Any earlier failure leaves no manifest — the
// next check re-stages from scratch.
func (s *Stager) Stage(ctx context.Context, rel *Release) error {
	archive, sums, err := FindAssets(rel, s.GOOS, s.GOARCH)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return fmt.Errorf("create update dir: %w", err)
	}

	// Download the archive to a temp file, hashing as we write.
	tmp, err := os.CreateTemp(s.Root, "download-*")
	if err != nil {
		return fmt.Errorf("create download temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	gotSum, err := s.downloadHashed(ctx, archive.DownloadURL, tmp)
	closeErr := tmp.Close()
	if err != nil {
		return fmt.Errorf("download %s: %w", archive.Name, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close download temp: %w", closeErr)
	}

	// Fetch checksums.txt and compare before touching the archive content.
	wantSums, err := s.fetchChecksums(ctx, sums.DownloadURL)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	want, ok := wantSums[archive.Name]
	if !ok {
		return fmt.Errorf("checksums.txt has no entry for %s", archive.Name)
	}
	if !strings.EqualFold(want, gotSum) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", archive.Name, gotSum, want)
	}

	// Extract into the staging dir. Remove partial leftovers from a
	// previous crashed attempt at the same version first.
	dir := filepath.Join(s.Root, "staged", rel.Version())
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear staging dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	files, err := extractBinaries(tmpPath, dir, s.GOOS)
	if err != nil {
		return fmt.Errorf("extract %s: %w", archive.Name, err)
	}

	// Manifest last — the atomic completion marker (temp+rename).
	man := Manifest{Version: rel.Version(), Files: files, StagedAt: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	manTmp := filepath.Join(dir, manifestName+".tmp")
	if err := os.WriteFile(manTmp, data, 0600); err != nil {
		return fmt.Errorf("write manifest temp: %w", err)
	}
	if err := os.Rename(manTmp, filepath.Join(dir, manifestName)); err != nil {
		os.Remove(manTmp)
		return fmt.Errorf("rename manifest: %w", err)
	}
	return nil
}

// downloadHashed streams url into dst, returning the sha256 hex of the
// bytes written.
func (s *Stager) downloadHashed(ctx context.Context, url string, dst io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(resp.Body, maxArchiveSize)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchChecksums downloads and parses checksums.txt ("<hex>  <name>" lines).
func (s *Stager) fetchChecksums(ctx context.Context, url string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	sums := make(map[string]string)
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxJSONBody))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 {
			sums[fields[1]] = fields[0]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}

// extractBinaries pulls only the quil/quild executables out of the archive
// into dir, ignoring archive paths entirely (base-name match only — immune
// to zip-slip by construction). Returns base name → sha256 of extracted
// bytes.
func extractBinaries(archivePath, dir, goos string) (map[string]string, error) {
	wanted := make(map[string]bool)
	for _, n := range BinaryNames(goos) {
		wanted[n] = true
	}
	files := make(map[string]string)

	writeOne := func(base string, r io.Reader) error {
		out, err := os.OpenFile(filepath.Join(dir, base), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, cpErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, maxArchiveSize))
		if closeErr := out.Close(); cpErr == nil {
			cpErr = closeErr
		}
		if cpErr != nil {
			return cpErr
		}
		files[base] = hex.EncodeToString(h.Sum(nil))
		return nil
	}

	if strings.HasSuffix(archivePath, ".zip") || goos == "windows" {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			base := filepath.Base(f.Name)
			if !wanted[base] {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			err = writeOne(base, rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		f, err := os.Open(archivePath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			base := filepath.Base(hdr.Name)
			if hdr.Typeflag != tar.TypeReg || !wanted[base] {
				continue
			}
			if err := writeOne(base, tr); err != nil {
				return nil, err
			}
		}
	}

	if len(files) != len(wanted) {
		return nil, fmt.Errorf("archive missing binaries: extracted %d of %d", len(files), len(wanted))
	}
	return files, nil
}

// FindStaged scans <root>/staged/* for completed stages (manifest present)
// and returns the highest-version one, its directory, or (nil, "", nil)
// when nothing is fully staged.
func FindStaged(root string) (*Manifest, string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "staged"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	var best *Manifest
	var bestDir string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, "staged", e.Name())
		data, err := os.ReadFile(filepath.Join(dir, manifestName))
		if err != nil {
			continue // no manifest → incomplete stage, ignore
		}
		var man Manifest
		if err := json.Unmarshal(data, &man); err != nil || man.Version == "" {
			continue
		}
		if best == nil {
			best, bestDir = &man, dir
			continue
		}
		if cmp, err := version.Compare(man.Version, best.Version); err == nil && cmp > 0 {
			manCopy := man
			best, bestDir = &manCopy, dir
		}
	}
	return best, bestDir, nil
}

// VerifyStaged re-hashes every file listed in the manifest against the
// staged dir — the pre-swap corruption/tamper gate.
func VerifyStaged(dir string, m *Manifest) error {
	for name, want := range m.Files {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("staged %s: %w", name, err)
		}
		h := sha256.New()
		_, cpErr := io.Copy(h, f)
		f.Close()
		if cpErr != nil {
			return fmt.Errorf("hash staged %s: %w", name, cpErr)
		}
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
			return fmt.Errorf("staged %s: sha256 %s, manifest says %s", name, got, want)
		}
	}
	return nil
}

// PruneStaged removes every staged version dir except keep. Best-effort.
func PruneStaged(root, keep string) {
	entries, err := os.ReadDir(filepath.Join(root, "staged"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != keep {
			os.RemoveAll(filepath.Join(root, "staged", e.Name()))
		}
	}
}
```

Note: `Manifest` uses a loop over `m.Files` — a manifest fixture created by hand in later tasks must include the `files` map.

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — expected PASS (including a tar.gz path exercised only via code review here; the zip path is covered). `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/update/stage.go internal/update/stage_test.go
git commit -m "feat(update): download, checksum-verify, and stage release binaries"
```

---

### Task 4: `internal/update` — state.json / notified.json + writability probe

**Files:**
- Create: `internal/update/state.go`
- Test: `internal/update/state_test.go`

**Interfaces:**
- Produces: `update.State{LastCheckMs int64; LatestVersion, ReleaseURL, StagedVersion string; InstallWritable bool}`; `update.LoadState(path) State`; `update.SaveState(path, State) error`; `update.InstallWritable(dir string) bool`; `update.LoadNotifiedVersion(path) string`; `update.SaveNotifiedVersion(path, version) error`.

- [ ] **Step 1: Write the failing tests** (`internal/update/state_test.go`)

```go
package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestState_SaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update", "state.json")
	st := State{LastCheckMs: 1234, LatestVersion: "0.0.2", ReleaseURL: "https://example.invalid/r", StagedVersion: "0.0.2", InstallWritable: true}
	if err := SaveState(path, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got := LoadState(path)
	if got != st {
		t.Errorf("LoadState = %+v, want %+v", got, st)
	}
}

func TestLoadState_MissingOrCorrupt_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	if got := LoadState(filepath.Join(dir, "nope.json")); got != (State{}) {
		t.Errorf("missing file: LoadState = %+v, want zero", got)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0600)
	if got := LoadState(bad); got != (State{}) {
		t.Errorf("corrupt file: LoadState = %+v, want zero", got)
	}
}

func TestNotifiedVersion_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update", "notified.json")
	if got := LoadNotifiedVersion(path); got != "" {
		t.Errorf("missing file: LoadNotifiedVersion = %q, want empty", got)
	}
	if err := SaveNotifiedVersion(path, "0.0.2"); err != nil {
		t.Fatalf("SaveNotifiedVersion: %v", err)
	}
	if got := LoadNotifiedVersion(path); got != "0.0.2" {
		t.Errorf("LoadNotifiedVersion = %q, want 0.0.2", got)
	}
}

func TestInstallWritable(t *testing.T) {
	if !InstallWritable(t.TempDir()) {
		t.Error("InstallWritable(TempDir) = false, want true")
	}
	if InstallWritable(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("InstallWritable(nonexistent dir) = true, want false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test` — FAIL, symbols undefined.

- [ ] **Step 3: Implement** (`internal/update/state.go`)

```go
package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State is the daemon-owned check/stage status persisted at
// config.UpdateStatePath(). The TUI reads it never — update info reaches
// the TUI over IPC; this file exists so a restarted daemon remembers its
// last check across runs.
type State struct {
	LastCheckMs     int64  `json:"last_check_ms"`
	LatestVersion   string `json:"latest_version,omitempty"`
	ReleaseURL      string `json:"release_url,omitempty"`
	StagedVersion   string `json:"staged_version,omitempty"`
	InstallWritable bool   `json:"install_writable"`
}

// LoadState returns the persisted state, or the zero State on any error
// (missing file, corrupt JSON) — the pipeline treats that as "never
// checked".
func LoadState(path string) State {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}
	}
	return st
}

// SaveState writes atomically (temp+rename), creating parent dirs.
func SaveState(path string, st State) error {
	return saveJSON(path, st)
}

// notifiedFile is the TUI-owned once-per-version dialog marker.
type notifiedFile struct {
	Version string `json:"version"`
}

// LoadNotifiedVersion returns the last version the startup dialog was
// shown for, or "" (never notified / unreadable).
func LoadNotifiedVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var n notifiedFile
	if err := json.Unmarshal(data, &n); err != nil {
		return ""
	}
	return n.Version
}

// SaveNotifiedVersion records that the startup dialog was shown for version.
func SaveNotifiedVersion(path, version string) error {
	return saveJSON(path, notifiedFile{Version: version})
}

func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// InstallWritable probes whether dir accepts file creation — the gate for
// self-update. Package-manager / system installs fail this; the pipeline
// then degrades to notify-only.
func InstallWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".quil-update-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — PASS. `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/update/state.go internal/update/state_test.go
git commit -m "feat(update): persisted check state, notified marker, writability probe"
```

---

### Task 5: IPC — `UpdateInfo` + stage-request message pair

**Files:**
- Modify: `internal/ipc/protocol.go`

**Interfaces:**
- Produces: `ipc.MsgStageUpdateReq = "stage_update_req"`, `ipc.MsgStageUpdateResp = "stage_update_resp"`; `ipc.UpdateInfo{LatestVersion, ReleaseURL, StagedVersion string; InstallWritable bool}`; `ipc.StageUpdateRespPayload{Success bool; Version, Error string}`.

- [ ] **Step 1: Implement** (no dedicated test — pure wire declarations; consumers test them)

In `internal/ipc/protocol.go`, after the pane-history message constants (`MsgPaneHistoryEntryResp`), add:

```go
	// Auto-update (TUI ⇄ daemon)
	MsgStageUpdateReq  = "stage_update_req"  // TUI → daemon (empty payload)
	MsgStageUpdateResp = "stage_update_resp" // daemon → TUI (unicast)
```

After `PaneHistoryEntryRespPayload`, add:

```go
// UpdateInfo rides the workspace_state broadcast under the "update" key
// when a newer release than the running daemon's version is known. Omitted
// entirely when up to date; old clients ignore the extra key.
type UpdateInfo struct {
	LatestVersion   string `json:"latest_version"`
	ReleaseURL      string `json:"release_url,omitempty"`
	StagedVersion   string `json:"staged_version,omitempty"` // set once fully staged
	InstallWritable bool   `json:"install_writable"`
}

// StageUpdateRespPayload answers MsgStageUpdateReq (About → Update now with
// nothing staged yet).
type StageUpdateRespPayload struct {
	Success bool   `json:"success"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}
```

- [ ] **Step 2: Build check**

Run: `./scripts/dev.sh vet` — clean (compiles).

- [ ] **Step 3: Commit**

```bash
git add internal/ipc/protocol.go
git commit -m "feat(ipc): update-info broadcast field and stage_update message pair"
```

---

### Task 6: Daemon — update checker goroutine + broadcast + on-demand stage

**Files:**
- Create: `internal/daemon/update.go`
- Modify: `internal/daemon/daemon.go` (struct fields, `Start()`, `buildWorkspaceState`, `handleMessage` dispatch)
- Test: `internal/daemon/update_test.go`

**Interfaces:**
- Consumes: `update.Checker/Stager/State/LoadState/SaveState/InstallWritable/PruneStaged` (Tasks 2-4); `ipc.UpdateInfo`, `ipc.MsgStageUpdateReq/Resp`, `ipc.StageUpdateRespPayload` (Task 5); `config.UpdateDir()/UpdateStatePath()` (Task 1); existing `d.cfg`, `d.shutdown`, `d.broadcastState()`, `respondTo(conn, id, type, payload)`, `versionpkg "github.com/artyomsv/quil/internal/version"`.
- Produces: `(d *Daemon).setUpdateInfo(*ipc.UpdateInfo) bool`, `(d *Daemon).currentUpdateInfo() *ipc.UpdateInfo` — used by `buildWorkspaceState`; `"update"` key in the broadcast state map.

- [ ] **Step 1: Write the failing test** (`internal/daemon/update_test.go`)

```go
package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestBuildWorkspaceState_UpdateKey asserts the broadcast state carries the
// "update" key exactly when update info is set.
func TestBuildWorkspaceState_UpdateKey(t *testing.T) {
	d := New(config.Default())

	state := d.buildWorkspaceState()
	if _, ok := state["update"]; ok {
		t.Error("update key present with no update info")
	}

	info := &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}
	if changed := d.setUpdateInfo(info); !changed {
		t.Error("setUpdateInfo(first) = false, want true (changed)")
	}
	if changed := d.setUpdateInfo(info); changed {
		t.Error("setUpdateInfo(same) = true, want false (unchanged)")
	}

	state = d.buildWorkspaceState()
	got, ok := state["update"].(*ipc.UpdateInfo)
	if !ok {
		t.Fatalf("state[update] = %T, want *ipc.UpdateInfo", state["update"])
	}
	if got.LatestVersion != "0.0.2" || got.StagedVersion != "0.0.2" || !got.InstallWritable {
		t.Errorf("state[update] = %+v", got)
	}

	if changed := d.setUpdateInfo(nil); !changed {
		t.Error("setUpdateInfo(nil after set) = false, want true")
	}
	if _, ok := d.buildWorkspaceState()["update"]; ok {
		t.Error("update key present after clearing info")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test` — FAIL: `d.setUpdateInfo` undefined.

- [ ] **Step 3: Implement**

Create `internal/daemon/update.go`:

```go
package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/update"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// updateCheckInitialDelay defers the first release check past daemon
// startup so a heavy workspace restore is never slowed by network I/O.
const updateCheckInitialDelay = 1 * time.Minute

// updateCheckInterval paces the recurring release check. Unauthenticated
// GitHub API allows 60 req/hr/IP; one per day is far below it.
const updateCheckInterval = 24 * time.Hour

// updateCheckTimeout bounds one full check+stage cycle (JSON GET + archive
// download + extraction).
const updateCheckTimeout = 10 * time.Minute

// updateChecker is the daily release check + auto-stage loop. Started from
// Start() alongside idleChecker; exits on d.shutdown. Entirely inert for
// dev builds and when [update] check = false.
func (d *Daemon) updateChecker() {
	if !d.cfg.Update.Check || !versionpkg.IsRelease() {
		return
	}
	timer := time.NewTimer(updateCheckInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-d.shutdown:
			return
		case <-timer.C:
			d.runUpdateCheck()
			timer.Reset(updateCheckInterval)
		}
	}
}

// runUpdateCheck performs one check (and, when enabled and possible, one
// stage) and refreshes the broadcast update info.
func (d *Daemon) runUpdateCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()

	checker := &update.Checker{}
	rel, err := checker.Latest(ctx)
	if err != nil {
		// Network failures are routine (offline laptop); keep quiet.
		logger.Debug("update check: %v", err)
		return
	}

	st := update.LoadState(config.UpdateStatePath())
	st.LastCheckMs = time.Now().UnixMilli()
	st.LatestVersion = rel.Version()
	st.ReleaseURL = rel.URL

	cmp, err := versionpkg.Compare(rel.Version(), versionpkg.Current())
	if err != nil || cmp <= 0 {
		// Up to date (or unparseable tag): clear any stale announcement.
		st.StagedVersion = ""
		if saveErr := update.SaveState(config.UpdateStatePath(), st); saveErr != nil {
			log.Printf("update: save state: %v", saveErr)
		}
		if d.setUpdateInfo(nil) {
			d.broadcastState()
		}
		return
	}

	st.InstallWritable = installDirWritable()
	if d.cfg.Update.Auto && st.InstallWritable && st.StagedVersion != rel.Version() {
		if stageErr := d.stageRelease(ctx, rel); stageErr != nil {
			log.Printf("update: stage v%s: %v", rel.Version(), stageErr)
		} else {
			st.StagedVersion = rel.Version()
			update.PruneStaged(config.UpdateDir(), rel.Version())
			log.Printf("update: staged v%s (applies on next quil launch)", rel.Version())
		}
	}
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		log.Printf("update: save state: %v", err)
	}

	info := &ipc.UpdateInfo{
		LatestVersion:   st.LatestVersion,
		ReleaseURL:      st.ReleaseURL,
		StagedVersion:   st.StagedVersion,
		InstallWritable: st.InstallWritable,
	}
	if d.setUpdateInfo(info) {
		d.broadcastState()
	}
}

// stageRelease runs the download/verify/extract pipeline, single-flight
// guarded so the daily tick and an on-demand request can't stage twice
// concurrently.
func (d *Daemon) stageRelease(ctx context.Context, rel *update.Release) error {
	if !d.updateStaging.CompareAndSwap(false, true) {
		return fmt.Errorf("staging already in progress")
	}
	defer d.updateStaging.Store(false)
	s := &update.Stager{Root: config.UpdateDir(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	return s.Stage(ctx, rel)
}

// installDirWritable probes the daemon executable's own directory — the
// swap target of the apply step.
func installDirWritable() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return update.InstallWritable(filepath.Dir(exe))
}

// setUpdateInfo swaps the broadcast update info, reporting whether it
// changed (callers broadcast only on change). nil clears it.
func (d *Daemon) setUpdateInfo(info *ipc.UpdateInfo) bool {
	d.updateMu.Lock()
	defer d.updateMu.Unlock()
	old := d.updateInfo
	same := (old == nil && info == nil) ||
		(old != nil && info != nil && *old == *info)
	d.updateInfo = info
	return !same
}

// currentUpdateInfo returns a copy-safe pointer for the state broadcast.
func (d *Daemon) currentUpdateInfo() *ipc.UpdateInfo {
	d.updateMu.Lock()
	defer d.updateMu.Unlock()
	return d.updateInfo
}

// handleStageUpdateReq stages the latest release on demand (About → Update
// now before the daily tick staged anything). The download takes seconds
// to minutes, so it must NOT run on the conn's dispatch goroutine — the
// worker responds when done and refreshes the broadcast on success.
func (d *Daemon) handleStageUpdateReq(conn *ipc.Conn, msg *ipc.Message) {
	go func() {
		payload := d.stageOnDemand()
		respondTo(conn, msg.ID, ipc.MsgStageUpdateResp, payload)
		if payload.Success {
			d.broadcastState()
		}
	}()
}

func (d *Daemon) stageOnDemand() ipc.StageUpdateRespPayload {
	if !versionpkg.IsRelease() {
		return ipc.StageUpdateRespPayload{Error: "dev build — updates disabled"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()

	rel, err := (&update.Checker{}).Latest(ctx)
	if err != nil {
		return ipc.StageUpdateRespPayload{Error: fmt.Sprintf("check release: %v", err)}
	}
	cmp, err := versionpkg.Compare(rel.Version(), versionpkg.Current())
	if err != nil || cmp <= 0 {
		return ipc.StageUpdateRespPayload{Error: "already up to date"}
	}
	if !installDirWritable() {
		return ipc.StageUpdateRespPayload{Error: "install directory not writable"}
	}
	if err := d.stageRelease(ctx, rel); err != nil {
		return ipc.StageUpdateRespPayload{Error: fmt.Sprintf("stage: %v", err)}
	}

	st := update.LoadState(config.UpdateStatePath())
	st.LastCheckMs = time.Now().UnixMilli()
	st.LatestVersion = rel.Version()
	st.ReleaseURL = rel.URL
	st.StagedVersion = rel.Version()
	st.InstallWritable = true
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		log.Printf("update: save state: %v", err)
	}
	update.PruneStaged(config.UpdateDir(), rel.Version())
	d.setUpdateInfo(&ipc.UpdateInfo{
		LatestVersion:   st.LatestVersion,
		ReleaseURL:      st.ReleaseURL,
		StagedVersion:   st.StagedVersion,
		InstallWritable: true,
	})
	log.Printf("update: staged v%s on demand", rel.Version())
	return ipc.StageUpdateRespPayload{Success: true, Version: rel.Version()}
}
```

(The `updateStaging atomic.Bool` field itself is declared on the Daemon struct in daemon.go below — do NOT import `sync/atomic` in update.go; it isn't referenced here.)

In `internal/daemon/daemon.go`:

1. Add fields to the `Daemon` struct (near other mutex-guarded fields):

```go
	// updateMu guards updateInfo, the currently-announced newer release
	// (nil = up to date). updateStaging is the single-flight guard for the
	// download/stage pipeline (daily tick vs on-demand request).
	updateMu      sync.Mutex
	updateInfo    *ipc.UpdateInfo
	updateStaging atomic.Bool
```

(`sync` and `sync/atomic` are already imported in daemon.go; verify.)

2. In `Start()`, next to `go d.idleChecker()` (line ~187):

```go
	go d.updateChecker()
```

3. In `buildWorkspaceState()` (line ~1726), attach the key:

```go
func (d *Daemon) buildWorkspaceState() map[string]any {
	activeTab, tabs, panesByTab := d.session.SnapshotState()
	state := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, true)
	// Broadcast-only (never persisted): announced newer release, if any.
	if info := d.currentUpdateInfo(); info != nil {
		state["update"] = info
	}
	return state
}
```

(Note: this is the broadcast path only. Disk snapshots call `workspaceStateFromSnapshot` directly and never see the key — matching the spec's "runtime-only".)

4. In `handleMessage`'s dispatch switch (find the `case ipc.MsgMemoryReportReq:` neighborhood, line ~821), add:

```go
	case ipc.MsgStageUpdateReq:
		d.handleStageUpdateReq(conn, msg)
```

Also at daemon startup (in `Start()`, next to the `SessionsDir` MkdirAll), seed the announcement from the persisted state so a daemon restart doesn't lose the notice until the next daily tick:

```go
	// Re-announce a previously-detected update immediately after restart
	// (the daily tick would otherwise leave a 1-day blind spot). Only when
	// the persisted latest is still newer than this (possibly just
	// upgraded) daemon.
	if versionpkg.IsRelease() && d.cfg.Update.Check {
		if st := update.LoadState(config.UpdateStatePath()); st.LatestVersion != "" {
			if cmp, err := versionpkg.Compare(st.LatestVersion, versionpkg.Current()); err == nil && cmp > 0 {
				d.setUpdateInfo(&ipc.UpdateInfo{
					LatestVersion:   st.LatestVersion,
					ReleaseURL:      st.ReleaseURL,
					StagedVersion:   st.StagedVersion,
					InstallWritable: st.InstallWritable,
				})
			}
		}
	}
```

Add imports to daemon.go as needed: `"github.com/artyomsv/quil/internal/update"` (versionpkg is already imported for the version handshake — verify the alias used there and reuse it).

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — PASS. `./scripts/dev.sh test-race` — clean (setUpdateInfo/currentUpdateInfo race coverage). `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/update.go internal/daemon/update_test.go internal/daemon/daemon.go
git commit -m "feat(daemon): daily release check, auto-stage, update-info broadcast"
```

---

### Task 7: TUI — parse update info + status bar segment

**Files:**
- Create: `internal/tui/update.go`
- Modify: `internal/tui/model.go` (WorkspaceStateMsg struct, `parseWorkspaceState`, WorkspaceStateMsg Update-handler, `renderStatusBar`, listener case)
- Test: `internal/tui/update_test.go`

**Interfaces:**
- Consumes: `ipc.UpdateInfo`, `ipc.MsgStageUpdateResp`, `ipc.StageUpdateRespPayload`; `version.Compare` (`internal/version`).
- Produces: `WorkspaceStateMsg.Update *ipc.UpdateInfo`; `Model.updateInfo *ipc.UpdateInfo`; `updateStatusSegment(info *ipc.UpdateInfo, current string) string`; `stageUpdateRespMsg{Resp ipc.StageUpdateRespPayload}` (Bubble Tea msg). Task 8/9 consume `m.updateInfo` and `stageUpdateRespMsg`.

- [ ] **Step 1: Write the failing tests** (`internal/tui/update_test.go`)

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestUpdateStatusSegment(t *testing.T) {
	cases := []struct {
		name    string
		info    *ipc.UpdateInfo
		current string
		want    string
	}{
		{"nil info", nil, "0.0.1", ""},
		{"up to date", &ipc.UpdateInfo{LatestVersion: "0.0.1"}, "0.0.1", ""},
		{"older latest (rollback)", &ipc.UpdateInfo{LatestVersion: "0.0.1"}, "0.0.2", ""},
		{"newer not staged", &ipc.UpdateInfo{LatestVersion: "0.0.2"}, "0.0.1", "↑ v0.0.2"},
		{"newer staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2"}, "0.0.1", "↑ v0.0.2 ready"},
		{"dev build current", &ipc.UpdateInfo{LatestVersion: "0.0.2"}, "dev", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateStatusSegment(tc.info, tc.current); got != tc.want {
				t.Errorf("updateStatusSegment = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseWorkspaceState_UpdateKey(t *testing.T) {
	raw := map[string]any{
		"active_tab": "tab-aaaaaaaa",
		"update": map[string]any{
			"latest_version":   "0.0.2",
			"release_url":      "https://example.invalid/r",
			"staged_version":   "0.0.2",
			"install_writable": true,
		},
	}
	state := parseWorkspaceState(raw)
	if state.Update == nil {
		t.Fatal("state.Update = nil, want parsed info")
	}
	if state.Update.LatestVersion != "0.0.2" || state.Update.StagedVersion != "0.0.2" ||
		state.Update.ReleaseURL != "https://example.invalid/r" || !state.Update.InstallWritable {
		t.Errorf("state.Update = %+v", state.Update)
	}

	if got := parseWorkspaceState(map[string]any{"active_tab": "t"}); got.Update != nil {
		t.Errorf("no update key: state.Update = %+v, want nil", got.Update)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test` — FAIL, `updateStatusSegment` undefined / `state.Update` undefined.

- [ ] **Step 3: Implement**

Create `internal/tui/update.go`:

```go
package tui

import (
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/version"
)

// stageUpdateRespMsg carries the daemon's answer to MsgStageUpdateReq.
type stageUpdateRespMsg struct {
	Resp ipc.StageUpdateRespPayload
}

// updateAvailable reports whether info announces a release strictly newer
// than the running TUI. False for dev builds (unparseable current) — the
// pipeline is daemon-gated too, this is the belt-and-suspenders TUI gate.
func updateAvailable(info *ipc.UpdateInfo, current string) bool {
	if info == nil || info.LatestVersion == "" {
		return false
	}
	cmp, err := version.Compare(info.LatestVersion, current)
	return err == nil && cmp > 0
}

// updateStatusSegment renders the persistent status-bar reminder:
// "↑ v1.37.0" (known, not staged) / "↑ v1.37.0 ready" (staged — applies on
// next launch). Empty when up to date or no info.
func updateStatusSegment(info *ipc.UpdateInfo, current string) string {
	if !updateAvailable(info, current) {
		return ""
	}
	if info.StagedVersion == info.LatestVersion {
		return "↑ v" + info.LatestVersion + " ready"
	}
	return "↑ v" + info.LatestVersion
}
```

In `internal/tui/model.go`:

1. Extend `WorkspaceStateMsg` (line ~44):

```go
type WorkspaceStateMsg struct {
	ActiveTab string
	Tabs      []TabInfo
	Panes     []PaneInfo
	// Update is the daemon's announced newer release (nil when up to date).
	Update *ipc.UpdateInfo
}
```

2. In `parseWorkspaceState` (line ~3476), after the `active_tab` block, add:

```go
	if u, ok := raw["update"].(map[string]any); ok {
		info := &ipc.UpdateInfo{}
		if s, ok := u["latest_version"].(string); ok {
			info.LatestVersion = s
		}
		if s, ok := u["release_url"].(string); ok {
			info.ReleaseURL = s
		}
		if s, ok := u["staged_version"].(string); ok {
			info.StagedVersion = s
		}
		if b, ok := u["install_writable"].(bool); ok {
			info.InstallWritable = b
		}
		if info.LatestVersion != "" {
			state.Update = info
		}
	}
```

3. Add a Model field (near `flashText`):

```go
	// updateInfo mirrors the daemon's announced newer release; drives the
	// status-bar segment, the About row, and the startup notice.
	updateInfo *ipc.UpdateInfo
```

4. In the `Update` handler's `case WorkspaceStateMsg:` branch (find where `applyWorkspaceState` is invoked), add as the FIRST lines of the case:

```go
		m.updateInfo = msg.Update
```

(Unconditional copy, mirroring `syncPaneMeta`'s reasoning: an absent key propagates "up to date" after a daemon-side clear.)

5. In `renderStatusBar` (line ~3295), after the `mem` prepend and before the `[dev]` prepend:

```go
	if seg := updateStatusSegment(m.updateInfo, m.version); seg != "" {
		right = seg + " | " + right
	}
```

6. In `listenForMessages` (line ~3385), add a case before `default:`:

```go
		case ipc.MsgStageUpdateResp:
			var payload ipc.StageUpdateRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode stage_update_resp: %v", err)
				return listenContinueMsg{}
			}
			return stageUpdateRespMsg{Resp: payload}
```

7. In the `Update` handler, add a top-level case (next to `memoryReportMsg` — CRITICAL: it must re-arm the listener like every IPC-response msg or the listen loop dies):

```go
	case stageUpdateRespMsg:
		if msg.Resp.Success {
			m.setFlash("update v" + msg.Resp.Version + " staged — applies on next launch")
		} else {
			m.setFlash("update failed: " + msg.Resp.Error)
		}
		return m, tea.Batch(m.listenForMessages(), m.flashCmd())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — PASS. `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/update.go internal/tui/update_test.go internal/tui/model.go
git commit -m "feat(tui): show available update in status bar from workspace state"
```

---

### Task 8: TUI — About dialog row + apply/stage actions + confirm

**Files:**
- Modify: `internal/tui/dialog.go` (`aboutStopDaemonIndex` 7→8, new `aboutUpdateIndex`, `handleAboutKey`, `renderAboutDialog`, `handleConfirmKey`, confirm render), `internal/tui/update.go` (label + action), `internal/tui/model.go` (apply-intent field + accessor)
- Test: `internal/tui/update_test.go` (append)

**Interfaces:**
- Consumes: `m.updateInfo`, `updateAvailable` (Task 7); `ipc.MsgStageUpdateReq`.
- Produces: `confirmKindApplyUpdate = "apply-update"`; `Model.applyUpdateOnExit` + `(m Model).ApplyUpdateRequested() bool` (consumed by Task 11); `aboutUpdateLabel(info, current) string`; `(m Model).handleUpdateAction() (tea.Model, tea.Cmd)` (also consumed by Task 9's dialog).

- [ ] **Step 1: Write the failing test** (append to `internal/tui/update_test.go`)

```go
func TestAboutUpdateLabel(t *testing.T) {
	cases := []struct {
		name    string
		info    *ipc.UpdateInfo
		current string
		want    string
	}{
		{"up to date", nil, "0.0.1", "Check for updates (up to date)"},
		{"staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}, "0.0.1", "Update to v0.0.2 (staged — applies on restart)"},
		{"not staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}, "0.0.1", "Update to v0.0.2 (download)"},
		{"unwritable", &ipc.UpdateInfo{LatestVersion: "0.0.2"}, "0.0.1", "Update available: v0.0.2 (manual install)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aboutUpdateLabel(tc.info, tc.current); got != tc.want {
				t.Errorf("aboutUpdateLabel = %q, want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test` — FAIL, `aboutUpdateLabel` undefined.

- [ ] **Step 3: Implement**

Append to `internal/tui/update.go`:

```go
// aboutUpdateLabel is the dynamic F1 → About row for updates.
func aboutUpdateLabel(info *ipc.UpdateInfo, current string) string {
	if !updateAvailable(info, current) {
		return "Check for updates (up to date)"
	}
	if !info.InstallWritable {
		return "Update available: v" + info.LatestVersion + " (manual install)"
	}
	if info.StagedVersion == info.LatestVersion {
		return "Update to v" + info.LatestVersion + " (staged — applies on restart)"
	}
	return "Update to v" + info.LatestVersion + " (download)"
}
```

Add the action method (also in `internal/tui/update.go`; add imports `"log"`, `tea "charm.land/bubbletea/v2"` — match the import alias used in model.go):

```go
// handleUpdateAction is the Enter action for the About update row and the
// startup notice's "Update now" button. Branches: up to date → flash;
// unwritable → flash pointing at the release page; staged → apply confirm;
// otherwise → on-demand stage request to the daemon.
func (m Model) handleUpdateAction() (tea.Model, tea.Cmd) {
	info := m.updateInfo
	m.dialog = dialogNone
	if !updateAvailable(info, m.version) {
		m.setFlash("quil is up to date (v" + m.version + ")")
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	if !info.InstallWritable {
		m.setFlash("v" + info.LatestVersion + " available — install dir not writable, see " + info.ReleaseURL)
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	if info.StagedVersion == info.LatestVersion {
		m.dialog = dialogConfirm
		m.confirmKind = confirmKindApplyUpdate
		m.confirmID = ""
		m.confirmName = info.LatestVersion
		m.dialogCursor = 0
		return m, nil
	}
	// Nothing staged yet ([update] auto = false, or the daily tick hasn't
	// run): ask the daemon to stage now. Response lands as
	// stageUpdateRespMsg; the refreshed broadcast updates m.updateInfo.
	m.setFlash("downloading update v" + info.LatestVersion + "…")
	if m.client != nil {
		req, err := ipc.NewMessage(ipc.MsgStageUpdateReq, nil)
		if err != nil {
			log.Printf("stage update: marshal: %v", err)
		} else if err := m.client.Send(req); err != nil {
			log.Printf("stage update: send: %v", err)
		}
	}
	return m, tea.Batch(tea.ClearScreen, m.flashCmd())
}
```

In `internal/tui/dialog.go`:

1. Update the index constants (line ~362):

```go
// aboutUpdateIndex is the row index of the dynamic update row in the F1 →
// About (root) menu; aboutStopDaemonIndex sits below it. Named constants
// so handleAboutKey, lastAboutItem, and the confirm-dialog Esc handlers
// cannot drift on the indices.
const aboutUpdateIndex = 7
const aboutStopDaemonIndex = 8
```

2. In `handleAboutKey` add the case before `case aboutStopDaemonIndex:`:

```go
		case aboutUpdateIndex:
			return m.handleUpdateAction()
```

3. In `renderAboutDialog`, replace the `items := []string{...}` literal with:

```go
	items := []string{
		"Settings",
		"Shortcuts",
		"Plugins",
		"Memory",
		"View client log",
		"View daemon log",
		"View MCP logs",
		aboutUpdateLabel(m.updateInfo, m.version),
		"Stop daemon",
	}
```

4. Add the confirm-kind constant next to `confirmKindRestartPane` (line ~229):

```go
// confirmKindApplyUpdate is the discriminator on confirmKind for the
// "apply staged update now" confirm (About → Update / startup notice →
// Update now). Accepting quits the TUI with an apply-intent flag;
// cmd/quil/main.go runs the swap after tea.Program exits.
const confirmKindApplyUpdate = "apply-update"
```

5. In `handleConfirmKey`'s `case "esc", "n":` branch, add before the final fallthrough:

```go
		if m.confirmKind == confirmKindApplyUpdate {
			// Back to the About menu, cursor on the update row.
			m.dialog = dialogAbout
			m.dialogCursor = aboutUpdateIndex
			return m, nil
		}
```

6. In `handleConfirmKey`'s `case "enter", "y":` branch, after the `confirmKindRestartPane` block:

```go
		// Apply-update: quit the TUI with the apply intent set; main.go
		// performs verify → swap → respawn after the program exits (the
		// terminal must be released before the wrapper respawn).
		if kind == confirmKindApplyUpdate {
			m.dialog = dialogNone
			m.applyUpdateOnExit = true
			return m, tea.Quit
		}
```

7. Find the confirm-dialog render switch (`case confirmKindShutdown:` around line ~892) and add a case mirroring its structure with this copy (adapt variable names to the surrounding code — title/message strings assigned the same way the shutdown case assigns them):

```go
	case confirmKindApplyUpdate:
		title = "Apply update"
		message = "Apply update v" + m.confirmName + " now?\n" +
			"The TUI restarts and the daemon respawns all panes.\n" +
			"Claude sessions resume; running shell commands are killed."
```

In `internal/tui/model.go`:

8. Add the Model field next to `updateInfo`:

```go
	// applyUpdateOnExit signals cmd/quil/main.go to run the staged-update
	// swap after tea.Program returns (set by the apply confirm).
	applyUpdateOnExit bool
```

9. Add the accessor next to `ConfigChanged()`/`Config()`:

```go
// ApplyUpdateRequested reports whether the user confirmed applying the
// staged update; main.go acts on it after the program exits.
func (m Model) ApplyUpdateRequested() bool { return m.applyUpdateOnExit }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — PASS, including the existing `dialog_test.go` About tests (they use the constant, which moved to 8 — if any hardcodes the old count `7`, update it to the constant). `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dialog.go internal/tui/update.go internal/tui/update_test.go internal/tui/model.go
git commit -m "feat(tui): About update row with stage and apply actions"
```

---

### Task 9: TUI — once-per-version startup notice dialog

**Files:**
- Modify: `internal/tui/model.go` (dialogScreen const, WorkspaceStateMsg handler), `internal/tui/dialog.go` (dispatch + render switch), `internal/tui/update.go` (trigger, key handler, render)
- Test: `internal/tui/update_test.go` (append)

**Interfaces:**
- Consumes: `update.LoadNotifiedVersion/SaveNotifiedVersion` + `config.UpdateNotifiedPath()` (Tasks 1, 4); `m.handleUpdateAction()` (Task 8); `updateAvailable` (Task 7).
- Produces: `dialogUpdateNotice` screen; `(m *Model).maybeShowUpdateNotice()` called from the WorkspaceStateMsg handler.

- [ ] **Step 1: Write the failing test** (append to `internal/tui/update_test.go`)

```go
func TestMaybeShowUpdateNotice(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}}
	m.maybeShowUpdateNotice()
	if m.dialog != dialogUpdateNotice {
		t.Fatalf("dialog = %v, want dialogUpdateNotice", m.dialog)
	}

	// Second call for the same version: already notified → no dialog.
	m2 := &Model{version: "0.0.1", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}}
	m2.maybeShowUpdateNotice()
	if m2.dialog == dialogUpdateNotice {
		t.Error("second notice for same version shown, want suppressed")
	}

	// A modal other than the disclaimer blocks the notice.
	m3 := &Model{version: "0.0.1", dialog: dialogPluginMigration, updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true}}
	m3.maybeShowUpdateNotice()
	if m3.dialog != dialogPluginMigration {
		t.Error("notice replaced migration dialog, want migration kept")
	}

	// The disclaimer yields to the notice (spec: update notice > disclaimer).
	m4 := &Model{version: "0.0.1", dialog: dialogDisclaimer, updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true}}
	m4.maybeShowUpdateNotice()
	if m4.dialog != dialogUpdateNotice {
		t.Error("notice did not replace disclaimer, want replaced")
	}

	// Up to date → no dialog.
	m5 := &Model{version: "0.0.2", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2"}}
	m5.maybeShowUpdateNotice()
	if m5.dialog == dialogUpdateNotice {
		t.Error("notice shown when up to date")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test` — FAIL, `dialogUpdateNotice` / `maybeShowUpdateNotice` undefined.

- [ ] **Step 3: Implement**

In `internal/tui/model.go`, append to the `dialogScreen` iota block (after the LAST existing constant — appending avoids renumbering persisted-free but switch-matched values):

```go
	dialogUpdateNotice
```

In the `case WorkspaceStateMsg:` handler, directly after the `m.updateInfo = msg.Update` line added in Task 7:

```go
		m.maybeShowUpdateNotice()
```

Append to `internal/tui/update.go` (imports: `"github.com/artyomsv/quil/internal/config"`, `"github.com/artyomsv/quil/internal/update"`, `"strings"`, and lipgloss alias as used in dialog.go):

```go
// maybeShowUpdateNotice opens the once-per-version startup dialog.
// Priority: the migration dialog (blocking) and any interactive dialog win;
// only the informational disclaimer yields (spec: migration > update notice
// > disclaimer — the disclaimer reappears next launch).
func (m *Model) maybeShowUpdateNotice() {
	if !updateAvailable(m.updateInfo, m.version) {
		return
	}
	if m.dialog != dialogNone && m.dialog != dialogDisclaimer {
		return
	}
	if update.LoadNotifiedVersion(config.UpdateNotifiedPath()) == m.updateInfo.LatestVersion {
		return
	}
	m.dialog = dialogUpdateNotice
	m.dialogCursor = 0
	if err := update.SaveNotifiedVersion(config.UpdateNotifiedPath(), m.updateInfo.LatestVersion); err != nil {
		log.Printf("save update notified marker: %v", err)
	}
}

// handleUpdateNoticeKey drives the two-button startup notice
// (OK / Update now), mirroring the disclaimer's cursor idiom.
func (m Model) handleUpdateNoticeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case "left", "right", "tab":
		m.dialogCursor = 1 - m.dialogCursor
		return m, nil
	case "enter":
		if m.dialogCursor == 1 {
			return m.handleUpdateAction()
		}
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	}
	return m, nil
}

// renderUpdateNoticeDialog renders the once-per-version startup notice.
func (m Model) renderUpdateNoticeDialog() string {
	info := m.updateInfo
	if info == nil {
		return ""
	}
	var b strings.Builder
	title := dialogTitle.Render("Update available")
	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, title))
	b.WriteString("\n\n")
	b.WriteString(dialogNormal.Render("  Installed: v" + m.version))
	b.WriteByte('\n')
	b.WriteString(dialogNormal.Render("  Latest:    v" + info.LatestVersion))
	b.WriteString("\n\n")
	switch {
	case info.StagedVersion == info.LatestVersion:
		b.WriteString(dialogSubtle.Render("  Downloaded and staged — applies on next launch."))
	case !info.InstallWritable:
		b.WriteString(dialogSubtle.Render("  Install dir not writable — update manually:"))
	default:
		b.WriteString(dialogSubtle.Render("  Will download in the background."))
	}
	b.WriteByte('\n')
	if info.ReleaseURL != "" {
		b.WriteString(dialogSubtle.Render("  " + info.ReleaseURL))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	okLabel := "  OK  "
	updateLabel := "  Update now  "
	okStyle, updateStyle := dialogSelected, dialogNormal
	if m.dialogCursor == 1 {
		okStyle, updateStyle = dialogNormal, dialogSelected
	}
	buttons := okStyle.Render(okLabel) + "    " + updateStyle.Render(updateLabel)
	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, buttons))
	return b.String()
}
```

(Adapt the style names `dialogTitle`/`dialogNormal`/`dialogSubtle`/`dialogSelected` and `dialogWidth` to the identifiers actually used by `renderDisclaimerDialog` in dialog.go — they exist there; copy exactly.)

In `internal/tui/dialog.go`:

1. `handleDialogKey` switch — add:

```go
	case dialogUpdateNotice:
		return m.handleUpdateNoticeKey(msg)
```

2. The render dispatch switch (line ~700) — add:

```go
	case dialogUpdateNotice:
		width = dialogWidth
		content = m.renderUpdateNoticeDialog()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — PASS. `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/dialog.go internal/tui/update.go internal/tui/update_test.go
git commit -m "feat(tui): once-per-version update notice dialog on startup"
```

---

### Task 10: Apply flow — binary swap + respawn + version-gate auto-confirm

**Files:**
- Create: `cmd/quil/update_apply.go`
- Modify: `cmd/quil/version_gate.go` (auto-confirm branch)
- Test: `cmd/quil/update_apply_test.go`

**Interfaces:**
- Consumes: `update.FindStaged/VerifyStaged/BinaryNames` (Task 3), `config.UpdateDir()`, `versionpkg.Current()/IsRelease()/Compare`, existing `findDaemonBinaryForUpgrade()`, `daemonBinary` ldflag var.
- Produces: `maybeApplyStagedUpdate(preConfirmed bool) bool` and `cleanupAppliedUpdate()` — consumed by Task 11; `swapBinaries(stagedDir string) error`, `swapOne(target, staged string) error`, `copyFile(src, dst string) error` (internal); env contract `QUIL_UPDATE_RESTART=1` read by `version_gate.go`.

- [ ] **Step 1: Write the failing tests** (`cmd/quil/update_apply_test.go`)

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSwapOne_ReplacesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	staged := filepath.Join(dir, "staged-quil.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)
	os.WriteFile(staged, []byte("new-binary"), 0755)

	if err := swapOne(target, staged); err != nil {
		t.Fatalf("swapOne: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new-binary" {
		t.Errorf("target content = %q, want new-binary", got)
	}
	backup, _ := os.ReadFile(target + ".old")
	if string(backup) != "old-binary" {
		t.Errorf("backup content = %q, want old-binary", backup)
	}
}

func TestSwapOne_MissingStaged_RollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quil.exe")
	os.WriteFile(target, []byte("old-binary"), 0755)

	if err := swapOne(target, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("swapOne with missing staged = nil error, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("target after rollback = %q (err %v), want old-binary restored", got, err)
	}
}

func TestUpdateRestartPreapproved(t *testing.T) {
	t.Setenv("QUIL_UPDATE_RESTART", "")
	if updateRestartPreapproved() {
		t.Error("preapproved with empty env, want false")
	}
	t.Setenv("QUIL_UPDATE_RESTART", "1")
	if !updateRestartPreapproved() {
		t.Error("not preapproved with env=1, want true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test` — FAIL, `swapOne` / `updateRestartPreapproved` undefined.

- [ ] **Step 3: Implement**

Create `cmd/quil/update_apply.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/update"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// maybeApplyStagedUpdate applies a fully-staged newer release: verify →
// prompt (skipped when preConfirmed — the TUI confirm already asked) →
// swap binaries → respawn quil. Returns true when the caller must return
// immediately because the new binary ran (this process acted as a thin
// wrapper and the session is over). Every failure path rolls back and
// returns false so the old version launches normally.
func maybeApplyStagedUpdate(preConfirmed bool) bool {
	if !versionpkg.IsRelease() {
		return false
	}
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err != nil || man == nil {
		return false
	}
	cmp, err := versionpkg.Compare(man.Version, versionpkg.Current())
	if err != nil || cmp <= 0 {
		return false
	}
	// Corruption/tamper gate: re-hash staged files against the manifest.
	if err := update.VerifyStaged(dir, man); err != nil {
		log.Printf("staged update v%s failed verification: %v — discarding", man.Version, err)
		os.RemoveAll(dir)
		return false
	}
	if !preConfirmed && !promptApplyUpdate(man.Version) {
		return false
	}
	if err := swapBinaries(dir); err != nil {
		fmt.Fprintf(os.Stderr, "update to v%s failed: %v — continuing on v%s\n",
			man.Version, err, versionpkg.Current())
		return false
	}
	log.Printf("update: swapped binaries to v%s, respawning", man.Version)
	return respawnSelf()
}

// promptApplyUpdate asks on the terminal, version-gate style. Default is
// YES (plain Enter applies): consent to auto-update was given via
// [update] auto = true, and this prompt fires at a natural restart moment.
func promptApplyUpdate(ver string) bool {
	fmt.Fprintf(os.Stderr,
		"\n"+
			"  Quil update ready.\n"+
			"\n"+
			"    Installed: %s\n"+
			"    Staged:    %s\n"+
			"\n"+
			"  Applying restarts the daemon; panes respawn (claude sessions\n"+
			"  resume, in-flight shell commands are killed).\n"+
			"\n"+
			"  Apply now? [Y/n] ",
		versionpkg.Current(), ver,
	)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "" || a == "y" || a == "yes"
}

// swapBinaries installs the staged quil and quild over the live install.
// If the second swap fails, the first is rolled back so the pair never
// splits versions.
func swapBinaries(stagedDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	names := update.BinaryNames(runtime.GOOS)
	quilName, quildName := names[0], names[1]

	quilTarget := exe
	quildTarget := findDaemonBinaryForUpgrade()
	if !filepath.IsAbs(quildTarget) {
		return fmt.Errorf("cannot locate installed quild (got %q)", quildTarget)
	}

	if err := swapOne(quilTarget, filepath.Join(stagedDir, quilName)); err != nil {
		return err
	}
	if err := swapOne(quildTarget, filepath.Join(stagedDir, quildName)); err != nil {
		// Roll the first swap back so quil/quild stay version-matched.
		os.Remove(quilTarget)
		if rbErr := os.Rename(quilTarget+".old", quilTarget); rbErr != nil {
			return fmt.Errorf("%w (AND quil rollback failed: %v — restore %s.old manually)", err, rbErr, quilTarget)
		}
		return err
	}
	return nil
}

// swapOne backs the target up as <target>.old (renaming a running
// executable is legal on Windows — NT locks the image by open handle, not
// path) and copies the staged binary into place. On failure the backup is
// renamed back.
func swapOne(target, staged string) error {
	backup := target + ".old"
	os.Remove(backup) // stale backup from a previous update (best-effort)
	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("back up %s: %w", target, err)
	}
	if err := copyFile(staged, target); err != nil {
		if rbErr := os.Rename(backup, target); rbErr != nil {
			return fmt.Errorf("install %s: %w (AND rollback failed: %v — restore %s manually)", target, err, rbErr, backup)
		}
		return fmt.Errorf("install %s: %w", target, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	if closeErr := out.Close(); cpErr == nil {
		cpErr = closeErr
	}
	return cpErr
}

// respawnSelf runs the freshly-installed quil with the original args and
// QUIL_UPDATE_RESTART=1 (the version gate reads it to skip the second
// restart prompt). This process stays as a thin wrapper waiting for the
// child — exiting immediately would hand the terminal back to the shell
// while the child TUI still owns it. Always returns true: the swap is
// already done, so even on spawn failure the caller must not fall through
// to running the (renamed-away) old binary's launch path — the user just
// relaunches manually.
func respawnSelf() bool {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "update applied — relaunch quil manually (%v)\n", err)
		return true
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "QUIL_UPDATE_RESTART=1")
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "update applied but relaunch failed: %v — run quil again\n", err)
	}
	return true
}

// cleanupAppliedUpdate removes .old backups and the staged dir once the
// running version has caught up with the staged one. Best-effort: on
// Windows the wrapper parent from the apply respawn may still hold
// quil.exe.old open as its process image, so deletion can fail — the next
// launch (no wrapper) retries.
func cleanupAppliedUpdate() {
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err == nil && man != nil {
		if cmp, cErr := versionpkg.Compare(man.Version, versionpkg.Current()); cErr == nil && cmp <= 0 {
			os.RemoveAll(dir)
		}
	}
	if exe, exeErr := os.Executable(); exeErr == nil {
		os.Remove(exe + ".old")
	}
	if quild := findDaemonBinaryForUpgrade(); filepath.IsAbs(quild) {
		os.Remove(quild + ".old")
	}
}

// updateRestartPreapproved reports whether this process was respawned by
// the apply path — the user already confirmed the daemon restart there, so
// the version gate must not prompt a second time.
func updateRestartPreapproved() bool {
	return os.Getenv("QUIL_UPDATE_RESTART") == "1"
}
```

In `cmd/quil/version_gate.go`, in `gateVersionCheck`'s `default:` branch, replace:

```go
		if !promptRestartDaemon(versionpkg.Current(), res.DaemonVersion, res.DaemonUnknown) {
```

with:

```go
		// A staged-update respawn already got user consent at the apply
		// prompt — asking again here would double-prompt every update.
		if !updateRestartPreapproved() && !promptRestartDaemon(versionpkg.Current(), res.DaemonVersion, res.DaemonUnknown) {
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` — PASS. `./scripts/dev.sh vet` — clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/update_apply.go cmd/quil/update_apply_test.go cmd/quil/version_gate.go
git commit -m "feat(quil): apply staged update via binary swap and respawn"
```

---

### Task 11: Wire apply into launch + post-exit paths

**Files:**
- Modify: `cmd/quil/main.go` (`launchTUI`)

**Interfaces:**
- Consumes: `maybeApplyStagedUpdate`, `cleanupAppliedUpdate` (Task 10); `tui.Model.ApplyUpdateRequested()` (Task 8).

- [ ] **Step 1: Implement launch-time hook**

In `launchTUI` (cmd/quil/main.go), right BEFORE the `sockPath := config.SocketPath()` / first `ipc.NewClient` block (line ~274), add:

```go
	// Staged auto-update: apply before touching the daemon. On success the
	// new binary was respawned and has already run the whole session — this
	// process was just a wrapper. On decline/failure, fall through to a
	// normal launch; cleanup only runs when nothing is being applied.
	if maybeApplyStagedUpdate(false) {
		return
	}
	cleanupAppliedUpdate()
```

- [ ] **Step 2: Implement post-exit hook**

In the same function, inside the `if m, ok := finalModel.(tui.Model); ok {` block (line ~344), after the `ConfigChanged` handling, add:

```go
		// About → Update now / notice → Update now: the confirm dialog
		// already asked, so apply pre-confirmed. The respawned TUI runs a
		// fresh session; this process waits as a wrapper.
		if m.ApplyUpdateRequested() {
			if maybeApplyStagedUpdate(true) {
				return
			}
		}
```

(The `defer client.Close()` above is fine — by the time the wrapper returns, the socket conn is long dead; Close on a dead conn just logs.)

- [ ] **Step 3: Build + full test suite**

Run: `./scripts/dev.sh build` — all six binaries build. `./scripts/dev.sh test` — PASS. `./scripts/dev.sh vet` — clean.

- [ ] **Step 4: Manual smoke test (dev daemon, mock apply)**

Dev builds skip the pipeline (`IsRelease()` false), so the automated tests carry correctness here; the smoke test verifies nothing regressed at launch:

1. `./scripts/quil-dev.ps1` — TUI starts, `[dev]` visible, no update segment (dev build), F1 → About shows "Check for updates (up to date)" row above "Stop daemon", Enter on it flashes "quil is up to date".
2. Stop dev daemon by PID from `./.quil/quild.pid` when done.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/main.go
git commit -m "feat(quil): apply staged updates at launch and on Update-now exit"
```

---

### Task 12: Documentation

**Files:**
- Modify: `docs/configuration.md` (add `[update]` section), `docs/features.md` (auto-update entry), `.claude/CLAUDE.md` (architecture bullet)

- [ ] **Step 1: `docs/configuration.md`** — add a section following the file's existing per-section format (table of keys with defaults):

```markdown
## [update]

Automatic update checking and staging.

| Key | Default | Description |
|-----|---------|-------------|
| `check` | `true` | Daily check for new releases (one unauthenticated GET to `api.github.com`). Set `false` to disable all update networking. |
| `auto` | `true` | Download and stage new releases in the background. The update applies at the next `quil` launch after a single `[Y/n]` confirmation. Set `false` for notify-only. |

A pending update shows as `↑ v<version>` in the status bar (`ready` once
staged), in F1 → About, and once per version as a startup dialog. Dev
builds and installs in non-writable locations (package managers) never
self-update; the latter show the release page URL instead.
```

- [ ] **Step 2: `docs/features.md`** — add an entry in the appropriate group (match surrounding entry style):

```markdown
- **Auto-update** — the daemon checks GitHub daily for new releases,
  downloads and verifies them (sha256) in the background, and stages them
  under `~/.quil/update/`. The next `quil` launch applies the update with
  one confirmation and restarts the daemon; tabs, layouts, CWDs, notes,
  and Claude sessions are preserved via the workspace snapshot. Configure
  via `[update]` in `config.toml`; About (F1) has a manual "Update now".
```

- [ ] **Step 3: `.claude/CLAUDE.md`** — add one bullet to the Key Conventions list (keep the file's dense style):

```markdown
- Auto-update: `internal/update/` (stdlib-only: GitHub `releases/latest` checker, sha256-verified download, extract to `$QUIL_HOME/update/staged/<ver>/` with `manifest.json` written LAST as the atomic completion marker; daemon-owned `state.json`, TUI-owned `notified.json` — single writer per file). Daemon: `updateChecker` goroutine (`internal/daemon/update.go`, 1 min after listen then every 24 h; gated on `[update] check` + `version.IsRelease()` + exe-dir writability probe; `[update] auto` gates staging) publishes `ipc.UpdateInfo` under the broadcast-only `update` key of `workspace_state` and serves `MsgStageUpdateReq` (on-demand stage, worker goroutine — never on the dispatch goroutine, single-flight via `updateStaging` atomic). TUI (`internal/tui/update.go`): status-bar `↑ vX [ready]` segment, dynamic About row (`aboutUpdateIndex`, Stop daemon moved to index 8), once-per-version `dialogUpdateNotice` (replaces the disclaimer, yields to migration), `confirmKindApplyUpdate` quits with `Model.applyUpdateOnExit`. Apply (`cmd/quil/update_apply.go`): verify manifest hashes → `[Y/n]` prompt (skipped when pre-confirmed) → rename-aside swap (`<bin>.old` backup, pair-rollback) → respawn self as wrapper with `QUIL_UPDATE_RESTART=1`, which makes the version gate skip its own restart prompt; `.old` + staged dir cleaned on the next launch where versions match
```

- [ ] **Step 4: Commit**

```bash
git add docs/configuration.md docs/features.md .claude/CLAUDE.md
git commit -m "docs: document auto-update pipeline and [update] config"
```

---

## Post-plan verification (whole feature)

- `./scripts/dev.sh test-race` — full suite under the race detector (updateMu/updateStaging paths).
- End-to-end against a mock release server is deliberately NOT automated in this plan (would need a `QUIL_UPDATE_URL` override env threaded through `update.Checker`; the checker already supports `BaseURL` for tests — a follow-up if manual QA finds gaps).
- Real-world validation happens on the first release that ships after this lands: the previous installed release's daemon must detect, stage, and apply it.

## Self-review notes (already folded in)

- Spec coverage: check/stage (T2-4, 6), IPC + 3 surfaces (T5, 7-9), apply + gate auto-confirm (T10-11), config (T1), docs (T12), on-demand stage for `auto = false` (T6 handler + T8 action). Edge cases: dev builds (gates in T6/T7/T10), unwritable install (probe T4, degraded labels T8/T9), crash mid-stage (manifest-last T3), second TUI (existing gate, no code), GitHub unreachable (debug-log T6), swap failure rollback (T10).
- Type consistency: `ipc.UpdateInfo` shared daemon/TUI; `update.State` daemon-only; `FindStaged/VerifyStaged` signatures identical in T3 (producer) and T10 (consumer); `aboutStopDaemonIndex` moves 7→8 in exactly one place (T8) with tests referencing the constant.
