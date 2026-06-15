package kubediscover

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleKubeconfig = `apiVersion: v1
kind: Config
current-context: prod
contexts:
- name: prod
  context:
    cluster: prod-cluster
    namespace: payments
    user: prod-user
- name: staging
  context:
    cluster: staging-cluster
    user: staging-user
clusters: []
users: []
`

func writeConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestContexts_ParsesNamesNamespacesAndCurrent(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "config", sampleKubeconfig)
	t.Setenv("KUBECONFIG", cfg)

	got := Contexts()
	if len(got) != 2 {
		t.Fatalf("got %d contexts, want 2: %+v", len(got), got)
	}
	if got[0].Name != "prod" || got[0].Namespace != "payments" || !got[0].Current {
		t.Errorf("ctx[0] = %+v, want prod/payments/current", got[0])
	}
	if got[1].Name != "staging" || got[1].Namespace != "" || got[1].Current {
		t.Errorf("ctx[1] = %+v, want staging/empty/not-current", got[1])
	}
}

func TestContexts_KubeconfigList_FirstFileWins(t *testing.T) {
	dir := t.TempDir()
	a := writeConfig(t, dir, "a", "contexts:\n- name: dup\n  context:\n    namespace: from-a\ncurrent-context: dup\n")
	b := writeConfig(t, dir, "b", "contexts:\n- name: dup\n  context:\n    namespace: from-b\n- name: only-b\n  context: {}\n")
	t.Setenv("KUBECONFIG", a+string(os.PathListSeparator)+b)

	got := Contexts()
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (dup deduped + only-b): %+v", len(got), got)
	}
	if got[0].Name != "dup" || got[0].Namespace != "from-a" {
		t.Errorf("dup should resolve from the first file: %+v", got[0])
	}
}

func TestContexts_MissingFile_DegradesToEmpty(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "nope"))
	if got := Contexts(); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestContexts_MalformedYAML_DegradesToEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "config", "contexts: [this is : not : valid : yaml\n")
	t.Setenv("KUBECONFIG", cfg)
	if got := Contexts(); len(got) != 0 {
		t.Errorf("got %+v, want empty for malformed yaml", got)
	}
}

func TestContexts_SymlinkedConfig_Rejected(t *testing.T) {
	dir := t.TempDir()
	real := writeConfig(t, dir, "real", sampleKubeconfig)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err) // Windows without privilege
	}
	t.Setenv("KUBECONFIG", link)
	if got := Contexts(); len(got) != 0 {
		t.Errorf("symlinked kubeconfig must be rejected, got %+v", got)
	}
}

func TestKubeconfigPaths_SplitsListSeparator(t *testing.T) {
	t.Setenv("KUBECONFIG", "x"+string(os.PathListSeparator)+"y")
	got := KubeconfigPaths()
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("got %v, want [x y]", got)
	}
}
