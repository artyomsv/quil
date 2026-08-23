package gitworktree

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// runGit buffered the WHOLE of git's stdout with no cap: only stderr was bounded
// (Go's Output() caps it through a prefixSuffixSaver). A repository with a very
// large packed-refs — a mirror clone, or one a pane's own child creates — is
// then a few hundred MB allocated in one burst inside a daemon that hosts every
// pane on the machine.
//
// The bound is on the READ, so it is a property of runGit rather than of any one
// caller, and every caller inherits it.
func TestRunGit_BoundsWhatItBuffers(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not on PATH")
	}
	// Deliberately NOT the runGit seam: the point is to exercise the real
	// implementation's read, which every stub bypasses.
	prev := gitBinary
	gitBinary = "sh"
	t.Cleanup(func() { gitBinary = prev })

	// Emits far more than the cap, in one stream.
	out, err := runGit(context.Background(), t.TempDir(),
		"-c", "i=0; while [ $i -lt 200000 ]; do echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; i=$((i+1)); done")
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("err = %v, want ErrOutputTruncated", err)
	}
	if len(out) > maxGitOutput {
		t.Errorf("buffered %d bytes, want at most maxGitOutput (%d)", len(out), maxGitOutput)
	}
	if len(out) == 0 {
		t.Error("nothing was returned — a truncated read should still hand back what it got")
	}
}

// An ordinary listing is untouched: the cap is a ceiling, not a rewrite.
func TestRunGit_LeavesOrdinaryOutputAlone(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not on PATH")
	}
	prev := gitBinary
	gitBinary = "sh"
	t.Cleanup(func() { gitBinary = prev })

	out, err := runGit(context.Background(), t.TempDir(), "-c", "echo master; echo feat/x")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if strings.TrimSpace(out) != "master\nfeat/x" {
		t.Errorf("out = %q, want the two lines verbatim", strings.TrimSpace(out))
	}
}

// List must REFUSE a truncated listing rather than parse it. `git worktree list
// --porcelain` is a record format, so a listing cut mid-record yields a
// confidently wrong answer — a worktree whose branch is silently absent, or a
// phantom entry — and the caller renders that as fact. This is the whole reason
// the bound could not simply be applied inside Branches.
func TestList_RefusesATruncatedListing(t *testing.T) {
	stubGit(t, "worktree /repo\nHEAD 1111\nbranch refs/heads/master\n", ErrOutputTruncated)

	got, err := List(context.Background(), "/repo")
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("err = %v, want ErrOutputTruncated — a cut record parses into a wrong answer", err)
	}
	if got != nil {
		t.Errorf("List() = %v, want nothing alongside the refusal", got)
	}
}

// Branches is the opposite: its output is one independent name per line, so a
// cut listing is simply a SHORTER list — and the caller already treats a short
// list as "no opinion" rather than "this name is free". Refusing would throw
// away a usable answer.
func TestBranches_AcceptsATruncatedListingAsTruncated(t *testing.T) {
	stubGit(t, "master\nfeat/x\nfeat/y\n", ErrOutputTruncated)

	got, truncated, err := Branches(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("Branches: %v — a truncated branch listing is still usable", err)
	}
	if !truncated {
		t.Error("truncated = false — the caller would trust an incomplete list")
	}
	if len(got) != 3 {
		t.Errorf("Branches() = %v, want the names that did arrive", got)
	}
}

// Status counts lines, so a truncated read UNDERCOUNTS — and its zero is the one
// answer that invites a force-delete. Refused for the same reason it refuses a
// non-repository: a number nobody fully obtained must never render as "clean".
func TestStatus_RefusesATruncatedListing(t *testing.T) {
	stubGit(t, " M a\n M b\n", ErrOutputTruncated)

	if _, err := Status(context.Background(), "/repo"); !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("err = %v, want ErrOutputTruncated — an undercount renders as clean", err)
	}
}
