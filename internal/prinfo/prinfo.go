// Package prinfo reads the one fact about a checkout that git itself cannot
// know: which pull request its branch belongs to, and where that request
// stands. Only GitHub knows, so this package asks `gh`, the GitHub CLI, and
// folds its answer to a small closed set of statuses the sidebar colors.
//
// A sibling of gitinfo: stdlib-only, every command through one seam so tests
// need no gh, no login and no network, and the same Windows CREATE_NO_WINDOW
// treatment, because the daemon owns no console and a child started from a
// console-less parent gets a brand-new window allocated for it.
//
// The branch is passed to gh as an ARGUMENT, never inferred from the
// checkout. Argument-free `gh pr view` asks about whatever branch the checkout
// is on at the moment gh runs, and the daemon probes asynchronously, so that
// can differ from the branch the daemon recorded when it launched the probe.
// The argument is the branch the git probe saw, which is the string the row
// shows, so the answer and the row name the same branch by construction. It
// travels through argv, never a shell.
//
// Stderr is read for exactly one classification — "no pull requests found",
// which is a positive fact, not a failure — and is otherwise discarded. It
// can carry the repository URL, so nothing here logs or surfaces it.
package prinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Status is where a pull request stands, as the sidebar tells it. The
// spellings are the wire contract: the TUI matches them by name and renders
// anything it does not know as StatusOpen, so a newer daemon never blanks an
// older client's chip.
type Status string

const (
	StatusMerged    Status = "merged"
	StatusClosed    Status = "closed"    // closed without merging; GitHub has no "declined"
	StatusAttention Status = "attention" // red CI, changes requested, or a merge conflict
	StatusRunning   Status = "running"   // a check is still in flight
	StatusApproved  Status = "approved"
	StatusDraft     Status = "draft"
	StatusOpen      Status = "open"
)

// Info is one branch's pull request. The zero value means "no request known".
type Info struct {
	Number int
	Status Status
}

// Result classifies what a probe learned. The caller never parses an error:
// the three classes are the whole contract.
type Result int

const (
	// Unknown means gh could not answer — missing, not logged in, network
	// down, timed out, or unparseable output. The caller keeps what it had.
	Unknown Result = iota
	// Answer means a pull request was found and Info describes it.
	Answer
	// NoPR means gh looked and the branch has no pull request. A positive
	// fact: the caller clears what it had.
	NoPR
)

// Check is one element of gh's statusCheckRollup. GitHub sends two shapes in
// one array — a CheckRun (Status + Conclusion) and a StatusContext (State) —
// and this struct holds the union so one decode reads both.
type Check struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// View is the subset of `gh pr view --json` this package asks for.
type View struct {
	Number            int     `json:"number"`
	State             string  `json:"state"`
	IsDraft           bool    `json:"isDraft"`
	ReviewDecision    string  `json:"reviewDecision"`
	Mergeable         string  `json:"mergeable"`
	StatusCheckRollup []Check `json:"statusCheckRollup"`
}

// jsonFields is the --json argument, fixed: the fields View decodes.
const jsonFields = "number,state,isDraft,reviewDecision,mergeable,statusCheckRollup"

// noPRMarker is the one stderr phrase read: gh's own wording for a branch
// that has no pull request.
const noPRMarker = "no pull requests found"

// ghWaitDelay caps how long a killed gh may hold its output pipe open —
// CommandContext kills the process, but Output still waits for every holder
// of the pipe, and gh's own children (a credential helper) inherit it.
const ghWaitDelay = 2 * time.Second

// runGh is the seam every command goes through. It returns stdout and stderr
// separately: stdout is the JSON, stderr is classified and dropped.
var runGh = func(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	hideWindow(cmd)
	cmd.WaitDelay = ghWaitDelay
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	return string(out), errBuf.String(), err
}

// Available reports whether gh is on PATH. Cheap — no process — so callers
// check it before spending a permit on a probe that cannot start.
func Available() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// Probe asks gh about one branch of the checkout at dir. An empty branch is a
// detached HEAD: there is nothing to ask about, and gh is never run.
func Probe(ctx context.Context, dir, branch string) (Info, Result) {
	// An all-digit branch is refused too: gh reads a numeric argument as a
	// PULL REQUEST NUMBER, so branch "1234" would answer with PR #1234 —
	// somebody else's. No chip beats a wrong one.
	if branch == "" || allDigits(branch) {
		return Info{}, Unknown
	}
	out, errOut, err := runGh(ctx, dir, "pr", "view", branch, "--json", jsonFields)
	if err != nil {
		if strings.Contains(errOut, noPRMarker) {
			return Info{}, NoPR
		}
		return Info{}, Unknown
	}
	var v View
	if json.Unmarshal([]byte(out), &v) != nil || v.Number <= 0 {
		return Info{}, Unknown
	}
	return Info{Number: v.Number, Status: Derive(v)}, Answer
}

// Forge is what a checkout's remote host is, as far as this package cares:
// whether gh can answer for it at all.
type Forge int

const (
	// ForgeUnknown is a host this package cannot place — a self-hosted
	// GitHub Enterprise under a name without "github" in it looks exactly
	// like any other git server. gh is asked; its answer settles it.
	ForgeUnknown Forge = iota
	// ForgeGitHub is github.com or a host named for GitHub.
	ForgeGitHub
	// ForgeOther is a host known NOT to be GitHub (GitLab, Bitbucket,
	// Codeberg, Azure DevOps, sourcehut, Gitea), or no remote at all. gh is
	// never run for it: every call would fail, and a merge request is not a
	// pull request this package knows how to read.
	ForgeOther
)

// runGit is the seam for the one git call this package makes (RemoteForge).
// Same shape as gitinfo's: raw stdout, stderr discarded.
var runGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	hideWindow(cmd)
	cmd.WaitDelay = ghWaitDelay
	out, err := cmd.Output()
	return string(out), err
}

// RemoteForge classifies the checkout's remote with one git call, preferring
// `origin` (what gh itself resolves against) and otherwise the first remote
// listed. No remote, or no answer, is ForgeOther: gh has nothing to ask.
func RemoteForge(ctx context.Context, dir string) Forge {
	out, err := runGit(ctx, dir, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return ForgeOther
	}
	first := ""
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "remote.origin.url" {
			return ForgeOf(fields[1])
		}
		if first == "" {
			first = fields[1]
		}
	}
	if first == "" {
		return ForgeOther
	}
	return ForgeOf(first)
}

// ForgeOf classifies one remote URL by its host: https, ssh:// and the scp
// form (git@host:path) are all read; a local path has no host and is
// Unknown. The host compare ignores case and a port.
func ForgeOf(url string) Forge {
	host := strings.ToLower(remoteHost(url))
	if host == "" {
		return ForgeUnknown
	}
	switch {
	case host == "github.com", strings.Contains(host, "github"):
		return ForgeGitHub
	case strings.Contains(host, "gitlab"), strings.Contains(host, "bitbucket"),
		strings.Contains(host, "gitea"), strings.Contains(host, "codeberg"),
		strings.HasSuffix(host, "dev.azure.com"), strings.HasSuffix(host, "visualstudio.com"),
		host == "git.sr.ht":
		return ForgeOther
	}
	return ForgeUnknown
}

// remoteHost pulls the host out of a remote URL, or "" when it has none.
func remoteHost(url string) string {
	rest := url
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	} else if i := strings.Index(rest, ":"); i >= 0 && !strings.Contains(rest[:i], "/") {
		// scp form: [user@]host:path — the colon comes before any slash.
		rest = rest[:i]
	} else {
		return "" // a local path
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, ":"); i >= 0 {
		rest = rest[:i] // port
	}
	return rest
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// verdict is what one check element says once its shape is folded away.
type verdict int

const (
	ok verdict = iota
	pending
	failed
)

// fold reads one rollup element. A CheckRun that has completed speaks through
// its conclusion; one that has not, through its status; a StatusContext
// through its state. Anything unlisted — a value GitHub adds later, an element
// of neither shape — is ok: the closed-set rule applied to GitHub's side, so
// an unknown value can never paint the chip orange.
func fold(c Check) verdict {
	switch {
	case c.Conclusion != "" || c.Status == "COMPLETED":
		switch c.Conclusion {
		case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			return failed
		case "STALE":
			// GitHub marks a run stale when the head moved under it, so it
			// says nothing about the current commit; treating it as green
			// beside an approved review would claim a verdict nobody gave.
			return pending
		}
		return ok
	case c.Status != "":
		switch c.Status {
		case "QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED":
			return pending
		}
		return ok
	default:
		switch c.State {
		case "FAILURE", "ERROR":
			return failed
		case "PENDING", "EXPECTED":
			return pending
		}
		return ok
	}
}

// Derive folds a View to its Status. Precedence is the order of the cases:
// the first that matches wins. Exported so the table lives in one place and
// its tests read like the table.
func Derive(v View) Status {
	switch v.State {
	case "MERGED":
		return StatusMerged
	case "CLOSED":
		return StatusClosed
	}
	anyFailed, anyPending := false, false
	for _, c := range v.StatusCheckRollup {
		switch fold(c) {
		case failed:
			anyFailed = true
		case pending:
			anyPending = true
		}
	}
	switch {
	case anyFailed, v.ReviewDecision == "CHANGES_REQUESTED", v.Mergeable == "CONFLICTING":
		return StatusAttention
	case anyPending:
		return StatusRunning
	case v.ReviewDecision == "APPROVED":
		return StatusApproved
	case v.IsDraft:
		return StatusDraft
	}
	return StatusOpen
}
