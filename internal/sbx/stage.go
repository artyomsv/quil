package sbx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// The environment copy.
//
// sbx reads the environment file and its kits by PATH when it creates the
// sandbox, and the repository holding them stays writable while it does. So
// sbx is given a private copy of the APPROVED bytes instead (stageEnv), in a
// folder beside the approval store, with the kits at the same place relative
// to the file so their "./" sources still resolve.
//
// What a copy changes, measured against sbx 0.48: every relative path in an
// environment file resolves against the folder of the file, and both
// ${{ env.fileDir }} and ${{ env.projectDir }} name that folder when a path is
// passed on the command line. The copy therefore spells those out: the two
// tokens become the original folder, and a relative workspace path becomes
// absolute. Any other relative path would resolve inside the private copy —
// it can break a create, but it cannot widen the cage.

// tokenRe matches the two sbx expressions that name the file's folder.
var tokenRe = regexp.MustCompile(`\$\{\{\s*env\.(fileDir|projectDir)\s*\}\}`)

// envDoc is the part of an environment file Quil reads.
type envDoc struct {
	// kitSources is every kit's source, in either form sbx accepts: a bare
	// string, or a mapping with `source`.
	kitSources []string
	// lifecycle is true when the file declares host lifecycle commands.
	lifecycle bool
}

// parseEnv reads what Quil needs from an environment file's bytes. It fails
// CLOSED: a document it cannot interpret is an error, never "no kits" — a kit
// missed here is a kit neither pinned nor copied.
func parseEnv(raw []byte) (envDoc, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return envDoc{}, err
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return envDoc{}, errors.New("not a YAML mapping")
	}
	var doc envDoc
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i].Value, m.Content[i+1]
		switch key {
		case "lifecycle":
			doc.lifecycle = true
		case "kits":
			if val.Kind != yaml.SequenceNode {
				return envDoc{}, errors.New("kits: not a list")
			}
			for _, item := range val.Content {
				src, ok := kitSource(item)
				if !ok {
					return envDoc{}, fmt.Errorf("kits: an entry at line %d has no source Quil can read", item.Line)
				}
				doc.kitSources = append(doc.kitSources, src)
			}
		}
	}
	return doc, nil
}

// kitSource is a kit entry's source: the string itself, or a mapping's
// `source`.
func kitSource(n *yaml.Node) (string, bool) {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value, n.Value != ""
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "source" && n.Content[i+1].Kind == yaml.ScalarNode {
				return n.Content[i+1].Value, n.Content[i+1].Value != ""
			}
		}
	}
	return "", false
}

// errLifecycle refuses an environment file with host lifecycle commands. Quil
// approves sbx's plan for the user (`--force`), and a host command can run a
// script the approval does not cover; the host step Quil does cover is the
// profile's prepare_host.
var errLifecycle = errors.New("the environment file declares lifecycle commands, which run on the host " +
	"outside the approval; move them to the profile's prepare_host")

// stageEnv writes the approved environment file, local kits and post-create
// env file to the stage folder, and returns the profile with its paths
// pointing at the copies. The folder is replaced on every create.
func (a Approved) stageEnv() (Profile, error) {
	staged := a.Profile
	envPath, err := a.stageEnvFile()
	if err != nil {
		return Profile{}, err
	}
	staged.EnvFile = envPath
	if a.PostCreateEnvFile != "" {
		f, ok := a.files[a.PostCreateEnvFile]
		if !ok {
			return Profile{}, fmt.Errorf("post_create_env_file %s is not in the approval", a.PostCreateEnvFile)
		}
		staged.PostCreateEnvFile = filepath.Join(a.stageDir, ".quil-post-create.env")
		if err := os.WriteFile(staged.PostCreateEnvFile, f.body, 0o600); err != nil {
			return Profile{}, err
		}
	}
	return staged, nil
}

func (a Approved) stageEnvFile() (string, error) {
	env, ok := a.files[a.EnvFile]
	if !ok {
		return "", fmt.Errorf("env_file %s is not in the approval", a.EnvFile)
	}
	envDir := filepath.Dir(a.EnvFile)
	kits, err := envKitDirs(env.body, a.EnvFile)
	if err != nil {
		return "", err
	}
	// One root above the file and every kit, so "../" sources keep working.
	base := envDir
	for _, k := range kits {
		for !ancestorOrSelf(base, k) {
			base = filepath.Dir(base)
		}
	}
	body, err := rewriteEnv(env.body, envDir)
	if err != nil {
		return "", fmt.Errorf("env_file %s: %w", a.EnvFile, err)
	}

	if err := os.RemoveAll(a.stageDir); err != nil {
		return "", err
	}
	place := func(path string) (string, error) {
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return "", err
		}
		dst := filepath.Join(a.stageDir, rel)
		return dst, os.MkdirAll(filepath.Dir(dst), 0o700)
	}
	staged, err := place(a.EnvFile)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(staged, body, 0o600); err != nil {
		return "", err
	}
	for path, f := range a.files {
		inKit := false
		for _, k := range kits {
			inKit = inKit || ancestorOrSelf(k, path)
		}
		if !inKit || path == a.EnvFile {
			continue
		}
		dst, err := place(path)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, f.body, f.mode.Perm()&^0o022|0o600); err != nil {
			return "", err
		}
	}
	return staged, nil
}

// rewriteEnv spells out what would otherwise resolve against the copy's
// folder: the two folder tokens everywhere, and a relative workspace path.
func rewriteEnv(raw []byte, envDir string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	expand(&root, envDir)
	if len(root.Content) == 1 && root.Content[0].Kind == yaml.MappingNode {
		m := root.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			switch m.Content[i].Value {
			case "workspace":
				if err := absolutize(m.Content[i+1], envDir); err != nil {
					return nil, err
				}
			case "additionalWorkspaces":
				if seq := m.Content[i+1]; seq.Kind == yaml.SequenceNode {
					for _, item := range seq.Content {
						if err := absolutize(item, envDir); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	return yaml.Marshal(&root)
}

// expand replaces the folder tokens in every string of the document.
func expand(n *yaml.Node, envDir string) {
	if n.Kind == yaml.ScalarNode {
		n.Value = tokenRe.ReplaceAllLiteralString(n.Value, envDir)
	}
	for _, c := range n.Content {
		expand(c, envDir)
	}
}

// absolutize makes a workspace path absolute: a plain string, or the `path`
// of a mapping. A relative path that still holds an expression is refused:
// what it expands to — perhaps an absolute path — decides whether it may be
// anchored to the original folder, and only sbx knows that.
func absolutize(n *yaml.Node, envDir string) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" || filepath.IsAbs(n.Value) {
			return nil
		}
		if strings.Contains(n.Value, "${{") {
			return fmt.Errorf("workspace path %q starts with an expression Quil cannot resolve; "+
				"write it as an absolute path or start it with ${{ env.fileDir }}", n.Value)
		}
		n.Value = filepath.Join(envDir, filepath.FromSlash(n.Value))
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "path" {
				return absolutize(n.Content[i+1], envDir)
			}
		}
	}
	return nil
}
