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
	"github.com/artyomsv/quil/internal/prinfo"
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

	// prRefreshInterval is how often a checkout's pull request is re-asked
	// about while gh keeps answering. It is a network call to GitHub, so it
	// must never ride the 5 s git tick; a branch change re-asks at once.
	prRefreshInterval = 2 * time.Minute

	// prProbeTimeout bounds one gh run. Longer than the git probe's because
	// the answer crosses a network, and run OFF the tick goroutine so this
	// budget can never stall the branch refresh.
	prProbeTimeout = 10 * time.Second

	// prBackoffCap is the longest wait between two gh runs that keep
	// failing — a remote that is not GitHub, a gh that is not logged in.
	prBackoffCap = 30 * time.Minute

	// maxPRProbePermits is the PR probe's own ceiling on the blocking-call
	// pool, leaving a RESERVE it can never consume. PR probes are the pool's
	// only concurrent holders — every referenced checkout fires one at daemon
	// start — and each holds its permit for the full timeout while GitHub is
	// unreachable (VPN off, DNS hang). Claimed against the whole pool, eight
	// checkouts on an offline laptop refused the git probe (every row `~`),
	// the browse and discover dialogs, a worktree add and a plugin fix for
	// 10 s at a time: the same priority inversion browse_roots.go's
	// maxRootProbePermits exists for. The speculative network call yields;
	// the user's own request gets the reserve.
	maxPRProbePermits = maxBlockingFSCalls / 2

	// forgeRefreshInterval bounds how long a checkout's remote classification
	// is trusted. Remotes change — a pane opened before `git remote add`, a
	// repository moved to another forge — and neither a branch change nor the
	// PR cadence says so, so the one git call is simply repeated this often.
	forgeRefreshInterval = 10 * time.Minute
)

// prBackoff is the wait before the next gh run after `failures` consecutive
// Unknown results: 2, 4, 8, 16, 30 min. Zero or fewer failures is the plain
// interval.
func prBackoff(failures int) time.Duration {
	if failures <= 0 {
		return prRefreshInterval
	}
	d := prRefreshInterval
	for i := 0; i < failures; i++ {
		d *= 2
		if d >= prBackoffCap {
			return prBackoffCap
		}
	}
	return d
}

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

	// Pull request state for the checkout's CURRENT branch, bound to a
	// branch generation: prGen counts every observed branch change
	// (detachment included), and a probe's result is accepted only when the
	// generation it was launched for is still current. Branch A's number is
	// never shown beside branch B, however late A's answer lands.
	pr      prinfo.Info
	prKnown bool // an Answer of the current generation is held
	prStale bool // the last run did not answer; pr is the previous Answer
	// prAt is the last completion (any result) or the generation reset.
	// Zero means "ask at once".
	prAt     time.Time
	prBranch string // the branch prGen was minted for
	prGen    uint64
	// prInFlightGen is the generation of the probe currently running, 0 when
	// none. Stored as a generation rather than a bool so an obsolete
	// completion can tell whether the flag it sees is its own.
	prInFlightGen uint64
	// prFailures counts consecutive Unknown results and drives prBackoff.
	prFailures int
	// forge is the checkout's remote host class, read with one git call and
	// trusted for forgeRefreshInterval from forgeAt (forgeKnown): gh is never
	// run for a remote known not to be GitHub, where every call failed and
	// the backoff was spent on nothing. A failed read stores nothing and is
	// retried next tick. A generation change does not re-read it — a remote
	// does not change with the branch — the interval and a swept-and-
	// recreated entry do.
	forge      prinfo.Forge
	forgeKnown bool
	forgeAt    time.Time
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
//
// A CWD that is not a checkout but holds checkouts as DIRECT children — a
// folder grouping one ticket's worktrees — resolves to "" in cwdToDir like any
// non-repository and lists its children's per-checkout dirs in cwdChildren.
// Each child is an ordinary byDir entry probed in its own directory, so it
// shares everything a pane's own checkout gets: the HEAD-mtime skip, the
// per-checkout dedupe (a pane open INSIDE a child and the pane in the parent
// cost one probe between them), the stale marking. lookup folds the children
// through gitinfo.Combine, which is pure, so it stays off the filesystem.
type gitCache struct {
	mu          sync.Mutex
	byDir       map[string]*gitEntry // per-checkout git dir → state
	cwdToDir    map[string]string    // pane CWD → per-checkout git dir ("" = not a repo)
	cwdAt       map[string]time.Time // when that CWD was last resolved
	cwdChildren map[string][]string  // non-repo CWD → its child checkouts' git dirs

	// prEnabled gates the pull request probe ([daemon] pr_status). Off, no
	// gh is ever run and no PR state is ever written.
	prEnabled bool
	// onPRChange is called — OUTSIDE c.mu — when a probe completion changed
	// what a client would be told. The daemon supplies broadcastState; tests
	// supply a counter.
	onPRChange func()
	// prWG counts in-flight probe goroutines so tests (and nothing else) can
	// wait for completions to land.
	prWG sync.WaitGroup
	// prAvailable caches prAvailableFn's answer for prRefreshInterval from
	// prAvailAt: without it a missing gh cost an exec.LookPath per checkout
	// per 5 s tick forever (Windows: PATH entries × PATHEXT stats).
	prAvailable bool
	prAvailAt   time.Time
}

// prCompletionHook runs at the very end of runPRProbe, after the result was
// applied or discarded and the lock released. A no-op in production; tests
// use it to wait for a completion to have LANDED rather than sleep and hope.
var prCompletionHook = func() {}

func newGitCache() *gitCache {
	return &gitCache{
		byDir:       make(map[string]*gitEntry),
		cwdToDir:    make(map[string]string),
		cwdAt:       make(map[string]time.Time),
		cwdChildren: make(map[string][]string),
	}
}

// prProbeFn and prAvailableFn are the seams the PR probe runs through, so
// tests exercise the cache's decisions — the two triggers, the gates, the
// generation rules, the backoff — without gh, a login or a network.
var (
	prProbeFn     = prinfo.Probe
	prAvailableFn = prinfo.Available
	prForgeFn     = prinfo.RemoteForge
)

// lookupPR returns a CWD's pull request for the broadcast: the Info, whether
// an Answer is held at all, and whether the last run failed to refresh it.
// Never probes and never blocks, like lookup.
func (c *gitCache) lookupPR(cwd string) (prinfo.Info, bool, bool) {
	if cwd == "" {
		return prinfo.Info{}, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dir, known := c.cwdToDir[cwd]
	if !known || dir == "" {
		return prinfo.Info{}, false, false
	}
	e := c.byDir[dir]
	if e == nil || !e.repo {
		return prinfo.Info{}, false, false
	}
	return e.pr, e.prKnown, e.prStale
}

// lookupBoth is lookup and lookupPR under ONE lock hold, for the broadcast:
// read separately, a branch change landing between the two pairs branch B
// with A's pull request in one frame — the thing the generation exists to
// prevent. Never probes and never blocks.
func (c *gitCache) lookupBoth(cwd string) (info gitinfo.Info, ok, stale bool, pr prinfo.Info, prKnown, prStale bool) {
	if cwd == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dir, known := c.cwdToDir[cwd]
	if !known {
		return
	}
	if dir == "" {
		// A folder of child checkouts: its git state is their combination,
		// as lookup answers. A pull request belongs to one branch of one
		// checkout, so none is reported for the folder.
		info, ok, stale = c.lookupChildrenLocked(cwd)
		return
	}
	e := c.byDir[dir]
	if e == nil || !e.repo {
		return
	}
	return e.info, true, e.stale, e.pr, e.prKnown, e.prStale
}

// notePRBranch records the checkout's current branch on the PR side. Called
// with c.mu held. A changed branch is a new GENERATION: the old number is
// cleared at once — stale or not, it describes another branch — and the new
// branch is asked about immediately. An in-flight probe is not cancelled (it
// holds a permit it will release itself); its result fails the generation
// check in runPRProbe. Clearing the in-flight marker is what lets the new
// branch be asked at once rather than after the obsolete probe's full
// timeout. Same branch: nothing changes.
func (e *gitEntry) notePRBranch(branch string) {
	if branch == e.prBranch {
		return
	}
	e.prBranch = branch
	e.prGen++
	e.pr, e.prKnown, e.prStale = prinfo.Info{}, false, false
	e.prFailures, e.prAt = 0, time.Time{}
	e.prInFlightGen = 0
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
	if !known {
		return gitinfo.Info{}, false, false
	}
	if dir == "" {
		return c.lookupChildrenLocked(cwd)
	}
	e := c.byDir[dir]
	if e == nil || !e.repo {
		return gitinfo.Info{}, false, false
	}
	return e.info, true, e.stale
}

// lookupChildrenLocked answers for a non-repository CWD from its children's
// entries. Stale when ANY child is: the row claims one thing about the whole
// folder, and it cannot be current while a part of it is not.
func (c *gitCache) lookupChildrenLocked(cwd string) (gitinfo.Info, bool, bool) {
	dirs := c.cwdChildren[cwd]
	if len(dirs) == 0 {
		return gitinfo.Info{}, false, false
	}
	infos := make([]gitinfo.Info, 0, len(dirs))
	stale := false
	for _, dir := range dirs {
		e := c.byDir[dir]
		if e == nil || !e.repo {
			continue
		}
		infos = append(infos, e.info)
		stale = stale || e.stale
	}
	if len(infos) == 0 {
		return gitinfo.Info{}, false, false
	}
	return gitinfo.Combine(infos), true, stale
}

// gitProbeFn is the seam the refresh runs its probe through, so tests exercise
// the cache's decisions — which CWDs resolve, which checkouts re-probe, what a
// timeout does — without a git binary or a repository on disk.
var gitProbeFn = gitinfo.Probe

// gitDirsFn is the matching seam for the CWD→checkout resolution.
var gitDirsFn = gitinfo.Dirs

// gitChildrenFn is the seam for the one-level walk under a non-repository.
var gitChildrenFn = gitinfo.ListChildRepos

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
		var children []gitinfo.ChildRepo
		if !ok {
			// Not a checkout: look one level down, under the same permit
			// and one shared deadline for the whole walk. Bounded by
			// gitinfo.MaxChildDirs on the far side and by gitProbeTimeout
			// here, and repeated only when the negative answer expires —
			// the cadence a plain non-repository already re-resolves at,
			// which is also what lets a clone finishing under the folder
			// show up.
			childCtx, cancel := context.WithTimeout(ctx, gitProbeTimeout)
			children = gitChildrenFn(childCtx, cwd)
			cancel()
		}
		releaseBlockingFSCall()

		c.mu.Lock()
		c.cwdAt[cwd] = now
		if !ok {
			c.cwdToDir[cwd] = ""
			c.setChildrenLocked(cwd, children)
			c.mu.Unlock()
			continue
		}
		c.cwdToDir[cwd] = gitDir
		c.ensureEntryLocked(gitDir, commonDir, cwd)
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
			// The PR generation moves in THIS critical section, not in
			// prTick after the loop: the loop probes checkouts serially, so
			// with a later checkout slow to answer a broadcast could read
			// branch B beside A's still-known PR for seconds.
			e.notePRBranch(info.Branch)
		}
		c.mu.Unlock()
	}

	c.prTick(ctx, now, c.referencedDirs(cwds))
	c.sweep(cwds)
}

// ensureEntryLocked registers a checkout the first time a CWD — or a child
// walk — resolves to it, probed in probeDir. An existing entry is left alone:
// the second CWD into one checkout is the dedupe this cache exists for.
func (c *gitCache) ensureEntryLocked(gitDir, commonDir, probeDir string) {
	if _, seen := c.byDir[gitDir]; seen {
		return
	}
	c.byDir[gitDir] = &gitEntry{
		info: gitinfo.Info{
			LinkedWorktree: gitDir != commonDir,
			// Named here as well as in gitinfo.Probe, and neither is
			// redundant: this entry is what lookup() answers with
			// between resolving the CWD and the first probe landing —
			// the tick right after a pane is created in a worktree,
			// i.e. exactly when the user is looking at the row. Probe
			// covers every tick after that, because it replaces this
			// whole struct.
			WorktreeName: worktreeName(gitDir, commonDir),
		},
		repo:     true,
		probeDir: probeDir,
	}
}

// setChildrenLocked records a non-repository CWD's child checkouts, replacing
// whatever the previous resolution found (a child cloned or removed since),
// and registers each child as its own entry probed in its own directory —
// never in the parent, where git resolves nothing.
func (c *gitCache) setChildrenLocked(cwd string, children []gitinfo.ChildRepo) {
	if len(children) == 0 {
		delete(c.cwdChildren, cwd)
		return
	}
	dirs := make([]string, 0, len(children))
	for _, ch := range children {
		c.ensureEntryLocked(ch.GitDir, ch.CommonDir, ch.Dir)
		dirs = append(dirs, ch.GitDir)
	}
	c.cwdChildren[cwd] = dirs
}

// prTick starts a pull request probe for every referenced checkout that is
// due. It is evaluated on EVERY tick, independently of whether the git probe
// above ran: that probe skips a checkout whose HEAD mtime is unchanged, which
// is the steady state, so a PR check placed "after the git probe" would never
// fire on its 2-minute cadence. It reads e.info.Branch as it stands — a stale
// git entry still holds the last branch seen, which is the right branch to
// ask about.
//
// The probe itself runs on its own goroutine (runPRProbe). refresh is one
// goroutine probing checkouts serially, and a 10 s gh timeout inline would
// stall the 5 s branch refresh of every checkout behind an unreachable
// GitHub — N checkouts, up to N×10 s per tick.
func (c *gitCache) prTick(ctx context.Context, now time.Time, dirs []string) {
	c.mu.Lock()
	enabled := c.prEnabled
	c.mu.Unlock()
	if !enabled || len(dirs) == 0 {
		return
	}
	available := c.ghAvailable(now)
	for _, dir := range dirs {
		c.mu.Lock()
		e := c.byDir[dir]
		if e == nil {
			c.mu.Unlock()
			continue
		}
		branch := e.info.Branch
		// Normally a no-op: refresh already moved the generation where it
		// published the branch. It still catches the placeholder entry
		// (resolved, not yet probed) and keeps the invariant local.
		e.notePRBranch(branch)
		due := e.prAt.IsZero() || now.Sub(e.prAt) >= prBackoff(e.prFailures)
		if branch == "" || e.prInFlightGen != 0 || !due {
			c.mu.Unlock()
			continue
		}
		gen, probeDir := e.prGen, e.probeDir
		c.mu.Unlock()

		if !available {
			// No gh on PATH: nothing known, nothing stale, no permit spent.
			// The user without gh sees today's row, not a `~`.
			continue
		}
		if !c.forgeKnownFor(ctx, now, dir, e, probeDir) {
			continue // no permit or no answer this tick, or a remote gh cannot answer for
		}
		if !claimBlockingFSCallUpTo(maxPRProbePermits) {
			c.markPRStale(dir)
			continue
		}
		c.mu.Lock()
		if c.byDir[dir] != e || e.prGen != gen {
			// Swept or moved on while the permit was claimed.
			c.mu.Unlock()
			releaseBlockingFSCall()
			continue
		}
		e.prInFlightGen = gen
		c.mu.Unlock()
		c.prWG.Add(1)
		go c.runPRProbe(ctx, dir, e, gen, branch, probeDir)
	}
}

// runPRProbe runs one gh probe and applies its result under the generation
// rules: the result is accepted only when c.byDir[dir] is still the SAME
// entry and its generation is the one this probe was launched for. Otherwise
// it is discarded entirely — it may not write pr, may not touch prAt or
// prFailures (the new branch's retry clock), and may not clear an in-flight
// marker that is not its own.
//
// c.mu is released BEFORE onPRChange runs: the callback reaches lookup (via
// gitFingerprint), which takes the same non-reentrant mutex.
func (c *gitCache) runPRProbe(ctx context.Context, dir string, e *gitEntry, gen uint64, branch, probeDir string) {
	// First deferred, so it runs LAST: after the permit is back and prWG is
	// decremented, on the discard path as well as the apply path.
	defer prCompletionHook()
	defer c.prWG.Done()
	defer releaseBlockingFSCall()
	pctx, cancel := context.WithTimeout(ctx, prProbeTimeout)
	defer cancel()
	info, res := prProbeFn(pctx, probeDir, branch)

	c.mu.Lock()
	if c.byDir[dir] != e || e.prGen != gen {
		if c.byDir[dir] == e && e.prInFlightGen == gen {
			e.prInFlightGen = 0
		}
		c.mu.Unlock()
		return
	}
	before := prTuple{e.pr, e.prKnown, e.prStale}
	switch res {
	case prinfo.Answer:
		e.pr, e.prKnown, e.prStale, e.prFailures = info, true, false, 0
	case prinfo.NoPR:
		e.pr, e.prKnown, e.prStale, e.prFailures = prinfo.Info{}, false, false, 0
	default:
		// Only a held Answer can be stale: with nothing known there is
		// nothing to mark, and the row stays exactly as today.
		e.prStale = e.prKnown
		e.prFailures++
	}
	e.prAt = time.Now()
	e.prInFlightGen = 0
	changed := before != prTuple{e.pr, e.prKnown, e.prStale}
	c.mu.Unlock()

	// A probe cancelled by shutdown returns Unknown and can flip a held
	// value stale; nobody is left to broadcast that to.
	if changed && ctx.Err() == nil && c.onPRChange != nil {
		c.onPRChange()
	}
}

// forgeKnownFor reports whether gh may be asked about this checkout at all,
// reading the remote when the entry has no classification or the one it has
// is older than forgeRefreshInterval. Called with c.mu NOT held. Returns
// false when the remote is a forge gh cannot answer for (nothing is probed,
// nothing is marked — and a PR row a previous classification left behind is
// cleared), when the read FAILED (no answer: nothing cached, retried next
// tick), or when the read could not get a permit this tick (it is a git call
// like any other). In every false case the gate is never skipped, so gh
// never runs unchecked.
func (c *gitCache) forgeKnownFor(ctx context.Context, now time.Time, dir string, e *gitEntry, probeDir string) bool {
	c.mu.Lock()
	fresh := e.forgeKnown && now.Sub(e.forgeAt) < forgeRefreshInterval
	forge := e.forge
	c.mu.Unlock()
	if fresh {
		return forge != prinfo.ForgeOther
	}
	if !claimBlockingFSCallUpTo(maxPRProbePermits) {
		return false
	}
	fctx, cancel := context.WithTimeout(ctx, gitProbeTimeout)
	forge, ok := prForgeFn(fctx, probeDir)
	cancel()
	releaseBlockingFSCall()
	if !ok {
		return false
	}
	c.mu.Lock()
	if c.byDir[dir] == e {
		e.forge, e.forgeKnown, e.forgeAt = forge, true, now
		if forge == prinfo.ForgeOther {
			// The remote moved off GitHub (or never was): whatever gh last
			// said about this checkout no longer describes its remote.
			e.pr, e.prKnown, e.prStale = prinfo.Info{}, false, false
		}
	}
	c.mu.Unlock()
	return forge != prinfo.ForgeOther
}

// ghAvailable answers prAvailableFn, re-asked at most once per
// prRefreshInterval. Called with c.mu NOT held.
func (c *gitCache) ghAvailable(now time.Time) bool {
	c.mu.Lock()
	fresh := !c.prAvailAt.IsZero() && now.Sub(c.prAvailAt) < prRefreshInterval
	avail := c.prAvailable
	c.mu.Unlock()
	if fresh {
		return avail
	}
	avail = prAvailableFn()
	c.mu.Lock()
	c.prAvailable, c.prAvailAt = avail, now
	c.mu.Unlock()
	return avail
}

// prTuple is what a client is told about a checkout's pull request; a
// completion that leaves it unchanged broadcasts nothing.
type prTuple struct {
	info  prinfo.Info
	known bool
	stale bool
}

// markPRStale records a refused permit: a held Answer is marked stale, and the
// clock is zeroed so the next tick tries again rather than waiting out the
// interval behind a pool that was momentarily full.
func (c *gitCache) markPRStale(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.byDir[dir]; e != nil {
		if e.prKnown {
			e.prStale = true
		}
		e.prAt = time.Time{}
	}
}

// sweep drops entries for CWDs no live pane is in any more.
//
// referencedDirs bounds what is PROBED, not what is stored, and the three maps
// only ever grew. OSC 7 rewrites Pane.CWD on every cd, so a shell roaming a
// monorepo adds an entry per distinct directory it visits and byDir keeps every
// checkout ever seen — for a daemon that runs for weeks, which is the lifetime
// this file's other budgets are all reasoned from.
//
// Re-resolving a directory the user returns to costs one rev-parse on the next
// tick, off the broadcast path. That is the right trade against holding every
// path a shell has ever printed.
func (c *gitCache) sweep(cwds []string) {
	live := make(map[string]bool, len(cwds))
	for _, cwd := range cwds {
		if cwd != "" {
			live[cwd] = true
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	keptDirs := make(map[string]bool, len(live))
	for cwd, dir := range c.cwdToDir {
		if !live[cwd] {
			delete(c.cwdToDir, cwd)
			delete(c.cwdAt, cwd)
			delete(c.cwdChildren, cwd)
			continue
		}
		if dir != "" {
			keptDirs[dir] = true
		}
		for _, child := range c.cwdChildren[cwd] {
			keptDirs[child] = true
		}
	}
	for dir := range c.byDir {
		if !keptDirs[dir] {
			delete(c.byDir, dir)
		}
	}
}

// referencedDirs lists the distinct checkouts the given CWDs resolve to —
// directly, or as the children of a non-repository folder — so a checkout
// whose last pane closed stops being probed.
func (c *gitCache) referencedDirs(cwds []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]bool, len(cwds))
	out := make([]string, 0, len(cwds))
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, cwd := range cwds {
		if dir := c.cwdToDir[cwd]; dir != "" {
			add(dir)
			continue
		}
		for _, child := range c.cwdChildren[cwd] {
			add(child)
		}
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

// worktreeName derives a linked worktree's name from its per-checkout git
// dir, which git spells <common>/worktrees/<name>.
//
// The same derivation gitinfo.Probe makes, duplicated for the placeholder
// entry rather than shared through an exported function: it is one
// filepath.Base behind a comparison, and exporting it from gitinfo would
// invite a caller to derive a name from a path that did not come from Dirs —
// where the relationship this depends on does not hold.
func worktreeName(gitDir, commonDir string) string {
	if gitDir == commonDir {
		return ""
	}
	return filepath.Base(gitDir)
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
		info, ok, stale, pr, prKnown, prStale := d.gitCache.lookupBoth(cwd)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s\x00%s\x00%t\x00%t\x00%s\x00%d\x00%d\x00%t\x00%t\x00%d\x00%d\x00%s\x00%t\x00%t\n",
			cwd, info.Branch, info.Detached, info.LinkedWorktree, info.WorktreeName,
			info.Ahead, info.Behind, info.HasUpstream, stale, info.ChildRepos,
			pr.Number, pr.Status, prKnown, prStale)
	}
	return b.String()
}
