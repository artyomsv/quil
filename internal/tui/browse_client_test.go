package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/clipboard"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// browseReqPaths returns the Path of every MsgBrowseDirReq the model sent, in
// order. The chain tests assert on the whole sequence rather than the last
// entry: a chain that loops would still end on the right path.
func browseReqPaths(t *testing.T, fake *fakeSender) []string {
	t.Helper()
	var out []string
	for i, msg := range fake.sent {
		if msg.Type != ipc.MsgBrowseDirReq {
			continue
		}
		var p ipc.BrowseDirReqPayload
		decodeSentPayload(t, fake, i, &p)
		out = append(out, p.Path)
	}
	return out
}

// browseReqs returns every MsgBrowseDirReq payload the model sent, in order.
func browseReqs(t *testing.T, fake *fakeSender) []ipc.BrowseDirReqPayload {
	t.Helper()
	var out []ipc.BrowseDirReqPayload
	for i, msg := range fake.sent {
		if msg.Type != ipc.MsgBrowseDirReq {
			continue
		}
		var p ipc.BrowseDirReqPayload
		decodeSentPayload(t, fake, i, &p)
		out = append(out, p)
	}
	return out
}

// requestBrowseDir records the request and clears any error left over from a
// previous attempt — otherwise a retry after a failed scan would render with
// the old message still attached.
func TestRequestBrowseDir_SendsRequestAndRecordsState(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "/a")
	m.browse.err = "stale error from a previous attempt"

	cmd := m.requestBrowseDir("/a", "sub", "up")
	runCmd(cmd)

	if !m.browse.pending {
		t.Error("pending must be true immediately after the request is issued")
	}
	if m.browse.path != "/a" || m.browse.child != "sub" || m.browse.select_ != "up" {
		t.Errorf("browse state = %+v, want path=/a child=sub select_=up", m.browse)
	}
	if m.browse.err != "" {
		t.Errorf("browse.err = %q, want cleared on a new request", m.browse.err)
	}

	var found bool
	for i, msg := range fake.sent {
		if msg.Type == ipc.MsgBrowseDirReq {
			found = true
			var p ipc.BrowseDirReqPayload
			decodeSentPayload(t, fake, i, &p)
			if p.Path != "/a" || p.Child != "sub" {
				t.Errorf("sent payload = %+v, want Path=/a Child=sub", p)
			}
		}
	}
	if !found {
		t.Fatalf("requestBrowseDir sent no MsgBrowseDirReq (sent: %v)", debugSentTypes(fake))
	}
}

// A response for a directory the user has already left must be dropped. The
// browser fires a request per keystroke of navigation, so a superseded
// answer must not clobber the listing for wherever the user has since moved
// — the same reasoning as applyGitRepos in discover_client.go.
func TestApplyBrowseDir_StalePathIgnored(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/a", pending: true, gen: "g1"}

	got := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:    "/b",
		Entries: []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	}, "g1")

	if got != browseDropped {
		t.Errorf("outcome = %v, want browseDropped", got)
	}
	if !m.browse.pending {
		t.Error("a stale response by Path cleared the in-flight request")
	}
	if m.cwdBrowseDir != "" || m.cwdBrowseEntries != nil {
		t.Errorf("listing was touched by a stale response: dir=%q entries=%v", m.cwdBrowseDir, m.cwdBrowseEntries)
	}
}

// Two descents from one directory differ only in Child; matching on Path
// alone would let one answer land as though it belonged to the other.
func TestApplyBrowseDir_StaleChildIgnored(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/a", child: "current", pending: true, gen: "g1"}

	got := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:    "/a",
		Child:   "other",
		Entries: []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	}, "g1")

	if got != browseDropped {
		t.Errorf("outcome = %v, want browseDropped", got)
	}
	if !m.browse.pending {
		t.Error("a stale response by Child alone cleared the in-flight request")
	}
	if m.cwdBrowseDir != "" || m.cwdBrowseEntries != nil {
		t.Errorf("listing was touched by a stale response: dir=%q entries=%v", m.cwdBrowseDir, m.cwdBrowseEntries)
	}
}

// The matching case: the listing fills and pending clears.
func TestApplyBrowseDir_MatchFillsListingAndClearsPending(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/a", child: "sub", pending: true, gen: "g1"}

	got := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:     "/a",
		Child:    "sub",
		Resolved: "/a/sub",
		Entries: []ipc.BrowseEntry{
			{Name: "z", IsDir: true},
			{Name: "a", IsDir: true},
			{Name: "file.txt", IsDir: false},
		},
	}, "g1")

	if got != browseFilled {
		t.Errorf("outcome = %v, want browseFilled", got)
	}
	if m.browse.pending {
		t.Error("pending survived a matching response")
	}
	if m.cwdBrowseDir != "/a/sub" {
		t.Errorf("cwdBrowseDir = %q, want /a/sub", m.cwdBrowseDir)
	}
	want := []string{"a", "z"} // sorted, files excluded, no ".." (showUp false)
	if len(m.cwdBrowseEntries) != len(want) {
		t.Fatalf("cwdBrowseEntries = %v, want %v", m.cwdBrowseEntries, want)
	}
	for i, w := range want {
		if m.cwdBrowseEntries[i] != w {
			t.Errorf("cwdBrowseEntries[%d] = %q, want %q", i, m.cwdBrowseEntries[i], w)
		}
	}
}

// A failed scan must not render as an empty directory — the distinction this
// task exists to preserve. The listing on screen predates the failed request
// and must survive it untouched.
func TestApplyBrowseDir_ErrorLeavesListingUntouched(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.cwdBrowseDir = "/previous"
	m.cwdBrowseEntries = []string{"kept"}
	m.browse = browseState{path: "/a", pending: true, gen: "g1"}

	got := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:  "/a",
		Error: "permission denied",
	}, "g1")

	if got != browseFailed {
		t.Errorf("outcome = %v, want browseFailed", got)
	}
	if m.browse.pending {
		t.Error("pending survived an error response")
	}
	if m.browse.err != "permission denied" {
		t.Errorf("browse.err = %q, want %q", m.browse.err, "permission denied")
	}
	if m.cwdBrowseDir != "/previous" || len(m.cwdBrowseEntries) != 1 || m.cwdBrowseEntries[0] != "kept" {
		t.Errorf("listing was touched by an error response: dir=%q entries=%v", m.cwdBrowseDir, m.cwdBrowseEntries)
	}
}

// showUp is true when Parent != "" (not a filesystem root), true when Roots
// is non-empty (a Windows drive root offers the drive list as "up"), and
// false only when both are empty (a Unix root, nothing above it).
func TestApplyBrowseDir_ShowUp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		parent string
		roots  []string
		want   bool
	}{
		{"has parent", "/a", nil, true},
		{"has roots, no parent", "", []string{`C:\`, `D:\`}, true},
		{"unix root: neither", "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, _, _ := overlayTestModel(t, "/a")
			m.browse = browseState{path: "/a", pending: true, gen: "g1"}

			m.applyBrowseDir(ipc.BrowseDirRespPayload{
				Path:     "/a",
				Resolved: "/a",
				Parent:   tt.parent,
				Roots:    tt.roots,
				Entries:  []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
			}, "g1")

			hasUp := len(m.cwdBrowseEntries) > 0 && m.cwdBrowseEntries[0] == ".."
			if hasUp != tt.want {
				t.Errorf("showUp (via %q presence) = %v, want %v (entries: %v)", "..", hasUp, tt.want, m.cwdBrowseEntries)
			}
		})
	}
}

// select_ positions the cursor on the named entry — used by "up" navigation
// to keep the user oriented on the directory they just exited.
func TestApplyBrowseDir_SelectNamePositionsCursor(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/a", pending: true, select_: "target", gen: "g1"}

	m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:     "/a",
		Resolved: "/a",
		Entries: []ipc.BrowseEntry{
			{Name: "aaa", IsDir: true},
			{Name: "target", IsDir: true},
			{Name: "zzz", IsDir: true},
		},
	}, "g1")

	want := -1
	for i, name := range m.cwdBrowseEntries {
		if name == "target" {
			want = i
		}
	}
	if want == -1 {
		t.Fatalf("cwdBrowseEntries %v does not contain %q", m.cwdBrowseEntries, "target")
	}
	if m.cwdBrowseCursor != want {
		t.Errorf("cwdBrowseCursor = %d, want %d (the row for %q)", m.cwdBrowseCursor, want, "target")
	}
}

// TestMain shortens browseTimeout so the suite does not sleep on a timer it
// never asserts on. That is only safe because the timeout path is driven
// DIRECTLY — every TestApplyBrowseTimeout_* calls applyBrowseTimeout with an
// explicit (path, child) rather than waiting for a tick — and because runCmd
// discards whatever message a Cmd returns instead of dispatching it, so a tick
// that fires during a test cannot reach Update. This pins both, so shrinking
// the timer can never quietly become the thing under test.
func TestBrowseTimeout_IsOverriddenForTestsWithoutDrivingTheTimeoutPath(t *testing.T) {
	t.Parallel()
	if browseTimeout >= 8*time.Second {
		t.Errorf("browseTimeout = %v; TestMain is expected to shorten it for the suite", browseTimeout)
	}

	m, _, _ := overlayTestModel(t, "/a")
	// A real request, whose tick is now due almost immediately.
	runCmd(m.requestBrowseDir("/a", "", ""))
	time.Sleep(50 * time.Millisecond) // well past the shortened timer

	// The tick fired into runCmd, which dropped its message. Nothing observed
	// it, so the request is still pending and carries no error.
	if !m.browse.pending || m.browse.err != "" {
		t.Errorf("a fired tick reached the model: pending=%v err=%q; runCmd must discard the message",
			m.browse.pending, m.browse.err)
	}
}

// A late tick from a superseded request must not cancel a live one, matching
// the Alt+G timeout precedent in discover_client_test.go.
func TestApplyBrowseTimeout_StaleTickIgnored(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/current", child: "c", pending: true, gen: "g1"}

	runCmd(m.applyBrowseTimeout("/old", "c", "g1"))

	if !m.browse.pending {
		t.Error("a stale timeout cancelled the live request")
	}
	if m.browse.path != "/current" || m.browse.child != "c" {
		t.Errorf("browse state clobbered by a stale timeout: %+v", m.browse)
	}
	if m.browse.err != "" {
		t.Errorf("browse.err = %q, want empty (a stale timeout must not report an error for the live request)", m.browse.err)
	}
}

// A request's own timeout tick must not fire AFTER its own successful
// response and overwrite the listing it just delivered. requestBrowseDir
// schedules the tick alongside the send, and applyBrowseDir leaves
// path/child in place on a match (it cannot clear them to the zero value —
// "" is itself a valid Path), so the key alone cannot tell "this tick
// belongs to the request that already finished" from "this tick belongs to
// a request still in flight". pending is what makes the distinction.
func TestApplyBrowseTimeout_AfterSuccessDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/a", child: "c", pending: true, gen: "g1"}

	m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:     "/a",
		Child:    "c",
		Resolved: "/a/c",
		Entries:  []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	}, "g1")
	if m.browse.pending {
		t.Fatal("setup: pending must be false after the successful match")
	}

	// The same request's own timer, scheduled back when the request was
	// issued, fires only now — after the response already landed. Same gen:
	// it genuinely IS the same request's own late tick, which is exactly what
	// this test pins pending (not gen) as the guard for.
	runCmd(m.applyBrowseTimeout("/a", "c", "g1"))

	if m.browse.err != "" {
		t.Errorf("browse.err = %q, want empty; a request's own late timeout must not overwrite its own successful result", m.browse.err)
	}
	if m.cwdBrowseDir != "/a/c" {
		t.Errorf("cwdBrowseDir = %q, want /a/c; the successful listing must survive the late timeout", m.cwdBrowseDir)
	}
}

// The matching case: a genuinely unanswered request becomes diagnosable
// rather than leaving the browser spinning forever.
func TestApplyBrowseTimeout_MatchClearsAndSetsError(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/a", child: "c", pending: true, gen: "g1"}

	runCmd(m.applyBrowseTimeout("/a", "c", "g1"))

	if m.browse.pending {
		t.Error("pending survived a matching timeout")
	}
	if m.browse.err == "" {
		t.Error("a timed-out request left no diagnosable error")
	}
}

// TestApplyBrowseTimeout_IdenticalRerequestUnaffectedByStaleTick pins the
// browse equivalent of the review-round-1 crossing finding — Task A's
// deferred minor, fixed alongside repoScan in the same round: (path, child)
// identifies WHAT was asked, but a repeated pair is reachable in earnest (the
// pre-fill chain retrying a candidate, or the user re-descending into the
// same directory), and content alone cannot tell request N's tick apart from
// a DIFFERENT request N+1 that happens to share the same key.
//
// Reproduced exactly as it is reachable: request N is issued for /x; before
// its answer (or its own timeout) resolves, request N+1 is issued for the
// IDENTICAL /x; N's timeout tick — scheduled 8s out when N was issued — fires
// while N+1 is still genuinely pending.
//
// Confirmed to fail against e0382ea (this task's prior commit, before gen was
// added): reproduced there with a standalone test using e0382ea's actual
// 2-argument applyBrowseTimeout(path, child), which incorrectly cleared
// N+1's pending flag and set "directory listing timed out" for a request that
// was still legitimately in flight.
func TestApplyBrowseTimeout_IdenticalRerequestUnaffectedByStaleTick(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")

	runCmd(m.requestBrowseDir("/x", "", ""))
	genN := m.browse.gen

	// Request N+1 for the IDENTICAL (path, child) — the one shape (path,
	// child) alone cannot tell apart from N.
	runCmd(m.requestBrowseDir("/x", "", ""))
	if m.browse.gen == genN {
		t.Fatalf("setup: requests N and N+1 must get distinct generations, got %q twice", genN)
	}

	// N's tick, scheduled back when N was issued, fires only now — after
	// N+1 is already in flight for the same directory.
	runCmd(m.applyBrowseTimeout("/x", "", genN))

	if !m.browse.pending {
		t.Error("N's stale tick incorrectly resolved N+1's still-pending request")
	}
	if m.browse.err != "" {
		t.Errorf("browse.err = %q, want empty — N's stale tick must not report a timeout for N+1", m.browse.err)
	}
}

// Q12 polish fix: going up highlights the directory just exited. A name that
// is not in the listing leaves the cursor at the top rather than somewhere
// arbitrary — reachable in earnest now that the leaf is derived from the
// daemon's own strings, which yield "" for a pair that does not line up.
func TestApplyBrowseListing_SelectName(t *testing.T) {
	t.Parallel()
	entries := []ipc.BrowseEntry{
		{Name: "alpha", IsDir: true},
		{Name: "beta", IsDir: true},
		{Name: "gamma", IsDir: true},
	}
	tests := []struct {
		selectName string
		want       int
	}{
		{"beta", 2}, // listing is ["..", "alpha", "beta", "gamma"]
		{"no-such-dir", 0},
		{"", 0},
	}
	for _, tt := range tests {
		m := &Model{}
		m.applyBrowseListing("/root", entries, true, tt.selectName)
		if m.cwdBrowseCursor != tt.want {
			t.Errorf("selectName %q: cursor = %d, want %d (entries: %v)",
				tt.selectName, m.cwdBrowseCursor, tt.want, m.cwdBrowseEntries)
		}
	}
}

// ---------------------------------------------------------------------------
// Pre-fill candidate chain
// ---------------------------------------------------------------------------

// failCurrent answers whatever request is in flight with an error, which is
// what advances the pre-fill chain. Goes through applyBrowseResponse — the
// same entry point Update uses — so the chain is exercised as it is wired, not
// as the test imagines it.
func failCurrent(t *testing.T, m *Model) {
	t.Helper()
	if !m.browse.pending {
		t.Fatalf("no request in flight to fail (browse = %+v)", m.browse)
	}
	runCmd(m.applyBrowseResponse(ipc.BrowseDirRespPayload{
		Path:  m.browse.path,
		Child: m.browse.child,
		Error: "no such directory",
	}, m.browse.gen))
}

// A candidate the daemon cannot list hands off to the next one. The chain was
// a synchronous loop over os.ReadDir; each link now costs a round trip, and
// the failing response is the only thing that can advance it.
func TestBrowseCandidateChain_AdvancesPastFailure(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "/pane/cwd")
	m.lastSelectedCWD = "/remembered"

	runCmd(m.initSetupBrowser())
	failCurrent(t, m)

	want := []string{"/remembered", "/pane/cwd"}
	if got := browseReqPaths(t, fake); !reflect.DeepEqual(got, want) {
		t.Errorf("browse requests = %v, want %v", got, want)
	}
}

// When every candidate fails the chain STOPS. A chain that restarted would
// hammer the daemon for the rest of the session, and against a remote host
// each attempt is a round trip. The browser is left empty with the last error
// visible — a failure reported as a failure, not as an empty directory.
func TestBrowseCandidateChain_StopsWhenAllFail(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "/pane/cwd")
	m.lastSelectedCWD = "/remembered"

	runCmd(m.initSetupBrowser())
	for i := 0; i < 3; i++ {
		failCurrent(t, m)
	}

	// lastSelectedCWD, the pane CWD, then "~" — and nothing after it.
	want := []string{"/remembered", "/pane/cwd", "~"}
	if got := browseReqPaths(t, fake); !reflect.DeepEqual(got, want) {
		t.Errorf("browse requests = %v, want %v (the chain must not loop)", got, want)
	}
	if m.browse.pending {
		t.Error("chain exhausted but a request is still marked in flight")
	}
	if m.browse.err == "" {
		t.Error("chain exhausted with no error to show; the browser would render as empty")
	}
	if len(m.cwdBrowseEntries) != 0 {
		t.Errorf("cwdBrowseEntries = %v, want empty after a fully failed chain", m.cwdBrowseEntries)
	}
}

// An empty candidate costs no round trip — the active pane may have no CWD at
// all, and asking about "" would list the daemon's default rather than
// skipping to the next candidate.
func TestBrowseCandidateChain_SkipsEmptyCandidates(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "") // no pane CWD, nothing remembered

	runCmd(m.initSetupBrowser())

	if got := browseReqPaths(t, fake); !reflect.DeepEqual(got, []string{"~"}) {
		t.Errorf("browse requests = %v, want [~]", got)
	}
}

// The remembered directory is very often the active pane's CWD. Asking twice
// costs a round trip to be told the same thing, and — because the browse
// client's staleness key is the (Path, Child) pair — two consecutive requests
// with an identical key are the one shape it cannot tell apart.
func TestBrowseCandidateChain_DropsAdjacentDuplicates(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "/same")
	m.lastSelectedCWD = "/same"

	runCmd(m.initSetupBrowser())
	failCurrent(t, m)

	want := []string{"/same", "~"} // not /same, /same, ~
	if got := browseReqPaths(t, fake); !reflect.DeepEqual(got, want) {
		t.Errorf("browse requests = %v, want %v", got, want)
	}
}

// The chain never has two of its own requests in flight at once: the next
// candidate is asked about from inside the handler for the previous one's
// answer, so a request is only ever issued after its predecessor has landed.
func TestBrowseCandidateChain_IsSerial(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "/pane/cwd")
	m.lastSelectedCWD = "/remembered"

	runCmd(m.initSetupBrowser())
	if got := len(browseReqPaths(t, fake)); got != 1 {
		t.Fatalf("%d requests issued before any answer, want 1", got)
	}
	for i := 0; i < 2; i++ {
		before := len(browseReqPaths(t, fake))
		failCurrent(t, m)
		if got := len(browseReqPaths(t, fake)); got != before+1 {
			t.Errorf("answer %d produced %d new requests, want exactly 1", i, got-before)
		}
	}
}

// A remembered directory that no longer lists is stale memory: it is dropped
// so the next pane creation does not start by failing on it again.
func TestBrowseCandidateChain_FailedLastSelectedIsForgotten(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/pane/cwd")
	m.lastSelectedCWD = "/remembered"

	runCmd(m.initSetupBrowser())
	failCurrent(t, m)

	if m.lastSelectedCWD != "" {
		t.Errorf("lastSelectedCWD = %q, want cleared after the remembered directory failed", m.lastSelectedCWD)
	}
}

// A candidate that succeeds ends the chain. Otherwise a later failure — the
// user descending into an unreadable directory — would bounce the browser to
// a fallback nobody asked for.
func TestBrowseCandidateChain_SuccessEndsChain(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "/pane/cwd")
	m.lastSelectedCWD = "/remembered"

	runCmd(m.initSetupBrowser())
	runCmd(m.applyBrowseResponse(ipc.BrowseDirRespPayload{
		Path:     "/remembered",
		Resolved: "/remembered",
		Parent:   "/",
		Entries:  []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	}, m.browse.gen))

	// Now a user-driven descend fails.
	runCmd(m.browseTo("/remembered", "sub", ""))
	failCurrent(t, m)

	want := []string{"/remembered", "/remembered"}
	if got := browseReqPaths(t, fake); !reflect.DeepEqual(got, want) {
		t.Errorf("browse requests = %v, want %v (a failed navigation must not resume the pre-fill chain)", got, want)
	}
}

// ---------------------------------------------------------------------------
// Navigation: descend, up, paste
// ---------------------------------------------------------------------------

// cwdPlugin is the minimal plugin the CWD field needs. handleSetupCWDKey only
// forwards it to submitSetupDialog, which the navigation keys never reach.
func cwdPlugin() *plugin.PanePlugin {
	return &plugin.PanePlugin{Name: "ai", Command: plugin.CommandConfig{PromptsCWD: true}}
}

// browserModel puts a Model in the directory browser with a listing already
// delivered, as if the daemon had just answered.
func browserModel(t *testing.T, dir, parent string, entries []string) (*Model, *fakeSender) {
	t.Helper()
	m, fake, _ := overlayTestModel(t, "")
	m.dialog = dialogCreatePaneSetup
	m.cwdBrowseDir = dir
	m.cwdBrowseParent = parent
	m.cwdBrowseEntries = entries
	return m, fake
}

// Descending sends the entry as Child and leaves the join to the daemon.
// filepath.Join here would build the path with THIS machine's separator, so a
// Windows TUI against a Linux daemon would ask for `/srv\work` and list
// nothing — the bug this whole conversion removes.
func TestSetupCWDKey_DescendSendsChildNotAJoin(t *testing.T) {
	t.Parallel()
	m, fake := browserModel(t, "/srv", "/", []string{"..", "work"})
	m.cwdBrowseCursor = 1 // "work"

	_, cmd := m.handleSetupCWDKey(cwdPlugin(), "enter")
	runCmd(cmd)

	reqs := browseReqs(t, fake)
	if len(reqs) != 1 {
		t.Fatalf("browse requests = %v, want exactly one", reqs)
	}
	if reqs[0].Path != "/srv" || reqs[0].Child != "work" {
		t.Errorf("request = %+v, want Path=/srv Child=work", reqs[0])
	}
}

// Going up asks for the Parent the daemon reported, not a locally computed
// one. Same reasoning as the descend above, and the reason cwdBrowseParent is
// kept at all.
func TestSetupCWDKey_UpSendsDaemonReportedParent(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"backspace", "left", "h"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			m, fake := browserModel(t, `C:\srv\work`, `C:\srv`, []string{"..", "sub"})

			_, cmd := m.handleSetupCWDKey(cwdPlugin(), key)
			runCmd(cmd)

			reqs := browseReqs(t, fake)
			if len(reqs) != 1 {
				t.Fatalf("browse requests = %v, want exactly one", reqs)
			}
			if reqs[0].Path != `C:\srv` || reqs[0].Child != "" {
				t.Errorf("request = %+v, want Path=%q Child=\"\"", reqs[0], `C:\srv`)
			}
		})
	}
}

// The ".." row is the same navigation as the "up" key.
func TestSetupCWDKey_DotDotSendsDaemonReportedParent(t *testing.T) {
	t.Parallel()
	m, fake := browserModel(t, "/srv/work", "/srv", []string{"..", "sub"})
	m.cwdBrowseCursor = 0 // ".."

	_, cmd := m.handleSetupCWDKey(cwdPlugin(), "enter")
	runCmd(cmd)

	reqs := browseReqs(t, fake)
	if len(reqs) != 1 {
		t.Fatalf("browse requests = %v, want exactly one", reqs)
	}
	if reqs[0].Path != "/srv" || reqs[0].Child != "" {
		t.Errorf("request = %+v, want Path=/srv Child=\"\"", reqs[0])
	}
}

// Going up highlights the directory just exited. The leaf is subtracted from
// the daemon's own two strings rather than taken with filepath.Base, which
// splits on the LOCAL separator — on a Unix client `C:\srv\work` would come
// back as one element and the cursor would land nowhere.
func TestBrowseLeaf_SeparatorAgnostic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dir, parent, want string
	}{
		{"/srv/work", "/srv", "work"},
		{"/srv", "/", "srv"},
		{`C:\srv\work`, `C:\srv`, "work"},
		{`C:\srv`, `C:\`, "srv"},
		{"/srv/work", "", ""},           // no parent reported (a root)
		{"/srv/work", "/elsewhere", ""}, // inconsistent pair degrades, never guesses
	}
	for _, tt := range tests {
		if got := browseLeaf(tt.dir, tt.parent); got != tt.want {
			t.Errorf("browseLeaf(%q, %q) = %q, want %q", tt.dir, tt.parent, got, tt.want)
		}
	}
}

func TestSetupCWDKey_UpCarriesTheExitedLeafAsSelectName(t *testing.T) {
	t.Parallel()
	m, _ := browserModel(t, "/srv/work", "/srv", []string{"..", "sub"})

	out, cmd := m.handleSetupCWDKey(cwdPlugin(), "left")
	runCmd(cmd)

	if got := out.(Model).browse.select_; got != "work" {
		t.Errorf("select_ = %q, want %q (the folder just exited)", got, "work")
	}
}

// At a filesystem root, "up" shows the roots the DAEMON reported. No request
// is sent — the list is already in hand — and no drive letters are
// enumerated locally, which described the wrong machine.
func TestSetupCWDKey_UpFromRootShowsDaemonRoots(t *testing.T) {
	t.Parallel()
	m, fake := browserModel(t, `C:\`, "", []string{"..", "Users"})
	m.cwdBrowseRoots = []string{`C:\`, `D:\`}

	out, cmd := m.handleSetupCWDKey(cwdPlugin(), "left")
	runCmd(cmd)
	got := out.(Model)

	if reqs := browseReqs(t, fake); len(reqs) != 0 {
		t.Errorf("browse requests = %v, want none — the roots were already reported", reqs)
	}
	if got.cwdBrowseDir != "" {
		t.Errorf("cwdBrowseDir = %q, want \"\" (the showing-roots sentinel)", got.cwdBrowseDir)
	}
	if !reflect.DeepEqual(got.cwdBrowseEntries, []string{`C:\`, `D:\`}) {
		t.Errorf("cwdBrowseEntries = %v, want the daemon's roots", got.cwdBrowseEntries)
	}
}

// A Unix root has nothing above it, so "up" is inert rather than navigating to
// a root list that does not exist.
func TestSetupCWDKey_UpFromUnixRootIsInert(t *testing.T) {
	t.Parallel()
	m, fake := browserModel(t, "/", "", []string{"home", "srv"})

	out, cmd := m.handleSetupCWDKey(cwdPlugin(), "left")
	runCmd(cmd)

	if reqs := browseReqs(t, fake); len(reqs) != 0 {
		t.Errorf("browse requests = %v, want none", reqs)
	}
	if got := out.(Model); got.cwdBrowseDir != "/" {
		t.Errorf("cwdBrowseDir = %q, want / (unchanged)", got.cwdBrowseDir)
	}
}

// A row in the roots list is already a full path, so it is asked about as Path.
// Sending it as Child would have the daemon join `C:\` onto its own parent.
func TestSetupCWDKey_DescendFromRootsListSendsPath(t *testing.T) {
	t.Parallel()
	m, fake := browserModel(t, "", "", []string{`C:\`, `D:\`})
	m.cwdBrowseCursor = 1

	_, cmd := m.handleSetupCWDKey(cwdPlugin(), "enter")
	runCmd(cmd)

	reqs := browseReqs(t, fake)
	if len(reqs) != 1 {
		t.Fatalf("browse requests = %v, want exactly one", reqs)
	}
	if reqs[0].Path != `D:\` || reqs[0].Child != "" {
		t.Errorf("request = %+v, want Path=%q Child=\"\"", reqs[0], `D:\`)
	}
}

// Pasting a path asks the daemon about it verbatim. The old path stat-ed it
// locally, so a perfectly valid REMOTE path was rejected as "does not exist"
// — this test runs on Windows too, where "/home/artyom/homelab" cannot be
// stat-ed and must still be sent.
func TestSetupCWDKey_PasteSendsPathWithoutStatting(t *testing.T) {
	const pasted = "/home/artyom/homelab"
	clipboardReadText = func() (string, error) { return `"` + pasted + `"  `, nil }
	t.Cleanup(func() { clipboardReadText = clipboard.Read })

	m, fake := browserModel(t, `C:\srv`, `C:\`, []string{"..", "work"})

	out, cmd := m.handleSetupCWDKey(cwdPlugin(), "ctrl+v")
	runCmd(cmd)

	reqs := browseReqs(t, fake)
	if len(reqs) != 1 {
		t.Fatalf("browse requests = %v, want exactly one", reqs)
	}
	// Quotes and surrounding whitespace stripped; nothing else touched — no ~
	// expansion, no Abs, no existence check.
	if reqs[0].Path != pasted || reqs[0].Child != "" {
		t.Errorf("request = %+v, want Path=%q Child=\"\"", reqs[0], pasted)
	}
	if got := out.(Model); got.cwdInputError != "" {
		t.Errorf("cwdInputError = %q, want empty — the daemon decides whether the path exists", got.cwdInputError)
	}
}

// Paste works with nothing in the listing. A pre-fill chain that failed on
// every candidate leaves the browser empty, and paste is then the only way to
// reach a directory — every other key has no row to act on.
func TestSetupCWDKey_PasteWorksFromAnEmptyListing(t *testing.T) {
	clipboardReadText = func() (string, error) { return "/srv/work", nil }
	t.Cleanup(func() { clipboardReadText = clipboard.Read })

	m, fake := browserModel(t, "", "", nil)
	m.browse = browseState{path: "~", err: "no such directory"}

	_, cmd := m.handleSetupCWDKey(cwdPlugin(), "ctrl+v")
	runCmd(cmd)

	if got := browseReqPaths(t, fake); len(got) != 1 || got[0] != "/srv/work" {
		t.Errorf("browse requests = %v, want [/srv/work]", got)
	}
}

// A tilde travels to the daemon unexpanded: "~" names a directory on the
// machine holding the filesystem.
func TestSetupCWDKey_PasteLeavesTildeForTheDaemon(t *testing.T) {
	clipboardReadText = func() (string, error) { return "~/projects", nil }
	t.Cleanup(func() { clipboardReadText = clipboard.Read })

	m, fake := browserModel(t, "/srv", "/", []string{"..", "work"})

	_, cmd := m.handleSetupCWDKey(cwdPlugin(), "ctrl+v")
	runCmd(cmd)

	reqs := browseReqs(t, fake)
	if len(reqs) != 1 || reqs[0].Path != "~/projects" {
		t.Fatalf("browse requests = %v, want [{Path:~/projects}]", reqs)
	}
}

// ---------------------------------------------------------------------------
// Render: the hint line is exactly one line in every state
// ---------------------------------------------------------------------------

// The listing window writes browserVisibleRows lines unconditionally and the
// hint below it writes exactly one, so the dialog's height must not move
// between loading, loaded, empty and failed. A taller error state would shift
// every field below it — the invariant
// TestRenderSetup_PickFocusChange_HeightStable pins for the pick list.
func TestRenderSetup_BrowserStates_HeightStable(t *testing.T) {
	base := func() Model {
		return Model{
			dialog:         dialogCreatePaneSetup,
			pluginRegistry: registryWithAICWD(t),
			selectedPlugin: "ai",
			width:          100,
		}
	}
	loaded := base()
	loaded.cwdBrowseDir = "/srv/work"
	loaded.cwdBrowseEntries = []string{"..", "cmd", "internal"}

	empty := base()
	empty.cwdBrowseDir = "/srv/empty"

	pending := base()
	pending.browse = browseState{path: "/srv", pending: true}

	failed := base()
	failed.cwdBrowseDir = "/srv/work"
	failed.cwdBrowseEntries = []string{"..", "cmd", "internal"}
	failed.browse = browseState{path: "/srv/work", err: "listing /srv/nope timed out after 10s — is it an unreachable network mount?"}

	truncated := base()
	truncated.cwdBrowseDir = "/srv/big"
	truncated.cwdBrowseEntries = []string{"..", "cmd", "internal"}
	truncated.cwdBrowseTruncated = true

	truncatedEmpty := base()
	truncatedEmpty.cwdBrowseDir = "/srv/allfiles"
	truncatedEmpty.cwdBrowseTruncated = true

	states := map[string]Model{
		"loaded": loaded, "empty": empty, "pending": pending, "failed": failed,
		"truncated": truncated, "truncated-empty": truncatedEmpty,
	}
	want := strings.Count(loaded.renderCreatePaneSetupDialog(), "\n")
	for name, m := range states {
		if got := strings.Count(m.renderCreatePaneSetupDialog(), "\n"); got != want {
			t.Errorf("%s state rendered %d rows, want %d (dialog height must not move)", name, got, want)
		}
	}
}

// A pending or failed browse must never read as "(empty directory)": that is a
// confidently wrong answer, and telling the two apart is the point of the
// whole conversion.
func TestRenderSetup_PendingAndErrorAreNotEmptyDirectory(t *testing.T) {
	base := func() Model {
		return Model{
			dialog:         dialogCreatePaneSetup,
			pluginRegistry: registryWithAICWD(t),
			selectedPlugin: "ai",
			width:          100,
		}
	}

	pending := base()
	pending.browse = browseState{path: "/srv", pending: true}
	out := stripANSI(pending.renderCreatePaneSetupDialog())
	if strings.Contains(out, "(empty directory)") {
		t.Errorf("a pending browse rendered as an empty directory\n%s", out)
	}
	if !strings.Contains(out, "(loading…)") {
		t.Errorf("a pending browse showed no loading state\n%s", out)
	}

	failed := base()
	failed.browse = browseState{path: "/srv", err: "permission denied"}
	out = stripANSI(failed.renderCreatePaneSetupDialog())
	if strings.Contains(out, "(empty directory)") {
		t.Errorf("a failed browse rendered as an empty directory\n%s", out)
	}
	if !strings.Contains(out, "permission denied") {
		t.Errorf("a failed browse did not show its error\n%s", out)
	}

	// The genuine case still reaches it.
	empty := base()
	empty.cwdBrowseDir = "/srv/empty"
	out = stripANSI(empty.renderCreatePaneSetupDialog())
	if !strings.Contains(out, "(empty directory)") {
		t.Errorf("a genuinely empty listing lost its empty state\n%s", out)
	}
}

// A capped listing must not read as a complete one. Otherwise a user scrolling
// a large directory without finding a folder concludes it is absent — the same
// confidently-wrong answer as rendering a failure as an empty directory.
func TestRenderSetup_TruncatedListingSaysSo(t *testing.T) {
	base := func(truncated bool) Model {
		return Model{
			dialog:             dialogCreatePaneSetup,
			pluginRegistry:     registryWithAICWD(t),
			selectedPlugin:     "ai",
			width:              100,
			cwdBrowseDir:       "/srv/big",
			cwdBrowseEntries:   []string{"..", "a", "b"},
			cwdBrowseTruncated: truncated,
		}
	}
	complete := stripANSI(base(false).renderCreatePaneSetupDialog())
	capped := stripANSI(base(true).renderCreatePaneSetupDialog())

	if complete == capped {
		t.Fatalf("a capped listing rendered identically to a complete one\n%s", capped)
	}
	if !strings.Contains(capped, "capped") {
		t.Errorf("capped listing does not say so\n%s", capped)
	}
	if strings.Contains(complete, "capped") {
		t.Errorf("a complete listing claimed it was capped\n%s", complete)
	}
	// The navigation hint survives alongside the warning — the marker leads so
	// the width clamp eats the hints, not the news.
	if !strings.Contains(capped, "↑↓ move") {
		t.Errorf("capped listing lost its navigation hint\n%s", capped)
	}
}

// The cap marker LEADS the hint so the width clamp eats the navigation text —
// which the user has already read — instead of the one part of the line that is
// news. Checked at the narrow width that made the original hint wrap, since
// that is the only place the ordering can be observed.
func TestRenderSetup_TruncatedMarkerSurvivesNarrowBox(t *testing.T) {
	entries := []string{".."}
	for i := 0; i < 15; i++ {
		entries = append(entries, fmt.Sprintf("dir%02d", i))
	}
	m := Model{
		dialog:             dialogCreatePaneSetup,
		pluginRegistry:     registryWithAICWD(t),
		selectedPlugin:     "ai",
		cwdBrowseDir:       `E:\Projects\Stukans\monorepo`,
		cwdBrowseEntries:   entries,
		cwdBrowseTruncated: true,
		width:              64,
	}
	out := m.renderCreatePaneSetupDialog()
	textArea := m.setupTextWidth()
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > textArea {
			t.Errorf("line %q width %d exceeds text area %d (would wrap)", line, w, textArea)
		}
	}
	if !strings.Contains(stripANSI(out), "capped") {
		t.Errorf("the cap warning was clamped away on a narrow box\n%s", stripANSI(out))
	}
}

// Capped with nothing to show: every entry that survived the cap was a file.
// Silence here would imply the directory holds no folders.
func TestRenderSetup_TruncatedWithNoEntriesIsNotEmptyDirectory(t *testing.T) {
	m := Model{
		dialog:             dialogCreatePaneSetup,
		pluginRegistry:     registryWithAICWD(t),
		selectedPlugin:     "ai",
		width:              100,
		cwdBrowseDir:       "/srv/allfiles",
		cwdBrowseTruncated: true,
	}
	out := stripANSI(m.renderCreatePaneSetupDialog())
	if strings.Contains(out, "(empty directory)") {
		t.Errorf("a capped listing rendered as an empty directory\n%s", out)
	}
	if !strings.Contains(out, "capped") {
		t.Errorf("a capped listing does not say so\n%s", out)
	}
}

// Truncated is recorded from the response, and cleared by the next answer that
// is complete — a stale warning about a directory no longer on screen is its
// own wrong answer.
func TestApplyBrowseDir_RecordsAndClearsTruncated(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")

	m.browse = browseState{path: "/big", pending: true, gen: "g1"}
	m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path: "/big", Resolved: "/big", Truncated: true,
		Entries: []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	}, "g1")
	if !m.cwdBrowseTruncated {
		t.Error("Truncated was not recorded from the response")
	}

	m.browse = browseState{path: "/small", pending: true, gen: "g2"}
	m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path: "/small", Resolved: "/small",
		Entries: []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	}, "g2")
	if m.cwdBrowseTruncated {
		t.Error("a complete listing left the previous listing's cap warning up")
	}
}

// An error is visible even with a listing still on screen — which is the
// common case, since a failed descend deliberately leaves the previous
// listing in place. Ordering the error behind the scroll hint would render an
// ordinary hint and make the keypress look ignored.
func TestRenderSetup_ErrorVisibleOverAnExistingListing(t *testing.T) {
	m := Model{
		dialog:           dialogCreatePaneSetup,
		pluginRegistry:   registryWithAICWD(t),
		selectedPlugin:   "ai",
		width:            100,
		cwdBrowseDir:     "/srv/work",
		cwdBrowseEntries: []string{"..", "cmd", "internal"},
		browse:           browseState{path: "/srv/work", child: "secret", err: "permission denied"},
	}
	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, "permission denied") {
		t.Errorf("the failed descend's error is hidden behind the navigation hint\n%s", out)
	}
}
