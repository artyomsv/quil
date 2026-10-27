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
