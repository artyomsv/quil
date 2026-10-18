package sbx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoLayout builds a main checkout at <root>/app with a linked worktree at
// <root>/app-worktrees/feat-x, laid out the way git writes them. gitdir is
// written absolute or relative, as git does on different versions.
func repoLayout(t *testing.T, relative bool) (main, wt string) {
	t.Helper()
	root := t.TempDir()
	main = filepath.Join(root, "app")
	wt = filepath.Join(root, "app-worktrees", "feat-x")
	adminDir := filepath.Join(main, ".git", "worktrees", "feat-x")
	writeFile(t, filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(adminDir, "commondir"), "../..\n")
	gitdir, back := adminDir, filepath.Join(wt, ".git")
	if relative {
		gitdir = filepath.Join("..", "..", "app", ".git", "worktrees", "feat-x")
		rel, err := filepath.Rel(adminDir, back)
		if err != nil {
			t.Fatal(err)
		}
		back = rel
	}
	// git's own back pointer: the main repository lists the worktree.
	writeFile(t, filepath.Join(adminDir, "gitdir"), back+"\n")
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+gitdir+"\n")
	if err := os.MkdirAll(filepath.Join(wt, "src", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(main, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	return main, wt
}

func TestFindCheckout(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(fmt.Sprintf("relative=%v", relative), func(t *testing.T) {
			main, wt := repoLayout(t, relative)
			tests := []struct {
				name string
				dir  string
				want Checkout
			}{
				{"main checkout", main, Checkout{Root: main, Main: main}},
				{"main subdirectory", filepath.Join(main, "src"), Checkout{Root: main, Main: main}},
				{"linked worktree", wt, Checkout{Root: wt, Main: main, Linked: true}},
				{"worktree subdirectory", filepath.Join(wt, "src", "deep"), Checkout{Root: wt, Main: main, Linked: true}},
			}
			for _, tt := range tests {
				got, ok := FindCheckout(tt.dir)
				if !ok || !reflect.DeepEqual(got, tt.want) {
					t.Errorf("%s: FindCheckout = %+v, %v; want %+v", tt.name, got, ok, tt.want)
				}
			}
		})
	}
}

func TestFindCheckout_SubmoduleIsItsOwnMain(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "super", "lib")
	writeFile(t, filepath.Join(root, "super", ".git", "modules", "lib", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(sub, ".git"), "gitdir: ../.git/modules/lib\n")
	got, ok := FindCheckout(sub)
	want := Checkout{Root: sub, Main: sub}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("FindCheckout = %+v, %v; want %+v", got, ok, want)
	}
}

// Pointer files inside a checkout are writable by whatever runs there, so they
// must not be able to name another repository as their main checkout.
func TestFindCheckout_ForgedPointersAreNotALinkedWorktree(t *testing.T) {
	t.Run("own admin dir naming another repository", func(t *testing.T) {
		main, _ := repoLayout(t, false)
		other := filepath.Join(filepath.Dir(main), "other")
		writeFile(t, filepath.Join(other, ".git", "HEAD"), "ref: refs/heads/main\n")
		evil := filepath.Join(main, "src", "evil")
		admin := filepath.Join(main, "src", "fake-admin")
		writeFile(t, filepath.Join(admin, "commondir"), filepath.Join(other, ".git")+"\n")
		writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(evil, ".git")+"\n")
		writeFile(t, filepath.Join(evil, ".git"), "gitdir: "+admin+"\n")
		if co, ok := FindCheckout(evil); ok && co.Linked {
			t.Errorf("FindCheckout accepted a forged worktree: %+v", co)
		}
	})
	t.Run("pointing at another worktree's real admin dir", func(t *testing.T) {
		main, wt := repoLayout(t, false)
		evil := filepath.Join(wt, "src", "evil")
		writeFile(t, filepath.Join(evil, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "feat-x")+"\n")
		if co, ok := FindCheckout(evil); ok && co.Linked {
			t.Errorf("FindCheckout accepted a worktree the main repository does not list: %+v", co)
		}
	})
}

func TestFindCheckout_NotARepository(t *testing.T) {
	// t.TempDir can sit under a directory that is itself a checkout on a
	// developer machine; the walk must at least not invent a linked one.
	if co, ok := FindCheckout(t.TempDir()); ok && co.Linked {
		t.Errorf("FindCheckout invented a linked worktree: %+v", co)
	}
}

func TestFindCheckout_BrokenPointerIsNotACheckout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git"), "not a pointer\n")
	if co, ok := FindCheckout(dir); ok {
		t.Errorf("FindCheckout = %+v, want not ok", co)
	}
}

func TestMount_ArgsAndErrorLine(t *testing.T) {
	if got, want := MountArgs("app", "/w/app-worktrees"), []string{"mount", "app", "/w/app-worktrees"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MountArgs = %v, want %v", got, want)
	}
	f := &fakeRunner{answers: func([]string) (string, error) {
		return "error: mount policy denies /w/app-worktrees\n  try: sbx mount --help\n", fmt.Errorf("exit status 1")
	}}
	err := Client{Bin: "sbx", Runner: f}.Mount(context.Background(), "app", "/w/app-worktrees")
	if err == nil || !strings.Contains(err.Error(), "mount policy denies") {
		t.Fatalf("err = %v, want sbx's own error line", err)
	}
	if !f.called("sbx", "mount", "app", "/w/app-worktrees") {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestNewTag_PerSpawnAndPrefixed(t *testing.T) {
	a, b := NewTag("pane-1"), NewTag("pane-1")
	if a == b {
		t.Errorf("two spawns got one tag %q", a)
	}
	if !strings.HasPrefix(a, TagPrefix("pane-1")) || strings.HasPrefix(NewTag("pane-10"), TagPrefix("pane-1")) {
		t.Errorf("prefix does not separate panes: %q", a)
	}
}

func TestStopArgs_TagIsAnArgumentNotScript(t *testing.T) {
	got := StopArgs("app", "pane-1-abc", false)
	if len(got) != 8 || got[0] != "exec" || got[1] != "app" || got[2] != "sh" || got[3] != "-c" ||
		got[5] != "sh" || got[6] != "pane-1-abc" || got[7] != "exact" {
		t.Fatalf("StopArgs = %q", got)
	}
	if strings.Contains(got[4], "pane-1-abc") {
		t.Error("the tag is inside the script text")
	}
	if StopArgs("app", "pane-1-", true)[7] != "prefix" {
		t.Error("prefix mode not passed")
	}
}

// A worktree uses its main checkout's profile, and its own copy only when the
// main checkout has none.
func TestFindProfileFor_WorktreeUsesTheMainCheckout(t *testing.T) {
	main, wt := repoLayout(t, false)
	writeFile(t, filepath.Join(wt, ProfileFile), "[sbx]\nenv_file = \"x.yaml\"\n")
	if got, err := FindProfileFor(wt); err != nil || got != filepath.Join(wt, ProfileFile) {
		t.Errorf("no main profile: FindProfileFor = %q, %v; want the worktree's own", got, err)
	}
	writeFile(t, filepath.Join(main, ProfileFile), "[sbx]\nenv_file = \"x.yaml\"\n")
	if got, err := FindProfileFor(filepath.Join(wt, "src")); err != nil || got != filepath.Join(main, ProfileFile) {
		t.Errorf("FindProfileFor = %q, %v; want the main checkout's", got, err)
	}
}
