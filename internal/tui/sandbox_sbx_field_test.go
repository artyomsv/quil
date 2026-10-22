package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sbx"
)

// A destination with sbx alone still offers the row, on the sbx backend.
func TestResetSandboxField_PicksTheBackendTheDestinationOffers(t *testing.T) {
	tests := []struct {
		name            string
		cap             ipc.SandboxCapRespPayload
		cfgBackend      string
		wantAvail       bool
		wantSbxSelected bool
	}{
		{"docker only", ipc.SandboxCapRespPayload{Available: true}, "", true, false},
		{"docker only, config asks sbx", ipc.SandboxCapRespPayload{Available: true}, "sbx", true, false},
		{"sbx only", ipc.SandboxCapRespPayload{SbxAvailable: true}, "", true, true},
		{"both, default docker", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true}, "", true, false},
		{"both, config sbx", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true}, "sbx", true, true},
		{"neither", ipc.SandboxCapRespPayload{}, "sbx", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Model
			m.cfg.Sandbox.Backend = tt.cfgBackend
			m.applySandboxCap("", tt.cap)
			m.resetSandboxField("")
			if m.sandboxDialogAvail != tt.wantAvail {
				t.Errorf("avail = %v, want %v", m.sandboxDialogAvail, tt.wantAvail)
			}
			if m.sandboxBackendIsSbx() != tt.wantSbxSelected {
				t.Errorf("sbx selected = %v, want %v", m.sandboxBackendIsSbx(), tt.wantSbxSelected)
			}
		})
	}
}

// An sbx pane sends the backend and nothing else: the daemon refuses an sbx
// spec that carries an image or a sign-in mode.
func TestSandboxSpec_SbxCarriesOnlyTheBackend(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxBackend = config.SandboxBackendSbx
	m.sandboxImage = "img:1"
	spec := m.sandboxSpec()
	if spec == nil || spec.Backend != "sbx" || spec.Image != "" || spec.Auth != "" || spec.ClaudeConfig != "" {
		t.Fatalf("spec = %+v", spec)
	}
	if err := m.sandboxSubmitError(); err != "" {
		t.Errorf("an sbx row with no image refused to submit: %q", err)
	}
	if m.showSandboxAuthField(promptsCWDPlugin()) {
		t.Error("the sign-in row is offered for an sbx pane")
	}
}

func TestSandboxRow_ArrowsSwitchOnlyWhenBothAreOffered(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxDialogDocker = true
	m.sandboxBackend = config.SandboxBackendDocker
	if m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
		t.Error("→ was taken with only docker offered")
	}
	m.sandboxDialogSbx = true
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) || !m.sandboxBackendIsSbx() {
		t.Error("→ did not switch to sbx")
	}
	// Typing is not taken on an sbx row: there is no field to type into.
	if m.handleSandboxFieldKey(tea.KeyPressMsg{Code: 'a', Text: "a"}) {
		t.Error("typing was taken by an sbx row")
	}
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyLeft}) || m.sandboxBackendIsSbx() {
		t.Error("← did not switch back to docker")
	}
	out := m.renderSetupSandboxField(true)
	if !strings.Contains(out, "←/→ sbx") {
		t.Errorf("the row does not name the other backend:\n%s", out)
	}
}

// With sbx selected the row names sbx, offers Docker as the other backend,
// points at the profile instead of an image field, and shows a refusal.
func TestSandboxRow_SbxRendersTheProfileNotAnImage(t *testing.T) {
	m := Model{pluginRegistry: plugin.NewRegistry()}
	m.sandboxOn = true
	m.sandboxDialogDocker = true
	m.sandboxDialogSbx = true
	m.sandboxBackend = config.SandboxBackendSbx
	m.sandboxErr = "no profile here"
	out := m.renderSetupSandboxField(true)
	for _, want := range []string{"Run in an sbx sandbox", "←/→ Docker", sbx.ProfileFile, "no profile here"} {
		if !strings.Contains(out, want) {
			t.Errorf("row does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "image") {
		t.Errorf("an sbx row shows an image field:\n%s", out)
	}
}

// attachModel is a dialog on a destination with docker, sbx and two existing
// sandboxes, one of which mounts the browsed folder.
func attachModel() Model {
	m := Model{pluginRegistry: plugin.NewRegistry()}
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true, SbxSandboxes: []ipc.SbxSandbox{
		{Name: "ticket-1", Status: "stopped", Folders: []string{"/w/ticket-1"}},
		{Name: "ticket-2", Status: "running", Folders: []string{"/w/ticket-2"}},
		{Name: "-evil", Folders: []string{"/w"}},
	}})
	m.resetSandboxField("")
	m.sandboxOn = true
	m.cwdBrowseDir = "/w/ticket-1/app"
	return m
}

// Sandboxes whose listed folders hold the folder come first; the others are
// still offered, after them, because `sbx ls` does not list a folder added
// with `sbx mount`. A name sbx would read as a flag is never offered.
func TestSandboxRow_AttachOffersTheFittingSandboxesFirst(t *testing.T) {
	m := attachModel()
	got := m.sandboxChoices()
	want := []sandboxChoice{{backend: "docker"}, {backend: "sbx"},
		{backend: "sbx", name: "ticket-1"}, {backend: "sbx", name: "ticket-2"}}
	if len(got) != len(want) {
		t.Fatalf("choices = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("choice %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	m.cwdBrowseDir = "/w/ticket-2/app"
	if c := m.sbxAttachChoices(); len(c) != 2 || c[0].Name != "ticket-2" {
		t.Errorf("choices for ticket-2's folder = %+v, want ticket-2 first", c)
	}
}

// → walks Docker, the profile, then the sandboxes; the spec carries the name.
func TestSandboxRow_ArrowsReachAnExistingSandbox(t *testing.T) {
	m := attachModel()
	m.sandboxBackend = config.SandboxBackendDocker
	for i := 0; i < 2; i++ {
		if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
			t.Fatal("→ was not taken")
		}
	}
	if !m.sandboxBackendIsSbx() || m.sandboxSbxName != "ticket-1" {
		t.Fatalf("backend=%q name=%q, want sbx ticket-1", m.sandboxBackend, m.sandboxSbxName)
	}
	spec := m.sandboxSpec()
	if spec == nil || spec.Backend != "sbx" || spec.Name != "ticket-1" || spec.Image != "" {
		t.Fatalf("spec = %+v", spec)
	}
	out := m.renderSetupSandboxField(true)
	for _, w := range []string{"attach: ticket-1 (stopped), not changed", "←/→ sbx ticket-2"} {
		if !strings.Contains(out, w) {
			t.Errorf("row does not contain %q:\n%s", w, out)
		}
	}
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyLeft}) || m.sandboxSbxName != "" || !m.sandboxBackendIsSbx() {
		t.Errorf("← did not go back to the profile: backend=%q name=%q", m.sandboxBackend, m.sandboxSbxName)
	}
}

// A sandbox whose listed folders do not hold the folder may still be
// submitted — the daemon asks the sandbox itself — and the row says so. One
// sbx no longer lists is refused, with the way out named.
func TestSandboxSubmitError_AttachChecks(t *testing.T) {
	m := attachModel()
	m.sandboxBackend, m.sandboxSbxName = config.SandboxBackendSbx, "ticket-1"
	m.cwdBrowseDir = "/w/ticket-2"
	if msg := m.sandboxSubmitError(); msg != "" {
		t.Errorf("an unlisted folder was refused: %q", msg)
	}
	if out := m.renderSetupSandboxField(false); !strings.Contains(out, "attach: ticket-1 (stopped), folder checked at start") {
		t.Errorf("the row does not say the folder is checked at start:\n%s", out)
	}
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true})
	if msg := m.sandboxSubmitError(); !strings.Contains(msg, "not listed by sbx") {
		t.Errorf("submit error = %q", msg)
	}
	// The cycle restarts from the first choice rather than losing its place.
	if next, ok := m.nextSandboxChoice(1); !ok || next.backend != "docker" {
		t.Errorf("next = %+v, %v", next, ok)
	}
}

// The dialog opens on the profile, never on a sandbox picked for an earlier
// pane.
func TestResetSandboxField_ForgetsTheAttachChoice(t *testing.T) {
	m := attachModel()
	m.sandboxSbxName = "ticket-1"
	m.resetSandboxField("")
	if m.sandboxSbxName != "" {
		t.Errorf("sandboxSbxName = %q after reset", m.sandboxSbxName)
	}
}

func TestFolderHolds(t *testing.T) {
	for _, tc := range []struct {
		folder, dir string
		want        bool
	}{
		{"/w/t", "/w/t", true},
		{"/w/t/", "/w/t/app", true},
		{"/w/t", "/w/t-2", false},
		{`C:\w\t`, `C:\w\t\app`, true},
		{"/", "/w", false},
		{"", "/w", false},
	} {
		if got := folderHolds(tc.folder, tc.dir); got != tc.want {
			t.Errorf("folderHolds(%q, %q) = %v, want %v", tc.folder, tc.dir, got, tc.want)
		}
	}
}

// The most specific listed folder decides, as the daemon's check does.
func TestSbxListsFolder_MostSpecificWins(t *testing.T) {
	s := ipc.SbxSandbox{Folders: []string{"/w/app", "/w/ref/notes"}, ReadOnlyFolders: []string{"/w/app/reference", "/w/ref"}}
	for dir, want := range map[string]bool{
		"/w/app":             true,
		"/w/app/src":         true,
		"/w/app/reference":   false, // read-only inside a writable folder
		"/w/app/reference/x": false,
		"/w/ref":             false,
		"/w/ref/notes/today": true, // writable inside a read-only folder
		"/w/other":           false,
		"":                   true, // no folder chosen yet
	} {
		if got := sbxListsFolder(s, dir); got != want {
			t.Errorf("sbxListsFolder(%q) = %v, want %v", dir, got, want)
		}
	}
	if sbxListsFolder(ipc.SbxSandbox{Folders: []string{"/w"}, ReadOnlyFolders: []string{"/w"}}, "/w") {
		t.Error("one path listed both ways counted as writable")
	}
}

// With sbx alone, a sandbox sbx stopped listing leaves one choice — and the
// arrows must still reach it, or the dialog is stuck.
func TestSandboxRow_SbxOnlyRecoversFromAStaleSandbox(t *testing.T) {
	m := attachModel()
	m.sandboxDialogDocker = false
	m.sandboxBackend, m.sandboxSbxName = config.SandboxBackendSbx, "ticket-1"
	m.applySandboxCap("", ipc.SandboxCapRespPayload{SbxAvailable: true})
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
		t.Fatal("→ was not taken on a stale sandbox choice")
	}
	if m.sandboxSbxName != "" || !m.sandboxBackendIsSbx() {
		t.Errorf("backend=%q name=%q, want the sbx profile", m.sandboxBackend, m.sandboxSbxName)
	}
	if msg := m.sandboxSubmitError(); msg != "" {
		t.Errorf("submit still refused: %q", msg)
	}
	// Back on the only choice, the arrows are not the row's to take.
	if m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
		t.Error("→ was taken with one choice")
	}
}

// Picking a sandbox whose listed folders do not hold the dialog's folder
// moves the folder to the sandbox's primary workspace, so the pane is not
// refused at spawn for a folder the sandbox never mounted.
func TestFollowSbxAttachFolder_MovesToThePrimaryWorkspace(t *testing.T) {
	m := attachModel()
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true, SbxSandboxes: []ipc.SbxSandbox{
		{Name: "ticket-1", Folders: []string{"/w/ticket-1", "/home/.m2"}, ReadOnlyFolders: []string{"/w/repo/.git"}},
		{Name: "ticket-2", Folders: []string{"/w/ticket-2"}},
	}})
	m.cwdBrowseDir = "/w"
	m.recentCandidates = []string{"/w"}
	m.selectedWorktree = "/w/other-tree"
	m.sandboxBackend, m.sandboxSbxName = config.SandboxBackendSbx, "ticket-1"
	if cmd := m.followSbxAttachFolder(); cmd == nil {
		t.Fatal("no browse was requested for the new folder")
	}
	if m.cwdBrowseDir != "/w/ticket-1" || m.selectedWorktree != "" || m.recentCandidates != nil {
		t.Errorf("dir=%q worktree=%q recent=%v, want /w/ticket-1 and both cleared",
			m.cwdBrowseDir, m.selectedWorktree, m.recentCandidates)
	}
	if !m.sbxAttachFits("ticket-1") {
		t.Error("the moved folder does not fit the sandbox")
	}
}

// A folder the sandbox already holds is left alone, and so is everything
// when no sandbox is picked.
func TestFollowSbxAttachFolder_KeepsAFittingFolder(t *testing.T) {
	m := attachModel() // folder /w/ticket-1/app
	m.sandboxBackend, m.sandboxSbxName = config.SandboxBackendSbx, "ticket-1"
	if cmd := m.followSbxAttachFolder(); cmd != nil || m.cwdBrowseDir != "/w/ticket-1/app" {
		t.Errorf("a fitting folder was moved to %q", m.cwdBrowseDir)
	}
	m.sandboxSbxName = ""
	m.cwdBrowseDir = "/elsewhere"
	if cmd := m.followSbxAttachFolder(); cmd != nil || m.cwdBrowseDir != "/elsewhere" {
		t.Errorf("the profile choice moved the folder to %q", m.cwdBrowseDir)
	}
}

// With no folder chosen yet the daemon would pick its own default, which the
// sandbox may not hold, so the folder is set too.
func TestFollowSbxAttachFolder_FillsAnEmptyFolder(t *testing.T) {
	m := attachModel()
	m.cwdBrowseDir = ""
	m.sandboxBackend, m.sandboxSbxName = config.SandboxBackendSbx, "ticket-2"
	if cmd := m.followSbxAttachFolder(); cmd == nil || m.cwdBrowseDir != "/w/ticket-2" {
		t.Errorf("dir = %q, want /w/ticket-2", m.cwdBrowseDir)
	}
}

// → through the real dialog key path visits every sandbox once. Moving the
// folder reorders the fits-first list, so without a pinned order the cycle
// stepped back to the sandbox just left and never reached the third.
func TestSandboxRow_ArrowsVisitEverySandboxWhileTheFolderFollows(t *testing.T) {
	m := sandboxKeyModel(t)
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true, SbxSandboxes: []ipc.SbxSandbox{
		{Name: "a", Folders: []string{"/w/a"}},
		{Name: "b", Folders: []string{"/w/b"}},
		{Name: "c", Folders: []string{"/w/c"}},
	}})
	m.resetSandboxField("")
	m.sandboxOn = true
	m.sandboxBackend = config.SandboxBackendDocker

	var got []string
	for i := 0; i < 5; i++ {
		upd, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyRight})
		m = upd.(Model)
		got = append(got, sandboxChoice{backend: m.sandboxBackend, name: m.sandboxSbxName}.label()+"@"+m.cwdBrowseDir)
	}
	want := []string{"sbx@/repo", "sbx a@/w/a", "sbx b@/w/b", "sbx c@/w/c", "Docker@/w/c"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("cycle = %v\nwant    %v", got, want)
	}
}

// The listing on screen belongs to the folder just left, so moving clears it:
// Enter on a stale row would join that name onto the new path.
func TestFollowSbxAttachFolder_ClearsTheOldListing(t *testing.T) {
	m := attachModel()
	m.cwdBrowseDir = "/w"
	m.cwdBrowseEntries = []string{"..", "ticket-1", "ticket-2"}
	m.cwdBrowseParent = "/"
	m.cwdBrowseCursor = 2
	m.sandboxBackend, m.sandboxSbxName = config.SandboxBackendSbx, "ticket-2"
	m.followSbxAttachFolder()
	if m.cwdBrowseDir != "/w/ticket-2" || m.cwdBrowseEntries != nil || m.cwdBrowseParent != "" || m.cwdBrowseCursor != 0 {
		t.Errorf("dir=%q entries=%v parent=%q cursor=%d", m.cwdBrowseDir, m.cwdBrowseEntries, m.cwdBrowseParent, m.cwdBrowseCursor)
	}
}

// Picking a sandbox owns the folder: a recent-folder check or a git pick-list
// scan still in flight from the dialog's opening must not put its own folder
// back when it finally answers — or, timed out, restart the browser — while
// the sandbox stays picked. Driven through the key handler, then the late
// answers in the order a slow remote host delivers them.
func TestSandboxRow_LateDiscoveryCannotUndoTheSandboxFolder(t *testing.T) {
	m := sandboxKeyModel(t)
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true, SbxAvailable: true, SbxSandboxes: []ipc.SbxSandbox{
		{Name: "a", Folders: []string{"/w/a"}},
	}})
	m.resetSandboxField("")
	m.sandboxOn = true
	m.sandboxBackend = config.SandboxBackendDocker
	m.recentScan = recentScanState{gen: "recent-1", asked: []string{"/old"}}
	m.repoScan = repoScanState{cwd: "/repo", purpose: repoScanPickList, gen: "repo-1"}

	for i := 0; i < 2; i++ { // Docker → sbx profile → sandbox a
		upd, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyRight})
		m = upd.(Model)
	}
	if m.sandboxSbxName != "a" || m.cwdBrowseDir != "/w/a" {
		t.Fatalf("name=%q dir=%q, want sandbox a at /w/a", m.sandboxSbxName, m.cwdBrowseDir)
	}
	m.applyBrowseResponse(ipc.BrowseDirRespPayload{Path: "/w/a", Resolved: "/w/a", Parent: "/w",
		Entries: []ipc.BrowseEntry{{Name: "src", IsDir: true}}}, m.browse.gen)

	m.applyExistingDirs(ipc.DirsExistRespPayload{Paths: []string{"/old"}}, "recent-1")
	m.applyExistingDirsTimeout("recent-1")
	m.applyGitRepos(ipc.GitReposRespPayload{CWD: "/repo", Repos: []string{"/repo/x"}}, "repo-1")

	if m.cwdBrowseDir != "/w/a" || m.sandboxSbxName != "a" {
		t.Errorf("dir=%q name=%q after the late answers, want /w/a and a", m.cwdBrowseDir, m.sandboxSbxName)
	}
	if m.recentCandidates != nil || m.repoCandidates != nil {
		t.Errorf("a late answer refilled a pick list: recent=%v repo=%v", m.recentCandidates, m.repoCandidates)
	}
	if len(m.cwdBrowseEntries) == 0 || m.cwdBrowseEntries[len(m.cwdBrowseEntries)-1] != "src" {
		t.Errorf("the sandbox folder's listing was replaced: %v", m.cwdBrowseEntries)
	}
}
