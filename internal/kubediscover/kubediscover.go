// Package kubediscover enumerates kube contexts from the kubeconfig file(s)
// referenced by KUBECONFIG (OS-list-separated) or ~/.kube/config. Pure reads
// with no cluster I/O: every failure (missing file, unreadable, malformed
// YAML, symlinked path) degrades to "no contexts" so discovery can never
// block pane creation. Only context names, default namespaces, and
// current-context are parsed — clusters, users, and credentials are ignored.
package kubediscover

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Context is one entry enumerated from the kubeconfig file(s).
type Context struct {
	Name      string // value passed to k9s --context
	Namespace string // default namespace for the context; may be empty
	Current   bool   // true if this is the kubeconfig's current-context
}

// KubeconfigPaths returns the resolved kubeconfig precedence list: KUBECONFIG
// split on the OS list separator if set, else ~/.kube/config. Exported for
// testing.
func KubeconfigPaths() []string {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		var paths []string
		for _, p := range filepath.SplitList(env) {
			if p != "" {
				paths = append(paths, p)
			}
		}
		return paths
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".kube", "config")}
}

// kubeconfigFile is the minimal shape we unmarshal — nothing more.
type kubeconfigFile struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// Contexts merges the kubeconfig path list (first-file-wins for duplicate
// context names, matching kubectl) and returns the contexts with the
// current-context marked.
func Contexts() []Context {
	var out []Context
	seen := make(map[string]bool)
	current := ""
	for _, path := range KubeconfigPaths() {
		// Reject symlinks: an attacker-controlled KUBECONFIG could otherwise
		// point discovery at an arbitrary file. k9s itself handles the real
		// connection; we only read named contexts.
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var kc kubeconfigFile
		if err := yaml.Unmarshal(data, &kc); err != nil {
			continue
		}
		if current == "" && kc.CurrentContext != "" {
			current = kc.CurrentContext
		}
		for _, c := range kc.Contexts {
			if c.Name == "" || seen[c.Name] {
				continue
			}
			seen[c.Name] = true
			out = append(out, Context{Name: c.Name, Namespace: c.Context.Namespace})
		}
	}
	for i := range out {
		if out[i].Name == current {
			out[i].Current = true
		}
	}
	return out
}
