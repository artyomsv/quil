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
	"reflect"
	"sort"
	"strings"
	"time"
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
// existing file, absolute or relative to the profile directory (the scripts;
// see fileArgs).
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
	// The host step may name ONE project file: its script. Quil runs a copy
	// of the approved bytes in its place (see Approved.hostArgv), and an
	// argument it cannot tell apart from the script — an input, or an output
	// the step writes — would either run unchecked or be written into the
	// copy and lost.
	//
	// An absolute COMMAND outside the profile's directory is refused: Quil
	// cannot tell a system program from a script in a folder the agent can
	// write — the worktrees folder next to the repository is shared with the
	// sandbox. A bare name ("sh") is looked up on PATH, never in the project.
	if len(p.PrepareHost) > 0 {
		cmd := p.PrepareHost[0]
		if filepath.IsAbs(cmd) && !ancestorOrSelf(p.Dir, filepath.Clean(cmd)) {
			return nil, fmt.Errorf("prepare_host command %s is an absolute path outside %s; name a program "+
				"on PATH (\"sh\") or a script in the project", cmd, p.Dir)
		}
	}
	host, err := p.fileArgs(p.PrepareHost)
	if err != nil {
		return nil, fmt.Errorf("prepare_host: %w", err)
	}
	if len(host) > 1 {
		names := make([]string, len(host))
		for i, f := range host {
			names[i] = p.PrepareHost[f.index]
		}
		return nil, fmt.Errorf("prepare_host names %d project files (%s); it may name only its script — "+
			"put the other steps inside the script", len(host), strings.Join(names, ", "))
	}
	for _, f := range host {
		set[f.path] = true
	}
	for i, step := range p.PostCreate {
		files, err := p.fileArgs(step)
		if err != nil {
			return nil, fmt.Errorf("post_create[%d]: %w", i, err)
		}
		for _, f := range files {
			set[f.path] = true
		}
	}

	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// fileArg is an argv element that names an existing file.
type fileArg struct {
	index int
	path  string
}

// fileArgs returns the arguments of argv that name an existing file, absolute
// or relative to the profile's directory — however a script is spelt, it is
// still the repository's file. The one exception is an absolute COMMAND
// (argv[0]) outside the profile's directory, which a post_create step uses to
// name a program inside the sandbox; PinnedFiles refuses one in prepare_host.
//
// A link is refused: on the file itself always, and on the folders between it
// and the profile's directory when the file is under it.
func (p Profile) fileArgs(argv []string) ([]fileArg, error) {
	var out []fileArg
	for i, a := range argv {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		cand := filepath.Clean(p.resolve(a))
		if i == 0 && filepath.IsAbs(a) && !ancestorOrSelf(p.Dir, cand) {
			continue
		}
		st, err := os.Lstat(cand)
		if err != nil || st.IsDir() {
			continue // not a file: a command name, a flag value, a word
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("argument %s: %s is a symbolic link; a sandbox profile's files must be "+
				"real files (replace the link with the file it points to)", a, cand)
		}
		if !filepath.IsAbs(a) || ancestorOrSelf(p.Dir, cand) {
			if err := refuseLinks(p.Dir, cand); err != nil {
				return nil, fmt.Errorf("argument %s: %w", a, err)
			}
		}
		if st.Mode().IsRegular() {
			out = append(out, fileArg{index: i, path: cand})
		}
	}
	return out, nil
}

// localKitDirs reads the environment file and returns its local kits as
// absolute paths (envKitDirs).
func localKitDirs(envFile string) ([]string, error) {
	raw, err := os.ReadFile(envFile)
	if err != nil {
		return nil, fmt.Errorf("env_file: %w", err)
	}
	return envKitDirs(raw, envFile)
}

// envKitDirs returns the local kits an environment file's bytes name, as
// absolute paths. It refuses a file with host lifecycle commands
// (errLifecycle) and a file it cannot interpret: the pin and the copy both
// come from here, so neither can miss a kit the other sees.
func envKitDirs(raw []byte, envFile string) ([]string, error) {
	doc, err := parseEnv(raw)
	if err != nil {
		return nil, fmt.Errorf("env_file %s: %w", envFile, err)
	}
	if doc.lifecycle {
		return nil, fmt.Errorf("env_file %s: %w", envFile, errLifecycle)
	}
	base := filepath.Dir(envFile)
	// Refused at approval, not first at the create: the copy sbx gets must be
	// writable from these bytes.
	if _, err := rewriteEnv(raw, base); err != nil {
		return nil, fmt.Errorf("env_file %s: %w", envFile, err)
	}
	var out []string
	for _, src := range doc.kitSources {
		if IsLocalKitSource(src) {
			out = append(out, filepath.Join(base, filepath.FromSlash(src)))
		}
	}
	return out, nil
}

// IsLocalKitSource reports whether sbx treats a kit source as a local path:
// "./…", "../…", "." or "..". sbx reads anything else — including a bare
// "mixins/x" — as a registry image reference.
func IsLocalKitSource(s string) bool {
	return s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}

// Digest hashes the pinned files: each file's path and content.
func Digest(p Profile) (string, []string, error) {
	files, _, digest, err := readPinned(p)
	return digest, files, err
}

// pinnedFile is one pinned file as it was read: its bytes and its mode (a kit
// may install an executable).
type pinnedFile struct {
	body []byte
	mode fs.FileMode
}

// readPinned reads every pinned file ONCE and hashes the bytes it read, so
// a caller holding the contents holds exactly what the digest describes.
func readPinned(p Profile) ([]string, map[string]pinnedFile, string, error) {
	files, err := PinnedFiles(p)
	if err != nil {
		return nil, nil, "", err
	}
	h := sha256.New()
	contents := make(map[string]pinnedFile, len(files))
	var total int64
	for _, f := range files {
		pf, err := readRegular(f, maxPinnedBytes-total)
		if err != nil {
			return nil, nil, "", err
		}
		total += int64(len(pf.body))
		sum := sha256.Sum256(pf.body)
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(f), hex.EncodeToString(sum[:]))
		contents[f] = pf
	}
	return files, contents, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// readRegular reads a regular file, at most limit bytes. It refuses a file
// that turned into something else between the check and the open — a link
// swapped in at that moment would otherwise be followed.
func readRegular(path string, limit int64) (pinnedFile, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return pinnedFile{}, err
	}
	if !st.Mode().IsRegular() {
		return pinnedFile{}, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return pinnedFile{}, err
	}
	defer f.Close()
	if fst, err := f.Stat(); err != nil || !os.SameFile(st, fst) {
		return pinnedFile{}, fmt.Errorf("%s changed while it was read", path)
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return pinnedFile{}, err
	}
	if int64(len(body)) > limit {
		return pinnedFile{}, fmt.Errorf("pinned files exceed %d MiB; refusing to pin", maxPinnedBytes>>20)
	}
	return pinnedFile{body: body, mode: st.Mode()}, nil
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
// takes seconds. So nothing approved is read from the repository again: the
// profile is parsed from the approved bytes, the host step runs a private
// copy of its approved script (hostArgv), and sbx creates the sandbox from a
// private copy of the approved environment file and kits (stageEnv).
type Approved struct {
	Profile
	digest string
	files  map[string]pinnedFile
	// stageDir is where stageEnv writes the environment copy, beside the
	// approval store: sbx keeps the file it was created from mounted, so the
	// copy must outlive the create.
	stageDir string
}

// Approve checks the profile against the approval store and returns it with
// the bytes it was checked on. It returns an *ErrUntrusted when the profile
// was never approved or changed since, and any other error when the files
// cannot be read.
//
// The returned profile is parsed from the SAME bytes the digest covers. p was
// parsed from an earlier read, and a writer that changed the profile for that
// read and restored it for this one would otherwise get an unapproved command
// run under an approved digest.
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
	approved, err := parseProfile(p.Path, contents[p.Path].body)
	if err != nil {
		return Approved{}, err
	}
	if !reflect.DeepEqual(approved, p) {
		return Approved{}, &ErrUntrusted{ProfilePath: p.Path, Changed: true}
	}
	return Approved{
		Profile:  approved,
		digest:   digest,
		files:    contents,
		stageDir: filepath.Join(filepath.Dir(storePath), "sbx-env", approved.Name),
	}, nil
}

// recheck reads the pinned files again and refuses when they no longer match
// what was approved. The host step must not change them.
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

// hostArgv is the host step's argv with its script replaced by a private copy
// of the approved bytes, in a new directory only this user can read. The
// caller runs it and then calls cleanup.
//
// The copy is why a host script must not find other files through its own
// path ("$(dirname "$0")"): it runs in the profile's directory, so it uses
// paths relative to that.
func (a Approved) hostArgv() (argv []string, cleanup func(), err error) {
	argv = append([]string(nil), a.PrepareHost...)
	cleanup = func() {}
	// The script is chosen from the APPROVED snapshot, never from what is on
	// disk now: a script removed after approval would otherwise be skipped
	// here, its original path passed on, and a file put back before the
	// command opens it run on the host.
	script := -1
	for i, arg := range argv {
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		cand := filepath.Clean(a.resolve(arg))
		if i == 0 && filepath.IsAbs(arg) && !ancestorOrSelf(a.Dir, cand) {
			continue
		}
		if _, ok := a.files[cand]; ok {
			if script >= 0 {
				return nil, nil, fmt.Errorf("prepare_host names more than one project file; run: quil sandbox trust %s", a.Dir)
			}
			script = i
		}
	}
	// Any OTHER argument that names a file now appeared after approval.
	files, err := a.fileArgs(argv)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range files {
		if f.index != script {
			return nil, nil, fmt.Errorf("prepare_host names a file the approval does not cover; run: quil sandbox trust %s", a.Dir)
		}
	}
	if script < 0 {
		return argv, cleanup, nil
	}
	path := filepath.Clean(a.resolve(argv[script]))
	tmp, err := os.MkdirTemp("", "quil-sbx-host-")
	if err != nil {
		return nil, nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	dst := filepath.Join(tmp, filepath.Base(path))
	if err := os.WriteFile(dst, a.files[path].body, 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	argv[script] = dst
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
