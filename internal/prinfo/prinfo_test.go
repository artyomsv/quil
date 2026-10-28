package prinfo

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// ghReply is one canned gh invocation: what it printed and whether it failed.
type ghReply struct {
	stdout string
	stderr string
	err    error
}

// fakeGh swaps the command seam for a table of canned answers keyed by the
// joined argv, so these tests never need gh, a login or a network — and so a
// wrong ARGUMENT is a test failure rather than a silently different answer.
func fakeGh(t *testing.T, answers map[string]ghReply) *[]string {
	t.Helper()
	var calls []string
	orig := runGh
	t.Cleanup(func() { runGh = orig })
	runGh = func(_ context.Context, _ string, args ...string) (string, string, error) {
		key := strings.Join(args, " ")
		calls = append(calls, key)
		r, ok := answers[key]
		if !ok {
			t.Errorf("unexpected gh invocation: gh %s", key)
			return "", "", errors.New("unexpected")
		}
		return r.stdout, r.stderr, r.err
	}
	return &calls
}

const argView = "pr view feat/x --json number,state,isDraft,reviewDecision,mergeable,statusCheckRollup"

func TestDerive(t *testing.T) {
	run := func(s, c string) Check { return Check{Status: s, Conclusion: c} }
	ctx := func(state string) Check { return Check{State: state} }
	cases := []struct {
		name string
		v    View
		want Status
	}{
		{"merged wins over everything", View{State: "MERGED", ReviewDecision: "CHANGES_REQUESTED"}, StatusMerged},
		{"closed is not declined but is red", View{State: "CLOSED", ReviewDecision: "APPROVED"}, StatusClosed},
		{"failed check run", View{State: "OPEN", StatusCheckRollup: []Check{run("COMPLETED", "FAILURE")}}, StatusAttention},
		{"timed out check run", View{State: "OPEN", StatusCheckRollup: []Check{run("COMPLETED", "TIMED_OUT")}}, StatusAttention},
		{"failed status context", View{State: "OPEN", StatusCheckRollup: []Check{ctx("ERROR")}}, StatusAttention},
		{"changes requested", View{State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}, StatusAttention},
		{"conflicting", View{State: "OPEN", Mergeable: "CONFLICTING"}, StatusAttention},
		{"attention beats running", View{State: "OPEN", StatusCheckRollup: []Check{run("IN_PROGRESS", ""), run("COMPLETED", "FAILURE")}}, StatusAttention},
		{"in-progress run", View{State: "OPEN", StatusCheckRollup: []Check{run("IN_PROGRESS", "")}}, StatusRunning},
		{"queued run", View{State: "OPEN", StatusCheckRollup: []Check{run("QUEUED", "")}}, StatusRunning},
		{"pending context", View{State: "OPEN", StatusCheckRollup: []Check{ctx("PENDING")}}, StatusRunning},
		{"expected context", View{State: "OPEN", StatusCheckRollup: []Check{ctx("EXPECTED")}}, StatusRunning},
		{"STALE run is pending not green", View{State: "OPEN", ReviewDecision: "APPROVED", StatusCheckRollup: []Check{run("COMPLETED", "STALE")}}, StatusRunning},
		{"running beats approved", View{State: "OPEN", ReviewDecision: "APPROVED", StatusCheckRollup: []Check{run("IN_PROGRESS", "")}}, StatusRunning},
		{"approved with green checks", View{State: "OPEN", ReviewDecision: "APPROVED", StatusCheckRollup: []Check{run("COMPLETED", "SUCCESS"), ctx("SUCCESS")}}, StatusApproved},
		{"approved with no checks at all", View{State: "OPEN", ReviewDecision: "APPROVED"}, StatusApproved},
		{"neutral and skipped are ok", View{State: "OPEN", ReviewDecision: "APPROVED", StatusCheckRollup: []Check{run("COMPLETED", "NEUTRAL"), run("COMPLETED", "SKIPPED")}}, StatusApproved},
		{"draft", View{State: "OPEN", IsDraft: true}, StatusDraft},
		{"draft loses to running", View{State: "OPEN", IsDraft: true, StatusCheckRollup: []Check{run("QUEUED", "")}}, StatusRunning},
		{"plain open", View{State: "OPEN"}, StatusOpen},
		{"open with review required", View{State: "OPEN", ReviewDecision: "REVIEW_REQUIRED"}, StatusOpen},
		{"unknown conclusion is ok, not orange", View{State: "OPEN", StatusCheckRollup: []Check{run("COMPLETED", "SOMETHING_NEW")}}, StatusOpen},
		{"element of neither shape is ignored", View{State: "OPEN", StatusCheckRollup: []Check{{}}}, StatusOpen},
		{"unknown state string is open", View{State: "WEIRD"}, StatusOpen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Derive(tc.v); got != tc.want {
				t.Errorf("Derive = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProbe_Answer(t *testing.T) {
	calls := fakeGh(t, map[string]ghReply{argView: {stdout: `{"number":277,"state":"OPEN","isDraft":false,"reviewDecision":"APPROVED","mergeable":"MERGEABLE","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}`}})

	info, res := Probe(context.Background(), "/repo", "feat/x")
	if res != Answer || info.Number != 277 || info.Status != StatusApproved {
		t.Fatalf("Probe = %+v, %v; want Answer {277 approved}", info, res)
	}
	if len(*calls) != 1 || (*calls)[0] != argView {
		t.Fatalf("argv = %v, want exactly one %q", *calls, argView)
	}
}

func TestProbe_NoPR(t *testing.T) {
	fakeGh(t, map[string]ghReply{argView: {stderr: "no pull requests found for branch \"feat/x\"\n", err: errors.New("exit status 1")}})

	info, res := Probe(context.Background(), "/repo", "feat/x")
	if res != NoPR {
		t.Fatalf("res = %v, want NoPR", res)
	}
	if info != (Info{}) {
		t.Errorf("info = %+v, want zero on NoPR", info)
	}
}

func TestProbe_Unknown(t *testing.T) {
	for name, r := range map[string]ghReply{
		"auth":     {stderr: "To get started with GitHub CLI, please run:  gh auth login\n", err: errors.New("exit status 4")},
		"network":  {stderr: "error connecting to api.github.com\n", err: errors.New("exit status 1")},
		"bad json": {stdout: "{not json"},
		"zero num": {stdout: `{"number":0,"state":"OPEN"}`},
	} {
		t.Run(name, func(t *testing.T) {
			fakeGh(t, map[string]ghReply{argView: r})
			if _, res := Probe(context.Background(), "/repo", "feat/x"); res != Unknown {
				t.Errorf("res = %v, want Unknown", res)
			}
		})
	}
}

// An empty branch is a detached HEAD: gh has nothing to resolve, and running
// it would ask about whatever the checkout happens to be on NOW.
func TestProbe_EmptyBranchNeverRunsGh(t *testing.T) {
	calls := fakeGh(t, nil)
	if _, res := Probe(context.Background(), "/repo", ""); res != Unknown || len(*calls) != 0 {
		t.Fatalf("empty branch: res=%v calls=%v, want Unknown and no gh run", res, *calls)
	}
}

// gh reads an all-digit argument as a PULL REQUEST NUMBER, so a branch
// literally named "1234" would show PR #1234 — somebody else's. Such a branch
// gets no chip rather than a wrong one.
func TestProbe_AllDigitBranchNeverRunsGh(t *testing.T) {
	calls := fakeGh(t, nil)
	if _, res := Probe(context.Background(), "/repo", "1234"); res != Unknown || len(*calls) != 0 {
		t.Fatalf("all-digit branch: res=%v calls=%v, want Unknown and no gh run", res, *calls)
	}
}

func TestForgeOf(t *testing.T) {
	cases := []struct {
		url  string
		want Forge
	}{
		{"https://github.com/artyomsv/quil.git", ForgeGitHub},
		{"git@github.com:artyomsv/quil.git", ForgeGitHub},
		{"ssh://git@github.com/artyomsv/quil.git", ForgeGitHub},
		{"https://github.corp.example/org/repo", ForgeGitHub}, // GitHub Enterprise, named
		{"https://gitlab.com/group/repo.git", ForgeOther},
		{"git@gitlab.example.org:group/repo.git", ForgeOther},
		{"https://bitbucket.org/team/repo.git", ForgeOther},
		{"git@bitbucket.org:team/repo.git", ForgeOther},
		{"https://codeberg.org/user/repo", ForgeOther},
		{"https://dev.azure.com/org/project/_git/repo", ForgeOther},
		{"git@ssh.dev.azure.com:v3/org/project/repo", ForgeOther},
		{"https://org.visualstudio.com/project/_git/repo", ForgeOther},
		{"https://git.sr.ht/~user/repo", ForgeOther},
		{"https://gitea.example.com/user/repo.git", ForgeOther},
		{"https://git.example.com/user/repo.git", ForgeUnknown}, // could be GHE under another name: ask gh
		{"ssh://user@GitHub.COM:22/x/y", ForgeGitHub},            // host compare ignores case and port
		{"/srv/git/repo.git", ForgeUnknown},
		{"", ForgeUnknown},
	}
	for _, tc := range cases {
		if got := ForgeOf(tc.url); got != tc.want {
			t.Errorf("ForgeOf(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// RemoteForge reads EVERY remote with one git call and answers for the set,
// the way gh itself picks a repository: any GitHub remote means gh can
// answer (a GitLab origin beside a GitHub upstream is the reviewed case —
// gh filters the remotes to the hosts it supports, it does not take
// origin), a host it cannot place means ask, and only a set with no usable
// remote — or no remote at all — is Other. A read that FAILED is not an
// answer: ok is false and the caller retries, so a timeout on the first
// tick cannot become a permanent "not GitHub".
func TestRemoteForge(t *testing.T) {
	const argRemotes = "config --get-regexp ^remote\\..*\\.url$"
	cases := []struct {
		name   string
		stdout string
		err    error
		want   Forge
		wantOK bool
	}{
		{"origin github", "remote.origin.url git@github.com:a/b.git\n", nil, ForgeGitHub, true},
		{"gitlab origin, github upstream: gh can still answer", "remote.origin.url https://gitlab.com/a/b.git\nremote.upstream.url https://github.com/a/b.git\n", nil, ForgeGitHub, true},
		{"only bitbucket", "remote.fork.url https://bitbucket.org/a/b.git\n", nil, ForgeOther, true},
		{"gitlab plus an unplaceable host: ask", "remote.origin.url https://gitlab.com/a/b.git\nremote.mirror.url https://git.example.com/a/b.git\n", nil, ForgeUnknown, true},
		{"no remotes (git: no match)", "", errNoMatch, ForgeOther, true},
		{"empty output is no remotes too", "", nil, ForgeOther, true},
		{"unknown host asks gh", "remote.origin.url https://git.example.com/a/b.git\n", nil, ForgeUnknown, true},
		{"a failed read is no answer", "", errors.New("context deadline exceeded"), ForgeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := runGit
			t.Cleanup(func() { runGit = orig })
			var calls []string
			runGit = func(_ context.Context, _ string, args ...string) (string, error) {
				calls = append(calls, strings.Join(args, " "))
				return tc.stdout, tc.err
			}
			got, ok := RemoteForge(context.Background(), "/repo")
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("RemoteForge = %v, %v; want %v, %v", got, ok, tc.want, tc.wantOK)
			}
			if len(calls) != 1 || calls[0] != argRemotes {
				t.Errorf("git argv = %v, want exactly one %q", calls, argRemotes)
			}
		})
	}
}

// fakeExit stands in for *exec.ExitError: classifyGitErr reads the exit code
// through the ExitCode method both share, so no real process is needed.
type fakeExit int

func (e fakeExit) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e fakeExit) ExitCode() int { return int(e) }

func exitErr(code int) error { return fakeExit(code) }

// git config exits 1 for "no key matched" and 2 or more for a real error;
// the production seam maps only the first to errNoMatch.
func TestClassifyGitErr(t *testing.T) {
	if err := classifyGitErr(nil); err != nil {
		t.Fatalf("nil → %v", err)
	}
	if err := classifyGitErr(errors.New("plain")); errors.Is(err, errNoMatch) {
		t.Fatal("a non-exit error read as no-match")
	}
	if err := classifyGitErr(exitErr(1)); !errors.Is(err, errNoMatch) {
		t.Fatalf("exit 1 → %v, want errNoMatch", err)
	}
	if err := classifyGitErr(exitErr(2)); errors.Is(err, errNoMatch) {
		t.Fatal("exit 2 read as no-match")
	}
}

// The statuses on the wire are a closed set the TUI matches by name; the
// spelling IS the contract.
func TestStatusNames(t *testing.T) {
	want := map[Status]string{
		StatusMerged: "merged", StatusClosed: "closed", StatusAttention: "attention",
		StatusRunning: "running", StatusApproved: "approved", StatusDraft: "draft", StatusOpen: "open",
	}
	for s, name := range want {
		if string(s) != name {
			t.Errorf("Status %q, want %q", s, name)
		}
	}
}
