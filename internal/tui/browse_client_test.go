package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

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
	m.browse = browseState{path: "/a", pending: true}

	cmd := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:    "/b",
		Entries: []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	})
	runCmd(cmd)

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
	m.browse = browseState{path: "/a", child: "current", pending: true}

	cmd := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:    "/a",
		Child:   "other",
		Entries: []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
	})
	runCmd(cmd)

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
	m.browse = browseState{path: "/a", child: "sub", pending: true}

	cmd := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:     "/a",
		Child:    "sub",
		Resolved: "/a/sub",
		Entries: []ipc.BrowseEntry{
			{Name: "z", IsDir: true},
			{Name: "a", IsDir: true},
			{Name: "file.txt", IsDir: false},
		},
	})
	runCmd(cmd)

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
	m.browse = browseState{path: "/a", pending: true}

	cmd := m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:  "/a",
		Error: "permission denied",
	})
	runCmd(cmd)

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
			m.browse = browseState{path: "/a", pending: true}

			runCmd(m.applyBrowseDir(ipc.BrowseDirRespPayload{
				Path:     "/a",
				Resolved: "/a",
				Parent:   tt.parent,
				Roots:    tt.roots,
				Entries:  []ipc.BrowseEntry{{Name: "sub", IsDir: true}},
			}))

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
	m.browse = browseState{path: "/a", pending: true, select_: "target"}

	runCmd(m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:     "/a",
		Resolved: "/a",
		Entries: []ipc.BrowseEntry{
			{Name: "aaa", IsDir: true},
			{Name: "target", IsDir: true},
			{Name: "zzz", IsDir: true},
		},
	}))

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

// A late tick from a superseded request must not cancel a live one, matching
// the Alt+G timeout precedent in discover_client_test.go.
func TestApplyBrowseTimeout_StaleTickIgnored(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/current", child: "c", pending: true}

	runCmd(m.applyBrowseTimeout("/old", "c"))

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

// The matching case: a genuinely unanswered request becomes diagnosable
// rather than leaving the browser spinning forever.
func TestApplyBrowseTimeout_MatchClearsAndSetsError(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, "/a")
	m.browse = browseState{path: "/a", child: "c", pending: true}

	runCmd(m.applyBrowseTimeout("/a", "c"))

	if m.browse.pending {
		t.Error("pending survived a matching timeout")
	}
	if m.browse.err == "" {
		t.Error("a timed-out request left no diagnosable error")
	}
}
