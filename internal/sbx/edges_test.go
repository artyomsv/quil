package sbx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The real runner runs in the directory it is given and hands both output
// streams to the writer, which is how a failed step's reason reaches the pane.
func TestExecRunner_RunsInDirAndCapturesBothStreams(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	var out bytes.Buffer
	err := ExecRunner{}.Run(context.Background(), dir, &out, "sh", "-c", "pwd; echo to-stderr >&2")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(out.String(), real) || !strings.Contains(out.String(), "to-stderr") {
		t.Errorf("output = %q, want the directory and stderr", out.String())
	}
	if err := (ExecRunner{}).Run(context.Background(), dir, io.Discard, "sh", "-c", "exit 3"); err == nil {
		t.Error("a failing command reported success")
	}
	if err := (ExecRunner{}).Run(context.Background(), dir, io.Discard); err == nil {
		t.Error("an empty command was accepted")
	}
}

func TestTrustStorePath_BesideTheSandboxState(t *testing.T) {
	if got, want := TrustStorePath(filepath.Join("h", "q")), filepath.Join("h", "q", "sandbox", "sbx-trust.json"); got != want {
		t.Errorf("TrustStorePath = %q, want %q", got, want)
	}
}

func TestErrUntrusted_SaysWhetherItChanged(t *testing.T) {
	path := filepath.Join("/repo", ProfileFile)
	if msg := (&ErrUntrusted{ProfilePath: path}).Error(); !strings.Contains(msg, "has not been approved") {
		t.Errorf("new: %q", msg)
	}
	if msg := (&ErrUntrusted{ProfilePath: path, Changed: true}).Error(); !strings.Contains(msg, "changed since you approved it") {
		t.Errorf("changed: %q", msg)
	}
}

// A store Quil cannot read is an error of its own, never "not approved": the
// user should fix the file, not approve again over it.
func TestCheckTrust_CorruptStoreIsNotUntrusted(t *testing.T) {
	p := loadFixture(t).Profile
	store := filepath.Join(t.TempDir(), "sbx-trust.json")
	writeFile(t, store, "{not json")
	err := CheckTrust(store, p)
	var ut *ErrUntrusted
	if err == nil || errors.As(err, &ut) {
		t.Errorf("err = %v, want a read error", err)
	}
	if _, err := Trust(store, p); err == nil {
		t.Error("Trust wrote over a store it could not read")
	}
}

func TestLoadProfile_Refusals(t *testing.T) {
	tests := map[string]string{
		"no [sbx] table":      "[other]\nx = 1\n",
		"NUL in an argument":  "[sbx]\nenv_file = \"a.yaml\"\nprepare_host = [\"sh\", \"a\\u0000b\"]\n",
		"bad post_create":     "[sbx]\nenv_file = \"a.yaml\"\npost_create = [[\"\"]]\n",
		"not TOML":            "[sbx\n",
		"name starts with -":  "[sbx]\nenv_file = \"a.yaml\"\nname = \"-x\"\n",
		"missing env_file":    "[sbx]\nname = \"x\"\n",
		"blank env_file only": "[sbx]\nenv_file = \"  \"\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ProfileFile)
			writeFile(t, path, body)
			if _, err := LoadProfile(path); err == nil {
				t.Error("accepted")
			}
		})
	}
	if _, err := LoadProfile(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("a missing profile was accepted")
	}
}

// A post_create step's project scripts are pinned like the host step's:
// they decide what the cage is set up with.
func TestPinnedFiles_PostCreateScriptsArePinned(t *testing.T) {
	dir := fixture(t, "[sbx]\nenv_file = \"sandbox/sbxenv.yaml\"\n"+
		"post_create = [[\"bash\", \"sandbox/setup.sh\"], [\"/usr/local/bin/tool\", \"--flag\"]]\n")
	writeFile(t, filepath.Join(dir, "sandbox", "setup.sh"), "echo setup\n")
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
		pinned = pinned || f == filepath.Join(dir, "sandbox", "setup.sh")
	}
	if !pinned {
		t.Errorf("the post_create script is not pinned: %v", files)
	}
	// A link on the way to an argument is refused like any pinned file.
	moved := t.TempDir()
	writeFile(t, filepath.Join(moved, "setup.sh"), "echo setup\n")
	replaceWithLink(t, moved, filepath.Join(dir, "scripts"))
	writeFile(t, filepath.Join(dir, ProfileFile), "[sbx]\nenv_file = \"sandbox/sbxenv.yaml\"\n"+
		"post_create = [[\"bash\", \"scripts/setup.sh\"]]\n")
	p, err = LoadProfile(filepath.Join(dir, ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PinnedFiles(p); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v, want the link refusal", err)
	}
}

// A host step with no project file runs as written, flags and all; one with
// flags before its script still gets the copy in the script's place.
func TestHostArgv_NoScriptAndFlags(t *testing.T) {
	t.Run("no project file", func(t *testing.T) {
		dir := fixture(t, "[sbx]\nenv_file = \"sandbox/sbxenv.yaml\"\nprepare_host = [\"sh\", \"-c\", \"echo hi\"]\n")
		a := loadFixtureFrom(t, dir)
		argv, cleanup, err := a.hostArgv()
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		if !reflect.DeepEqual(argv, []string{"sh", "-c", "echo hi"}) {
			t.Errorf("argv = %q", argv)
		}
	})
	t.Run("flags before the script", func(t *testing.T) {
		dir := fixture(t, "[sbx]\nenv_file = \"sandbox/sbxenv.yaml\"\nprepare_host = [\"sh\", \"-e\", \"sandbox/prepare-host.sh\"]\n")
		a := loadFixtureFrom(t, dir)
		argv, cleanup, err := a.hostArgv()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		body, err := os.ReadFile(argv[2])
		if argv[0] != "sh" || argv[1] != "-e" || err != nil || string(body) != "#!/bin/sh\necho prep\n" {
			t.Errorf("argv = %q, script = %q (%v)", argv, body, err)
		}
	})
	t.Run("a new file in another argument", func(t *testing.T) {
		dir := fixture(t, "[sbx]\nenv_file = \"sandbox/sbxenv.yaml\"\nprepare_host = [\"sh\", \"sandbox/prepare-host.sh\", \"late.txt\"]\n")
		a := loadFixtureFrom(t, dir)
		writeFile(t, filepath.Join(dir, "late.txt"), "x")
		if _, _, err := a.hostArgv(); err == nil {
			t.Error("a file that appeared after approval was accepted")
		}
	})
}

// Kits outside the environment file's folder keep their place relative to it
// in the copy, so a "../" source still resolves.
func TestStageEnv_KitOutsideTheFilesFolder(t *testing.T) {
	dir := fixture(t, fullProfile)
	writeFile(t, filepath.Join(dir, "shared", "kit", "spec.yaml"), "kind: shared\n")
	writeFile(t, filepath.Join(dir, "sandbox", "sbxenv.yaml"),
		"schemaVersion: \"1\"\nworkspace: /work/app\nkits:\n  - source: ../shared/kit\n")
	a := loadFixtureFrom(t, dir)
	staged, err := a.stageEnv()
	if err != nil {
		t.Fatal(err)
	}
	kit, err := os.ReadFile(filepath.Join(filepath.Dir(staged.EnvFile), "..", "shared", "kit", "spec.yaml"))
	if err != nil || string(kit) != "kind: shared\n" {
		t.Errorf("kit in the copy = %q, %v", kit, err)
	}
	post, err := os.ReadFile(staged.PostCreateEnvFile)
	if err != nil || string(post) != "FOO=bar\n" {
		t.Errorf("post-create env in the copy = %q, %v", post, err)
	}
	if !strings.HasPrefix(staged.EnvFile, a.stageDir) || !strings.HasPrefix(staged.PostCreateEnvFile, a.stageDir) {
		t.Errorf("copies outside the stage folder: %q, %q", staged.EnvFile, staged.PostCreateEnvFile)
	}
}

// An environment file Quil cannot interpret is refused, never read as "no
// kits": a kit missed by the reader is a kit neither pinned nor copied.
func TestParseEnv_FailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"not YAML":       "kits: [\n",
		"not a mapping":  "- a\n- b\n",
		"kits not lists": "kits: ./kits/x\n",
		"empty source":   "kits:\n  - \"\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseEnv([]byte(body)); err == nil {
				t.Error("accepted")
			}
		})
	}
	doc, err := parseEnv([]byte("kits:\n  - ./a\n  - source: ../b\n  - registry/kit:1\n"))
	if err != nil || !reflect.DeepEqual(doc.kitSources, []string{"./a", "../b", "registry/kit:1"}) {
		t.Errorf("parseEnv = %+v, %v", doc, err)
	}
	if _, err := rewriteEnv([]byte("kits: [\n"), "/r"); err == nil {
		t.Error("rewriteEnv accepted bad YAML")
	}
	if _, err := rewriteEnv([]byte("additionalWorkspaces:\n  - ${{ env.args.x }}\n"), "/r"); err == nil {
		t.Error("rewriteEnv accepted an expression in an additional workspace")
	}
}

func TestIsLocalKitSource(t *testing.T) {
	for src, want := range map[string]bool{
		"./kits/x": true, "../x": true, ".": true, "..": true,
		"kits/x": false, "docker.io/x:1": false, "": false,
	} {
		if got := IsLocalKitSource(src); got != want {
			t.Errorf("IsLocalKitSource(%q) = %v, want %v", src, got, want)
		}
	}
}

// A bare repository has no main checkout, so a worktree of one is not a
// linked worktree Quil can share a folder for.
func TestFindCheckout_BareRepositoryIsNotLinked(t *testing.T) {
	root := t.TempDir()
	admin := filepath.Join(root, "app.git", "worktrees", "wt")
	wt := filepath.Join(root, "wt")
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(wt, ".git")+"\n")
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
	if co, ok := FindCheckout(wt); ok {
		t.Errorf("FindCheckout = %+v, want not ok", co)
	}
	// No back pointer at all: the main repository does not list it.
	main, wt2 := repoLayout(t, false)
	if err := os.Remove(filepath.Join(main, ".git", "worktrees", "feat-x", "gitdir")); err != nil {
		t.Fatal(err)
	}
	if co, ok := FindCheckout(wt2); ok && co.Linked {
		t.Errorf("FindCheckout = %+v without a back pointer", co)
	}
}
