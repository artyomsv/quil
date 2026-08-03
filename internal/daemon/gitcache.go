package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/gitinfo"
)

// Git state rides the workspace broadcast in the pane payload — no new
// request/response pair, no staleness key, no single-flight slot, and no added
// latency on the broadcast path. The refresh is a background ticker; the
// broadcast only READS an already-computed answer, so a repository on a mount
// that stopped answering can never slow a state update down.

const (
	// gitRefreshInterval is the ticker cadence. Branch changes are the thing
	// users notice, and they follow a checkout the user just performed — the
	// HEAD-mtime check below makes the common case free, so this can be
	// frequent without being expensive.
	gitRefreshInterval = 5 * time.Second

	// gitProbeTimeout bounds ONE checkout's whole probe (all three commands
	// share it), not each command. A repository on a dead mount otherwise
	// costs the budget three times over.
	gitProbeTimeout = 3 * time.Second

	// gitNegativeTTL is how long a "not a repository" answer is trusted. It is
	// not forever: a pane can sit in a directory that later becomes a
	// repository (git init, or a clone finishing), and re-probing every tick
	// costs exactly as much as probing a real one.
	gitNegativeTTL = 60 * time.Second
)

// gitEntry is one checkout's cached state.
type gitEntry struct {
	info gitinfo.Info
	// repo is false for a directory that is not inside a repository — the
	// negative cache that stops a plain shell pane from re-probing forever.
	repo bool
	// probeDir is a working directory that resolved to this checkout, and is
	// where the probe runs. It cannot be the git dir itself: for a linked
	// worktree that is `<common>/worktrees/<name>`, and a git command run
	// there resolves against the repository rather than the worktree, so it
	// would report the MAIN checkout's branch for every worktree.
	probeDir string
	at       time.Time
	// headMtime is the modification time of the checkout's HEAD file at the
	// last successful probe. A branch change rewrites HEAD, so an unchanged
	// mtime means the branch cannot have moved and the probe can be skipped
	// entirely. Zero when unknown, which forces a probe.
	headMtime time.Time
	// stale marks an entry whose last refresh did not complete — a timeout, or
	// a refused permit. The value is the last one we actually observed, and
	// the flag is what lets the client say so rather than present an old
	// branch as current. A pane whose lookup times out renders its last known
	// value marked stale, never a guess.
	stale bool
}

// gitCache maps a pane CWD to its checkout, and a checkout to its state.
//
// Two levels because the two have different lifetimes and different costs. A
// pane's CWD→checkout mapping changes only when the pane moves; the checkout's
// branch changes whenever the user checks something out. Collapsing them would
// re-run `rev-parse` on every tick for every pane.
//
// Keyed by the PER-CHECKOUT git dir, not the repository's common dir: linked
// worktrees share a common dir while sitting on different branches, so a
// common-dir key would report every worktree as being on whichever branch was
// probed first. See gitinfo.Dirs.
type gitCache struct {
	mu       sync.Mutex
	byDir    map[string]*gitEntry // per-checkout git dir → state
	cwdToDir map[string]string    // pane CWD → per-checkout git dir ("" = not a repo)
	cwdAt    map[string]time.Time // when that CWD was last resolved
}

func newGitCache() *gitCache {
	return &gitCache{
		byDir:    make(map[string]*gitEntry),
		cwdToDir: make(map[string]string),
		cwdAt:    make(map[string]time.Time),
	}
}

// lookup returns a CWD's cached state for the broadcast. Never probes and
// never blocks — the broadcast path must not wait on a filesystem.
func (c *gitCache) lookup(cwd string) (gitinfo.Info, bool, bool) {
	if cwd == "" {
		return gitinfo.Info{}, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dir, known := c.cwdToDir[cwd]
	if !known || dir == "" {
		return gitinfo.Info{}, false, false
	}
	e := c.byDir[dir]
	if e == nil || !e.repo {
		return gitinfo.Info{}, false, false
	}
	return e.info, true, e.stale
}

// gitProbeFn is the seam the refresh runs its probe through, so tests exercise
// the cache's decisions — which CWDs resolve, which checkouts re-probe, what a
// timeout does — without a git binary or a repository on disk.
var gitProbeFn = gitinfo.Probe

// gitDirsFn is the matching seam for the CWD→checkout resolution.
var gitDirsFn = gitinfo.Dirs

// refreshGitInfo brings the cache up to date for the given pane CWDs. Called
// on a ticker from the daemon's own goroutine, never from a broadcast.
//
// Every git invocation runs under the process-wide blocking-FS permit pool. A
// git command on a dead network mount is the same hazard browse.go already
// bounds: the process cannot cancel a syscall that never returns, a goroutine
// parked in one pins an OS thread, and a daemon runs for weeks. Being refused
// a permit marks the entry stale rather than dropping it, because the last
// known branch is still the best answer available.
func (c *gitCache) refresh(ctx context.Context, cwds []string) {
	now := time.Now()

	// Resolve any CWD whose checkout is unknown or whose negative answer has
	// expired. Distinct CWDs in the same checkout collapse here, so the probe
	// loop below sees one entry per checkout however many panes there are.
	for _, cwd := range cwds {
		if cwd == "" {
			continue
		}
		c.mu.Lock()
		dir, known := c.cwdToDir[cwd]
		at := c.cwdAt[cwd]
		c.mu.Unlock()
		// A resolved repository is remembered indefinitely; only the negative
		// answer expires. A checkout does not stop being one.
		if known && (dir != "" || now.Sub(at) < gitNegativeTTL) {
			continue
		}
		if !claimBlockingFSCall() {
			continue // pool exhausted; try again next tick
		}
		gitDir, commonDir, ok := gitDirsFn(ctx, cwd)
		releaseBlockingFSCall()

		c.mu.Lock()
		c.cwdAt[cwd] = now
		if !ok {
			c.cwdToDir[cwd] = ""
			c.mu.Unlock()
			continue
		}
		c.cwdToDir[cwd] = gitDir
		if _, seen := c.byDir[gitDir]; !seen {
			c.byDir[gitDir] = &gitEntry{
				info:     gitinfo.Info{LinkedWorktree: gitDir != commonDir},
				repo:     true,
				probeDir: cwd,
			}
		}
		c.mu.Unlock()
	}

	// Probe each distinct checkout that is actually referenced by a live pane,
	// and only when something could have changed.
	for _, dir := range c.referencedDirs(cwds) {
		c.mu.Lock()
		e := c.byDir[dir]
		var probeDir string
		if e != nil {
			probeDir = e.probeDir
		}
		c.mu.Unlock()
		if e == nil || probeDir == "" {
			continue
		}
		head := headMtime(dir)
		c.mu.Lock()
		fresh := !e.at.IsZero() && !e.stale && !head.IsZero() && head.Equal(e.headMtime)
		c.mu.Unlock()
		if fresh {
			// HEAD has not been rewritten since the last probe, so the branch
			// cannot have moved. Ahead/behind can still drift under a fetch,
			// which the next HEAD change or a restart will pick up — that is
			// the trade this check exists to make.
			continue
		}
		if !claimBlockingFSCall() {
			c.markStale(dir)
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, gitProbeTimeout)
		info, ok := gitProbeFn(probeCtx, probeDir)
		cancel()
		releaseBlockingFSCall()

		c.mu.Lock()
		if !ok {
			// The probe did not answer. Keep the previous value and say so.
			e.stale = true
		} else {
			e.info, e.repo, e.at, e.headMtime, e.stale = info, true, now, head, false
		}
		c.mu.Unlock()
	}
}

// referencedDirs lists the distinct checkouts the given CWDs resolve to, so a
// checkout whose last pane closed stops being probed.
func (c *gitCache) referencedDirs(cwds []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]bool, len(cwds))
	out := make([]string, 0, len(cwds))
	for _, cwd := range cwds {
		dir := c.cwdToDir[cwd]
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

func (c *gitCache) markStale(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.byDir[dir]; e != nil {
		e.stale = true
	}
}

// headMtime reads the checkout's HEAD timestamp. Zero on any error, which
// forces a probe — the check is an optimisation, so failing it must cost
// accuracy nothing.
//
// Stat rather than the permit pool deliberately: this is a single stat on a
// path git itself just resolved, and gating it would spend a permit on the
// very check that exists to avoid spending one.
func headMtime(gitDir string) time.Time {
	fi, err := os.Stat(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// gitWatcher keeps the git cache warm. It runs on the daemon's own goroutine
// so the broadcast path only ever reads an already-computed answer — a
// repository on a mount that stopped answering can slow this ticker down, but
// never a workspace state update.
//
// A refresh that changes nothing broadcasts nothing: requestSnapshot is only
// called when a pane's rendered git state actually moved, or an idle session
// would push a full workspace state every few seconds forever.
func (d *Daemon) gitWatcher() {
	ticker := time.NewTicker(gitRefreshInterval)
	defer ticker.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-d.shutdown
		cancel()
	}()
	for {
		select {
		case <-d.shutdown:
			return
		case <-ticker.C:
			before := d.gitFingerprint()
			d.gitCache.refresh(ctx, d.paneCWDs())
			if d.gitFingerprint() != before {
				d.broadcastState()
			}
		}
	}
}

// paneCWDs collects the working directories of every live pane, deduplicated.
// CWD is PluginMu-protected — the lazy-spawn error path rewrites it — so it is
// read under the same lock every other reader uses.
func (d *Daemon) paneCWDs() []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			pane.PluginMu.Lock()
			cwd := pane.CWD
			pane.PluginMu.Unlock()
			if cwd == "" || seen[cwd] {
				continue
			}
			seen[cwd] = true
			out = append(out, cwd)
		}
	}
	return out
}

// gitFingerprint summarises what the clients would be told, so a refresh that
// found nothing new can skip the broadcast. It covers exactly the fields that
// reach the wire — including staleness, since a pane flipping to stale is a
// visible change.
func (d *Daemon) gitFingerprint() string {
	var b strings.Builder
	for _, cwd := range d.paneCWDs() {
		info, ok, stale := d.gitCache.lookup(cwd)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s\x00%s\x00%t\x00%t\x00%d\x00%d\x00%t\x00%t\n",
			cwd, info.Branch, info.Detached, info.LinkedWorktree,
			info.Ahead, info.Behind, info.HasUpstream, stale)
	}
	return b.String()
}
