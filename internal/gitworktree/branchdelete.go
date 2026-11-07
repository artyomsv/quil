package gitworktree

import (
	"context"
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
	args := RemotePushArgs(remote, branch)
	env := remotePushEnvFor(ctx, repo, os.Environ())
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	cmd.Dir = repo
	cmd.Env = env
	hideWindow(cmd)
	cmd.WaitDelay = gitWaitDelay
	// Bounded like runGit's buffers: a remote or a hook can print without
	// end, and the push may run for the whole timeout.
	out := &boundedBuffer{max: maxPushOutput}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(out.String())
		if len(msg) > 400 {
			msg = msg[:400]
		}
		if ctx.Err() != nil {
			return fmt.Errorf("git push %s :%s: timed out", remote, branch)
		}
		return fmt.Errorf("git push %s :%s: %w: %s", remote, branch, err, msg)
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
	if out, err := runGit(ctx, repo, "config", "--get", "core.sshCommand"); err == nil && strings.TrimSpace(out) != "" {
		return RemotePushEnv(append(append([]string{}, base...), "GIT_SSH_COMMAND="+strings.TrimSpace(out)))
	}
	return RemotePushEnv(base)
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
func RemotePushEnv(base []string) []string {
	env := append([]string{}, base...)
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
	userSSH := false
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
