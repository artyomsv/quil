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
	"strconv"
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
//
// A symbolic link is an error too — on a pinned file, on any directory
// between it and the profile's directory, or inside a kit. A link
// cannot be pinned by its text (an agent changes the file it points to and
// the digest still matches), and pinning what it points to would pin a file
// outside the repository that nobody reviewed with the profile.
func PinnedFiles(p Profile) ([]string, error) {
	set := map[string]bool{}
	add := func(path string) error {
		if err := refuseLinks(p.Dir, path); err != nil {
			return err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st.Mode().IsRegular() {
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
		if err := refuseLinks(p.Dir, dir); err != nil {
			return nil, fmt.Errorf("kit %s: %w", dir, err)
		}
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
			// An absolute argument is a system command (/bin/bash), not one
			// of the profile's own scripts, and a system path may well run
			// through a link (/bin -> usr/bin).
			if a == "" || strings.HasPrefix(a, "-") || filepath.IsAbs(a) {
				continue
			}
			cand := p.resolve(a)
			if _, err := os.Lstat(cand); err != nil {
				continue // not a file: a command name, a flag value, a word
			}
			if err := add(cand); err != nil {
				return nil, fmt.Errorf("argument %s: %w", a, err)
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

// Digest hashes the pinned files: each file's path and content.
func Digest(p Profile) (string, []string, error) {
	files, _, digest, err := readPinned(p)
	return digest, files, err
}

// readPinned reads every pinned file ONCE and hashes the bytes it read, so
// a caller holding the contents holds exactly what the digest describes.
func readPinned(p Profile) ([]string, map[string][]byte, string, error) {
	files, err := PinnedFiles(p)
	if err != nil {
		return nil, nil, "", err
	}
	h := sha256.New()
	contents := make(map[string][]byte, len(files))
	var total int64
	for _, f := range files {
		body, err := readRegular(f, maxPinnedBytes-total)
		if err != nil {
			return nil, nil, "", err
		}
		total += int64(len(body))
		sum := sha256.Sum256(body)
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(f), hex.EncodeToString(sum[:]))
		contents[f] = body
	}
	return files, contents, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// readRegular reads a regular file, at most limit bytes. It refuses a file
// that turned into something else between the check and the open — a link
// swapped in at that moment would otherwise be followed.
func readRegular(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fst, err := f.Stat(); err != nil || !os.SameFile(st, fst) {
		return nil, fmt.Errorf("%s changed while it was read", path)
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("pinned files exceed %d MiB; refusing to pin", maxPinnedBytes>>20)
	}
	return body, nil
}

// refuseLinks reports an error when path, or any directory between it and
// base, is a symbolic link. Directories at and above base are the user's own
// layout and are not checked.
func refuseLinks(base, path string) error {
	for p := filepath.Clean(path); !ancestorOrSelf(p, base); {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; a sandbox profile's files must be real files "+
				"(replace the link with the file it points to)", p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
	return nil
}

// ancestorOrSelf reports whether dir is of or one of its parents.
func ancestorOrSelf(dir, of string) bool {
	rel, err := filepath.Rel(dir, of)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Approved is a profile whose pinned files matched the user's approval,
// together with the bytes that were read when they matched.
//
// The repository stays writable after the check, and preparing a sandbox
// takes seconds before the host step runs. So the host step never runs a
// script from the repository: it runs a private copy of the approved bytes
// (hostArgv). What sbx itself reads — the environment file and its kits — is
// checked once more right before the create (recheck); sbx reads those by
// path, and a kit's relative sources mean they cannot be moved to a copy.
type Approved struct {
	Profile
	digest string
	files  map[string][]byte
}

// Approve checks the profile against the approval store and returns it with
// the bytes it was checked on. It returns an *ErrUntrusted when the profile
// was never approved or changed since, and any other error when the files
// cannot be read.
func Approve(storePath string, p Profile) (Approved, error) {
	_, contents, digest, err := readPinned(p)
	if err != nil {
		return Approved{}, err
	}
	m, err := loadTrust(storePath)
	if err != nil {
		return Approved{}, err
	}
	e, ok := m[trustKey(p.Path)]
	switch {
	case !ok:
		return Approved{}, &ErrUntrusted{ProfilePath: p.Path}
	case e.Digest != digest:
		return Approved{}, &ErrUntrusted{ProfilePath: p.Path, Changed: true}
	}
	return Approved{Profile: p, digest: digest, files: contents}, nil
}

// recheck reads the pinned files again and refuses when they no longer match
// what was approved.
func (a Approved) recheck() error {
	digest, _, err := Digest(a.Profile)
	if err != nil {
		return err
	}
	if digest != a.digest {
		return &ErrUntrusted{ProfilePath: a.Path, Changed: true}
	}
	return nil
}

// hostArgv is the host step's argv with every approved script replaced by a
// private copy of its approved bytes, in a new directory only this user can
// read. The caller runs it and then calls cleanup.
//
// The copy is why a host script must not find other files through its own
// path ("$(dirname "$0")"): it runs in the profile's directory, so it uses
// paths relative to that.
func (a Approved) hostArgv() (argv []string, cleanup func(), err error) {
	argv = append([]string(nil), a.PrepareHost...)
	var tmp string
	cleanup = func() {
		if tmp != "" {
			_ = os.RemoveAll(tmp)
		}
	}
	for i, arg := range argv {
		if arg == "" || strings.HasPrefix(arg, "-") || filepath.IsAbs(arg) {
			continue
		}
		cand := filepath.Clean(a.resolve(arg))
		body, pinned := a.files[cand]
		if !pinned {
			if st, err := os.Lstat(cand); err == nil && !st.IsDir() {
				cleanup()
				return nil, nil, fmt.Errorf("prepare_host argument %s names a file the approval does not cover; "+
					"run: quil sandbox trust %s", arg, a.Dir)
			}
			continue
		}
		if tmp == "" {
			if tmp, err = os.MkdirTemp("", "quil-sbx-host-"); err != nil {
				return nil, nil, err
			}
		}
		dst := filepath.Join(tmp, strconv.Itoa(i), filepath.Base(cand))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			cleanup()
			return nil, nil, err
		}
		if err := os.WriteFile(dst, body, 0o700); err != nil {
			cleanup()
			return nil, nil, err
		}
		argv[i] = dst
	}
	return argv, cleanup, nil
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
	_, err := Approve(storePath, p)
	return err
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
