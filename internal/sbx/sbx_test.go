package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFile creates path (and its parents) with body.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const envYAML = `schemaVersion: "1"
agent: claude
workspace: /work/app
kits:
  - source: ./kits/networking
  - source: docker/registry-kit:1.0
`

// fixture lays out a repository with a profile, an env file, one local kit,
// a host script and a set-up step.
func fixture(t *testing.T, profile string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ProfileFile), profile)
	writeFile(t, filepath.Join(dir, "sandbox", "sbxenv.yaml"), envYAML)
	writeFile(t, filepath.Join(dir, "sandbox", "kits", "networking", "spec.yaml"), "kind: mixin\n")
	writeFile(t, filepath.Join(dir, "sandbox", "prepare-host.sh"), "#!/bin/sh\necho prep\n")
	writeFile(t, filepath.Join(dir, "sandbox", "setup.env"), "FOO=bar\n")
	return dir
}

const fullProfile = `[sbx]
env_file = "sandbox/sbxenv.yaml"
name = "my-app"
prepare_host = ["sh", "sandbox/prepare-host.sh"]
post_create = [["bash", "-c", "echo set-up"]]
post_create_env_file = "sandbox/setup.env"
`

func TestLoadProfile_ResolvesPathsAgainstTheProfileDirectory(t *testing.T) {
	dir := fixture(t, fullProfile)
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != dir {
		t.Errorf("Dir = %q, want %q", p.Dir, dir)
	}
	if want := filepath.Join(dir, "sandbox", "sbxenv.yaml"); p.EnvFile != want {
		t.Errorf("EnvFile = %q, want %q", p.EnvFile, want)
	}
	if p.Name != "my-app" {
		t.Errorf("Name = %q", p.Name)
	}
	if !reflect.DeepEqual(p.PrepareHost, []string{"sh", "sandbox/prepare-host.sh"}) {
		t.Errorf("PrepareHost = %v", p.PrepareHost)
	}
	if len(p.PostCreate) != 1 || p.PostCreate[0][0] != "bash" {
		t.Errorf("PostCreate = %v", p.PostCreate)
	}
	if want := filepath.Join(dir, "sandbox", "setup.env"); p.PostCreateEnvFile != want {
		t.Errorf("PostCreateEnvFile = %q, want %q", p.PostCreateEnvFile, want)
	}
}

func TestLoadProfile_Errors(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"no table", `x = 1`, "unknown keys"},
		{"empty table", "[sbx]\n", "env_file is required"},
		{"unknown key", "[sbx]\nenv_file = \"a.yaml\"\nprepare_hosts = [\"sh\"]\n", "unknown keys: sbx.prepare_hosts"},
		{"flag-like name", "[sbx]\nenv_file = \"a.yaml\"\nname = \"--rm\"\n", "name"},
		{"upper-case name", "[sbx]\nenv_file = \"a.yaml\"\nname = \"MyApp\"\n", "name"},
		{"empty host step", "[sbx]\nenv_file = \"a.yaml\"\nprepare_host = [\"\"]\n", "prepare_host: empty command"},
		{"empty post step", "[sbx]\nenv_file = \"a.yaml\"\npost_create = [[]]\n", "post_create[0]: empty command"},
		{"bad toml", "[sbx\n", "toml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ProfileFile)
			writeFile(t, path, tt.body)
			_, err := LoadProfile(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadProfile_DefaultName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Repo")
	writeFile(t, filepath.Join(dir, ProfileFile), "[sbx]\nenv_file = \"e.yaml\"\n")
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "quil-my-repo" {
		t.Errorf("Name = %q, want quil-my-repo", p.Name)
	}
	if !ValidName(p.Name) {
		t.Errorf("default name %q is not a valid name", p.Name)
	}
}

func TestDefaultName_AlwaysValid(t *testing.T) {
	for _, dir := range []string{"/", "/x/---", "/x/Ünïcödé", "/x/" + strings.Repeat("a", 200), "/x/a b.c"} {
		if n := DefaultName(dir); !ValidName(n) {
			t.Errorf("DefaultName(%q) = %q, not valid", dir, n)
		}
	}
}

func TestFindProfile_WalksUp(t *testing.T) {
	dir := fixture(t, fullProfile)
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindProfile(sub)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ProfileFile); got != want {
		t.Errorf("FindProfile = %q, want %q", got, want)
	}
}

func TestFindProfile_NoneFound(t *testing.T) {
	_, err := FindProfile(t.TempDir())
	if !errors.Is(err, ErrNoProfile) {
		t.Fatalf("err = %v, want ErrNoProfile", err)
	}
}

func TestPinnedFiles_CoversTheCageAndTheHostScript(t *testing.T) {
	dir := fixture(t, fullProfile)
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	files, err := PinnedFiles(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, ProfileFile),
		filepath.Join(dir, "sandbox", "kits", "networking", "spec.yaml"),
		filepath.Join(dir, "sandbox", "prepare-host.sh"),
		filepath.Join(dir, "sandbox", "sbxenv.yaml"),
		filepath.Join(dir, "sandbox", "setup.env"),
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("pinned\n got %v\nwant %v", files, want)
	}
}

func TestPinnedFiles_MissingEnvFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ProfileFile), "[sbx]\nenv_file = \"missing.yaml\"\n")
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PinnedFiles(p); err == nil {
		t.Fatal("a missing env file must fail the pin, not be skipped")
	}
}

func TestTrust_RefusesUntilApprovedAndAfterAnyChange(t *testing.T) {
	dir := fixture(t, fullProfile)
	store := filepath.Join(t.TempDir(), "sandbox", "sbx-trust.json")
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}

	var ut *ErrUntrusted
	if err := CheckTrust(store, p); !errors.As(err, &ut) || ut.Changed {
		t.Fatalf("new profile: err = %v, want untrusted (not changed)", err)
	}
	if _, err := Trust(store, p); err != nil {
		t.Fatal(err)
	}
	if err := CheckTrust(store, p); err != nil {
		t.Fatalf("approved profile refused: %v", err)
	}

	// Each file that decides what runs on the host or what the cage allows
	// must break the approval when it changes.
	for _, rel := range []string{
		ProfileFile,
		"sandbox/sbxenv.yaml",
		"sandbox/kits/networking/spec.yaml",
		"sandbox/prepare-host.sh",
		"sandbox/setup.env",
	} {
		t.Run(rel, func(t *testing.T) {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			orig, _ := os.ReadFile(path)
			writeFile(t, path, string(orig)+"\n# changed\n")
			defer writeFile(t, path, string(orig))
			p2, err := LoadProfile(filepath.Join(dir, ProfileFile))
			if err != nil {
				t.Fatal(err)
			}
			var ut *ErrUntrusted
			if err := CheckTrust(store, p2); !errors.As(err, &ut) || !ut.Changed {
				t.Fatalf("after changing %s: err = %v, want untrusted+changed", rel, err)
			}
		})
	}

	// A new file dropped into a kit changes the cage as well.
	writeFile(t, filepath.Join(dir, "sandbox", "kits", "networking", "files", "home", "x"), "y")
	if err := CheckTrust(store, p); !errors.As(err, &ut) {
		t.Fatalf("after adding a kit file: err = %v, want untrusted", err)
	}
}

func TestErrUntrusted_NamesTheCommand(t *testing.T) {
	e := &ErrUntrusted{ProfilePath: filepath.Join("/repo", ProfileFile)}
	if !strings.Contains(e.Error(), "quil sandbox trust") {
		t.Errorf("message %q does not name the remedy", e.Error())
	}
}

func TestParseList(t *testing.T) {
	out := "SANDBOX         AGENT   STATUS    PORTS   WORKSPACE\n" +
		"my-app          claude  running           /work/my-app\n" +
		"other                   stopped           /work/other\n\n"
	got := ParseList(out)
	want := []Sandbox{{"my-app", "running"}, {"other", "stopped"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseList = %v, want %v", got, want)
	}
	if ParseList("no sandboxes\n") != nil {
		t.Error("output without a header must parse to nothing")
	}
}

func TestHint(t *testing.T) {
	tests := map[string]string{
		"ERROR: Not authenticated to Docker":                                         "sbx login",
		"error: not signed in to Docker\n  try: sbx login":                           "sbx login",
		"412 Precondition Failed: global network policy has not been initialized":    "sbx policy init balanced",
		`ERROR: resolve kits: kit "mixins/x": copy from registry: invalid reference`: "./ or ../",
		"something else": "",
	}
	for in, want := range tests {
		got := Hint(in)
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("Hint(%q) = %q, want it to contain %q", in, got, want)
		}
	}
}

func TestArgs(t *testing.T) {
	p := Profile{Name: "my-app", EnvFile: "/r/sandbox/sbxenv.yaml", PostCreateEnvFile: "/r/sandbox/setup.env"}
	if got, want := CreateArgs(p, "--force"), []string{"env", "create", "/r/sandbox/sbxenv.yaml", "--name", "my-app", "--force"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CreateArgs = %v", got)
	}
	if got, want := PostCreateArgs(p, []string{"bash", "x.sh"}), []string{"exec", "--env-file", "/r/sandbox/setup.env", "my-app", "bash", "x.sh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PostCreateArgs = %v", got)
	}
	p.PostCreateEnvFile = ""
	if got, want := PostCreateArgs(p, []string{"true"}), []string{"exec", "my-app", "true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PostCreateArgs without env file = %v", got)
	}
	got := ExecArgs("my-app", "/work/app", []string{"A=1"}, "claude", []string{"--model", "x"})
	want := []string{"exec", "-it", "-w", "/work/app", "-e", "A=1", "my-app", "claude", "--model", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExecArgs = %v", got)
	}
	if got := ExecArgs("my-app", "", nil, "claude", nil); !reflect.DeepEqual(got, []string{"exec", "-it", "my-app", "claude"}) {
		t.Errorf("ExecArgs without workdir = %v", got)
	}
}

func TestInstallHint_PerOS(t *testing.T) {
	for goos, want := range map[string]string{
		"darwin":  "brew install docker/tap/sbx",
		"windows": "winget install Docker.sbx",
		"linux":   "docs.docker.com",
	} {
		if got := InstallHint(goos); !strings.Contains(got, want) {
			t.Errorf("InstallHint(%s) = %q", goos, got)
		}
	}
}

// fakeRunner answers sbx calls from a script and records every call.
type fakeRunner struct {
	calls   [][]string
	dirs    []string
	answers func(argv []string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, dir string, w io.Writer, argv ...string) error {
	f.calls = append(f.calls, append([]string(nil), argv...))
	f.dirs = append(f.dirs, dir)
	out, err := f.answers(argv)
	io.WriteString(w, out)
	return err
}

func (f *fakeRunner) called(prefix ...string) bool {
	for _, c := range f.calls {
		if len(c) >= len(prefix) && reflect.DeepEqual(c[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

const lsHeader = "SANDBOX   AGENT   STATUS   PORTS   WORKSPACE\n"

func loadFixture(t *testing.T) Profile {
	t.Helper()
	dir := fixture(t, fullProfile)
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnsure_ReusesAnExistingSandboxWithoutRunningAnything(t *testing.T) {
	p := loadFixture(t)
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		return lsHeader + "my-app    claude  running          /work\n", nil
	}}
	created, err := Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, io.Discard)
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if len(f.calls) != 1 {
		t.Errorf("an existing sandbox must cost one `sbx ls` and nothing else; calls = %v", f.calls)
	}
}

func TestEnsure_CreatesInOrder(t *testing.T) {
	p := loadFixture(t)
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		if argv[0] == "sbx" && argv[1] == "ls" {
			return lsHeader, nil
		}
		return "", nil
	}}
	var out bytes.Buffer
	created, err := Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, &out)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	var seq []string
	for _, c := range f.calls {
		seq = append(seq, strings.Join(c[:2], " "))
	}
	want := []string{"sbx ls", "sbx policy", "sh sandbox/prepare-host.sh", "sbx env", "sbx env", "sbx exec"}
	if !reflect.DeepEqual(seq, want) {
		t.Errorf("order = %v, want %v", seq, want)
	}
	// The host step runs in the profile's own directory.
	if f.dirs[2] != p.Dir {
		t.Errorf("prepare_host ran in %q, want %q", f.dirs[2], p.Dir)
	}
}

func TestEnsure_HostStepFailureAbortsBeforeCreate(t *testing.T) {
	p := loadFixture(t)
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		switch {
		case argv[0] == "sbx" && argv[1] == "ls":
			return lsHeader, nil
		case argv[0] == "sh":
			return "registry login failed\n", fmt.Errorf("exit status 1")
		}
		return "", nil
	}}
	var out bytes.Buffer
	_, err := Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, &out)
	if err == nil || !strings.Contains(err.Error(), "prepare_host failed") {
		t.Fatalf("err = %v", err)
	}
	if f.called("sbx", "env", "create") {
		t.Error("a failed host step must not be followed by a create")
	}
	if !strings.Contains(out.String(), "registry login failed") {
		t.Error("the host step's own output must reach the pane")
	}
}

func TestEnsure_ClassifiesKnownFailures(t *testing.T) {
	p := loadFixture(t)
	tests := []struct {
		name    string
		answers func([]string) (string, error)
		want    string
	}{
		{"not logged in", func(argv []string) (string, error) {
			return "ERROR: Not authenticated to Docker\n", fmt.Errorf("exit 1")
		}, "sbx login"},
		{"policy not set", func(argv []string) (string, error) {
			if argv[1] == "ls" {
				return lsHeader, nil
			}
			return "global network policy has not been initialized\n", fmt.Errorf("exit 1")
		}, "sbx policy init"},
		{"bare kit path", func(argv []string) (string, error) {
			switch {
			case argv[1] == "ls" || argv[1] == "policy" || argv[0] == "sh":
				return lsHeader, nil
			}
			return `resolve kits: kit "mixins/x": invalid reference`, fmt.Errorf("exit 1")
		}, "./ or ../"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRunner{answers: tt.answers}
			_, err := Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestEnsure_MissingEnvFile(t *testing.T) {
	p := loadFixture(t)
	if err := os.Remove(p.EnvFile); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{answers: func([]string) (string, error) { return lsHeader, nil }}
	_, err := Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "env_file") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsure_PostCreateFailureNamesTheCleanup(t *testing.T) {
	p := loadFixture(t)
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		if argv[1] == "exec" {
			return "", fmt.Errorf("exit 2")
		}
		if argv[1] == "ls" {
			return lsHeader, nil
		}
		return "", nil
	}}
	created, err := Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, io.Discard)
	if !created || err == nil || !strings.Contains(err.Error(), "sbx rm --force my-app") {
		t.Fatalf("created=%v err=%v", created, err)
	}
}

func TestApproveFlag_FollowsTheInstalledSbx(t *testing.T) {
	tests := map[string]string{
		"  -y, --auto-approve   Apply the environment plan without asking": "--auto-approve", // 0.47
		"  -f, --force          Apply the environment plan without asking": "--force",        // 0.48
		"": "--force",
	}
	for help, want := range tests {
		if got := ApproveFlag(help); got != want {
			t.Errorf("ApproveFlag(%q) = %q, want %q", help, got, want)
		}
	}
}

// The pane keeps only the error line once a spawn fails, so it must carry
// sbx's own reason, not the "try: --help" line that follows it.
func TestEnsure_CreateFailureCarriesTheSbxErrorLine(t *testing.T) {
	p := loadFixture(t)
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		switch {
		case argv[0] == "sbx" && argv[1] == "ls":
			return lsHeader, nil
		case argv[0] == "sbx" && argv[1] == "env" && len(argv) > 3 && argv[3] == "--help":
			return "  -f, --force   Apply the environment plan without asking\n", nil
		case argv[0] == "sbx" && argv[1] == "env":
			return "error: workspace path is not shared\n  try: sbx env create --help\n", fmt.Errorf("exit status 1")
		}
		return "", nil
	}}
	_, err := Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "workspace path is not shared") {
		t.Fatalf("err = %v, want sbx's own error line", err)
	}
	if !f.called("sbx", "env", "create", p.EnvFile, "--name", p.Name, "--force") {
		t.Errorf("create did not use the flag the help named; calls = %v", f.calls)
	}
}
