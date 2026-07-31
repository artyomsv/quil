package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// mkRepo creates a directory that gitdiscover will recognise as a repository.
func mkRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	return dir
}

func TestGitReposResponse_FindsTheEnclosingRepo(t *testing.T) {
	root := mkRepo(t, t.TempDir())

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: root}, "")

	if got.Error != "" {
		t.Fatalf("Error = %q, want empty", got.Error)
	}
	if len(got.Repos) == 0 {
		t.Fatal("no repos found for a directory that is itself a repo")
	}
	// EvalSymlinks is applied inside gitdiscover, so compare resolved forms.
	want, _ := filepath.EvalSymlinks(root)
	if got.Repos[0] != want && got.Repos[0] != root {
		t.Errorf("Repos[0] = %q, want %q", got.Repos[0], want)
	}
}

// The echo is the staleness key: the answer only means anything for the
// directory that was asked about, and the user may have moved on.
func TestGitReposResponse_EchoesCWDVerbatim(t *testing.T) {
	root := mkRepo(t, t.TempDir())
	req := ipc.GitReposReqPayload{CWD: root + string(filepath.Separator)}

	got := gitReposResponse(req, "")

	if got.CWD != req.CWD {
		t.Errorf("CWD = %q, want %q — normalising here makes a live request look stale",
			got.CWD, req.CWD)
	}
}

// TestGitReposResponse_NoRepoIsAnAnswerNotAnError pins the distinction the UI
// depends on.
//
// "No repository here" is a finding and the TUI flashes it. A failure is not,
// and must not be reported as one — that is precisely the wrong-but-confident
// answer this phase exists to remove.
func TestGitReposResponse_NoRepoIsAnAnswerNotAnError(t *testing.T) {
	plain := t.TempDir()

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: plain}, "")

	if got.Error != "" {
		t.Errorf("Error = %q, want empty — an absent repo is a real answer", got.Error)
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos = %v, want none", got.Repos)
	}
}

// A missing directory must not be reported as "no repositories" either.
func TestGitReposResponse_MissingDirEchoesAndFindsNothing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: missing}, "")

	if got.CWD != missing {
		t.Errorf("CWD = %q, want the request echoed", got.CWD)
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos = %v, want none for a directory that does not exist", got.Repos)
	}
}

func TestGitReposResponse_EmptyCWDUsesTheFallback(t *testing.T) {
	root := mkRepo(t, t.TempDir())

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: ""}, root)

	if got.CWD != "" {
		t.Errorf("CWD = %q, want the empty request echoed unchanged", got.CWD)
	}
	if len(got.Repos) == 0 {
		t.Error("the fallback directory was not scanned")
	}
}

// A rejected scan must still echo the CWD, or the TUI drops it as stale and
// waits out its whole timeout on an answer it already holds.
func TestBeginGitDiscover_RejectionEchoesTheCWD(t *testing.T) {
	d := &Daemon{}
	msg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: "/srv/work"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}

	if _, ok := d.beginGitDiscover(msg); !ok {
		t.Fatal("first claim was refused; the slot starts free")
	}
	rejection, ok := d.beginGitDiscover(msg)
	if ok {
		t.Fatal("second claim succeeded; the single-flight guard does not hold")
	}
	if rejection.CWD != "/srv/work" {
		t.Errorf("rejection CWD = %q, want the request echoed", rejection.CWD)
	}
	if rejection.Error == "" {
		t.Error("rejection carries no reason")
	}

	d.gitDiscovering.Store(false)
	if _, ok := d.beginGitDiscover(msg); !ok {
		t.Error("slot was not released")
	}
}

// The browse and discovery guards must be independent: the setup dialog
// resolves a directory and then scans it, so a shared slot would make each step
// fail exactly when it followed the other.
func TestGitDiscoverAndBrowse_HaveIndependentSlots(t *testing.T) {
	d := &Daemon{}
	gitMsg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: "/a"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	if _, ok := d.beginGitDiscover(gitMsg); !ok {
		t.Fatal("git slot refused while free")
	}
	if _, ok := d.beginBrowseScan(ipc.BrowseDirReqPayload{Path: "/a"}); !ok {
		t.Error("a held git-discovery slot blocked a directory listing; the guards are shared")
	}
}

// The parallel of TestBrowseDirResponse_NoPathAndNoFallback: an empty request
// with no fallback is an answer, and it must be distinguishable from the
// genuine finding "no repositories here" — only one of the two is news.
func TestGitReposResponse_NoCWDAndNoFallback(t *testing.T) {
	got := gitReposResponse(ipc.GitReposReqPayload{}, "")

	if got.Error == "" {
		t.Error("Error is empty when there is nothing to scan and no default; " +
			"the caller cannot tell this from 'no repository here'")
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos = %v, want none", got.Repos)
	}
}

// TestDiscoverWithin_TimerFiresOnAHungWalk pins that the CALL is bounded, not
// just the walk.
//
// gitdiscover.Candidates takes a context and checks it between steps, which
// reads like a bound but is not one: a context cannot interrupt an os.Stat or
// os.ReadDir already blocked in the kernel, and an unresponsive mount is exactly
// that. The call holds the single-flight slot, so an unbounded one answers every
// later Alt+G in the session with "another repository scan is already running".
func TestDiscoverWithin_TimerFiresOnAHungWalk(t *testing.T) {
	waitForFSCallsDrained(t)

	block := make(chan struct{})
	orig := discoverCandidates
	discoverCandidates = func(context.Context, string) []string {
		<-block
		return nil
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { discoverCandidates = orig }) })

	start := time.Now()
	repos, answered := discoverWithin("/never-answers", 50*time.Millisecond)
	elapsed := time.Since(start)

	if answered {
		t.Error("a walk that never returns reported as answered")
	}
	if repos != nil {
		t.Errorf("repos = %v, want nil when the walk never answered", repos)
	}
	if elapsed > time.Second {
		t.Errorf("took %v against a 50ms budget; the scan is not bounded", elapsed)
	}
}

// The timeout must reach the user as an error, not as the finding "no repos".
func TestGitReposResponse_HungWalkReportsTimeoutNotEmpty(t *testing.T) {
	waitForFSCallsDrained(t)

	block := make(chan struct{})
	origDiscover, origTimeout := discoverCandidates, gitDiscoverTimeout
	discoverCandidates = func(context.Context, string) []string {
		<-block
		return nil
	}
	gitDiscoverTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		restoreSeam(t, block, func() {
			discoverCandidates, gitDiscoverTimeout = origDiscover, origTimeout
		})
	})

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: "/srv/work"}, "")

	if got.Error == "" {
		t.Error("a timed-out scan reported as 'no repositories here'; only one of " +
			"those two is a finding")
	}
	if got.CWD != "/srv/work" {
		t.Errorf("CWD = %q, want the request echoed even on the timeout path", got.CWD)
	}
}
