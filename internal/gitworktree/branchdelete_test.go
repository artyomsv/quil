package gitworktree

import (
	"context"
	"os"
	"os/exec"
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

// The push keeps at most maxPushOutput bytes of what a real child prints —
// the exact first bytes, not nothing — while the child is drained to the end.
//
// Fails if runPush returns to unbounded capture (CombinedOutput), or keeps
// nothing.
func TestRunPush_BoundsWhatAChildPrints(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not on PATH")
	}
	prev := gitBinary
	gitBinary = "sh"
	t.Cleanup(func() { gitBinary = prev })

	// 4x the cap: an "a" line, then lines of x, then "done" on stderr — the
	// child exits 0 only if every write was accepted (none blocked or failed).
	script := `printf 'a\n'; i=0; while [ $i -lt 2048 ]; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n'; i=$((i+1)); done; echo done >&2`
	out, err := runPush(context.Background(), t.TempDir(), os.Environ(), []string{"-c", script})

	if err != nil {
		t.Fatalf("runPush: %v (the child must be drained to the end)", err)
	}
	if len(out) != maxPushOutput {
		t.Fatalf("kept %d bytes, want exactly maxPushOutput (%d)", len(out), maxPushOutput)
	}
	if !strings.HasPrefix(out, "a\nxxxx") {
		t.Errorf("kept %q…, want the first bytes the child printed", out[:8])
	}
	if strings.Contains(out, "done") {
		t.Error("kept output from past the cap")
	}
}

// bareRemote makes a disposable bare repository that ACCEPTS deleting its
// own default branch (receive.denyDeleteCurrent=ignore), so a test can tell
// Quil's refusal apart from the server's.
func bareRemote(t *testing.T, repo, name, head string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name+".git")
	runGitIn(t, repo, "init", "--bare", "-q", "-b", head, dir)
	runGitIn(t, dir, "config", "receive.denyDeleteCurrent", "ignore")
	return dir
}

func remoteHas(t *testing.T, _, url, branch string) bool {
	t.Helper()
	// From a folder outside the repository, so a url rule a test adds to the
	// repository config cannot redirect the check itself.
	return strings.Contains(runGitIn(t, t.TempDir(), "ls-remote", url, "refs/heads/"+branch), "refs/heads/"+branch)
}

// The remote changed its default (master → develop) and this clone's
// refs/remotes/fork/HEAD still says master. The server WOULD delete develop
// (denyDeleteCurrent=ignore); Quil asks the remote first and refuses. An
// ordinary feature branch still goes.
//
// Fails if DeleteRemoteBranch trusts the local symref.
func TestDeleteRemoteBranch_RealGit_AsksTheRemoteForItsDefault(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fork := bareRemote(t, repo, "fork", "master")
	runGitIn(t, repo, "branch", "develop")
	runGitIn(t, repo, "branch", "feat2")
	runGitIn(t, repo, "remote", "add", "fork", fork)
	runGitIn(t, repo, "push", "-q", "fork", "master", "develop", "feat2")
	runGitIn(t, repo, "remote", "set-head", "fork", "--auto") // records master
	runGitIn(t, fork, "symbolic-ref", "HEAD", "refs/heads/develop")

	if err := DeleteRemoteBranch(ctx, repo, "fork", "develop"); err == nil {
		t.Fatal("deleted develop, the remote's current default")
	}
	if !remoteHas(t, repo, fork, "develop") {
		t.Fatal("develop is gone from the remote")
	}
	if err := DeleteRemoteBranch(ctx, repo, "fork", "feat2"); err != nil {
		t.Fatalf("DeleteRemoteBranch(feat2): %v", err)
	}
	if remoteHas(t, repo, fork, "feat2") {
		t.Error("feat2 is still on the remote")
	}
}

// Every case where the check cannot be bound to the delete refuses, and the
// remote is left exactly as it was: a push address unlike the fetch address,
// several push addresses, a remote whose HEAD names no branch, and a remote
// that does not answer.
//
// Fails if any of them reaches the push.
func TestDeleteRemoteBranch_RealGit_FailsClosed(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGitIn(t, repo, "branch", "feat")
	fetch := bareRemote(t, repo, "fetch", "master")
	push := bareRemote(t, repo, "push", "feat") // its default IS feat
	for _, url := range []string{fetch, push} {
		runGitIn(t, repo, "push", "-q", url, "master", "feat")
	}

	runGitIn(t, repo, "remote", "add", "split", fetch)
	runGitIn(t, repo, "remote", "set-url", "--push", "split", push)
	if err := DeleteRemoteBranch(ctx, repo, "split", "feat"); err == nil {
		t.Error("split fetch/push addresses: delete went ahead")
	}

	runGitIn(t, repo, "remote", "add", "multi", fetch)
	runGitIn(t, repo, "remote", "set-url", "--add", "--push", "multi", fetch)
	runGitIn(t, repo, "remote", "set-url", "--add", "--push", "multi", push)
	if err := DeleteRemoteBranch(ctx, repo, "multi", "feat"); err == nil {
		t.Error("several push addresses: delete went ahead")
	}

	detached := bareRemote(t, repo, "detached", "master")
	runGitIn(t, repo, "push", "-q", detached, "master", "feat")
	sha := runGitIn(t, detached, "rev-parse", "refs/heads/master")
	if err := os.WriteFile(filepath.Join(detached, "HEAD"), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, repo, "remote", "add", "nohead", detached)
	if err := DeleteRemoteBranch(ctx, repo, "nohead", "feat"); err == nil {
		t.Error("a remote HEAD naming no branch: delete went ahead")
	}

	runGitIn(t, repo, "remote", "add", "gone", filepath.Join(t.TempDir(), "missing.git"))
	if err := DeleteRemoteBranch(ctx, repo, "gone", "feat"); err == nil {
		t.Error("an unreachable remote: delete reported success")
	}

	for _, url := range []string{fetch, push, detached} {
		if !remoteHas(t, repo, url, "feat") {
			t.Errorf("feat is gone from %s", url)
		}
	}
}

// The address is frozen between the default check and the push. The remote
// is pointed at another server in exactly that window — one whose default IS
// the branch and which would accept deleting it. The push still goes to the
// checked server; the other one is untouched.
//
// Fails if the push resolves the remote's name again.
func TestDeleteRemoteBranch_RealGit_PushGoesToTheCheckedAddress(t *testing.T) {
	repo := realGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGitIn(t, repo, "branch", "feat")
	checked := bareRemote(t, repo, "checked", "master")
	other := bareRemote(t, repo, "other", "feat") // its default is feat
	for _, url := range []string{checked, other} {
		runGitIn(t, repo, "push", "-q", url, "master", "feat")
	}
	runGitIn(t, repo, "remote", "add", "r", checked)

	prev := afterRemoteCheck
	afterRemoteCheck = func() {
		// Every way to redirect the push, added in the window: the remote's
		// url, an insteadOf and a pushInsteadOf rule for the frozen URL.
		runGitIn(t, repo, "remote", "set-url", "r", other)
		runGitIn(t, repo, "config", "url."+other+".insteadOf", checked)
		runGitIn(t, repo, "config", "url."+other+".pushInsteadOf", checked)
	}
	t.Cleanup(func() { afterRemoteCheck = prev })

	if err := DeleteRemoteBranch(ctx, repo, "r", "feat"); err != nil {
		t.Fatalf("DeleteRemoteBranch: %v", err)
	}
	if !remoteHas(t, repo, other, "feat") {
		t.Fatal("feat was deleted from the server that was never checked")
	}
	if remoteHas(t, repo, checked, "feat") {
		t.Error("feat is still on the checked server")
	}
}

// The isolated environment carries only the snapshot: system and global
// config off, inherited config/dir/trace variables gone, credential and ssh
// settings passed in memory, and a configured ssh command kept (no BatchMode
// override).
//
// Fails if a trace or an inherited config variable reaches the push, or the
// snapshot is not passed.
func TestIsolatedPushEnv_CarriesOnlyTheSnapshot(t *testing.T) {
	stubGit(t, "core.sshcommand\nssh -i key\x00credential.helper\nstore\x00", nil)
	base := []string{"PATH=/bin", "GIT_TRACE=1", "GIT_TRACE_CURL=1", "GIT_CONFIG_PARAMETERS='url.x.insteadof'=y",
		"GIT_DIR=/elsewhere", "git_config_global=/evil"}

	env, err := isolatedPushEnv(context.Background(), "/repo", base)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"GIT_TRACE", "GIT_CONFIG_PARAMETERS", "GIT_DIR=", "/evil", "BatchMode"} {
		if strings.Contains(joined, bad) {
			t.Errorf("env carries %q:\n%s", bad, joined)
		}
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=core.sshcommand", "GIT_CONFIG_VALUE_0=ssh -i key", "GIT_CONFIG_KEY_1=credential.helper", "PATH=/bin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env lacks %q:\n%s", want, joined)
		}
	}
}

// URL credentials never reach an error, the log or a card.
func TestRedactURLs(t *testing.T) {
	in := "fatal: unable to access 'https://bob:s3cret@git.example/r.git/': timeout; ssh://alice@h/x ok"
	got := redactURLs(in)
	if strings.Contains(got, "s3cret") || strings.Contains(got, "bob") || strings.Contains(got, "alice@") {
		t.Errorf("redactURLs = %q", got)
	}
	if !strings.Contains(got, "https://***@git.example/r.git/") {
		t.Errorf("redactURLs = %q, want the rest kept", got)
	}
}
