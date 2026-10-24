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
	"errors"
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

// The refusals FetchVerified makes, as sentinels so a caller can tell "this
// release has nothing for my platform" from "the download was corrupted"
// without parsing the message.
var (
	// ErrNoAsset: the release carries no asset (archive or checksums file)
	// with the requested name.
	ErrNoAsset = errors.New("release has no such asset")
	// ErrNoChecksum: the checksums file was fetched but has no line for the
	// archive.
	ErrNoChecksum = errors.New("checksums file has no entry for the asset")
	// ErrChecksumMismatch: the archive downloaded, and its sha256 is not the
	// one the checksums file promises.
	ErrChecksumMismatch = errors.New("checksum mismatch")
)

// BinaryNames returns the REQUIRED executable names inside the release archive
// for a platform. An archive missing any of them is not a usable release.
func BinaryNames(goos string) []string {
	if goos == "windows" {
		return []string{"quil.exe", "quild.exe"}
	}
	return []string{"quil", "quild"}
}

// OptionalBinaryNames returns executables extracted when the archive carries
// them and skipped without complaint when it does not.
//
// quil-activate.exe is the windowless handler a desktop-toast click runs. It
// joined the release archive with the toast feature and NOT this updater, so
// every in-place `quil update` since installed the pair and dropped the helper
// — an install that has only ever upgraded therefore has no helper at all, and
// `quil notify setup` registers `quil.exe activate` instead: a console binary,
// whose window takes the foreground on every click and then vanishes. Silent,
// because the fallback routes correctly; it only looks wrong.
//
// It cannot simply join BinaryNames. That set is what extractBinaries REQUIRES,
// and every release published before the helper existed would stop staging —
// turning a downgrade away from a broken version into a dead end. Optionality
// is about the archives that already exist, not about how much the file
// matters.
//
// Verification does not need a tier of its own: VerifyStaged hashes every file
// the manifest DECLARES, so an extracted helper is covered by the same
// tamper gate as the pair, and an absent one declares nothing.
func OptionalBinaryNames(goos string) []string {
	if goos == "windows" {
		return []string{"quil-activate.exe"}
	}
	return nil
}

// Manifest records a completed staging: which files landed and their
// hashes. It is written LAST — its presence marks the stage as complete;
// its absence means "nothing staged" regardless of other files present.
type Manifest struct {
	Version  string            `json:"version"`
	Files    map[string]string `json:"files"` // base name → sha256 hex
	StagedAt string            `json:"staged_at"` // RFC3339
}

// SafeVersion rejects a release version that cannot be used as a single path
// component under staged/.
//
// A hostile tag ("v1.0.0-../../evil") survives semver parsing — version.Parsed
// strips the suffix after "-" — yet would escape the staging root once joined.
// Exported because Stage is no longer the only consumer of a version string
// that came off the wire: the daemon also persists it into state.json and
// broadcasts it as display text on a path that never reaches Stage at all.
func SafeVersion(v string) error {
	if v == "" || v != filepath.Base(v) || strings.ContainsAny(v, `/\`) || strings.Contains(v, "..") {
		return fmt.Errorf("release version %q is not a safe version string", v)
	}
	return nil
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
	if err := SafeVersion(rel.Version()); err != nil {
		return err
	}

	// Both assets must exist before any I/O — FetchVerified checks the same,
	// but FindAssets' error names the platform archive it looked for.
	archive, _, err := FindAssets(rel, s.GOOS, s.GOARCH)
	if err != nil {
		return err
	}

	tmpPath, err := FetchVerified(ctx, s.httpClient(), rel, archive.Name, ChecksumsName, s.Root)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)

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

// FetchVerified downloads the release asset named assetName into a temp file
// under dstDir and verifies its sha256 against the line for it in the
// release's checksumsName asset, BEFORE anything reads the archive content.
// It returns the archive path; the caller removes it when done. A nil client
// gets the Stager's 5 min default.
//
// Any failure leaves nothing behind. The refusals are typed: ErrNoAsset
// (either asset missing from the release), ErrNoChecksum (no line for the
// archive) and ErrChecksumMismatch — each wrapped with the names involved.
//
// Both names are parameters because Quil's own release is not the only
// layout: a plugin's upstream ships one checksums file per OS and names its
// archives by its own template, and only the caller knows either.
func FetchVerified(ctx context.Context, client *http.Client, rel *Release, assetName, checksumsName, dstDir string) (string, error) {
	archive := findAsset(rel, assetName)
	if archive == nil {
		return "", fmt.Errorf("release %s: %w: %s", rel.TagName, ErrNoAsset, assetName)
	}
	sums := findAsset(rel, checksumsName)
	if sums == nil {
		return "", fmt.Errorf("release %s: %w: %s", rel.TagName, ErrNoAsset, checksumsName)
	}
	if client == nil {
		client = (&Stager{}).httpClient()
	}

	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return "", fmt.Errorf("create update dir: %w", err)
	}

	// Download the archive to a temp file, hashing as we write.
	tmp, err := os.CreateTemp(dstDir, "download-*")
	if err != nil {
		return "", fmt.Errorf("create download temp: %w", err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpPath)
		}
	}()

	gotSum, err := downloadHashed(ctx, client, archive.DownloadURL, tmp)
	closeErr := tmp.Close()
	if err != nil {
		return "", fmt.Errorf("download %s: %w", archive.Name, err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close download temp: %w", closeErr)
	}

	// Fetch the checksums file and compare before touching the archive content.
	wantSums, err := fetchChecksums(ctx, client, sums.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", sums.Name, err)
	}
	want, ok := wantSums[archive.Name]
	if !ok {
		return "", fmt.Errorf("%s: %w: %s", sums.Name, ErrNoChecksum, archive.Name)
	}
	if !strings.EqualFold(want, gotSum) {
		return "", fmt.Errorf("%w for %s: got %s, want %s", ErrChecksumMismatch, archive.Name, gotSum, want)
	}
	keep = true
	return tmpPath, nil
}

// downloadHashed streams url into dst, returning the sha256 hex of the
// bytes written.
func downloadHashed(ctx context.Context, client *http.Client, url string, dst io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
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

// fetchChecksums downloads and parses a checksums file ("<hex>  <name>"
// lines, the sha256sum format; a leading "*" on the name, sha256sum's
// binary-mode marker, is dropped).
func fetchChecksums(ctx context.Context, client *http.Client, url string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
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
			sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}

// matchBinary returns the allowlisted binary name (the constant from names)
// that entryName's base matches, or "" if none match. The returned string is
// always one of our own constants — never derived from the archive entry —
// so the path written to disk cannot be influenced by a hostile entry name
// (breaks the CodeQL go/zipslip taint chain at the source).
//
// fold compares case-insensitively, for windows archives: the filesystem the
// binary lands on treats "Tool.EXE" and "tool.exe" as one name, and an
// upstream's packaging may spell it either way. The name WRITTEN is still
// ours.
func matchBinary(names []string, entryName string, fold bool) string {
	base := filepath.Base(entryName)
	for _, n := range names {
		if base == n || (fold && strings.EqualFold(base, n)) {
			return n
		}
	}
	return ""
}

// extractBinaries pulls only quil's own executables out of the archive into
// dir, ignoring archive paths entirely (base-name match only — immune to
// zip-slip by construction). Returns base name → sha256 of extracted bytes.
//
// Both tiers are extracted; only the REQUIRED tier is demanded back.
func extractBinaries(archivePath, dir, goos string) (map[string]string, error) {
	names := BinaryNames(goos)
	wanted := append(append([]string{}, names...), OptionalBinaryNames(goos)...)
	files, err := extractNamed(archivePath, dir, goos, wanted)
	if err != nil {
		return nil, err
	}

	// Named rather than counted. files now also holds whatever OPTIONAL entries
	// the archive happened to carry, so a length compare would pass an archive
	// with the helper and no quild — the split-pair case the count was there to
	// catch in the first place.
	for _, name := range names {
		if _, ok := files[name]; !ok {
			return nil, fmt.Errorf("archive missing required binary %s", name)
		}
	}
	return files, nil
}

// ExtractBinary pulls ONE executable out of a release archive into dstDir
// and returns its path. binaryName is the bare name ("stripe"); ".exe" is
// appended for windows unless already present. The entry is matched by base
// name wherever it sits in the archive, and written under dstDir at that
// constant name — extractNamed's zip-slip immunity, unchanged.
//
// This is the plugin-update path (`quild plugin-update`): one binary, no
// manifest, no tiers. Quil's own stage keeps extractBinaries above.
func ExtractBinary(archivePath, binaryName, goos, dstDir string) (string, error) {
	if binaryName == "" || binaryName != filepath.Base(binaryName) {
		return "", fmt.Errorf("binary name %q is not a bare file name", binaryName)
	}
	name := binaryName
	if goos == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	files, err := extractNamed(archivePath, dstDir, goos, []string{name})
	if err != nil {
		return "", err
	}
	if _, ok := files[name]; !ok {
		return "", fmt.Errorf("archive has no %s", name)
	}
	return filepath.Join(dstDir, name), nil
}

// extractNamed writes every archive entry whose BASE name is one of wanted
// to dir under that name, and returns name → sha256 for the ones it found.
// Archive paths are never used for anything but the base-name match, so a
// hostile "../../x" entry can only ever land as dir/x.
func extractNamed(archivePath, dir, goos string, wanted []string) (map[string]string, error) {
	files := make(map[string]string)
	fold := goos == "windows"

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
			name := matchBinary(wanted, f.Name, fold)
			if name == "" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			err = writeOne(name, rc)
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
			name := matchBinary(wanted, hdr.Name, fold)
			if hdr.Typeflag != tar.TypeReg || name == "" {
				continue
			}
			if err := writeOne(name, tr); err != nil {
				return nil, err
			}
		}
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
// staged dir and requires the manifest to cover every name in required —
// the pre-swap corruption/tamper gate. Callers pass the names they are about
// to install (BinaryNames).
func VerifyStaged(dir string, m *Manifest, required []string) error {
	if m == nil {
		return errors.New("no staged manifest")
	}
	// Verifying only the DECLARED set is vacuous. The set of files the caller
	// INSTALLS is fixed (BinaryNames), but the set verified here came from the
	// manifest — and the manifest is attacker-controlled the moment anything
	// can write into the staged dir. A manifest with no "files" key hashed
	// nothing and still passed; one naming only quil let quild be installed
	// and executed unverified, a genuinely mismatched pair. Require the caller
	// to name what it is about to install, and refuse to vouch for anything
	// the manifest declines to cover.
	for _, name := range required {
		if _, ok := m.Files[name]; !ok {
			return fmt.Errorf("staged manifest does not cover %s — refusing to install unverified", name)
		}
	}
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
