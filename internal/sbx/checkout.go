package sbx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Checkout says where a pane directory sits in git terms.
type Checkout struct {
	// Root is the top of the checkout holding the directory.
	Root string
	// Main is the repository's MAIN checkout — Root itself unless Root is a
	// linked worktree.
	Main string
	// Linked is true when Root is a linked worktree.
	Linked bool
}

// FindCheckout walks up from dir to the checkout that holds it. ok is false
// outside a repository, and for a shape it does not recognise.
//
// It reads files only, no git process, so the synchronous spawn path can call
// it: the spawn runs on an IPC dispatch goroutine, which must never wait on a
// child process.
//
// A linked worktree's `.git` is a FILE naming its per-checkout git dir
// (`<main>/.git/worktrees/<name>`), and that dir holds a `commondir` file
// naming the shared one (`<main>/.git`). A submodule's `.git` is also a file,
// but its git dir has no `commondir` — it is its own main checkout.
func FindCheckout(dir string) (Checkout, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Checkout{}, false
	}
	for {
		st, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			if st.IsDir() {
				return Checkout{Root: dir, Main: dir}, true
			}
			if st.Mode().IsRegular() {
				return linkedCheckout(dir)
			}
			return Checkout{}, false
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Checkout{}, false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Checkout{}, false
		}
		dir = parent
	}
}

// maxGitFile bounds the two small files read here.
const maxGitFile = 4096

func linkedCheckout(root string) (Checkout, bool) {
	gitDir, ok := readPointer(filepath.Join(root, ".git"), "gitdir:")
	if !ok {
		return Checkout{}, false
	}
	gitDir = against(root, gitDir)
	common, ok := readPointer(filepath.Join(gitDir, "commondir"), "")
	if !ok {
		// No commondir: a submodule or a separate git dir, not a worktree.
		return Checkout{Root: root, Main: root}, true
	}
	common = against(gitDir, common)
	if filepath.Base(common) != ".git" {
		return Checkout{}, false // a bare repository has no main checkout
	}
	return Checkout{Root: root, Main: filepath.Dir(common), Linked: true}, true
}

// readPointer reads a one-line file and strips prefix from it.
func readPointer(path, prefix string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, maxGitFile)
	n, _ := f.Read(buf)
	line := strings.TrimSpace(strings.SplitN(string(buf[:n]), "\n", 2)[0])
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
	return line, line != ""
}

// against resolves a path git wrote relative to base.
func against(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}
