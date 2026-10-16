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
