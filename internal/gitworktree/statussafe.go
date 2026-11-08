package gitworktree

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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
			// A gitlink's directory is invisible to git status whether or not
			// the submodule was initialised: git treats a gitlink with no
			// valid .git as unmodified and never lists files under it. So the
			// directory must be ABSENT or truly EMPTY; any content, any other
			// kind of entry, or anything unreadable keeps the checkout.
			empty, err := fsWithin(ctx, func() (bool, error) {
				return absentOrEmptyDir(filepath.Join(path, filepath.FromSlash(name)))
			})
			if err != nil || !empty {
				return fmt.Errorf("%w: submodule folder %s is not empty", ErrHiddenState, name)
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
// stdout to fn as it arrives (scanNULRecords). The first error fn returns
// stops git and is returned; so does a read error, an over-long or
// unterminated record, or git failing. When ctx ends the pipe is closed, so
// the read returns even if a descendant still holds git's stdout, and git is
// always reaped. A seam like runGit.
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
	stop := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stop()
	scanErr := scanNULRecords(stdout, maxScanRecord, fn)
	if scanErr != nil {
		cancel() // stop git; its own exit status no longer matters
	}
	waitErr := cmd.Wait()
	switch {
	case scanErr != nil:
		if ctx.Err() != nil && !errors.Is(scanErr, ErrHiddenState) {
			return ctx.Err()
		}
		return scanErr
	case waitErr != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", waitErr, msg)
		}
		return waitErr
	}
	return nil
}

// errRecordTooLong and errUnterminatedRecord refuse a stream that is not a
// well-formed list of NUL-terminated records within the size bound.
var (
	errRecordTooLong      = errors.New("a record longer than the limit")
	errUnterminatedRecord = errors.New("the output ended inside a record")
)

// scanNULRecords reads NUL-terminated records from r and hands each (without
// its NUL) to fn. Memory is bounded WHILE reading: a record that reaches max
// bytes is refused before more of it is buffered. A final record with no NUL,
// or any read error other than EOF, is an error too — a truncated listing is
// never taken for a complete one.
func scanNULRecords(r io.Reader, max int, fn func(string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), max)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, errUnterminatedRecord
		}
		return 0, nil, nil
	})
	for sc.Scan() {
		if rec := sc.Text(); rec != "" {
			if err := fn(rec); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return errRecordTooLong
		}
		return err
	}
	return nil
}

// maxScanRecord bounds one record: a path is at most a few KiB, so a longer
// one is not an index entry this check can trust.
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
	// The worker holds a permit from the host's process-wide limit until fn
	// REALLY returns, so abandoned calls on a dead mount cannot pile up past
	// it; with none free the check is refused at once ("could not check").
	var claim func() bool
	var release func()
	if p := fsPermits.Load(); p != nil { // read here: the worker can outlive this call
		claim, release = p.claim, p.release
	}
	if claim != nil && !claim() {
		var zero T
		return zero, ErrFSBusy
	}
	ch := make(chan result, 1)
	go func() {
		if release != nil {
			defer release()
		}
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

// absentOrEmptyDir is true for a path that does not exist, or a REAL
// directory (not a link) with no entries. Anything else — a file, a link, a
// directory holding even one entry, an error — is false or an error.
func absentOrEmptyDir(dir string) (bool, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if len(names) > 0 {
		return false, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return true, nil
}

// ErrFSBusy means no filesystem-call permit was free for a safety check.
var ErrFSBusy = errors.New("too many filesystem calls in flight")

// fsPermits bounds fsWithin's workers by the host's process-wide
// filesystem-call limit (the daemon's). Unset, nothing is bounded (a CLI or a
// test). Atomic: daemons are built concurrently in tests while checks run.
var fsPermits atomic.Pointer[fsPermitFuncs]

type fsPermitFuncs struct {
	claim   func() bool
	release func()
}

// SetFSPermits hands this package the host's filesystem-call limit: claim
// takes a permit without blocking (false when none is free), release returns
// it. Call it at start, before any worktree check; later calls replace it.
func SetFSPermits(claim func() bool, release func()) {
	fsPermits.Store(&fsPermitFuncs{claim: claim, release: release})
}
