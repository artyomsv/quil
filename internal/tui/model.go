package tui

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/changelog"
	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/clipboard"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/kubediscover"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/memreport"
	"github.com/artyomsv/quil/internal/notify"
	"github.com/artyomsv/quil/internal/plugin"
)

// chromeHeight is the vertical space consumed by tab bar (1) + status bar (1).
const chromeHeight = 2

// Minimum terminal dimensions for rendering.
const (
	minTermWidth  = 40
	minTermHeight = 10
)

// Messages from daemon
type PaneOutputMsg struct {
	PaneID string
	Data   []byte
	Ghost  bool
}

type WorkspaceStateMsg struct {
	ActiveTab string
	Tabs      []TabInfo
	Panes     []PaneInfo
	// Projects is the sending daemon's project grouping, in its own order;
	// ActiveProject is the project that daemon considers current. A broadcast
	// is the FULL state of ONE daemon, so both describe that daemon only.
	Projects      []ProjectInfo
	ActiveProject string
	// Dest is the destination this broadcast arrived on — client-side only,
	// empty for the local daemon. It has to ride the message because
	// listenForMessages returns the parsed state directly as the tea.Msg, so
	// Update, not the parse site, is where applyWorkspaceState is called.
	Dest string
	// Update is the daemon's announced newer release (nil when up to date).
	Update *ipc.UpdateInfo
}

// ProjectInfo is one daemon-side project as broadcast. TabIDs carries the
// project's own tab ORDER — the tab bar renders it verbatim — and ActiveTab is
// the tab that project was last left on (the daemon keeps it in sync with the
// global active tab for the active project, so it needs no special casing).
type ProjectInfo struct {
	ID        string
	Name      string
	RootDir   string
	TabIDs    []string
	ActiveTab string
	// Bootstrap is the daemon saying it invented this project rather than a
	// user naming it — see daemon.Project.Bootstrap. It is what makes naming a
	// project on a fresh host adopt the host's tabs instead of leaving a
	// "Default" beside them.
	Bootstrap bool
}

type TabInfo struct {
	ID   string
	Name string
	// ProjectID is the owning project as the TAB records it. Project.TabIDs is
	// what a rebuild iterates; this is the tab's own answer to the same
	// question, used to reject a stale TabIDs entry that would otherwise build
	// one TabModel into two projects at once.
	ProjectID string
	Color     string
	Panes     []string
	Layout    json.RawMessage
}

type PaneInfo struct {
	ID    string
	TabID string
	CWD   string
	Name  string
	Type  string
	Muted bool
	Eager bool
	// PinnedAttention is the user's "don't let me forget" mark. Daemon-owned
	// like Muted, so it survives a TUI restart and reads the same on every
	// client attached to that daemon.
	PinnedAttention bool
	Overlay         bool
	Pending         bool // deferred restore — not yet lazy-spawned
	SessionID       string
	HistoryLines    int
	// MouseTracking/MouseSGR are daemon-authoritative (scanned from the PTY
	// stream): the child app has enabled mouse tracking, so wheel events
	// should be forwarded to it. Mirrored onto PaneModel for the wheel handler.
	MouseTracking bool
	MouseSGR      bool
	// BracketedPaste is daemon-authoritative (scanned from the PTY stream):
	// the child app has enabled bracketed paste (?2004), so pasted text should
	// be wrapped in \x1b[200~/\x1b[201~ markers. Mirrored onto PaneModel for
	// the paste paths.
	BracketedPaste bool
	// Git state is daemon-authoritative and broadcast-only: the daemon holds
	// the disk the repository lives on, which is the whole point when that
	// daemon is on another machine. GitUpstream distinguishes "in sync" from
	// "nothing to compare against" — without it, 0/0 would claim the first
	// when it means the second.
	// SpawnError explains why a pane has no process — today, a worktree-owned
	// pane whose directory is gone. Daemon-authoritative and runtime-only.
	SpawnError  string
	GitBranch   string
	GitDetached bool
	GitWorktree bool
	// GitWorktreeName names the linked worktree the pane's CWD is in. Derived
	// daemon-side: path separators belong to the machine holding the disk, so
	// a Windows daemon's path split by a Linux client's filepath.Base returns
	// the whole string.
	GitWorktreeName string
	GitUpstream     bool
	GitAhead        int
	GitBehind       int
	GitStale        bool
	// WorktreeOwned marks a pane this daemon created a linked worktree for.
	// GitWorktree beside it says only that the CWD is IN one, which a pane the
	// user made by hand also satisfies — the close dialog offers deletion on
	// this field alone, because the offer is a claim that the checkout is
	// Quil's to remove.
	WorktreeOwned bool
	// WorktreePath is the directory git created for this pane, as the daemon
	// recorded it at creation. The close dialog prices and names THIS, never
	// CWD: CWD is rewritten by OSC 7 on every `cd`, so pricing it would show
	// the user one worktree's dirty count while the daemon deleted another.
	// Empty for a pane restored from a snapshot older than the field, which is
	// what makes such a pane un-offerable on both sides.
	WorktreePath string
	// Model/ContextTokens are daemon-authoritative (extracted from hook event
	// data at turn boundaries): the model id and context-window token count of
	// the pane's last completed AI turn. Empty/zero for non-AI panes.
	Model         string
	ContextTokens int64
	// Cols/Rows are the last resize payload the daemon ACCEPTED for this pane
	// (daemon.Pane.Cols/Rows, written only by handleResizePane). They are the
	// same quantity resizeAllPanes computes, which is what lets the broadcast
	// stand in for a client-side "what did I last send" cache: a pane whose
	// reported size already equals the one we would send needs no frame.
	// Zero means the daemon has never applied a size — always send then.
	Cols uint16
	Rows uint16
}

// paneSettleRepaintMsg fires shortly after a pane's first live output and
// forces a full repaint. The child reflows its UI right after the daemon's
// spawn-time resize kick; when the host terminal disagrees with the renderer
// about glyph widths (Claude Code's logo on Windows fonts), that redraw
// leaves stale cells only a full repaint clears.
type paneSettleRepaintMsg struct{}

// sizePollMsg fires on a fixed interval and re-queries the terminal size.
// conhost coalesces/drops WINDOW_BUFFER_SIZE_EVENTs during rapid resize →
// maximize, so the final WindowSizeMsg can simply never arrive; the poll
// closes the gap. Unchanged sizes no-op in the WindowSizeMsg handler.
type sizePollMsg struct{}

// sizePollInterval balances recovery latency against poll cost (one
// terminal-size query per tick — a single syscall).
const sizePollInterval = 1 * time.Second

func sizePollTick() tea.Cmd {
	return tea.Tick(sizePollInterval, func(time.Time) tea.Msg { return sizePollMsg{} })
}

// sizePollProbe runs the conhost grid fixup (no-op off Windows / when the
// grid already fits) and then asks Bubble Tea to re-query the terminal
// size. One command instead of a batch so the fixup is guaranteed to run
// before the query.
func sizePollProbe() tea.Msg {
	fixupConsoleGrid()
	return tea.RequestWindowSize()
}

// resizeTickMsg fires after the debounce delay; seq tracks freshness.
type resizeTickMsg struct {
	seq int
}

// PluginErrorMsg is received when the daemon detects a plugin error pattern.
type PluginErrorMsg struct {
	PaneID  string
	Title   string
	Message string
}

// setActivePaneMsg is sent when MCP requests focus on a specific pane.
type setActivePaneMsg struct {
	PaneID string
}

// paneEventMsg delivers a notification event from the daemon.
type paneEventMsg ipc.PaneEventPayload

// pasteRefreshMsg triggers a re-render after paste so the cursor updates.
type pasteRefreshMsg struct{}

// clipboardPastedMsg carries text read from the system clipboard back to the
// Update goroutine. Reading the clipboard blocks, so it must happen in a
// tea.Cmd — but the resulting PTY write must NOT, or paste becomes a second
// producer racing the keystroke path into inputCh (and reads pane/dest state
// off its owning goroutine). The Cmd therefore does I/O only and returns this;
// Update does the enqueue. See enqueueInput.
//
// paneID is the pane that was active when the user ASKED to paste, captured on
// the Update goroutine at that moment. Resolving the target at completion time
// instead would deliver into whatever pane happened to be active by then —
// clipboard contents are exactly the payload you least want landing in the
// wrong pane, and the image path (DIB decode → PNG encode → disk write) leaves
// a wide enough window to switch panes in.
//
// KNOWN AND DELIBERATE: the paste enters the queue when the READ finishes, so a
// key typed during a slow read is queued ahead of it. Closing that would mean
// reserving the paste's slot at request time, which makes a slow clipboard read
// head-of-line block every keystroke behind it — trading a rare, self-inflicted
// interleave for a visible freeze of the whole input stream on the image path.
// The pane binding above is the half that actually matters, because delivering
// to the wrong pane is silent while a mis-ordered paste is not.
type clipboardPastedMsg struct {
	text   string
	paneID string
}

// sidebarTickMsg triggers a periodic sidebar re-render to update relative timestamps.
type sidebarTickMsg struct{}

// PaneRef stores a pane location for navigation history. ProjectID names the
// project the location was recorded under — a bare TabIndex is only
// meaningful relative to ITS OWN project's tab list, so restoring one
// without first resolving the project it belongs to can reinterpret it
// against whichever project happens to be active at pop time.
type PaneRef struct {
	ProjectID string
	TabIndex  int
	PaneID    string
}

// highlightPaneMsg triggers an orange border highlight on a pane for MCP interactions.
type highlightPaneMsg struct {
	PaneID string
}

// clearHighlightMsg clears the orange border highlight after the timer expires.
// Seq must match the pane's current sequence to avoid clearing a renewed highlight.
type clearHighlightMsg struct {
	PaneID string
	Seq    int
}

// spinnerTickMsg advances the resuming spinner animation for a pane.
type spinnerTickMsg struct {
	paneID string
	frame  int
}

// workSpinnerTickMsg advances the shared work-in-progress spinner animation.
type workSpinnerTickMsg struct{}

// dialogPasteMsg delivers clipboard content to the active dialog input field.
type dialogPasteMsg string

var tabColors = []string{
	"",    // default (no custom color)
	"1",   // red
	"2",   // green
	"3",   // yellow
	"4",   // blue
	"5",   // magenta
	"6",   // cyan
	"208", // orange
}

type dialogScreen int

const (
	dialogNone dialogScreen = iota
	dialogAbout
	dialogSettings
	dialogShortcuts
	dialogConfirm
	dialogCreatePane
	dialogCreatePaneSetup
	dialogPluginError
	dialogInstanceForm
	dialogPlugins
	dialogTOMLEditor
	dialogLogViewer
	dialogDisclaimer
	dialogPluginMigration
	dialogMemory
	dialogGitRepoPick // Alt+G repo picker (Task 12 fills handler/render)
	dialogCommandHistory
	dialogUpdateNotice
	dialogCommandPalette
	dialogProjectNew    // Alt+Shift+N: create a project (Task 13)
	dialogProjectRename // sidebar context menu: rename a project (Task 13)
	dialogProjectPick   // Alt+P: fuzzy project picker (Task 14)
	dialogWhatsNew      // post-upgrade highlights; also F1 → What's New
)

// tuiClient is the subset of *ipc.Client the TUI uses on the Model. Defined
// at the consumer (here) so tests can inject a fake — e.g. for the Stop-
// daemon confirm — without depending on a real Unix socket. *ipc.Client
// satisfies this interface, so the assignment in NewModel needs no change.
type tuiClient interface {
	Send(*ipc.Message) error
	Receive() (*ipc.Message, error)
}

// Client is the exported spelling of tuiClient, so callers outside this package
// can write a RedialFunc: cmd/quil builds the reconnect dialer and cannot name
// an unexported type.
//
// An alias rather than a second interface declaration — the two are the same
// type, so no conversion exists at any boundary and the internal name stays
// the one used throughout this file.
type Client = tuiClient

type Model struct {
	// projects owns every tab. There is no flat tab list: activeProject
	// selects the project, and that project's own activeTab selects the tab
	// within it, so switching projects restores the tab each was left on.
	projects      []*ProjectModel
	activeProject int
	// prevProject is the ID of the project switchProject moved AWAY from —
	// the bounce target for the last-project toggle.
	//
	// An ID, not an index. m.projects is rebuilt on every broadcast and can
	// legitimately grow, shrink or (for a brand-new project) gain an entry at
	// the end, so an index survives as a NUMBER while silently coming to mean
	// a different project — and Alt+O would then move the user to another
	// daemon's work without touching a key that says so. An ID that no longer
	// resolves is a visible no-op, which is the honest failure.
	prevProject string
	width       int
	height      int
	client      tuiClient
	// inputCh is the ordered PTY-input queue feeding inputForwarder. Wire order
	// is fixed when bytes are pushed here (synchronously, on the Update
	// goroutine), NOT when they reach the socket — see forwardInputBytes.
	// inputDone stops the forwarder on TUI exit (StopInputForwarder), and
	// inputIdle is how the forwarder reports that it has finished draining, so
	// the exit path can wait for the queue to reach the socket before the
	// connection is closed out from under it.
	inputCh       chan paneInput
	inputDone     chan struct{}
	inputIdle     chan struct{}
	clientGen     int          // bumped on every client swap; see linkLostMsg for why
	closeClientFn func(Client) // releases a connection; see SetClientCloser
	cfg           config.Config
	// keymap resolves key presses to registry actions; keyConflicts is
	// surfaced in F1 -> Shortcuts.
	keymap       *keymap.Keymap
	keyConflicts []keymap.Conflict
	// pendingSeq holds the chords typed so far in a multi-step binding; empty
	// means the machine is idle. pendingGen is bumped on every state change so
	// a cancelled sequence's in-flight timeout tick cannot clear a sequence
	// started after it. seqFlash reports a dropped sequence in the status bar
	// and is cleared by the next keypress rather than by a timer.
	pendingSeq  []keymap.Chord
	pendingGen  int
	pendingPane string
	seqFlash    string
	// seqTimeout drops a pending sequence after this long. Zero = off, which
	// is the shipped default and matches tmux.
	seqTimeout time.Duration
	version    string
	sized      bool            // the terminal has reported its geometry at least once
	attached   map[string]bool // destinations already attached — see attachAllDests
	// offlineWoken records which offline destinations have had their ladder
	// started, so the wake-up fires once rather than on every resize.
	offlineWoken map[string]bool
	// listenCountFn is a test seam: production leaves it nil. It exists because
	// the failure it guards — a SECOND reader of the router's channel — has no
	// error to assert on, only reordering.
	listenCountFn func()
	// sizedOnce records panes this connection has sent at least one
	// MsgResizePane for. A broadcast-driven resize is suppressed when the
	// reported size already matches, but the FIRST one is always sent: the
	// daemon's duplicate guard is appliedCols/appliedRows, which it zeroes on
	// every PTY install, and repaintAfterResize's redraw kick for a restored
	// pane rides that first client resize. Cleared by armReattachReset, since
	// a reattach is exactly when the daemon's guard may have been zeroed, and
	// pruned by applyWorkspaceState when a pane stops existing.
	//
	// Keyed by sizedKey(dest, paneID), not by pane id alone: this decides
	// whether a pane's FIRST resize ships, so a shared key would let one
	// daemon's pane consume another's kick and let armReattachReset for one
	// dest clear the other's flag. Two daemons minting the same UUID is not a
	// realistic accident, but the invariant should not rest on that.
	sizedOnce          map[string]bool
	renaming           bool
	renameInput        string
	renamingPane       bool
	paneRenameInput    string
	pendingWidth       int
	pendingHeight      int
	resizeSeq          int
	pendingSplit       map[string]*LayoutNode // tabID → placeholder node awaiting pane from daemon
	pendingOverlayShow map[string]bool        // tabID → show overlay on its first arrival; set by the Alt+G overlay sender (wired in a follow-up commit); reads/deletes are nil-map-safe
	dialog             dialogScreen           // active dialog screen
	dialogCursor       int                    // highlighted item in dialog
	shortcutsCursor    int                    // scroll position in the Shortcuts list
	shortcutsScroll    int                    // window origin for the Shortcuts list
	logViewerReturn    dialogScreen           // dialog to return to when the read-only log/text viewer closes (default About)
	dialogEdit         bool                   // editing a settings value
	dialogInput        string                 // text input buffer for editing
	confirmKind        string                 // "pane" or "tab"
	confirmID          string                 // ID of pane/tab to delete
	confirmName        string                 // display name for confirmation
	confirmDetail      string                 // extra remote-sourced line (upgrade/apply-update confirm); sanitized+bounded at render
	pendingApplyVer    string                 // version to apply when the in-flight MsgStageUpdateReq answers; set only by a press meaning APPLY (see applyStageUpdateResp)
	updateReqInFlight  bool                   // a MsgStageUpdateReq is unanswered; a second press must not queue a second one (see sendStageUpdateReq)
	applyConfirmReturn dialogScreen           // where Esc on the apply confirm goes: About when the press came from there, else back to the panes
	// confirmWorktrees holds the worktrees this close would delete, empty for
	// every close that would delete none — which is what keeps an ordinary
	// close dialog byte-identical to what it has always been.
	//
	// confirmRemoveWorktree is the toggle and is ALWAYS false on open: the
	// destructive half must be something the user reached for. All three are
	// cleared by resetConfirmWorktrees on every route out of the dialog.
	confirmWorktrees      []confirmWorktree
	confirmRemoveWorktree bool
	confirmWorktreeGen    string           // in-flight status request; "" = none
	upgradeQueue          []upgradePrompt  // hosts waiting to be ASKED about provisioning; see enqueueUpgradePrompt
	devMode               bool             // true when QUIL_HOME is set
	pluginRegistry        *plugin.Registry // plugin registry (shared with daemon)
	lastWidth             int              // last known window width (for persistence)
	lastHeight            int              // last known window height (for persistence)
	createPaneStep        int              // 0=category, 1=plugin, 2=instance form, 3=split direction
	createPaneTarget      paneTarget       // where the pane goes: into this tab (Ctrl+N) or a new one (Ctrl+T)
	// createPaneDest pins the destination the dialog was OPENED against.
	//
	// Resolving it at submit instead would read whichever project is active by
	// then, and this dialog stays open for as long as the user takes — MCP
	// set_active_pane moves the active project with no keyboard involved. Every
	// value in the form describes the open-time daemon's disk (the browse and
	// git-discovery listings were answered by it), so submitting against a
	// different one hands, at worst, a WorktreeSpec.RepoRoot describing machine
	// A to machine B's `git worktree add`.
	createPaneDest string
	// newTabWorktrees tracks branches asked for by a NEW-TAB create, keyed by
	// BRANCH because such a create owns no tab id yet — the daemon mints it. It
	// is the staleness key that lets applyCreatePaneResp report a failed add on
	// that path without also reporting one belonging to another client.
	newTabWorktrees      map[string]bool
	selectedCategory     int           // selected category index in create pane dialog
	selectedPlugin       string        // selected plugin name in create pane dialog
	pluginErrorTitle     string        // title for plugin error dialog
	pluginErrorMessage   string        // message for plugin error dialog
	instanceStore        InstanceStore // saved plugin instances (loaded from instances.json)
	instanceFormValues   []string      // form field values (indexed by FormField position)
	instanceFormCursor   int           // active field in instance form
	selectedInstanceArgs []string      // args from selected instance (for IPC); toggles are appended here
	selectedInstanceName string        // name from selected instance (for IPC)
	// Setup-dialog state. selectedCWD is the value committed at submit time
	// (a snapshot of cwdBrowseDir) and is what handleCreatePaneSplit reads
	// for CreatePanePayload.CWD. The two fields exist separately so that the
	// browser can navigate freely without dirtying the "to be sent" value
	// until the user actually presses Continue.
	repoCandidates     []string               // git repos offered by the setup dialog (discover="git"); nil = plain browser
	repoPickCandidates []string               // candidates for dialogGitRepoPick (Alt+G / Alt+H, multiple repos)
	repoPickPlugin     string                 // overlay tool the picker will spawn on Enter (lazygit / hunk)
	kubeContexts       []kubediscover.Context // contexts offered by the setup dialog (discover="kube"); nil = none
	kubeCursor         int                    // row cursor in the kube field: 0 = Default context, 1.. = kubeContexts
	kubeScan           kubeScanState          // in-flight kube-context request (zero value = none); see kubeScanState.gen
	recentScan         recentScanState        // in-flight recent-directory existence check (zero value = none)
	kubeTruncated      bool                   // the daemon capped this listing — what is shown is not all there is
	lastSelectedCWD    string                 // remembers previous CWD selection across pane creations
	recentCWDs         []string               // last N committed CWDs (persisted; scoped per remote host — see config.RecentCWDsPath)
	recentCandidates   []string               // recent CWDs offered by the setup dialog; nil = not in recent-pick mode
	selectedCWD        string                 // CWD chosen in dialogCreatePaneSetup (empty = daemon default)
	cwdInputError      string                 // validation error shown under CWD input (empty = ok)
	toggleStates       []bool                 // checkbox states; one entry per plugin's Toggles slice, same indexing
	setupFieldCursor   int                    // focused field in setup dialog: 0 = CWD (if PromptsCWD), then toggles, then Continue
	cwdBrowseDir       string                 // current dir shown in the setup dialog's directory browser ("" = showing the root list)
	cwdBrowseEntries   []string               // browser listing: ".." (if not at root) + sorted subdirs
	cwdBrowseCursor    int                    // selected entry index in cwdBrowseEntries
	cwdBrowseScroll    int                    // scroll offset (top index) for the visible window of cwdBrowseEntries
	// The daemon's own answers for "what is above cwdBrowseDir". Kept rather
	// than recomputed: separators and the set of filesystem roots belong to the
	// machine holding the disk, so filepath.Dir here would answer for the wrong
	// one whenever the daemon is remote.
	cwdBrowseParent    string   // parent of cwdBrowseDir; "" when it is a filesystem root
	cwdBrowseRoots     []string // filesystem roots, reported only when at a root (Windows drives; empty on Unix)
	cwdBrowseTruncated bool     // the daemon capped this listing — what is shown is not all there is
	// Held separately from cwdBrowseTruncated because the two describe different
	// listings and only one of them is on screen at a time: this one applies once
	// showRootsList promotes the roots to BE the listing.
	cwdBrowseRootsTruncated bool     // the daemon gave up part-way through enumerating roots
	browseCandidates        []string // remaining pre-fill candidates for the setup browser's start-up chain
	// Session-picker state (plugins with [command] sessions = "claude"). Rows
	// are scoped to sessionScanCWD; when the browser moves to a different
	// directory the rows AND selectedSessionID are discarded, since a session
	// recorded under another project is not a meaningful resume target.
	// selectedSessionID is what handleCreatePaneSplit reads for
	// CreatePanePayload.ResumeSessionID; empty means "start a fresh session".
	sessionRows      []ipc.ClaudeSessionInfo // listing for sessionScanCWD, newest first
	sessionCursor    int                     // row cursor: 0 = "New session", 1.. = sessionRows
	sessionScroll    int                     // scroll offset for the visible window of the expanded list
	repoScan         repoScanState           // in-flight git discovery — Alt+G overlay or setup-dialog pick list (zero value = none)
	browse           browseState             // in-flight directory-browser request (zero value = none)
	worktrees        worktreeState           // create-pane dialog's worktree listing
	selectedWorktree string                  // chosen worktree PATH; "" = off (spawn in the CWD field's directory)
	// worktreeNewBranch is the branch a NEW worktree will be created on.
	// Non-empty and selectedWorktree empty means "create"; the two are
	// mutually exclusive, and both handlers clear the other.
	worktreeNewBranch string
	// worktreeNaming is true while the name is being typed. It swallows the
	// list's j/k, which are letters a branch name may contain.
	worktreeNaming bool
	// worktreeErr is the validation message shown beside the name field.
	worktreeErr string
	// worktreeCreates holds the tabs with a worktree create in flight, each
	// holding a layout placeholder only the response can retire.
	//
	// A MAP, not a scalar: the setup dialog closes on submit, so a second
	// Ctrl+N create can start while the first is still checking out — and the
	// daemon's single-flight rejects the second IMMEDIATELY, so its response
	// routinely arrives BEFORE the first's. A single slot is overwritten by
	// the second and then cleared, stranding the first tab's placeholder
	// permanently: every later pane created in that tab is swallowed by the
	// dead leaf, with no error anywhere.
	worktreeCreates map[string]string
	// worktreeReplaced holds, per tab, the pane a worktree-backed REPLACE
	// detached but has not destroyed yet.
	//
	// An ordinary replace disposes the old pane at send time: the daemon
	// destroys it the moment it handles the message, so there is nothing to go
	// back to. A worktree replace is ANSWERED, and the answer can be a failure
	// seconds later — the daemon creates the worktree BEFORE it touches the
	// pane, so on failure the old pane is still alive on both sides. Holding
	// the model here is what lets applyCreatePaneResp put it back rather than
	// costing the user a live pane over a branch name git refused.
	//
	// Cleared on every settling path — success disposes it (the swap really
	// happened), failure and timeout restore it — so an entry can never outlive
	// the request that armed it.
	worktreeReplaced  map[string]*PaneModel
	worktreeCursor    int                // row cursor in the worktree field's expanded list; row 0 = "off"
	worktreeScroll    int                // scroll offset for the visible window of the expanded worktree list
	reqGen            int                // monotonic instance id source for repoScan/browse/worktrees; see nextReqGen
	sessionScanCWD    string             // directory sessionRows belong to
	sessionState      sessionScanState   // request lifecycle for the session field
	sessionError      string             // daemon-reported error (sessionScanFailed)
	sessionTruncated  bool               // daemon capped the listing
	selectedSessionID string             // committed resume target (empty = fresh session)
	sessionDetail     sessionDetailPanel // the picker's "i" panel (zero value = closed)

	// Project New/Rename dialog state (Task 13). Shared by both dialogs —
	// m.dialog tells them apart, and Rename pre-fills projectFormID/Name from
	// the target project. The root-dir field reuses the SAME cwdBrowse* /
	// browse fields the pane-setup dialog's CWD field uses (they hold
	// whatever the currently open dialog put there — see projectdialog.go):
	// its committed value at submit time is simply m.cwdBrowseDir, exactly
	// like submitSetupDialog's selectedCWD capture.
	projectFormID     string // "" for New; the project ID being edited for Rename
	projectFormName   string // Name field's live text
	projectFormCursor int    // focused row: 0 = name, 1 = root dir, 2 = submit button
	projectFormErr    string // the one message line under the form (e.g. "name required")
	// projectFormMsgKind colours that line by what it MEANS — a failure, work
	// under way, a success, or a consequence awaiting confirmation. Written only
	// through setFormError/Busy/OK/Warn, whose doc comment says why assigning
	// the string alone is a bug.
	projectFormMsgKind projectFormMsgKind
	// projectFormMerge is the fold the NEXT Enter performs on a host that
	// already holds projects — nil when nothing is armed. It stores the plan it
	// described so the second Enter can verify the description still holds
	// rather than trusting that every edit path remembered to disarm it.
	projectFormMerge *projectMergePlan
	// projectFormHost is the Host field's live text — an ssh destination, or
	// empty for the local daemon. projectFormDialing holds the host a dial is
	// currently in flight for, so a result arriving for a host the user has
	// since retyped is discarded rather than applied to the wrong form.
	projectFormHost    string
	projectFormUser    string
	projectFormDialing string
	// projectFormInstalling holds the host a remote install is running for,
	// so its result is matched the same way a dial's is.
	projectFormInstalling string
	// installedDests records hosts this session has already provisioned, so a
	// dial that still reports the binary missing afterwards reports instead of
	// installing again.
	installedDests map[string]bool
	// projectFormRemote gates the ssh rows. A local project is the common
	// case, so User/Host are hidden until this is on — and turning it off
	// clears them, because a hidden field that still decided where the project
	// landed would be the worst of both.
	projectFormRemote bool
	// projectFormDest is the daemon the root-dir browser asks — the OWNING
	// project's dest for Rename (which may not be the active project; the
	// sidebar context menu can target a background one), the active dest for
	// New. Unlike the pane-setup dialog's CWD field, this can't rely on
	// requestBrowseDir's unstamped-resolves-to-active-dest fallback.
	projectFormDest string

	// projectPick is the fuzzy project picker's (Alt+P) query buffer, result
	// list, and cursor — same shape as paletteState (zero value = empty,
	// m.dialog is the sole open/closed authority). See projectpicker.go.
	projectPick projectPickState

	tomlEditor       *TextEditor // active TOML editor (nil when not editing)
	selection        *Selection  // active text selection (nil when none)
	mouseDown        bool        // true while left mouse button is held
	mouseStartX      int         // screen X of mouse press
	mouseStartY      int         // screen Y of mouse press
	configChanged    bool        // true when config needs saving on exit
	disclaimerTipIdx int         // random tip index for disclaimer dialog

	// whatsNew holds the release window the What's New dialog renders. Nil
	// when the dialog has never been opened this session.
	whatsNew         *changelog.Window
	whatsNewExpanded bool         // fixes list expanded rather than collapsed to a count
	whatsNewScroll   int          // first rendered row, when the body overflows
	whatsNewReturn   dialogScreen // dialog to restore on dismissal (About, or none)

	// termFocused tracks whether the terminal window has focus, from
	// tea.FocusMsg/BlurMsg (DEC 1004).
	//
	// Initialised TRUE. A terminal that does not implement focus reporting
	// sends neither message, so this keeps its initial value for the life of
	// the process — and the two possible defaults fail in opposite directions.
	// True means desktop toasts silently never fire there; false means they
	// fire constantly while the user is looking straight at the screen.
	// Quiet-and-diagnosable beats loud-and-wrong, so this starts true and
	// focusEverReported exists to explain the silence.
	termFocused bool

	// focusEverReported records that at least one focus event arrived, i.e.
	// that the terminal really does implement DEC 1004. Without it, "no toasts
	// ever appear" has no distinguishable cause.
	focusEverReported bool

	// notifier raises desktop toasts. Nil on every platform but Windows, and
	// nil on Windows until the user has run `quil notify setup` — so every
	// call site is a single nil check rather than a platform branch.
	//
	// notifyOpts is stored rather than re-derived from m.devMode: that field is
	// QUIL_HOME != "", which is also true for a PROD binary run against a
	// custom QUIL_HOME, and would then read the dev AUMID that `quil notify
	// setup` never wrote. The variant is a property of the BUILD, resolved by
	// cmd/quil and passed in.
	//
	// There is deliberately NO cached DesktopConfig here. raiseAttentionToast
	// reads m.cfg.Notification.Desktop directly — the same field the Settings
	// row's setter writes — so the toggle applies live by construction rather
	// than by remembering to sync two copies. Two copies of one value with
	// independent writers is the shape behind both the applyBrowseDir
	// staleness bug and the PinnedAttention local-write bug.
	notifier   desktopNotifier
	notifyOpts notify.Options
	notifyPID  int

	// outstandingToasts maps a pane id to the KIND of live toast it has.
	// The kind is load-bearing, not bookkeeping — see toastKind.
	outstandingToasts map[string]toastKind

	// lastFocusedPaneID is the previous value of focusedPaneID(), so Update can
	// notice the user moved away from a pane without walking the workspace.
	lastFocusedPaneID string

	mcpHighlights   map[string]bool     // pane IDs with active MCP highlight
	mcpHighlightSeq map[string]int      // sequence number for highlight timer reset
	notifications   *NotificationCenter // notification sidebar
	paneHistory     []PaneRef           // navigation history (bounded, 20 max)
	sidebarFocused  bool                // true when notification sidebar has keyboard focus
	// sidebarOpen/sidebarWidth control the PROJECT sidebar (internal/tui/sidebar.go)
	// — not to be confused with the notification sidebar above. Unlike that one
	// (a compositor overlay, zero layout width), the project sidebar is a real
	// reserved left column: its width is subtracted in the layout path
	// (paneAreaWidth/resizeTabs) so pane rects and PTY sizes always agree with
	// what gets painted. A screen property, not a session one — loaded from
	// UIConfig at startup (NewModel), never workspace.json, so a workspace saved
	// with it open can't fight a narrower terminal on restore.
	sidebarOpen  bool
	sidebarWidth int
	// sidebarScroll is the PANES section's scroll offset in rows. The PROJECTS
	// block is pinned and never scrolls. Written only by scrollSidebar /
	// scrollSidebarToPane; sidebarVisibleRows clamps a local copy, because it
	// runs on the render path and a render that writes model state is how a
	// paint and a hit test come to disagree.
	sidebarScroll     int
	notesMode         bool         // true when pane notes editor is open for the active pane
	notesEditor       *NotesEditor // active notes editor (nil when notesMode is false)
	notesPaneFocused  bool         // true when keyboard input goes to the bound pane (PTY) instead of the notes editor
	notesEnteredFocus bool         // true when toggleNotesMode was the one that turned the tab's focus mode on (so exit reverts)
	notesMouseDown    bool         // true while a left-button drag is in progress inside the notes editor
	notesAnchorRow    int          // document row where a notes-editor drag began (resolved once on click)
	notesAnchorCol    int          // document col where a notes-editor drag began (resolved once on click)
	viewerMouseDown   bool         // true while a left-button drag is in progress inside the read-only full-screen viewer
	viewerAnchorRow   int          // document row where a viewer drag began (resolved once on click)
	viewerAnchorCol   int          // document col where a viewer drag began (resolved once on click)

	// Scrollbar click-and-drag. Set on a left-click that hits a pane's
	// rightmost content column (the scrollbar track). While
	// scrollDragPaneID is non-empty, every MouseMotionMsg with the left
	// button held maps Y → scrollback position on that pane regardless of
	// where the cursor lands — matches GUI scrollbar UX. The rect is
	// captured once at click time so layout changes (e.g. window resize
	// mid-drag) don't drift the mapping; on release the state is cleared.
	scrollDragPaneID string
	scrollDragRect   PaneRect

	// Tab drag-and-drop. tabDragFromIdx == -1 means no drag in progress.
	// On left-click at Y=0 over a tab we record the index; subsequent
	// motion events at Y=0 swap the dragged tab into the hovered slot
	// (one slot at a time, slide semantics — other tabs shift, the
	// dragged tab moves through positions). Each swap fires an
	// MsgReorderTab IPC so the daemon's state stays authoritative and
	// the next workspace_state broadcast is a no-op.
	tabDragFromIdx int

	// Split-border drag-resize. splitDragNode is non-nil while a border
	// drag is in progress; splitDragRect captures the owning node's region
	// at click time so mid-drag layout changes can't drift the ratio math.
	// PTY resize + layout persistence are deferred to release
	// (finishSplitDrag) — mid-drag only the local tree and VT change.
	splitDragNode *LayoutNode
	splitDragRect BorderHit

	// Project-sidebar edge drag. sidebarDragging is set while a drag is in
	// flight; sidebarDragW is the PENDING width, painted as a preview rule and
	// committed to sidebarWidth only on release.
	//
	// The split is not cosmetic. View() calls tab.Resize on every frame, and
	// ResizeVT's contract pairs every emulator resize with a PTY redraw — so
	// moving the real width per motion event replays the 2026-07-15 corruption
	// bug, where unpaired intermediate-width rewraps permanently garble
	// content at the narrowest width crossed. Same deferral, same reason, as
	// finishSplitDrag.
	sidebarDragging bool
	sidebarDragW    int

	// ctxMenu is the pane context menu overlay (right-click / quick_actions).
	// Zero value = closed. Not a dialogScreen — see ctxmenu.go.
	ctxMenu ctxMenuState

	// palette is the command-palette state (dialogCommandPalette). Zero value
	// = empty; m.dialog is the sole open/closed authority. See palette.go.
	palette paletteState

	// Event-loop performance stats. Pointer so mutations persist across
	// Bubble Tea's value-receiver copies.
	perfStats *eventLoopStats

	// viewCache holds the last built frame so a message that provably changed
	// nothing visible can reuse it. Pointer for the same reason perfStats is
	// one: View has a value receiver, so it cannot write back to the Model
	// Bubble Tea will hand to the next Update.
	//
	// Nil is a supported state and simply disables reuse — the many tests that
	// build Model{} directly keep rendering every time.
	viewCache *viewCacheBox

	// skipRender marks the message just handled as inert: the frame cannot
	// have moved, so View may return the cached one.
	//
	// The default is FALSE — render — and Update resets it on every message.
	// Only branches audited as inert set it, so an unexamined branch (or a new
	// one) behaves exactly as it did before coalescing existed. Getting this
	// backwards would trade a performance win for stale pixels.
	skipRender bool

	// skipHidden narrows skipRender: set when this message was inert because it
	// carried output from a pane the user cannot see. Read only by View, which
	// is the sole place that knows a skip HAPPENED rather than was intended —
	// Update can set skipRender and still be rebuilt if the cache is invalid.
	skipHidden bool

	// Plugin migration dialog state
	migrationPlugins    []plugin.StalePlugin // stale plugins needing migration
	migrationIdx        int                  // active plugin tab index
	migrationLeft       *TextEditor          // user config (editable)
	migrationRight      *TextEditor          // new default (read-only)
	migrationRightFocus bool                 // true when right pane has keyboard focus
	migrationError      string               // validation error message

	// Memory dialog state
	mem         memoryDialogState
	lastMemResp *ipc.MemoryReportRespPayload

	// Input-history modal state (dialogCommandHistory)
	history historyState

	// Work-in-progress indicators. Derived TUI-side from the hook event
	// stream (see internal/tui/workstate.go). workSpinnerFrame is the shared
	// braille frame for the tab + pane spinners; workTickRunning guards
	// against starting multiple animation tick loops.
	workSpinnerFrame int
	workTickRunning  bool

	// sidebarTickRunning and notesTickRunning guard against stacking multiple
	// self-perpetuating tick chains (one immortal chain per unguarded schedule
	// call). Each chain clears its flag when it decides not to reschedule,
	// allowing a fresh chain to start on the next trigger.
	sidebarTickRunning bool
	notesTickRunning   bool

	// flashText is a transient status-bar message shown until flashUntil.
	// No dedicated timer is needed — the 1 s sizePollTick already repaints,
	// and the status-bar renderer checks flashUntil on every frame.
	flashText  string
	flashUntil time.Time

	// updateInfos maps a destination to the newer release ITS daemon
	// announced; drives the status-bar segment, the About row, and the
	// startup notice. Keyed rather than a single field because a client can
	// hold several daemons and each announces its own version — one field is
	// whatever broadcast last, so the status bar could describe a remote host
	// while a LOCAL project is on screen. Read through updateInfoFor /
	// activeUpdateInfo, never indexed directly at a call site.
	updateInfos map[string]*ipc.UpdateInfo

	// sawFirstState gates the once-per-launch update notice to the FIRST
	// WorkspaceStateMsg after attach — every broadcast thereafter (switch
	// tab, create pane, etc.) also carries the update key and would
	// otherwise reopen the dialog mid-session. The daemon re-announces its
	// update info to every newly-attached client, so the first broadcast is
	// exactly the "startup" moment the notice's spec calls for; the
	// status-bar segment already covers mid-session discovery.
	sawFirstState bool

	// applyUpdateOnExit signals cmd/quil/main.go to run the staged-update
	// swap after tea.Program returns (set by the apply confirm).
	applyUpdateOnExit bool

	// links holds ONE reconnectState per destination, keyed the way everything
	// else in this client is keyed: "" is the daemon a single connection routes
	// to, a host name is one of several. Absent means "never dropped" — linkOf
	// reads that as the zero value, so a session that never loses a link
	// allocates nothing. A map rather than a field is the whole point: one
	// daemon reconnecting leaves every other entry, and every other project's
	// input, untouched.
	//
	// redialFns is the matching dialer table. A destination with no dialer never
	// reconnects, which is what local sessions get and what keeps a dead local
	// daemon fatal — its panes died with it, so retrying would hide the loss.
	links     map[string]*reconnectState
	redialFns map[string]RedialFunc
	// cachedRemote is the last project set WRITTEN for each destination, so a
	// broadcast that changed nothing costs no disk write. In memory only.
	cachedRemote map[string][]CachedProject
	// saveRemoteProjectsFn is the cache writer, a seam so a test can count
	// writes without a temp dir per case. nil means SaveRemoteProjects.
	saveRemoteProjectsFn func(path string, list []CachedProject) error
	// dialDestFn connects a destination that is not in the table yet, and
	// redialDestFn builds the reconnect ladder for one once it is. Both are
	// supplied by cmd/quil (the ssh transport lives there); a Model without
	// them keeps working with the destinations it was constructed with, which
	// is every test Model.
	dialDestFn    DialFunc
	installDestFn InstallFunc
	redialDestFn  func(dest string) RedialFunc
}

// RemoteMode reports whether the daemon behind the ACTIVE project lives on
// another host.
//
// The active project's Dest is now the WHOLE answer. It used to be the union of
// that and a session-wide remoteDest field, because `quil --remote <host>`
// routed everything unstamped and stamped no project — so activeDest() read ""
// for a session that was entirely remote. That union had a known expiry, and
// this is it: once a client can hold a local daemon beside a remote one, a live
// session-wide flag answers "remote" for a LOCAL project the user is looking
// at, which is the wrong answer for every caller — the update controls it
// suppresses are wired to local disk, and the plugin availability it swaps out
// describes the wrong machine. --remote now keys its own connection by host, so
// its project carries a Dest like any other and nothing is lost.
func (m *Model) RemoteMode() bool { return m.remoteModeFor(m.activeDest()) }

// remoteModeFor reports whether dest names a daemon on another host — the
// per-destination counterpart to RemoteMode, for a call site that already knows
// which daemon it is asking about rather than defaulting to the active one.
//
// Trivial by construction, and kept as a named function anyway: "" means the
// local daemon EVERYWHERE in this client (a project's Dest, a tab's, the
// router's key, the Origin a local pump stamps), and this is where that
// convention is written down rather than re-derived at each call site.
func (m *Model) remoteModeFor(dest string) bool { return dest != "" }

// SetRecentCWDs replaces the remembered working-directory list.
//
// Exists because the list is scoped per remote destination while NewModel —
// which runs before the destination is known — can only load the local one.
// Kept as an explicit setter rather than a side effect inside SetRemoteDest:
// that setter is called from ~46 tests which build a Model directly and never
// set QUIL_HOME, and a disk read there would point every one of them at the
// developer's real ~/.quil.
func (m *Model) SetRecentCWDs(list []string) { m.recentCWDs = list }

// NewModel builds the client.
//
// The first parameter is the Client INTERFACE rather than *ipc.Client so a
// *Router can be passed — the router is what multiplexes several daemons behind
// the single client the Model consumes, and it must be constructed before the
// Model rather than installed afterwards: tea.NewProgram takes the Model BY
// VALUE, so anything that reads back through a closure over main's copy would be
// frozen at startup.
// whatsNew is the post-upgrade window, resolved by the caller via
// ResolveWhatsNew and passed in rather than computed here — that resolution
// WRITES the last-run marker, and no constructor may write to $QUIL_HOME: a
// test building a Model without setting it would clobber the developer's real
// ~/.quil, invisibly, because dev.sh test runs with a throwaway /root. (This
// function still READS there via LoadInstances and LoadRecentCWDs; the write is
// what had to move.) Nil when there is nothing to show.
func NewModel(client Client, cfg config.Config, version string, registry *plugin.Registry, stalePlugins []plugin.StalePlugin, whatsNew *changelog.Window) Model {
	m := Model{
		client:  client,
		cfg:     cfg,
		version: version,
		devMode: os.Getenv("QUIL_HOME") != "",
		// See the field comment: a terminal with no focus reporting never
		// corrects this, and assuming focused is the quiet failure.
		termFocused:      true,
		pluginRegistry:   registry,
		instanceStore:    LoadInstances(config.InstancesPath()),
		recentCWDs:       LoadRecentCWDs(config.RecentCWDsPath("")),
		mcpHighlights:    make(map[string]bool),
		viewCache:        &viewCacheBox{},
		mcpHighlightSeq:  make(map[string]int),
		notifications:    NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
		migrationPlugins: stalePlugins,
		perfStats:        newEventLoopStats(),
		tabDragFromIdx:   -1,
		sidebarOpen:      cfg.UI.SidebarOpen,
		sidebarWidth:     cfg.UI.SidebarWidth,
		inputCh:          make(chan paneInput, inputForwardBuffer),
		inputDone:        make(chan struct{}),
		inputIdle:        make(chan struct{}),
	}
	// Startup dialog priority: migration > what's-new > update-notice >
	// disclaimer. Migration blocks startup until every stale plugin is
	// resolved. What's-new outranks the disclaimer because it is specific to
	// this launch; the disclaimer reappears next time, as it always has. The
	// update notice is decided later, on the first broadcast, and its own guard
	// already yields to any dialog that is already open — returning BEFORE it
	// saves its marker, so the offer is not lost.
	if len(stalePlugins) == 0 {
		if whatsNew != nil {
			m.openWhatsNew(*whatsNew, dialogNone)
		} else if cfg.UI.ShowDisclaimer && len(disclaimerTips) > 0 {
			m.dialog = dialogDisclaimer
			m.disclaimerTipIdx = rand.Intn(len(disclaimerTips))
		}
	}
	m.initKeymap()
	return m
}

// initKeymap resolves the configured bindings into the dispatch keymap. Safe
// to call repeatedly; the last call wins.
func (m *Model) initKeymap() {
	m.keymap, m.keyConflicts = buildKeymap(m.cfg.Keybindings)
}

// WindowSize returns the last known window dimensions for persistence.
func (m Model) WindowSize() (width, height int) {
	return m.lastWidth, m.lastHeight
}

// Config returns the current config (may be modified by user actions).
func (m Model) Config() config.Config { return m.cfg }

// FlushNotes writes any pending notes edits to disk. Safe to call when notes
// mode is inactive (no-op).
//
// Precondition: must be invoked AFTER tea.Program.Run has returned, when the
// Update goroutine is no longer pumping events. Calling concurrently with the
// Update loop is unsafe — the editor is mutable shared state.
func (m Model) FlushNotes() {
	if m.notesEditor != nil {
		if err := m.notesEditor.Close(); err != nil {
			log.Printf("flush notes on exit: %v", err)
		}
	}
}

// ConfigChanged reports whether the config was modified and needs saving.
func (m Model) ConfigChanged() bool { return m.configChanged }

// ApplyUpdateRequested reports whether the user confirmed applying the
// staged update; main.go acts on it after the program exits.
func (m Model) ApplyUpdateRequested() bool { return m.applyUpdateOnExit }

func (m Model) Init() tea.Cmd {
	log.Print("TUI Init — starting listener")
	startUpdateWatchdog(defaultWatchdogConfig())
	go m.inputForwarder()
	return tea.Batch(m.listenForMessages(), memoryTickCmd(), sizePollTick())
}

// msgTypeName avoids per-Update reflection for the hot message types; the
// default arm keeps unknown types observable via fmt.Sprintf("%T", msg).
func msgTypeName(msg tea.Msg) string {
	switch msg.(type) {
	case PaneOutputMsg:
		return "tui.PaneOutputMsg"
	case paneEventMsg:
		return "tui.paneEventMsg"
	case tea.KeyPressMsg:
		return "tea.KeyPressMsg"
	case tea.MouseMotionMsg:
		return "tea.MouseMotionMsg"
	case tea.MouseClickMsg:
		return "tea.MouseClickMsg"
	case tea.MouseWheelMsg:
		return "tea.MouseWheelMsg"
	case tea.MouseReleaseMsg:
		return "tea.MouseReleaseMsg"
	case tea.WindowSizeMsg:
		return "tea.WindowSizeMsg"
	case sizePollMsg:
		return "tui.sizePollMsg"
	case resizeTickMsg:
		return "tui.resizeTickMsg"
	case workSpinnerTickMsg:
		return "tui.workSpinnerTickMsg"
	case spinnerTickMsg:
		return "tui.spinnerTickMsg"
	case sidebarTickMsg:
		return "tui.sidebarTickMsg"
	case notesTickMsg:
		return "tui.notesTickMsg"
	case memoryTickMsg:
		return "tui.memoryTickMsg"
	case listenContinueMsg:
		return "tui.listenContinueMsg"
	case WorkspaceStateMsg:
		return "tui.WorkspaceStateMsg"
	default:
		return fmt.Sprintf("%T", msg)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	start := time.Now()
	markUpdateStart(start)
	defer func() {
		markUpdateEnd()
		m.perfStats.recordMsg(msgTypeName(msg), time.Since(start))
	}()
	// Render coalescing starts from "this frame must be rebuilt" and is lowered
	// only by branches audited as inert (see Model.skipRender). Resetting here
	// means the flag describes THIS message and can never leak into the next.
	m.skipRender = false
	m.skipHidden = false
	// Acknowledge the focused pane of the active tab before processing the
	// message — focusing is the acknowledgement; see ackFocusedPane.
	//
	// This clears a flag the tab bar and sidebar draw, so it can move the
	// screen even when the message itself is inert.
	//
	// prologueChangedView is named for the REGION, not for this call: every
	// mutation between here and the type switch runs on every message and can
	// therefore move the screen under an otherwise-inert one. Anything added to
	// that region must fold into this flag. It was called ackChangedView first
	// and covered only the ack — which is precisely how the context-menu prune
	// below was missed, shipping a cached frame that still drew a closed menu.
	prologueChangedView := m.ackFocusedPane()
	// Beside ackFocusedPane deliberately: this is the one point every message
	// passes through, and the sweep's own emptiness check makes it free in the
	// common case. It must not be an edge hook — see sweepOutstandingToasts.
	m.sweepOutstandingToasts()
	// Moving away from a pane is a trigger, exactly like leaving the app: a
	// pane that parked while you were looking at it was correctly suppressed,
	// and switching tab or project is the moment it becomes something you
	// cannot see. Keyed on the focused pane CHANGING so this costs one string
	// compare per message rather than a workspace walk.
	if cur := m.focusedPaneID(); cur != m.lastFocusedPaneID {
		m.lastFocusedPaneID = cur
		m.raiseDeferredToasts()
	}
	// A context menu whose target vanished (daemon reconciliation, pane
	// destroy, MsgDestroyProject from another client) closes itself. Single
	// choke point — no need to audit every pruning path.
	//
	// The two menu kinds are checked against their OWN target: a project menu
	// has no paneID at all, so testing whether paneID resolves closed it on
	// the very next message — any spinner tick, PTY chunk or resize — which is
	// what the user saw as "the project menu flashes and vanishes". projectID
	// and paneID are mutually exclusive discriminators (see ctxMenuState), so
	// the else arm is exactly the original pane case. Both lookups are
	// nil-safe.
	// Folded into prologueChangedView: View both DRAWS this menu and derives
	// v.MouseMode from it, so closing it here moves the screen — on a message
	// that may otherwise be inert.
	if m.ctxMenu.open() {
		if projectID := m.ctxMenu.projectID; projectID != "" {
			if m.projectByID(projectID) == nil {
				m.closeCtxMenu()
				prologueChangedView = true
			}
		} else if pane, _, _ := m.findPaneAndTab(m.ctxMenu.paneID); pane == nil {
			m.closeCtxMenu()
			prologueChangedView = true
		}
	}
	// The resume key is checked BEFORE the freeze, or it would be swallowed with
	// every other keystroke. It cannot live inside freezeInput: that has a value
	// receiver and returns (tea.Cmd, bool), so it can neither clear the parked
	// state nor hand back a mutated Model. It IS scoped to the active
	// destination — it is a keypress, and keypresses go wherever the user is
	// typing.
	if link := m.linkOf(m.activeDest()); link.active && link.parked {
		if key, ok := msg.(tea.KeyPressMsg); ok && kbMatches(key.String(), reconnectResumeKey) {
			return m.resumeReconnect()
		}
	}
	// A dead link drops input rather than queueing it. Placed ahead of the type
	// switch so input decisions are made in one place instead of in a branch
	// nobody updated.
	//
	// freezeInput is called UNCONDITIONALLY and owns the whole decision,
	// including which destination each message is scoped to. It used to sit
	// behind an active-destination check here, which made it unreachable
	// whenever the active project was healthy — and that silently disabled the
	// per-destination gate for a delayed paste, whose target pane can belong to
	// a different daemon than the one on screen. A gate in two places is a gate
	// in neither.
	if cmd, frozen := m.freezeInput(msg); frozen {
		return m, cmd
	}
	switch msg := msg.(type) {
	case ActivatePaneMsg:
		// Validated a third time. The value has already passed
		// ParseActivateURI and the pipe listener, but this is the boundary that
		// actually moves the user's focus, and jumpToPane is reached from five
		// other callers that owe it nothing.
		if !notify.ValidPaneID(msg.PaneID) {
			return m, nil
		}
		_, cmd := m.jumpToPane(msg.PaneID)
		return m, cmd

	case tea.FocusMsg:
		// DEC 1004. Arriving at all is the proof that this terminal reports
		// focus — see the focusEverReported field comment.
		m.termFocused = true
		m.focusEverReported = true
		return m, nil

	case tea.BlurMsg:
		m.termFocused = false
		m.focusEverReported = true
		// Leaving is itself the trigger. An agent that parked or finished while
		// you were watching produced a rising edge that was correctly
		// suppressed — without this, walking away never surfaces it.
		m.raiseDeferredToasts()
		return m, nil

	case tea.WindowSizeMsg:
		// Poll echo: size matches both the applied and any pending value —
		// nothing to do. Keeps the 1s size poll free when idle.
		//
		// Gated on `sized`, not on the attach ledger. Those are two different
		// questions now — has the terminal reported a geometry, versus which
		// daemons have been attached — and only the first belongs here. A
		// destination left unattached by a failed send is one whose connection
		// is broken, so its pump reports the loss and finishReconnect attaches
		// it; making the 1 s size poll a second retry path would only race that.
		if m.sized && msg.Width == m.width && msg.Height == m.height &&
			msg.Width == m.pendingWidth && msg.Height == m.pendingHeight {
			// Inert: the branch reports a size we already hold and touches
			// nothing. The 1 s poll makes this one of the most frequent
			// messages the TUI sees.
			m.skipRender = !prologueChangedView
			return m, nil
		}
		log.Printf("WindowSizeMsg: %dx%d", msg.Width, msg.Height)
		m.pendingWidth = msg.Width
		m.pendingHeight = msg.Height
		m.lastWidth = msg.Width
		m.lastHeight = msg.Height
		m.resizeSeq++

		// A taller terminal shows more rows, which lowers the What's New
		// dialog's scroll ceiling. The renderer clamps its own copy, so
		// nothing draws wrong — but the STORED offset would sit above the new
		// limit and cost one dead key press before the view moved.
		if m.dialog == dialogWhatsNew {
			m.clampWhatsNewScroll()
		}

		// First resize: apply immediately for initial attach
		if !m.sized {
			m.sized = true
			m.width = msg.Width
			m.height = msg.Height
			m.resizeTabs()
			log.Print("first WindowSizeMsg — attaching to every daemon")
			// Sequenced, not `return m, tea.Batch(…, m.attachAllDests())`. Go
			// orders function CALLS within a statement left to right, but says
			// nothing about a plain operand like `m` against them — and
			// attachAllDests has a pointer receiver that lazily allocates the
			// attach ledger. Copy `m` into the return slot first and the ledger
			// is lost, so the next real resize attaches every destination a
			// SECOND time and replays every ghost buffer twice. gc happens to
			// evaluate the calls first today; nothing requires it to.
			//
			// wakeOfflineDests rides the same statement for the same reason, and
			// belongs on THIS branch specifically rather than the "subsequent
			// resize" code below: an unresized session hits the poll-echo guard
			// above on every later tick and never reaches that code at all, so
			// wiring the wake-up there would mean a host merely switched off at
			// launch never redials until the user actually resizes the window.
			// This branch runs exactly once, unconditionally, which is what a
			// launch-time wake-up needs.
			// The launch drain, on THIS branch for the same reason
			// wakeOfflineDests is here: it runs exactly once, unconditionally,
			// and an unresized session never reaches the subsequent-resize code
			// below — so a host stale at launch would never be offered.
			//
			// A STATEMENT, not an operand of the return: promptNextUpgrade has
			// a pointer receiver and mutates dialog state, and Go says nothing
			// about the order of a plain `m` against calls in the same
			// statement. The same hazard the ledger comment above documents.
			m.promptNextUpgrade()
			resize, attach, wake := m.resizeAllPanes(), m.attachAllDests(), m.wakeOfflineDests()
			return m, tea.Batch(resize, attach, wake)
		}

		// A destination can join the router after the first resize (a host that
		// was unreachable at launch, brought back by its reconnect ladder), and
		// a send that failed leaves its dest unattached on purpose. Retrying
		// here is what picks both up — the 1 s size poll makes it a bounded
		// wait rather than a wedge. Batched into the returns below rather than
		// returned on its own: a resize that ALSO needs an attach still has to
		// be debounced like any other.
		attach := m.attachAllDests()

		// Full-screen dialogs (migration, disclaimer) have no panes to
		// resize via IPC, so apply immediately — debouncing would leave
		// m.width stale during the delay, causing rendering artifacts
		// (e.g., on window maximize).
		if m.dialog == dialogPluginMigration || m.dialog == dialogDisclaimer {
			m.width = msg.Width
			m.height = msg.Height
			return m, attach
		}
		// The launch path's drain point. SeedOfflineDest runs before the
		// program starts, so the queue it fills has to be opened from inside
		// Update — and a WindowSizeMsg always arrives, which makes this the one
		// arm guaranteed to run on every startup. A no-op once the queue is
		// empty, and gated on dialogNone so it never displaces a dialog the
		// user is already answering.
		m.promptNextUpgrade()

		// Debounce subsequent resizes
		seq := m.resizeSeq
		return m, tea.Batch(attach, tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
			return resizeTickMsg{seq: seq}
		}))

	case offlineDestMsg:
		// No relisten, deliberately — see offlineDestMsg's doc comment.
		if !m.canReconnect(msg.dest) {
			log.Printf("offline destination %s has no dialer; leaving it parked", msg.dest)
			m.linkFor(msg.dest).parked = true
			return m, nil
		}
		p := m.projectForDest(msg.dest)
		if p == nil || p.Offline == nil || !p.Offline.Kind.laddered() {
			return m, nil
		}
		return m.beginReconnect(msg.dest, errors.New("unreachable at launch"))

	case linkLostMsg:
		// The generation check retires a report from a SUPERSEDED client, and it
		// applies to the single-connection path ONLY. There finishReconnect
		// swaps m.client wholesale, so a loop still parked in the dead client's
		// Receive will report a death that stopped mattering. A router is never
		// swapped: its r.in is one channel for the life of the session and the
		// drop arrives as data on it, so there is nothing for a session
		// generation to identify — and checking one here was a live bug, since
		// any OTHER destination's reconnect bumped clientGen and this drop was
		// then discarded with no ladder ever started.
		if !m.isRouter() && msg.gen != m.clientGen {
			log.Printf("ignoring link loss from gen %d (current %d)", msg.gen, m.clientGen)
			return m, nil
		}
		// The listen loop stopped when it returned this message, and with a
		// router that loop is the ONLY reader of every other daemon's messages —
		// leaving it unarmed parks a healthy daemon's output behind a dead one's
		// reconnect ladder, for as long as the ladder climbs. Re-armed only for
		// a router: on the single-connection path the client itself is dead, so
		// a fresh Receive fails instantly and re-arming is a hot loop.
		// finishReconnect owns the re-arm there instead.
		var relisten tea.Cmd
		if m.isRouter() {
			relisten = m.listenForMessages()
		}
		if !m.canReconnect(msg.dest) {
			// Quitting is right for a session whose ONLY daemon just died: its
			// panes died with it and there is nothing left to show. It is wrong
			// for one of several — a local daemon crashing would take down the
			// view of remote work that is still running perfectly well. The
			// surviving daemons keep the session; the dead one gets an honest
			// banner instead of a countdown that will never fire.
			if m.lastDaemon(msg.dest) {
				return m, tea.Quit
			}
			log.Printf("link to %s lost with no way to reconnect it; other daemons keep the session: %v",
				m.linkHost(msg.dest), msg.err)
			m.handleLinkLost(msg.dest, msg.err)
			m.linkFor(msg.dest).parked = true
			return m, relisten
		}
		mdl, cmd := m.beginReconnect(msg.dest, msg.err)
		return mdl, tea.Batch(relisten, cmd)

	case redialTickMsg:
		// msg.attempt is checked, not just carried. It makes a second concurrent
		// dial impossible by construction rather than by argument: only the tick
		// for the attempt currently armed can start one, so the "slow attempt
		// completing after a fast one" case the result branch guards against has
		// no way to arise in the first place. Per destination, since two ladders
		// can be climbing at once and their attempt numbers are unrelated — and
		// so is the generation, for the same reason.
		link := m.linkOf(msg.dest)
		if msg.gen != link.gen || !link.active || msg.attempt != link.attempt {
			return m, nil
		}
		return m, m.redialCmd(msg.dest)

	case redialResultMsg:
		// Dropped for a superseded generation even when it carries a LIVE
		// client: a slow attempt completing after a fast one would otherwise
		// replace a working connection with a second one, leaving the first
		// with a listen loop nobody reads.
		if msg.gen != m.linkOf(msg.dest).gen || !m.linkOf(msg.dest).active {
			// The !active half mirrors the tick branch above. Without it a
			// failure result arriving with active already false would call
			// scheduleRedial, incrementing attempt and arming a tick that the
			// tick branch then drops for !active — leaving a session with no
			// timer, no banner, and no way back.
			if msg.client != nil {
				// Closed through the seam because tui.Client is only
				// Send/Receive: this package structurally cannot release the
				// ssh child behind it, and dropping the reference leaks that
				// child plus its remote `quil --stdio` for the whole session.
				log.Printf("releasing late reconnect from gen %d (current %d)", msg.gen, m.clientGen)
				m.closeClient(msg.client)
			}
			return m, nil
		}
		if msg.err != nil && errors.Is(msg.err, ErrRemoteVersionMismatch) {
			// The ladder cannot fix this: every further attempt re-authenticates
			// and re-fails until the far side is upgraded. Stop it and let the
			// sidebar offer the upgrade instead.
			if p := m.projectForDest(msg.dest); p != nil && p.Offline != nil {
				p.Offline.Kind = offlineNeedsUpgrade
				p.Offline.Detail = msg.err.Error()
			}
			ls := m.linkFor(msg.dest)
			ls.active, ls.parked = false, false
			// The offer this comment used to promise. Until now "let the
			// sidebar offer the upgrade instead" described nothing: the sidebar
			// had no such affordance, so a link that drifted out of version
			// mid-session went quiet with no way to act on it in the tool.
			m.enqueueUpgradePrompt(msg.dest, msg.err.Error())
			m.promptNextUpgrade()
			return m, nil
		}
		// msg.client == nil with no error is not a success. A dialer returning
		// a typed nil pointer produces a non-nil interface, so this check is
		// the last place that can catch it before Receive panics on it.
		if msg.err != nil || msg.client == nil {
			if msg.err == nil {
				msg.err = errors.New("dialer returned no connection")
			}
			link := m.linkFor(msg.dest)
			link.lastErr = msg.err
			if errors.Is(msg.err, ErrLinkPermanent) {
				// Every reconnect is a full authentication, so retrying a
				// rejected key produces a steady stream of failed auths from the
				// operator's own address — which a default fail2ban sshd jail
				// bans, locking them out of a host that was never unreachable.
				// The banner stays up: the session is paused, not over.
				link.parked = true
				log.Printf("remote: parking reconnect to %s after a permanent failure: %v",
					m.linkHost(msg.dest), msg.err)
				return m, nil
			}
			return m.scheduleRedial(msg.dest)
		}
		return m.finishReconnect(msg.dest, msg.client)

	case sizePollMsg:
		// Inert: re-arms the tick and issues the console-grid probe as Cmds.
		// The model is untouched.
		m.skipRender = !prologueChangedView
		return m, tea.Batch(sizePollProbe, sizePollTick())

	case resizeTickMsg:
		if msg.seq != m.resizeSeq {
			return m, nil // stale tick, newer resize pending
		}
		// Anchor coordinates are stale after a reflow — cheapest correct
		// answer is to close.
		m.closeCtxMenu()
		m.width = m.pendingWidth
		m.height = m.pendingHeight
		m.resizeTabs()
		// Also resize an active overlay pane so the daemon's PTY tracks the new size.
		var overlayCmds []tea.Cmd
		overlayCmds = append(overlayCmds, m.resizeAllPanes())
		if tab := m.activeTabModel(); tab != nil && tab.overlayVisible && tab.overlayPane != nil {
			overlayCmds = append(overlayCmds, m.overlayResizeCmd(tab))
		}
		return m, tea.Batch(overlayCmds...)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		// A click can change the active pane, so a sequence completed after one
		// would target a different pane than the one the prefix was pressed in.
		m.cancelSequence()
		if msg.Mod.Contains(tea.ModCtrl) {
			return m, nil
		}
		// The full-screen read-only viewer paints over EVERYTHING — panes,
		// sidebar, lazygit overlay — so it owns the mouse while it is open.
		// Checked ahead of every swallow below: a tab that happens to have an
		// overlay pane would otherwise eat clicks aimed at a dialog drawn on
		// top of it.
		if m.viewerOwnsMouse() {
			return m.handleViewerMouseClick(msg)
		}
		// A modal is up: the panes are not on screen, so nothing below may act
		// on them. clearDragState aborts a drag armed before the dialog opened.
		if m.modalSwallowsMouse() {
			m.clearDragState()
			return m, nil
		}
		// Overlay visible: swallow all mouse clicks (keyboard-only v1).
		// clearDragState ensures no drag flag stays set from before the overlay opened.
		// The context menu can never be open while the lazygit overlay is
		// visible, so this swallow is safe to keep ahead of the ctxMenu check.
		if tab := m.activeTabModel(); tab != nil && tab.overlayVisible {
			m.clearDragState()
			return m, nil
		}
		// Context menu open: it owns the mouse. Checked BEFORE the sidebar
		// swallow — the menu is drawn (compositor overlay) on TOP of the
		// sidebar, so a menu clamped near the right edge can show rows over
		// the sidebar strip. Input priority must match paint priority: if
		// the sidebar check ran first, a click on a menu row that happens
		// to overlap the strip would be silently swallowed by the sidebar
		// instead of executing the visibly-topmost menu item. Click on an
		// enabled row executes; anywhere else inside the box is swallowed;
		// outside closes — and an outside RIGHT-click falls through to the
		// open path below so it re-targets in one gesture (OS-menu
		// convention), which may include falling into the sidebar swallow
		// next if the retarget lands there.
		if m.ctxMenu.open() {
			if row, inside := ctxMenuHitRow(m.ctxMenu, msg.X, msg.Y); inside {
				if msg.Button == tea.MouseLeft && row >= 0 && m.ctxMenu.items[row].enabled {
					return m.executeCtxMenuItem(m.ctxMenu.items[row])
				}
				return m, nil
			}
			m.closeCtxMenu()
			if msg.Button != tea.MouseRight {
				return m, nil // closing click is consumed, never arms a drag
			}
		}
		// Sidebar overlay region: the press belongs to the sidebar, not
		// the pane rendered beneath it. Clear drag flags so no half-armed
		// drag survives the swallowed press.
		if m.sidebarSwallowsMouse(msg.X, msg.Y) {
			m.clearDragState()
			return m, nil
		}
		// Project sidebar: a RESERVED left column, so the press belongs to
		// it and never to a pane — the pane area starts at its right edge.
		// Ordered after the context menu and the notification overlay (both
		// paint on top of it) and before the pane region (which it
		// displaces), so input priority matches paint priority. The whole
		// strip is swallowed, not just its actionable rows: letting a click
		// on a heading fall through would arm a drag-selection at a column
		// the user never clicked on.
		//
		// It must also stay ahead of the Y==0 tab-bar branch below, which
		// is a bare row test: the tab bar starts at the sidebar's right
		// edge, so at row 0 the sidebar's own PROJECTS heading is what the
		// user clicked, and the strip claims it here.
		// Checked BEFORE projectSidebarSwallowsMouse: the zone's left column
		// is the sidebar's OWN last column, which that branch would otherwise
		// swallow as a row click — so the edge would be ungrabbable from the
		// side the user aims at it from.
		if msg.Button == tea.MouseLeft && m.hitTestSidebarEdge(msg.X, msg.Y) {
			m.beginSidebarDrag()
			return m, nil
		}
		if m.projectSidebarSwallowsMouse(msg.X, msg.Y) {
			m.clearDragState()
			var cmd tea.Cmd
			switch msg.Button {
			case tea.MouseLeft:
				if kind, idx := m.sidebarHit(msg.X, msg.Y); kind != "" {
					return m.activateSidebarRow(kind, idx)
				}
			case tea.MouseRight:
				// Rename/Destroy for a project row, and the pane menu for a
				// pane row. The pane context menu further down never reaches
				// here — the sidebar swallow returns first — so both kinds
				// need their own open call.
				switch kind, idx := m.sidebarHit(msg.X, msg.Y); kind {
				case sidebarRowProject:
					if idx >= 0 && idx < len(m.projects) {
						m.openProjectCtxMenu(m.projects[idx], msg.X, msg.Y)
					}
				case sidebarRowPane:
					// Right-click FOCUSES the pane first, exactly like
					// left-click (activateSidebarRow → focusSidebarPane) —
					// reversing an earlier "does not move focus" decision.
					// Required, not cosmetic: eight of the ten dispatched
					// menu items (Rename, Restart, Close, Mute, Notes, Focus,
					// Lazygit, History) are shared with the keybinding and
					// command-palette paths and resolve their target through
					// the ACTIVE tab's ACTIVE pane internally. Widening all
					// eight handlers to accept an explicit target pane was
					// the larger risk (it would touch every other caller
					// too); focusing first satisfies their existing contract
					// instead. The updated Model — and its switchTab IPC
					// cmd, when the pane's tab was not already active — must
					// carry forward, or the daemon never learns the tab
					// changed.
					//
					// The row is resolved BEFORE the focus, from the model
					// the click was hit-tested against. Resolving it after
					// was correct too — sidebarRows' order, index and
					// tabIdx are all independent of which tab is active,
					// and focusSidebarPane's scroll is a no-op for a row
					// that is on screen by definition — but that is two
					// separate arguments about a strip that moves, both
					// load-bearing for a menu that then acts on the pane
					// it names. Reading it once removes the question.
					row, rowOK := m.sidebarRowAt(msg.X, msg.Y)
					updated, focusCmd := m.activateSidebarRow(sidebarRowPane, idx)
					// Comma-ok, not a bare assertion: activateSidebarRow is
					// typed tea.Model, which is the signature that invites
					// someone to return something else — and a failed assertion
					// here PANICS inside the Update loop. Degrading to "no focus
					// change" keeps the menu open on the row that was clicked.
					if next, ok := updated.(Model); ok {
						m = next
					}
					cmd = focusCmd
					// findPaneAndTab spans every project and PaneModel is
					// a pointer, so a pre-focus id resolves to the same
					// pane on the updated model.
					if rowOK && row.paneID != "" {
						if pane, _, _ := m.findPaneAndTab(row.paneID); pane != nil {
							m.openCtxMenu(pane, msg.X, msg.Y)
						}
					}
				}
			}
			return m, cmd
		}
		// Right-click: copy the active selection to the clipboard. While
		// notes mode is on, the editor's selection takes priority.
		if msg.Button == tea.MouseRight {
			if m.notesMode && m.notesEditor.HasSelection() {
				text := m.notesEditor.ExtractSelection()
				m.notesEditor.ClearSelection()
				if text != "" {
					return m, func() tea.Msg {
						if err := clipboard.Write(text); err != nil {
							log.Printf("notes clipboard write: %v", err)
						}
						return nil
					}
				}
				return m, nil
			}
			if m.selection != nil {
				tab := m.activeTabModel()
				if tab != nil {
					if pane := tab.ActivePaneModel(); pane != nil {
						text := extractText(pane, m.selection)
						m.selection = nil
						if text != "" {
							return m, func() tea.Msg {
								if err := clipboard.Write(text); err != nil {
									log.Printf("pane clipboard write: %v", err)
								}
								return nil
							}
						}
						return m, nil
					}
				}
				m.selection = nil
				return m, nil
			}
			// No selection anywhere: open the pane context menu for the
			// pane under the cursor. Suppressed while a modal dialog,
			// rename edit, or notes mode owns input (the lazygit overlay
			// and sidebar swallows already returned above).
			if m.dialog == dialogNone && !m.notesMode && !m.renaming && !m.renamingPane {
				if rect := m.paneRectAt(msg.X, msg.Y); rect != nil && rect.Pane != nil {
					m.openCtxMenu(rect.Pane, msg.X, msg.Y)
				}
			}
			return m, nil
		}
		if msg.Button == tea.MouseLeft {
			if msg.Y == 0 {
				// Tab bar — prime the drag tracker so subsequent motion at
				// Y=0 reorders. clearDragState first enforces the
				// "one drag at a time" invariant.
				m.clearDragState()
				if idx := m.hitTestTab(msg.X); idx >= 0 {
					m.tabDragFromIdx = idx
					return m, m.switchTab(idx)
				}
			} else if msg.Y < m.height-1 {
				// Notes editor click takes priority — the document anchor
				// is resolved once at click time so motion events can't
				// drift it if ScrollTop changes mid-drag.
				if row, col, ok := m.notesEditorPosAt(msg.X, msg.Y); ok {
					m.clearDragState()
					m.notesMouseDown = true
					m.mouseStartX = msg.X
					m.mouseStartY = msg.Y
					m.notesAnchorRow = row
					m.notesAnchorCol = col
					m.selection = nil
					m.notesPaneFocused = false
					m.notesEditor.SetCursor(row, col)
					return m, nil
				}
				// Split-border click arms a drag-resize. Checked BEFORE
				// the scrollbar so the drawn split line (both border
				// glyphs) always grabs the border: the left glyph sits
				// inside the left pane's widened scrollbar hit zone and
				// would otherwise be swallowed silently — worst on panes
				// with no scrollback, where the eaten click gives no
				// feedback at all. The border zone never extends left of
				// the drawn line (asymmetric extent, layout.go), so the
				// scrollbar's drawn thumb column stays clickable.
				if hit := m.hitTestSplitBorder(msg.X, msg.Y); hit != nil {
					m.clearDragState()
					m.splitDragNode = hit.Node
					m.splitDragRect = *hit
					m.selection = nil
					m.setSplitDragHighlight(hit, true)
					return m, nil
				}
				// Scrollbar click jumps the thumb and starts a drag. The
				// rect is captured once so a window resize mid-drag doesn't
				// drift the mapping.
				if rect := m.hitTestScrollbar(msg.X, msg.Y); rect != nil {
					m.clearDragState()
					rect.Pane.ScrollToRelY(msg.Y-(rect.OY+1), rect.H-2)
					m.scrollDragPaneID = rect.Pane.ID
					m.scrollDragRect = *rect
					m.selection = nil
					return m, nil
				}
				// Pane area — start tracking for drag selection. Wide-canvas
				// preview panes route through previewPosAt in
				// updateMouseSelection, so a drag here arms normally.
				m.clearDragState()
				m.mouseDown = true
				m.mouseStartX = msg.X
				m.mouseStartY = msg.Y
				m.selection = nil
				if m.notesMode && m.notesEditor != nil {
					m.notesPaneFocused = true
				}
			}
		}
		return m, nil

	case tea.MouseMotionMsg:
		if m.viewerOwnsMouse() {
			return m.handleViewerMouseMotion(msg)
		}
		if m.modalSwallowsMouse() {
			return m, nil
		}
		// Overlay visible: swallow all motion (keyboard-only v1).
		if tab := m.activeTabModel(); tab != nil && tab.overlayVisible {
			return m, nil
		}
		// Context menu open: hover moves the cursor; everything else is
		// swallowed so no drag can advance underneath the popup.
		if m.ctxMenu.open() {
			if row, inside := ctxMenuHitRow(m.ctxMenu, msg.X, msg.Y); inside && row >= 0 && m.ctxMenu.items[row].enabled {
				m.ctxMenu.cursor = row
			}
			return m, nil
		}
		// Drag dispatch — at most one branch is active (clearDragState
		// invariant). Off-Y=0 motion during a tab drag pauses reorder but
		// keeps the drag alive so the user can return to the tab bar
		// without releasing.
		if m.tabDragFromIdx >= 0 && msg.Y == 0 {
			target := m.hitTestTab(msg.X)
			if target >= 0 && target != m.tabDragFromIdx && m.tabDragFromIdx < len(m.curTabs()) {
				tabID := m.curTabs()[m.tabDragFromIdx].ID
				if m.moveTab(m.tabDragFromIdx, target) {
					m.tabDragFromIdx = target
					return m, m.sendReorderTab(tabID, target)
				}
			}
			return m, nil
		}
		if m.scrollDragPaneID != "" {
			if pane := m.activePaneByID(m.scrollDragPaneID); pane != nil {
				rect := m.scrollDragRect
				pane.ScrollToRelY(msg.Y-(rect.OY+1), rect.H-2)
			}
			return m, nil
		}
		if m.splitDragNode != nil {
			m.dragSplitBorder(msg.X, msg.Y)
			return m, nil
		}
		if m.sidebarDragging {
			m.trackSidebarDrag(msg.X)
			return m, nil
		}
		if m.notesMouseDown && m.notesMode && m.notesEditor != nil {
			row, col, ok := m.notesEditorPosAt(msg.X, msg.Y)
			if !ok {
				return m, nil
			}
			if !m.notesEditor.HasSelection() {
				m.notesEditor.BeginSelection(m.notesAnchorRow, m.notesAnchorCol)
			}
			m.notesEditor.ExtendSelection(row, col)
			return m, nil
		}
		if m.mouseDown {
			tab := m.activeTabModel()
			if tab != nil && tab.Root != nil {
				tabH := m.height - chromeHeight
				m.updateMouseSelection(tab, msg.X, msg.Y, tabH)
			}
		}
		return m, nil

	case tea.MouseReleaseMsg:
		if m.viewerOwnsMouse() {
			// The selection stays — release only ends the drag.
			m.viewerMouseDown = false
			return m, nil
		}
		if m.modalSwallowsMouse() {
			m.clearDragState()
			return m, nil
		}
		// Overlay visible: clear any stale drag state and swallow the release.
		if tab := m.activeTabModel(); tab != nil && tab.overlayVisible {
			m.clearDragState()
			return m, nil
		}
		if m.ctxMenu.open() {
			return m, nil // no drags can be live while the menu is open
		}
		// A split-border drag commits on release: one PTY resize per pane
		// plus the persisted layout ratio (finishSplitDrag), highlight off.
		if m.splitDragNode != nil {
			return m, m.finishSplitDrag()
		}
		// The sidebar edge commits on release for the same reason the split
		// border does: one PTY resize per pane, once, rather than per motion
		// event.
		if m.sidebarDragging {
			return m, m.finishSidebarDrag()
		}
		// A tab drag or scrollbar drag terminates here with no further
		// processing — they don't share the click-vs-drag pane-focus
		// fall-through path below.
		if m.tabDragFromIdx >= 0 || m.scrollDragPaneID != "" {
			m.clearDragState()
			return m, nil
		}
		if m.notesMouseDown {
			m.clearDragState()
			return m, nil
		}
		if m.mouseDown {
			m.mouseDown = false
			if m.selection == nil {
				// No drag — treat as click for pane focus. Skip when notes
				// mode is active so the editor stays bound to its pane
				// regardless of where the user clicks.
				tab := m.activeTabModel()
				if tab != nil && tab.Root != nil && !tab.FocusMode() && !m.notesMode {
					tabH := m.height - chromeHeight
					if pane := tab.Root.FindPaneAt(m.mouseStartX, m.mouseStartY, m.projectSidebarWidth(), 1, m.paneAreaWidth(), tabH); pane != nil {
						if old := tab.ActivePaneModel(); old != nil {
							old.Active = false
						}
						pane.Active = true
						tab.ActivePane = pane.ID
					}
				}
			}
		}
		return m, nil

	case tea.MouseWheelMsg:
		if m.viewerOwnsMouse() {
			m.scrollViewer(msg.Button)
			return m, nil
		}
		// The history list is a scrolling list drawn over the panes; without
		// this the wheel would scroll a pane's scrollback behind the modal.
		if m.dialog == dialogCommandHistory {
			m.scrollHistoryList(msg.Button)
			return m, nil
		}
		// Every other modal has nothing to scroll, but the panes behind it must
		// not scroll either.
		if m.modalSwallowsMouse() {
			return m, nil
		}
		if m.ctxMenu.open() {
			return m, nil // wheel is swallowed while the menu is open
		}
		// Overlay visible: swallow wheel events (keyboard-only v1).
		if tab := m.activeTabModel(); tab != nil && tab.overlayVisible {
			return m, nil
		}
		// Wheel over the sidebar overlay must not scroll the pane beneath.
		if m.sidebarSwallowsMouse(msg.X, msg.Y) {
			return m, nil
		}
		// The project sidebar's reserved column scrolls its own PANES section;
		// the pane the wheel would otherwise scroll is not the one under the
		// cursor.
		if m.projectSidebarSwallowsMouse(msg.X, msg.Y) {
			// The two vertical buttons are matched EXPLICITLY, as every other
			// wheel consumer in this package does. tea.MouseWheelMsg also
			// carries MouseWheelLeft/Right (a trackpad or shift-scroll emits
			// them), and collapsing the button to `== MouseWheelUp` made both
			// of those read as "not up" and scrolled the body DOWN. The swallow
			// stays OUTSIDE the switch: a horizontal notch aimed at the strip
			// must not fall through to the pane area either.
			switch msg.Button {
			case tea.MouseWheelUp:
				m.scrollSidebar(true)
			case tea.MouseWheelDown:
				m.scrollSidebar(false)
			}
			return m, nil
		}
		lines := m.cfg.UI.MouseScrollLines
		if lines < 1 {
			lines = 3
		}
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				// Apps that requested mouse tracking (opencode, claude-code,
				// vim, htop, lazygit, …) scroll their own viewport. Forward
				// the wheel to the PTY — these run on the alternate screen,
				// which never feeds Quil's scrollback, so local scrolling is a
				// silent no-op. One event per wheel notch matches a real
				// terminal; the app applies its own scroll step.
				if pane.MouseTracking() {
					// Only forward when we can resolve the pane's rect: a nil rect
					// means the layout is momentarily unsettled (rapid tab switch,
					// split-then-focus), and forwarding with a (0,0) origin would
					// hand any-event tracking (?1003) a bogus cursor position.
					// Either way this pane's local scrollback is never populated
					// (alt-screen), so swallow the event rather than scrolling it.
					if rect := m.activePaneRect(); rect != nil {
						relX := msg.X - rect.OX - 1
						relY := msg.Y - rect.OY - 1
						if seq := pane.wheelForwardSeq(msg.Button == tea.MouseWheelUp, relX, relY); seq != nil {
							logger.Debug("wheel: forward pane=%s type=%s btn=%v rel=(%d,%d) seq=%q (local n=%v b=%v a=%v sgr=%v daemonTrack=%v)",
								pane.ID, pane.Type, msg.Button, relX, relY, string(seq),
								pane.mouseNormal, pane.mouseButton, pane.mouseAny, pane.mouseSGR, pane.daemonMouseTracking)
							m.sendInputToPane(pane.ID, seq)
						}
					}
					return m, nil
				}
				if msg.Button == tea.MouseWheelUp {
					pane.ScrollUp(lines)
				} else if msg.Button == tea.MouseWheelDown {
					pane.ScrollDown(lines)
				}
			}
		}
		return m, nil

	case sequenceTimeoutMsg:
		// Local timer: this arm must NOT re-arm listenForMessages. The
		// generation compare is what stops a cancelled sequence's in-flight
		// tick from clearing the one started after it.
		if msg.gen == m.pendingGen {
			m.cancelSequence()
		}
		return m, nil

	case tea.PasteMsg:
		// Paste bypasses handleKey and lands in the PTY, so an armed prefix
		// would read the next keystroke as a sequence step.
		m.cancelSequence()
		if m.dialog == dialogPluginMigration && m.migrationLeft != nil && !m.migrationRightFocus {
			text := strings.ReplaceAll(msg.Content, "\r", "")
			m.migrationLeft.InsertMultiLine(text)
			m.migrationLeft.Dirty = true
			return m, nil
		} else if m.dialog == dialogTOMLEditor && m.tomlEditor != nil {
			text := strings.ReplaceAll(msg.Content, "\r", "")
			m.tomlEditor.InsertMultiLine(text)
			m.tomlEditor.Dirty = true
			return m, nil
		} else if m.dialog != dialogNone && m.dialogEdit {
			m.dialogInput += sanitizeDialogInput(msg.Content)
			return m, nil
		} else if m.notesMode && m.notesEditor != nil {
			text := strings.ReplaceAll(msg.Content, "\r", "")
			m.notesEditor.HandlePaste(text)
			return m, nil
		} else if m.dialog == dialogCommandPalette {
			// Fold pasted text into the fuzzy query, keeping only printable runes
			// (same guard as typed input — drops newlines, tabs, control bytes).
			// Without this branch the paste would fall through to
			// sendClipboardToPane and be injected into the hidden pane's PTY — an
			// input-isolation break and a clipboard leak into a background shell
			// (a trailing newline could even execute it).
			m.palette.query += sanitizePaletteQuery(msg.Content)
			return m.afterPaletteQueryChange()
		} else {
			// Empty bracketed-paste content means the terminal (e.g. Windows
			// Terminal on Ctrl+V) fired a paste for a clipboard that holds an
			// image but no text. Route to the same image-capable path the
			// F8/Ctrl+Alt+V keypress uses (pasteClipboard's image fallback) so
			// screenshot paste works on Ctrl+V again.
			if msg.Content == "" {
				logger.Debug("PasteMsg: empty content, routing to image-capable pasteClipboard")
				return m, m.pasteClipboard()
			}
			m.sendClipboardToPane(msg.Content)
			// Schedule re-render after PTY echo arrives to update cursor position
			return m, tea.Tick(100*time.Millisecond, func(_ time.Time) tea.Msg { return pasteRefreshMsg{} })
		}

	case clipboardPastedMsg:
		// The clipboard read happened on a tea.Cmd goroutine; the ENQUEUE
		// happens here, on the Update goroutine, so paste joins the ordered
		// input queue at a defined point relative to the keys around it, and
		// goes to the pane that was active when the paste was requested.
		m.sendClipboardToPaneID(msg.paneID, msg.text)
		// Schedule re-render after PTY echo arrives to update cursor position
		return m, tea.Tick(100*time.Millisecond, func(_ time.Time) tea.Msg { return pasteRefreshMsg{} })

	case pasteRefreshMsg:
		return m, nil // triggers re-render with updated VT emulator cursor

	case dialogPasteMsg:
		m.dialogInput += sanitizeDialogInput(string(msg))
		return m, nil

	case editorPasteMsg:
		if m.dialog == dialogPluginMigration && m.migrationLeft != nil && !m.migrationRightFocus {
			text := strings.ReplaceAll(string(msg), "\r", "")
			m.migrationLeft.InsertMultiLine(text)
			m.migrationLeft.Dirty = true
		} else if m.dialog == dialogTOMLEditor && m.tomlEditor != nil {
			text := strings.ReplaceAll(string(msg), "\r", "")
			m.tomlEditor.InsertMultiLine(text)
			m.tomlEditor.Dirty = true
		} else if m.notesMode && m.notesEditor != nil {
			text := strings.ReplaceAll(string(msg), "\r", "")
			m.notesEditor.HandlePaste(text)
		}
		return m, nil

	case PaneOutputMsg:
		cmd, changedView := m.handlePaneOutput(msg)
		// Only the active tab is rendered, so output from any other pane leaves
		// the frame identical. Folds in the prologue exactly as the four inert
		// message types do — an unacked focus change or a pruned context menu
		// is a real change this arm must not swallow.
		m.skipRender = !changedView && !prologueChangedView
		// Attributed in View, not here: this arm knows a skip was INTENDED,
		// only View knows one happened.
		m.skipHidden = m.skipRender
		if cmd != nil {
			return m, tea.Batch(cmd, m.listenForMessages())
		}
		return m, m.listenForMessages()

	case paneSettleRepaintMsg:
		return m, tea.ClearScreen

	case flashExpireMsg:
		// Clear flash only if it hasn't been refreshed by a newer setFlash call.
		if !time.Now().Before(m.flashUntil) {
			m.flashText = ""
		}
		return m, nil

	case spinnerTickMsg:
		// Advance spinner frame for the resuming/preparing pane. Exactly one
		// tick chain per pane: spinnerTickRunning (set at the start site) is
		// cleared here when the chain stops, so a re-arm can start a fresh one
		// without ever stacking two chains (which would double the frame rate).
		pane := m.spinnerTargetPane(msg.paneID)
		if pane == nil {
			return m, nil
		}
		// Keep the indicator alive until the pane's first live output
		// (min display met) or the safety cap — not a fixed 2s timer.
		if (pane.resuming || pane.preparing) && !pane.restoreSettled() {
			pane.spinnerFrame = msg.frame
			nextFrame := msg.frame + 1
			paneID := msg.paneID
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{paneID: paneID, frame: nextFrame}
			})
		}
		// Chain stopped: settled, or no longer resuming/preparing.
		pane.resuming = false
		pane.preparing = false
		pane.spinnerTickRunning = false
		return m, nil

	case workSpinnerTickMsg:
		// Self-stopping loop: only keep ticking while a pane is mid-turn.
		if !m.anyPaneWorking() {
			m.workTickRunning = false
			return m, nil
		}
		m.workSpinnerFrame++
		// Mirror the shared frame onto every working pane so the top-border
		// spinner (rendered inside PaneModel.View) stays in sync with the tab.
		for _, tab := range m.allTabs() {
			if tab.Root == nil {
				continue
			}
			for _, p := range tab.Leaves() {
				if p != nil && p.working {
					p.workFrame = m.workSpinnerFrame
				}
			}
		}
		return m, m.workSpinnerTick()

	case destDialedMsg:
		// Discard an answer for a host the user has since retyped: the dial is
		// slow enough that editing the field during one is ordinary, and
		// applying a stale result would point the form at a machine the user
		// has already moved on from.
		if msg.dest != m.projectFormDialing {
			return m, nil
		}
		m.projectFormDialing = ""
		if msg.err != nil {
			// The host answered with something provisioning can fix — no quil
			// at all, or one too old for this client to attach to. Do it rather
			// than making the user leave the dialog for `quil remote setup`:
			// the machinery is the same, only the entry point differs, and a
			// user who just named a host has already said where they want to
			// work.
			// At most ONE install per host per session. A dial that still
			// reports the binary missing right after a successful install
			// means something the install cannot fix — it landed somewhere the
			// non-interactive PATH does not cover, or the recorded path never
			// reached the dialer — and offering again just spins: install,
			// retry, 127, install. Observed as a five-second loop. The CLI
			// path has healRemoteRecord for the same hazard. The guard is
			// shared with the upgrade because the loop is: a daemon that still
			// reports the old version after an upgrade did not restart, and
			// pushing the same archive again cannot change that.
			if note := installOffer(msg.err, msg.dest); note != "" && m.installDestFn != nil && !m.installedDests[msg.dest] {
				if m.installedDests == nil {
					m.installedDests = map[string]bool{}
				}
				m.installedDests[msg.dest] = true
				m.projectFormInstalling = msg.dest
				// Busy, not an error: the host answered and Quil is working on
				// it. Rendered as a red ✗ this read as the failure the line
				// below reports, which is a different outcome entirely.
				m.setFormBusy(note)
				return m, m.installDest(msg.dest)
			}
			m.setFormError("cannot connect: " + truncateToWidth(sanitizeRemoteText(msg.err.Error()), formMsgDetailCap))
			return m, nil
		}
		attach := m.adoptDest(msg.dest, msg.client)
		m.setFormOK("connected to " + sanitizeRemoteText(msg.dest))
		m.projectFormDest = msg.dest
		m.projectFormCursor = projectRowRootDir
		m.resetProjectBrowseState()
		// Sequenced, not batched with the browse: adoptDest writes the attach
		// ledger onto this Model, and the browse must be requested against the
		// destination it just installed.
		browse := m.requestBrowseDirForDest(msg.dest, "", "", "")
		return m, tea.Batch(attach, browse)

	case destInstalledMsg:
		if msg.dest != m.projectFormInstalling {
			return m, nil // the user moved on, same as a stale dial
		}
		m.projectFormInstalling = ""
		// ClearScreen on BOTH arms: runRemoteSetup still narrates its progress
		// to stderr, which is the real terminal underneath this dialog, so
		// whatever it printed has to be painted over before the user reads the
		// result. Threading a writer through it is the proper fix; repainting
		// is what keeps the screen honest until then.
		if msg.err != nil {
			m.setFormError("install failed: " + truncateToWidth(sanitizeRemoteText(msg.err.Error()), formMsgDetailCap))
			return m, tea.ClearScreen
		}
		// Provisioned — retry the dial that failed. Retrying rather than
		// assuming success is the point: the install proves a binary is on
		// disk, not that this client can attach to the daemon it starts, and
		// the version gate still has to run.
		m.projectFormDialing = msg.dest
		// Still busy: the install landed but the dial that proves it has not run
		// yet, and a blank line here would read as "finished" for the seconds
		// that retry takes.
		m.setFormBusy("installed on " + sanitizeRemoteText(msg.dest) + " — reconnecting…")
		return m, tea.Batch(tea.ClearScreen, m.dialDest(msg.dest))

	case PluginErrorMsg:
		m.dialog = dialogPluginError
		m.pluginErrorTitle = msg.Title
		m.pluginErrorMessage = msg.Message
		return m, tea.Batch(tea.ClearScreen, m.listenForMessages())

	case WorkspaceStateMsg:
		// Refuse state from a destination the Model has forgotten. Router.Remove
		// closes the pump's stop channel and every publish point checks it, but
		// that only covers a pump PARKED in Receive — a broadcast already in the
		// buffer is still delivered, and one that passed the check just before
		// Remove wins the select about half the time. applyWorkspaceState treats
		// a broadcast as authoritative for its dest, so a late one re-appends
		// the projects the user just dismissed, and they come back unusable:
		// knownDests no longer lists the dest, so nothing re-attaches it and
		// every send for its panes is dropped. Dropping the state here rather
		// than trying to purge the channel keeps the fix at the one boundary
		// that knows what the Model currently holds.
		if !m.destConnected(msg.Dest) {
			log.Printf("ignoring workspace state from %s: disconnected", msg.Dest)
			return m, m.listenForMessages()
		}
		m.noteWorkspaceState(msg.Update, msg.Dest)
		// TODO(freeze-diagnostic): the 8 "apply: ..." breadcrumbs in this case
		// and inside applyWorkspaceState were added to pinpoint a TUI Update
		// wedge during claude-code pane creation (2026-04-22). The root cause
		// turned out to be a drained-less VT emulator pipe in pane.go, fixed
		// in the same PR. Keep the breadcrumbs for ~2 weeks of watchdog-clean
		// runs, then either delete or demote them to logger.Debug.
		log.Printf("WorkspaceState: %d tabs, %d panes", len(msg.Tabs), len(msg.Panes))
		newPaneIDs, overlayResizeCmds := m.applyWorkspaceState(msg, msg.Dest)
		log.Printf("apply: returned, %d new panes", len(newPaneIDs))
		// An open project picker holds a filtered snapshot taken when it opened.
		// A project created or destroyed by another client — or a host
		// disconnected — would otherwise be invisible to it until it closed,
		// which is exactly when the user is choosing from that list.
		if m.dialog == dialogProjectPick {
			m.projectPick.filtered = m.filterProjects(m.projectPick.query)
			m.clampProjectPickCursor()
		}
		m.resizeTabs()
		log.Printf("apply: resizeTabs done")
		// Diffed, not swept. A broadcast that agrees with what we already hold
		// used to cost one must-deliver frame per tab PLUS one per pane — 69
		// frames on a 64-slot queue at 33 tabs/36 panes, which overflowed and
		// made the client's own IPC layer close the connection (2026-08-09).
		// Both diffs run here, on the Update goroutine, because they read
		// m.projects, which applyWorkspaceState has just rebuilt.
		cmds := []tea.Cmd{
			m.listenForMessages(),
			m.sendDiffedResizes(m.diffResizes(msg)),
			m.sendDiffedLayouts(m.diffLayouts(msg)),
		}
		// Resize overlay PTYs that just became visible on initial creation.
		// resizeAllPanes only walks tab.Leaves() (the layout tree), so overlay
		// panes are skipped there; these cmds are the only resize they receive.
		cmds = append(cmds, overlayResizeCmds...)
		// Start spinner ticks for newly restored panes. Guard tree panes with
		// spinnerTickRunning so a pane that already has a live tick chain (e.g.
		// armed in a previous broadcast) never gets a second one — two chains
		// would advance spinnerFrame independently and double the visible rate.
		// Overlay panes are guarded too: their ticks used to find no pane and
		// die, so a stacked chain was invisible — and it stopped being so the
		// moment the handler learned to resolve them.
		for _, paneID := range newPaneIDs {
			id := paneID
			if pane := m.spinnerTargetPane(id); pane != nil {
				if pane.spinnerTickRunning {
					continue
				}
				pane.spinnerTickRunning = true
			}
			cmds = append(cmds, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{paneID: id, frame: 1}
			}))
		}
		// If stale plugins need migration, show the dialog now that workspace
		// is loaded and the user can see their panes behind the dialog.
		if len(m.migrationPlugins) > 0 && m.migrationLeft == nil {
			m.openMigrationDialog()
		}
		log.Printf("apply: cmds prepared (n=%d), returning from Update", len(cmds))
		return m, tea.Batch(cmds...)

	case setActivePaneMsg:
		// MCP set_active_pane targets any pane daemon-wide, including one in
		// a background project — jumpToPane spans every project so the
		// request cannot silently no-op just because the target isn't in the
		// project currently on screen.
		ok, overlayCmd := m.jumpToPane(msg.PaneID)
		if ok {
			log.Printf("set_active_pane: switched to pane %s", msg.PaneID)
		} else {
			log.Printf("set_active_pane: pane %s not found", msg.PaneID)
		}
		return m, tea.Batch(overlayCmd, m.listenForMessages())

	case highlightPaneMsg:
		m.mcpHighlights[msg.PaneID] = true
		m.mcpHighlightSeq[msg.PaneID]++
		seq := m.mcpHighlightSeq[msg.PaneID]
		dur, err := time.ParseDuration(m.cfg.MCP.HighlightDuration)
		if err != nil || dur <= 0 {
			dur = 10 * time.Second
		}
		if dur > 60*time.Second {
			dur = 60 * time.Second
		}
		paneID := msg.PaneID
		return m, tea.Batch(
			m.listenForMessages(),
			tea.Tick(dur, func(_ time.Time) tea.Msg {
				return clearHighlightMsg{PaneID: paneID, Seq: seq}
			}),
		)

	case clearHighlightMsg:
		// Only clear if sequence matches (a newer highlight hasn't replaced us)
		if m.mcpHighlightSeq[msg.PaneID] == msg.Seq {
			delete(m.mcpHighlights, msg.PaneID)
		}
		return m, nil

	case paneEventMsg:
		// Skip output_idle events for the pane the user is currently looking
		// at — it's redundant noise. Other event types (process_exit, bell,
		// command_complete) stay even on the active pane: they're transient
		// state changes that benefit from a sidebar audit trail.
		//
		// Work-state-only signals (see workStateOnlyEvent) drive the spinner
		// and nothing else — never a user-facing card.
		//
		// A muted pane's daemon still forwards work-state hook events live
		// (see emitEvent) so `working` tracks reality across mute/unmute —
		// but muting must still mean "no visible notification", so suppress
		// the sidebar card for any event sourced from a muted pane.
		eventPane, _, _ := m.findPaneAndTab(msg.PaneID)
		muted := eventPane != nil && eventPane.Muted
		workStateOnly := workStateOnlyEvent(msg.Type)
		if !muted && !workStateOnly && !(msg.Type == "output_idle" && m.isActivePane(msg.PaneID)) {
			m.notifications.AddEvent(ipc.PaneEventPayload(msg))
		}
		cmds := []tea.Cmd{m.listenForMessages()}
		// Model/context usage rides turn-boundary hook events as Data keys
		// (claude Stop/PostCompact, opencode session.idle) — apply it live so
		// the status bar refreshes without waiting for a workspace snapshot.
		if eventPane != nil {
			if msg.Data["compacting"] == "1" {
				// Post-compaction reset: show "<model> · compacting" until the
				// next completed turn reports the true reduced context size.
				eventPane.ContextTokens = ipc.ContextTokensCompacting
			} else if model := msg.Data["model"]; model != "" {
				if tokens, err := strconv.ParseInt(msg.Data["context_tokens"], 10, 64); err == nil && tokens >= 0 {
					eventPane.Model = model
					eventPane.ContextTokens = tokens
				}
			}
		}
		// Update working state + unseen marks from the same hook stream.
		m.applyWorkTransition(msg.PaneID, msg.Type, msg.Data)
		if m.anyPaneWorking() && !m.workTickRunning {
			m.workTickRunning = true
			cmds = append(cmds, m.workSpinnerTick())
		}
		// Refresh sidebar tick if visible (no auto-show — user controls with Alt+N)
		if m.notifications.visible {
			cmds = append(cmds, m.startSidebarTick())
		}
		return m, tea.Batch(cmds...)

	case sidebarTickMsg:
		// Re-render sidebar to update relative timestamps; schedule next tick if still visible
		if m.notifications.visible && m.notifications.Count() > 0 {
			return m, m.sidebarTick() // chain continues; running flag stays set
		}
		m.sidebarTickRunning = false
		return m, nil

	case notesTickMsg:
		// Debounce check: save if dirty and idle for >= notesDebounceWindow.
		if m.notesMode && m.notesEditor != nil {
			m.notesEditor.MaybeAutoSave()
			return m, m.notesTick() // chain continues; running flag stays set
		}
		m.notesTickRunning = false
		return m, nil

	case memoryTickMsg:
		// Inert: refreshMemory only builds a Cmd that sends an IPC request.
		// The reply (memoryReportMsg) is what updates the status-bar total, and
		// that branch renders.
		m.skipRender = !prologueChangedView
		return m, tea.Batch(m.refreshMemory(), memoryTickCmd())

	case memoryReportMsg:
		m = m.applyMemoryReport(msg.Resp)
		return m, m.listenForMessages()

	case paletteSearchDebounceMsg:
		// Only fire if still open on the same non-empty query. Local timer —
		// consumed no daemon message, so it must NOT re-arm listenForMessages.
		if m.dialog == dialogCommandPalette && m.palette.query == msg.query && strings.TrimSpace(msg.query) != "" {
			m.palette.searching = true
			return m, tea.Batch(m.requestPaneSearch(msg.query), paletteSearchTimeout(msg.query))
		}
		return m, nil

	case paletteSearchTimeoutMsg:
		// The request for this query never answered — surface it instead of
		// leaving "Searching…" up forever. Also a local timer: no re-arm.
		if m.dialog == dialogCommandPalette &&
			m.palette.query == msg.query && m.palette.searching {
			m.palette.searching = false
			m.palette.timedOut = true
		}
		return m, nil

	case paneSearchRespMsg:
		m = m.applyPaneSearch(msg.Resp)
		return m, m.listenForMessages()

	case claudeSessionsRespMsg:
		m = m.applyClaudeSessions(msg.Resp)
		return m, m.listenForMessages()

	case gitReposMsg:
		// MUST re-arm the listen loop, like every other IPC response branch —
		// omitting it kills IPC for the session, a bug this package has shipped.
		cmd := m.applyGitRepos(msg.Resp, msg.Gen)
		return m, tea.Batch(cmd, m.listenForMessages())

	case gitScanTimeoutMsg:
		// Local timer, so deliberately no re-arm.
		return m, m.applyGitScanTimeout(msg.cwd, msg.gen)

	case worktreeListMsg:
		// MUST re-arm the listen loop, like every other IPC response branch —
		// omitting it kills IPC for the session, a bug this package has shipped.
		m.applyWorktreeList(msg)
		return m, m.listenForMessages()

	case worktreeStatusMsg:
		// Same rule: an IPC response arm that does not re-arm ends the loop.
		m.applyWorktreeStatus(msg)
		return m, m.listenForMessages()

	case worktreeStatusTimeoutMsg:
		// Local timer, so deliberately NO re-arm — the distinction
		// worktreeTimeoutMsg below makes.
		m.applyWorktreeStatusTimeout(msg)
		return m, nil

	case createPaneRespMsg:
		// Same rule: an IPC response arm that returns a bare nil ends the
		// listen loop for the session.
		m.applyCreatePaneResp(msg.Resp, msg.Dest)
		return m, m.listenForMessages()

	case createPaneTimeoutMsg:
		// Local timer, so deliberately NO re-arm — the same distinction
		// worktreeTimeoutMsg below makes.
		m.applyCreatePaneTimeout(msg.tabID)
		return m, nil

	case createTabFailedMsg:
		// Not an IPC response — it is the send reporting that it never left, so
		// no re-arm (the listen loop was never involved). Router.Send would have
		// dropped this silently and returned nil, which is why the send is
		// strict: a tab the user asked for and did not get has to say so.
		m.setFlash("cannot reach " + hostLabel(msg.dest) + " — new tab not created")
		return m, m.flashCmd()

	case worktreeTimeoutMsg:
		// Local timer, so deliberately no re-arm.
		m.applyWorktreeTimeout(msg)
		return m, nil

	case kubeCtxMsg:
		// MUST re-arm the listen loop, like every other IPC response branch —
		// omitting it kills IPC for the session, a bug this package has shipped.
		cmd := m.applyKubeContexts(msg.Resp, msg.Gen)
		return m, tea.Batch(cmd, m.listenForMessages())

	case kubeScanTimeoutMsg:
		// Local timer, so deliberately no re-arm.
		return m, m.applyKubeScanTimeout(msg.gen)

	case recentDirsMsg:
		// MUST re-arm the listen loop, like every other IPC response branch —
		// omitting it kills IPC for the session, a bug this package has shipped.
		cmd := m.applyExistingDirs(msg.Resp, msg.Gen)
		return m, tea.Batch(cmd, m.listenForMessages())

	case recentScanTimeoutMsg:
		// Local timer, so deliberately no re-arm.
		return m, m.applyExistingDirsTimeout(msg.gen)

	case pluginListMsg:
		// MUST re-arm the listen loop, like every other IPC response branch.
		cmd := m.applyPluginList(msg.Resp)
		return m, tea.Batch(cmd, m.listenForMessages())

	case browseDirMsg:
		// MUST re-arm the listen loop, like every other IPC response branch —
		// omitting it kills IPC for the session, a bug this package has shipped.
		cmd := m.applyBrowseResponse(msg.Resp, msg.Gen)
		return m, tea.Batch(cmd, m.listenForMessages())

	case browseTimeoutMsg:
		// Local timer, so deliberately no re-arm.
		return m, m.applyBrowseTimeout(msg.path, msg.child, msg.gen)

	case sessionScanTimeoutMsg:
		// Turn a never-answered listing into something diagnosable instead of
		// leaving "Scanning…" up forever. Local timer: no re-arm.
		if m.sessionScanCWD == msg.cwd && m.sessionState == sessionScanning {
			m.sessionState = sessionScanTimedOut
		}
		return m, nil

	case claudeSessionDetailRespMsg:
		m = m.applyClaudeSessionDetail(msg.Resp)
		return m, m.listenForMessages()

	case sessionDetailTimeoutMsg:
		// Same contract as sessionScanTimeoutMsg: local timer, no re-arm.
		if m.sessionDetail.id == msg.id && m.sessionDetail.state == sessionScanning {
			m.sessionDetail.state = sessionScanTimedOut
		}
		return m, nil

	case stageUpdateRespMsg:
		next, cmd := m.applyStageUpdateResp(msg.Resp)
		// Always re-arm the listen loop: this is an IPC response, so dropping
		// the re-arm kills every later message on the connection.
		return next, tea.Batch(m.listenForMessages(), cmd)

	case historyListMsg:
		m = m.applyHistoryList(msg.Resp)
		return m, m.listenForMessages()

	case historyTimeoutMsg:
		// LOCAL timer — deliberately does NOT re-arm listenForMessages. Doing so
		// would put a second reader on the IPC connection.
		return m.applyHistoryTimeout(msg.paneID), nil

	case historyEntryMsg:
		// All branches must keep the IPC listen loop alive — these messages
		// originate from listenForMessages.
		//
		// Drop a stale response: if the user navigated to another pane's history
		// or closed the dialog before this entry arrived, opening the viewer now
		// would yank them into the wrong pane's prompt. The list path guards the
		// same way in applyHistoryList.
		if msg.Resp.PaneID != m.history.paneID || m.dialog != dialogCommandHistory {
			return m, m.listenForMessages()
		}
		if !msg.Resp.Found {
			return m, tea.Batch(m.requestHistory(m.history.paneID), m.listenForMessages())
		}
		label := fmt.Sprintf("Input @ %s", time.UnixMilli(msg.Resp.TsMs).Format("2006-01-02 15:04:05"))
		// The viewer writes straight to the terminal — a full-screen editor is
		// not a pane, so nothing re-renders this through the VT emulator. A
		// recorded prompt is free text the user pasted into, and content lifted
		// from a README, an issue or a terminal capture can carry OSC 52, which
		// several terminals honour as "set the system clipboard": re-reading an
		// old prompt would silently replace what the next paste types. Same
		// sanitizer, same reasoning as sessions.go's prompt panel; line breaks
		// survive because the shape of a prompt is most of its readability.
		// Tabs become spaces, which the editor needs anyway — it does not expand
		// them, so a literal tab drifts the columns its selection math assumes.
		mdl, cmd := m.openReadonlyText(label, claudesessions.SanitizePrompt(msg.Resp.Text))
		return mdl, tea.Batch(cmd, m.listenForMessages())

	case listenContinueMsg:
		// Inert: re-arms the IPC listen loop only.
		m.skipRender = !prologueChangedView
		return m, m.listenForMessages()

	default:
		// No case matched, so nothing in this switch touched the model and the
		// frame cannot have moved. This is reached by message types the TUI
		// does not handle — Bubble Tea's own internal windowSizeMsg among them,
		// which the 1 s size poll produces on every tick.
		//
		// Safe against the cases above that fall out of the switch instead of
		// returning: a type switch runs `default` only when NOTHING matched, so
		// those still reach the tail with skipRender false and render.
		m.skipRender = !prologueChangedView
	}

	return m, nil
}

func (m Model) handleNotificationKey(key string) (tea.Model, tea.Cmd) {
	action, eventID, paneID := m.notifications.HandleKey(key)
	switch action {
	case "navigate":
		// The sidebar carries events from every pane in every project, so the
		// jump must be able to cross a project boundary. History has to be
		// pushed BEFORE the jump (it records the location being left) but
		// only once the jump is known to land — checked first so a pane that
		// vanished between the event firing and the user pressing Enter
		// doesn't grow history for a jump that never happened.
		if pane, _, _ := m.findPaneAndTab(paneID); pane == nil {
			return m, nil
		}
		m.pushPaneHistory()
		_, overlayCmd := m.jumpToPane(paneID)
		if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() {
			tab.ToggleFocus()
		}
		m.sidebarFocused = false
		return m, overlayCmd
	case "dismiss":
		if eventID != "" {
			if msg, err := ipc.NewMessage(ipc.MsgDismissEvent, ipc.DismissEventPayload{EventID: eventID}); err == nil {
				if err := m.client.Send(msg); err != nil {
					log.Printf("dismiss event send: %v", err)
				}
			}
		}
		return m, nil
	case "dismiss_all":
		if msg, err := ipc.NewMessage(ipc.MsgDismissEvent, ipc.DismissEventPayload{}); err == nil {
			if err := m.client.Send(msg); err != nil {
				log.Printf("dismiss all send: %v", err)
			}
		}
		return m, nil
	case "unfocus":
		m.sidebarFocused = false
		return m, nil
	}
	return m, nil
}

func (m *Model) pushPaneHistory() {
	proj := m.cur()
	if proj == nil {
		return
	}
	if tab := m.activeTabModel(); tab != nil && tab.ActivePane != "" {
		ref := PaneRef{ProjectID: proj.ID, TabIndex: m.activeTabIdx(), PaneID: tab.ActivePane}
		m.paneHistory = append(m.paneHistory, ref)
		if len(m.paneHistory) > 20 {
			m.paneHistory = m.paneHistory[len(m.paneHistory)-20:]
		}
	}
}

// popPaneHistory restores the most recent history entry that is still valid,
// pop-and-skip past any entry whose project has since closed or whose pane
// has since been destroyed (degrades safely rather than jumping somewhere
// wrong). ProjectID is resolved to a project FIRST — a TabIndex recorded
// under a background project is meaningless against whichever project
// happens to be active now, which is exactly what made cross-project
// back-navigation unreachable before ProjectID existed. Once the recorded
// (project, tab, pane) triple is confirmed to still hold, the actual
// project/tab/pane move is delegated to jumpToPane so there is one
// implementation of that sequence.
func (m Model) popPaneHistory() (tea.Model, tea.Cmd) {
	for len(m.paneHistory) > 0 {
		ref := m.paneHistory[len(m.paneHistory)-1]
		m.paneHistory = m.paneHistory[:len(m.paneHistory)-1]
		for _, proj := range m.projects {
			if proj.ID != ref.ProjectID {
				continue
			}
			if ref.TabIndex < len(proj.tabs) {
				tab := proj.tabs[ref.TabIndex]
				if tab.Root != nil && tab.Root.PaneIDs()[ref.PaneID] {
					_, overlayCmd := m.jumpToPane(ref.PaneID)
					return m, overlayCmd
				}
			}
			break
		}
	}
	return m, nil
}

// paneAreaWidth returns the width available for pane content. The
// notification sidebar is a compositor overlay (overlayRight) — it does
// NOT reserve layout width, so panes never resize when it toggles. This
// constant width is what kills the sidebar-driven resize churn that made
// background claude panes repaint and garble their scrollback.
//
// The PROJECT sidebar is the opposite: it is a real reserved left column,
// so its width IS subtracted here — this is the single source of truth
// resizeTabs()/View() read, which is what keeps painted pane rects and PTY
// sizes in agreement when the sidebar toggles.
func (m Model) paneAreaWidth() int {
	return m.width - m.projectSidebarWidth()
}

// projectSidebarWidth returns the layout width reserved for the project
// sidebar: 0 when closed or the terminal is too narrow to spare it.
func (m Model) projectSidebarWidth() int {
	return sidebarWidth(m.width, m.sidebarOpen, m.sidebarWidth)
}

// sidebarContentHeight is how many screen rows the project sidebar spans:
// everything except the status bar. The TAB BAR row is included — the sidebar
// is a full-height left column and the tab bar sits inside the pane column
// beside it, so sidebar row k is screen row k (chromeHeight, which excludes
// both, is the PANE area's height and is the wrong budget here).
//
// renderSidebar, sidebarRowAt and activateSidebarRow all read this one
// value: sidebarVisibleRows caps against it and sidebarRowAt indexes the
// capped slice, so a height that differs between paint and hit test resolves
// clicks to a row the user never saw.
func (m Model) sidebarContentHeight() int {
	return m.height - 1
}

// pluginWideCanvas resolves the wide-canvas flag for a pane type via the
// plugin registry. Unknown types (registry miss, nil registry in tests)
// render 1:1.
func (m Model) pluginWideCanvas(paneType string) bool {
	if m.pluginRegistry == nil {
		return false
	}
	if p := m.pluginRegistry.Get(paneType); p != nil {
		return p.Display.WideCanvas
	}
	return false
}

// pluginMinNativeCols resolves the native-rendering column threshold for a
// pane type via the plugin registry. Unknown types (registry miss, nil
// registry in tests) return 0, which paneVTSize treats as the default (80).
func (m Model) pluginMinNativeCols(paneType string) int {
	if m.pluginRegistry == nil {
		return 0
	}
	if p := m.pluginRegistry.Get(paneType); p != nil {
		return p.Display.MinNativeCols
	}
	return 0
}

// sidebarOverlayWidth returns the drawn width of the notification sidebar
// overlay, or 0 when it isn't drawn (hidden, a dialog is open, or the
// terminal is too narrow). Unlike the old reservation logic there is no
// focus-mode suppression: visible ⇒ drawn, over whatever is beneath.
func (m Model) sidebarOverlayWidth() int {
	if !m.notifications.visible || m.dialog != dialogNone {
		return 0
	}
	// Against paneAreaWidth(), not m.width — the overlay is composited onto
	// tabContent, which the project sidebar has already taken its columns
	// out of (same correction notesPanelWidth carries). Measured against the
	// full terminal, a wide sidebar_width leaves this reporting a strip that
	// overlayRight then declines to paint (its overlayW >= totalW bail),
	// while sidebarSwallowsMouse goes on eating clicks in those columns.
	if m.paneAreaWidth()-m.notifications.width < minTermWidth {
		return 0
	}
	return m.notifications.width
}

// sidebarSwallowsMouse reports whether a mouse press/wheel at (x, y) lands
// on the sidebar overlay. Such events must not reach the pane rendered
// beneath it. Row 0 (tab bar) and the last row (status bar) are exempt;
// release/motion events are also exempt at the call sites so an in-flight
// drag can always terminate.
func (m Model) sidebarSwallowsMouse(x, y int) bool {
	sw := m.sidebarOverlayWidth()
	return sw > 0 && x >= m.width-sw && y >= 1 && y < m.height-1
}

// scrollbarHitPadding is how many cells on each side of the visible
// scrollbar column also register as a scrollbar click. The visual
// scrollbar stays 1 cell wide; the hit target is wider so a slightly off
// click jumps the thumb instead of starting a 1-column text selection.
// Trade-off: the rightmost `scrollbarHitPadding` content cells are no
// longer selectable by clicking — drag selection still covers them.
const scrollbarHitPadding = 1

// PaneRect ORIGIN CONTRACT (this block and every function below it):
// rects are SCREEN-ABSOLUTE. Their OX is seeded with projectSidebarWidth()
// rather than 0, because View() joins the project sidebar onto the LEFT of
// tabContent — with the sidebar open the pane area genuinely begins at
// screen column projectSidebarWidth(), not 0. Seeding the recursion is what
// makes every consumer correct for free: mouse coordinates arrive
// screen-absolute, so hit tests compare like with like, and a rect handed
// to the compositor (the context menu's anchor) is already in the frame's
// coordinate space. Only tab-INTERNAL walks stay 0-seeded — TabModel.View
// renders into a canvas whose own column 0 is the pane area's left edge,
// and NavigateDirection compares rects only against each other.
//
// activePaneRectFocus returns the rendered rect of the active pane when the
// active tab is in focus mode (notes mode implies focus mode), or nil when the
// tab is not in focus mode. The geometry mirrors View(): the active pane fills
// the area below the tab bar and left of the notes panel + notification
// sidebar (both reserve 0 width in plain focus mode, so the pane is
// full-width) — and right of the project sidebar, which DOES reserve width
// whenever it's open (paneAreaWidth), focus mode or not.
func (m *Model) activePaneRectFocus() *PaneRect {
	tab := m.activeTabModel()
	if tab == nil || !tab.FocusMode() {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}
	// The notification sidebar is an overlay (reserves 0 layout width); only
	// the notes panel narrows the pane area further, on top of the project
	// sidebar's own reservation.
	notesW := m.notesPanelWidth()
	return &PaneRect{
		Pane: pane,
		OX:   m.projectSidebarWidth(),
		OY:   1, // tab bar occupies row 0
		W:    m.paneAreaWidth() - notesW,
		H:    m.height - chromeHeight,
	}
}

// activePaneRect returns the rendered rect of the active pane in any layout
// mode (focus, notes, or split). Returns nil if there is no active pane.
func (m *Model) activePaneRect() *PaneRect {
	if r := m.activePaneRectFocus(); r != nil {
		return r
	}
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil {
		return nil
	}
	tabH := m.height - chromeHeight
	notesW := m.notesPanelWidth()
	var rects []PaneRect
	tab.Root.CollectRects(m.projectSidebarWidth(), 1, m.paneAreaWidth()-notesW, tabH, &rects)
	for i := range rects {
		if rects[i].Pane != nil && rects[i].Pane.ID == tab.ActivePane {
			return &rects[i]
		}
	}
	return nil
}

// paneRectAt returns the rendered pane rect containing screen coordinate
// (x, y) in the active tab, or nil. Focus mode resolves to the single
// full-area rect; split layouts walk the same CollectRects geometry the
// scrollbar and border hit-tests use.
func (m *Model) paneRectAt(x, y int) *PaneRect {
	if r := m.activePaneRectFocus(); r != nil {
		if x >= r.OX && x < r.OX+r.W && y >= r.OY && y < r.OY+r.H {
			return r
		}
		return nil
	}
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil {
		return nil
	}
	tabH := m.height - chromeHeight
	notesW := m.notesPanelWidth()
	var rects []PaneRect
	tab.Root.CollectRects(m.projectSidebarWidth(), 1, m.paneAreaWidth()-notesW, tabH, &rects)
	for i := range rects {
		r := &rects[i]
		if r.Pane != nil && x >= r.OX && x < r.OX+r.W && y >= r.OY && y < r.OY+r.H {
			return r
		}
	}
	return nil
}

// hitTestScrollbar returns the pane rect under (x, y) when the click hits
// the pane's scrollbar zone. The visible scrollbar lives at
// `rect.OX + rect.W - 2` (just inside the right border); the hit zone
// extends `scrollbarHitPadding` cells to either side so the target is
// 1 + 2*padding cells wide. The valid Y range is the content area
// (rows `rect.OY + 1` through `rect.OY + rect.H - 2` inclusive).
func (m *Model) hitTestScrollbar(x, y int) *PaneRect {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	// Resolve the rect using the SAME width View() lays the pane area out with
	// (m.width - sidebarW - notesW). paneAreaWidth() omits the notes-panel
	// width, so in notes mode the scrollbar column would be computed too far
	// right and every click would miss. Focus mode renders only the active pane
	// full-area and never resizes the split tree, so use its rendered rect.
	var rect *PaneRect
	if r := m.activePaneRectFocus(); r != nil {
		rect = r
	} else if tab.Root != nil {
		tabH := m.height - chromeHeight
		notesW := m.notesPanelWidth()
		rect = tab.Root.FindPaneRectAt(x, y, m.projectSidebarWidth(), 1, m.paneAreaWidth()-notesW, tabH)
	}
	if rect == nil {
		return nil
	}
	if rect.W < 4 || rect.H < 4 {
		// Pane too small to render a meaningful scrollbar.
		return nil
	}
	scrollbarX := rect.OX + rect.W - 2
	contentTopY := rect.OY + 1
	contentBottomY := rect.OY + rect.H - 2
	if x < scrollbarX-scrollbarHitPadding || x > scrollbarX+scrollbarHitPadding {
		return nil
	}
	if y < contentTopY || y > contentBottomY {
		return nil
	}
	return rect
}

// clearDragState resets every mutually-exclusive drag flag in one place.
//
// Invariant: at most one drag is active at any time — tab reorder, pane
// scrollbar, notes editor selection, and pane text selection cannot
// coexist because each is started by a different (Y, X) region of a
// MouseClickMsg. Routing every "start a new drag" / "drag ended" path
// through this helper keeps the invariant enforced in one place rather
// than spread across each click handler that has to remember to zero its
// siblings.
func (m *Model) clearDragState() {
	if m.splitDragNode != nil {
		m.setSplitDragHighlight(&m.splitDragRect, false)
	}
	m.tabDragFromIdx = -1
	m.scrollDragPaneID = ""
	m.scrollDragRect = PaneRect{}
	m.mouseDown = false
	m.notesMouseDown = false
	m.viewerMouseDown = false
	m.splitDragNode = nil
	m.splitDragRect = BorderHit{}
	m.sidebarDragging = false
	m.sidebarDragW = 0
}

// beginSidebarDrag arms an edge drag, seeding the pending width from the
// current one so a click with no motion commits no change.
func (m *Model) beginSidebarDrag() {
	m.clearDragState()
	m.sidebarDragging = true
	m.sidebarDragW = m.projectSidebarWidth()
	m.selection = nil
}

// trackSidebarDrag moves the pending width to follow the cursor. Column x
// becomes the sidebar's LAST column, so the width is x+1.
//
// Clamped through sidebarWidth() rather than a second min/max pair: that
// function is the single source of truth for how much screen the strip may
// take, and a private clamp here could land on a width the renderer would
// silently correct — the sidebar would then not stop where the user let go.
// The minSidebarWidth floor is applied FIRST so it is what sidebarWidth sees;
// applying it after would let the clamp's own result fall back below it.
func (m *Model) trackSidebarDrag(x int) {
	if !m.sidebarDragging {
		return
	}
	w := x + 1
	if w < minSidebarWidth {
		w = minSidebarWidth
	}
	m.sidebarDragW = sidebarWidth(m.width, m.sidebarOpen, w)
}

// finishSidebarDrag commits the pending width. This is the single point at
// which the layout actually moves.
//
// The sequence matches toggleProjectSidebar and is not optional: resizeTabs
// runs FIRST because it is what WRITES pane.Width/Height and tab.CanvasW/H —
// resizeAllPanes only reads and ships them, so without it every background tab
// keeps its pre-drag PTY size. ClearScreen because every column right of the
// strip shifts in one frame, which is the shift Bubble Tea's cell diff
// mis-tracks.
func (m *Model) finishSidebarDrag() tea.Cmd {
	if !m.sidebarDragging {
		return nil
	}
	w := m.sidebarDragW
	m.sidebarDragging = false
	m.sidebarDragW = 0
	if w <= 0 || w == m.sidebarWidth {
		return nil
	}
	m.sidebarWidth = w
	// A screen preference, not session state: persisted to config (saved on
	// exit via ConfigChanged), never to workspace.json.
	m.cfg.UI.SidebarWidth = w
	m.configChanged = true
	m.resizeTabs()
	return tea.Batch(tea.ClearScreen, m.resizeAllPanes())
}

// hitTestSplitBorder returns the deepest split line containing (x, y), or
// nil. Disabled in focus mode (one full-area pane — no inner borders),
// notes mode (implies focus mode plus a squeezed layout), and on tabs
// without splits (a leaf root emits no borders). Runs AFTER
// hitTestScrollbar at the call site so the scrollbar keeps priority where
// its widened hit zone overlaps a pane's right border.
func (m *Model) hitTestSplitBorder(x, y int) *BorderHit {
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil || tab.FocusMode() || m.notesMode {
		return nil
	}
	tabH := m.height - chromeHeight
	var borders []BorderHit
	tab.Root.CollectBorders(m.projectSidebarWidth(), 1, m.paneAreaWidth(), tabH, &borders)
	for i := len(borders) - 1; i >= 0; i-- {
		if borders[i].Contains(x, y) {
			return &borders[i]
		}
	}
	return nil
}

// setSplitDragHighlight toggles the transient drag highlight on every leaf
// whose rect touches the dragged split line, on both sides of it.
// Adjacency is topological (which leaves border the line never changes as
// the ratio moves), so the same recomputation clears exactly the set it
// set — even after the drag moved the boundary.
func (m *Model) setSplitDragHighlight(hit *BorderHit, on bool) {
	if hit == nil || hit.Node == nil || hit.Node.IsLeaf() {
		return
	}
	bd := hit.boundary()
	var rects []PaneRect
	hit.Node.CollectRects(hit.OX, hit.OY, hit.W, hit.H, &rects)
	for i := range rects {
		if rects[i].Pane == nil {
			continue
		}
		var touches bool
		if hit.Node.Split == SplitHorizontal {
			touches = rects[i].OX == bd || rects[i].OX+rects[i].W == bd
		} else {
			touches = rects[i].OY == bd || rects[i].OY+rects[i].H == bd
		}
		if touches {
			rects[i].Pane.splitDragHighlight = on
		}
	}
}

// treeContains reports whether target is reachable from n (pointer
// identity). Guards a drag whose node was pruned or replaced by a
// workspace_state reconciliation mid-drag.
func treeContains(n, target *LayoutNode) bool {
	if n == nil {
		return false
	}
	if n == target {
		return true
	}
	return treeContains(n.Left, target) || treeContains(n.Right, target)
}

// dragSplitBorder maps the cursor to a new Ratio on the dragged node,
// clamped so every leaf in BOTH subtrees keeps its minimum size (nested
// splits included, via minWidth/minHeight). Clamping happens in cells and
// the ratio is derived from the clamped cell count — exact at the
// extremes, no float-truncation flicker. Local-only: the PTY resize and
// layout persistence fire on release (finishSplitDrag).
func (m *Model) dragSplitBorder(x, y int) {
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil || m.splitDragNode == nil ||
		!treeContains(tab.Root, m.splitDragNode) {
		m.clearDragState()
		return
	}
	node, rect := m.splitDragNode, m.splitDragRect
	switch node.Split {
	case SplitHorizontal:
		if rect.W <= 0 {
			return
		}
		leftW := min(max(x-rect.OX, node.Left.minWidth()), rect.W-node.Right.minWidth())
		node.Ratio = float64(leftW) / float64(rect.W)
	case SplitVertical:
		if rect.H <= 0 {
			return
		}
		topH := min(max(y-rect.OY, node.Left.minHeight()), rect.H-node.Right.minHeight())
		node.Ratio = float64(topH) / float64(rect.H)
	}
	// Rects only — the VT emulator must NOT resize mid-drag (see
	// resizeNodeRects). The full tab.Resize runs once in finishSplitDrag.
	resizeNodeRects(tab.Root, tab.Width, tab.Height)
}

// finishSplitDrag commits an in-progress border drag: the daemon gets the
// final pane sizes (one PTY resize per pane — children reflow once, per
// the on-release-only design) and every tab's layout blob (persists the
// new Ratio). resizeAllPanes/sendAllLayouts cover all panes/tabs; the
// daemon's same-size guard drops the untouched panes' resizes, and layout
// updates are stored opaquely without broadcast, so the extra breadth is
// harmless and reuses tested plumbing.
func (m *Model) finishSplitDrag() tea.Cmd {
	// The one VT resize of the whole drag: old size → final size, paired
	// with the PTY resize below so the child's SIGWINCH redraw lands in a
	// matching grid (mid-drag only rects moved — see resizeNodeRects).
	if tab := m.activeTabModel(); tab != nil {
		tab.Resize(tab.Width, tab.Height)
	}
	m.clearDragState()
	return tea.Batch(m.resizeAllPanes(), m.sendAllLayouts())
}

// moveTab repositions the active project's tab at `from` to ordinal `to`,
// sliding the tabs
// between them by one position. Other multiplexers and every browser tab
// strip use this UX — a swap would teleport the displaced tab to the
// dragged tab's old slot, which feels wrong when dragging across several
// positions. The active tab follows the dragged tab.
//
// Returns true when the order actually changed.
func (m *Model) moveTab(from, to int) bool {
	tabs := m.curTabs()
	if from == to || from < 0 || to < 0 || from >= len(tabs) || to >= len(tabs) {
		return false
	}
	tab := tabs[from]
	if from < to {
		copy(tabs[from:to], tabs[from+1:to+1])
	} else {
		copy(tabs[to+1:from+1], tabs[to:from])
	}
	tabs[to] = tab
	// activeTab tracks position, not identity — adjust to the dragged
	// tab's new ordinal so the visual selection follows it.
	m.setActiveTabIdx(to)
	return true
}

// activePaneByID returns the pane with the given ID from the active tab,
// or nil if no such pane exists. Used to look up the drag target across
// MouseMotion / MouseRelease events. The active tab may change between
// the click and a motion event (e.g. the user pressed Alt+2 mid-drag);
// in that case the drag is silently dropped on the next motion.
func (m *Model) activePaneByID(id string) *PaneModel {
	tab := m.activeTabModel()
	if tab == nil || tab.Root == nil {
		return nil
	}
	for _, p := range tab.Leaves() {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// spinnerTargetPane resolves the pane a restore-spinner tick names, across
// every tab and INCLUDING each tab's overlay pane.
//
// The overlay half is the whole reason this exists. Overlay panes are
// deliberately outside the layout tree, so the tick handler's old walk of
// tab.Root found nothing for lazygit or hunk: the chain stopped on its first
// message, and a boot indicator on one of those panes would have rendered a
// single frozen frame. The start site (see the newPaneIDs loop) has always
// armed a tick for them.
func (m *Model) spinnerTargetPane(id string) *PaneModel {
	for _, tab := range m.allTabs() {
		if tab.overlayPane != nil && tab.overlayPane.ID == id {
			return tab.overlayPane
		}
		if tab.Root == nil {
			continue
		}
		if leaf := tab.Root.FindLeaf(id); leaf != nil {
			return leaf.Pane
		}
	}
	return nil
}

// sidebarTick schedules the next relative-timestamp refresh for the
// notification sidebar.
func (m Model) sidebarTick() tea.Cmd {
	return tea.Tick(10*time.Second, func(_ time.Time) tea.Msg {
		return sidebarTickMsg{}
	})
}

// notesTick schedules a debounce check while notes mode is active.
func (m Model) notesTick() tea.Cmd {
	return tea.Tick(notesTickInterval, func(_ time.Time) tea.Msg {
		return notesTickMsg{}
	})
}

// startSidebarTick schedules the sidebar refresh chain unless one is already
// in flight. Mirrors workTickRunning: the chain self-perpetuates inside the
// sidebarTickMsg handler, so unguarded scheduling stacks immortal chains.
func (m *Model) startSidebarTick() tea.Cmd {
	if m.sidebarTickRunning {
		return nil
	}
	m.sidebarTickRunning = true
	return m.sidebarTick()
}

// startNotesTick schedules the notes auto-save debounce chain unless one is
// already in flight. Same immortal-chain guard as startSidebarTick.
func (m *Model) startNotesTick() tea.Cmd {
	if m.notesTickRunning {
		return nil
	}
	m.notesTickRunning = true
	return m.notesTick()
}

// toggleNotesMode opens the notes editor for the active pane, or closes
// (and flushes) it if notes mode is already active.
//
// Opening notes auto-enters focus mode for the tab so the user only sees the
// bound pane next to the editor — sibling panes are hidden but keep running.
// If the user was already in focus mode, the existing focus state is left
// alone. Tab/Shift+Tab cycles keyboard focus between the editor and the pane
// while notes mode is active.
func (m Model) toggleNotesMode() (tea.Model, tea.Cmd) {
	if m.notesMode && m.notesEditor != nil {
		return m.exitNotesMode()
	}
	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}
	// Initial dimensions are placeholders — View() will Resize the editor
	// to fit the actual notes panel area on the next render pass.
	editor, err := NewNotesEditor(config.NotesDir(), pane.ID, pane.Name, 1, 1)
	if err != nil {
		log.Printf("open notes: %v", err)
		return m, nil
	}
	// Auto-enter focus mode so the bound pane fills the available area to
	// the left of the editor. Track that we were the ones to do so, so
	// exiting notes reverts focus only when we owned the toggle.
	enteredFocus := false
	if !tab.FocusMode() {
		tab.ToggleFocus()
		enteredFocus = tab.FocusMode() // ToggleFocus is a no-op on single-pane tabs
	}
	m.notesMode = true
	m.notesEditor = editor
	m.notesEnteredFocus = enteredFocus
	m.notesPaneFocused = false // editor starts focused so the user can immediately type
	return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes(), m.startNotesTick())
}

// openClosePaneConfirm opens the close-pane confirm dialog for the active
// pane. Extracted from the kb.ClosePane case; shared with the context menu.
func (m Model) openClosePaneConfirm() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			m.dialog = dialogConfirm
			m.confirmKind = "pane"
			m.confirmID = pane.ID
			m.confirmName = paneDisplayName(pane)
			// Re-collected on every open rather than carried, and the toggle is
			// re-cleared: the previous dialog's answer describes a different
			// pane, and an inherited arm is a deletion nobody was offered.
			m.resetConfirmWorktrees()
			m.confirmWorktrees = collectConfirmWorktrees([]*PaneModel{pane})
			if cmd := m.requestWorktreeStatus(m.destOfPane(pane.ID)); cmd != nil {
				return m, tea.Batch(tea.ClearScreen, cmd)
			}
		}
	}
	return m, tea.ClearScreen
}

// openRestartPaneConfirm opens the restart confirm dialog for the active
// pane. Extracted from the kb.RestartPane case; shared with the context menu.
func (m Model) openRestartPaneConfirm() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			m.dialog = dialogConfirm
			m.confirmKind = confirmKindRestartPane
			m.confirmID = pane.ID
			m.confirmName = paneDisplayName(pane)
			// Cleared HERE as well as on every exit. The exits do cover it
			// today — both terminal branches of handleConfirmKey reset — but
			// that makes the invariant global, and "instance" already renders
			// through renderConfirmDialog's default arm, so one future exit
			// that skips the reset would paint worktree rows on a confirm that
			// has nothing to do with worktrees. Clearing at each opener makes
			// it local.
			m.resetConfirmWorktrees()
		}
	}
	return m, tea.ClearScreen
}

// beginPaneRename enters inline pane-rename mode for the active pane.
// Extracted from the kb.RenamePane case; shared with the context menu.
func (m Model) beginPaneRename() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			m.renamingPane = true
			m.paneRenameInput = pane.Name
		}
	}
	return m, nil
}

// toggleProjectSidebar shows or hides the reserved project column. Extracted
// from the kb.SidebarToggle case so the command palette dispatches into the
// same implementation the key does — the palette is a launcher, not a second
// code path, and this one has enough ordering to it that a copy would drift.
func (m Model) toggleProjectSidebar() (tea.Model, tea.Cmd) {
	// Refused below minWidthForSidebar rather than flipped invisibly:
	// sidebarWidth() returns 0 on a narrow terminal whatever sidebarOpen
	// says, so the toggle would repaint nothing while still writing
	// cfg.UI.SidebarOpen to disk — the user's next launch on a wide
	// terminal would then come up in whichever state the narrow one
	// happened to leave behind. Flash instead, so the key is not silent.
	if m.width < minWidthForSidebar {
		m.setFlash(fmt.Sprintf("terminal too narrow for the project sidebar (needs %d columns)", minWidthForSidebar))
		return m, m.flashCmd()
	}
	// The PROJECT sidebar reserves real layout width (paneAreaWidth), so
	// unlike the notification overlay this has to resize every pane's PTY —
	// and ClearScreen, because every column right of the strip shifts by its
	// width in one frame, which is exactly the kind of shift Bubble Tea's cell
	// diff mis-tracks.
	m.sidebarOpen = !m.sidebarOpen
	// resizeTabs FIRST, and it is not optional: resizeAllPanes does not
	// compute geometry, it READS pane.Width/Height and tab.CanvasW/H and
	// ships them. Those are written only by tab.Resize — i.e. by
	// resizeTabs (every tab of every project) or by View (the active tab
	// only). The toggle changes paneAreaWidth() for all of them, so
	// without this every background tab keeps its pre-toggle PTY size
	// until the next workspace broadcast or real window resize, and even
	// the active tab is a race between View and this Cmd's goroutine that
	// the daemon's same-size guard can settle the wrong way. Same
	// ordering as resizeTickMsg and toggleFocusForActiveTab.
	m.resizeTabs()
	// A screen preference, not session state: persisted to config (saved
	// on exit via ConfigChanged), never to workspace.json.
	m.cfg.UI.SidebarOpen = m.sidebarOpen
	m.configChanged = true
	return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
}

// toggleFocusForActiveTab toggles focus mode on the active tab. Extracted
// from the kb.FocusPane case; shared with the context menu.
func (m Model) toggleFocusForActiveTab() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil && tab.Root != nil {
		tab.ToggleFocus()
		m.resizeTabs()
		return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
	}
	return m, nil
}

// openHistoryForActivePane opens the input-history modal for the active
// pane, gated on the plugin's record_history opt-in. Extracted from the
// kb.CommandHistory case; shared with the context menu.
func (m Model) openHistoryForActivePane() (tea.Model, tea.Cmd) {
	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}
	supported := false
	if m.pluginRegistry != nil {
		if p := m.pluginRegistry.Get(pane.Type); p != nil {
			supported = p.Command.RecordHistory
		}
	}
	m = m.openHistoryDialog(pane.ID, pane.Type, supported)
	if supported {
		return m, m.requestHistory(pane.ID)
	}
	return m, nil
}

// openCloseTabConfirm opens the close-tab confirm for the active tab. Extracted
// from the kb.CloseTab case; shared with the command palette.
func (m Model) openCloseTabConfirm() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		m.dialog = dialogConfirm
		m.confirmKind = "tab"
		m.confirmID = tab.ID
		m.confirmName = tab.Name
		// EVERY owned worktree in the tab, not the active pane's: a tab closes
		// as a unit, and one toggle covers the set it takes with it.
		m.resetConfirmWorktrees()
		m.confirmWorktrees = collectConfirmWorktrees(tab.Leaves())
		if cmd := m.requestWorktreeStatus(m.destOfTab(tab.ID)); cmd != nil {
			return m, tea.Batch(tea.ClearScreen, cmd)
		}
	}
	return m, tea.ClearScreen
}

// beginTabRename enters inline tab-rename mode for the active tab. Extracted
// from the kb.RenameTab case; shared with the command palette.
func (m Model) beginTabRename() (tea.Model, tea.Cmd) {
	if tab := m.activeTabModel(); tab != nil {
		m.renaming = true
		m.renameInput = tab.Name
	}
	return m, nil
}

// paneTarget says where the pane the create-pane dialog is building will go.
//
// The dialog's first three steps (category → plugin → instance → setup) are
// identical either way; only the last step and the message sent differ. The
// zero value is the Ctrl+N behaviour that predates the new-tab flow, so any
// path that forgets to set this splits the current tab rather than creating a
// tab nobody asked for.
type paneTarget int

const (
	paneTargetSplit  paneTarget = iota // Ctrl+N: into the active tab
	paneTargetNewTab                   // Ctrl+T: as the first pane of a new tab
)

// openCreatePaneDialog opens the create-pane dialog at step 0 (the Ctrl+N flow).
// Extracted from the `key == "ctrl+n"` case; shared with the command palette.
func (m Model) openCreatePaneDialog() (tea.Model, tea.Cmd) {
	return m.openCreatePaneDialogFor(paneTargetSplit)
}

// openCreatePaneDialogFor is the shared opener for both targets.
//
// createPaneTarget is reset HERE rather than on each close path, and that is
// load-bearing: the step-0 escape, the instance-delete detour into the confirm
// dialog, and handleCreatePaneSplit's three early refusals all leave the dialog
// without reaching its teardown block. A target that outlived any one of those
// would make the next plain Ctrl+N create a TAB instead of a split — the fourth
// recurrence of the stale-dialog-state class this file already documents three
// of, and the reason this is a parameter rather than a field somebody sets
// afterwards.
func (m Model) openCreatePaneDialogFor(target paneTarget) (tea.Model, tea.Cmd) {
	m.dialog = dialogCreatePane
	m.dialogCursor = 0
	m.createPaneStep = 0
	m.selectedCategory = 0
	m.createPaneTarget = target
	m.createPaneDest = m.pinnableDest()
	return m, tea.ClearScreen
}

// pinnableDest is the destination a dialog opened NOW should submit to, or ""
// meaning "not known yet — let the router resolve it at send time".
//
// The empty answer is not a fallback, it is the two documented startup windows,
// and it must not be confused with `activeDest() == ""`:
//
//   - m.cur() == nil is the pre-first-broadcast window every session passes
//     through. Under --remote the router is keyed by the REMOTE host and holds
//     no "" conn at all, so pinning "" would aim the send at a destination that
//     does not exist.
//   - onlyOfflineProjects() is that same window once offline stand-ins have been
//     seeded: every known project is a placeholder for some OTHER host that
//     failed to dial, so m.cur() names an unreachable machine while the daemon
//     the send actually resolves to is fine.
//
// In both, Router.Send's sole-conn fallback is the thing that delivers — and
// that fallback is gated on the message being UNSTAMPED, so the send must skip
// the stamp entirely rather than stamp a best guess. See sendCreateTab.
func (m Model) pinnableDest() string {
	if m.cur() == nil || m.onlyOfflineProjects() {
		return ""
	}
	return m.activeDest()
}

// handleNewTab opens the create-pane dialog to choose the new tab's first pane.
//
// Ctrl+T used to send create_tab immediately, which always produced a shell.
// The daemon still does exactly that for a create carrying no spec, so the
// escape path and every other client keep the old behaviour.
//
// The reachability refusal moved here from the keypress: the send is what
// Router.Send drops silently, and letting the user fill in a directory, a
// branch and a session first — then dropping it — is worse than refusing the
// keystroke. Both carve-outs come with it, and both encode shipped bugs: a NIL
// m.cur() is the pre-first-broadcast window every session passes through (the
// unstamped send resolves through Router.Send's sole-conn fallback there), and
// onlyOfflineProjects is the same window once offline rows have been seeded for
// some OTHER destination while the local daemon is fine.
func (m Model) handleNewTab() (tea.Model, tea.Cmd) {
	if p := m.cur(); p != nil && !m.projectActionable(p) && !m.onlyOfflineProjects() {
		m.setFlash("cannot reach " + hostLabel(p.Dest) + " — new tab not created")
		return m, m.flashCmd()
	}
	return m.openCreatePaneDialogFor(paneTargetNewTab)
}

// createTerminalTab sends the bare create_tab every producer sent before the
// first-pane spec existed: no FirstPane, so the daemon spawns its terminal.
//
// This is the Esc fallback, and it is why cancelling the picker still leaves a
// usable tab rather than nothing at all.
func (m Model) createTerminalTab() tea.Cmd {
	return m.sendCreateTab(nil)
}

// sendCreateTab is the ONE create_tab producer.
//
// A KNOWN destination is aimed with sendForDestStrict rather than the unstamped
// m.client.Send the old createTab used: that send was safe only because the
// keypress and the send were the same instant, and a dialog can stay open while
// the active project moves under it (see createPaneDest). Strict because
// Router.Send drops an unroutable message and returns nil, so the loose form
// cannot tell the user their tab was never created.
//
// An EMPTY destination is deliberately sent UNSTAMPED, and that asymmetry is
// load-bearing rather than a shortcut. Empty means one of the two startup
// windows pinnableDest documents, where the router must pick the destination
// itself — and its sole-conn fallback is gated on `!stamped`, so stamping ""
// (which stampDest maps to destLocal) makes that fallback unreachable and the
// send is dropped against a "" conn that, under --remote, never existed. Both
// windows regressed exactly that way when this function stamped unconditionally.
func (m Model) sendCreateTab(spec *ipc.FirstPaneSpec) tea.Cmd {
	dest := m.createPaneDest
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{
			Name:      "New Tab",
			FirstPane: spec,
		})
		if err != nil {
			log.Printf("create tab: build message: %v", err)
			return nil
		}
		if dest == "" {
			// No pre-flight check to make: the router resolves this one, and a
			// drop there is already logged. Reporting "cannot reach" about a
			// destination nobody named is the bug this branch exists to avoid.
			if err := m.client.Send(msg); err != nil {
				log.Printf("create tab: send: %v", err)
			}
			return nil
		}
		if err := m.sendForDestStrict(dest, msg); err != nil {
			return createTabFailedMsg{dest: dest}
		}
		return nil
	}
}

// createTabFailedMsg reports a create_tab that never reached its daemon. The
// send happens off the Update goroutine, so the flash cannot be set there.
type createTabFailedMsg struct{ dest string }

// forceRedraw is the full-repaint recovery hatch: drop every pane's render
// cache and every tab's leaves cache, then ClearScreen + re-probe the terminal
// size. Extracted verbatim from the kb.Redraw case; shared with the command
// palette. It mutates tab state, so it cannot be a bare func() tea.Cmd.
func (m Model) forceRedraw() (tea.Model, tea.Cmd) {
	for _, tab := range m.allTabs() {
		tab.invalidateLeaves()
		if tab.Root != nil {
			for _, pane := range tab.Leaves() {
				pane.invalidateRenderCache()
			}
		}
	}
	return m, tea.Batch(tea.ClearScreen, sizePollProbe)
}

// notesEditorBox computes the screen bounding box of the bordered notes
// notesPanelWidthNumerator / Denominator set the default notes-panel
// width as a fraction of the available tab area (numerator/denominator).
// The 2/5 ratio gives the pane the dominant share while leaving a
// comfortable editor panel on the right.
const (
	notesPanelWidthNumerator   = 2
	notesPanelWidthDenominator = 5
	notesPanelMinWidth         = 30 // minimum editor width, in columns
)

// notesPanelWidth returns the notes panel width for the current model
// state. Returns 0 when notes mode is inactive or the terminal is too
// narrow to render the editor. The notification sidebar is an overlay and
// no longer reserves width here. Single source of truth for the layout
// math used by both View() and notesEditorBox.
//
// The fraction (and its collapse guard) is taken of paneAreaWidth(), not raw
// m.width: the project sidebar has already claimed its columns by the time
// notes squeezes further, so basing the split on the full terminal width
// hands the notes panel a share of space the panes never actually had.
// Concretely, at total=100/sidebar=22 (paneAreaWidth=78), a raw-m.width split
// gave notes 40 and panes 38 — BELOW minTermWidth=40, undetected because the
// guard also checked against raw m.width (100-40=60, comfortably clear) — so
// the one guard whose entire job is preventing an unusably narrow pane region
// missed it. Splitting paneAreaWidth() gives notes 31 and panes 47, and the
// guard now protects the width panes actually get.
func (m Model) notesPanelWidth() int {
	if !m.notesMode || m.notesEditor == nil {
		return 0
	}
	avail := m.paneAreaWidth()
	notesW := avail * notesPanelWidthNumerator / notesPanelWidthDenominator
	if notesW < notesPanelMinWidth {
		notesW = notesPanelMinWidth
	}
	if avail-notesW < minTermWidth {
		return 0
	}
	return notesW
}

// notesEditorBox returns the outer screen box (x0/y0 inclusive, x1/y1
// exclusive) of the notes editor. Returns ok=false when notes mode is
// inactive or the terminal is too narrow to render the editor.
func (m Model) notesEditorBox() (boxX0, boxY0, boxX1, boxY1 int, ok bool) {
	if !m.notesMode || m.notesEditor == nil || m.activeTabModel() == nil {
		return 0, 0, 0, 0, false
	}
	notesW := m.notesPanelWidth()
	if notesW == 0 {
		return 0, 0, 0, 0, false
	}
	// m.width - notesW is still the right edge-anchored formula even with the
	// project sidebar open: View() joins [sidebar][panes][notes] left to
	// right, panes get exactly paneAreaWidth()-notesW, and
	// projectSidebarWidth()+paneAreaWidth() == m.width by construction — so
	// the notes box's screen-absolute left edge reduces to m.width-notesW
	// regardless of how much the sidebar reserved.
	boxX0 = m.width - notesW
	boxY0 = 1 // y=0 is the tab bar
	boxX1 = m.width
	boxY1 = m.height - 1 // last row is the status bar
	return boxX0, boxY0, boxX1, boxY1, true
}

// notesEditorPosAt converts screen (x, y) to a (row, col) document position
// in the notes editor, accounting for the bordered box, header/footer rows,
// the line number gutter, and the editor's current scroll offset.
//
// Returns ok=false when the screen point is outside the editor's outer box.
// Points inside the box but on the border / header / footer / gutter are
// clamped to the nearest body cell so a drag into the gutter still selects
// the first column of the relevant row.
func (m Model) notesEditorPosAt(screenX, screenY int) (row, col int, ok bool) {
	boxX0, boxY0, boxX1, boxY1, exists := m.notesEditorBox()
	if !exists {
		return 0, 0, false
	}
	if screenX < boxX0 || screenX >= boxX1 || screenY < boxY0 || screenY >= boxY1 {
		return 0, 0, false
	}
	// Body area: strip 1 char border on each side, 1 row of header at the
	// top (after the top border), and 1 row of footer at the bottom (before
	// the bottom border). The line number gutter width is dynamic — for
	// documents with >99 lines the gutter grows, so we query the editor
	// for its current value rather than hardcoding 4.
	lineNumWidth := m.notesEditor.editor.GutterWidth()
	bodyX0 := boxX0 + 1 + lineNumWidth
	bodyY0 := boxY0 + 2 // top border + header line
	bodyX1 := boxX1 - 1
	bodyY1 := boxY1 - 2 // bottom border + footer line
	if bodyX1 <= bodyX0 || bodyY1 <= bodyY0 {
		return 0, 0, false
	}

	// Clamp gutter / border / header / footer clicks into the body so a
	// drag into those zones still resolves to a sensible cell.
	if screenX < bodyX0 {
		screenX = bodyX0
	} else if screenX >= bodyX1 {
		screenX = bodyX1 - 1
	}
	if screenY < bodyY0 {
		screenY = bodyY0
	} else if screenY >= bodyY1 {
		screenY = bodyY1 - 1
	}

	ed := m.notesEditor.editor
	vrow := ed.ScrollTop + (screenY - bodyY0)
	vcol := screenX - bodyX0
	if ed.SoftWrap {
		// The editor is scrolled in visual-row space; translate the
		// visual (row, col) back to the underlying logical position
		// before returning to the caller, which expects logical
		// coordinates for selection and cursor updates.
		layout := ed.visualLayout(ed.contentWForLayout())
		row, col = ed.visualToLogical(layout, vrow, vcol)
		return row, col, true
	}
	return vrow, vcol, true
}

// logViewerPosAt converts screen (x, y) to a logical (row, col) position in the
// full-screen read-only viewer (dialogLogViewer), accounting for the title bar,
// the status bar, the line-number gutter, and the editor's scroll offset.
//
// Geometry mirrors renderTOMLEditorFullScreen exactly: row 0 is the title bar,
// the last row is the status bar, and everything between is editor body whose
// first GutterWidth() columns are line numbers. Returns ok=false outside the
// body; inside it, gutter columns clamp to column 0 so a drag that wanders left
// still selects from the start of the line.
func (m Model) logViewerPosAt(screenX, screenY int) (row, col int, ok bool) {
	e := m.tomlEditor
	if e == nil {
		return 0, 0, false
	}
	const bodyY0 = 1 // title bar
	bodyY1 := m.height - 1
	if bodyY1 <= bodyY0 || screenY < bodyY0 || screenY >= bodyY1 {
		return 0, 0, false
	}
	bodyX0 := e.GutterWidth()
	if screenX < bodyX0 {
		screenX = bodyX0
	}
	vrow := e.ScrollTop + (screenY - bodyY0)
	vcol := screenX - bodyX0
	if e.SoftWrap {
		// ScrollTop is a visual-row index while wrapping; translate back to the
		// logical position the selection API expects.
		layout := e.visualLayout(e.contentWForLayout())
		row, col = e.visualToLogical(layout, vrow, vcol)
	} else {
		row, col = vrow, vcol
	}
	// Both branches return a position that is valid to index Lines with. The
	// SoftWrap branch is already clamped by visualToLogical; the other is not —
	// a click below the last line yields a row past the end. Today's callers
	// clamp again, so nothing is out of range, but leaving the two branches with
	// different contracts hands the next caller a panic for free.
	row, col = e.clampPos(row, col)
	return row, col, true
}

// rawKeyFor is tryPluginRawKey, suppressed when a sequence just completed.
//
// A pending sequence outranks a plugin's raw_keys claim. This is the one place
// the design deliberately changes the precedence the Tier split encodes, and it
// is scoped to multi-step bindings: a single chord never sets seqAction, so no
// existing binding's relationship with raw_keys moves. Without it,
// `pane.close = "ctrl+b x"` is dead on any pane whose plugin claims x.
func (m Model) rawKeyFor(seqAction keymap.ActionID, key string, msg tea.KeyPressMsg) []byte {
	if seqAction != "" {
		return nil
	}
	return m.tryPluginRawKey(key, msg)
}

// structuralActions destroy or restructure the pane a notes editor is bound
// to, so notes must be flushed and torn down before one runs. Named once here
// because two call sites need the same list: the notes block in handleKey, and
// the completed-sequence path that bypasses it.
var structuralActions = map[keymap.ActionID]bool{
	"pane.close": true, "tab.close": true,
	"pane.split_h": true, "pane.split_v": true,
}

func isStructuralAction(id keymap.ActionID) bool { return structuralActions[id] }

// armedPaneID is the pane a pending sequence was started in. A completed
// sequence must act on that pane, not on whichever one happens to be active
// two keystrokes later.
func (m Model) armedPaneID() string {
	if tab := m.activeTabModel(); tab != nil {
		return tab.ActivePane
	}
	return ""
}

// cancelSequence clears any pending prefix sequence. Bumping the generation is
// what stops a cancelled sequence's in-flight timeout tick from clearing a
// sequence started after it.
func (m *Model) cancelSequence() {
	if len(m.pendingSeq) == 0 {
		return
	}
	m.pendingSeq = nil
	m.pendingGen++
}

// sequenceTimeoutMsg drops a pending sequence. gen pins it to the sequence that
// armed it: without that, a cancelled sequence's in-flight tick clears the one
// the user started after it.
type sequenceTimeoutMsg struct{ gen int }

// armSequenceTimeout returns a tick for the current sequence generation, or nil
// when the timeout is off (the shipped default).
func (m Model) armSequenceTimeout() tea.Cmd {
	// Nothing pending means nothing to expire. Without this the esc-cancel and
	// dropped-sequence paths each schedule a tick that can only no-op, one per
	// rejected keystroke once a timeout is configured.
	if m.seqTimeout <= 0 || len(m.pendingSeq) == 0 {
		return nil
	}
	gen := m.pendingGen
	return tea.Tick(m.seqTimeout, func(time.Time) tea.Msg {
		return sequenceTimeoutMsg{gen: gen}
	})
}

// stepSequence advances the prefix machine by one chord.
//
// Returns:
//   - (m, "", true)   the key was consumed — a sequence is now pending, or one
//     was cancelled or dropped. handleKey returns immediately.
//   - (m, id, false)  a multi-step sequence completed. handleKey continues with
//     its between-tier guards disabled so the action runs from its own tier.
//   - (m, "", false)  nothing happened; handleKey dispatches as it always did.
//
// The probe is TIER-AGNOSTIC. pane.close is late-tier, so binding it to
// "ctrl+b x" leaves the opening chord in neither tier's chord map; a
// tier-scoped probe would answer none and the sequence could never complete.
func (m Model) stepSequence(msg tea.KeyPressMsg, key string) (Model, keymap.ActionID, bool) {
	m.seqFlash = ""

	// The world can change under an armed prefix with no keypress at all: a
	// daemon broadcast moves the active pane (MCP set_active_pane, switch_tab),
	// or a message-driven dialog opens and closes. Completing the sequence then
	// would act on a pane the user never armed it in — the same hazard the
	// mouse-click cancel exists for, arriving by a route no input event covers.
	// The timeout would eventually catch it, but it ships off.
	if len(m.pendingSeq) > 0 && m.armedPaneID() != m.pendingPane {
		m.cancelSequence()
	}

	c, err := keymap.ParseChord(key)
	if err != nil {
		// An unparseable key is not a sequence step. Drop any pending prefix
		// rather than leaving it armed: every other "not part of this sequence"
		// path clears it, and leaving it would read the NEXT keystroke as step
		// two of a sequence the user has every reason to think was abandoned.
		if len(m.pendingSeq) > 0 {
			m.cancelSequence()
			return m, "", true
		}
		return m, "", false
	}

	// Esc always cancels, ahead of the probe: a binding could legitimately use
	// esc as a sequence step, and the escape hatch outranks it.
	if key == "esc" && len(m.pendingSeq) > 0 {
		m.cancelSequence()
		return m, "", true
	}

	// Literal escape: prefix prefix sends ONE raw chord to the pane. Returning
	// unhandled hands the original KeyPressMsg to handleKey's default branch,
	// so keyToBytes does the encoding and there is no second byte table to keep
	// in sync. Quil panes routinely ssh into hosts running tmux, and without
	// this the remote tmux has no reachable prefix at all.
	if len(m.pendingSeq) == 1 && m.pendingSeq[0] == c {
		m.cancelSequence()
		return m, "", false
	}

	cand := make([]keymap.Chord, 0, len(m.pendingSeq)+1)
	cand = append(cand, m.pendingSeq...)
	cand = append(cand, c)

	switch id, kind := m.keymap.MatchSeq(cand); kind {
	case keymap.MatchPartial:
		m.pendingSeq = cand
		m.pendingPane = m.armedPaneID()
		m.pendingGen++
		return m, "", true
	case keymap.MatchExact:
		// A length-1 exact is a plain chord. Leave it to the tier lookups, or
		// it would run on the wrong side of tryPluginRawKey and quietly beat a
		// plugin's raw_keys claim it is supposed to lose to.
		if len(cand) == 1 {
			return m, "", false
		}
		m.cancelSequence()
		return m, id, false
	}

	// No match. A pending sequence is dropped with a visible flash; a bare
	// chord falls through untouched, which is what keeps every single-chord
	// binding byte-identical to its pre-sequence behaviour.
	if len(m.pendingSeq) > 0 {
		// Names the PENDING prefix, never the unmatched chord that ended it.
		// Ctrl+B is readline's backward-char, so under the tmux preset the
		// machine arms on an ordinary shell keystroke — and the next character
		// typed might be a character of a password at an ssh or sudo prompt.
		// It is swallowed either way; painting it on the status bar until the
		// following keypress is what this avoids.
		m.seqFlash = keymap.Sequence(m.pendingSeq).String() + " — no such binding"
		m.cancelSequence()
		return m, "", true
	}
	return m, "", false
}

// notesKeyExempt reports whether a key should bypass the notes editor and
// reach the normal global handlers (structural changes, tab/pane management,
// dialogs). Anything not on this list is consumed by the editor as text
// input while notes mode is active.
//
// Note: Tab and Shift+Tab are deliberately NOT in this list — in notes mode
// they cycle keyboard focus between the editor and the bound pane, handled
// as a hard-coded case in the caller (not driven by kb.NextPane, which is
// now unbound by default since spatial navigation moved to Alt+Arrow).
//
// Note: ToggleLazygit (Alt+G) and ToggleHunk (Alt+H) are deliberately NOT in
// this list — notes mode binds the editor to a pane, and popping a full-screen
// overlay over it mid-edit conflicts with the notes layout. Both fall through
// to the editor harmlessly as plain text input.
func (m Model) notesKeyExempt(key string) bool {
	if key == "" {
		return false
	}
	// Exempt ACTIONS, not raw config strings. Resolving through the registry is
	// what lets the binding source move: comparing against cfg.Keybindings here
	// would silently start matching against empty strings the moment bindings
	// live anywhere else, and every structural key would stop flushing the
	// editor before it fires.
	exempt := []keymap.ActionID{
		// Vertical spatial nav — there's no up/down axis in the notes 2-panel
		// layout (pane|editor), so Alt+Up/Alt+Down flush and exit notes, then
		// the global handler runs NavigateDirection to the closest neighbor.
		// Alt+Left and Alt+Right are handled by the notes-mode focus toggle
		// earlier in handleKey and never reach this function.
		"pane.up", "pane.down",
		// Structural — close/split implicitly destroys the bound pane and must
		// flush + exit notes before running.
		"pane.close", "tab.close", "pane.split_h", "pane.split_v",
		// Tab management.
		"tab.new", "tab.rename", "pane.rename", "tab.cycle_color",
		// Other modes.
		"pane.focus_toggle",
		// Force repaint — view-level, harmless while the editor is open.
		"app.redraw",
		// Notification center.
		"notification.toggle", "notification.focus", "pane.go_back", "pane.mute", "pane.toggle_eager",
		// Project sidebar — view-level, and resizeAllPanes covers the notes
		// layout's own dependency on paneAreaWidth().
		"sidebar.toggle",
		// Preview wrap toggle — pane-level view state, harmless in notes mode.
		"pane.toggle_wrap",
		// Pane process restart — opens a confirm dialog, never types into
		// the notes editor.
		"pane.restart",
		// Tools and dialogs.
		"json.transform", "pane.quick_actions", "pane.command_history", "project.new",
		// Project navigation — switchProject (reached by both) already calls
		// exitNotesModeInPlace itself, so exempting these just lets the key
		// reach it instead of being swallowed as editor text.
		"project.picker", "project.toggle", "project.next", "project.prev",
		// Attention queue — notes are exactly the sort of thing left open
		// while an agent grinds in another pane, so "notes are focused" is a
		// likely state at the moment the queue is needed, arguably more so
		// than for the project picker above. handleKey's editor-focused
		// exempt branch calls exitNotesModeInPlace BEFORE falling through to
		// jumpToNextBlocked, so the teardown always lands on the OLD tab
		// whether the jump crosses a project boundary or only moves the
		// active tab within the current one.
		"project.attention_queue",
	}
	for _, id := range exempt {
		if m.isAction(key, id) {
			return true
		}
	}
	// Tab switching is exempt for the same reason as the structural keys: it
	// moves off the tab the editor is bound to. It reads from the registry now
	// that alt+1..9 are actions — a literal alt+digit list would exempt the
	// stale keys and not the rebound ones.
	for _, id := range []keymap.ActionID{
		"tab.switch_1", "tab.switch_2", "tab.switch_3", "tab.switch_4", "tab.switch_5",
		"tab.switch_6", "tab.switch_7", "tab.switch_8", "tab.switch_9",
		"tab.next", "tab.prev", "system.shortcuts",
	} {
		if m.isAction(key, id) {
			return true
		}
	}
	// Still hardcoded in handleKey, so still listed literally here.
	switch key {
	case "f1", "ctrl+n":
		return true
	}
	return false
}

// exitNotesModeInPlace flushes pending notes and tears down notes mode
// state on the receiver, but does NOT return a command — used when the
// caller intends to fall through to another handler in the same Update
// invocation.
// exitNotesModeInPlace is the single canonical teardown for notes mode. It
// flushes pending edits, reverts the tab's focus mode if we owned the toggle,
// and clears every notes-mode flag on the model. All other code paths
// (exitNotesMode, structural shortcut fall-through, applyWorkspaceState
// reconciliation, switchTab) delegate to this function so the teardown is
// guaranteed consistent.
//
// IMPORTANT: this function operates on the active project's active tab
// at the time of the call. Callers that are about to change that tab
// (e.g. switchTab) must invoke this FIRST so focus reverts on the old tab.
func (m *Model) exitNotesModeInPlace() {
	if m.notesEditor != nil {
		if err := m.notesEditor.Close(); err != nil {
			log.Printf("save notes on exit: %v", err)
		}
	}
	if m.notesEnteredFocus {
		if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
			tab.ExitFocus()
		}
	}
	m.notesMode = false
	m.notesEditor = nil
	m.notesPaneFocused = false
	m.notesEnteredFocus = false
	m.notesAnchorRow = 0
	m.notesAnchorCol = 0
}

// exitNotesMode is the command-returning form of exitNotesModeInPlace, used
// when the Update loop needs a batched ClearScreen + resize command after
// the teardown. Uses a pointer receiver so a discarded call (e.g., a bare
// `m.exitNotesMode()` statement) still mutates the model — preventing the
// "silent reinstate" footgun the previous review flagged.
func (m *Model) exitNotesMode() (tea.Model, tea.Cmd) {
	m.exitNotesModeInPlace()
	return *m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
}

// viewCacheBox holds the frame View last built. See Model.viewCache for why
// this is reached through a pointer.
type viewCacheBox struct {
	valid bool
	view  tea.View
	// builds counts real rebuilds. It is what the coalescing tests assert on:
	// comparing rendered content proves a skip was HONEST, this proves the skip
	// actually happened.
	builds int
}

func (m Model) View() tea.View {
	if m.skipRender && m.viewCache != nil && m.viewCache.valid {
		// Counted, because the perf log's view(n=...) is the number this
		// project's render-cost analysis is built on and coalescing silently
		// changed what it means. Without this the stat would report REBUILDS
		// while reading like frames delivered, and a future measurement would
		// compare it against pre-coalescing numbers that counted every frame.
		m.perfStats.recordSkippedView()
		if m.skipHidden {
			// A strict subset of the above — see recordHiddenSkip. Counted here
			// rather than in Update because this is the only place that knows
			// the cache was actually served.
			m.perfStats.recordHiddenSkip()
		}
		return m.viewCache.view
	}
	viewStart := time.Now()
	defer func() { m.perfStats.recordView(time.Since(viewStart)) }()
	var content string

	if m.width == 0 || m.height == 0 {
		content = "Connecting to quild..."
	} else if m.width < minTermWidth || m.height < minTermHeight {
		content = fmt.Sprintf("Terminal too small (%dx%d)\nMinimum: %dx%d",
			m.width, m.height, minTermWidth, minTermHeight)
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	} else if m.dialog == dialogPluginMigration && m.migrationLeft != nil {
		content = m.renderMigrationFullScreen()
	} else if (m.dialog == dialogTOMLEditor || m.dialog == dialogLogViewer) && m.tomlEditor != nil {
		// TOML editor and log viewer both take over the full screen
		// (bypass dialog rendering). The log viewer reuses the same
		// TextEditor with ReadOnly=true and HighlightPlain.
		content = m.renderTOMLEditorFullScreen()
	} else if m.dialog != dialogNone {
		content = m.renderDialog()
	} else {
		var sections []string

		// Active tab content + optional notes editor; the notification
		// sidebar is composited OVER the right edge afterwards
		// (overlayRight) — it takes no layout width, so panes never
		// resize when it toggles. Layout math single source of truth:
		// notesPanelWidth / sidebarOverlayWidth (notesEditorBox and the
		// mouse handlers stay in lockstep with this renderer). The project
		// sidebar is different — it DOES reserve layout width
		// (paneAreaWidth), so it is joined on the LEFT before any of the
		// above rather than composited over anything.
		tabH := m.height - chromeHeight
		notesW := m.notesPanelWidth()
		projSidebarW := m.projectSidebarWidth()
		// tabContent is assembled whether or not there is an active tab. An
		// active project with NO tabs used to skip this whole section — the
		// project sidebar with it — so the user got an empty screen and lost
		// the navigation needed to leave it (Alt+P/Alt+O still worked; the
		// mouse did not). Two shipped paths reach that state, both now also
		// repaired daemon-side, but the client must not depend on a daemon's
		// repair to keep its own navigation painted.
		var tabContent string
		if tab := m.activeTabModel(); tab != nil {
			tab.SetCanvas(m.paneAreaWidth(), tabH)
			tab.SetChrome(m.projectSidebarWidth())
			tab.Resize(m.paneAreaWidth()-notesW, tabH)
			// Pass per-frame state to panes for rendering
			if tab.Root != nil {
				for _, pane := range tab.Leaves() {
					pane.activeSel = m.selection
					pane.focusMode = tab.FocusMode() && pane.ID == tab.ActivePane
					pane.mcpHighlight = m.mcpHighlights[pane.ID]
				}
			}
			tabContent = tab.View()
			if notesW > 0 {
				editorFocused := !m.notesPaneFocused
				tabContent = lipgloss.JoinHorizontal(lipgloss.Top, tabContent, m.notesEditor.View(notesW, tabH, editorFocused))
			}
		} else {
			tabContent = m.renderEmptyTabArea(m.paneAreaWidth(), tabH)
		}
		if sw := m.sidebarOverlayWidth(); sw > 0 {
			m.notifications.focused = m.sidebarFocused
			// totalW is the PANE AREA, not the terminal: at this point
			// tabContent is only paneAreaWidth wide and the project
			// sidebar has not been joined on yet. Passing m.width made
			// overlayRight pad every line out to the full terminal width,
			// so the JoinHorizontal below produced a frame
			// projectSidebarWidth() columns WIDER than the terminal. The
			// strip's screen columns are unchanged — after the left join
			// the pane area's right edge is still the screen's.
			tabContent = overlayRight(tabContent, m.notifications.View(tabH), m.paneAreaWidth(), sw)
		}
		// The tab bar labels the PANE column, so it is joined above the panes
		// and INSIDE that column — one line of paneAreaWidth() starting at
		// screen column projectSidebarWidth(). Joining it as its own
		// full-width section above everything instead put row 0 over the
		// project sidebar too, so the tabs sat flush against the sidebar's
		// left edge rather than above the panes they name. The sidebar is a
		// full-height left column beside the pair, which is what puts its
		// PROJECTS heading on the same screen row as the tab names.
		paneArea := lipgloss.JoinVertical(lipgloss.Left, m.renderTabBar(), tabContent)
		if projSidebarW > 0 {
			paneArea = lipgloss.JoinHorizontal(lipgloss.Top, m.renderSidebar(m.sidebarContentHeight()), paneArea)
		}
		// Drag preview: a rule at the PENDING edge. The strip itself must not
		// move mid-drag — see the sidebarDragging field comment — so the
		// indicator is the only thing that follows the cursor. Composited like
		// the context menu, on paneArea, whose first line IS screen row 0.
		if m.sidebarDragging && m.sidebarDragW > 0 {
			rows := strings.Count(paneArea, "\n") + 1
			paneArea = overlayAt(paneArea, sidebarDragRuleBlock(rows), m.sidebarDragW-1, 0, m.width)
		}
		if m.ctxMenu.open() {
			// ctxMenu coords are screen rows and paneArea's first line IS
			// screen row 0 (the tab bar), so no shift.
			paneArea = overlayAt(paneArea, renderCtxMenu(m.ctxMenu), m.ctxMenu.x, m.ctxMenu.y, m.width)
		}
		sections = append(sections, paneArea)

		// Status bar
		sections = append(sections, m.renderStatusBar())

		content = lipgloss.JoinVertical(lipgloss.Left, sections...)
	}

	// Reconnect banner: an overlay over row 0 (the tab bar), so it reserves no
	// layout height and appearing or clearing it never resizes a pane. The tab
	// bar is the right thing to cover — tab switching is frozen anyway, and
	// obscuring pane content would hide the state the user is waiting on.
	//
	// Only for the ACTIVE project's daemon: it is one row, the user is looking at
	// one project, and a banner naming a host whose panes are not on screen would
	// read as an outage of the ones that are.
	if m.linkOf(m.activeDest()).active {
		content = overlayAt(content, m.renderReconnectBanner(m.width), 0, 0, m.width)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	// Desktop toasts suppress only for the pane the user is DEMONSTRABLY watching
	// — which needs window focus as well as on-screen position — and this is the
	// only thing that makes window focus knowable. Harmless where unsupported: the
	// terminal ignores the sequence and simply never reports.
	v.ReportFocus = true
	v.MouseMode = tea.MouseModeCellMotion
	if m.ctxMenu.open() {
		// Cell-motion only reports motion while a button is held, so the
		// context menu's hover highlight would be dead under it. All-motion
		// is scoped to exactly the frames where the menu is open — the
		// flood of buttonless motion events ends the moment it closes (the
		// menu's Update routing swallows them meanwhile).
		v.MouseMode = tea.MouseModeAllMotion
	}
	// v.Cursor stays nil — the hardware cursor is never shown. Every pane
	// type gets a software reverse-video caret drawn into the frame by
	// renderContent/insertCursor instead. Positioning the real cursor via
	// tea.View.Cursor was tried and reverted: the per-frame repositioning
	// desynced Bubble Tea's diff writer on Windows and the first typed
	// character landed one cell off ("Test" → "T est").
	if m.viewCache != nil {
		m.viewCache.view = v
		m.viewCache.valid = true
		m.viewCache.builds++
	}
	return v
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Per-key trace for modified keys. Flip [logging] level = "debug" in
	// config.toml to see every modified key reaching Quil. Useful for
	// diagnosing input-freeze and missing-key bugs.
	if msg.Mod != 0 {
		logger.Debug("handleKey: key=%q Mod=%v Code=%d Text=%q", key, msg.Mod, msg.Code, msg.Text)
	}

	// Dialog mode: route input to dialog handler.
	//
	// Cancel any pending sequence on the way in. A dialog can open with no
	// keypress at all — MsgPluginError matches a pattern against a pane's PTY
	// output, and the upgrade prompt is driven from WindowSizeMsg — and while
	// one is up View draws only the dialog, so the pending indicator is not on
	// screen either. Without this, dismissing that dialog leaves a prefix armed
	// that the user has no way to know about, and the next character they type
	// completes a sequence they never started.
	if m.dialog != dialogNone {
		m.cancelSequence()
		return m.handleDialogKey(msg)
	}

	// Rename mode: capture input for tab/pane name editing
	if m.renaming {
		return m.handleRenameKey(msg)
	}
	if m.renamingPane {
		return m.handlePaneRenameKey(msg)
	}

	// Context menu open: it captures navigation until closed. Quit passes
	// through inside the handler (never swallow quit).
	if m.ctxMenu.open() {
		return m.handleCtxMenuKey(key)
	}

	// Notes mode: while active, keyboard input is split between the bound
	// pane (left) and the notes editor (right). Alt+Left focuses the pane,
	// Alt+Right focuses the editor — spatial directions that match the
	// physical layout of the two panels. Tab inside notes mode is NOT a
	// focus-toggle: it reaches the editor (inserts tab) or the PTY (shell
	// completion), matching the rest of Quil's "Tab belongs to the PTY"
	// policy.
	if m.notesMode && m.notesEditor != nil {
		// Universal keys — handled the same way regardless of which side
		// currently has focus.
		switch {
		case m.isAction(key, "pane.notes_toggle"):
			return m.exitNotesMode()
		case m.isAction(key, "app.quit"):
			if err := m.notesEditor.Close(); err != nil {
				log.Printf("save notes on quit: %v", err)
			}
			return m, tea.Quit
		case m.isAction(key, "pane.left"):
			// Alt+Left — focus the bound pane (on the left in notes layout).
			// Idempotent: no-op if the pane is already focused.
			m.notesPaneFocused = true
			return m, nil
		case m.isAction(key, "pane.right"):
			// Alt+Right — focus the editor (on the right in notes layout).
			// Idempotent: no-op if the editor is already focused.
			m.notesPaneFocused = false
			return m, nil
		}

		// Structural keys (close pane/tab, split) destroy or restructure
		// the bound pane. Flush + exit notes first, regardless of which
		// side currently has focus, then fall through to the normal
		// handler so the structural action still fires.
		structural := m.isAction(key, "pane.close") || m.isAction(key, "tab.close") ||
			m.isAction(key, "pane.split_h") || m.isAction(key, "pane.split_v")
		if structural {
			m.exitNotesModeInPlace()
		} else if m.notesPaneFocused {
			// Pane has focus — fall through to the normal handlers below.
			// Global shortcuts (dialogs, rename, ...) work as usual, and
			// unmatched keys are forwarded to the PTY by the default
			// branch at the bottom of this function.
		} else if m.notesKeyExempt(key) {
			// Editor focused + non-structural exempt shortcut — flush
			// notes and fall through so the global handler runs.
			m.exitNotesModeInPlace()
		} else {
			// Editor has focus and the key is plain text input.
			action, cmd := m.notesEditor.HandleKey(key)
			if action == notesActionExit {
				return m.exitNotesMode()
			}
			return m, cmd
		}
	}

	// Overlay visible: intercept keys before global shortcuts reach pane-level
	// handlers (ClosePane, RenamePane, notes toggle, split, etc.). The sidebar-
	// focused branch below must NOT steal keys while an overlay tool is on
	// screen. The kb.ToggleLazygit / kb.ToggleHunk cases in the main switch are
	// still reachable when the overlay is hidden (this block only fires when
	// overlayVisible is true).
	if tab := m.activeTabModel(); tab != nil && tab.overlayVisible && tab.overlayPane != nil && m.dialog == dialogNone && !m.renaming && !m.renamingPane {
		return m, m.handleOverlayKey(msg, tab)
	}

	// Prefix sequence machine. Placed here so every mode that fully owns the
	// keyboard is already inert by construction: dialog, rename, pane-rename,
	// context menu and overlay all return above. The reconnect/parked screen
	// never reaches handleKey at all — freezeInput is called unconditionally in
	// Update and returns frozen.
	//
	// The two modes named below sit DOWNSTREAM of this point, so ordering does
	// not cover them. Add a mode here if you add one that consumes keys after
	// this line.
	//
	// seqAction is set only when a MULTI-STEP sequence completed. It makes the
	// lookups below resolve to that action and disables the between-tier
	// guards, because a completed sequence must run from its own tier and must
	// outrank a plugin's raw_keys claim on its final chord.
	// The dropped-sequence flash lives until the next keypress. Cleared here
	// rather than inside stepSequence, which is skipped entirely while the
	// sidebar has focus or a selection is active — press F3 after a dropped
	// sequence and the message would otherwise stay on the status bar for the
	// rest of the session.
	m.seqFlash = ""

	var seqAction keymap.ActionID
	if !(m.sidebarFocused && m.notifications.visible) && m.selection == nil {
		var consumed bool
		m, seqAction, consumed = m.stepSequence(msg, key)
		if consumed {
			return m, m.armSequenceTimeout()
		}
		// A completed sequence reaches its action WITHOUT passing through the
		// notes block above, so that block's handling has to be repeated for
		// the actions it treats specially. This mirrors it arm for arm — see
		// the notesMode branch earlier in this function.
		//
		// The two that bite hardest are pane.left / pane.right. In notes mode
		// they switch focus between the editor and the bound pane; run through
		// the ordinary late-tier arm instead, they navigate to another pane
		// entirely, and the next workspace broadcast re-syncs the active pane
		// back to the bound one — so the move silently undoes itself. Under a
		// vim-style prefix keymap (`pane.right = "${prefix} l"`) that is the
		// normal way to press them.
		if seqAction != "" && m.notesMode && m.notesEditor != nil {
			switch {
			case seqAction == "pane.notes_toggle":
				return m.exitNotesMode()
			case seqAction == "app.quit":
				if err := m.notesEditor.Close(); err != nil {
					log.Printf("save notes on quit: %v", err)
				}
				return m, tea.Quit
			case seqAction == "pane.left":
				m.notesPaneFocused = true
				return m, nil
			case seqAction == "pane.right":
				m.notesPaneFocused = false
				return m, nil
			case isStructuralAction(seqAction):
				// Destroys or restructures the bound pane: flush and tear down
				// before it runs, then fall through so the action still fires.
				m.exitNotesModeInPlace()
			}
		}
	}

	// Early-tier actions (always available). This lookup sits BEFORE
	// tryPluginRawKey, so an action resolved here beats a plugin's raw_keys
	// claim on the same chord. Moving one of these into the late switch below
	// silently hands the chord to the plugin — see keymap.Tier.
	earlyID, _ := m.keymap.MatchTier(keymap.TierEarly, key)
	if seqAction != "" {
		// A completed sequence replaces the chord lookup entirely. Blanking the
		// other tier matters: the sequence's final chord may ALSO be bound as a
		// plain chord here, and running that instead would fire an action the
		// user did not ask for.
		earlyID = ""
		if a, ok := keymap.Lookup(seqAction); ok && a.Tier == keymap.TierEarly {
			earlyID = seqAction
		}
	}
	switch earlyID {
	case "notification.toggle":
		// Alt+N: toggle visibility only, never focus. The sidebar is an
		// overlay — no pane resize needed, only a full repaint.
		m.notifications.visible = !m.notifications.visible
		m.sidebarFocused = false
		if m.notifications.visible {
			return m, tea.Batch(tea.ClearScreen, m.startSidebarTick())
		}
		return m, tea.ClearScreen
	case "sidebar.toggle":
		return m.toggleProjectSidebar()
	case "notification.focus":
		// Ctrl+Alt+N: open (if hidden) and focus sidebar
		if !m.notifications.visible {
			m.notifications.visible = true
		}
		m.sidebarFocused = true
		return m, tea.Batch(tea.ClearScreen, m.startSidebarTick())
	case "pane.go_back":
		return m.popPaneHistory()
	case "pane.mute":
		return m, m.toggleActivePaneMute()
	case "pane.toggle_eager":
		return m, m.toggleActivePaneEager()
	case "pane.toggle_wrap":
		// Flip the active wide-canvas pane's preview between left-edge
		// crop (default) and soft-wrap. View-only state — no IPC, no PTY
		// touch; the preview layout cache re-keys on the flag.
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil && pane.WideCanvas {
				pane.previewWrap = !pane.previewWrap
			}
		}
		return m, nil
	case "pane.toggle_lazygit":
		return m, m.handleToggleLazygit()
	case "pane.toggle_hunk":
		return m, m.handleToggleHunk()
	case "pane.command_history":
		return m.openHistoryForActivePane()
	case "pane.quick_actions":
		return m.openQuickActionsMenu()
	case "project.new":
		return m.openNewProjectDialog()
	case "project.destroy":
		// The ACTIVE project, because that is the only one a keystroke can
		// name — the sidebar's right-click menu is how another one is reached.
		// Opens the same confirm the menu does rather than destroying: this
		// takes every tab and pane under it, so it must never fire straight
		// off a keypress.
		// Same choice the context menu makes, for the same reason: on a remote
		// project Destroy is the action that cannot do what the user means,
		// because the daemon simply bootstraps a replacement.
		if p := m.cur(); p != nil {
			if p.Dest != "" {
				return m, m.confirmDisconnectHost(p.ID)
			}
			return m, m.confirmDestroyProject(p.ID)
		}
		return m, nil
	case "project.picker":
		return m.openProjectPicker()
	case "project.next", "project.prev":
		// A single project flashes rather than no-opping silently, for the
		// same reason the empty attention queue and the narrow-terminal
		// sidebar refusal do — and this is the ordinary state until the user
		// creates a second project, so it is the FIRST thing they would press
		// it in.
		if len(m.projects) < 2 {
			m.setFlash("only one project")
			return m, m.flashCmd()
		}
		delta := 1
		// The resolved action IS the direction now. Re-matching the key against
		// kb.ProjectPrev here would answer a different question than the lookup
		// that got us into this arm: with both fields bound to one chord the
		// registry awards it to project.next (lower Order, reported as a
		// duplicate conflict), and a kbMatches re-check would then run it
		// backwards.
		if earlyID == "project.prev" {
			delta = -1
		}
		// Sequenced for the same reason as ProjectToggle below: cycleProject
		// mutates m through a pointer receiver via switchProject.
		cmd := m.cycleProject(delta)
		return m, cmd
	case "project.toggle":
		// No bounce target flashes rather than doing nothing, for the same
		// reason the AttentionQueue empty case below does. This is the
		// ORDINARY state on a fresh launch: prevProject is only written by
		// switchProject, so until the user has switched once there is
		// genuinely nowhere to bounce back to — and a silent key there reads
		// as broken rather than as "not yet". It also covers a prevProject
		// whose project has since been destroyed. The check lives here, not
		// in toggleLastProject, which keeps its nil-means-nowhere-to-go
		// contract (and its own guard, which this must not duplicate the
		// meaning of).
		if m.prevProject == "" || m.projectByID(m.prevProject) == nil {
			m.setFlash("no previous project to switch back to")
			return m, m.flashCmd()
		}
		// Sequenced, not `return m, m.toggleLastProject()`: toggleLastProject
		// mutates m through a pointer receiver (via switchProject), and Go
		// does not order a plain operand against a call in the same return
		// statement (see activateSidebarRow's identical note in project.go).
		cmd := m.toggleLastProject()
		return m, cmd
	case "project.attention_queue":
		// An empty queue flashes rather than doing nothing, for the same
		// reason the SidebarToggle refusal above does: a key that no-ops
		// silently is indistinguishable from a broken one, and "nothing is
		// waiting" is the ordinary state for anyone whose agents never stop
		// for a permission prompt. The check lives here, not in
		// jumpToNextBlocked, which keeps its nil-means-nowhere-to-go contract.
		if len(m.blockedPanes()) == 0 {
			m.setFlash("no agent is waiting on you")
			return m, m.flashCmd()
		}
		// Sequenced for the same reason as ProjectToggle above:
		// jumpToNextBlocked mutates m through a pointer receiver.
		cmd := m.jumpToNextBlocked()
		return m, cmd
	}

	// Everything from here to the late-tier lookup is skipped for a completed
	// sequence: these guards protect the plain-chord path, and a sequence has
	// already been resolved against the whole keymap. In particular the raw-key
	// seam below must not claim a sequence's final chord.

	// Sidebar focused: route keys to notification center
	if seqAction == "" && m.sidebarFocused && m.notifications.visible {
		return m.handleNotificationKey(key)
	}

	// Selection: Enter copies (tmux convention), Esc clears, Cmd+C for macOS
	if seqAction == "" && m.selection != nil && key == "esc" {
		m.selection = nil
		return m, nil
	}
	if seqAction == "" && m.selection != nil && (key == "enter" || key == "super+c") {
		tab := m.activeTabModel()
		if tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				text := extractText(pane, m.selection)
				m.selection = nil
				if text != "" {
					return m, func() tea.Msg {
						if err := clipboard.Write(text); err != nil {
							log.Printf("pane clipboard write: %v", err)
						}
						return nil
					}
				}
				return m, nil
			}
		}
		m.selection = nil
		return m, nil
	}

	// Plugin-declared raw key passthrough (e.g., claude-code consumes shift+tab
	// for mode toggling). When the active pane's plugin lists the current key
	// in its RawKeys, send it straight to the PTY and skip every global
	// shortcut, selection guard, and pane-navigation binding below.
	if data := m.rawKeyFor(seqAction, key, msg); data != nil {
		m.selection = nil
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				pane.ResetScroll()
				// A typed key is the answer a parked pane was waiting for;
				// approving a permission prompt fires no hook of its own.
				pane.answerBlockedByInput()
			}
		}
		return m, m.forwardInputBytes(data)
	}

	// Selection: Shift+Arrow / Ctrl+Shift+Arrow / Ctrl+Alt+Shift+Arrow.
	// Match only the specific arrow-based combos the selection handler
	// actually supports — a broader prefix match would swallow shift+tab
	// (Claude Code mode toggle), shift+enter, and similar app-specific
	// keys that must reach the PTY.
	if seqAction == "" && isSelectionExtendKey(key) {
		return m.handleSelectionKey(key)
	}

	// Late-tier actions. Everything here is reachable only once tryPluginRawKey
	// above has declined the key, so a plugin's raw_keys claim beats these —
	// that is the whole reason the registry carries a Tier.
	lateID, _ := m.keymap.MatchTier(keymap.TierLate, key)
	if seqAction != "" {
		// Early-tier sequences already ran and returned above, so anything
		// still carrying a seqAction here is late-tier by elimination.
		lateID = seqAction
	}
	switch lateID {
	case "app.quit":
		return m, tea.Quit

	case "tab.new":
		return m.handleNewTab()

	case "pane.close":
		return m.openClosePaneConfirm()

	case "pane.restart":
		return m.openRestartPaneConfirm()

	case "tab.close":
		return m.openCloseTabConfirm()

	case "pane.split_h":
		if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
			tab.ExitFocus()
		}
		return m, m.splitPane(SplitHorizontal)

	case "pane.split_v":
		if tab := m.activeTabModel(); tab != nil && tab.FocusMode() {
			tab.ExitFocus()
		}
		return m, m.splitPane(SplitVertical)

	case "tab.rename":
		return m.beginTabRename()

	case "pane.rename":
		return m.beginPaneRename()

	case "tab.cycle_color":
		return m, m.cycleTabColor()

	case "app.redraw":
		// Recovery hatch for rendering artifacts: cell-diff drift (width
		// disagreements with the host terminal) accumulates until a full
		// repaint. See forceRedraw — shared with the command palette.
		return m.forceRedraw()

	case "pane.scroll_page_up":
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				lines := m.cfg.UI.PageScrollLines
				if lines <= 0 {
					lines = pane.vt.Height() / 2
				}
				pane.ScrollUp(lines)
			}
		}
		return m, nil

	case "pane.scroll_page_down":
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				lines := m.cfg.UI.PageScrollLines
				if lines <= 0 {
					lines = pane.vt.Height() / 2
				}
				pane.ScrollDown(lines)
			}
		}
		return m, nil

	case "pane.next":
		if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() {
			tab.NextPane()
		}
		return m, nil

	case "pane.prev":
		if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() {
			tab.PrevPane()
		}
		return m, nil

	case "pane.left":
		if tab := m.activeTabModel(); tab != nil {
			tab.NavigateDirection(DirLeft)
		}
		return m, nil

	case "pane.right":
		if tab := m.activeTabModel(); tab != nil {
			tab.NavigateDirection(DirRight)
		}
		return m, nil

	case "pane.up":
		if tab := m.activeTabModel(); tab != nil {
			tab.NavigateDirection(DirUp)
		}
		return m, nil

	case "pane.down":
		if tab := m.activeTabModel(); tab != nil {
			tab.NavigateDirection(DirDown)
		}
		return m, nil

	case "pane.paste":
		// Two more chords also paste; they are NOT registry actions and are
		// checked below, after this switch. See that block for why.
		return m, m.pasteClipboard()

	case "pane.focus_toggle":
		return m.toggleFocusForActiveTab()

	case "pane.notes_toggle":
		return m.toggleNotesMode()

	case "app.command_palette":
		return m.openCommandPalette()

	case "tab.next":
		// Sequenced, not inlined into the return: switchTabBy mutates m
		// through a pointer receiver, and Go does not order a plain operand
		// against a call in the same return statement — `return m, m.switchTabBy(1)`
		// may copy m before the mutation lands. Same reason the alt+digit case
		// below has always split it.
		cmd := m.switchTabBy(1)
		return m, cmd

	case "tab.prev":
		cmd := m.switchTabBy(-1)
		return m, cmd

	case "tab.switch_1", "tab.switch_2", "tab.switch_3", "tab.switch_4", "tab.switch_5",
		"tab.switch_6", "tab.switch_7", "tab.switch_8", "tab.switch_9":
		// The ID's last rune is the 1-based tab number; switchTab is 0-based.
		cmd := m.switchTab(int(lateID[len(lateID)-1] - '1'))
		return m, cmd

	case "system.shortcuts":
		return m.openShortcutsDialog()
	}

	// Multiple aliases for paste because Windows Terminal captures the
	// default Ctrl+V binding for its own paste action and never delivers
	// the key event to the TUI:
	//   - kb.Paste (ctrl+v): works on Linux/macOS native ttys; eaten by
	//                        Windows Terminal
	//   - ctrl+alt+v:        works on most Windows configs but is ambiguous
	//                        with AltGr on European keyboard layouts
	//   - f8:                guaranteed pass-through on every terminal,
	//                        no AltGr ambiguity (recommended on Windows)
	//
	// These are checked AFTER the tier lookup, not inside it, so every late
	// action keeps beating them — notably pane.restart, which preceded the
	// paste case in the pre-registry switch, so `restart_pane = "f8"` still
	// restarts. The cost is that a late action bound to f8 or ctrl+alt+v now
	// shadows the alias where the actions AFTER paste used not to; the build
	// reports that as a ConflictHardcoded, which is why both chords are in
	// keymap's hardcodedKeys table.
	if seqAction == "" && (key == "ctrl+alt+v" || key == "f8") {
		return m, m.pasteClipboard()
	}

	// A completed sequence that reached here resolved to an action with no
	// dispatch case (json.transform is the only one). Swallow it rather than
	// letting the final chord fall through to the reserved-key switch or the
	// pane — the user pressed a binding, not a key.
	if seqAction != "" {
		return m, nil
	}

	// Keys Quil reserves outright: they are never registry actions, so a
	// binding on one of them can never win. Also in keymap's hardcodedKeys.
	switch {
	case key == "ctrl+n":
		return m.openCreatePaneDialog()

	case key == "f1":
		return m.openAboutDialog()

	default:
		// Only process keys that produce PTY bytes.
		// Bare modifiers (shift, ctrl, alt, super) produce nil — ignore them.
		data := keyToBytes(msg)
		if data == nil {
			return m, nil
		}
		m.selection = nil
		if tab := m.activeTabModel(); tab != nil {
			if pane := tab.ActivePaneModel(); pane != nil {
				pane.ResetScroll()
				// Same trigger as the scroll reset above — the user acted on
				// this pane — and the answer a parked pane never otherwise
				// hears, since approving a prompt emits no hook.
				pane.answerBlockedByInput()
				// ESC is Claude's interrupt, and the one turn ending it
				// reports NO hook for — measured, not assumed. Without this
				// the pane goes on claiming work until SessionEnd. See
				// interruptWorkingPane for why a keystroke is sound here.
				if key == "esc" {
					m.interruptWorkingPane(pane.ID)
				}
			}
		}
		return m, m.forwardInputBytes(data)
	}
}

func (m Model) handleRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Tab rename mutates the tab bar's width: each typed character grows
	// the active tab cell, shifting every neighbor to the right. Bubble Tea
	// v2's cell-diff renderer occasionally leaves stale glyphs where the
	// previous-shorter render ended, producing visible tab-label overlap
	// that only goes away on a window resize. Every tab-bar-width-changing
	// key returns tea.ClearScreen so the next frame is a full repaint —
	// the same pattern used elsewhere in the codebase ("width changes —
	// force full redraw"). The cost is one extra clear+repaint per keypress
	// during an explicit rename, which is imperceptible.
	switch key {
	case "enter":
		m.renaming = false
		name := strings.TrimSpace(m.renameInput)
		if name != "" {
			if tab := m.activeTabModel(); tab != nil {
				tab.Name = name
				return m, tea.Batch(tea.ClearScreen, m.updateTab(tab.ID, name, tab.Color))
			}
		}
		return m, tea.ClearScreen

	case "escape":
		m.renaming = false
		return m, tea.ClearScreen

	case "backspace":
		if len(m.renameInput) > 0 {
			m.renameInput = m.renameInput[:len(m.renameInput)-1]
		}
		return m, tea.ClearScreen

	default:
		changed := false
		if len(key) == 1 {
			m.renameInput += key
			changed = true
		} else if key == "space" {
			m.renameInput += " "
			changed = true
		}
		if changed {
			return m, tea.ClearScreen
		}
		return m, nil
	}
}

func (m Model) handlePaneRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "enter":
		m.renamingPane = false
		name := strings.TrimSpace(m.paneRenameInput)
		if name != "" {
			if tab := m.activeTabModel(); tab != nil {
				if pane := tab.ActivePaneModel(); pane != nil {
					pane.Name = name
					return m, m.updatePane(pane.ID, name)
				}
			}
		}
		return m, nil

	case "escape":
		m.renamingPane = false
		return m, nil

	case "backspace":
		if len(m.paneRenameInput) > 0 {
			m.paneRenameInput = m.paneRenameInput[:len(m.paneRenameInput)-1]
		}
		return m, nil

	default:
		if len(key) == 1 {
			m.paneRenameInput += key
		} else if key == "space" {
			m.paneRenameInput += " "
		}
		return m, nil
	}
}

// handlePaneOutput applies one chunk of PTY output to its pane and reports
// whether anything the user can currently see changed.
//
// The bool exists because output is ~65% of this model's View rebuilds and most
// of it belongs to panes on other tabs, which View() does not draw. Everything
// this function does to a pane — rawBuf, the VT write, contentGen — feeds a
// PaneModel whose View() is cached on renderKey() and which is not rendered at
// all while off-screen.
//
// INVARIANT: a branch added here that can move the screen MUST set changedView.
// Nothing else enforces it, and the failure mode is a stale frame rather than a
// crash. Three branches set it today even though they render only inside the
// pane (see the comments at each): they fire once or twice per pane, so forcing
// a rebuild costs nothing, and they are exactly what a future tab-bar or
// sidebar indicator would read.
func (m *Model) handlePaneOutput(msg PaneOutputMsg) (tea.Cmd, bool) {
	// Overlay panes live outside the layout tree — check them first.
	for _, tab := range m.allTabs() {
		if tab.overlayPane != nil && tab.overlayPane.ID == msg.PaneID {
			// Same armed-reset consume as the layout-tree branch below. This
			// branch returns early, so without it an overlay pane's replay would
			// append onto content it was supposed to replace. Today's only
			// overlay (lazygit) has ghost_buffer = false and so never receives a
			// replay at all, leaving the flag armed and harmless — but the
			// asymmetry is what would bite a future overlay plugin that does.
			if msg.Ghost && tab.overlayPane.reattachReset {
				tab.overlayPane.reattachReset = false
				tab.overlayPane.resetForReattach()
			}
			tab.overlayPane.AppendOutput(msg.Data)
			// Settled the way the layout-tree branch below settles: AFTER the
			// append, and only once the pane really shows something. This used
			// to clear unconditionally on the FIRST byte — but lazygit's opening
			// frames set up the terminal and clear the screen well before they
			// paint, so the flag went down while the rectangle was still empty,
			// which is the state the indicator exists to cover.
			overlayChanged := m.paneIsVisible(msg.PaneID)
			if (tab.overlayPane.preparing || tab.overlayPane.resuming) && tab.overlayPane.restoreSettled() {
				tab.overlayPane.preparing = false
				tab.overlayPane.resuming = false
				// Same treatment as the layout branch's settle below, for the
				// same reason: once per pane, and the state a future indicator
				// would draw. Returning bare visibility here would leave the
				// two branches inconsistent for no gain.
				overlayChanged = true
			}
			return nil, overlayChanged
		}
	}
	for _, tab := range m.allTabs() {
		if tab.Root == nil {
			continue
		}
		if leaf := tab.Root.FindLeaf(msg.PaneID); leaf != nil {
			// Base: a pane the user is looking at always redraws. The branches
			// below raise this for state that is cheap to be conservative about.
			changedView := m.paneIsVisible(msg.PaneID)
			oldCWD := leaf.Pane.CWD
			// Reattach reset, applied on the daemon's FIRST replayed chunk rather
			// than predicted before the attach. Only a replay can double a pane's
			// scrollback, so only a replay needs the reset — and this is the one
			// place that knows a replay actually arrived. Doing it here needs no
			// agreement with the daemon about which plugins replay; see
			// armReattachReset.
			if msg.Ghost && leaf.Pane.reattachReset {
				leaf.Pane.reattachReset = false
				leaf.Pane.resetForReattach()
			}
			if msg.Ghost && m.cfg.GhostBuffer.Dimmed {
				if !leaf.Pane.ghost {
					log.Printf("pane %s: ghost=true (received %d bytes)", msg.PaneID, len(msg.Data))
				}
				leaf.Pane.ghost = true
			} else if !msg.Ghost {
				// Transitioning from replayed to live output. The replayed
				// content is KEPT, for every pane type.
				//
				// This used to reset the VT for claude-code specifically, on the
				// theory that replayed ANSI pollutes cursor state. The branch was
				// written in the same commit that introduced ghost_buffer — which
				// set claude-code to false — so nothing could ever satisfy it: a
				// pane with no ghost replay never sets Pane.ghost. It named the
				// one plugin that could not reach it, and sat unexercised until
				// the flag was flipped.
				//
				// It then destroyed exactly what the flag exists to restore.
				// ResetVT installs a fresh emulator, and the emulator's scrollback
				// is where the replayed history lives, so the pane came back with
				// its history and lost it on the first keystroke — the reset fires
				// on the live frame, and typing is what produces one.
				//
				// ghost_buffer = true already states that a pane's replayed
				// content is wanted; a type name in the TUI cannot know better
				// than the plugin's own setting. Cursor state needs no help here
				// either: the live frame that triggers this transition IS the
				// child painting, which is what fixes the cursor.
				if leaf.Pane.ghost {
					log.Printf("pane %s: ghost->live transition, preserving VT (type=%q)", msg.PaneID, leaf.Pane.Type)
				}
				leaf.Pane.ghost = false
			}
			appendStart := time.Now()
			leaf.Pane.AppendOutput(msg.Data)
			m.perfStats.recordPaneOutput(len(msg.Data), time.Since(appendStart))

			// Settle the restore state once the pane actually shows visible
			// content (checked AFTER AppendOutput so the VT reflects this
			// frame). A boot frame that only clears the screen leaves it blank
			// and keeps the indicator up; the frame that paints real content
			// clears it. Mirrors restoreSettled() used by the spinner tick.
			if (leaf.Pane.resuming || leaf.Pane.preparing) && leaf.Pane.restoreSettled() {
				leaf.Pane.resuming = false
				leaf.Pane.preparing = false
				// Renders only inside the pane today (see buildTopBorder), so
				// this is conservatism rather than necessity: it fires once per
				// pane and is what a tab-bar or sidebar pending indicator would
				// read if one is ever added.
				changedView = true
			}

			var cmds []tea.Cmd
			if !msg.Ghost && !leaf.Pane.liveOutputSeen {
				leaf.Pane.liveOutputSeen = true
				changedView = true
				// First live output: the child reflows right after the
				// daemon's resize kick lands. Repaint quickly to clean
				// boot-frame leftovers, and once more after the UI settles
				// (see paneSettleRepaintMsg).
				cmds = append(cmds,
					tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return paneSettleRepaintMsg{} }),
					tea.Tick(2*time.Second, func(time.Time) tea.Msg { return paneSettleRepaintMsg{} }),
				)
			}
			if leaf.Pane.CWD != oldCWD && leaf.Pane.CWD != "" {
				// Drawn only in the pane's own top border today. Conservative
				// for the same reason as the settle above: rare, cheap, and the
				// obvious candidate for a future sidebar column.
				changedView = true
				cmds = append(cmds, m.updatePaneCWD(msg.PaneID, leaf.Pane.CWD))
			}
			if len(cmds) == 0 {
				return nil, changedView
			}
			return tea.Batch(cmds...), changedView
		}
	}
	// Unknown pane: nothing was touched, so nothing changed.
	return nil, false
}

// applyWorkspaceState rebuilds the TUI state from one daemon's broadcast.
// dest names the destination that broadcast arrived on (empty = the local
// daemon) and scopes the merge: a broadcast is the FULL state of ONE daemon,
// so it may only replace THAT daemon's projects. Returns:
//   - newPaneIDs: IDs of PaneModels created during this reconciliation (for
//     spinner setup in the caller).
//   - overlayResizeCmds: resize commands that must be batched by the caller
//     for overlay panes that just became visible on initial creation (fixing
//     the 80×24 boot size they would otherwise keep until a window resize).
func (m *Model) applyWorkspaceState(state WorkspaceStateMsg, dest string) ([]string, []tea.Cmd) {
	var newPaneIDs []string
	var overlayResizeCmds []tea.Cmd

	// Index existing tabs and panes for preservation. Both maps span EVERY
	// project, not just the ones this broadcast rebuilds: reuse is keyed by ID
	// alone, so a tab or pane the daemon moved between projects keeps its
	// layout tree / VT emulator / scrollback instead of being rebuilt at its
	// new home while the original lives on in the old one. The dispose sweep
	// below can also only visit what is indexed here.
	existingTabs := make(map[string]*TabModel)
	existingPanes := make(map[string]*PaneModel)
	for _, tab := range m.allTabs() {
		existingTabs[tab.ID] = tab
		if tab.Root != nil {
			for _, pane := range tab.Leaves() {
				existingPanes[pane.ID] = pane
			}
		}
		if tab.overlayPane != nil {
			existingPanes[tab.overlayPane.ID] = tab.overlayPane
		}
	}

	paneMap := make(map[string]*PaneInfo)
	for i := range state.Panes {
		paneMap[state.Panes[i].ID] = &state.Panes[i]
	}

	// Index this dest's existing projects for reuse, so a project the daemon
	// still has keeps its identity (and its ProjectModel pointer) across the
	// rebuild. Projects from OTHER dests are preserved by mergeProjects below
	// — clearing all of them lets two daemons clobber each other on every tick.
	existingProjects := make(map[string]*ProjectModel, len(m.projects))
	for _, p := range m.projects {
		if p.Dest == dest {
			existingProjects[p.ID] = p
		}
	}

	// Which project is active is the CLIENT's state: a broadcast from one
	// daemon must never pull focus off another's project. state.ActiveProject
	// is adopted only when there is no active project yet — startup, before
	// any broadcast has been applied.
	activeID := ""
	if p := m.cur(); p != nil && !m.onlyOfflineProjects() {
		activeID = p.ID
	}
	if activeID == "" {
		activeID = state.ActiveProject
	}

	// Active-tab moves the DAEMON made, collected here and reported after the
	// merge below. Every path where the CLIENT changes the active tab reports
	// overlay truth (switchTab, jumpToPane, jumpToNextBlocked); this arm adopts
	// info.ActiveTab unconditionally, and at least four producers reach it —
	// MCP switch_tab, a new tab, a tab destroy, and a second client. Both drift
	// directions are the ones the feature exists to prevent: a tab left behind
	// keeps its overlay marked visible (exempt from the sweep forever), and a
	// tab entered while still stamped hidden is one sweep from having its
	// lazygit destroyed on screen.
	//
	// Deferred rather than built in the loop because overlayOnScreen answers for
	// the ACTIVE PROJECT, and which project that is only settles at
	// resolveActiveProjectIndex below.
	type activeTabMove struct{ from, target *TabModel }
	var tabMoves []activeTabMove

	infos := broadcastProjects(state, dest)
	rebuilt := make([]*ProjectModel, 0, len(infos))
	for _, info := range infos {
		proj, ok := existingProjects[info.ID]
		if !ok {
			proj = &ProjectModel{ID: info.ID, Dest: dest}
		}
		// The tab this project had active BEFORE the rebuild. Tabs are reused by
		// ID, so an unchanged active tab yields the same pointer and the
		// comparison below is a no-op — which is what keeps an ordinary
		// broadcast (the git ticker alone sends one every 5 s) off the wire.
		var fromTab *TabModel
		if ok && proj.activeTab >= 0 && proj.activeTab < len(proj.tabs) {
			fromTab = proj.tabs[proj.activeTab]
		}
		proj.Name, proj.RootDir, proj.Bootstrap = info.Name, info.RootDir, info.Bootstrap
		// The daemon answered, so whatever this row was standing in for is over.
		// This is the ONLY clear point, and it is here rather than in
		// finishReconnect because it also covers a host brought back through
		// adoptDest from the New Project dialog, which never passes through the
		// reconnect path at all.
		proj.Offline = nil
		tabs, projPaneIDs, projResizeCmds := m.rebuildTabs(info, state, existingTabs, existingPanes, paneMap, dest)
		proj.tabs = tabs
		proj.activeTab = indexOfTab(proj.tabs, info.ActiveTab)
		if targetTab := tabAt(proj.tabs, proj.activeTab); fromTab != targetTab {
			tabMoves = append(tabMoves, activeTabMove{from: fromTab, target: targetTab})
		}
		newPaneIDs = append(newPaneIDs, projPaneIDs...)
		overlayResizeCmds = append(overlayResizeCmds, projResizeCmds...)
		rebuilt = append(rebuilt, proj)
	}

	m.projects = mergeProjects(m.projects, rebuilt, dest)
	m.activeProject = resolveActiveProjectIndex(m.projects, activeID)
	// Both halves of the router's default just changed — the project list and
	// which of them is active — so push the answer immediately rather than at
	// the end of the function, where a later early return could skip it.
	m.syncActiveDest()

	// Now that the active project has settled, overlayOnScreen can answer for
	// the tabs the daemon moved between.
	for _, mv := range tabMoves {
		if cmd := m.overlayTruthTransitionCmd(mv.from, mv.target); cmd != nil {
			overlayResizeCmds = append(overlayResizeCmds, cmd)
		}
	}

	// Record what this destination holds, so a launch that cannot reach it can
	// still show these projects by name instead of dropping them. Placed after
	// the merge because that is where m.projects becomes authoritative for this
	// destination, and it self-skips when nothing changed.
	m.cacheRemoteProjects(dest)

	// Dispose panes that did not survive reconciliation — both panes pruned
	// from surviving tabs and every pane of tabs the daemon dropped. Without
	// this, each removed pane leaks its VT emulator (drain goroutine +
	// scrollback grid) for the TUI session's lifetime. The sweep spans every
	// project for the same reason the index does: a pane this broadcast did
	// not mention is still live if some other dest's project holds it.
	surviving := make(map[string]bool)
	for _, tab := range m.allTabs() {
		if tab.Root != nil {
			for id := range tab.Root.PaneIDs() {
				surviving[id] = true
			}
		}
		if tab.overlayPane != nil {
			surviving[tab.overlayPane.ID] = true
		}
	}
	for id, pane := range existingPanes {
		if !surviving[id] {
			pane.Dispose()
		}
	}
	// sizedOnce is the other per-pane map keyed by an id that can stop
	// existing. It is one bool per pane so nothing here is urgent, but the
	// sweep that answers "is this pane still real?" is already built, and a
	// map with no disposal path is the kind of thing that gets explained
	// rather than fixed.
	//
	// Its keys are dest-scoped, so the survivor set has to be too — a plain
	// pane-id set would drop every entry on the first sweep and hand each pane
	// a fresh first-resize kick on every broadcast.
	survivingKeys := make(map[string]bool, len(surviving))
	for _, proj := range m.projects {
		for _, tab := range proj.tabs {
			if tab.Root != nil {
				for id := range tab.Root.PaneIDs() {
					survivingKeys[sizedKey(proj.Dest, id)] = true
				}
			}
			if tab.overlayPane != nil {
				survivingKeys[sizedKey(proj.Dest, tab.overlayPane.ID)] = true
			}
		}
	}
	for key := range m.sizedOnce {
		if !survivingKeys[key] {
			delete(m.sizedOnce, key)
		}
	}

	log.Printf("apply: active project = %d, active tab = %d", m.activeProject, m.activeTabIdx())

	// Reconcile notes mode after daemon state sync:
	//   (a) If the bound pane no longer exists in any tab, tear down
	//       notes mode — the notes file is orphaned and the editor would
	//       otherwise keep writing to a dead pane ID.
	//   (b) If the bound pane still exists but the containing tab's
	//       ActivePane is now something else (e.g., a split created a new
	//       pane and the daemon promoted it), force ActivePane back to the
	//       bound pane so the focus-mode render shows the right pane next
	//       to the editor. Without this, the editor would silently sit
	//       next to an unrelated pane while still writing to the bound
	//       pane's notes file.
	log.Printf("apply: notes reconciliation start (mode=%v)", m.notesMode)
	if m.notesMode && m.notesEditor != nil {
		bound := m.notesEditor.PaneID()
		var boundTab *TabModel
		for _, tab := range m.allTabs() {
			if tab.Root != nil && tab.Root.PaneIDs()[bound] {
				boundTab = tab
				break
			}
		}
		if boundTab == nil {
			log.Printf("notes: bound pane %s pruned — exiting notes mode", bound)
			m.exitNotesModeInPlace()
		} else if boundTab.ActivePane != bound {
			log.Printf("notes: bound pane %s is no longer active (active=%s) — re-syncing", bound, boundTab.ActivePane)
			for _, p := range boundTab.Leaves() {
				p.Active = (p.ID == bound)
			}
			boundTab.ActivePane = bound
		}
	}
	log.Printf("apply: notes reconciliation done")

	return newPaneIDs, overlayResizeCmds
}

// rebuildTabs reconciles ONE project's tab list against the broadcast — the
// per-tab reuse-or-restore loop applyWorkspaceState used to run over every tab
// in the message, now scoped to info.TabIDs so a project can only ever rebuild
// its own tabs.
//
// The order is info.TabIDs' order: the project owns its tab order, and the tab
// bar renders the result verbatim. A TabID the broadcast does not describe is
// skipped rather than materialised as an empty tab.
//
// Returns the project's tabs, the pane IDs it created (the caller arms a
// spinner per ID) and the overlay resize commands the caller must batch.
func (m *Model) rebuildTabs(info ProjectInfo, state WorkspaceStateMsg, existingTabs map[string]*TabModel, existingPanes map[string]*PaneModel, paneMap map[string]*PaneInfo, dest string) ([]*TabModel, []string, []tea.Cmd) {
	var newPaneIDs []string
	var overlayResizeCmds []tea.Cmd

	tabByID := make(map[string]TabInfo, len(state.Tabs))
	for _, t := range state.Tabs {
		tabByID[t.ID] = t
	}

	out := make([]*TabModel, 0, len(info.TabIDs))
	for _, tabID := range info.TabIDs {
		tabInfo, ok := tabByID[tabID]
		if !ok {
			// The project claims a tab this broadcast does not describe.
			continue
		}
		// The tab's own project_id disagrees with the list that named it:
		// building it here would put ONE TabModel in two projects at once
		// (the tab bar would then render it twice and both copies would
		// fight over the same layout tree). The tab's own answer wins.
		if tabInfo.ProjectID != "" && tabInfo.ProjectID != info.ID {
			continue
		}
		// Reuse existing tab if possible (preserves layout tree).
		tab, exists := existingTabs[tabInfo.ID]
		if !exists {
			tab = NewTabModel(tabInfo.ID, tabInfo.Name)

			// New tab that doesn't exist locally — try to restore layout from daemon.
			if len(tabInfo.Layout) > 0 {
				tab = m.restoreTabLayout(tab, tabInfo, paneMap, existingPanes)
				tab.Dest = dest
				// All non-overlay panes in a restored tab are new.
				for _, pid := range tabInfo.Panes {
					if isOverlayPane(paneMap, pid) {
						continue
					}
					newPaneIDs = append(newPaneIDs, pid)
				}
				var shown bool
				var visCmd tea.Cmd
				newPaneIDs, shown, visCmd = m.reconcileOverlayPane(tab, tabInfo, paneMap, existingPanes, newPaneIDs)
				overlayResizeCmds = append(overlayResizeCmds, visCmd)
				if shown {
					overlayResizeCmds = append(overlayResizeCmds, m.overlayResizeCmd(tab))
				}
				out = append(out, tab)
				continue
			}
		}
		tab.Dest = dest
		tab.Name = tabInfo.Name
		tab.Color = tabInfo.Color

		// Build the set of panes the daemon says belong to this tab.
		// Overlay panes are excluded: they live outside the layout tree and
		// are reconciled separately below.
		daemonPaneSet := make(map[string]bool, len(tabInfo.Panes))
		for _, pid := range tabInfo.Panes {
			if isOverlayPane(paneMap, pid) {
				continue
			}
			daemonPaneSet[pid] = true
		}

		// Prune panes the daemon removed.
		if tab.Root != nil {
			for id := range tab.Root.PaneIDs() {
				if !daemonPaneSet[id] {
					tab.RemovePane(id)
				}
			}
		}

		// Exit focus mode if the tree was reduced to a single pane or empty.
		if tab.FocusMode() && (tab.Root == nil || tab.Root.IsLeaf()) {
			tab.ExitFocus()
		}

		// Add panes the daemon has but the tree doesn't.
		treePaneIDs := make(map[string]bool)
		if tab.Root != nil {
			treePaneIDs = tab.Root.PaneIDs()
		}
		for _, paneID := range tabInfo.Panes {
			// Overlay panes are reconciled separately — never insert into the tree.
			if isOverlayPane(paneMap, paneID) {
				continue
			}

			if treePaneIDs[paneID] {
				// Already in tree — just update metadata.
				if info, ok := paneMap[paneID]; ok {
					if leaf := tab.Root.FindLeaf(paneID); leaf != nil {
						wasPending := leaf.Pane.Pending
						syncPaneMeta(leaf.Pane, info, m.pluginWideCanvas(info.Type), m.pluginMinNativeCols(info.Type))
						// A deferred pane that just lazy-spawned (Pending→running,
						// e.g. on tab switch): arm the restore indicator NOW so it
						// covers the real boot, and enroll it for spinner ticks.
						// Its boot clock starts here, not at the original restore.
						if wasPending && !info.Pending {
							leaf.Pane.resuming = true
							leaf.Pane.resumeStart = time.Now()
							newPaneIDs = append(newPaneIDs, paneID)
							log.Printf("apply: pane %s spawned (pending→resuming)", paneID)
						}
					}
				}
				continue
			}

			// The pane a worktree REPLACE detached is not new — it is live on
			// both sides and deliberately absent from the tree while the add
			// runs, held in worktreeReplaced so a failure can put it back.
			//
			// Without this it takes the branch below: existingPanes is built
			// from tab.Leaves(), which skips the nil-Pane leaf it used to
			// occupy, so a broadcast landing mid-checkout builds an EMPTY
			// PaneModel for a live pane, consumes the reserved leaf, and clears
			// worktreeCreates — after which both settling paths bail on
			// `worktreeCreates[tabID] == ""` and the held model is leaked for
			// the session. A `git worktree add` is seconds to minutes wide and
			// the git-fingerprint ticker alone broadcasts every 5 s, so the
			// window is not a corner case.
			if held := m.worktreeReplaced[tab.ID]; held != nil && held.ID == paneID {
				continue
			}

			// New pane — reuse model if it existed elsewhere, otherwise create.
			pane, ok := existingPanes[paneID]
			info := paneMap[paneID]
			if !ok {
				pane = NewPaneModel(paneID, m.replayBufSize())
				pane.resumeStart = time.Now()
				switch {
				case info != nil && info.Pending:
					// Deferred pane (other tab, not spawned yet). Don't arm the
					// boot clock now — it spawns lazily on tab switch, where the
					// Pending→running transition arms resuming. The indicator
					// still shows while Pending (showRestoreIndicator) if visited.
					log.Printf("apply: new pane %s (pending/deferred)", paneID)
				case len(existingTabs) > 0:
					pane.preparing = true // new pane created while TUI is running
					log.Printf("apply: new pane %s (preparing)", paneID)
				case len(tabInfo.Layout) > 0:
					pane.resuming = true // restored pane with saved layout
					log.Printf("apply: new pane %s (resuming, has layout)", paneID)
				default:
					log.Printf("apply: new pane %s (fresh, no layout)", paneID)
				}
				newPaneIDs = append(newPaneIDs, paneID)
			}
			if info != nil {
				syncPaneMeta(pane, info, m.pluginWideCanvas(info.Type), m.pluginMinNativeCols(info.Type))
			}

			// Try to fill a pending split placeholder first.
			if m.pendingSplit != nil {
				if placeholder, ok := m.pendingSplit[tab.ID]; ok {
					placeholder.fill(pane)
					tab.invalidateLeaves()
					delete(m.pendingSplit, tab.ID)
					// The pane LANDING is what retires a worktree create's
					// prune exemption — not the daemon saying it succeeded.
					// Dropping it on the success response instead would trust
					// the daemon's send ordering: a daemon that answers "ok"
					// and never creates a pane would have the exemption
					// retired, the next broadcast prune the placeholder, and
					// the node detach while pendingSplit still pointed at it.
					// The next pane created in that tab then lands on an
					// unreachable leaf — a live PTY with no visible pane.
					delete(m.worktreeCreates, tab.ID)
					// A pane really landing in the reserved leaf is what proves
					// a REPLACE went through, and it is the only proof that
					// arrives reliably: the daemon broadcasts state BEFORE it
					// answers the request, so this runs BEFORE
					// applyCreatePaneResp — which then bails on the
					// already-cleared worktreeCreates and never reaches its own
					// dispose. Leaving it to that handler leaked the model on
					// every SUCCESSFUL replace, and left a stale entry that a
					// later failure in the same tab would restore into the
					// layout as a pane the daemon destroyed long ago.
					if held := m.worktreeReplaced[tab.ID]; held != nil {
						held.Dispose()
						delete(m.worktreeReplaced, tab.ID)
					}
					// Focus the new pane (it replaced the previously active one)
					tab.ActivePane = pane.ID
					continue
				}
			}

			// Fallback: insert at root level.
			if tab.Root == nil {
				tab.Root = NewLeaf(pane)
				tab.invalidateLeaves()
			} else if leaves := tab.Leaves(); len(leaves) == 0 {
				// The root is a bare placeholder, which PrunePlaceholders
				// cannot repair — it only inspects a split node's CHILDREN, so
				// a placeholder that IS the root is invisible to it. A replace
				// on a single-pane tab produces exactly that (leaf.Pane = nil
				// on the root leaf), and `tab.Leaves()[0]` below then panicked
				// with index out of range.
				//
				// Latent until now: the broadcast that consumed the reserved
				// leaf also refilled the root in the same pass, which masked
				// it. Skipping the held pane above removes that mask.
				tab.Root = NewLeaf(pane)
				tab.invalidateLeaves()
			} else {
				// Split the root vertically (stacked) to accommodate the new pane.
				tab.Root.SplitLeaf(leaves[0].ID, SplitVertical)
				tab.Root.FillPlaceholder(pane)
				tab.invalidateLeaves()
			}
		}

		// Clean up any unfilled placeholders (e.g., rapid double-splits).
		//
		// EXCEPT while a worktree create is in flight for this tab. For an
		// ordinary create the placeholder is unfilled for microseconds, so no
		// broadcast lands inside that window; a `git worktree add` holds it
		// for SECONDS, and spontaneous broadcasts land there routinely (a
		// child toggling mouse modes, a pane exiting, a git-fingerprint
		// change, another client). Pruning then detaches the node while
		// pendingSplit still points at it, so the pane that finally arrives is
		// assigned to an unreachable leaf and appears NOWHERE until a later
		// broadcast heals it through the root-insert fallback.
		//
		// The create's own response is what retires this placeholder — on
		// failure, or on timeout. Both delete the map entry, so the exemption
		// cannot outlive the request that armed it.
		// Pushed from the SAME read that decides the exemption, so the
		// placeholder and the message standing in it can never disagree about
		// whether a create is in flight.
		tab.CreatingBranch = m.worktreeCreates[tab.ID]
		if tab.Root != nil && tab.CreatingBranch == "" {
			tab.Root.PrunePlaceholders()
			tab.invalidateLeaves()
		}
		if tab.Root != nil {
			log.Printf("apply: tab %s panes reconciled (n=%d leaves)", tab.ID, len(tab.Leaves()))
		} else {
			log.Printf("apply: tab %s panes reconciled (root=nil)", tab.ID)
		}

		var shown bool
		var visCmd tea.Cmd
		newPaneIDs, shown, visCmd = m.reconcileOverlayPane(tab, tabInfo, paneMap, existingPanes, newPaneIDs)
		overlayResizeCmds = append(overlayResizeCmds, visCmd)
		if shown {
			overlayResizeCmds = append(overlayResizeCmds, m.overlayResizeCmd(tab))
		}

		m.finalizeTabPanes(tab)
		log.Printf("apply: tab %s finalized", tab.ID)
		out = append(out, tab)
	}

	return out, newPaneIDs, overlayResizeCmds
}

// restoreTabLayout rebuilds a tab's layout tree from serialized daemon state.
func (m *Model) restoreTabLayout(tab *TabModel, tabInfo TabInfo, paneMap map[string]*PaneInfo, existingPanes map[string]*PaneModel) *TabModel {
	log.Printf("restoreLayout: tab %s %q with %d panes", tab.ID, tabInfo.Name, len(tabInfo.Panes))
	tab.Name = tabInfo.Name
	tab.Color = tabInfo.Color

	// Build PaneModel objects for all panes in this tab. Overlay panes are
	// excluded — they never enter the tree and reconcileOverlayPane adopts
	// them from existingPanes; building one here would leak its VT drain
	// goroutine (never adopted, never disposed).
	paneModels := make(map[string]*PaneModel, len(tabInfo.Panes))
	for _, paneID := range tabInfo.Panes {
		if isOverlayPane(paneMap, paneID) {
			continue
		}
		pane, ok := existingPanes[paneID]
		if !ok {
			pane = NewPaneModel(paneID, m.replayBufSize())
			pane.resuming = true
			pane.resumeStart = time.Now()
		}
		if info, ok := paneMap[paneID]; ok {
			syncPaneMeta(pane, info, m.pluginWideCanvas(info.Type), m.pluginMinNativeCols(info.Type))
		}
		paneModels[paneID] = pane
	}

	// Deserialize the layout tree.
	serialized, err := UnmarshalLayout(tabInfo.Layout)
	if err == nil && serialized != nil {
		tab.Root = DeserializeLayout(serialized, paneModels)
		if tab.Root != nil {
			tab.Root.PrunePlaceholders()
		}
		tab.invalidateLeaves()
	}

	// Add any panes not in the deserialized tree (e.g., created while TUI was away).
	// Overlay panes are never part of the tree — skip them here.
	treePaneIDs := make(map[string]bool)
	if tab.Root != nil {
		treePaneIDs = tab.Root.PaneIDs()
	}
	for _, paneID := range tabInfo.Panes {
		if isOverlayPane(paneMap, paneID) {
			continue
		}
		if treePaneIDs[paneID] {
			continue
		}
		pane := paneModels[paneID]
		if tab.Root == nil {
			tab.Root = NewLeaf(pane)
		} else {
			tab.Root.SplitLeaf(tab.Leaves()[0].ID, SplitVertical)
			tab.Root.FillPlaceholder(pane)
		}
		tab.invalidateLeaves()
	}

	m.finalizeTabPanes(tab)
	return tab
}

// isOverlayPane reports whether the daemon broadcast marks pane id as an
// overlay (never part of the layout tree).
func isOverlayPane(paneMap map[string]*PaneInfo, id string) bool {
	info, ok := paneMap[id]
	return ok && info.Overlay
}

// reconcileOverlayPane adopts the overlay pane reported by the daemon into
// tab.overlayPane, or clears the slot when the daemon no longer reports one.
// The overlay is never part of the layout tree. Returns newPaneIDs extended
// with the overlay pane ID when a new PaneModel was created for it,
// overlayShown=true when the overlay just flipped from hidden to visible due
// to a pendingOverlayShow entry (the caller should issue an overlayResizeCmd
// so the daemon PTY gets the correct dimensions immediately on creation), and
// a visibility command the caller must batch whenever overlayVisible flips
// here — the daemon's hidden-overlay timer has no other way to learn it.
//
// Disposal ownership: this function never calls Dispose. Every pre-existing
// overlay PaneModel was indexed into existingPanes by the caller, so a pane
// dropped from the slot here is simply absent from the surviving set and the
// caller's post-reconciliation sweep disposes it exactly once.
func (m *Model) reconcileOverlayPane(
	tab *TabModel,
	tabInfo TabInfo,
	paneMap map[string]*PaneInfo,
	existingPanes map[string]*PaneModel,
	newPaneIDs []string,
) ([]string, bool, tea.Cmd) {
	// Find the overlay pane for this tab in the daemon broadcast, if any.
	var overlayInfo *PaneInfo
	for _, pid := range tabInfo.Panes {
		if isOverlayPane(paneMap, pid) {
			overlayInfo = paneMap[pid]
			break
		}
	}

	switch {
	case overlayInfo == nil:
		// Daemon has no overlay for this tab (exited or destroyed).
		// The dropped PaneModel is disposed by the caller's sweep.
		if tab.overlayPane != nil {
			// Captured before the slot is cleared: overlayVisibilityCmd reads
			// tab.overlayPane, which is nil immediately below.
			visCmd := m.overlayVisibilityCmd(tab, false)
			tab.overlayPane = nil
			tab.overlayVisible = false
			return newPaneIDs, false, visCmd
		}
	case tab.overlayPane == nil || tab.overlayPane.ID != overlayInfo.ID:
		// New overlay arrived (or replaced an old one — the replaced
		// PaneModel is disposed by the caller's sweep).
		pane, ok := existingPanes[overlayInfo.ID]
		if !ok {
			pane = NewPaneModel(overlayInfo.ID, m.replayBufSize())
			// The boot indicator, for the same reason a tree pane gets one at
			// the `len(existingTabs) > 0` arm above: a full-tab overlay whose
			// tool has not painted yet is an empty rectangle, and lazygit
			// scanning a large repository or hunk computing a diff is seconds
			// of it with nothing on screen to say so.
			//
			// Gated on pendingOverlayShow, which is precisely "this client's
			// Alt+G/Alt+D just created this". An overlay adopted on a plain
			// REATTACH is a tool that has been running for hours — claiming it
			// is starting up would be a confidently wrong answer, and it is
			// hidden by default there anyway, so it renders nothing at all.
			if m.pendingOverlayShow[tab.ID] {
				pane.preparing = true
				pane.resumeStart = time.Now()
			}
			newPaneIDs = append(newPaneIDs, overlayInfo.ID)
		}
		syncPaneMeta(pane, overlayInfo, m.pluginWideCanvas(overlayInfo.Type), m.pluginMinNativeCols(overlayInfo.Type))
		tab.overlayPane = pane
		// Show the overlay immediately when this TUI's Alt+G triggered its
		// creation (pendingOverlayShow entry). On plain reattach, default hidden.
		if m.pendingOverlayShow[tab.ID] {
			delete(m.pendingOverlayShow, tab.ID)
			tab.overlayVisible = true
			// newly visible — caller must resize
			return newPaneIDs, true, m.overlayVisibilityCmd(tab, true)
		}
	default:
		// Same overlay pane — refresh metadata only.
		syncPaneMeta(tab.overlayPane, overlayInfo, m.pluginWideCanvas(overlayInfo.Type), m.pluginMinNativeCols(overlayInfo.Type))
	}

	return newPaneIDs, false, nil
}

// finalizeTabPanes ensures the active pane is valid and focus flags are set.
func (m *Model) finalizeTabPanes(tab *TabModel) {
	if tab.Root == nil {
		return
	}
	leaves := tab.Leaves()
	if len(leaves) == 0 {
		return
	}
	found := false
	for _, p := range leaves {
		if p.ID == tab.ActivePane {
			found = true
			p.Active = true
		} else {
			p.Active = false
		}
	}
	if !found {
		tab.ActivePane = leaves[0].ID
		leaves[0].Active = true
	}
}

// replayBufSize returns the byte capacity for per-pane replay buffers,
// matching the daemon's ring buffer sizing.
func (m *Model) replayBufSize() int {
	size := m.cfg.GhostBuffer.MaxLines * 512
	if size <= 0 {
		size = 500 * 512
	}
	return size
}

func (m *Model) resizeTabs() {
	tabH := m.height - chromeHeight
	for _, tab := range m.allTabs() {
		// Canvas is independent of the notes squeeze (a compositor crop) but
		// NOT of the project sidebar — the sidebar is a real reservation of
		// screen estate, so from a wide-canvas pane's perspective toggling it
		// is indistinguishable from a real window resize.
		tab.SetCanvas(m.paneAreaWidth(), tabH)
		tab.SetChrome(m.projectSidebarWidth())
		tab.Resize(m.paneAreaWidth(), tabH)
	}
}

// isActivePane reports whether paneID is the pane the user is currently
// focused on (active pane of the active tab). Used by the notification
// dispatcher to suppress redundant idle events for the pane the user is
// already staring at.
func (m Model) isActivePane(paneID string) bool {
	if paneID == "" {
		return false
	}
	tab := m.activeTabModel()
	if tab == nil {
		return false
	}
	return tab.ActivePane == paneID
}

// switchTab sets the active tab locally and notifies the daemon so its
// active_tab stays in sync (prevents stale overwrites on broadcastState).
//
// It also reports both tabs' overlay visibility, because only the active tab
// renders: leaving a tab takes its overlay off screen without touching
// overlayVisible (which survives the switch by design), and entering one with
// the flag still set puts its overlay straight back on screen. Neither
// transition flips a flag, so without these two reports the daemon's copy is
// wrong for as long as the user stays away — and a "visible" overlay is exempt
// from the idle sweep forever.
// switchTabBy moves the active tab by delta, wrapping at both ends.
//
// Pointer receiver, matching switchTab: a value receiver would apply the
// index change to a local copy and discard it, which looks identical to
// "nothing happened" at the call site.
//
// Wrapping matches tmux's next-window/previous-window. A non-wrapping version
// makes the last tab a dead end for anyone driving purely from the keyboard.
func (m *Model) switchTabBy(delta int) tea.Cmd {
	tabs := len(m.curTabs())
	if tabs == 0 {
		return nil
	}
	idx := ((m.activeTabIdx()+delta)%tabs + tabs) % tabs
	return m.switchTab(idx)
}

// openShortcutsDialog jumps straight to F1 -> Shortcuts, which is otherwise
// reachable only by opening About and selecting a row. tmux binds prefix-? to
// the equivalent list, so the preset needs a direct entry point.
func (m Model) openShortcutsDialog() (tea.Model, tea.Cmd) {
	m.dialog = dialogShortcuts
	m.dialogCursor = 0
	return m, tea.ClearScreen
}

func (m *Model) switchTab(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.curTabs()) {
		return nil
	}
	// Switching tabs leaves the notes-bound pane behind. Flush and exit
	// notes mode BEFORE the active tab changes so exitNotesModeInPlace
	// reverts focus mode on the OLD tab.
	if m.notesMode && m.notesEditor != nil {
		m.exitNotesModeInPlace()
	}
	target := m.curTabs()[idx]
	from := m.activeTabModel()
	tabID, dest := target.ID, target.Dest
	m.setActiveTabIdx(idx)
	cmds := []tea.Cmd{func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgSwitchTab, ipc.SwitchTabPayload{
			TabID: tabID,
		})
		m.sendForDest(dest, msg)
		return nil
	}}
	// Built AFTER setActiveTabIdx so both answers describe the new state, and
	// idempotent daemon-side in both directions (a repeat hide does not push
	// the deadline out; a show just re-stamps OverlayShownAt).
	if cmd := m.overlayTruthTransitionCmd(from, target); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if len(cmds) == 1 {
		return cmds[0]
	}
	return tea.Batch(cmds...)
}

// eagerTabMarker is a single-width BMP glyph (deliberately not an emoji — wide
// glyphs drift conhost columns; see pane_widechar_test.go). Shown on any tab
// containing at least one eager-restore pane.
const eagerTabMarker = "●"

// tabHasEagerPane reports whether any pane in the tab at idx has Eager set.
func (m Model) tabHasEagerPane(idx int) bool {
	tabs := m.curTabs()
	if idx < 0 || idx >= len(tabs) || tabs[idx].Root == nil {
		return false
	}
	for _, p := range tabs[idx].Leaves() {
		if p != nil && p.Eager {
			return true
		}
	}
	return false
}

// tabLabel returns the label text rendered inside a tab cell at index idx.
// The active tab is prefixed with "* " so it's visible at a glance even when
// colored tabs override the bold-active styling. `renderTabBar` and
// `hitTestTab` MUST go through this helper so click coordinates stay aligned
// with the rendered widths.
func (m Model) tabLabel(idx int) string {
	if m.renaming && idx == m.activeTabIdx() {
		return "* " + m.renameInput + "▎"
	}
	name := fmt.Sprintf("%d:%s", idx+1, m.curTabs()[idx].Name)
	if m.tabHasEagerPane(idx) {
		name = eagerTabMarker + name
	}
	// Outside tabStyle's precedence on purpose. The colour can carry only one
	// fact, and blocked outranks pinned there — so on a tab that is both, the
	// glyph is the only thing left to say the pin exists. glyphPinned rather
	// than a marker of its own: the sidebar already spends ◆ on this, and a
	// second symbol for one state is the vocabulary confusion this change is
	// removing. One cell, like eagerTabMarker, and prefixed the same way so the
	// two read as one row of marks rather than two conventions.
	if m.tabPinnedAttention(idx) {
		name = glyphPinned + name
	}
	if m.tabHasWorkingPane(idx) {
		name = workingGlyph(m.workSpinnerFrame) + " " + name
	}
	if idx == m.activeTabIdx() {
		return "* " + name
	}
	return name
}

// tabStyle returns the lipgloss style for the tab at idx. Precedence: amber
// blocked mark (a pane parked on the user) > purple pinned mark (a pane the
// user marked by hand from the context menu) > green unseen mark (background
// tab with an unfocused finished pane) > custom tab color > active/inactive
// default. Shared by renderTabBar and hitTestTab so rendered widths and click
// hit-testing never diverge.
//
// Pinned and unseen used to SHARE the green, which made the mark the user set
// indistinguishable from the one the agent caused — and only one of the two
// ever clears itself. The ◆ prefix tabLabel adds is independent of this
// precedence, so a tab that is both blocked and pinned still says so.
func (m Model) tabStyle(idx int) lipgloss.Style {
	tab := m.curTabs()[idx]
	active := idx == m.activeTabIdx()
	// Blocked first: a parked agent is a question waiting on the user, and it
	// includes the ACTIVE tab because the pane may be in an unfocused split.
	// Amber replaces the background outright, which is the ONE thing active and
	// inactive differ by — so the active tab needs its own variant, or two
	// parked tabs (the case this exists for) leave the bar unable to say which
	// one you are on. Same width by construction; see blockedActiveTabStyle.
	if m.tabBlocked(idx) {
		if active {
			return blockedActiveTabStyle
		}
		return blockedTabStyle
	}
	// Pinned before unseen, and in its own colour rather than sharing green.
	// tabUnseen self-excludes the active tab; tabPinnedAttention deliberately
	// does not (a pin colors the active tab's label unless the pinned pane is
	// the one in focus).
	if m.tabPinnedAttention(idx) {
		if active {
			return pinnedActiveTabStyle
		}
		return pinnedTabStyle
	}
	if m.tabUnseen(idx) {
		return unseenTabStyle
	}
	if tab.Color != "" {
		c := lipgloss.Color(tab.Color)
		if active {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(c).Padding(0, 1)
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(c).Padding(0, 1)
	}
	if active {
		return activeTabStyle
	}
	return inactiveTabStyle
}

// fitTabBar clamps an assembled tab bar to at most barW CELLS, so the bar is
// always exactly one painted line.
//
// lipgloss's .Width() WRAPS an over-wide line onto a new one rather than
// truncating it — the same behaviour that forced the sidebar's rows onto cell
// measurement (see truncateCells). A wrapped tab bar is the worse version of
// that bug: row 0 becomes two lines, so the WHOLE frame shifts down one row
// while sidebarRowAt, the pane rects and every hit test still compute against
// the unshifted layout, and the status bar is pushed off the bottom. Every
// click in the UI lands one row out.
//
// The overflow path can produce it: the active tab is included
// unconditionally, before any budget check, so a single label wider than the
// whole bar survives. That was reachable when the bar spanned m.width and is
// projectSidebarWidth() columns more reachable now that it spends
// paneAreaWidth() — a tab named after a long branch or directory reaches it.
//
// ansi.Truncate measures CELLS and drops a straddling wide glyph whole rather
// than emitting half of one; the reset closes any SGR the cut left open, so
// the spaces .Width() pads with cannot inherit a tab's background colour.
// An in-budget bar is returned byte-for-byte untouched.
//
// hitTestTab needs no mirror of this: truncation only ever removes the TAIL,
// and the loop that fills the bar admits a second tab only while the running
// total stays inside barW — so an over-wide active tab is alone on the bar and
// still owns every column the hit test can be asked about.
func fitTabBar(bar string, barW int) string {
	if barW <= 0 || lipgloss.Width(bar) <= barW {
		return bar
	}
	return ansi.Truncate(bar, barW, "") + "\x1b[0m"
}

// renderTabBar renders the tab strip that sits directly above the panes.
//
// It is sized to paneAreaWidth(), NOT the terminal: View() joins it into the
// pane COLUMN (above tabContent, right of the project sidebar), so its first
// painted cell is screen column projectSidebarWidth() and it has exactly the
// panes' width to spend. Sizing it to m.width instead made the bar overhang
// the sidebar by that many columns. hitTestTab mirrors this budget — which
// tabs overflow depends on it, so the two must read the same width.
func (m Model) renderTabBar() string {
	barW := m.paneAreaWidth()
	tabs := m.curTabs()
	if len(tabs) == 0 {
		return lipgloss.NewStyle().Width(barW).Render("")
	}

	type renderedTab struct {
		text  string
		width int
	}

	// Pre-render all tabs
	all := make([]renderedTab, len(tabs))
	for i := range tabs {
		name := m.tabLabel(i)
		style := m.tabStyle(i)
		rendered := style.Render(name)
		all[i] = renderedTab{text: rendered, width: lipgloss.Width(rendered)}
	}

	// Try to fit all tabs
	totalW := 0
	for i, rt := range all {
		totalW += rt.width
		if i > 0 {
			totalW++ // space separator
		}
	}

	if totalW <= barW {
		// Everything fits
		tabs := make([]string, len(all))
		for i, rt := range all {
			tabs[i] = rt.text
		}
		bar := strings.Join(tabs, " ")
		return lipgloss.NewStyle().Width(barW).Render(fitTabBar(bar, barW))
	}

	// Overflow: include active tab, expand outward, show indicator for hidden
	included := make([]bool, len(tabs))
	activeIdx := m.activeTabIdx()
	included[activeIdx] = true
	usedW := all[activeIdx].width

	// Reserve space for overflow indicator (e.g. " «3 more»")
	indicatorReserve := 12

	// Expand left, then right from active tab
	left := activeIdx - 1
	right := activeIdx + 1
	for left >= 0 || right < len(tabs) {
		if left >= 0 {
			need := all[left].width + 1 // +1 for separator
			if usedW+need+indicatorReserve <= barW {
				included[left] = true
				usedW += need
				left--
			} else {
				left = -1 // stop expanding left
			}
		}
		if right < len(tabs) {
			need := all[right].width + 1
			if usedW+need+indicatorReserve <= barW {
				included[right] = true
				usedW += need
				right++
			} else {
				right = len(tabs) // stop expanding right
			}
		}
	}

	// Build the bar with overflow indicators
	hidden := 0
	for _, inc := range included {
		if !inc {
			hidden++
		}
	}

	var parts []string
	for i, rt := range all {
		if included[i] {
			parts = append(parts, rt.text)
		}
	}
	bar := strings.Join(parts, " ")
	if hidden > 0 {
		indicator := fmt.Sprintf(" «%d more»", hidden)
		bar += lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(indicator)
	}

	return lipgloss.NewStyle().Width(barW).Render(fitTabBar(bar, barW))
}

// hitTestTab returns the tab index at screen X coordinate, or -1 if none.
// Mirrors renderTabBar() width/overflow logic exactly.
//
// x arrives SCREEN-absolute, and the bar's first cell is screen column
// projectSidebarWidth() (View() joins it into the pane column, right of the
// project sidebar) — so the offset comes off first and everything below runs
// in bar-local columns against the same paneAreaWidth() budget renderTabBar
// spends. Columns inside the sidebar answer -1: the sidebar swallows them
// before Update ever reaches the tab-bar branch, and answering with a tab
// would make a click on the PROJECTS heading switch tabs.
func (m *Model) hitTestTab(x int) int {
	barW := m.paneAreaWidth()
	x -= m.projectSidebarWidth()
	if x < 0 {
		return -1
	}
	tabs := m.curTabs()
	if len(tabs) == 0 {
		return -1
	}

	type renderedTab struct {
		width int
		index int
	}

	// Pre-render tab widths using the same styling as renderTabBar.
	all := make([]renderedTab, len(tabs))
	for i := range tabs {
		name := m.tabLabel(i)
		style := m.tabStyle(i)
		rendered := style.Render(name)
		all[i] = renderedTab{width: lipgloss.Width(rendered), index: i}
	}

	// Determine which tabs are visible (same overflow logic).
	totalW := 0
	for i, rt := range all {
		totalW += rt.width
		if i > 0 {
			totalW++
		}
	}

	included := make([]bool, len(tabs))
	if totalW <= barW {
		for i := range included {
			included[i] = true
		}
	} else {
		activeIdx := m.activeTabIdx()
		included[activeIdx] = true
		usedW := all[activeIdx].width
		indicatorReserve := 12

		left := activeIdx - 1
		right := activeIdx + 1
		for left >= 0 || right < len(tabs) {
			if left >= 0 {
				need := all[left].width + 1
				if usedW+need+indicatorReserve <= barW {
					included[left] = true
					usedW += need
					left--
				} else {
					left = -1
				}
			}
			if right < len(tabs) {
				need := all[right].width + 1
				if usedW+need+indicatorReserve <= barW {
					included[right] = true
					usedW += need
					right++
				} else {
					right = len(tabs)
				}
			}
		}
	}

	// Walk visible tabs and match X coordinate.
	cursor := 0
	for i, rt := range all {
		if !included[i] {
			continue
		}
		if cursor > 0 {
			cursor++ // space separator
		}
		if x >= cursor && x < cursor+rt.width {
			return i
		}
		cursor += rt.width
	}

	return -1
}

func (m Model) renderTOMLEditorFullScreen() string {
	e := m.tomlEditor
	e.ViewWidth = m.width
	e.ViewHeight = m.height - 2 // title bar + status bar

	var b strings.Builder

	// Title bar (raw ANSI — background color 236). Read-only buffers (log
	// viewer, history entry) are for viewing, not editing — label them so and
	// never show the dirty marker.
	title := "Edit: "
	if e.ReadOnly {
		title = "View: "
	}
	if idx := strings.LastIndex(e.FilePath, "/"); idx >= 0 {
		title += e.FilePath[idx+1:]
	} else if idx := strings.LastIndex(e.FilePath, "\\"); idx >= 0 {
		title += e.FilePath[idx+1:]
	} else {
		title += e.FilePath
	}
	if e.Dirty && !e.ReadOnly {
		title += " *"
	}
	// Pad title to full width
	for len(title) < m.width {
		title += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[38;5;250m " + title + "\x1b[0m\n")

	// Editor content
	b.WriteString(e.Render())

	// Status bar — context-sensitive hints. Read-only buffers omit the
	// mutating affordances (save, paste, cut); copy still works on a selection.
	var status string
	switch {
	case e.SaveErr != "":
		status = fmt.Sprintf(" \x1b[31mError: %s\x1b[0m\x1b[48;5;236m\x1b[38;5;250m    Ln %d, Col %d", e.SaveErr, e.CursorRow+1, e.CursorCol+1)
	case e.Sel != nil && !e.Sel.IsEmpty():
		if e.ReadOnly {
			status = fmt.Sprintf(" Enter copy  Esc clear    Ln %d, Col %d", e.CursorRow+1, e.CursorCol+1)
		} else {
			status = fmt.Sprintf(" Enter copy  Ctrl+X cut  Esc clear    Ln %d, Col %d", e.CursorRow+1, e.CursorCol+1)
		}
	case e.ReadOnly:
		// Selection and copy already worked here, but nothing said so — the
		// hint listed only "Esc close", so a user who opened a history entry to
		// copy it had no way to discover Ctrl+A or drag-select.
		status = fmt.Sprintf(" Ctrl+A select all  drag/shift+arrows select  Esc close    Ln %d, Col %d", e.CursorRow+1, e.CursorCol+1)
	default:
		status = fmt.Sprintf(" Ctrl+S save  Ctrl+V paste  Esc close    Ln %d, Col %d", e.CursorRow+1, e.CursorCol+1)
	}
	for len(status) < m.width {
		status += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[38;5;250m" + status + "\x1b[0m")

	return b.String()
}

func (m Model) renderStatusBar() string {
	// Left side: pane info
	left := "quil"
	if m.renamingPane {
		left = "Rename pane: " + m.paneRenameInput + "▎"
	} else if tab := m.activeTabModel(); tab != nil {
		paneCount := 0
		if tab.Root != nil {
			paneCount = len(tab.Leaves())
		}
		paneInfo := fmt.Sprintf("tab %d/%d  panes:%d", m.activeTabIdx()+1, len(m.curTabs()), paneCount)

		if pane := tab.ActivePaneModel(); pane != nil {
			displayPath := pane.CWD
			if displayPath == "" {
				displayPath = pane.Name
			}
			if displayPath == "" {
				if len(pane.ID) > 8 {
					displayPath = pane.ID[:8]
				} else {
					displayPath = pane.ID
				}
			}
			left = fmt.Sprintf("%s  %s", displayPath, paneInfo)
			if seg := modelStatusSegment(pane.Model, pane.ContextTokens); seg != "" {
				left = fmt.Sprintf("%s  %s  %s", displayPath, seg, paneInfo)
			}
			if tab.FocusMode() {
				left = "[focus] " + left
			}
			if m.notesMode && m.notesEditor != nil {
				var marker string
				if m.notesPaneFocused {
					marker = "[notes pane]"
				} else if m.notesEditor.Dirty() {
					marker = "[notes*]"
				} else {
					marker = "[notes]"
				}
				left = marker + " " + left
			}
			if pane.scrollBack > 0 {
				left += fmt.Sprintf("  ↑%d", pane.scrollBack)
			}
		} else {
			left = paneInfo
		}
	}

	// Pending prefix, or the message from the last dropped sequence. Leftmost
	// because it is transient and outranks everything beside it: an armed
	// machine that shows nothing is indistinguishable from a frozen TUI, and a
	// sequence that silently did nothing is the support ticket this prevents.
	if len(m.pendingSeq) > 0 {
		left = keymap.Sequence(m.pendingSeq).String() + "… " + left
	} else if m.seqFlash != "" {
		left = m.seqFlash + " " + left
	}

	// Right side: keybinding hints + version
	right := "^T tab | ^N pane | ^W close | F1 help | ^Q quit | v" + m.version
	if m.lastMemResp != nil {
		total := m.lastMemResp.Total + m.tuiLocalMemTotal()
		right = "mem " + memreport.HumanBytes(total) + " | " + right
	}
	// Suppressed in remote mode: the announcement describes the REMOTE
	// daemon's staging dir, but every apply path reads the LOCAL
	// config.UpdateDir(). Offering "↑ v1.43.0 [ready]" here would either fail
	// ("staged files missing") or, worse, silently install whatever different
	// version this machine happens to have staged. Showing nothing is better
	// than showing a control wired to the wrong host. activeUpdateInfo (not a
	// single last-broadcast field) is the other half: a mixed session would
	// otherwise pass this gate with a LOCAL project active and then render the
	// REMOTE daemon's version.
	// remoteModeFor(activeDest()) as well as RemoteMode(): the session-wide flag
	// misses a MIXED session, where the TUI is local but the project on screen
	// belongs to another host — and there this rendered "↑ v1.2.3 ready" for a
	// release staged on that host, beside an About row that (correctly) says
	// updates apply locally. Neither is the machine the segment implies.
	if !m.RemoteMode() && !m.remoteModeFor(m.activeDest()) {
		if seg := updateStatusSegment(m.activeUpdateInfo(), m.version); seg != "" {
			right = seg + " | " + right
		}
	}
	if m.devMode {
		right = "[dev] " + right
	}
	// Placed after [dev] so it renders leftmost of the two, i.e. first in
	// reading order: which MACHINE you are driving outranks which build you
	// are running. Without it the status bar is identical whether the panes
	// are on this laptop or a cluster node, which is what makes any
	// wrong-host bug silent — and these panes routinely run AI agents with
	// permission prompts disabled.
	//
	// Gated on the DEST, not on the rendered host name: linkHost answers "the
	// local daemon" for "", so testing its output for emptiness would put a
	// "[remote …]" badge on every local session.
	if dest := m.activeDest(); dest != "" {
		right = "[remote " + m.linkHost(dest) + "] " + right
	}
	if count := m.notifications.Count(); count > 0 && !m.notifications.visible {
		right = fmt.Sprintf("[%d events] ", count) + right
	}
	if m.flashText != "" && time.Now().Before(m.flashUntil) {
		right = m.flashText + " | " + right
	}

	// Fit within width: left takes priority
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2 // 2 for padding
	if gap < 2 {
		// Not enough room for hints
		return statusBarStyle.Width(m.width).Render(left)
	}

	spacer := strings.Repeat(" ", gap)
	return statusBarStyle.Width(m.width).Render(left + spacer + right)
}

// flashDuration is how long a flash message stays in the status bar.
const flashDuration = 3 * time.Second

// flashExpireMsg is sent by flashCmd when the flash timer fires.
type flashExpireMsg struct{}

// flashCmd returns a tea.Cmd that fires flashExpireMsg after flashDuration.
// The Update handler re-checks flashUntil to avoid clobbering a newer flash.
func (m Model) flashCmd() tea.Cmd {
	return tea.Tick(flashDuration, func(time.Time) tea.Msg { return flashExpireMsg{} })
}

// setFlash shows a transient message in the status bar for flashDuration.
// The 1 s sizePollTick is a backstop; flashCmd provides a crisp expiry timer.
func (m *Model) setFlash(text string) {
	m.flashText = text
	m.flashUntil = time.Now().Add(flashDuration)
}

// nextReqGen returns a fresh instance id for a one-shot request whose content
// key (a CWD, or a (path, child) pair) can repeat across genuinely different
// requests — repoScanState, browseState, and worktreeState all use it, mirroring clientGen's
// role for the reconnect dial: without it, a slot that matches on content
// alone cannot tell "this is the answer to the request I just asked" from
// "this is a late answer to a PREVIOUS request that happened to ask about the
// same thing". Shared by both slots rather than one counter each — a
// generation only ever has to be unique within its OWN slot's comparisons,
// never across slots, so splitting the source buys nothing.
func (m *Model) nextReqGen() string {
	m.reqGen++
	return strconv.Itoa(m.reqGen)
}

// Daemon communication commands

// attachCWD returns the working directory to advertise to the daemon so it can
// spawn new panes where the client is.
//
// Empty for a remote destination: os.Getwd() names a directory on the laptop,
// and the daemon would test it against the SERVER's disk. defaultCWD() validates
// and falls back, so this was safe — but only by coincidence, and a path that
// happens to exist on both machines is exactly where the coincidence stops. An
// empty value makes the daemon use its own working directory, which is a real
// directory on the machine that will hold the pane.
//
// Keyed on the destination being attached, not on a session-wide flag: a mixed
// session attaches a local daemon and a remote one from the same client, and
// only the local one may be told where this process is standing.
func attachCWD(dest, localCWD string) string {
	if dest != "" {
		return ""
	}
	return localCWD
}

// knownDests lists every destination this client can talk to.
//
// A router answers with its whole routing table; anything else is a single
// connection, whose destination is "" — the key its projects, its link loss and
// its reconnect state all already use.
func (m Model) knownDests() []string {
	if r, ok := m.client.(*Router); ok {
		return r.Dests()
	}
	return []string{""}
}

// attachAllDests attaches every destination that is not attached yet, using the
// geometry the terminal has reported. Returns nil when there is nothing to do.
//
// The attached map is what keeps this idempotent, and idempotence is the whole
// requirement: handleAttach replays the ENTIRE ghost buffer on every attach, so
// a second one doubles a pane's scrollback and re-counts the work-state events
// in the replay (resetWorkStateForReattach exists for the same hazard). That is
// also why attach could not simply move to dial time in cmd/quil — attach
// already has two owners, this one and the post-redial reattach, and a third
// would attach every conn twice.
//
// Nor can it move to dial time for a second, independent reason: AttachPayload
// carries Cols/Rows and the daemon sizes the first PTY from them, while at dial
// time Bubble Tea has not reported a window size yet — a fresh daemon would
// spawn its default Shell pane at 0×0.
//
// A send that fails leaves the destination unattached deliberately, so the next
// WindowSizeMsg retries it.
func (m *Model) attachAllDests() tea.Cmd {
	// Sends run HERE rather than inside the returned command, because the ledger
	// entry has to land on a Model that Update returns — a command holds a copy
	// and could not write one back. That makes a client-less Model reachable in
	// a way the old command-wrapped attach never was: dozens of tests drive
	// Update on a Model they never gave a connection to.
	if m.client == nil {
		return nil
	}
	if m.attached == nil {
		m.attached = map[string]bool{}
	}
	var cmds []tea.Cmd
	// Set, not a bool: the overlay-truth report below must be scoped to
	// exactly these destinations, not to "at least one attached this round"
	// (see the comment on that report).
	newlyAttached := map[string]bool{}
	for _, dest := range m.knownDests() {
		if m.attached[dest] {
			continue
		}
		if err := m.sendForDest(dest, m.attachMessage(dest)); err != nil {
			log.Printf("attach to %q failed, retrying on the next resize: %v", dest, err)
			continue
		}
		m.attached[dest] = true
		newlyAttached[dest] = true
		// Batched per destination so each daemon is asked about its OWN
		// registry; see requestPluginListFor.
		if cmd := m.requestPluginListFor(dest); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// Batched ONCE per round, not per destination like requestPluginListFor:
	// overlayPolicyCmd already loops over every known dest itself, so
	// calling it inside the loop above would resend the same two ints once
	// per newly-attached destination. Gated on len(newlyAttached) because
	// this function reruns on every WindowSizeMsg (see the comment on the
	// unattached-retry above) — an unconditional push would resend the
	// current policy on every resize, not just after a fresh attach.
	if len(newlyAttached) > 0 {
		if cmd := m.overlayPolicyCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		// Rides the same post-attach path as the policy, and for the same
		// reason: this is the only moment the daemon's copy of overlay
		// visibility can be stale, in either direction. A TUI that exited with
		// an overlay on screen left it marked visible (never swept); a
		// transient last-client disconnect stamped every overlay hidden, and a
		// client that reconnects with one still on screen would otherwise
		// watch the sweep destroy it five minutes later. Last attacher wins,
		// the same trade MsgOverlayPolicy already documents.
		//
		// Scoped to the destinations that JUST attached, never to every
		// destination this client knows about: a destination that was
		// already attached did not just drop, so its overlays' visibility
		// cannot have gone stale, and re-reporting it anyway would re-stamp
		// their OverlayShownAt on every unrelated attach round — perturbing
		// enforceOverlayCap's LRU order for an overlay that had nothing to do
		// with this one. overlayTruthDestCmd already makes exactly this
		// trade for the reconnect path; this is that same scoping applied to
		// a round that can attach more than one destination at once.
		if cmd := m.overlayTruthCmds(func(t *TabModel) bool { return newlyAttached[t.Dest] }); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// attachMessage builds the MsgAttach describing this client's geometry for one
// destination.
func (m Model) attachMessage(dest string) *ipc.Message {
	// Subtract chrome (tab bar + status bar), the project sidebar (if open),
	// then pane border (2) — the same reservation resizeTabs applies, so the
	// very first spawned pane isn't sized wider than what's about to be
	// painted for it.
	tabH := m.height - chromeHeight
	cols := m.paneAreaWidth() - 2
	rows := tabH - 2
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	// Best-effort; if Getwd fails the daemon falls back to its own CWD.
	localCWD, _ := os.Getwd()
	msg, _ := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{
		Cols: cols,
		Rows: rows,
		CWD:  attachCWD(dest, localCWD),
	})
	return msg
}

// attachToDest re-attaches to ONE daemon after its link came back.
//
// Stamped, because by now the client knows which destination it is talking to
// and an unstamped attach resolves to whatever project is ACTIVE: a background
// daemon reconnecting would make the foreground one replay its whole output
// buffer, doubling every pane's scrollback on the machine that never dropped.
// Harmless on the single-connection path — Origin never reaches the wire.
//
// requestPluginListFor rides along rather than being asked at Ctrl+N: .Available
// is also read by the context menu, the palette and the Alt+G overlay, so asking
// only on dialog open would leave those describing the wrong machine. Reconnect
// is where a daemon that restarted — and therefore re-detected — shows up.
//
// The overlay-visibility report rides along for a sharper reason: the drop this
// function recovers from is ITSELF what made the daemon stamp every overlay
// hidden (the last attached client went away), so without re-asserting the
// truth here the sweep destroys an overlay the user is looking at five minutes
// after a link blip. attachAllDests never runs for this flow — finishReconnect
// sets m.attached[dest] — so its copy of this report does not cover it.
func (m Model) attachToDest(dest string) tea.Cmd {
	attachCmd := func() tea.Msg {
		m.sendForDest(dest, m.attachMessage(dest))
		return nil
	}
	return tea.Batch(attachCmd, m.requestPluginListFor(dest), m.overlayTruthDestCmd(dest))
}

// listenContinueMsg signals the TUI to keep listening for daemon messages.
type listenContinueMsg struct{}

// paneDisplayName resolves the human-readable label confirm dialogs show
// for a pane: explicit name, else CWD, else the truncated pane id.
func paneDisplayName(pane *PaneModel) string {
	if pane.Name != "" {
		return pane.Name
	}
	if pane.CWD != "" {
		return pane.CWD
	}
	if len(pane.ID) > 8 {
		return pane.ID[:8]
	}
	return pane.ID
}

func (m Model) listenForMessages() tea.Cmd {
	if m.listenCountFn != nil {
		m.listenCountFn()
	}
	return func() tea.Msg {
		msg, err := m.client.Receive()
		if err != nil {
			log.Printf("listen error (gen %d): %v", m.clientGen, err)
			// Reported as data, not as a quit. Update decides whether this is a
			// reconnectable drop or a fatal one — the MsgCloseTUI case below is
			// the deliberate-exit path and must stay distinguishable from it.
			return linkLostMsg{gen: m.clientGen, err: err}
		}

		switch msg.Type {
		case ipc.MsgLinkLost:
			// Synthesised by the Router when one of its connections died — it
			// never reaches a socket. Receive itself cannot report that error,
			// because the other daemons are still up, so the loss arrives as
			// data naming the dest that died.
			//
			// Honoured ONLY from a router, because nothing stops a daemon from
			// putting this type on the wire: acting on it would let the peer
			// quit the TUI in local mode, or start reconnect churn in remote
			// mode — where the daemon is on a host the user may not control.
			// Origin is json:"-", so a wire-borne one would also arrive naming
			// the local daemon whatever its true source.
			if _, isRouter := m.client.(destRouter); !isRouter {
				log.Printf("ipc recv: ignoring wire-borne link_lost")
				return listenContinueMsg{}
			}
			// The generation is the CURRENT one: a router's pumps are not
			// superseded the way a whole client swap is, and a zero here would
			// be discarded as stale.
			log.Printf("ipc recv: link_lost from %q", msg.Origin)
			return linkLostMsg{gen: m.clientGen, dest: msg.Origin, err: errLinkLost}

		case ipc.MsgPaneOutput:
			var payload ipc.PaneOutputPayload
			msg.DecodePayload(&payload)
			return PaneOutputMsg{PaneID: payload.PaneID, Data: payload.Data, Ghost: payload.Ghost}

		case ipc.MsgWorkspaceState:
			log.Print("ipc recv: workspace_state")
			var raw map[string]any
			msg.DecodePayload(&raw)
			state := parseWorkspaceState(raw)
			// Origin is client-side routing state, never on the wire, so it
			// cannot come out of the payload — stamp it here, where the message
			// still exists. Empty means the local daemon.
			state.Dest = msg.Origin
			return state

		case ipc.MsgPluginError:
			log.Printf("ipc recv: plugin_error")
			var payload ipc.PluginErrorPayload
			msg.DecodePayload(&payload)
			return PluginErrorMsg{
				PaneID:  payload.PaneID,
				Title:   payload.Title,
				Message: payload.Message,
			}

		case ipc.MsgSetActivePane:
			var payload ipc.SetActivePanePayload
			msg.DecodePayload(&payload)
			log.Printf("ipc recv: set_active_pane %s", payload.PaneID)
			return setActivePaneMsg{PaneID: payload.PaneID}

		case ipc.MsgCloseTUI:
			log.Print("ipc recv: close_tui")
			return tea.QuitMsg{}

		case ipc.MsgHighlightPane:
			var payload ipc.HighlightPanePayload
			msg.DecodePayload(&payload)
			return highlightPaneMsg{PaneID: payload.PaneID}

		case ipc.MsgPaneEvent:
			var payload ipc.PaneEventPayload
			msg.DecodePayload(&payload)
			log.Printf("ipc recv: pane_event %s %s %s", payload.Type, payload.PaneID, payload.Title)
			return paneEventMsg(payload)

		case ipc.MsgMemoryReportResp:
			var payload ipc.MemoryReportRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode memory_report_resp: %v", err)
				return listenContinueMsg{}
			}
			return memoryReportMsg{Resp: payload}

		case ipc.MsgPaneHistoryResp:
			var payload ipc.PaneHistoryRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode pane_history_resp: %v", err)
				return listenContinueMsg{}
			}
			return historyListMsg{Resp: payload}

		case ipc.MsgPaneHistoryEntryResp:
			var payload ipc.PaneHistoryEntryRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode pane_history_entry_resp: %v", err)
				return listenContinueMsg{}
			}
			return historyEntryMsg{Resp: payload}

		case ipc.MsgPaneSearchResp:
			var payload ipc.PaneSearchRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode pane_search_resp: %v", err)
				return listenContinueMsg{}
			}
			return paneSearchRespMsg{Resp: payload}

		case ipc.MsgClaudeSessionsResp:
			var payload ipc.ClaudeSessionsRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode claude_sessions_resp: %v", err)
				return listenContinueMsg{}
			}
			return claudeSessionsRespMsg{Resp: payload}

		case ipc.MsgGitReposResp:
			var payload ipc.GitReposRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode git_repos_resp: %v", err)
				return listenContinueMsg{}
			}
			// msg.ID echoes the requesting message's ID verbatim (respondTo on
			// the daemon side) — this is the request instance correlator
			// applyGitRepos needs on top of the echoed CWD; see repoScanState.gen.
			return gitReposMsg{Resp: payload, Gen: msg.ID}

		case ipc.MsgKubeCtxResp:
			var payload ipc.KubeCtxRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode kube_ctx_resp: %v", err)
				return listenContinueMsg{}
			}
			// Same correlator as MsgGitReposResp above; see kubeScanState.gen.
			return kubeCtxMsg{Resp: payload, Gen: msg.ID}

		case ipc.MsgWorktreeListResp:
			var resp ipc.WorktreeListRespPayload
			if err := msg.DecodePayload(&resp); err != nil {
				log.Printf("worktree list: decode: %v", err)
				return listenContinueMsg{}
			}
			return worktreeListMsg{Resp: resp, Gen: msg.ID}

		case ipc.MsgWorktreeStatusResp:
			var resp ipc.WorktreeStatusRespPayload
			if err := msg.DecodePayload(&resp); err != nil {
				log.Printf("worktree status: decode: %v", err)
				return listenContinueMsg{}
			}
			return worktreeStatusMsg{Resp: resp, Gen: msg.ID}

		case ipc.MsgCreatePaneResp:
			var resp ipc.CreatePaneRespPayload
			if err := msg.DecodePayload(&resp); err != nil {
				log.Printf("create pane resp: decode: %v", err)
				return listenContinueMsg{}
			}
			return createPaneRespMsg{Resp: resp, Dest: msg.Origin}

		case ipc.MsgDirsExistResp:
			var payload ipc.DirsExistRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode dirs_exist_resp: %v", err)
				return listenContinueMsg{}
			}
			// The echoed ID is the ONLY correlator here — unlike the browse and
			// git responses there is no content key, because a path list makes a
			// poor staleness key. See recentScanState.gen.
			return recentDirsMsg{Resp: payload, Gen: msg.ID}

		case ipc.MsgPluginListResp:
			var payload ipc.PluginListRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode plugin_list_resp: %v", err)
				return listenContinueMsg{}
			}
			return pluginListMsg{Resp: payload}

		case ipc.MsgBrowseDirResp:
			var payload ipc.BrowseDirRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode browse_dir_resp: %v", err)
				return listenContinueMsg{}
			}
			// Same correlator as MsgGitReposResp above; see browseState.gen.
			return browseDirMsg{Resp: payload, Gen: msg.ID}

		case ipc.MsgClaudeSessionDetailResp:
			var payload ipc.ClaudeSessionDetailRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode claude_session_detail_resp: %v", err)
				return listenContinueMsg{}
			}
			return claudeSessionDetailRespMsg{Resp: payload}

		case ipc.MsgRestartPaneResp:
			// Response to the Alt+R restart confirm. The respawned pane
			// announces itself through the normal workspace_state /
			// pane_output flow; here we only log the outcome.
			var payload ipc.RestartPaneRespPayload
			msg.DecodePayload(&payload)
			log.Printf("ipc recv: restart_pane_resp pane=%s success=%v", payload.PaneID, payload.Success)
			return listenContinueMsg{}

		case ipc.MsgStageUpdateResp:
			// Honoured ONLY from the LOCAL daemon, the MsgLinkLost gate's
			// reasoning applied to a reply rather than a synthetic message:
			// applying swaps THIS machine's binaries out of THIS machine's
			// staging dir, and sendStageUpdateReq never asks anyone else. An
			// unsolicited one from a remote daemon would otherwise consume the
			// pending apply intent and name its own version on the confirm.
			if msg.Origin != "" {
				log.Printf("ipc recv: ignoring stage_update_resp from %q", msg.Origin)
				return listenContinueMsg{}
			}
			var payload ipc.StageUpdateRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode stage_update_resp: %v", err)
				return listenContinueMsg{}
			}
			return stageUpdateRespMsg{Resp: payload}

		default:
			log.Printf("ipc recv: unknown type %q", msg.Type)
			return listenContinueMsg{}
		}
	}
}

func parseWorkspaceState(raw map[string]any) WorkspaceStateMsg {
	state := WorkspaceStateMsg{}
	if at, ok := raw["active_tab"].(string); ok {
		state.ActiveTab = at
	}
	if u, ok := raw["update"].(map[string]any); ok {
		info := &ipc.UpdateInfo{}
		if s, ok := u["latest_version"].(string); ok {
			info.LatestVersion = s
		}
		if s, ok := u["release_url"].(string); ok {
			info.ReleaseURL = s
		}
		if s, ok := u["staged_version"].(string); ok {
			info.StagedVersion = s
		}
		if b, ok := u["install_writable"].(bool); ok {
			info.InstallWritable = b
		}
		if info.LatestVersion != "" {
			state.Update = info
		}
	}
	if ap, ok := raw["active_project"].(string); ok {
		state.ActiveProject = ap
	}
	if projects, ok := raw["projects"].([]any); ok {
		for _, p := range projects {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			pi := ProjectInfo{}
			if id, ok := pm["id"].(string); ok {
				pi.ID = id
			}
			if name, ok := pm["name"].(string); ok {
				pi.Name = name
			}
			if root, ok := pm["root_dir"].(string); ok {
				pi.RootDir = root
			}
			if at, ok := pm["active_tab"].(string); ok {
				pi.ActiveTab = at
			}
			if b, ok := pm["bootstrap"].(bool); ok {
				pi.Bootstrap = b
			}
			if ids, ok := pm["tab_ids"].([]any); ok {
				for _, tid := range ids {
					if s, ok := tid.(string); ok {
						pi.TabIDs = append(pi.TabIDs, s)
					}
				}
			}
			// A project with no ID cannot be matched against the client's own
			// list, so every broadcast would rebuild it from scratch.
			if pi.ID != "" {
				state.Projects = append(state.Projects, pi)
			}
		}
	}
	if tabs, ok := raw["tabs"].([]any); ok {
		for _, t := range tabs {
			if tm, ok := t.(map[string]any); ok {
				ti := TabInfo{}
				if id, ok := tm["id"].(string); ok {
					ti.ID = id
				}
				if name, ok := tm["name"].(string); ok {
					ti.Name = name
				}
				if pid, ok := tm["project_id"].(string); ok {
					ti.ProjectID = pid
				}
				if color, ok := tm["color"].(string); ok {
					ti.Color = color
				}
				if panes, ok := tm["panes"].([]any); ok {
					for _, p := range panes {
						if s, ok := p.(string); ok {
							ti.Panes = append(ti.Panes, s)
						}
					}
				}
				if layout, ok := tm["layout"]; ok && layout != nil {
					// Re-marshal the nested map back to json.RawMessage
					if data, err := json.Marshal(layout); err == nil {
						ti.Layout = data
					}
				}
				state.Tabs = append(state.Tabs, ti)
			}
		}
	}
	if panes, ok := raw["panes"].([]any); ok {
		for _, p := range panes {
			if pm, ok := p.(map[string]any); ok {
				pi := PaneInfo{}
				if id, ok := pm["id"].(string); ok {
					pi.ID = id
				}
				if tabID, ok := pm["tab_id"].(string); ok {
					pi.TabID = tabID
				}
				if cwd, ok := pm["cwd"].(string); ok {
					pi.CWD = cwd
				}
				if name, ok := pm["name"].(string); ok {
					pi.Name = name
				}
				if typ, ok := pm["type"].(string); ok {
					pi.Type = typ
				}
				if muted, ok := pm["muted"].(bool); ok {
					pi.Muted = muted
				}
				if eager, ok := pm["eager"].(bool); ok {
					pi.Eager = eager
				}
				if pinned, ok := pm["pinned_attention"].(bool); ok {
					pi.PinnedAttention = pinned
				}
				if overlay, ok := pm["overlay"].(bool); ok {
					pi.Overlay = overlay
				}
				if pending, ok := pm["pending"].(bool); ok {
					pi.Pending = pending
				}
				if sid, ok := pm["session_id"].(string); ok {
					pi.SessionID = sid
				}
				if hl, ok := pm["history_lines"].(float64); ok {
					pi.HistoryLines = int(hl)
				}
				if mt, ok := pm["mouse_tracking"].(bool); ok {
					pi.MouseTracking = mt
				}
				if ms, ok := pm["mouse_sgr"].(bool); ok {
					pi.MouseSGR = ms
				}
				if bp, ok := pm["bracketed_paste"].(bool); ok {
					pi.BracketedPaste = bp
				}
				if model, ok := pm["model"].(string); ok {
					pi.Model = model
				}
				if ct, ok := pm["context_tokens"].(float64); ok {
					pi.ContextTokens = int64(ct)
				}
				if s, ok := pm["spawn_error"].(string); ok {
					pi.SpawnError = s
				}
				if b, ok := pm["git_branch"].(string); ok {
					pi.GitBranch = b
				}
				if b, ok := pm["git_detached"].(bool); ok {
					pi.GitDetached = b
				}
				if b, ok := pm["git_worktree"].(bool); ok {
					pi.GitWorktree = b
				}
				if s, ok := pm["git_worktree_name"].(string); ok {
					pi.GitWorktreeName = s
				}
				if b, ok := pm["worktree_owned"].(bool); ok {
					pi.WorktreeOwned = b
				}
				if s, ok := pm["worktree_path"].(string); ok {
					pi.WorktreePath = s
				}
				if b, ok := pm["git_upstream"].(bool); ok {
					pi.GitUpstream = b
				}
				if n, ok := pm["git_ahead"].(float64); ok {
					pi.GitAhead = int(n)
				}
				if n, ok := pm["git_behind"].(float64); ok {
					pi.GitBehind = int(n)
				}
				if b, ok := pm["git_stale"].(bool); ok {
					pi.GitStale = b
				}
				// Range-checked: Go leaves an out-of-range float→integer
				// conversion implementation-dependent, so a daemon reporting
				// 1e300, -1 or NaN would yield an unspecified uint16 rather
				// than an obviously wrong one. Out-of-range is left at 0,
				// which reads as "never sized" and re-sends — the safe
				// direction, since the alternative is suppressing a resize
				// against a garbage comparison.
				if n, ok := pm["cols"].(float64); ok && n >= 0 && n <= math.MaxUint16 {
					pi.Cols = uint16(n)
				}
				if n, ok := pm["rows"].(float64); ok && n >= 0 && n <= math.MaxUint16 {
					pi.Rows = uint16(n)
				}
				state.Panes = append(state.Panes, pi)
			}
		}
	}
	return state
}

func (m *Model) splitPane(dir SplitDir) tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}

	// The same in-flight refusal handleCreatePaneSplit makes, and it belongs
	// here for one MORE reason than it does there. pendingSplit is keyed by
	// tab, so a second placeholder overwrites the leaf the create reserved and
	// the pane still on its way lands nowhere. On top of that, renderNode hands
	// the TAB's CreatingBranch to every placeholder leaf — correct only while a
	// tab holds at most one — so the split's own placeholder would render
	// "Creating worktree <branch>" while having no worktree at all.
	if inflight := m.worktreeCreates[tab.ID]; inflight != "" {
		m.setFlash("still creating the worktree for " + truncateCells(sanitizeRemoteText(inflight), createErrFlashCap) + " — wait for it to finish")
		return m.flashCmd()
	}

	// Split the active pane's leaf, creating a placeholder for the new pane.
	placeholder := tab.SplitAtPane(pane.ID, dir)
	if placeholder == nil {
		return nil
	}

	// Track the placeholder so applyWorkspaceState can fill it.
	if m.pendingSplit == nil {
		m.pendingSplit = make(map[string]*LayoutNode)
	}
	m.pendingSplit[tab.ID] = placeholder
	// The same node-local label the dialog path records. The payload below
	// carries no Type and the daemon normalises an empty one to terminal, so
	// this is what the pane will be rather than a guess about it.
	placeholder.phType = "terminal"

	tabID, dest := tab.ID, tab.Dest
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID: tabID,
		})
		m.sendForDest(dest, msg)
		return nil
	}
}

func (m Model) updateTab(tabID, name, color string) tea.Cmd {
	dest := m.destOfTab(tabID)
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgUpdateTab, ipc.UpdateTabPayload{
			TabID: tabID,
			Name:  name,
			Color: color,
			// An empty color here always means "back to default" — both the
			// color cycle wrap and a rename of an uncolored tab want the tab
			// to end up colorless, so the flag is safe to derive.
			ClearColor: color == "",
		})
		m.sendForDest(dest, msg)
		return nil
	}
}

// sendReorderTab fires a MsgReorderTab IPC for a drag-induced tab move.
// The daemon snapshots + broadcasts; the next workspace_state arriving at
// the TUI just confirms what we already rearranged locally.
func (m Model) sendReorderTab(tabID string, newIdx int) tea.Cmd {
	dest := m.destOfTab(tabID)
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgReorderTab, ipc.ReorderTabPayload{
			TabID:    tabID,
			NewIndex: newIdx,
		})
		if m.client != nil {
			_ = m.sendForDest(dest, msg)
		}
		return nil
	}
}

func (m Model) cycleTabColor() tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}

	// Find current color index and cycle to next
	idx := 0
	for i, c := range tabColors {
		if c == tab.Color {
			idx = i
			break
		}
	}
	idx = (idx + 1) % len(tabColors)
	tab.Color = tabColors[idx]

	return m.updateTab(tab.ID, tab.Name, tab.Color)
}

// tryPluginRawKey returns the PTY bytes for the given key if the active pane's
// plugin has opted into raw passthrough for it (via the plugin's RawKeys list).
// Returns nil when there is no active pane, the plugin doesn't claim the key,
// or the key has no encoding in keyToBytes.
//
// The linear scan over RawKeys is intentional: lists are tiny in practice
// (≤5 entries), and the loader caps len(RawKeys) at load time so a hostile
// TOML cannot turn this into a per-keystroke hot path.
func (m Model) tryPluginRawKey(key string, keyMsg tea.KeyPressMsg) []byte {
	// Guard against zero-value Model{} (which is the shape used in unit tests
	// where the registry isn't wired). Production always sets pluginRegistry
	// in NewModel, so this branch is purely defensive.
	if m.pluginRegistry == nil {
		return nil
	}
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}
	paneType := pane.Type
	if paneType == "" {
		paneType = "terminal" // legacy panes without an explicit type
	}
	p := m.pluginRegistry.Get(paneType)
	if p == nil {
		return nil
	}
	for _, rk := range p.Command.RawKeys {
		if rk == key {
			return keyToBytes(keyMsg)
		}
	}
	return nil
}

// paneInput is one ordered chunk of PTY-bound input for a pane. Both the pane
// and its OWNING DAEMON are resolved synchronously on the Update goroutine at
// enqueue time. The pane, so a later active-pane change cannot misroute
// already-typed bytes; the dest, because destOfPane walks m.projects, which the
// Update goroutine rebuilds on every workspace broadcast — resolving it on the
// forwarder goroutine would be a data race, and a stale answer would type into
// another machine's pane.
type paneInput struct {
	dest   string
	paneID string
	data   []byte
}

// inputForwardBuffer bounds the ordered input queue between the Update loop and
// inputForwarder. Generous because the forwarder normally drains far faster
// than a human types — the buffer only absorbs brief bursts (e.g. a fast
// key-repeat).
//
// client.Send is no longer strictly non-blocking: it waits up to
// ipc.clientSendTimeout for room on the conn's critical queue before declaring
// the peer wedged. That wait is what sizes this buffer's worst case — see
// enqueueInput, which blocks when the buffer is full.
const inputForwardBuffer = 1024

// forwardInputBytes queues keystroke bytes for the active pane's PTY.
//
// It returns a nil tea.Cmd on purpose, and that is the whole fix. This used to
// return a Cmd that did the send inside it; Bubble Tea runs every Cmd on its own
// goroutine with no inter-Cmd ordering (tea.go handleCommands: go func(){
// p.Send(cmd()) }), so one goroutine per keystroke raced to the socket. The
// window is normally nanoseconds, but under scheduler starvation it widens to
// milliseconds and adjacent keys swap — typing "image containers" arrives as
// "iamg ecotniaesnr", the same characters in the wrong order. A Cmd buys nothing
// here — the enqueue below is a channel send, and the one goroutine draining it
// is where any waiting belongs — and it costs ordering.
func (m Model) forwardInputBytes(data []byte) tea.Cmd {
	if len(data) == 0 {
		// Bare modifiers and unencodable keys produce no PTY bytes — skip the
		// enqueue and the useless zero-length frame. (Live callers already
		// pre-filter nil, but this keeps the entry point self-defending.)
		return nil
	}
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}
	m.enqueueInput(pane.ID, data)
	return nil
}

// enqueueInput hands ordered PTY-input bytes to inputForwarder. EVERY producer
// of MsgPaneInput goes through here — keystrokes, wheel notches and paste all
// land on the same PTY stdin, so a direct send from any one of them could
// overtake bytes still queued from another.
//
// When the forwarder channel is absent — unit tests construct Model literally,
// and there is a brief pre-Init window — it falls back to a direct synchronous
// send, which also preserves order because the caller is single-threaded. Both
// paths fix wire order on the calling goroutine.
//
// The channel send blocks if the 1024-deep buffer ever fills. That is by design:
// dropping a keystroke silently corrupts the very input stream this whole change
// exists to keep correct, so we prefer momentary backpressure over loss.
//
// The drainer cannot die — forwardOne recovers from any panic — but it CAN
// park: client.Send waits up to ipc.clientSendTimeout for room on the conn's
// critical queue before it gives up and closes the link. So a full buffer is
// reachable in one case, a peer wedged for that long while 1024 further inputs
// arrive, and the cost is that this call blocks the Update goroutine until the
// timeout fires. That bound is the reason the timeout exists and is measured in
// seconds rather than deferred to sendLoop's 30 s write deadline: past the
// timeout the conn closes, every queued send fails instantly, and the drain
// completes.
//
// CONTRACT: data must not be mutated after the call. Every producer allocates a
// fresh slice (keyToBytes, wheelForwardSeq, pastePayload), and the forwarder
// marshals it on its own goroutine.
func (m Model) enqueueInput(paneID string, data []byte) {
	if len(data) == 0 || paneID == "" {
		return
	}
	dest := m.destOfPane(paneID)
	if m.inputCh == nil {
		m.sendPaneInput(dest, paneID, data)
		return
	}
	m.inputCh <- paneInput{dest: dest, paneID: paneID, data: data}
}

// sendPaneInput marshals and sends one MsgPaneInput frame to an ALREADY-RESOLVED
// destination. It deliberately takes dest rather than calling sendForPane: the
// forwarder goroutine must not walk m.projects (see paneInput). client.Send is
// non-blocking (the frame is queued on the conn's own send buffer), so this
// never stalls its caller.
func (m Model) sendPaneInput(dest, paneID string, data []byte) {
	msg, err := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
		PaneID: paneID,
		Data:   data,
	})
	if err != nil {
		log.Printf("pane input encode: %v", err)
		return
	}
	if err := m.sendForDest(dest, msg); err != nil {
		log.Printf("pane input send: %v", err)
	}
}

// inputForwarder drains the ordered input queue and forwards each entry in FIFO
// order. A single goroutine draining one channel means typed order is wire order.
//
// Lifecycle: started once in Init, stopped via StopInputForwarder on TUI exit
// (after tea.Program.Run returns, when the Update goroutine is gone). This
// matches the codebase's other connection-lifetime goroutines (idleChecker,
// hookEventsWatcher). The select over inputDone gives it the explicit shutdown
// path go-conventions wants; inputCh itself is never closed (the Update
// goroutine may still reference it, so closing would risk a send-on-closed
// panic) — closing inputDone is the clean stop signal instead.
// On stop it DRAINS what is already queued before returning. A bare return
// would silently discard input the Update goroutine had already accepted —
// exactly the loss enqueueInput blocks to avoid, reintroduced at exit. Draining
// is safe and terminates because StopInputForwarder runs after tea.Program.Run
// returns, so the Update goroutine is gone and the queue can only shrink; the
// non-blocking default is what stops a late tea.Cmd from holding shutdown open.
//
// The drain is needed even though inputDone is checked in the same select:
// with both cases ready, Go picks pseudo-randomly, so entries would be dropped
// only sometimes — the worst kind of loss to diagnose.
func (m Model) inputForwarder() {
	// Announce that the queue has reached the socket. StopInputForwarder waits
	// on this before the caller closes the client — signalling the drain
	// without awaiting it just moves the loss one layer down, from an
	// undrained channel to frames discarded by a closed connection.
	defer func() {
		if m.inputIdle != nil {
			close(m.inputIdle)
		}
	}()
	for {
		select {
		case <-m.inputDone:
			for {
				select {
				case in := <-m.inputCh:
					m.forwardOne(in)
				default:
					return
				}
			}
		case in := <-m.inputCh:
			m.forwardOne(in)
		}
	}
}

// forwardOne sends a single queued entry, isolating each send in its own recover
// scope. Without this, a panic anywhere under sendPaneInput would kill the sole
// drainer; the 1024 buffer would then fill and every subsequent keystroke would
// deadlock the blocking enqueue in Update — a silent, total input freeze.
// Recovering per-entry keeps the drainer immortal.
func (m Model) forwardOne(in paneInput) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("inputForwarder: recovered forwarding to pane %s: %v", in.paneID, r)
		}
	}()
	m.sendPaneInput(in.dest, in.paneID, in.data)
}

// inputDrainTimeout bounds how long TUI exit waits for queued input to reach
// the socket, so a stalled drain degrades to "exit anyway, having said so"
// instead of a TUI that will not quit.
//
// It is deliberately SHORTER than ipc.clientSendTimeout, so a peer wedged at
// exit is a bounded wait here rather than a bounded wait there: one parked
// client.Send can now exceed this on its own, and the log line that follows is
// then accurate rather than alarming. Exit is the one path where out-waiting
// the user is worse than losing the tail of a queue nobody will type into
// again.
const inputDrainTimeout = 2 * time.Second

// StopInputForwarder stops inputForwarder and WAITS for it to finish draining.
//
// The wait is the point. Closing inputDone only asks the forwarder to drain;
// the caller then closes the IPC client, and a connection closed mid-drain
// discards whatever had not yet been written — the same lost keystrokes, one
// layer further down. Blocking here is safe because it runs after
// tea.Program.Run returns: the Update goroutine is gone, so nothing can add to
// the queue and the drain is bounded by what is already in it.
//
// Safe to call once. No-op when the channels were never created (tests that
// construct Model literally). Wired from main.go's TUI-exit path, ahead of the
// client close.
func (m Model) StopInputForwarder() {
	if m.inputDone == nil {
		return
	}
	close(m.inputDone)
	if m.inputIdle == nil {
		return // no forwarder was started; nothing to wait for
	}
	select {
	case <-m.inputIdle:
	case <-time.After(inputDrainTimeout):
		log.Printf("inputForwarder: drain did not finish within %s — %d queued entries may not have been sent",
			inputDrainTimeout, len(m.inputCh))
	}
}

// clipboardReadText and clipboardReadImage indirect over the clipboard package
// so the paste flow can be exercised hermetically in tests (the real readers
// touch the OS clipboard). Production leaves them at the package functions.
var (
	clipboardReadText  = clipboard.Read
	clipboardReadImage = clipboard.ReadImage
)

func (m Model) pasteClipboard() tea.Cmd {
	// Bind the destination pane HERE — this runs on the Update goroutine, at
	// the moment the user asked to paste. See clipboardPastedMsg.
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}
	paneID := pane.ID
	return func() tea.Msg {
		logger.Debug("pasteClipboard: invoked")
		// Try text first. If text is non-empty, paste it as-is. Otherwise
		// fall through to image — this works around claude-code's broken
		// Windows clipboard image reader (anthropics/claude-code#32791) by
		// reading the image ourselves, saving it as a PNG under
		// config.PasteDir(), and pasting the absolute path so any PTY tool
		// can pick it up via its file-reading tools.
		text, textErr := clipboardReadText() // text-only error is non-fatal — fall through
		logger.Debug("pasteClipboard: clipboard.Read() text_len=%d err=%v", len(text), textErr)
		if text == "" {
			logger.Debug("pasteClipboard: text empty, attempting image fallback")
			if path, ok := m.tryPasteClipboardImage(); ok {
				logger.Debug("pasteClipboard: image fallback succeeded, path=%q", path)
				text = path
			} else {
				logger.Debug("pasteClipboard: image fallback returned no path")
			}
		}
		if text == "" {
			logger.Debug("pasteClipboard: nothing to paste, returning")
			return nil
		}
		// Hand the text back to Update rather than writing to the pane here.
		// Resolving the pane, wrapping the payload and enqueueing all read
		// Model state and must happen on the Update goroutine — see
		// clipboardPastedMsg.
		logger.Debug("pasteClipboard: read %d bytes for pane %s, handing to Update", len(text), paneID)
		return clipboardPastedMsg{text: text, paneID: paneID}
	}
}

// tryPasteClipboardImage attempts to read an image from the system clipboard,
// save it as a PNG under config.PasteDir(), and return the absolute path of
// the saved file. Returns ("", false) when no image is available or any step
// fails — the caller falls back to its existing text-paste path.
//
// This is the proxy that works around the broken claude-code clipboard image
// reader on Windows (anthropics/claude-code#32791): Quil grabs the image from
// the OS clipboard itself, drops it in a known location, and types the path
// into the PTY. Any AI tool with file-reading tools can then pick it up.
func (m Model) tryPasteClipboardImage() (string, bool) {
	pngBytes, err := clipboardReadImage()
	if err != nil {
		if !errors.Is(err, clipboard.ErrNoImage) {
			log.Printf("clipboard image: read failed: %v", err)
		}
		return "", false
	}
	if len(pngBytes) == 0 {
		return "", false
	}

	dir := config.PasteDir()
	// 0o700 — only the owner can list / read pasted screenshots. They may
	// contain sensitive material (passwords visible on screen, source code,
	// etc.) so we deliberately don't share with other local users.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("clipboard image: mkdir %q: %v", dir, err)
		return "", false
	}
	// Filename uses a timestamp + 8-byte random suffix so:
	//  - concurrent pastes can't collide,
	//  - a co-tenant on a Unix box (where the parent dir might be world-
	//    traversable through the user's home permissions) can't enumerate
	//    or guess the filename to read recently-pasted screenshots.
	now := time.Now()
	suffixBytes := make([]byte, 8)
	if _, rerr := crand.Read(suffixBytes); rerr != nil {
		// Cryptographic randomness is on every supported platform; if it
		// somehow fails, refuse to write rather than fall back to a
		// predictable name.
		log.Printf("clipboard image: rand: %v", rerr)
		return "", false
	}
	name := fmt.Sprintf("quil-paste-%s-%s.png",
		now.Format("20060102-150405"), hex.EncodeToString(suffixBytes))
	abs := filepath.Join(dir, name)

	// 0o600 — file inherits owner-only access from the directory above. We
	// belt-and-braces it on the file too in case the umask is permissive
	// or the directory was pre-existing with looser bits.
	if err := os.WriteFile(abs, pngBytes, 0o600); err != nil {
		log.Printf("clipboard image: write %s: %v", abs, err)
		return "", false
	}
	log.Printf("clipboard image: pasted %d bytes → %s", len(pngBytes), abs)
	return abs, true
}

func (m Model) pasteToDialog() tea.Cmd {
	return func() tea.Msg {
		text, err := clipboard.Read()
		if err != nil {
			log.Printf("clipboard read for dialog: %v", err)
			return nil
		}
		if text == "" {
			return nil
		}
		return dialogPasteMsg(text)
	}
}

func (m *Model) updateMouseSelection(tab *TabModel, curX, curY, tabH int) {
	if tab.Root == nil {
		return
	}

	var pane *PaneModel
	var ox, oy int

	if tab.FocusMode() {
		// Focus mode: active pane fills entire tab, tree splits don't apply.
		// ox is the pane's screen-absolute left edge, which is where the
		// project sidebar ends (see the PaneRect origin contract above
		// activePaneRectFocus) — mouseStartX/curX below are screen columns,
		// so a 0 here would shear every selection right by the sidebar width.
		pane = tab.ActivePaneModel()
		if pane == nil {
			return
		}
		ox = m.projectSidebarWidth()
		oy = 1 // tab bar
	} else {
		startRect := tab.Root.FindPaneRectAt(m.mouseStartX, m.mouseStartY, m.projectSidebarWidth(), 1, m.paneAreaWidth(), tabH)
		if startRect == nil {
			return
		}
		pane = startRect.Pane
		ox = startRect.OX
		oy = startRect.OY
	}

	// Wide-canvas preview panes have no 1:1 grid mapping — the visible rows
	// are wrapped/cropped segments of a wider emulator. Map both endpoints
	// through the layout inverse instead of the raw screen mapping below.
	if pane.previewMode() {
		startCol, startLine, _ := pane.previewPosAt(m.mouseStartX-ox-1, m.mouseStartY-oy-1)
		curCol, curLine, _ := pane.previewPosAt(curX-ox-1, curY-oy-1)
		m.selection = &Selection{
			PaneID: pane.ID,
			Anchor: SelectionAnchor{Col: startCol, Line: startLine},
			Cursor: SelectionAnchor{Col: curCol, Line: curLine},
		}
		return
	}

	sbLen := pane.vt.ScrollbackLen()

	// Convert start screen coords to pane-local
	startCol := m.mouseStartX - ox - 1
	startRow := m.mouseStartY - oy - 1
	startLine := sbLen - pane.scrollBack + startRow

	// Convert current screen coords to pane-local (clamp to same pane)
	curCol := curX - ox - 1
	curRow := curY - oy - 1
	curLine := sbLen - pane.scrollBack + curRow

	// Clamp
	w := pane.vt.Width()
	h := pane.vt.Height()
	if startCol < 0 {
		startCol = 0
	}
	if startCol >= w {
		startCol = w - 1
	}
	if curCol < 0 {
		curCol = 0
	}
	if curCol >= w {
		curCol = w - 1
	}
	if startLine < 0 {
		startLine = 0
	}
	if curLine < 0 {
		curLine = 0
	}
	maxLine := sbLen + h - 1
	if startLine > maxLine {
		startLine = maxLine
	}
	if curLine > maxLine {
		curLine = maxLine
	}

	m.selection = &Selection{
		PaneID: pane.ID,
		Anchor: SelectionAnchor{Col: startCol, Line: startLine},
		Cursor: SelectionAnchor{Col: curCol, Line: curLine},
	}
}

// isSelectionExtendKey returns true for the exact set of shift-modified
// keys handleSelectionKey knows how to extend a selection with. Any other
// shift-modified key (shift+tab, shift+enter, shift+F*, etc.) must bypass
// the selection handler so it can reach plugin raw-key handling and the
// PTY — otherwise typing those in a claude-code or shell pane silently
// does nothing.
func isSelectionExtendKey(key string) bool {
	switch key {
	case "shift+left", "shift+right", "shift+up", "shift+down",
		"ctrl+shift+left", "ctrl+shift+right",
		"ctrl+alt+shift+left", "ctrl+alt+shift+right":
		return true
	}
	return false
}

func (m Model) handleSelectionKey(key string) (tea.Model, tea.Cmd) {
	tab := m.activeTabModel()
	if tab == nil {
		return m, nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return m, nil
	}
	// Keyboard selection is disabled in preview mode: stepping a caret by
	// logical lines through a wrapped/cropped view is disorienting when the
	// visual and absolute row grids don't match 1:1. Mouse selection IS
	// supported in preview — see updateMouseSelection, which maps through
	// previewPosAt instead of the raw screen grid used below.
	if pane.previewMode() {
		return m, nil
	}

	sbLen := pane.vt.ScrollbackLen()

	// Initialize selection at VT cursor position if not started
	if m.selection == nil {
		pos := pane.vt.CursorPosition()
		absLine := sbLen + pos.Y
		m.selection = &Selection{
			PaneID: pane.ID,
			Anchor: SelectionAnchor{Col: pos.X, Line: absLine},
			Cursor: SelectionAnchor{Col: pos.X, Line: absLine},
		}
	}

	cur := m.selection.Cursor
	maxLine := lastContentLine(pane)
	switch key {
	case "shift+right":
		cur.Col++
	case "shift+left":
		cur.Col--
	case "shift+down":
		cur.Line++
	case "shift+up":
		cur.Line--
	case "ctrl+shift+right":
		cur = selWordJump(pane, cur, 1, 1, maxLine)
	case "ctrl+shift+left":
		cur = selWordJump(pane, cur, -1, 1, maxLine)
	case "ctrl+alt+shift+right":
		cur = selWordJump(pane, cur, 1, 3, maxLine)
	case "ctrl+alt+shift+left":
		cur = selWordJump(pane, cur, -1, 3, maxLine)
	default:
		// Unknown shift combo — clear selection, don't forward
		m.selection = nil
		return m, nil
	}

	// Clamp vertical
	if cur.Line < 0 {
		cur.Line = 0
	}
	if cur.Line > maxLine {
		cur.Line = maxLine
	}

	// Wrap horizontal: if past end of line, move to start of next line;
	// if before start, move to end of previous line.
	endCol := lineContentEnd(pane, cur.Line)
	if cur.Col < 0 {
		// Wrap to previous line
		if cur.Line > 0 {
			cur.Line--
			prevEnd := lineContentEnd(pane, cur.Line)
			if prevEnd >= 0 {
				cur.Col = prevEnd
			} else {
				cur.Col = 0
			}
		} else {
			cur.Col = 0
		}
	} else if endCol >= 0 && cur.Col > endCol {
		// Wrap to next line
		if cur.Line < maxLine {
			cur.Line++
			cur.Col = 0
		} else {
			cur.Col = endCol
		}
	} else if endCol < 0 {
		// Empty line — try wrapping
		if cur.Col > 0 && cur.Line < maxLine {
			cur.Line++
			cur.Col = 0
		} else {
			cur.Col = 0
		}
	}

	// Calculate delta from previous cursor to new cursor
	prevCur := m.selection.Cursor
	m.selection.Cursor = cur

	// Move shell cursor horizontally when staying on the same line.
	// Cross-line selection is visual only — sending Up/Down to PTY
	// would trigger command history navigation.
	if cur.Line == prevCur.Line {
		colDelta := cur.Col - prevCur.Col
		if colDelta != 0 {
			var moveBytes []byte
			for i := 0; i < colDelta; i++ {
				moveBytes = append(moveBytes, "\x1b[C"...)
			}
			for i := 0; i > colDelta; i-- {
				moveBytes = append(moveBytes, "\x1b[D"...)
			}
			return m, m.forwardInputBytes(moveBytes)
		}
	}
	return m, nil
}

// sanitizeDialogInput strips control characters from text before inserting
// into dialog input fields. Prevents ANSI escapes, null bytes, and newlines
// from reaching form values that may be used as command arguments.
func sanitizeDialogInput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r >= ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Bracketed paste markers (DECSET 2004): a wrapped paste arrives at the child
// app as a single paste event instead of a stream of typed characters.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// bracketedPaste wraps text in bracketed paste markers. Callers must only use
// it for panes whose app has enabled the mode (BracketedPasteEnabled) — apps
// that didn't ask for it would receive the markers as literal input.
func bracketedPaste(text string) []byte {
	// A payload containing the end marker would close the paste early and
	// hand the remainder to the child as typed input — the classic
	// bracketed-paste escape (a trailing \r would execute it in a shell).
	// Clipboard content is attacker-influenceable, so strip it.
	text = strings.ReplaceAll(text, pasteEnd, "")
	data := make([]byte, 0, len(text)+len(pasteStart)+len(pasteEnd))
	data = append(data, pasteStart...)
	data = append(data, text...)
	data = append(data, pasteEnd...)
	return data
}

// pastePayload encodes text for injection into pane's PTY: bracketed when the
// pane's app has enabled paste mode, raw bytes otherwise — the same decision a
// real terminal makes before bracketing a paste.
func pastePayload(pane *PaneModel, text string) []byte {
	if pane.BracketedPasteEnabled() {
		return bracketedPaste(text)
	}
	return []byte(text)
}

// sendInputToPane writes raw bytes to a specific pane's PTY stdin via IPC.
// Used to forward encoded mouse-wheel events to mouse-tracking apps.
func (m Model) sendInputToPane(paneID string, data []byte) {
	// A wheel notch is PTY input like any other — a tracking app reads it off
	// the same stdin as typed keys — so it rides the ordered queue rather than
	// going straight to the client, where it could overtake queued keystrokes.
	m.enqueueInput(paneID, data)
}

// sendClipboardToPane sends pasted text to the active pane as PTY input,
// re-wrapped in bracketed paste markers when the pane's app enabled the mode.
// tea.PasteMsg carries text from the outer terminal's bracketed paste, but
// those markers terminate at Bubble Tea — the program inside the pane never
// sees them. Without re-wrapping, that program treats the paste as ordinary
// keystrokes: interactive TUIs replay it character by character, and a
// multi-line paste into a shell executes every line but the last.
func (m Model) sendClipboardToPane(text string) {
	if text == "" {
		return
	}
	tab := m.activeTabModel()
	if tab == nil {
		return
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return
	}
	// Pasted text is the user acting on the pane, so it answers a parked one
	// exactly as a typed key does.
	pane.answerBlockedByInput()
	m.enqueueInput(pane.ID, pastePayload(pane, text))
}

// sendClipboardToPaneID pastes into a NAMED pane rather than whichever is
// active now. It backs the asynchronous clipboard path, where the target was
// bound when the user asked to paste and the read finished some time later —
// see clipboardPastedMsg.
//
// The lookup spans every project, not just the active one: switching projects
// during the read must not misdeliver the paste either. A pane that closed
// while the clipboard was being read simply drops the paste, which is the only
// honest option — there is no longer anywhere it was meant to go.
func (m Model) sendClipboardToPaneID(paneID, text string) {
	if text == "" || paneID == "" {
		return
	}
	pane, _, _ := m.findPaneAndTab(paneID)
	if pane == nil {
		logger.Debug("paste: pane %s vanished during the clipboard read — dropping", paneID)
		return
	}
	// The target was bound when the user asked to paste, so it need not be the
	// active pane any more — which is exactly why the answer is keyed to input
	// reaching a pane rather than to which pane holds focus.
	pane.answerBlockedByInput()
	m.enqueueInput(paneID, pastePayload(pane, text))
}

func keyToBytes(keyMsg tea.KeyPressMsg) []byte {
	s := keyMsg.String()

	switch s {
	case "enter":
		return []byte("\r")
	case "tab":
		return []byte("\t")
	case "shift+tab":
		// xterm CSI Z — Claude Code uses this to cycle modes (auto-accept,
		// plan, etc.). Without this mapping the key would be silently dropped.
		return []byte("\x1b[Z")
	case "backspace":
		return []byte{0x7f}
	case "space":
		return []byte(" ")
	case "esc":
		return []byte{0x1b}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "ctrl+right":
		return []byte("\x1b[1;5C") // word jump right
	case "ctrl+left":
		return []byte("\x1b[1;5D") // word jump left
	case "ctrl+alt+right":
		// 3-word jump: send word-jump 3 times
		return []byte("\x1b[1;5C\x1b[1;5C\x1b[1;5C")
	case "ctrl+alt+left":
		return []byte("\x1b[1;5D\x1b[1;5D\x1b[1;5D")
	case "delete":
		return []byte("\x1b[3~")
	case "home":
		return []byte("\x1b[H")
	case "end":
		return []byte("\x1b[F")
	case "pgup":
		return []byte("\x1b[5~")
	case "pgdown":
		return []byte("\x1b[6~")
	case "insert":
		return []byte("\x1b[2~")
	case "f1":
		return []byte("\x1bOP")
	case "f2":
		return []byte("\x1bOQ")
	case "f3":
		return []byte("\x1bOR")
	case "f4":
		return []byte("\x1bOS")
	case "f5":
		return []byte("\x1b[15~")
	case "f6":
		return []byte("\x1b[17~")
	case "f7":
		return []byte("\x1b[18~")
	case "f8":
		return []byte("\x1b[19~")
	case "f9":
		return []byte("\x1b[20~")
	case "f10":
		return []byte("\x1b[21~")
	case "f11":
		return []byte("\x1b[23~")
	case "f12":
		return []byte("\x1b[24~")
	}

	// Ctrl+letter → raw control character (0x01-0x1a)
	if len(s) == 6 && s[:5] == "ctrl+" {
		ch := s[5]
		if ch >= 'a' && ch <= 'z' {
			return []byte{ch - 'a' + 1}
		}
	}

	// Alt/Meta + printable key → ESC-prefixed byte (Meta encoding). Terminals
	// send Meta this way, and readline / claude-code bind ESC-b / ESC-f to
	// backward-word / forward-word. macOS Terminal.app with "Use Option as Meta
	// key" delivers Option+b / Option+f (a common word-jump setup) as alt+b /
	// alt+f — which otherwise fall through here and get dropped (no Text on a
	// modified key). Require Alt WITHOUT Ctrl (Ctrl+Alt combos have explicit
	// cases above), and only printable ASCII (special keys are named cases).
	// Prefer Text (carries the shifted glyph, e.g. Alt+Shift+B → "B") and fall
	// back to Code (a bare modified key like Alt+B has empty Text).
	if keyMsg.Mod.Contains(tea.ModAlt) && !keyMsg.Mod.Contains(tea.ModCtrl) {
		if t := keyMsg.Text; len(t) == 1 && t[0] >= 0x20 && t[0] <= 0x7e {
			return []byte{0x1b, t[0]}
		}
		if r := keyMsg.Code; r >= 0x20 && r <= 0x7e {
			return []byte{0x1b, byte(r)}
		}
	}

	// Printable text — handles single ASCII, multi-byte UTF-8, and multi-rune IME input.
	if keyMsg.Text != "" {
		return []byte(keyMsg.Text)
	}

	return nil
}

// resizeAllPanes walks the projects rather than allTabs() so each pane's
// message can carry its own daemon: this is a broadcast over EVERY project, so
// the active dest would be the right answer for at most one of them.
func (m Model) resizeAllPanes() tea.Cmd {
	return func() tea.Msg {
		for _, proj := range m.projects {
			for _, tab := range proj.tabs {
				if tab.Root == nil {
					continue
				}
				for _, pane := range tab.Leaves() {
					// paneVTSize keeps the PTY in lockstep with the VT: rect
					// size for normal panes, tab canvas for wide-canvas panes.
					// The daemon drops exact duplicates (same-size guard).
					// pane.NativeW comes from the same resize pass that sized
					// the VT, so the mode this reproduces cannot disagree with
					// the one already applied.
					cols, rows := paneVTSize(pane.WideCanvas, pane.MinNativeCols, pane.Width, pane.Height, pane.NativeW, tab.CanvasW, tab.CanvasH)
					msg, _ := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
						PaneID: pane.ID,
						Cols:   uint16(cols),
						Rows:   uint16(rows),
					})
					m.sendForDest(proj.Dest, msg)
				}
			}
		}
		return nil
	}
}

func (m Model) updatePane(paneID, name string) tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID: paneID,
			Name:   name,
		})
		m.sendForPane(paneID, msg)
		return nil
	}
}

func (m Model) updatePaneCWD(paneID, cwd string) tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID: paneID,
			CWD:    cwd,
		})
		m.sendForPane(paneID, msg)
		return nil
	}
}

// toggleActivePaneMute flips the muted flag on the currently-focused pane and
// sends the update to the daemon. The daemon is the source of truth — it
// echoes the new state back via the next workspace_state broadcast and the
// pane border's `[muted]` chip updates from there. No-op if no active pane.
func (m Model) toggleActivePaneMute() tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}
	next := !pane.Muted
	paneID := pane.ID
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID: paneID,
			Muted:  &next,
		})
		if err != nil {
			log.Printf("toggleActivePaneMute build msg: %v", err)
			return nil
		}
		if err := m.sendForPane(paneID, msg); err != nil {
			log.Printf("toggleActivePaneMute send: %v", err)
		}
		return nil
	}
}

// sendPinnedAttention makes the daemon the authority on a pane's attention
// pin. Takes the target value rather than toggling, because both callers
// already know it: the context menu's Mark/Unmark row flips, and Clear
// attention only ever clears.
//
// The destination is resolved HERE, on the Update goroutine, and the closure
// gets a plain string. Its siblings all call sendForPane inside the Cmd, which
// walks m.projects → tabs → the layout tree off-goroutine while rebuildTabs
// mutates the same pointers on every workspace_state — the hazard the pane-input
// pipeline invariant in .claude/CLAUDE.md names ("resolving it off-goroutine is
// a race AND a stale answer"). Resolving it first costs nothing and is the only
// difference from toggleActivePaneMute worth having.
//
// Strict, unlike those siblings: Router.Send DROPS a message for a dest it has
// no conn for and returns nil, which is right for the bulk iterators it was
// written for and wrong here. This is a one-shot the user asked for, and the
// pin is the one flag whose entire selling point is that it survives — a mark
// that silently did not take is invisible until the user goes looking for it
// next week. There is no dialog to surface it in from a context menu, so the
// failure is named in the log rather than swallowed as success.
func (m Model) sendPinnedAttention(paneID string, pinned bool) tea.Cmd {
	if paneID == "" {
		return nil
	}
	dest := m.destOfPane(paneID)
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID:          paneID,
			PinnedAttention: &pinned,
		})
		if err != nil {
			log.Printf("sendPinnedAttention build msg: %v", err)
			return nil
		}
		if err := m.sendForDestStrict(dest, msg); err != nil {
			log.Printf("sendPinnedAttention: pin=%v for pane %s did not reach dest %q: %v",
				pinned, paneID, dest, err)
		}
		return nil
	}
}

// toggleActivePaneEager flips the eager-restore flag on the focused pane and
// sends the daemon the authoritative update; the eager state updates from the
// next workspace_state broadcast. No-op if no active pane.
func (m Model) toggleActivePaneEager() tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}
	pane := tab.ActivePaneModel()
	if pane == nil {
		return nil
	}
	next := !pane.Eager
	paneID := pane.ID
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID: paneID,
			Eager:  &next,
		})
		if err != nil {
			log.Printf("toggleActivePaneEager build msg: %v", err)
			return nil
		}
		if err := m.sendForPane(paneID, msg); err != nil {
			log.Printf("toggleActivePaneEager send: %v", err)
		}
		return nil
	}
}

// layoutSend and resizeSend are one decided frame each. The diff that produces
// them runs on the Update goroutine and the command only ships the result —
// the walk reads m.projects, which Update rebuilds on every broadcast, so
// deciding inside the command would read a list that is being replaced.
type layoutSend struct {
	dest  string
	tabID string
	data  json.RawMessage
}

type resizeSend struct {
	dest   string
	paneID string
	cols   uint16
	rows   uint16
}

// layoutAgrees reports whether the daemon's stored layout for a tab already
// describes the tree we hold.
//
// The comparison is STRUCTURAL, and that is not a style preference. The daemon
// stores MarshalLayout's bytes — a struct, so Go emits its fields in
// declaration order — but parseWorkspaceState decodes the whole broadcast into
// map[string]any and re-marshals the layout sub-map, and Go sorts map keys
// alphabetically. For any node with more than one key the two encodings differ
// for the identical tree, so a byte comparison reports "changed" forever on
// every tab containing a split, while still matching single-leaf tabs. That
// asymmetry is invisible in a workspace of single-pane tabs, which is exactly
// what the crash was reported from.
//
// Empty means the daemon holds nothing for this tab (fresh, or restored before
// any client described it) — the caller must send, or the arrangement is never
// persisted.
func layoutAgrees(stored json.RawMessage, root *LayoutNode) bool {
	if len(stored) == 0 {
		return false
	}
	theirs, err := UnmarshalLayout(stored)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(theirs, SerializeLayout(root))
}

// diffLayouts decides which tabs need their layout pushed after a broadcast.
//
// Scoped to the broadcast's OWN destination: a broadcast is the full state of
// one daemon, so it says nothing about another daemon's tabs and cannot be
// diffed against them. Before this scoping, any daemon's broadcast re-sent
// every daemon's layouts.
func (m *Model) diffLayouts(state WorkspaceStateMsg) []layoutSend {
	stored := make(map[string]json.RawMessage, len(state.Tabs))
	for _, ti := range state.Tabs {
		stored[ti.ID] = ti.Layout
	}
	// A broadcast whose dest matches no project means every layout and every
	// resize below is silently skipped, and the failure has no other symptom:
	// splits revert on restart, panes keep a stale PTY width, and nothing logs.
	// The invariant holds structurally today — ProjectModel.Dest is only ever
	// assigned from a broadcast's own dest — so this line exists to make a
	// future break greppable rather than a multi-hour hunt.
	if len(m.projects) > 0 && !m.hasProjectForDest(state.Dest) {
		log.Printf("apply: broadcast dest %q matches no project — no layout or "+
			"resize will be sent for it", state.Dest)
	}

	var out []layoutSend
	for _, proj := range m.projects {
		if proj.Dest != state.Dest {
			continue
		}
		for _, tab := range proj.tabs {
			if tab.Root == nil || layoutAgrees(stored[tab.ID], tab.Root) {
				continue
			}
			data, err := MarshalLayout(tab.Root)
			if err != nil {
				continue
			}
			out = append(out, layoutSend{dest: proj.Dest, tabID: tab.ID, data: data})
		}
	}
	return out
}

// sizedKey scopes a sizedOnce entry to its owning destination. NUL separates
// the halves because it cannot occur in either a dest or a pane id, so no pair
// of distinct inputs can collide on one key.
func sizedKey(dest, paneID string) string { return dest + "\x00" + paneID }

// hasProjectForDest reports whether any project belongs to dest.
func (m *Model) hasProjectForDest(dest string) bool {
	for _, proj := range m.projects {
		if proj.Dest == dest {
			return true
		}
	}
	return false
}

// diffResizes decides which panes need a resize pushed after a broadcast.
// See Model.sizedOnce for why the first send per pane is never suppressed.
func (m *Model) diffResizes(state WorkspaceStateMsg) []resizeSend {
	type size struct {
		cols, rows uint16
		pending    bool
	}
	stored := make(map[string]size, len(state.Panes))
	for _, pi := range state.Panes {
		stored[pi.ID] = size{cols: pi.Cols, rows: pi.Rows, pending: pi.Pending}
	}
	if m.sizedOnce == nil {
		m.sizedOnce = make(map[string]bool)
	}
	var out []resizeSend
	for _, proj := range m.projects {
		if proj.Dest != state.Dest {
			continue
		}
		for _, tab := range proj.tabs {
			if tab.Root == nil {
				continue
			}
			for _, pane := range tab.Leaves() {
				known := stored[pane.ID]
				// A deferred pane has no PTY, and handleResizePane returns
				// early on a nil one WITHOUT recording the size — so the
				// daemon's reported cols/rows never move however many resizes
				// we send, the diff never agrees, and the pane is re-sent on
				// every broadcast. Under lazy restore that is most of the
				// workspace. Left unmarked deliberately: the broadcast that
				// reports it spawned is the first one that can size it.
				if known.pending {
					continue
				}
				c, r := paneVTSize(pane.WideCanvas, pane.MinNativeCols,
					pane.Width, pane.Height, pane.NativeW, tab.CanvasW, tab.CanvasH)
				cols, rows := uint16(c), uint16(r)
				// A daemon that has never applied a size reports 0, which no
				// computed size can equal — paneVTSize floors both dimensions
				// at 1 — so that case needs no separate term.
				if m.sizedOnce[sizedKey(proj.Dest, pane.ID)] && known.cols == cols && known.rows == rows {
					continue
				}
				// Marked before the send rather than after it: the send rides a
				// tea.Cmd and reports nothing back. A frame dropped for an
				// unreachable dest therefore costs this pane its first-resize
				// kick until the next reattach — acceptable because the daemon
				// fires its own resizeKick on the pane's first PTY output.
				m.sizedOnce[sizedKey(proj.Dest, pane.ID)] = true
				out = append(out, resizeSend{dest: proj.Dest, paneID: pane.ID, cols: cols, rows: rows})
			}
		}
	}
	return out
}

// sendDiffedLayouts ships an already-decided layout list.
func (m Model) sendDiffedLayouts(items []layoutSend) tea.Cmd {
	if len(items) == 0 {
		return nil
	}
	return func() tea.Msg {
		for _, it := range items {
			msg, err := ipc.NewMessage(ipc.MsgUpdateLayout, ipc.UpdateLayoutPayload{
				TabID:  it.tabID,
				Layout: it.data,
			})
			if err != nil {
				continue
			}
			m.sendForDest(it.dest, msg)
		}
		return nil
	}
}

// sendDiffedResizes ships an already-decided resize list.
func (m Model) sendDiffedResizes(items []resizeSend) tea.Cmd {
	if len(items) == 0 {
		return nil
	}
	return func() tea.Msg {
		for _, it := range items {
			msg, err := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
				PaneID: it.paneID,
				Cols:   it.cols,
				Rows:   it.rows,
			})
			if err != nil {
				continue
			}
			m.sendForDest(it.dest, msg)
		}
		return nil
	}
}

// sendAllLayouts walks the projects for the same reason resizeAllPanes does —
// every tab's layout has to reach the daemon that owns that tab.
func (m Model) sendAllLayouts() tea.Cmd {
	return func() tea.Msg {
		for _, proj := range m.projects {
			for _, tab := range proj.tabs {
				if tab.Root == nil {
					continue
				}
				data, err := MarshalLayout(tab.Root)
				if err != nil {
					continue
				}
				msg, _ := ipc.NewMessage(ipc.MsgUpdateLayout, ipc.UpdateLayoutPayload{
					TabID:  tab.ID,
					Layout: data,
				})
				m.sendForDest(proj.Dest, msg)
			}
		}
		return nil
	}
}
