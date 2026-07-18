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
	Client *http.Client // nil → 5 min-timeout default (per request)
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
