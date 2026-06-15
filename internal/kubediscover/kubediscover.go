// Package kubediscover enumerates kube contexts from the kubeconfig file(s)
// referenced by KUBECONFIG (OS-list-separated) or ~/.kube/config. Pure reads
// with no cluster I/O: every failure (missing file, unreadable, malformed
// YAML) degrades to "no contexts" so discovery can never block pane creation.
// Only context names, default namespaces, and current-context are parsed —
// clusters, users, and credentials are ignored. Context names/namespaces are
// stripped of control characters (a kubeconfig is user-controlled but may be
// symlinked/synced from elsewhere) so a hostile value cannot inject terminal
// escape sequences into the TUI or the spawned --context argument.
package kubediscover

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

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
		// os.ReadFile follows symlinks — reading the target is correct: a
		// symlinked ~/.kube/config (dotfile managers, multi-cluster tooling)
		// is the exact file k9s/kubectl read. We only extract context names,
		// never credentials, and the values are sanitized below, so following
		// the link adds no meaningful risk over honouring KUBECONFIG itself.
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var kc kubeconfigFile
		if err := yaml.Unmarshal(data, &kc); err != nil {
			continue
		}
		if current == "" && kc.CurrentContext != "" {
			current = sanitize(kc.CurrentContext)
		}
		for _, c := range kc.Contexts {
			name := sanitize(c.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Context{Name: name, Namespace: sanitize(c.Context.Namespace)})
		}
	}
	for i := range out {
		if out[i].Name == current {
			out[i].Current = true
		}
	}
	return out
}

// sanitize strips control characters (including ESC, the lead byte of ANSI
// escape sequences) from a kubeconfig-sourced string and collapses tabs to
// spaces, so a hostile context name/namespace cannot inject terminal control
// codes into the TUI or the spawned --context argument. Surrounding
// whitespace is trimmed.
func sanitize(s string) string {
	mapped := strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(mapped)
}
