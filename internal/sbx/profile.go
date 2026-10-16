// Package sbx runs a sandbox pane inside a Docker Sandboxes (`sbx`) microVM.
//
// The docker backend (internal/sandbox) derives every property of its
// container from the pane's own checkout, because the mount set it builds IS
// the security boundary. This backend is the opposite shape: the cage is
// declared by an sbx environment file the user's own organisation writes —
// workspaces, network allowlist, kits — and Quil only POINTS at it. Nothing
// company-specific belongs in Quil; a profile file in the repository names
// the environment file and the optional set-up steps around it.
//
// Every sbx invocation goes through Runner, so the whole lifecycle is
// testable on a machine that has no sbx installed — which is every CI runner.
package sbx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// ProfileFile is the profile's file name. Looked up from the pane's working
// directory upwards, so a pane opened in a subdirectory finds the same file.
// A pane in a linked worktree looks it up from the MAIN checkout first (see
// FindProfileFor).
const ProfileFile = ".quil-sandbox.toml"

// ErrNoProfile reports that no profile file exists at or above a directory.
var ErrNoProfile = errors.New("no " + ProfileFile + " found")

// Profile is one parsed and resolved sandbox profile. Every path is absolute.
type Profile struct {
	// Path is the profile file itself; Dir is its directory, which every
	// relative path in the file is resolved against and which the host
	// script runs in.
	Path string
	Dir  string

	// EnvFile is the sbx environment file (`sbx env create <file>`).
	EnvFile string
	// Name is the sbx sandbox name. Several panes share one sandbox.
	Name string
	// PrepareHost is an argv run ON THE HOST before the sandbox is created.
	// Never run when the sandbox already exists.
	PrepareHost []string
	// PostCreate is a list of argvs run INSIDE the sandbox (`sbx exec`) right
	// after it is created.
	PostCreate [][]string
	// PostCreateEnvFile is passed to every PostCreate step as
	// `sbx exec --env-file`. `sbx exec` does not inherit the environment
	// file's `env:` block, so a set-up step that needs those variables gets
	// them from here.
	PostCreateEnvFile string
}

// fileProfile is the on-disk shape. One [sbx] table, so a later backend can
// add its own table beside it without a format change.
type fileProfile struct {
	Sbx *struct {
		EnvFile           string     `toml:"env_file"`
		Name              string     `toml:"name"`
		PrepareHost       []string   `toml:"prepare_host"`
		PostCreate        [][]string `toml:"post_create"`
		PostCreateEnvFile string     `toml:"post_create_env_file"`
	} `toml:"sbx"`
}

// nameRe is what an sbx sandbox name may look like here. It reaches sbx argv
// as a positional, so it must never start with "-" (sbx would read it as a
// flag), and it is shown in the pane, so it carries no control characters.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// ValidName reports whether s can be used as a sandbox name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// FindProfile returns the profile file at or above start.
//
// It stops at the filesystem root. It does not stop at a git boundary on
// purpose: a project may keep one profile above several checkouts.
func FindProfile(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, ProfileFile)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w at or above %s", ErrNoProfile, start)
		}
		dir = parent
	}
}

// FindProfileFor names the profile for a pane directory. A directory in a
// linked worktree uses its MAIN checkout's profile: the worktree then runs in
// the same sandbox as the main checkout (one VM, one sign-in, one approval),
// and a branch that edits its own copy cannot point its panes at another
// cage. The worktree's own copy is used only when the main checkout has none.
//
// The daemon and `quil sandbox trust` both call this, so the profile a user
// approves is the one a pane is checked against.
func FindProfileFor(dir string) (string, error) {
	if co, ok := FindCheckout(dir); ok && co.Linked {
		if path, err := FindProfile(co.Main); err == nil {
			return path, nil
		} else if !errors.Is(err, ErrNoProfile) {
			return "", err
		}
	}
	return FindProfile(dir)
}

// LoadProfile parses and validates a profile file.
//
// Strict: a key this build does not know is an error, not a silently ignored
// line. A misspelt `prepare_hosts` would otherwise create a sandbox without
// the host step the user wrote, and the failure would show up much later and
// somewhere else.
func LoadProfile(path string) (Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Profile{}, err
	}
	var fp fileProfile
	md, err := toml.DecodeFile(abs, &fp)
	if err != nil {
		return Profile{}, fmt.Errorf("%s: %w", abs, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return Profile{}, fmt.Errorf("%s: unknown keys: %s", abs, strings.Join(keys, ", "))
	}
	if fp.Sbx == nil {
		return Profile{}, fmt.Errorf("%s: no [sbx] table", abs)
	}
	s := fp.Sbx
	p := Profile{Path: abs, Dir: filepath.Dir(abs)}

	if strings.TrimSpace(s.EnvFile) == "" {
		return Profile{}, fmt.Errorf("%s: [sbx] env_file is required", abs)
	}
	p.EnvFile = p.resolve(s.EnvFile)

	p.Name = s.Name
	if p.Name == "" {
		p.Name = DefaultName(p.Dir)
	}
	if !ValidName(p.Name) {
		return Profile{}, fmt.Errorf("%s: [sbx] name %q: use lower-case letters, digits, '.', '_' or '-', "+
			"starting with a letter or digit, at most 63 characters", abs, p.Name)
	}

	if len(s.PrepareHost) > 0 {
		if err := checkArgv(s.PrepareHost); err != nil {
			return Profile{}, fmt.Errorf("%s: [sbx] prepare_host: %w", abs, err)
		}
		p.PrepareHost = append([]string(nil), s.PrepareHost...)
	}
	for i, step := range s.PostCreate {
		if err := checkArgv(step); err != nil {
			return Profile{}, fmt.Errorf("%s: [sbx] post_create[%d]: %w", abs, i, err)
		}
		p.PostCreate = append(p.PostCreate, append([]string(nil), step...))
	}
	if s.PostCreateEnvFile != "" {
		p.PostCreateEnvFile = p.resolve(s.PostCreateEnvFile)
	}
	return p, nil
}

// resolve makes a profile-relative path absolute.
func (p Profile) resolve(rel string) string {
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Join(p.Dir, filepath.FromSlash(rel))
}

// checkArgv refuses an argv that cannot be what the author meant.
func checkArgv(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return errors.New("empty command")
	}
	for _, a := range argv {
		if strings.ContainsRune(a, 0) {
			return errors.New("argument contains a NUL byte")
		}
	}
	return nil
}

// DefaultName derives a sandbox name from the profile's directory: "quil-"
// plus the directory name, lower-cased, every other character mapped to "-".
func DefaultName(dir string) string {
	base := strings.ToLower(filepath.Base(dir))
	var b strings.Builder
	b.WriteString("quil-")
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.TrimRight(b.String(), "-.")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-.")
	}
	if name == "quil" {
		name = "quil-sandbox"
	}
	return name
}
