package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/sbx"
)

func trustRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		sbx.ProfileFile: "[sbx]\nenv_file = \"sbxenv.yaml\"\nprepare_host = [\"sh\", \"prep.sh\"]\n",
		"sbxenv.yaml":   "schemaVersion: \"1\"\n",
		"prep.sh":       "echo prep\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSandboxTrust_ListsWhatItPinsAndAsks(t *testing.T) {
	repo, home := trustRepo(t), t.TempDir()
	var out bytes.Buffer
	if err := sandboxTrust(repo, false, strings.NewReader("y\n"), &out, home); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ON THIS HOST", "prep.sh", "sbxenv.yaml", "Approved"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not mention %q:\n%s", want, out.String())
		}
	}
	prof, err := sbx.LoadProfile(filepath.Join(repo, sbx.ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := sbx.CheckTrust(sbx.TrustStorePath(home), prof); err != nil {
		t.Errorf("approved profile not trusted: %v", err)
	}
}

func TestSandboxTrust_AnythingButYesApprovesNothing(t *testing.T) {
	repo, home := trustRepo(t), t.TempDir()
	if err := sandboxTrust(repo, false, strings.NewReader("\n"), &bytes.Buffer{}, home); err == nil {
		t.Fatal("an empty answer approved the profile")
	}
	prof, err := sbx.LoadProfile(filepath.Join(repo, sbx.ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	var ut *sbx.ErrUntrusted
	if err := sbx.CheckTrust(sbx.TrustStorePath(home), prof); !errors.As(err, &ut) {
		t.Errorf("err = %v, want untrusted", err)
	}
}

func TestParseTrustArgs(t *testing.T) {
	if dir, yes, err := parseTrustArgs([]string{"repo", "--yes"}); err != nil || dir != "repo" || !yes {
		t.Errorf("got %q %v %v", dir, yes, err)
	}
	if dir, yes, err := parseTrustArgs(nil); err != nil || dir != "." || yes {
		t.Errorf("defaults: got %q %v %v", dir, yes, err)
	}
	if _, _, err := parseTrustArgs([]string{"a", "b"}); err == nil {
		t.Error("two directories accepted")
	}
	if _, _, err := parseTrustArgs([]string{"--force"}); err == nil {
		t.Error("unknown flag accepted")
	}
}

// Run inside a linked worktree, the command approves the main checkout's
// profile — the one the daemon checks the worktree's panes against.
func TestSandboxTrust_WorktreeApprovesTheMainCheckout(t *testing.T) {
	repo, home := trustRepo(t), t.TempDir()
	admin := filepath.Join(repo, ".git", "worktrees", "feat-x")
	wt := filepath.Join(t.TempDir(), "feat-x")
	for path, body := range map[string]string{
		filepath.Join(repo, ".git", "HEAD"): "ref: refs/heads/main\n",
		filepath.Join(admin, "commondir"):   "../..\n",
		filepath.Join(admin, "gitdir"):      filepath.Join(wt, ".git") + "\n",
		filepath.Join(wt, ".git"):           "gitdir: " + admin + "\n",
		filepath.Join(wt, sbx.ProfileFile):  "[sbx]\nenv_file = \"sbxenv.yaml\"\nname = \"other\"\n",
		filepath.Join(wt, "sbxenv.yaml"):    "schemaVersion: \"1\"\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := sandboxTrust(wt, true, strings.NewReader(""), &bytes.Buffer{}, home); err != nil {
		t.Fatal(err)
	}
	mainProf, err := sbx.LoadProfile(filepath.Join(repo, sbx.ProfileFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := sbx.CheckTrust(sbx.TrustStorePath(home), mainProf); err != nil {
		t.Errorf("the main checkout's profile was not approved: %v", err)
	}
}

// The command lists the set-up steps that will run inside the sandbox, and
// approves nothing when the profile cannot be found, read or pinned.
func TestSandboxTrust_ListsStepsAndRefusesBrokenProfiles(t *testing.T) {
	repo, home := trustRepo(t), t.TempDir()
	profile := "[sbx]\nenv_file = \"sbxenv.yaml\"\nprepare_host = [\"sh\", \"prep.sh\"]\npost_create = [[\"bash\", \"-c\", \"make deps\"]]\n"
	if err := os.WriteFile(filepath.Join(repo, sbx.ProfileFile), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := sandboxTrust(repo, true, strings.NewReader(""), &out, home); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "inside the sandbox after a create (1): bash -c make deps") {
		t.Errorf("the set-up step is not listed:\n%s", out.String())
	}

	broken := map[string]func(dir string) error{
		"no profile": func(string) error { return nil },
		"bad profile": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, sbx.ProfileFile), []byte("[sbx\n"), 0o644)
		},
		"missing env file": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, sbx.ProfileFile), []byte("[sbx]\nenv_file = \"gone.yaml\"\n"), 0o644)
		},
	}
	for name, setup := range broken {
		t.Run(name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			if err := setup(dir); err != nil {
				t.Fatal(err)
			}
			if err := sandboxTrust(dir, true, strings.NewReader(""), &bytes.Buffer{}, home); err == nil {
				t.Error("approved")
			}
			if _, err := os.Stat(sbx.TrustStorePath(home)); err == nil {
				t.Error("a store was written for a refused profile")
			}
		})
	}
}
