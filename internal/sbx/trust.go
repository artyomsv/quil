package sbx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The trust pin.
//
// The profile lives in the repository, and the repository is exactly what the
// sandboxed agent can write. Without a pin an agent could edit the host script
// and have Quil run it ON THE HOST the next time a sandbox is created, or widen
// its own cage — more workspaces, a larger network allowlist — through the
// environment file and its kits. So the user approves a profile once
// (`quil sandbox trust`), Quil records a digest of every file that decides
// what runs on the host or what the cage allows, and a pane whose files no
// longer match is REFUSED until the user looks again. The refusal never
// softens into "run it anyway": a pane the user asked to isolate must not
// quietly run under a cage someone else redrew.

// Pin bounds. A kit is a handful of small files; anything near these limits
// is a mistake (a kit source pointing at a whole tree), and hashing it on
// every pane spawn would be slow.
const (
	maxPinnedFiles = 2000
	maxPinnedBytes = 64 << 20
)

// TrustStorePath is where approved digests are kept, beside the docker
// backend's own sandbox state.
func TrustStorePath(quilDir string) string {
	return filepath.Join(quilDir, "sandbox", "sbx-trust.json")
}

// PinnedFiles lists every file whose content the digest covers, sorted.
//
// That is: the profile itself; the environment file; every file under each
// LOCAL kit the environment file names (sources starting "./" or "../" —
// anything else is a registry reference sbx pulls itself); the post-create
// env file; and every prepare_host / post_create argument that names an
// existing file relative to the profile directory (the scripts).
//
// A missing environment file is an error: a digest that silently left it out
// would be a pin that does not pin the cage.
func PinnedFiles(p Profile) ([]string, error) {
	set := map[string]bool{}
	add := func(path string) error {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st.Mode().IsRegular() || st.Mode()&fs.ModeSymlink != 0 {
			set[filepath.Clean(path)] = true
		}
		return nil
	}

	if err := add(p.Path); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	if err := add(p.EnvFile); err != nil {
		return nil, fmt.Errorf("env_file: %w", err)
	}
	kits, err := localKitDirs(p.EnvFile)
	if err != nil {
		return nil, err
	}
	for _, dir := range kits {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if len(set) >= maxPinnedFiles {
				return fmt.Errorf("more than %d files under the kits; refusing to pin", maxPinnedFiles)
			}
			return add(path)
		})
		if err != nil {
			return nil, fmt.Errorf("kit %s: %w", dir, err)
		}
	}
	if p.PostCreateEnvFile != "" {
		if err := add(p.PostCreateEnvFile); err != nil {
			return nil, fmt.Errorf("post_create_env_file: %w", err)
		}
	}
	argvs := append([][]string{p.PrepareHost}, p.PostCreate...)
	for _, argv := range argvs {
		for _, a := range argv {
			if a == "" || strings.HasPrefix(a, "-") {
				continue
			}
			cand := p.resolve(a)
			if st, err := os.Stat(cand); err == nil && st.Mode().IsRegular() {
				set[filepath.Clean(cand)] = true
			}
		}
	}

	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// localKitDirs reads the kit sources the environment file names and returns
// the local ones as absolute paths.
//
// Only the `kits: [{source: ...}]` shape is read. An environment file this
// does not understand still has its own bytes pinned; only its kits would be
// missed, which is why the docs ask for kits under the environment file's
// directory with a "./" prefix — the form sbx itself requires.
func localKitDirs(envFile string) ([]string, error) {
	raw, err := os.ReadFile(envFile)
	if err != nil {
		return nil, fmt.Errorf("env_file: %w", err)
	}
	var doc struct {
		Kits []struct {
			Source string `yaml:"source"`
		} `yaml:"kits"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		// Pinned as bytes anyway; sbx will report the syntax error itself.
		return nil, nil
	}
	base := filepath.Dir(envFile)
	var out []string
	for _, k := range doc.Kits {
		if !IsLocalKitSource(k.Source) {
			continue
		}
		out = append(out, filepath.Join(base, filepath.FromSlash(k.Source)))
	}
	return out, nil
}

// IsLocalKitSource reports whether sbx treats a kit source as a local path.
// sbx reads anything else — including a bare "mixins/x" — as a registry
// image reference.
func IsLocalKitSource(s string) bool {
	return strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}

// Digest hashes the pinned files: each file's path and content (a symlink's
// TARGET, never what it points at, so a link swapped to another file
// changes the digest).
func Digest(p Profile) (string, []string, error) {
	files, err := PinnedFiles(p)
	if err != nil {
		return "", nil, err
	}
	h := sha256.New()
	var total int64
	for _, f := range files {
		fh, n, err := hashFile(f)
		if err != nil {
			return "", nil, err
		}
		total += n
		if total > maxPinnedBytes {
			return "", nil, fmt.Errorf("pinned files exceed %d MiB; refusing to pin", maxPinnedBytes>>20)
		}
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(f), fh)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), files, nil
}

func hashFile(path string) (string, int64, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	if st.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", 0, err
		}
		h.Write([]byte("symlink:" + target))
		return hex.EncodeToString(h.Sum(nil)), int64(len(target)), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(h, io.LimitReader(f, maxPinnedBytes+1))
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// trustEntry is one approved profile.
type trustEntry struct {
	Digest    string    `json:"digest"`
	TrustedAt time.Time `json:"trusted_at"`
}

// ErrUntrusted reports a profile that was never approved or changed since.
type ErrUntrusted struct {
	ProfilePath string
	// Changed is true when an approval exists for an older digest.
	Changed bool
}

func (e *ErrUntrusted) Error() string {
	what := "has not been approved"
	if e.Changed {
		what = "changed since you approved it"
	}
	return fmt.Sprintf("sandbox profile %s %s. Review it and the files it names, then run: quil sandbox trust %s",
		e.ProfilePath, what, filepath.Dir(e.ProfilePath))
}

func loadTrust(storePath string) (map[string]trustEntry, error) {
	raw, err := os.ReadFile(storePath)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]trustEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]trustEntry{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", storePath, err)
	}
	return m, nil
}

// CheckTrust returns nil when the profile's current digest is the approved
// one, an *ErrUntrusted when it is not, and any other error when the files
// cannot be read.
func CheckTrust(storePath string, p Profile) error {
	digest, _, err := Digest(p)
	if err != nil {
		return err
	}
	m, err := loadTrust(storePath)
	if err != nil {
		return err
	}
	e, ok := m[trustKey(p.Path)]
	switch {
	case !ok:
		return &ErrUntrusted{ProfilePath: p.Path}
	case e.Digest != digest:
		return &ErrUntrusted{ProfilePath: p.Path, Changed: true}
	}
	return nil
}

// Trust records the profile's current digest as approved and returns the
// files it covers, so the caller can show the user what was pinned.
func Trust(storePath string, p Profile) ([]string, error) {
	digest, files, err := Digest(p)
	if err != nil {
		return nil, err
	}
	m, err := loadTrust(storePath)
	if err != nil {
		return nil, err
	}
	m[trustKey(p.Path)] = trustEntry{Digest: digest, TrustedAt: time.Now().UTC()}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		return nil, err
	}
	if err := atomicWrite(storePath, append(body, '\n'), 0o600); err != nil {
		return nil, err
	}
	return files, nil
}

// trustKey is the store key for a profile: its cleaned absolute path, with
// symlinks resolved where possible so two spellings of one file share an
// approval.
func trustKey(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	return filepath.ToSlash(filepath.Clean(path))
}

// atomicWrite writes via a temp file in the same directory and a rename.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
