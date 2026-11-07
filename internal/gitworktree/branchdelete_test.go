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
	want := "-c credential.interactive=false -c core.askPass= push origin :refs/heads/feat/x"
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

	// An inherited askpass helper (a daemon started from an editor's
	// terminal carries the editor's) would raise a prompt in another app:
	// GIT_TERMINAL_PROMPT=0 does not stop askpass, an empty value does.
	plain := RemotePushEnv([]string{"PATH=/bin", "GIT_ASKPASS=/opt/editor/askpass.sh", "SSH_ASKPASS=/usr/bin/ksshaskpass"})
	if plain[len(plain)-1] == "" {
		t.Fatal("empty env entry")
	}
	for _, kv := range []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "GIT_ASKPASS=", "SSH_ASKPASS="} {
		if !has(plain, kv) {
			t.Errorf("env without a user ssh command lacks %q: %v", kv, plain)
		}
	}

	own := RemotePushEnv([]string{"GIT_SSH_COMMAND=ssh -J jump"})
	if has(own, "GIT_SSH_COMMAND=ssh -o BatchMode=yes") {
		t.Errorf("env replaced the user's GIT_SSH_COMMAND: %v", own)
	}
}

// A repository's own core.sshCommand (a key file, a jump host) must survive:
// GIT_SSH_COMMAND outranks it, so adding ours would break a push that works.
//
// Fails if DeleteRemoteBranch adds GIT_SSH_COMMAND when the repo sets one.
func TestRemotePushEnvFor_KeepsRepoSSHCommand(t *testing.T) {
	stubGit(t, "ssh -i ~/.ssh/deploy\n", nil)
	env := remotePushEnvFor(context.Background(), "/repo", []string{"PATH=/bin"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
			t.Errorf("env overrides the repository's core.sshCommand: %v", env)
		}
	}
}

// Push output is held in a bounded buffer, never all of it.
func TestBoundedPushOutput(t *testing.T) {
	b := &boundedBuffer{max: maxPushOutput}
	big := strings.Repeat("x", maxPushOutput*4)
	if _, err := b.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if len(b.String()) > maxPushOutput {
		t.Errorf("buffer holds %d bytes, cap %d", len(b.String()), maxPushOutput)
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
