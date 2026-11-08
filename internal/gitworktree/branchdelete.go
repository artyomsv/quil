package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DeleteLocalBranch deletes a local branch, whatever it holds.
//
// -D, not -d: the caller decided this with the unmerged-commit count on
// screen, and -d would refuse exactly the branch the user chose to drop. git
// itself still refuses a branch that is checked out in ANY worktree, which is
// what closes the race with an agent switching a sibling worktree onto it.
// `--` ends option parsing: a branch name may legally begin with a dash.
func DeleteLocalBranch(ctx context.Context, repo, branch string) error {
	if branch == "" {
		return fmt.Errorf("no branch to delete")
	}
	if _, err := runGit(ctx, repo, "branch", "-D", "--", branch); err != nil {
		return fmt.Errorf("git branch -D %s: %w", branch, err)
	}
	return nil
}

// DeleteRemoteBranch deletes branch on remote by pushing an empty source to
// its FULL ref. A fully-qualified refspec cannot be read as an option, and it
// names the branch rather than a tag of the same name.
//
// It must never wait for a person: the daemon has no terminal, and on Windows
// it has no console either, so any prompt is invisible and would hold the
// close for the whole timeout. credential.interactive=false stops Git
// Credential Manager's GUI, and the environment from RemotePushEnv stops the
// terminal and ssh prompts. The caller bounds ctx.
func DeleteRemoteBranch(ctx context.Context, repo, remote, branch string) error {
	if remote == "" || branch == "" || strings.HasPrefix(remote, "-") {
		return fmt.Errorf("no remote branch to delete")
	}
	// The address is resolved ONCE and FROZEN: the default check and the
	// push both use that URL, never the remote's name, so a config change in
	// between cannot send the push to a server that was never checked. Only
	// one address, the same for fetch and push, can be frozen; and a URL a
	// rewrite rule would still change is refused.
	url, err := singlePushTarget(ctx, repo, remote)
	if err != nil {
		return fmt.Errorf("git push %s :%s: %w", remote, branch, err)
	}
	if err := noURLRewrite(ctx, repo, url); err != nil {
		return fmt.Errorf("git push %s :%s: %w", remote, branch, err)
	}
	env := remotePushEnvFor(ctx, repo, os.Environ())
	// The remote's default is asked of the REMOTE, right before the push:
	// the local refs/remotes/<remote>/HEAD can be stale (the remote changed
	// its default since the last set-head), and this is the last moment a
	// wrong answer can be stopped. No answer refuses.
	def, err := remoteHeadLive(ctx, repo, env, url)
	if err != nil {
		return fmt.Errorf("git push %s :%s: could not confirm the remote's default branch: %w", remote, branch, err)
	}
	if def == branch {
		return fmt.Errorf("git push %s :%s: it is the remote's default branch", remote, branch)
	}
	afterRemoteCheck()
	out, err := runPush(ctx, repo, env, RemotePushArgs(url, branch))
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 400 {
			msg = msg[:400]
		}
		if ctx.Err() != nil {
			return fmt.Errorf("git push %s :%s: timed out", remote, branch)
		}
		return fmt.Errorf("git push %s :%s: %w: %s", remote, branch, err, msg)
	}
	// A push by URL does not update the remote-tracking ref; drop it so the
	// next facts do not report a branch the remote no longer has.
	_, _ = runGit(ctx, repo, "update-ref", "-d", "refs/remotes/"+remote+"/"+branch) // best-effort: a stale ref only mis-reports, the next fetch fixes it
	return nil
}

// afterRemoteCheck runs between the default check and the push. A seam only,
// so a test can change the remote's configuration in exactly that window.
var afterRemoteCheck = func() {}

// noURLRewrite refuses a URL that an insteadOf or pushInsteadOf rule would
// still rewrite when pushed to directly: the frozen address must be the
// address the push reaches.
func noURLRewrite(ctx context.Context, repo, url string) error {
	out, err := runGit(ctx, repo, "config", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil // no rewrite rules at all
		}
		return fmt.Errorf("could not read the url rewrite rules: %w", err)
	}
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		_, prefix, ok := strings.Cut(strings.TrimSpace(l), " ")
		if ok && prefix != "" && strings.HasPrefix(url, prefix) {
			return fmt.Errorf("a url rewrite rule applies to the remote's address")
		}
	}
	return nil
}

// maxPushOutput bounds what a remote push may leave in memory.
const maxPushOutput = 16 << 10

// remotePushEnvFor is RemotePushEnv for one repository: when the repository
// configures core.sshCommand (a key file, a jump host), GIT_SSH_COMMAND would
// outrank it and break a push that works by hand, so the BatchMode command is
// not added. Without a terminal or an askpass program ssh cannot prompt then
// either.
func remotePushEnvFor(ctx context.Context, repo string, base []string) []string {
	repoSSH := false
	if out, err := runGit(ctx, repo, "config", "--get", "core.sshCommand"); err == nil && strings.TrimSpace(out) != "" {
		repoSSH = true
	}
	return pushEnv(base, repoSSH)
}

// RemotePushArgs is the argv DeleteRemoteBranch runs, exported for its test.
func RemotePushArgs(remote, branch string) []string {
	// core.askPass= beside the env's GIT_ASKPASS=: an askpass program named
	// in the repository's or user's config would run as well.
	return []string{"-c", "credential.interactive=false", "-c", "core.askPass=", "push", remote, ":refs/heads/" + branch}
}

// RemotePushEnv returns base with the variables that make a push fail rather
// than prompt. GIT_TERMINAL_PROMPT=0 stops only the terminal prompt: git
// still runs an askpass program when one is named, and a daemon started from
// an editor's terminal inherits the editor's — a prompt in another app. An
// EMPTY GIT_ASKPASS/SSH_ASKPASS means "none" (a later duplicate key wins in
// exec). An ssh command the user configured is left alone: replacing it would
// drop their own options (a jump host, a key file).
func RemotePushEnv(base []string) []string { return pushEnv(base, false) }

// pushEnv is RemotePushEnv; keepSSH leaves ssh to a command the repository
// configures.
func pushEnv(base []string, keepSSH bool) []string {
	env := append([]string{}, base...)
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
	userSSH := keepSSH
	for _, kv := range base {
		if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") || strings.HasPrefix(kv, "GIT_SSH=") {
			userSSH = true
		}
	}
	if !userSSH {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return env
}

// runPush runs one push and returns what it printed, at most maxPushOutput
// bytes: a remote or a hook can print without end, and the push may run for
// the whole timeout, inside a daemon that hosts every pane.
func runPush(ctx context.Context, repo string, env, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	cmd.Dir = repo
	cmd.Env = env
	hideWindow(cmd)
	cmd.WaitDelay = gitWaitDelay
	out := &boundedBuffer{max: maxPushOutput}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return out.String(), err
}

// remoteHeadLive asks remote which branch its HEAD names
// (`ls-remote --symref <remote> HEAD`), with the same no-prompt environment
// and output bound as the push. An answer without a symref is an error:
// the default cannot be established.
func remoteHeadLive(ctx context.Context, repo string, env []string, remote string) (string, error) {
	out, err := runPush(ctx, repo, env, []string{"-c", "credential.interactive=false", "-c", "core.askPass=",
		"ls-remote", "--symref", "--", remote, "HEAD"})
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("%w: %s", err, msg)
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		ref, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			continue
		}
		target, name, _ := strings.Cut(ref, "\t")
		if strings.TrimSpace(name) != "HEAD" {
			continue
		}
		if b, ok := strings.CutPrefix(target, "refs/heads/"); ok && b != "" {
			return b, nil
		}
	}
	return "", fmt.Errorf("the remote names no default branch")
}

// singlePushTarget refuses unless remote has exactly ONE fetch address and
// exactly ONE push address and they are the same, after git's own insteadOf
// and pushInsteadOf expansion (`git remote get-url` applies both). Local
// config only, no network. It returns that one address.
func singlePushTarget(ctx context.Context, repo, remote string) (string, error) {
	urls := func(push bool) ([]string, error) {
		args := []string{"remote", "get-url", "--all"}
		if push {
			args = append(args, "--push")
		}
		out, err := runGit(ctx, repo, append(args, remote)...)
		if err != nil {
			return nil, err
		}
		var list []string
		for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				list = append(list, l)
			}
		}
		return list, nil
	}
	fetch, err := urls(false)
	if err != nil {
		return "", fmt.Errorf("could not read the remote's address: %w", err)
	}
	push, err := urls(true)
	if err != nil {
		return "", fmt.Errorf("could not read the remote's push address: %w", err)
	}
	if len(fetch) != 1 || len(push) != 1 || fetch[0] != push[0] {
		return "", fmt.Errorf("the remote pushes to a different or to several addresses (%d fetch, %d push)", len(fetch), len(push))
	}
	return fetch[0], nil
}
