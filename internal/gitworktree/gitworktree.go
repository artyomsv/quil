// Package gitworktree performs the repository WRITES Quil needs to give a pane
// its own linked worktree, plus the listing that offers the existing ones.
//
// It is deliberately a separate package from gitinfo rather than a few more
// functions in it. gitinfo's package documentation states that every call is a
// read and nothing there can modify a repository — and that is load-bearing
// rather than descriptive: the daemon runs gitinfo.Probe on a ticker against
// every pane's checkout, so a package that gains the ability to write is one
// careless refactor away from a ticker that writes. Keeping the two apart makes
// that mistake need an import to happen, which is a thing a reviewer can see.
//
// Pure and stdlib-only, a sibling of gitinfo, gitdiscover and kubediscover. It
// shells out to git plumbing rather than manipulating .git by hand — creating a
// worktree means writing a gitdir file, an admin directory under
// worktrees/<name>, and a checked-out tree, and getting any of it subtly wrong
// produces a repository git itself cannot repair.
package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Worktree is one entry of `git worktree list --porcelain`.
//
// Locked and Prunable are carried rather than filtered because both describe an
// entry that LOOKS attachable and is not: a prunable worktree's directory is
// gone, and a locked one refuses operations. Dropping them would hand the
// caller a shorter list with no way to explain the absence, so a pane could be
// offered a directory that cannot host it.
type Worktree struct {
	// Path is the working directory, absolute, as git reports it.
	Path string
	// Branch is the checked-out branch with the refs/heads/ prefix stripped.
	// Empty when Detached or Bare.
	Branch   string
	Detached bool
	// Main marks the repository's primary checkout — the first block git
	// prints. It is where a NEW worktree's path is derived from, and it is the
	// one entry that must never be offered as somewhere to attach.
	Main bool
	// Locked and Prunable mark entries git will refuse to operate on.
	Locked   bool
	Prunable bool
	// Bare marks a repository with no working tree. Only ever true on the main
	// entry, and it cannot host a pane.
	Bare bool
}

// runGit is the seam every command goes through, so tests never need a real
// repository on disk. Mirrors gitinfo.runGit, including its two non-obvious
// pieces: hideWindow (a console-less daemon otherwise allocates a visible
// console per child on Windows) and WaitDelay (CommandContext kills git on
// expiry, but Output still waits for every holder of the stdout pipe, and
// git's own children — a credential helper, fsmonitor — inherit it).
//
// Stderr is discarded for List, whose every failure means the same thing.
// Add needs the opposite and captures it; see there.
var runGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	cmd.WaitDelay = gitWaitDelay
	// cmd.Output() would buffer the WHOLE of stdout with no cap. Only stderr is
	// bounded there (Go caps it through a prefixSuffixSaver), which is why the
	// asymmetry was easy to miss: `git for-each-ref refs/heads` and `git
	// worktree list` both scale with the repository, and a mirror clone with a
	// very large packed-refs is a few hundred MB allocated in one burst — inside
	// a daemon that hosts every pane on the machine and runs for weeks.
	stdout := &boundedBuffer{max: maxGitOutput}
	stderr := &boundedBuffer{max: maxGitStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		// git writes the actionable part of a worktree failure to stderr —
		// "already exists", "already used by worktree", "invalid reference".
		// The caller shows it to the user, so unlike gitinfo this cannot
		// discard it. Read from our own buffer now that Stderr is set: an
		// ExitError only carries stderr when the exec package captured it
		// itself, which it no longer does.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
		return out, err
	}
	// Reported AFTER a successful exit, and as an error rather than a flag,
	// because "what git said" and "what we kept" have diverged and only the
	// caller knows whether that is survivable. Branches folds it back into its
	// own truncation flag; List and Status refuse.
	if stdout.truncated {
		return out, fmt.Errorf("%w: git wrote more than %d bytes", ErrOutputTruncated, maxGitOutput)
	}
	return out, nil
}

// gitBinary is the executable runGit invokes. A package var ONLY so the bounded
// read can be exercised against a real child process that emits more than the
// cap — every other test drives the runGit seam itself and never reaches here.
var gitBinary = "git"

// ErrOutputTruncated reports that git produced more than maxGitOutput and the
// tail was discarded.
//
// It is deliberately an ERROR rather than a second return value: every caller
// then has to decide, and they decide differently. A record format cut
// mid-record (List) parses into a confidently wrong answer; a line-per-item
// listing (Branches) is merely shorter, which its caller already handles; and a
// COUNT (Status) silently undercounts, and its zero is the one answer that
// invites a force-delete.
var ErrOutputTruncated = errors.New("git output truncated")

// maxGitOutput bounds what runGit retains from one invocation.
//
// Generous against every honest caller: 2000 branch names at the 255-byte
// maximum is ~512 KB, and Branches' own maxBranchList stops it long before this
// does. It exists for the pathological repository, not for the ordinary one.
const maxGitOutput = 1 << 20 // 1 MiB

// maxGitStderr matches what cmd.Output() used to give us for free, so error text
// is bounded exactly as it was before stdout took its own buffer.
const maxGitStderr = 32 << 10 // 32 KiB

// boundedBuffer keeps the first max bytes written to it and records that more
// arrived.
//
// Write always reports len(p) consumed. Returning short would make os/exec's
// copier stop with io.ErrShortWrite and surface as a spurious command failure;
// the goal is to stop RETAINING, not to stop the child. The excess is read and
// dropped, so memory is bounded while git still gets to finish and exit
// normally — and the context deadline remains the bound on time.
type boundedBuffer struct {
	buf       []byte
	max       int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.buf); room > 0 {
		if len(p) > room {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		} else {
			b.buf = append(b.buf, p...)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.buf) }

// gitWaitDelay caps how long a killed git may hold its output pipe open.
const gitWaitDelay = 2 * time.Second

// List reports the worktrees of the repository containing dir, main checkout
// first. The caller supplies the timeout via ctx.
//
// A directory outside any repository is NOT an error: the setup dialog asks
// about every directory the user browses to, and most are not repositories, so
// treating that as a failure would put an error on screen for the ordinary
// case. It returns no entries, which is the same shape the caller renders for
// a repository with nothing to attach to.
//
// That collapse is deliberately narrow: a plain non-zero exit from `git
// worktree list` — the shape "not a repository" actually takes — is the only
// case folded into (nil, nil). A missing git binary (exec.ErrNotFound) and a
// call that ran out of time (ctx.Err() != nil) are returned as errors
// instead, or a missing binary, a corrupt repository and a permissions error
// would all render the same confidently-wrong "not a git repository" the
// caller shows for the genuine case.
func List(ctx context.Context, dir string) ([]Worktree, error) {
	out, err := runGit(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		// A TRUNCATED listing is returned as an error, never folded into the
		// not-a-repository answer. This is a RECORD format: a listing cut
		// mid-record loses a `branch` line and parsePorcelain reports that
		// worktree as detached, or invents a phantom entry from a partial
		// `worktree ` line. Both are confidently wrong rather than absent, and
		// the caller renders them as fact.
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, ErrOutputTruncated) || ctx.Err() != nil {
			return nil, err
		}
		return nil, nil
	}
	return parsePorcelain(out), nil
}

// parsePorcelain reads `git worktree list --porcelain`: blank-line-separated
// blocks of "<key> <value>" or bare "<key>" lines, main checkout first.
//
// Split on the "worktree" key rather than on blank lines, because the trailing
// newlines vary — a bare repo's block is two lines, and git appends a final
// blank — so a blank-line split has to special-case its own output shape.
//
// Neither split is robust against a LOCK REASON, which is free text git does
// not escape: a blank line inside one would split an entry in two, and a line
// beginning "worktree " would invent one. Accepted rather than solved, because
// the consequence is bounded — a phantom entry is a row that fails to attach,
// and the reason is written by whoever locked the worktree, i.e. the user. If
// that ever needs to be airtight, the fix is `-z` (NUL-delimited records),
// not more parsing.
func parsePorcelain(out string) []Worktree {
	var list []Worktree
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "worktree":
			if val == "" {
				continue
			}
			list = append(list, Worktree{Path: val, Main: len(list) == 0})
		case "branch":
			if len(list) > 0 {
				list[len(list)-1].Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if len(list) > 0 {
				list[len(list)-1].Detached = true
			}
		case "bare":
			if len(list) > 0 {
				list[len(list)-1].Bare = true
			}
		case "locked":
			if len(list) > 0 {
				list[len(list)-1].Locked = true
			}
		case "prunable":
			if len(list) > 0 {
				list[len(list)-1].Prunable = true
			}
		}
	}
	return list
}

// defaultBranch reports the ref a new worktree should branch from, or "" when
// the repository has no discoverable default branch.
//
// This exists because `git worktree add -b <new> <path>` with no start-point
// branches from the HEAD of the repository the command runs in — the MAIN
// checkout's current branch, i.e. whatever the user last worked on there. That
// is ambient state nobody chose in the dialog: a fix worktree created while the
// main checkout sat on a feature branch carried that feature's unmerged
// commits, so its diff against master was the feature rather than the fix.
//
// origin/HEAD FIRST, because it is the repository's own recorded answer and
// outranks any convention — a repo whose default is `develop` must not be
// branched off a `master` that merely exists beside it. It is unset more often
// than not (git writes it on clone; `git init` plus a remote never gets one),
// which is why the conventional names are a normal path rather than an exotic
// fallback.
//
// Remote before local at the same name: the remote ref is the shared truth and
// a local branch of that name can be behind it, which is the quieter version of
// the same bug — a fix branched off a master that is three weeks stale.
//
// Every candidate is FULLY QUALIFIED, and short names were a bug. `rev-parse`
// applies ref_rev_parse_rules, which tries refs/heads/%s BEFORE refs/remotes/%s
// — so probing "origin/main" finds a local branch literally named
// `refs/heads/origin/main` in preference to the remote-tracking ref, inverting
// the ordering this comment promises (measured, git 2.53). Fully-qualified refs
// also make the ^{commit} peel unambiguous: it does NOT reject a tag as an
// earlier version of this comment claimed — it PEELS one, so an annotated tag
// named `master` answers for `master^{commit}` and the worktree branches off
// the tag.
//
// Returning "" is a real answer, not a failure. `git init -b trunk` makes a
// repository with no `main`, no `master` and no remote, and refusing to create
// a worktree there would trade one wrong behaviour for a broken one; the caller
// falls back to git's own HEAD default, which is correct for exactly that repo.
func defaultBranch(ctx context.Context, repo string) string {
	// NOT --short: the fully-qualified form is what usableStartPoint can probe
	// unambiguously, and it cannot begin with a dash by construction.
	if out, err := runGit(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		// VERIFIED like every other candidate, and falling through when it does
		// not resolve. `git symbolic-ref` reads the symref without resolving its
		// target, so it exits 0 on a DANGLING origin/HEAD — the ordinary state
		// of any clone made before its remote renamed the default branch, since
		// a later `fetch --prune` drops the branch and leaves the symref naming
		// it. Adopting that answer hands `worktree add` a reference it refuses
		// ("fatal: invalid reference: origin/master"), and because the daemon
		// creates NO pane on failure, worktree-backed panes stop working in that
		// repository entirely — naming a branch the user never typed. git itself
		// reports this state as "warning: ignoring dangling symref", i.e. as no
		// answer, which is what this mirrors.
		if ref := strings.TrimSpace(out); usableStartPoint(ctx, repo, ref) {
			return ref
		}
	}
	for _, ref := range []string{
		"refs/remotes/origin/main",
		"refs/remotes/origin/master",
		"refs/heads/main",
		"refs/heads/master",
	} {
		if usableStartPoint(ctx, repo, ref) {
			return ref
		}
	}
	return ""
}

// usableStartPoint reports whether ref can be handed to `git worktree add` as
// the commit-ish to branch from. One check for both resolution paths, because
// the primary one skipping it is what shipped the dangling-symref failure.
//
// The dash guard is LOAD-BEARING, not a formality — the opposite of what an
// earlier comment here asserted. git's ref grammar does NOT forbid a leading
// dash: `git update-ref refs/heads/-evil HEAD` succeeds, and git permutes
// option parsing past positional arguments, so a start-point of `-evil` reaches
// `worktree add` as an option ("error: unknown switch `e'") and `--force` as a
// trailing positional is ACCEPTED as the flag (both measured, git 2.53). A
// fully-qualified ref cannot start with a dash, so this is belt-and-braces
// today — but the guard must outlive any change back to short names, and an
// unusable value now falls THROUGH to the next candidate rather than silently
// reverting Add to the ambient-HEAD behaviour this package exists to remove.
//
// Output is checked as well as the error: `rev-parse --verify --quiet` prints
// the sha on success, so empty-with-no-error means something other than git
// answered, and adopting "" would put an empty argument where a commit-ish
// belongs.
func usableStartPoint(ctx context.Context, repo, ref string) bool {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return false
	}
	out, err := runGit(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil && strings.TrimSpace(out) != ""
}

// Add creates a linked worktree at path, checking out a NEW branch off the
// repository's default branch — see defaultBranch, and note that this is NOT
// the repository's current HEAD, which is what git would use if the start-point
// were omitted. repo is the directory the command runs in.
//
// There is deliberately no force option and no --force in the argv. Every
// refusal git can raise here is a fact the user needs — the path is occupied,
// the branch already exists, the branch is checked out in another worktree, the
// repository has no commits to branch from — and forcing past any of them lands
// a pane on top of a checkout something else is using. The error is returned
// with git's own stderr attached, because "already used by worktree
// '/x/feat-y'" tells the user which pane to go look at and no message this
// package could invent would.
// The resolved start-point is RETURNED rather than kept private, and empty
// means "git's own HEAD default". A wrong base is invisible at create time by
// construction — that is the whole premise of this function — and surfaces days
// later as a PR whose diff is somebody else's work, so the caller logging what
// was actually used is the only place anyone can ever confirm it.
func Add(ctx context.Context, repo, path, branch string) (string, error) {
	start := defaultBranch(ctx, repo)
	args := []string{"worktree", "add"}
	if start != "" {
		// --no-track, because a remote-tracking start-point would otherwise
		// configure an upstream (branch.autoSetupMerge defaults to true) and
		// every consequence of that is wrong here. `git push` in the new
		// worktree fails, and the remedy git prints — `git push origin
		// HEAD:master` — pushes the feature work straight onto the default
		// branch if it is pasted. `git pull` merges the base INTO the feature
		// branch. And gitinfo starts rendering ahead/behind counts against the
		// base for that pane, on a branch whose push does not work.
		//
		// Branching off HEAD never set an upstream, so tracking would be a
		// behaviour change smuggled in by a base-selection fix. Only meaningful
		// alongside a start-point, hence the shared condition.
		args = append(args, "--no-track")
	}
	args = append(args, "-b", branch, path)
	if start != "" {
		args = append(args, start)
	}
	if _, err := runGit(ctx, repo, args...); err != nil {
		return start, fmt.Errorf("git worktree add %s (branch %s): %w", path, branch, err)
	}
	return start, nil
}

// Remove undoes an Add whose pane could not be created, and deletes the branch
// that Add made along with it.
//
// This is the ONE place --force is right, and for a reason that does not
// generalise to Add: the worktree being removed was created by this daemon
// seconds ago and has never been handed to anyone, so there is no user work to
// protect — while without it git refuses to remove a worktree it considers
// dirty, which a fresh checkout can be on a repository with line-ending or
// filemode differences. Leaving it instead strands a full checkout on disk plus
// a branch pointing at it, and the next attempt at the same name then fails
// with "already exists" against a directory the user never made.
//
// Best-effort by contract: the caller is already reporting a failure and this
// is cleanup, so the error is for the log, not for the user.
func Remove(ctx context.Context, repo, path string, branch string) error {
	if err := RemoveWorktree(ctx, repo, path); err != nil {
		return err
	}
	// Only after the worktree is gone: git refuses to delete a branch that a
	// worktree still has checked out, so the order is load-bearing.
	if _, err := runGit(ctx, repo, "branch", "-D", branch); err != nil {
		return fmt.Errorf("git branch -D %s: %w", branch, err)
	}
	return nil
}

// RemoveWorktree deletes a linked worktree's directory and git registration,
// and LEAVES ITS BRANCH ALONE. repo is the directory the command runs in.
//
// The branch is the entire difference from Remove, and it is not a detail. The
// two callers are describing different situations: Remove undoes an Add whose
// pane could not be created — a checkout seconds old that nobody has touched,
// whose branch is empty and whose name must be free for the retry. This one
// runs when the user closes a pane they have been working in, so its branch can
// hold commits that exist nowhere else. Deleting it would destroy them with no
// warning and no undo, in a dialog whose stated subject is a directory.
//
// --force is deliberate and is what the caller asked for: uncommitted and
// untracked files under the worktree go with it. The dialog counts them (see
// Status) and says so before the toggle can be armed.
// The `--` is the same belt-and-braces the dash guard in usableStartPoint is:
// the path reaches git in option position, and while current git rejects a
// dash-prefixed one on its own (`worktree remove` exposes only -f), that is a
// property of today's git rather than of this call. Terminating option parsing
// makes it a property of the call.
func RemoveWorktree(ctx context.Context, repo, path string) error {
	if _, err := runGit(ctx, repo, "worktree", "remove", "--force", "--", path); err != nil {
		return fmt.Errorf("git worktree remove %s: %w", path, err)
	}
	return nil
}

// Status counts everything the forced removal would destroy in the worktree at
// path: modified tracked files, untracked ones, AND IGNORED ones.
//
// The ignored half is the one that was missing and it is the most dangerous to
// omit. `git status --porcelain` says nothing about ignored entries, so a
// worktree holding a `.env` and a `build/` reported ZERO — the dialog rendered
// "clean", which is the single answer that invites the toggle, and `--force`
// then deleted both. An ignored file is not in git at all, so unlike a
// committed change there is no branch to recover it from; it is simply gone.
// The earlier version of this comment claimed to cover exactly that case ("a
// whole unversioned build") and did not, because a build directory is normally
// ignored rather than merely untracked.
//
// --ignored is the TRADITIONAL mode (the default when the flag carries no
// value), which respects -unormal and collapses an ignored DIRECTORY to one
// entry — so a node_modules costs one line. --ignored=matching would expand it
// and walk every file, which is the slow call this package otherwise avoids.
// The same collapsing applies to untracked directories, which is why the caller
// counts "files" loosely rather than promising an exact number.
//
// --no-optional-locks because a plain `git status` refreshes and REWRITES the
// index: this runs against a checkout the user may be working in at that
// moment, and Quil is asking a question here, not doing work on their behalf.
//
// A non-repository is an ERROR, unlike List, which folds that case into an
// empty answer. There it means "nothing to attach to" and is a real answer; here
// a 0 would mean "nothing to lose", which is a guess — and the dialog renders
// "clean" and "could not check" apart precisely so the guess is never made.
//
// This is the one call gitinfo's ticker deliberately does not make: `git status`
// is the plumbing that can take seconds on a large repository without fsmonitor.
// It is affordable here because it runs ONCE, when the user opens a confirm
// dialog, against one worktree.
func Status(ctx context.Context, path string) (int, error) {
	// A truncated read arrives as an error here and is NOT special-cased into a
	// count, deliberately: this function returns a NUMBER, so truncation
	// undercounts — and 0 is the single answer that invites the force-delete.
	// "Could not check" and "clean" are rendered apart precisely so a count
	// nobody fully obtained never masquerades as one.
	out, err := runGit(ctx, path, "--no-optional-locks", "status", "--porcelain", "--ignored")
	if err != nil {
		return 0, fmt.Errorf("git status %s: %w", path, err)
	}
	var n int
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}
