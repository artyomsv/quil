package gitworktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The refspec is fully qualified so it can be neither an option nor a tag.
// Fails if the argv goes back to `--delete <b>` or drops the GCM switch.
func TestDeleteRemoteBranch_ArgvUsesFullRefspec(t *testing.T) {
	got := strings.Join(RemotePushArgs("origin", "feat/x"), " ")
	want := "-c credential.interactive=false push origin :refs/heads/feat/x"
	if got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// A push must fail rather than prompt; a user's own ssh command survives.
func TestRemotePushEnv_KeepsUserSSHCommand(t *testing.T) {
	has := func(env []string, kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}

	plain := RemotePushEnv([]string{"PATH=/bin"})
	for _, kv := range []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_SSH_COMMAND=ssh -o BatchMode=yes"} {
		if !has(plain, kv) {
			t.Errorf("env without a user ssh command lacks %q: %v", kv, plain)
		}
	}

	own := RemotePushEnv([]string{"GIT_SSH_COMMAND=ssh -J jump"})
	if has(own, "GIT_SSH_COMMAND=ssh -o BatchMode=yes") {
		t.Errorf("env replaced the user's GIT_SSH_COMMAND: %v", own)
	}
}

// `--` keeps a dash-led branch name from reaching git as an option.
func TestDeleteLocalBranch_TerminatesOptions(t *testing.T) {
	calls := stubGit(t, "", nil)
	if err := DeleteLocalBranch(context.Background(), "/repo", "-evil"); err != nil {
		t.Fatalf("DeleteLocalBranch: %v", err)
	}
	if got := strings.Join((*calls)[0][1:], " "); got != "branch -D -- -evil" {
		t.Errorf("argv = %q, want \"branch -D -- -evil\"", got)
	}
}

// Real git: once the worktree is removed, its branch can be deleted, and the
// branch is really gone afterwards.
func TestDeleteLocalBranch_RealGit_AfterWorktreeRemoved(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	path := DerivePath(repo, "feat/gone")
	if _, err := Add(ctx, repo, path, "feat/gone"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitIn(t, path, "add", "x.txt")
	runGitIn(t, path, "commit", "-qm", "unmerged work")
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if err := DeleteLocalBranch(ctx, repo, "feat/gone"); err != nil {
		t.Fatalf("DeleteLocalBranch: %v", err)
	}
	if out := runGitIn(t, repo, "branch", "--list", "feat/gone"); out != "" {
		t.Errorf("branch still listed after delete: %q", out)
	}
}
