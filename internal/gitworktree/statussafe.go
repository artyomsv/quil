package gitworktree

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrHiddenState means the checkout is in a state git status cannot see
// through, so a count of zero would not prove it holds nothing: files git is
// told not to look at (assume-unchanged, skip-worktree), a populated
// submodule, or a top-level folder that is not the checkout itself. Callers
// treat it as "could not check" and keep the worktree.
var ErrHiddenState = errors.New("git cannot see everything in this worktree")

// gitEnvSelectors are inherited environment variables that redirect git away
// from the directory it runs in, or inject configuration. A daemon started
// from a shell that had one set (a script, a hook, an IDE terminal) would
// otherwise run every safety check against another checkout or index than the
// one about to be removed. Matched as exact names, or as a prefix when they
// end in "_".
var gitEnvSelectors = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
	"GIT_PREFIX", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_",
}

// cleanGitEnv is env without the selectors above. Everything else — PATH,
// HOME, the platform's own variables, GIT_CONFIG_GLOBAL/NOSYSTEM — is kept.
func cleanGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if isGitSelector(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func isGitSelector(name string) bool {
	upper := strings.ToUpper(name) // Windows environment names are case-insensitive
	for _, s := range gitEnvSelectors {
		if strings.HasSuffix(s, "_") {
			if strings.HasPrefix(upper, s) {
				return true
			}
		} else if upper == s {
			return true
		}
	}
	return false
}

// statusPreflight runs before every Status. A seam so the unit tests that
// script one git call can skip it; the real-git tests run it.
var statusPreflight = checkStatusVisible

// checkStatusVisible refuses (ErrHiddenState) a checkout whose status would
// not show everything a removal destroys:
//   - git's top-level folder must be the checkout itself (core.worktree or a
//     selector could point status at another one);
//   - no index entry may be assume-unchanged (lowercase tag in ls-files -v,
//     also set by core.ignoreStat) or skip-worktree ("S"): status does not
//     look at those files, so an edit to one counts as nothing;
//   - no submodule may be populated: its contents are another repository,
//     which the parent's status filters by the submodule's own settings and
//     never lists ignored files of.
func checkStatusVisible(ctx context.Context, path string) error {
	top, err := runGit(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("git rev-parse in %s: %w", path, err)
	}
	same, err := fsWithin(ctx, func() (bool, error) { return sameFile(strings.TrimSpace(top), path) })
	if err != nil {
		return fmt.Errorf("checking the top-level folder of %s: %w", path, err)
	}
	if !same {
		return fmt.Errorf("%w: git's top-level folder is not this worktree", ErrHiddenState)
	}
	// STREAMED, one index entry at a time: the index of a large monorepo is
	// far past runGit's 1 MiB output cap, and a check that fails there would
	// make every worktree of that repository unremovable.
	err = scanGitRecords(ctx, path, func(rec string) error {
		// "<tag> <mode> <object> <stage>\t<path>"
		meta, name, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) < 2 {
			return fmt.Errorf("git ls-files in %s: unexpected entry %q", path, rec)
		}
		tag, mode := fields[0], fields[1]
		if tag == "S" || (tag != strings.ToUpper(tag)) {
			return fmt.Errorf("%w: %s is marked assume-unchanged or skip-worktree", ErrHiddenState, name)
		}
		if mode == "160000" {
			// Anything but a clear "no .git there" counts as populated.
			if _, err := fsWithin(ctx, func() (os.FileInfo, error) { return os.Lstat(filepath.Join(path, filepath.FromSlash(name), ".git")) }); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: submodule %s is populated", ErrHiddenState, name)
			}
		}
		return nil
	}, "-c", "core.fsmonitor=false", "ls-files", "-v", "-s", "-z")
	if err != nil && !errors.Is(err, ErrHiddenState) {
		return fmt.Errorf("git ls-files in %s: %w", path, err)
	}
	return err
}

// scanGitRecords runs git in dir and hands each NUL-terminated record of its
// stdout to fn as it arrives, keeping no more than one record in memory. The
// first error fn returns stops git and is returned. A seam like runGit.
var scanGitRecords = func(ctx context.Context, dir string, fn func(string) error, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	cmd.Dir = dir
	cmd.Env = cleanGitEnv(os.Environ())
	hideWindow(cmd)
	cmd.WaitDelay = gitWaitDelay
	stderr := &boundedBuffer{max: maxGitStderr}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReaderSize(stdout, 64<<10)
	var fnErr error
	for {
		rec, rerr := r.ReadString(0)
		if len(rec) > maxScanRecord {
			fnErr = fmt.Errorf("a record of %d bytes", len(rec))
			break
		}
		if rec = strings.TrimSuffix(rec, "\x00"); rec != "" {
			if fnErr = fn(rec); fnErr != nil {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	if fnErr != nil {
		cancel() // stop git; its exit status no longer matters
		_ = cmd.Wait()
		return fnErr
	}
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// maxScanRecord bounds one record: a path is at most a few KiB, so a longer
// one is not an index entry.
const maxScanRecord = 64 << 10

// fsWithin runs one filesystem call on its own goroutine and stops waiting
// when ctx ends: a stat on a network mount that stopped answering cannot be
// interrupted, and the check must still return (as an error, so the
// worktree is kept) by the caller's deadline. An abandoned call finishes on
// its own when the mount answers.
func fsWithin[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
