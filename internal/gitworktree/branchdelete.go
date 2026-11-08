package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
	fail := func(format string, a ...any) error {
		return fmt.Errorf("git push %s :%s: %s", remote, branch, redactURLs(fmt.Sprintf(format, a...)))
	}
	// The address is resolved ONCE and FROZEN: the default check and the
	// push both use that URL, never the remote's name. Only one address, the
	// same for fetch and push, can be frozen.
	url, err := singlePushTarget(ctx, repo, remote)
	if err != nil {
		return fail("%v", err)
	}
	// A local RELATIVE address is resolved against the repository now: the
	// git calls below run in the helper directory, where "../server.git"
	// would name a different folder — or another repository.
	url = absLocalAddress(repo, url)
	// And the CONFIGURATION is frozen with it: both git calls run in an
	// empty helper repository with system and global config off and only a
	// snapshot of the credential/http/ssh settings, taken now and passed in
	// memory. No url rewrite rule exists there, and nothing written to any
	// config file in between can redirect the push.
	env, err := isolatedPushEnv(ctx, repo, os.Environ())
	if err != nil {
		return fail("%v", err)
	}
	helper, err := os.MkdirTemp("", "quil-push-")
	if err != nil {
		return fail("could not make a helper repository: %v", err)
	}
	defer func() { _ = os.RemoveAll(helper) }() // best-effort: an empty bare repo in the temp dir
	if out, err := runPush(ctx, helper, env, []string{"init", "--bare", "-q", "--template=", "."}); err != nil {
		return fail("could not make a helper repository: %v: %s", err, strings.TrimSpace(out))
	}
	// The remote's default is asked of the REMOTE, right before the push:
	// the local refs/remotes/<remote>/HEAD can be stale (the remote changed
	// its default since the last set-head), and this is the last moment a
	// wrong answer can be stopped. No answer refuses.
	def, err := remoteHeadLive(ctx, helper, env, url)
	if err != nil {
		return fail("could not confirm the remote's default branch: %v", err)
	}
	if def == branch {
		return fail("it is the remote's default branch")
	}
	afterRemoteCheck()
	out, err := runPush(ctx, helper, env, RemotePushArgs(url, branch))
	if err != nil {
		if ctx.Err() != nil {
			return fail("timed out")
		}
		msg := strings.TrimSpace(out)
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return fail("%v: %s", err, msg)
	}
	// A push by URL updates no remote-tracking ref; drop it so the next
	// facts do not report a branch the remote no longer has.
	_, _ = runGit(ctx, repo, "update-ref", "-d", "refs/remotes/"+remote+"/"+branch) // best-effort: a stale ref only mis-reports until the next fetch
	return nil
}

// afterRemoteCheck runs between the default check and the push. A seam only,
// so a test can change the remote's configuration in exactly that window.
var afterRemoteCheck = func() {}

// maxPushOutput bounds what a remote push may leave in memory.
const maxPushOutput = 16 << 10

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

// isolatedPushEnv is the environment the remote check and the push run in:
// base without anything that points git at other config, another repository
// or a trace (GIT_CONFIG_*, GIT_DIR, GIT_TRACE*, GIT_CURL_VERBOSE — a trace
// prints the URL argv, credentials and all), with system and global config
// OFF, and a SNAPSHOT of repo's credential.*, http.* and core.sshCommand
// settings passed as GIT_CONFIG_COUNT/KEY/VALUE (in memory, never written to
// a file). url.* rules are deliberately not carried: the URL is already
// expanded, and a rule could only redirect it. Then RemotePushEnv's
// no-prompt variables, keeping a configured ssh command.
func isolatedPushEnv(ctx context.Context, repo string, base []string) ([]string, error) {
	out, err := runGit(ctx, repo, "config", "--null", "--get-regexp", `^(credential\..*|http\..*|core\.sshcommand)$`)
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 { // 1 = no such settings
			return nil, fmt.Errorf("could not read the push settings: %w", err)
		}
		out = ""
	}
	env := make([]string, 0, len(base)+8)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !isolatedDrop(strings.ToUpper(k)) {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	n, repoSSH := 0, false
	for _, rec := range strings.Split(out, "\x00") {
		key, val, ok := strings.Cut(rec, "\n")
		if !ok || key == "" {
			continue
		}
		if strings.EqualFold(key, "core.sshcommand") {
			repoSSH = true
		}
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, val))
		n++
	}
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n))
	return pushEnv(env, repoSSH), nil
}

// isolatedDrop names the inherited variables isolatedPushEnv removes (k is
// upper-cased: Windows environment names are case-insensitive).
func isolatedDrop(k string) bool {
	switch k {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_CONFIG",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
		"GIT_CONFIG_NOSYSTEM", "GIT_CURL_VERBOSE":
		return true
	}
	return strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_") || strings.HasPrefix(k, "GIT_TRACE")
}

// urlUserinfo matches the user:password@ part of a URL.
var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^/@\s]+@`)

// redactURLs masks any URL credentials in s: git output and errors reach
// quild.log and a sidebar card.
func redactURLs(s string) string {
	return urlUserinfo.ReplaceAllString(s, "${1}***@")
}

// scpLike matches git's scp-like address, "[user@]host:path".
var scpLike = regexp.MustCompile(`^([^/\:]+):`)

// absLocalAddress returns url with a local relative path made absolute
// against repo. URLs with a scheme ("ssh://", "file://") and scp-like
// addresses are returned unchanged; so is an absolute path. A single letter
// before the colon is a host everywhere but Windows, where it is a drive.
func absLocalAddress(repo, url string) string {
	if strings.Contains(url, "://") || filepath.IsAbs(url) {
		return url
	}
	if m := scpLike.FindStringSubmatch(url); m != nil && !(runtime.GOOS == "windows" && len(m[1]) == 1) {
		return url
	}
	return filepath.Join(repo, url)
}
