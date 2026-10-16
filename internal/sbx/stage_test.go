package sbx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// lsThenOK answers `sbx ls` with no sandbox and every other call with success.
func lsThenOK(argv []string) (string, error) {
	if argv[0] == "sbx" && argv[1] == "ls" {
		return lsHeader, nil
	}
	return "", nil
}

// However a script is spelt, it is the repository's file: an absolute path
// into the project is pinned and runs as an approved copy too.
func TestHostScript_AbsolutePathIsPinnedAndCopied(t *testing.T) {
	dir := fixture(t, "")
	script := filepath.Join(dir, "sandbox", "prepare-host.sh")
	writeFile(t, filepath.Join(dir, ProfileFile),
		"[sbx]\nenv_file = \"sandbox/sbxenv.yaml\"\nprepare_host = [\"sh\", \""+filepath.ToSlash(script)+"\"]\n")
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	files, err := PinnedFiles(p)
	if err != nil {
		t.Fatal(err)
	}
	pinned := false
	for _, f := range files {
		pinned = pinned || f == script
	}
	if !pinned {
		t.Fatalf("the absolute script is not pinned: %v", files)
	}
	a := approve(t, p)
	writeFile(t, script, "#!/bin/sh\nrm -rf ~\n")
	var ran string
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		if argv[0] == "sh" {
			body, _ := os.ReadFile(argv[1])
			ran = string(body)
		}
		return lsThenOK(argv)
	}}
	_, _ = Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), a, io.Discard)
	if ran != "#!/bin/sh\necho prep\n" {
		t.Errorf("host step ran %q, want the approved script", ran)
	}
}

// The host step may name one project file. A second one is either an input it
// would read unchecked or an output it would write into the copy.
func TestHostScript_OnlyOneProjectFile(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"interpreter and script", `["sh", "sandbox/prepare-host.sh"]`, false},
		{"absolute command outside the project", `["/bin/sh", "sandbox/prepare-host.sh"]`, true},
		{"a command and a new output", `["cp", "sandbox/prepare-host.sh", "out.txt"]`, false},
		{"copy over an existing file", `["cp", "sandbox/setup.env", "sandbox/prepare-host.sh"]`, true},
		{"script and a data file", `["sh", "sandbox/prepare-host.sh", "sandbox/setup.env"]`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fixture(t, "[sbx]\nenv_file = \"sandbox/sbxenv.yaml\"\nprepare_host = "+tt.host+"\n")
			p, err := LoadProfile(filepath.Join(dir, ProfileFile))
			if err != nil {
				t.Fatal(err)
			}
			_, err = PinnedFiles(p)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "only its script") && !strings.Contains(err.Error(), "on PATH") {
				t.Errorf("err = %v, want it to say how to fix it", err)
			}
		})
	}
}

// The profile used is the one in the approved bytes. A parse taken from an
// earlier read — changed, then restored before the digest read — is refused.
func TestApprove_RefusesAProfileParsedFromOtherBytes(t *testing.T) {
	dir := fixture(t, fullProfile)
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "s.json")
	if _, err := Trust(store, p); err != nil {
		t.Fatal(err)
	}
	// Same files, different command: the digest alone cannot tell them apart,
	// so only the parsed-profile comparison can refuse it.
	tampered := p
	tampered.PrepareHost = []string{"sh", "sandbox/prepare-host.sh", "--run-something-else"}
	if d1, _, err1 := Digest(p); err1 != nil {
		t.Fatal(err1)
	} else if d2, _, _ := Digest(tampered); d1 != d2 {
		t.Fatalf("the test needs one digest for both profiles: %s != %s", d1, d2)
	}
	var ut *ErrUntrusted
	if _, err := Approve(store, tampered); !errors.As(err, &ut) || !ut.Changed {
		t.Fatalf("err = %v, want untrusted+changed", err)
	}
	a, err := Approve(store, p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Profile, p) {
		t.Errorf("approved profile = %+v, want %+v", a.Profile, p)
	}
}

// sbx creates the sandbox from a private copy of the approved environment and
// kits: a change in the repository while it runs reaches nothing.
func TestEnsure_CreatesFromTheApprovedCopy(t *testing.T) {
	p := loadFixture(t)
	var created, kit string
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		switch {
		case argv[0] == "sbx" && argv[1] == "env" && len(argv) > 3 && argv[3] == "--help":
			// The window the review named: change both while sbx starts.
			writeFile(t, p.EnvFile, envYAML+"network:\n  allow: [\"*\"]\n")
			writeFile(t, filepath.Join(p.Dir, "sandbox", "kits", "networking", "spec.yaml"), "kind: open\n")
		case argv[0] == "sbx" && argv[1] == "env" && argv[2] == "create":
			body, _ := os.ReadFile(argv[3])
			created = string(body)
			k, _ := os.ReadFile(filepath.Join(filepath.Dir(argv[3]), "kits", "networking", "spec.yaml"))
			kit = string(k)
		}
		return lsThenOK(argv)
	}}
	if _, err := (Client{Bin: "sbx", Runner: f}).Ensure(context.Background(), p, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(created, "allow") || !strings.Contains(created, "./kits/networking") {
		t.Errorf("sbx read %q, want the approved environment file", created)
	}
	if kit != "kind: mixin\n" {
		t.Errorf("sbx's kit = %q, want the approved kit", kit)
	}
	if !f.called("sbx", "env", "create", filepath.Join(p.stageDir, "sbxenv.yaml")) {
		t.Errorf("create did not read the copy; calls = %v", f.calls)
	}
}

// What would resolve against the copy's folder is spelt out: the two folder
// tokens and relative workspace paths. Measured against sbx 0.48, both tokens
// and relative paths name the environment file's own folder.
func TestRewriteEnv_SpellsOutTheFilesFolder(t *testing.T) {
	envDir := filepath.Join(string(filepath.Separator)+"r", "sandbox")
	in := "schemaVersion: \"1\"\n" +
		"workspace: ${{ env.projectDir }}\n" +
		"additionalWorkspaces:\n" +
		"  - path: data\n" +
		"  - path: /abs/m2\n" +
		"  - ${{env.fileDir}}/cache\n" +
		"kits:\n  - source: ./kits/k\n"
	out, err := rewriteEnv([]byte(in), envDir)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Workspace            string `yaml:"workspace"`
		AdditionalWorkspaces []any  `yaml:"additionalWorkspaces"`
		Kits                 []struct {
			Source string `yaml:"source"`
		} `yaml:"kits"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Workspace != envDir {
		t.Errorf("workspace = %q, want %q", doc.Workspace, envDir)
	}
	want := []any{
		map[string]any{"path": filepath.Join(envDir, "data")},
		map[string]any{"path": "/abs/m2"},
		envDir + "/cache",
	}
	if !reflect.DeepEqual(doc.AdditionalWorkspaces, want) {
		t.Errorf("additionalWorkspaces = %#v, want %#v", doc.AdditionalWorkspaces, want)
	}
	if doc.Kits[0].Source != "./kits/k" {
		t.Errorf("a kit source was rewritten: %q", doc.Kits[0].Source)
	}
}

// Host lifecycle commands run outside the approval, so they are refused.
func TestPinnedFiles_RefusesLifecycleCommands(t *testing.T) {
	dir := fixture(t, fullProfile)
	writeFile(t, filepath.Join(dir, "sandbox", "sbxenv.yaml"),
		envYAML+"lifecycle:\n  initialize:\n    - command: ./scripts/seed.sh\n")
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PinnedFiles(p); !errors.Is(err, errLifecycle) {
		t.Errorf("err = %v, want the lifecycle refusal", err)
	}
}

func TestRedactEnv(t *testing.T) {
	got := RedactEnv([]string{"exec", "-it", "-e", "API_KEY=secret", "-e", "FLAG", "my-app", "claude", "-e", "x=y"})
	want := []string{"exec", "-it", "-e", "API_KEY=<redacted>", "-e", "FLAG", "my-app", "claude", "-e", "x=<redacted>"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RedactEnv = %q, want %q", got, want)
	}
}

// The script is chosen from the approved snapshot: removing it after the
// approval (to put a changed one back before the command opens it) must not
// make the host step fall back to the repository's path.
func TestHostScript_RemovedScriptStillRunsTheApprovedCopy(t *testing.T) {
	p := loadFixture(t)
	script := filepath.Join(p.Dir, "sandbox", "prepare-host.sh")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	var arg, ran string
	f := &fakeRunner{answers: func(argv []string) (string, error) {
		if argv[0] == "sh" {
			arg = argv[1]
			body, _ := os.ReadFile(argv[1])
			ran = string(body)
		}
		return lsThenOK(argv)
	}}
	_, _ = Client{Bin: "sbx", Runner: f}.Ensure(context.Background(), p, io.Discard)
	if arg == "sandbox/prepare-host.sh" || arg == script || ran != "#!/bin/sh\necho prep\n" {
		t.Errorf("host step ran %q (%q), want the approved copy", arg, ran)
	}
}

// sbx accepts a kit as a bare string as well as a mapping. Both forms are
// read, so a string-form kit can neither hide a lifecycle block nor escape
// the pin and the copy.
func TestEnv_StringFormKits(t *testing.T) {
	t.Run("lifecycle beside a string-form kit is refused", func(t *testing.T) {
		dir := fixture(t, fullProfile)
		writeFile(t, filepath.Join(dir, "sandbox", "sbxenv.yaml"),
			"schemaVersion: \"1\"\nkits:\n  - docker.io/sbx/some-kit:latest\n"+
				"lifecycle:\n  initialize:\n    - command: /w/helper.sh\n")
		p, err := LoadProfile(filepath.Join(dir, ProfileFile))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PinnedFiles(p); !errors.Is(err, errLifecycle) {
			t.Errorf("err = %v, want the lifecycle refusal", err)
		}
	})
	t.Run("a string-form local kit is pinned and copied", func(t *testing.T) {
		dir := fixture(t, fullProfile)
		writeFile(t, filepath.Join(dir, "sandbox", "sbxenv.yaml"),
			"schemaVersion: \"1\"\nworkspace: /work/app\nkits:\n  - ./kits/networking\n")
		p := loadFixtureFrom(t, dir)
		kit := filepath.Join(dir, "sandbox", "kits", "networking", "spec.yaml")
		if _, ok := p.files[kit]; !ok {
			t.Fatalf("the string-form kit is not pinned")
		}
		staged, err := p.stageEnv()
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(filepath.Dir(staged.EnvFile), "kits", "networking", "spec.yaml"))
		if err != nil || string(body) != "kind: mixin\n" {
			t.Errorf("the string-form kit was not copied: %q, %v", body, err)
		}
	})
	t.Run("an entry Quil cannot read is refused", func(t *testing.T) {
		dir := fixture(t, fullProfile)
		writeFile(t, filepath.Join(dir, "sandbox", "sbxenv.yaml"), "schemaVersion: \"1\"\nkits:\n  - {name: x}\n")
		p, err := LoadProfile(filepath.Join(dir, ProfileFile))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PinnedFiles(p); err == nil {
			t.Error("a kit entry without a source was accepted")
		}
	})
}

// A relative workspace that still holds an expression cannot be anchored to
// the original folder: it may expand to an absolute path. It is refused, at
// approval already.
func TestEnv_WorkspaceExpressionIsRefused(t *testing.T) {
	dir := fixture(t, fullProfile)
	writeFile(t, filepath.Join(dir, "sandbox", "sbxenv.yaml"),
		"schemaVersion: \"1\"\nargs:\n  checkout:\n    default: /work/app\nworkspace: ${{ env.args.checkout }}\n")
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PinnedFiles(p); err == nil || !strings.Contains(err.Error(), "expression") {
		t.Errorf("err = %v, want the expression refusal", err)
	}
	if _, err := rewriteEnv([]byte("workspace: ${{ env.fileDir }}/x\n"), "/r"); err != nil {
		t.Errorf("a folder token was refused: %v", err)
	}
}

// loadFixtureFrom loads and approves the profile in dir.
func loadFixtureFrom(t *testing.T, dir string) Approved {
	t.Helper()
	p, err := LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	return approve(t, p)
}
