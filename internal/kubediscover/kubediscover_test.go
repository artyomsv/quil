package kubediscover

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestContexts covers the single-file parse scenarios that share the same
// shape (write a kubeconfig body, point KUBECONFIG at it, parse).
func TestContexts(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []Context
	}{
		{
			name: "names namespaces and current",
			body: sampleKubeconfig,
			want: []Context{
				{Name: "prod", Namespace: "payments", Current: true},
				{Name: "staging", Namespace: "", Current: false},
			},
		},
		{
			name: "malformed yaml degrades to empty",
			body: "contexts: [this is : not : valid : yaml\n",
			want: nil,
		},
		{
			name: "empty contexts",
			body: "contexts: []\ncurrent-context: \"\"\n",
			want: nil,
		},
		{
			name: "no current-context marks nothing",
			body: "contexts:\n- name: dev\n  context:\n    namespace: web\n",
			want: []Context{{Name: "dev", Namespace: "web", Current: false}},
		},
		{
			name: "control characters stripped from name and namespace",
			body: "contexts:\n- name: \"saf\\u0000\\u0007e\"\n  context:\n    namespace: \"n\\u001bs\"\ncurrent-context: \"saf\\u0000\\u0007e\"\n",
			want: []Context{{Name: "safe", Namespace: "ns", Current: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := writeConfig(t, t.TempDir(), "config", tt.body)
			t.Setenv("KUBECONFIG", cfg)
			if got := Contexts(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Contexts() = %+v, want %+v", got, tt.want)
			}
		})
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

func TestContexts_SymlinkedConfig_Resolved(t *testing.T) {
	dir := t.TempDir()
	real := writeConfig(t, dir, "real", sampleKubeconfig)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err) // Windows without privilege
	}
	t.Setenv("KUBECONFIG", link)
	// A symlinked kubeconfig (dotfile managers, multi-cluster tooling) must be
	// followed to its target, not rejected — it's the exact file k9s reads.
	got := Contexts()
	if len(got) != 2 || got[0].Name != "prod" {
		t.Errorf("symlinked kubeconfig must be resolved, got %+v", got)
	}
}

func TestKubeconfigPaths_SplitsListSeparator(t *testing.T) {
	t.Setenv("KUBECONFIG", "x"+string(os.PathListSeparator)+"y")
	got := KubeconfigPaths()
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("got %v, want [x y]", got)
	}
}
