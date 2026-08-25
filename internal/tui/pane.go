package tui

import (
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/artyomsv/quil/internal/ringbuf"
)

// spinnerFrames are braille characters cycled for every animated indicator: the
// per-pane resuming/preparing spinner, the tab label's work spinner, the pane's
// top-border one, and the sidebar's working glyph (workingGlyph, sidebar.go).
// ONE sequence deliberately — a second set of frames would make the same fact
// look different depending on which part of the screen reported it.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// restoreAccentStyle / restoreDimStyle / restoreDoneStyle color the centered
// restore indicator: brand flame (256-color 208) for the spinner+label, dim
// grey for the context/pending rows, green (28) for done rows.
var (
	restoreAccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	restoreDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	// spawnErrorStyle marks a pane that has no process and is not getting one.
	// Red rather than the restore palette's dim grey: this is a terminal state
	// the user has to act on, not a step in progress.
	spawnErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	restoreDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
)

// Restore-indicator timing: the spinner shows for at least restoreMinDisplay
// (so a fast pane doesn't flash), persists until the pane's first live output,
// and is force-cleared after restoreSafetyCap so a silent or dead process does
// not spin forever.
const (
	restoreMinDisplay = 2 * time.Second
	restoreSafetyCap  = 30 * time.Second
)

type PaneModel struct {
	ID            string
	Type          string // plugin type ("terminal", "claude-code", etc.)
	WideCanvas    bool   // [display] wide_canvas: VT/PTY stay window-sized; small rects render a wrapped preview
	MinNativeCols int    // [display] min_native_cols: inner-width threshold for native (non-canvas) rendering; 0 = default 80
	// RestoresViaSession is the plugin capability resolved by Model (which owns
	// the registry) and copied in by syncPaneMeta. PaneModel.View has no
	// registry access, so it is resolved once per broadcast rather than looked
	// up at render time.
	RestoresViaSession bool
	Name               string // user-given name (empty if not set)
	CWD                string // current working directory from daemon
	Muted              bool   // notification mute (daemon-authoritative; mirrored here for border rendering)
	Eager              bool   // eager-restore flag (daemon-authoritative; mirrored for the tab marker)
	vt                 *vt.SafeEmulator
	vtDrain            *vtDrain       // drain goroutine tracker for p.vt (see closeVT)
	oscFilter          oscTitleFilter // strips OSC 0/1/2 before the emulator (see oscfilter.go)
	Width              int
	Height             int
	// NativeW is Width plus whatever the project sidebar reserved — the
	// width this rect would have with the sidebar closed. It decides the
	// pane's render mode and nothing else (paneVTSize), so toggling the
	// sidebar changes how much of a pane you see, never how it renders.
	// Written by the resize recursion so resizeAllPanes computes the same
	// wire size the VT already took, rather than re-deriving it and drifting.
	NativeW       int
	Active        bool
	scrollBack    int
	rawBuf        *ringbuf.RingBuffer // raw PTY bytes for resize replay
	cursorVisible bool                // tracks shell's DECTCEM state
	ghost         bool                // true while showing restored content
	resuming      bool                // true while waiting for first live output after restore
	preparing     bool                // true for newly created panes (not restored)
	Pending       bool                // deferred restore — not yet lazy-spawned (daemon-authoritative)
	SessionID     string              // tracked session id (daemon-authoritative; restore checklist)
	HistoryLines  int                 // ghost-buffer line count (daemon-authoritative; restore checklist)
	// Git state of the pane's CWD, daemon-authoritative (see PaneInfo).
	// GitStale means the last refresh did not complete, so these are the last
	// values actually observed rather than current ones.
	// SpawnError explains why this pane has no process; empty when it has one.
	// Rendered in the pane's own rectangle in place of VT content.
	SpawnError string
	// PreparingWorktree names the branch a `git worktree add` is checking out
	// for the pane that will REPLACE this one. Daemon-authoritative, and while
	// it is set this pane has no process at all — it is a placeholder standing
	// in a new tab so the tab is never pane-less, which is why it must not look
	// like an ordinary blank terminal.
	PreparingWorktree string
	GitBranch         string
	GitDetached       bool
	GitWorktree       bool
	GitWorktreeName   string
	// WorktreeOwned is daemon-authoritative: this pane's worktree was created
	// by Quil, so the close dialog may offer to delete it. WorktreePath is the
	// directory that was created — the close dialog prices THAT, never CWD,
	// which drifts with every `cd` the shell makes. See PaneInfo.
	WorktreeOwned      bool
	WorktreePath       string
	GitUpstream        bool
	GitAhead           int
	GitBehind          int
	GitStale           bool
	Model              string         // model id of the last completed AI turn (daemon-authoritative; status bar)
	ContextTokens      int64          // context-window tokens of the last completed AI turn (daemon-authoritative; status bar)
	resumeStart        time.Time      // when resuming/preparing started (minimum display duration)
	spinnerFrame       int            // current frame index in spinnerFrames
	spinnerTickRunning bool           // guards against stacking restore-spinner tick chains (cf. workTickRunning)
	activeSel          *Selection     // set by Model before View() for selection rendering
	focusMode          bool           // set by Model before View() when in focus mode
	mcpHighlight       bool           // set by Model before View() when MCP is interacting
	liveOutputSeen     bool           // first live (non-ghost) output received — settle repaints scheduled
	reattachReset      bool           // armed on reattach; consumed by the daemon's next replayed chunk (see armReattachReset)
	working            bool           // derived spinner state: turnActive || len(subagents) > 0 || subagentsOverflow (hook-driven)
	turnActive         bool           // main turn in flight (UserPromptSubmit/PostToolUse → Stop); a park does NOT clear this — see the workPark case
	subagents          map[string]int // agent_type → outstanding count (SubagentStart/Stop, burst-aware); a stop only cancels a start it can name
	subagentsOverflow  bool           // a start was refused by maxTrackedSubagents, so an untracked agent may still be live; sticky until a terminal edge
	unseen             bool           // work finished while this pane was not focused (a park no longer sets this — see workPark); cleared on focus
	markedForDeletion  bool           // context-menu "Mark for deletion" — red border + ⌫ saying the pane is finished with and safe to close; cleared only by Unmark, or by the daemon when the attention pin replaces it. DAEMON-owned (Pane.MarkedForDeletion) on the same terms as pinnedAttention: syncPaneMeta is the sole writer here, and every set goes out as MsgUpdatePane
	pinnedAttention    bool           // context-menu "Mark attention" pin — purple border + ◆ that SURVIVES focus; cleared only by Unmark/Clear attention. DAEMON-owned (Pane.PinnedAttention), so it survives restart and reads the same on every client: syncPaneMeta is the sole writer here, and every set goes out as MsgUpdatePane
	workFrame          int            // shared spinner frame index, mirrored here for top-border render
	blockedSince       time.Time      // set when the agent parks waiting on the user — workPark always, workNotify unless the producer marked the event as Claude's idle nudge AND the turn is already over; zero when not blocked. Cleared on workStart/workAbort/workStop/workStopFinal (a completed turn is by definition not blocked) — focus does NOT clear it (see ackFocusedPane); paneRow suppresses the glyph for the focused pane instead
	blockedReason      string         // optional tool name from the hook's Data["tool"]; genuinely absent for Notification/permission.ask, so left empty rather than invented
	// lastToastAt rate-limits desktop notifications for this pane. Per pane and
	// SHARED by both event kinds, matching the daemon's own per-pane bell
	// cooldown (Pane.LastBellEventAt): a pane that blocks and then completes
	// five seconds later is one event to a human, not two.
	lastToastAt time.Time

	// Mouse-tracking state, updated by the VT EnableMode/DisableMode callbacks
	// during AppendOutput (same goroutine as Update/View, like cursorVisible —
	// no synchronization needed). One bool per DEC mouse mode so disabling a
	// mode that was never set can't wrongly clear tracking. mouseSGR records the
	// SGR extended-encoding mode (?1006). When any tracking mode is active the
	// wheel handler forwards the event to the PTY instead of scrolling Quil's
	// own scrollback (which alt-screen TUI apps never populate).
	mouseX10       bool // ?9
	mouseNormal    bool // ?1000
	mouseButton    bool // ?1002
	mouseAny       bool // ?1003
	mouseSGR       bool // ?1006
	bracketedPaste bool // ?2004 (gates paste wrapping, not mouse forwarding)
	// bracketedPasteSeen records that this client's emulator has observed a
	// ?2004 toggle in the pane's own output. Once it has, bracketedPaste is a
	// live signal and the daemon mirror below is ignored — see
	// BracketedPasteEnabled for why OR-ing the two is wrong for this mode.
	bracketedPasteSeen bool
	// daemonMouseTracking/daemonMouseSGR/daemonBracketedPaste mirror the
	// daemon-authoritative mode state from the workspace snapshot. The daemon
	// sees the one-time mode-enable burst on every attach; the local emulator
	// does not when reattaching to an already-running app (ghost_buffer=false,
	// e.g. opencode), so this is the reliable signal. Set in syncPaneMeta.
	daemonMouseTracking  bool
	daemonMouseSGR       bool
	daemonBracketedPaste bool

	// Render cache: View() output is reused while renderKey() is unchanged.
	// contentGen covers VT-grid/raw-buffer mutations (the grid itself has no
	// public change counter; PaneModel mediates all writes via AppendOutput/
	// ResetVT/ResizeVT). Selection is snapshotted by VALUE into the key (it
	// lives on Model and is mutated there), so no selection generation is
	// needed. renderCount is test observability — incremented on real renders
	// only. invalidateRenderCache() is the explicit escape hatch (redraw key).
	contentGen  uint64
	cachedKey   paneRenderKey
	cachedView  string
	hasCache    bool
	renderCount int

	// pvCache: wrap layout for wide-canvas preview rendering. Invalidated
	// implicitly by its (contentGen, innerW, wrap) key — see previewLayoutFor.
	pvCache *previewLayout
	// previewWrap: soft-wrap the wide-canvas preview instead of the default
	// left-edge crop. Toggled per pane via the toggle_wrap keybinding
	// (default alt+shift+w). TUI-session state, not persisted.
	previewWrap bool

	// splitDragHighlight marks this pane's border while a split-border
	// drag-resize touching this pane is in progress. Transient TUI state,
	// never persisted; set/cleared by Model.setSplitDragHighlight.
	splitDragHighlight bool
	// ctxTargetHighlight marks this pane's border while the pane context
	// menu is open and targeting it. Transient TUI state, never persisted;
	// set/cleared by Model.openCtxMenu / Model.closeCtxMenu.
	ctxTargetHighlight bool
}

// paneRenderKey is the comparable fingerprint of everything View() reads,
// directly or transitively (renderContent, renderScrollback,
// renderWithSelection, insertCursor, buildTopBorder). Adding a new visual
// input to any of those REQUIRES adding it here — a missing field means
// stale frames. The redraw key (alt+shift+l) clears the cache as the
// user-facing escape hatch.
//
// Notes on coverage:
//   - contentGen stands in for everything derived from the VT emulator:
//     screen cells, scrollback cells, ScrollbackLen, CursorPosition, and the
//     emulator's own width/height (only PaneModel methods mutate the VT).
//   - cursorVisible and cwd are written by VT callbacks during vt.Write
//     (same Update goroutine); they are plain fields here.
//   - selActive/sel snapshot the Model-owned *Selection by value, already
//     resolved against this pane's ID — a selection on another pane renders
//     identically to no selection, so it is normalized to the zero value.
//   - spinnerFrame is only advanced while resuming/preparing, workFrame only
//     while working (guarded at the call sites in model.go/workstate.go), so
//     including them raw does not churn the key for idle panes.
type paneRenderKey struct {
	contentGen                     uint64
	width, height, scrollBack      int
	active, cursorVisible          bool
	ghost, resuming, preparing     bool
	pending                        bool
	preparingWorktree, spawnError  string
	mcpHighlight, muted, focusMode bool
	splitDragHighlight             bool
	ctxTargetHighlight             bool
	working                        bool
	unseen                         bool
	pinnedAttention                bool
	markedForDeletion              bool
	liveOutputSeen                 bool
	spinnerFrame, workFrame        int
	name, cwd                      string
	paneType, sessionID            string
	wideCanvas, previewWrap        bool
	historyLines                   int
	selActive                      bool
	sel                            Selection
}

// renderKey computes the current fingerprint of every View() input.
func (p *PaneModel) renderKey() paneRenderKey {
	k := paneRenderKey{
		contentGen:    p.contentGen,
		width:         p.Width,
		height:        p.Height,
		scrollBack:    p.scrollBack,
		active:        p.Active,
		cursorVisible: p.cursorVisible,
		ghost:         p.ghost,
		resuming:      p.resuming,
		preparing:     p.preparing,
		pending:       p.Pending,
		// Both are drawn by View and both now change on a LIVE pane: a failed
		// worktree create writes SpawnError onto a pane that has already
		// rendered, and Alt+R clears it with no output to bump contentGen.
		preparingWorktree:  p.PreparingWorktree,
		spawnError:         p.SpawnError,
		mcpHighlight:       p.mcpHighlight,
		splitDragHighlight: p.splitDragHighlight,
		ctxTargetHighlight: p.ctxTargetHighlight,
		muted:              p.Muted,
		focusMode:          p.focusMode,
		working:            p.working,
		unseen:             p.unseen,
		pinnedAttention:    p.pinnedAttention,
		markedForDeletion:  p.markedForDeletion,
		liveOutputSeen:     p.liveOutputSeen,
		spinnerFrame:       p.spinnerFrame,
		workFrame:          p.workFrame,
		name:               p.Name,
		cwd:                p.CWD,
		paneType:           p.Type,
		sessionID:          p.SessionID,
		wideCanvas:         p.WideCanvas,
		previewWrap:        p.previewWrap,
		historyLines:       p.HistoryLines,
	}
	// renderContent only honors a selection whose PaneID matches this pane;
	// foreign or absent selections render identically, so both normalize to
	// the zero value (no spurious invalidation while another pane is being
	// selected).
	if p.activeSel != nil && p.activeSel.PaneID == p.ID {
		k.selActive = true
		k.sel = *p.activeSel
	}
	return k
}

// invalidateRenderCache drops the cached frame so the next View() rebuilds
// it unconditionally. Wired to the redraw keybinding as the user-facing
// escape hatch for a hypothetical stale-cache bug. Also releases the cached
// string so the escape hatch doubles as a memory release.
func (p *PaneModel) invalidateRenderCache() {
	p.hasCache = false
	p.cachedView = ""
}

// vtDrain tracks the drain goroutine of one emulator so teardown can be
// sequenced: upstream x/vt's Emulator.Close races Emulator.Read on an
// unsynchronized closed flag (SafeEmulator wraps neither), so Close may only
// run after the drain goroutine has exited.
type vtDrain struct {
	stop atomic.Bool
	done chan struct{}
}

// newVTEmulator builds a SafeEmulator for this pane and starts a goroutine
// that drains the emulator's response pipe. The caller installs the returned
// pair into p.vt / p.vtDrain (newVTEmulator deliberately does NOT write p's
// fields itself — installVT must close the OLD emulator via the OLD vtDrain
// before the new pair is assigned).
//
// The charmbracelet/x/vt emulator answers queries like CSI c (Primary Device
// Attributes, DA1), DSR (Device Status Report), and OSC 10/11/12 by writing
// the response to an internal io.Pipe. That pipe blocks writers until a
// reader drains it. Without a drain, any TUI app that queries terminal
// capabilities — Claude Code 2.1.110 sends DA1 on startup — deadlocks the
// entire TUI inside vt.Write(). The drain goroutine terminates via the
// stop-flag protocol in closeVT(); only after it exits is Emulator.Close()
// safe to call.
func (p *PaneModel) newVTEmulator(w, h int) (*vt.SafeEmulator, *vtDrain) {
	em := vt.NewSafeEmulator(w, h)
	em.SetScrollbackSize(scrollbackLines())
	em.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			p.cursorVisible = visible
		},
		WorkingDirectory: func(dir string) {
			p.CWD = parseOSC7Path(dir)
		},
		EnableMode:  func(mode ansi.Mode) { p.setMouseMode(mode, true) },
		DisableMode: func(mode ansi.Mode) { p.setMouseMode(mode, false) },
	})
	d := &vtDrain{done: make(chan struct{})}
	go drainVTResponses(em, d)
	return em, d
}

// drainVTResponses continuously reads and discards the emulator's query
// responses. After each successful read it checks the stop flag so closeVT
// can retire it without calling Emulator.Close while a Read is in flight.
// Exits cleanly on EOF/closed-pipe (emulator closed); any other read error
// leaves a breadcrumb so a future library regression that re-introduces a
// deadlock isn't silent.
func drainVTResponses(em *vt.SafeEmulator, d *vtDrain) {
	defer close(d.done)
	buf := make([]byte, 256)
	for {
		if _, err := em.Read(buf); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				log.Printf("pane: VT drain exited unexpectedly: %v", err)
			}
			return
		}
		if d.stop.Load() {
			return
		}
	}
}

// closeVT stops the drain goroutine, then closes the emulator. The DA1 query
// makes the emulator emit a response into its pipe, waking the drain's
// blocked Read so it can observe the stop flag; only after it exits is
// Close safe (see vtDrain). The 1 s fallback guards a hypothetical
// non-responding emulator — closing then re-admits the benign upstream race
// rather than hanging the Update loop.
func (p *PaneModel) closeVT() {
	if p.vt == nil {
		return
	}
	if p.vtDrain != nil {
		p.vtDrain.stop.Store(true)
		_, _ = p.vt.Write([]byte("\x1b[c")) // DA1 — provokes a response
		select {
		case <-p.vtDrain.done:
		case <-time.After(time.Second):
			log.Printf("pane %s: VT drain did not stop within 1s — closing anyway", p.ID)
		}
	}
	_ = p.vt.Close()
	// Nil both so a second closeVT/Dispose is a no-op via the guard above.
	// Safe: disposal only happens after the pane is removed from every model
	// structure (layout tree, overlay slot), inside the single-threaded
	// Update path, so no other p.vt reader can be reached afterwards.
	p.vt = nil
	p.vtDrain = nil
}

// Dispose closes the VT emulator, stopping its drainVTResponses goroutine
// and releasing the scrollback grid. Must be called for every PaneModel
// removed from the layout tree — without it each closed pane leaks a parked
// goroutine plus up to a 10,000-line scrollback. The PaneModel must not be
// rendered or written to afterwards. Idempotent: a second call is a no-op.
func (p *PaneModel) Dispose() {
	p.closeVT()
}

// installVT closes the current emulator (stopping its drain goroutine via
// the OLD vtDrain) and installs the new pair.
func (p *PaneModel) installVT(em *vt.SafeEmulator, d *vtDrain) {
	p.closeVT()
	p.vt, p.vtDrain = em, d
}

// defaultScrollbackLines is the per-pane VT scrollback depth, and was the
// hardcoded value before it became configurable. It stays the default so no
// install loses history on upgrade.
const defaultScrollbackLines = 10000

// maxScrollbackLines caps what the config may ask for. Depth multiplies by pane
// count, so a stray zero turns a tuning knob into an out-of-memory on startup —
// on the one setting whose entire purpose is memory pressure. The ceiling sits
// far above any plausible use (100x the default) so it can only catch a typo.
const maxScrollbackLines = 1000000

// targetTotalScrollbackLines is the workspace-wide line budget the adaptive
// depth spends when ui.scrollback_lines is unset.
//
// Calibrated from one production measurement: 41 panes at 10 000 lines
// coincided with 847 MB resident, so roughly 2 KB per retained line. That
// sample includes memory which is not scrollback, so it OVER-estimates the
// per-line cost — the safe direction. Expressed in LINES, which is what this
// code controls; a byte budget would be a guess wearing a unit.
//
// 100 000 puts a ten-pane workspace exactly at the historical default, which is
// the property that matters: adaptation begins above ten panes, and nobody below
// that sees any change at all.
const targetTotalScrollbackLines = 100000

// minAdaptiveScrollbackLines floors the adaptive depth. Bounding memory must not
// make panes useless — a few hundred lines of history is not worth having. Only
// the adaptive path is floored; an explicit setting may go lower, because asking
// for it is a deliberate act.
const minAdaptiveScrollbackLines = 2000

// explicitScrollback is ui.scrollback_lines as configured; 0 means unset.
// Process-wide because every pane wants the same answer and panes are
// constructed from a dozen call sites that have no config in hand. The
// established precedent for this shape is version.SetUpdatesEnabled. Read
// through scrollbackLines(), never directly.
//
// ATOMIC, unlike that precedent, and the reason is worth stating: this pair is
// written by applyWorkspaceState — PRODUCTION code on the Update goroutine —
// while scrollbackLines() is read from NewPaneModel. Those are the same
// goroutine in production, but a plain int makes every parallel test in the
// package racy against any other that builds a pane, which the detector reports
// as a failure of whichever pair it happens to catch. Same reasoning as
// Daemon.clientCWD's atomic.Pointer.
var explicitScrollback atomic.Int64

// knownPaneCount is the workspace size the adaptive depth divides. Published by
// applyWorkspaceState BEFORE it creates any pane, so a restored workspace sizes
// every pane against its true total rather than against however many happened to
// exist when each one was built.
var knownPaneCount atomic.Int64

// logScrollbackChoiceOnce keeps the adaptive choice discoverable without making
// it noisy: a depth chosen FOR the user rather than BY them should appear in the
// log, once.
var logScrollbackChoiceOnce sync.Once

// adaptiveScrollbackLines resolves the depth a pane is created with.
//
// explicit is ui.scrollback_lines (0 = unset). A set value WINS unconditionally
// — pane count never overrides a depth the user asked for — and is clamped only
// against maxScrollbackLines, the typo guard.
//
// Unset spends targetTotalScrollbackLines across the workspace, clamped to the
// historical default at the top so small workspaces are untouched, and to
// minAdaptiveScrollbackLines at the bottom.
//
// Depth is settable only at pane CREATION: x/vt's SetScrollbackSize reslices its
// backing array rather than reallocating, so the dropped prefix stays reachable
// and trimming a live pane frees nothing. That is why this is a creation-time
// default rather than a background trim.
//
// Pure, so the policy is testable without a Model, a config file or a daemon.
func adaptiveScrollbackLines(explicit, paneCount int) int {
	if explicit > 0 {
		if explicit > maxScrollbackLines {
			return maxScrollbackLines
		}
		return explicit
	}
	if paneCount <= 0 {
		return defaultScrollbackLines
	}
	depth := targetTotalScrollbackLines / paneCount
	if depth > defaultScrollbackLines {
		return defaultScrollbackLines
	}
	if depth < minAdaptiveScrollbackLines {
		return minAdaptiveScrollbackLines
	}
	return depth
}

// SetScrollbackLines records ui.scrollback_lines. Called once at startup from
// the loaded config.
//
// Stores the raw value rather than resolving it, because the resolution now
// depends on pane count, which is not known until a workspace arrives.
func SetScrollbackLines(n int) {
	if n > maxScrollbackLines {
		log.Printf("ui.scrollback_lines = %d exceeds the %d cap — clamping; depth is per pane and multiplies by pane count",
			n, maxScrollbackLines)
		n = maxScrollbackLines
	}
	if n < 0 {
		n = 0
	}
	explicitScrollback.Store(int64(n))
}

// SetPaneCount publishes the workspace pane count for the adaptive depth.
//
// Only ever RAISES. Depth cannot be reclaimed from panes already built, so a
// count that oscillates would hand out inconsistent depths for no benefit — a
// closing pane must not deepen the next pane's allocation.
//
// Compare-and-swap rather than a read-then-write, so the monotonic property
// holds even though the field is atomic: two concurrent raises could otherwise
// interleave and let the smaller win.
func SetPaneCount(n int) {
	v := int64(n)
	for {
		cur := knownPaneCount.Load()
		if v <= cur {
			return
		}
		if knownPaneCount.CompareAndSwap(cur, v) {
			return
		}
	}
}

func scrollbackLines() int {
	explicit := int(explicitScrollback.Load())
	panes := int(knownPaneCount.Load())
	depth := adaptiveScrollbackLines(explicit, panes)
	if explicit == 0 && depth != defaultScrollbackLines {
		logScrollbackChoiceOnce.Do(func() {
			log.Printf("scrollback depth %d lines for %d panes (set ui.scrollback_lines to override)", depth, panes)
		})
	}
	return depth
}

func NewPaneModel(id string, bufSize int) *PaneModel {
	p := &PaneModel{
		ID:            id,
		Name:          "",
		rawBuf:        ringbuf.NewRingBuffer(bufSize),
		cursorVisible: true, // visible by default (matches terminal default)
	}
	p.vt, p.vtDrain = p.newVTEmulator(80, 24)
	return p
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.rawBuf.Write(data)
	// Strip OSC 0/1/2 (window title) before the emulator: x/vt ends an OSC at a
	// stray 0x9C even mid-UTF-8, so claude-code's "✳ Claude Code" title leaks
	// into the grid (see oscfilter.go). The raw ring buffer keeps the untouched
	// bytes; only the emulator feed is filtered.
	p.vt.Write(p.oscFilter.Filter(data))
	p.contentGen++
}

// outstandingSubagents totals the pane's subagent ledger.
//
// The ledger is keyed by agent_type so a SubagentStop can only cancel a start
// it can name — a phantom stop must not drain an unrelated live agent. The
// sidebar badge is the one consumer for which the identities do not matter,
// only how many are still running.
//
// Reports the TRACKED total, which is a floor rather than the truth when
// subagentsOverflow is set: a start refused by maxTrackedSubagents may still
// be alive with no entry to count. Callers that render the number are expected
// to mark that (see paneRow), because silently reporting a low number is worse
// than reporting an approximate one.
func (p *PaneModel) outstandingSubagents() int {
	n := 0
	for _, count := range p.subagents {
		n += count
	}
	return n
}

// ResetVT creates a fresh VT emulator at the current dimensions, clearing
// ghost buffer state so live output starts with a clean cursor position.
func (p *PaneModel) ResetVT() {
	w, h := p.vt.Width(), p.vt.Height()
	p.installVT(p.newVTEmulator(w, h))
	p.rawBuf.Reset()
	p.cursorVisible = true
	// Fresh emulator starts with every mode off; clear the mirrored flags so
	// a wheel event isn't forwarded — and a paste isn't bracketed — until the
	// new app re-enables the mode.
	p.mouseX10, p.mouseNormal, p.mouseButton, p.mouseAny, p.mouseSGR = false, false, false, false, false
	p.bracketedPaste, p.bracketedPasteSeen = false, false
	p.contentGen++
}

func (p *PaneModel) ResizeVT(cols, rows int) {
	if cols <= 0 || rows <= 0 || (cols == p.vt.Width() && rows == p.vt.Height()) {
		return
	}
	// Resize the emulator in place instead of rebuilding it from the raw PTY
	// ring buffer. Historical bytes from TUI apps (Claude Code, vim, htop,
	// fzf) contain CUP / scroll-region sequences laid out for the previous
	// width; replaying them into a freshly-sized emulator stamps narrow-
	// column ghost rows into the new screen. The x/vt library's Resize
	// preserves the current screen state, and the PTY child will redraw via
	// SIGWINCH (triggered separately by MsgResizePane) into the new size.
	p.vt.Resize(cols, rows)
	p.contentGen++
}

// maxScroll is the upper bound for scrollBack. In preview mode scrollBack
// counts VISUAL rows (wrapped segments beyond one viewport); natively it
// counts emulator scrollback lines.
func (p *PaneModel) maxScroll() int {
	if p.previewMode() {
		innerH := p.Height - 2
		if innerH < 1 {
			innerH = 1
		}
		m := p.previewLayoutFor(max(1, p.Width-2)).totalVisual() - innerH
		if m < 0 {
			m = 0
		}
		return m
	}
	return p.vt.ScrollbackLen()
}

func (p *PaneModel) ScrollUp(lines int) {
	p.scrollBack += lines
	if max := p.maxScroll(); p.scrollBack > max {
		p.scrollBack = max
	}
}

func (p *PaneModel) ScrollDown(lines int) {
	p.scrollBack -= lines
	if p.scrollBack < 0 {
		p.scrollBack = 0
	}
}

func (p *PaneModel) ResetScroll() {
	p.scrollBack = 0
}

// setMouseMode records a DEC private mode toggle reported by the VT emulator's
// EnableMode/DisableMode callback. Only the mouse modes and bracketed paste
// (?2004) are tracked; every other mode is ignored. Runs on the Update
// goroutine (inside vt.Write).
func (p *PaneModel) setMouseMode(mode ansi.Mode, on bool) {
	switch mode {
	case ansi.ModeMouseX10:
		p.mouseX10 = on
	case ansi.ModeMouseNormal:
		p.mouseNormal = on
	case ansi.ModeMouseButtonEvent:
		p.mouseButton = on
	case ansi.ModeMouseAnyEvent:
		p.mouseAny = on
	case ansi.ModeMouseExtSgr:
		p.mouseSGR = on
	case ansi.ModeBracketedPaste:
		p.bracketedPaste = on
		p.bracketedPasteSeen = true
	}
}

// MouseTracking reports whether the pane's child app has enabled any mouse
// tracking mode — i.e. it wants to handle mouse events (wheel scroll, clicks)
// itself rather than letting Quil scroll its local scrollback. Combines the
// local emulator state (fast path for freshly-created panes whose mouse-enable
// burst we just saw) with the daemon-authoritative flag (the reliable path on
// reattach, where the burst was emitted before this client connected).
func (p *PaneModel) MouseTracking() bool {
	return p.mouseX10 || p.mouseNormal || p.mouseButton || p.mouseAny || p.daemonMouseTracking
}

// BracketedPasteEnabled reports whether the pane's child app has enabled
// bracketed paste (?2004) — i.e. pasted text should be wrapped in
// \x1b[200~/\x1b[201~ markers. Apps that never enabled the mode must receive
// pastes as raw bytes: injecting markers they didn't ask for corrupts their
// stdin (e.g. `cat > file` writes the escape bytes into the file).
//
// Deliberately NOT the `local || daemon` shape MouseTracking uses. The daemon
// flag rides the workspace snapshot, which is throttled by the mode-broadcast
// cooldown, so it lags a disable by up to that window. OR-ing the two would
// keep wrapping pastes for an app that has just turned the mode off — and for
// this mode the cost is escape bytes injected into the app's stdin, the exact
// corruption the gate exists to prevent, rather than MouseTracking's cosmetic
// stray wheel notch. So the local emulator wins once it has actually seen a
// toggle for this pane; the daemon flag covers only the case the local
// emulator cannot answer — reattaching to an app that announced the mode
// before this client connected.
func (p *PaneModel) BracketedPasteEnabled() bool {
	if p.bracketedPasteSeen {
		return p.bracketedPaste
	}
	return p.daemonBracketedPaste
}

// wheelForwardSeq returns the mouse-wheel escape sequence to forward to the
// PTY child, or nil when the app has not enabled mouse tracking. relX/relY are
// content-relative (0-based) and are clamped to the emulator grid; the encoding
// follows the app's requested mode (SGR when ?1006 is set, else legacy X10).
func (p *PaneModel) wheelForwardSeq(up bool, relX, relY int) []byte {
	if !p.MouseTracking() {
		return nil
	}
	if w := p.vt.Width(); w > 0 {
		if relX < 0 {
			relX = 0
		} else if relX >= w {
			relX = w - 1
		}
	} else if relX < 0 {
		relX = 0
	}
	if h := p.vt.Height(); h > 0 {
		if relY < 0 {
			relY = 0
		} else if relY >= h {
			relY = h - 1
		}
	} else if relY < 0 {
		relY = 0
	}
	btn := ansi.MouseWheelUp
	if !up {
		btn = ansi.MouseWheelDown
	}
	b := ansi.EncodeMouseButton(btn, false, false, false, false)
	// SGR is the modern default; apps parse it regardless of the mode they
	// set, and it has no 223-column coordinate limit. Use legacy X10 only when
	// we positively know SGR is not in play (local emulator saw no ?1006 and
	// the daemon didn't report it either).
	if p.mouseSGR || p.daemonMouseSGR {
		return []byte(ansi.MouseSgr(b, relX, relY, false))
	}
	// X10 encodes each coordinate in a single byte (32+1+coord), so it cannot
	// represent a position past column/row 222 (0-based) — beyond that the byte
	// wraps into garbage. Clamp to the representable maximum on very large panes.
	const x10Max = 222
	if relX > x10Max {
		relX = x10Max
	}
	if relY > x10Max {
		relY = x10Max
	}
	return []byte(ansi.MouseX10(b, relX, relY))
}

// ScrollToRelY positions the scrollback so that the scrollbar thumb's TOP
// row lands at relY (relative to the content area, 0..innerH-1). Inverse
// of the thumb-position formula in renderScrollback — a click at row R
// puts the thumb's top at R, matching standard GUI scrollbar UX.
//
// CONTRACT (must stay in sync with renderScrollback):
//
//	renderScrollback:  thumbSize = max(1, h*h/totalLines)
//	                   thumbPos  = viewStart * (h - thumbSize) / scrollRange
//	                              where scrollRange = totalLines - h = sbLen
//	this fn (inverse): viewStart = relY * sbLen / (innerH - thumbSize)
//
// Drift between the two is a silent UX bug. The integer math is safe on
// every supported quil platform (Go int is 64-bit on amd64 and arm64);
// even a million-line scrollback with a thousand-row pane multiplies to
// well under 2^63.
//
// Out-of-range relY clamps to the valid scroll extent. Returns silently
// (no-op) when there's no scrollback to scroll into or the visible area
// is large enough to hold every line (no scrollable range).
func (p *PaneModel) ScrollToRelY(relY, innerH int) {
	// Preview mode scrolls in visual rows; the same inverse-thumb contract
	// holds with sbLen replaced by the visual scroll range.
	sbLen := p.maxScroll()
	if sbLen <= 0 || innerH <= 0 {
		return
	}
	totalLines := sbLen + innerH
	thumbSize := innerH * innerH / totalLines
	if thumbSize < 1 {
		thumbSize = 1
	}
	maxThumbPos := innerH - thumbSize
	if maxThumbPos <= 0 {
		return
	}
	if relY < 0 {
		relY = 0
	}
	if relY > maxThumbPos {
		relY = maxThumbPos
	}
	viewStart := relY * sbLen / maxThumbPos
	p.scrollBack = sbLen - viewStart
}

// restoreSettled reports whether the resuming/preparing restore state should
// clear: the pane is now showing visible content (after the minimum display
// time), or the safety cap elapsed. "Visible content" — not merely "first byte
// received" — is the right signal. claude-code emits terminal-setup bytes and a
// screen clear seconds before the resumed session paints, so gating on the first
// byte (liveOutputSeen) cleared the indicator while the pane was still blank for
// 5-15s.
//
// Since claude-code gained ghost_buffer = true this mostly settles immediately
// for it — a replayed pane is not blank — so the blank-boot gap now belongs to
// panes with nothing to replay: a fresh one, or one whose buffer was lost.
func (p *PaneModel) restoreSettled() bool {
	if time.Since(p.resumeStart) >= restoreSafetyCap {
		return true
	}
	return !p.screenBlank() && time.Since(p.resumeStart) >= restoreMinDisplay
}

// screenBlank reports whether the live VT screen has no visible content: every
// cell empty/space with no styling (a space with a background colour counts as
// visible). Cheap — returns on the first visible cell. Scrollback is irrelevant
// here; the indicator only shows at scrollBack==0.
func (p *PaneModel) screenBlank() bool {
	w, h := p.vt.Width(), p.vt.Height()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cell := p.vt.CellAt(x, y)
			if cell == nil {
				continue
			}
			if cell.Content != "" && cell.Content != " " {
				return false
			}
			if !cell.Style.IsZero() {
				return false
			}
		}
	}
	return true
}

// showRestoreIndicator reports whether View() should overlay the centered
// restore indicator: the pane is mid-restore/spawn (or still a deferred,
// not-yet-spawned pane) and its body is currently blank. Gating on the actual
// blank screen (rather than the ghost/liveOutputSeen flags) keeps the indicator
// up through claude-code's multi-second boot — where the child has emitted bytes
// and cleared the screen but not yet painted — and hides it the instant any
// content (ghost or live) fills the screen. Pending covers lazy-restored panes
// that have not spawned yet (other tabs), which arm resuming on spawn.
func (p *PaneModel) showRestoreIndicator() bool {
	return (p.resuming || p.preparing || p.Pending) && p.scrollBack == 0 && p.screenBlank()
}

// spinnerRunning reports whether the per-pane spinner chain should tick again.
//
// A worktree checkout is checked FIRST and is deliberately exempt from
// restoreSettled: that function bounds a pane BOOT — restoreMinDisplay up to
// restoreSafetyCap, seconds — while `git worktree add` against a large monorepo
// runs for minutes and the daemon allows it two. A frozen glyph in front of work
// that is still happening is exactly the "is this stuck?" question the indicator
// exists to answer, and it is worse than no glyph at all.
//
// It also cannot use screenBlank() as the other indicator does: a placeholder has
// no child, so nothing will ever paint over it. The daemon clearing the branch is
// the only thing that ends this wait.
func (p *PaneModel) spinnerRunning() bool {
	if p.PreparingWorktree != "" {
		return true
	}
	return (p.resuming || p.preparing) && !p.restoreSettled()
}

// restoreSettles reports whether an arriving output frame should take the
// restore indicator down, and is the ONE predicate both output call sites use.
//
// It exists because those two sites hand-rolled
// `(resuming || preparing) && restoreSettled()` and therefore knew nothing about
// a worktree checkout. Harmless as things stand — a placeholder has no child, so
// no output frame can reach them, and the restart guard is what keeps it that
// way — but "correct because another guard makes it unreachable" is exactly the
// coupling that breaks quietly when one of the two moves. Refusing here makes it
// structural instead.
//
// Pinned by a direct test rather than through the call sites, deliberately: a
// call-site test for this state would be vacuous, since nothing can deliver
// output to a pane that has no process.
func (p *PaneModel) restoreSettles() bool {
	if p.PreparingWorktree != "" {
		return false
	}
	return (p.resuming || p.preparing) && p.restoreSettled()
}

// restoreContext builds the dim second line: "<type> · <name-or-cwd-basename>".
// Falls back to just the type when neither a name nor a CWD is known.
func (p *PaneModel) restoreContext() string {
	typ := p.Type
	if typ == "" {
		typ = "terminal"
	}
	detail := p.Name
	if detail == "" && p.CWD != "" {
		detail = filepath.Base(p.CWD)
	}
	if detail == "" {
		return typ
	}
	return typ + " · " + detail
}

type stepState int

const (
	stepDone    stepState = iota // ✓ completed
	stepActive                   // ⠹ in progress (gets the spinner)
	stepPending                  // · not reached yet
	stepNone                     // ─ neutral (e.g. no saved history); rendering: '─'
)

type restoreStep struct {
	text  string
	state stepState
}

// resumeLabel is row 3 of the checklist: a human description of the resume
// strategy for this pane type, with the tracked session-id prefix appended when
// the plugin restores its own history through a session id.
//
// The switch is a copy table, not dispatch: "resuming claude" vs "reconnecting
// ssh" is per-plugin phrasing, and no plugin field carries a verb phrase. The
// capability question — does a session id mean anything for this pane — is the
// restoresViaSession parameter, which Model resolves from the plugin's resume
// strategy.
func resumeLabel(paneType, sessionID string, restoresViaSession bool) string {
	var base string
	switch paneType {
	case "claude-code":
		base = "resuming claude"
	case "opencode":
		base = "resuming opencode"
	case "ssh":
		base = "reconnecting ssh"
	case "stripe":
		base = "restarting stripe"
	case "", "terminal":
		base = "restarting shell"
	default:
		base = "starting " + paneType
	}
	// Session id is only meaningful for plugins that resume through one.
	if sessionID != "" && restoresViaSession {
		id := sessionID
		if r := []rune(sessionID); len(r) > 8 {
			id = string(r[:8])
		}
		base += " · " + id
	}
	return base
}

// restoreSteps builds the ordered checklist rows from the pane's restore state.
// Exactly one row is stepActive (the spinner row): row 3 while the pane is still
// deferred (Pending), otherwise row 4 (waiting for the first painted output).
func (p *PaneModel) restoreSteps() []restoreStep {
	steps := []restoreStep{
		{text: "session loaded", state: stepDone},
	}
	// Row 2 — where the history comes from:
	//   - HistoryLines > 0: Quil replayed a saved ghost buffer (terminal/ssh).
	//   - resume tools with a session id: the tool restores the conversation
	//     itself (claude --resume) even though Quil has no ghost buffer.
	//   - otherwise: genuinely nothing to restore (new pane, no session).
	switch {
	case p.HistoryLines > 0:
		steps = append(steps, restoreStep{
			text:  fmt.Sprintf("history restored (%d ln)", p.HistoryLines),
			state: stepDone,
		})
	case p.RestoresViaSession && p.SessionID != "":
		steps = append(steps, restoreStep{text: "history via resume", state: stepDone})
	default:
		steps = append(steps, restoreStep{text: "no saved history", state: stepNone})
	}

	spawned := !p.Pending
	resume := restoreStep{text: resumeLabel(p.Type, p.SessionID, p.RestoresViaSession), state: stepActive}
	wait := restoreStep{text: "waiting for first output", state: stepPending}
	if spawned {
		resume.state = stepDone
		wait.state = stepActive
	}
	return append(steps, resume, wait)
}

// renderRestoreIndicator centers the per-pane restore checklist in an
// innerW×innerH area: one row per restore step, the in-progress row carrying the
// animated spinner. Falls back to a compact single line when the pane is too
// short or narrow for the checklist. Border stays purple (handled in View).
func (p *PaneModel) renderRestoreIndicator(innerW, innerH int) string {
	steps := p.restoreSteps()
	rows := make([]string, len(steps))
	widest := 0
	for i, s := range steps {
		var row string
		switch s.state {
		case stepDone:
			row = restoreDoneStyle.Render("✓") + " " + restoreDimStyle.Render(s.text)
		case stepActive:
			row = restoreAccentStyle.Render(spinnerFrames[p.spinnerFrame%len(spinnerFrames)] + " " + s.text)
		case stepPending:
			row = restoreDimStyle.Render("· " + s.text)
		default: // stepNone
			row = restoreDimStyle.Render("─ " + s.text)
		}
		rows[i] = row
		if w := ansi.StringWidth(row); w > widest {
			widest = w
		}
	}

	// Fallback for panes too small for the checklist.
	if innerH < len(steps)+2 || widest+2 > innerW {
		return p.renderRestoreIndicatorCompact(innerW, innerH)
	}

	block := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return lipgloss.Place(innerW, innerH, lipgloss.Center, lipgloss.Center, block)
}

// renderSpawnError fills the pane with the reason it has no process, plus the
// key that retries.
//
// sanitizeRemoteText because the message interpolates a PATH from a daemon the
// user may not control under --remote, and this text is drawn straight into the
// frame rather than through the VT emulator that makes pane output safe.
//
// It offers Alt+R and nothing more specific. Quil records the worktree's path,
// not the repository it branched from, so "respawn in the repo" is an offer it
// could not honour — and inventing a destination is the same class of
// confidently-wrong answer the whole no-relocation rule exists to remove.
func (p *PaneModel) renderSpawnError(innerW, innerH int) string {
	// BOUNDED as well as sanitized, and the two are different jobs:
	// sanitizeRemoteText removes escapes without shortening anything, while
	// lipgloss.Place pads but never CLIPS — so an over-wide block is returned
	// whole and the pane body grows past its rect, shifting the whole tab's
	// JoinHorizontal. The message interpolates a full path, and a pane is at
	// its narrowest exactly when this fires: spawnRestoredPane sets the error
	// during restore, before the pane's first size message.
	//
	// Elided in the MIDDLE because the informative half of a worktree path is
	// its tail — the branch name — and cutting the end leaves every message
	// reading "worktree is gone: E:\Projects\Stuka…".
	msg := elideMiddle(sanitizeRemoteText(p.SpawnError), innerW)
	rows := []string{spawnErrorStyle.Render(msg)}
	// The hint costs two rows. Dropped rather than overflowing on a pane too
	// short for it: the reason is what the user needs, the key is in the docs.
	if innerH >= 3 {
		rows = append(rows, "", restoreDimStyle.Render(truncateToWidth("Alt+R to retry", innerW)))
	}
	return lipgloss.Place(innerW, innerH, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, rows...))
}

// renderPreparingWorktree fills the pane with the branch its worktree is being
// checked out for.
//
// It borrows renderSpawnError's shape rather than the restore checklist's,
// because there is exactly one thing happening and no steps to tick off — and
// the checklist's vocabulary ("session loaded", "waiting for first output")
// describes a pane that is booting, which this one is not: it has no process and
// is not going to get one. The pane that arrives will be a different pane.
//
// The duration note is not padding. A `git worktree add` against a large
// monorepo runs for minutes, and "is this thing stuck" is the question the whole
// indicator exists to answer.
func (p *PaneModel) renderPreparingWorktree(innerW, innerH int) string {
	glyph := spinnerFrames[p.spinnerFrame%len(spinnerFrames)]
	// Bounded AND sanitized, two different jobs — see renderSpawnError. Elided
	// in the MIDDLE because the informative half of a branch name is its tail.
	branch := elideMiddle(sanitizeRemoteText(p.PreparingWorktree), innerW)
	// ONE row unconditionally, like renderSpawnError — and for the reason its
	// comment gives: lipgloss.Place pads but never CLIPS, so a block taller than
	// innerH is returned whole and the pane body grows past its rect, shifting
	// every sibling in the tab's JoinHorizontal. Measured before this gate: at
	// Height 0-3 the budget is 3 lines and this rendered 4, while
	// renderSpawnError rendered 3.
	//
	// The BRANCH is the row that survives after the first, never the
	// reassurance line: it is the only part that says which checkout this is.
	rows := []string{
		restoreAccentStyle.Render(truncateToWidth(glyph+"  creating worktree", innerW)),
	}
	if innerH >= 2 {
		rows = append(rows, restoreDimStyle.Render(branch))
	}
	if innerH >= 4 {
		rows = append(rows, "", restoreDimStyle.Render(
			truncateToWidth("this can take a while on a large repository", innerW)))
	}
	return lipgloss.Place(innerW, innerH, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, rows...))
}

// renderRestoreIndicatorCompact is the small single-line indicator used when the
// pane is too small for the full checklist.
func (p *PaneModel) renderRestoreIndicatorCompact(innerW, innerH int) string {
	glyph := spinnerFrames[p.spinnerFrame%len(spinnerFrames)]
	label := "Rebuilding session"
	if p.preparing {
		label = "Building new pane"
	}
	block := restoreAccentStyle.Render(glyph + "  " + label)
	if ctx := p.restoreContext(); ctx != "" {
		block += "\n" + restoreDimStyle.Render(ctx)
	}
	return lipgloss.Place(innerW, innerH, lipgloss.Center, lipgloss.Center, block)
}

func (p *PaneModel) View() string {
	key := p.renderKey()
	if p.hasCache && key == p.cachedKey {
		return p.cachedView
	}
	p.renderCount++

	borderColor := lipgloss.Color("238")
	if p.unseen {
		// Green — finished while this pane was not focused. A PARK is no
		// longer one of the reasons: workPark keeps turnActive, so `working`
		// does not fall and no unseen mark is set. A parked pane is carried by
		// the sidebar's ▲ and by tabBlocked (which deliberately includes the
		// active tab, citing the unfocused-split case) rather than here.
		borderColor = lipgloss.Color("28")
	}
	if p.pinnedAttention {
		// Purple 141, matching sidebarPinnedStyle and pinnedTabStyle — and
		// deliberately NOT the green above, which it used to share. One is the
		// user's own mark and the other is the agent finishing; they clear by
		// different means (only the pin needs an explicit Unmark), so a single
		// colour for both left the user waiting for a green that never goes.
		// Second, so a pane that is both shows the pin: unseen clears on focus
		// by itself, the pin does not.
		borderColor = lipgloss.Color("141")
	}
	if p.markedForDeletion {
		// Red 160, matching sidebarDeletionStyle. Mutually exclusive with the
		// pin on the daemon, so the two cannot genuinely collide — placed after
		// it anyway so a client rendering mid-broadcast, with both flags briefly
		// set, shows the mark the user just chose rather than the one they
		// replaced.
		borderColor = lipgloss.Color("160")
	}
	if p.Active {
		borderColor = lipgloss.Color("57")
	}
	// A pane waiting on a worktree joins the set: it is the same "not ready
	// yet" state, and the muted border is half of what stops it reading as an
	// ordinary pane.
	if p.ghost || p.resuming || p.preparing || p.PreparingWorktree != "" {
		borderColor = lipgloss.Color("95") // muted purple — distinct but not jarring
	}
	if p.splitDragHighlight {
		borderColor = lipgloss.Color("39") // bright blue — split drag in progress
	}
	if p.ctxTargetHighlight {
		borderColor = lipgloss.Color("39") // bright blue — context-menu target
	}
	if p.mcpHighlight {
		borderColor = lipgloss.Color("208") // orange — MCP interaction
	}

	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	content := p.renderContent(p.activeSel)
	if p.showRestoreIndicator() {
		content = p.renderRestoreIndicator(innerW, innerH)
	}
	// Before the spawn-error check and after the restore one, which is the
	// order of certainty: this pane has no VT content to show and never will,
	// but a failure that has already landed outranks a wait that is over.
	// Ungated on screenBlank(), unlike the restore indicator — there is no
	// child to paint anything, so "blank" is the permanent state rather than a
	// window that closes.
	if p.PreparingWorktree != "" {
		content = p.renderPreparingWorktree(innerW, innerH)
	}
	// A pane with no process says WHY, in its own rectangle. Checked after the
	// restore indicator and last, because it is a terminal state: the pane is
	// not coming up, so a spinner claiming otherwise would be the wrong answer.
	//
	// Not a modal: this fires during restore, potentially for several panes at
	// once, and a modal each would be unusable. Not a log line either — a
	// failure nobody sees is the silent relocation this replaces.
	if p.SpawnError != "" {
		content = p.renderSpawnError(innerW, innerH)
	}

	// Render content with left, right, bottom borders (no top).
	// Lipgloss v2: Width/Height include borders in the budget (v1 was additive).
	// +2 width for left+right borders, +1 height for bottom border (top removed).
	bodyStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(borderColor).
		Width(innerW + 2).
		Height(innerH + 1)

	body := bodyStyle.Render(content)

	// Manual top border: CWD on the left, pane name on the right.
	// Muted panes prefix the right label so it's visible at a glance — the
	// border colour stays the same (no risk of confusion with ghost / mcp /
	// active states, each of which already owns a colour slot).
	rightLabel := p.Name
	if p.Muted {
		if rightLabel == "" {
			rightLabel = "[muted]"
		} else {
			rightLabel = "[muted] " + rightLabel
		}
	}
	// The worktree wait reuses the `preparing` label slot rather than adding a
	// third: "preparing..." is what it is, and the branch is already in the
	// pane body, where there is room to elide it honestly.
	topLine := buildTopBorder(p.Width, p.CWD, rightLabel, borderColor, p.ghost, p.resuming,
		p.preparing || p.PreparingWorktree != "", p.focusMode, p.spinnerFrame, p.working, p.workFrame)

	out := topLine + "\n" + body
	p.cachedKey, p.cachedView, p.hasCache = key, out, true
	return out
}

func buildTopBorder(width int, cwd, name string, color color.Color, ghost, resuming, preparing, focus bool, spinnerFrame int, working bool, workFrame int) string {
	if ghost {
		if name == "" {
			name = "restored"
		} else {
			name = name + " · restored"
		}
	}

	// Spinner overrides the right label temporarily
	if resuming || preparing {
		frame := spinnerFrames[spinnerFrame%len(spinnerFrames)]
		label := "resuming..."
		if preparing {
			label = "preparing..."
		}
		name = frame + " " + label
	}

	style := lipgloss.NewStyle().Foreground(color)
	b := lipgloss.RoundedBorder()
	innerW := width - 2
	if innerW < 1 {
		return style.Render(b.TopLeft + b.TopRight)
	}

	// Right label: pane name or spinner (only if it fits with padding).
	rightLabel := ""
	rightLen := 0
	if name != "" && len([]rune(name))+4 <= innerW {
		rightLabel = " " + name + " "
		rightLen = len([]rune(rightLabel))
	}

	// Optional working spinner — a fixed leading segment drawn before the CWD.
	// Reserved width is excluded from the CWD truncation budget so the spinner
	// itself is never cut off (the CWD truncates from its left with "…tail").
	spin := ""
	spinLen := 0
	if working {
		spin = " " + workingGlyph(workFrame)
		spinLen = 2 // leading space + single-width braille glyph
	}

	// Left label: CWD, truncated with ellipsis if needed.
	leftLabel := ""
	leftLen := 0
	if cwd != "" {
		available := innerW - rightLen - 1 - spinLen // reserve 1 dash + spinner
		cwdLabel := " " + cwd + " "
		cwdLabelLen := len([]rune(cwdLabel))

		if available < 0 {
			available = 0
		}
		if cwdLabelLen <= available {
			leftLabel = cwdLabel
			leftLen = cwdLabelLen
		} else if available >= 6 {
			// Truncate CWD from the left: " …tail "
			maxCwd := available - 4 // 4 = len(" …") + len(" ")
			cwdRunes := []rune(cwd)
			leftLabel = " …" + string(cwdRunes[len(cwdRunes)-maxCwd:]) + " "
			leftLen = len([]rune(leftLabel))
		}
	} else if working {
		// No CWD but working: still show the spinner with a trailing space.
		leftLabel = " "
		leftLen = 1
	}

	// Prepend the spinner segment (never truncated).
	leftLabel = spin + leftLabel
	leftLen += spinLen

	dashes := innerW - leftLen - rightLen
	if dashes < 0 {
		dashes = 0
	}

	// Focus mode: center "* FOCUS *" relative to the full border width
	if focus {
		focusLabel := "* FOCUS *"
		focusLen := len([]rune(focusLabel))
		if dashes >= focusLen+2 {
			// Center position relative to full innerW, then subtract left/right label offsets
			centerPos := (innerW - focusLen) / 2
			leftDash := centerPos - leftLen
			if leftDash < 1 {
				leftDash = 1
			}
			rightDash := dashes - focusLen - leftDash
			if rightDash < 0 {
				rightDash = 0
			}
			return style.Render(b.TopLeft + leftLabel +
				strings.Repeat(b.Top, leftDash) + focusLabel + strings.Repeat(b.Top, rightDash) +
				rightLabel + b.TopRight)
		}
	}

	return style.Render(b.TopLeft + leftLabel + strings.Repeat(b.Top, dashes) + rightLabel + b.TopRight)
}

// parseOSC7Path extracts a filesystem path from an OSC 7 URI (file://host/path).
func parseOSC7Path(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		if raw != "" {
			return raw // treat as plain path
		}
		return ""
	}
	path := u.Path
	// Windows: url.Parse("file:///C:/foo") gives Path="/C:/foo"; strip leading /.
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return path
}

func (p *PaneModel) renderContent(sel *Selection) string {
	// Wide-canvas preview: wrapped view of the window-sized buffer.
	if p.previewMode() {
		return p.renderPreview(sel)
	}
	// If selection is active on this pane, use cell-by-cell rendering
	if sel != nil && sel.PaneID == p.ID {
		return p.renderWithSelection(sel)
	}

	if p.scrollBack == 0 {
		// Live view — use Render() for full color support
		content := p.vt.Render()
		// Software reverse-video caret at the VT cursor for every pane
		// type. Interactive apps (claude-code, opencode) position the VT
		// cursor at their input caret exactly like shells do. A real
		// hardware cursor (tea.View.Cursor) was tried and reverted:
		// repositioning it every frame desynced Bubble Tea's diff writer
		// on Windows — the first typed character after a fresh input line
		// landed one cell off ("Test" → "T est").
		if p.Active && p.cursorVisible {
			content = p.insertCursor(content)
		}
		return content
	}

	// Scrollback view — render from scrollback + screen cells
	return p.renderScrollback()
}

// renderWithSelection renders content cell-by-cell with selection highlighting.
func (p *PaneModel) renderWithSelection(sel *Selection) string {
	w := p.vt.Width()
	h := p.vt.Height()
	sbLen := p.vt.ScrollbackLen()

	viewStart := sbLen - p.scrollBack

	lines := make([]string, h)
	for i := 0; i < h; i++ {
		absLine := viewStart + i

		var getCell func(x int) *uv.Cell
		if absLine < 0 {
			getCell = func(x int) *uv.Cell { return nil }
		} else if absLine < sbLen {
			srcLine := absLine
			getCell = func(x int) *uv.Cell {
				return p.vt.ScrollbackCellAt(x, srcLine)
			}
		} else {
			screenLine := absLine - sbLen
			getCell = func(x int) *uv.Cell {
				return p.vt.CellAt(x, screenLine)
			}
		}

		selStart, selEnd := sel.ColRange(absLine, w)
		lines[i] = p.styledCellLineWithSelection(getCell, w, selStart, selEnd)
	}

	return strings.Join(lines, "\n")
}

func (p *PaneModel) renderScrollback() string {
	w := p.vt.Width()
	h := p.vt.Height()
	sbLen := p.vt.ScrollbackLen()

	// viewStart is the first line to show (in combined scrollback+screen space)
	viewStart := sbLen - p.scrollBack

	lines := make([]string, h)
	for i := 0; i < h; i++ {
		srcLine := viewStart + i

		if srcLine < 0 {
			lines[i] = ""
		} else if srcLine < sbLen {
			lines[i] = p.styledCellLine(func(x int) *uv.Cell {
				return p.vt.ScrollbackCellAt(x, srcLine)
			}, w)
		} else {
			screenLine := srcLine - sbLen
			lines[i] = p.styledCellLine(func(x int) *uv.Cell {
				return p.vt.CellAt(x, screenLine)
			}, w)
		}
	}

	// Add scrollbar on the right side
	totalLines := sbLen + h
	thumbSize := max(1, h*h/totalLines)
	scrollRange := totalLines - h
	thumbPos := 0
	if scrollRange > 0 {
		thumbPos = viewStart * (h - thumbSize) / scrollRange
	}
	if thumbPos < 0 {
		thumbPos = 0
	}

	for i, line := range lines {
		ch := "░"
		if i >= thumbPos && i < thumbPos+thumbSize {
			ch = "█"
		}
		// Ensure line is exactly w-1 columns, then append scrollbar character
		lineW := ansi.StringWidth(line)
		if lineW > w-1 {
			line = ansi.Truncate(line, w-1, "")
		} else if lineW < w-1 {
			line = line + strings.Repeat(" ", w-1-lineW)
		}
		lines[i] = line + "\x1b[90m" + ch + "\x1b[0m"
	}

	return strings.Join(lines, "\n")
}

// styledCellLineWithSelection renders a row with optional selection highlighting.
// selStart/selEnd define the selected column range (-1 = no selection on this row).
func (p *PaneModel) styledCellLineWithSelection(getCell func(x int) *uv.Cell, width, selStart, selEnd int) string {
	var b strings.Builder
	var lastSGR string
	var pending int

	for x := 0; x < width; x++ {
		cell := getCell(x)
		// Wide-char continuation cell — the lead cell already spans this
		// column; emitting anything here drifts the rest of the row right.
		if cell != nil && cell.Width == 0 {
			continue
		}
		ch := " "
		styled := false
		var sgr string

		if cell != nil {
			if cell.Content != "" {
				ch = cell.Content
			}
			if !cell.Style.IsZero() {
				styled = true
				sgr = cell.Style.String()
			}
		}

		// Check if this cell is selected
		inSelection := selStart >= 0 && x >= selStart && x <= selEnd

		if inSelection {
			// Flush pending spaces before selection
			if pending > 0 {
				b.WriteString(strings.Repeat(" ", pending))
				pending = 0
			}
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			// Render with reverse video
			b.WriteString("\x1b[7m")
			b.WriteString(ch)
			b.WriteString("\x1b[m")
			continue
		}

		// Normal rendering (same as styledCellLine)
		if ch == " " && !styled {
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			pending++
			continue
		}

		if pending > 0 {
			b.WriteString(strings.Repeat(" ", pending))
			pending = 0
		}
		if sgr != lastSGR {
			if !styled && lastSGR != "" {
				b.WriteString("\x1b[m")
			} else if styled {
				b.WriteString(sgr)
			}
			lastSGR = sgr
		}
		b.WriteString(ch)
	}

	if lastSGR != "" {
		b.WriteString("\x1b[m")
	}
	return b.String()
}

// styledCellLine renders a row of cells with ANSI styles preserved.
// Trailing unstyled spaces are buffered and only flushed when followed by
// visible content, so the result is naturally right-trimmed.
func (p *PaneModel) styledCellLine(getCell func(x int) *uv.Cell, width int) string {
	var b strings.Builder
	var lastSGR string
	var pending int // buffered trailing unstyled spaces

	for x := 0; x < width; x++ {
		cell := getCell(x)
		// Wide-char continuation cell — the lead cell already spans this
		// column; emitting anything here drifts the rest of the row right.
		if cell != nil && cell.Width == 0 {
			continue
		}
		ch := " "
		styled := false
		var sgr string

		if cell != nil {
			if cell.Content != "" {
				ch = cell.Content
			}
			if !cell.Style.IsZero() {
				styled = true
				sgr = cell.Style.String()
			}
		}

		// Unstyled space — buffer (may be trailing)
		if ch == " " && !styled {
			if lastSGR != "" {
				b.WriteString("\x1b[m")
				lastSGR = ""
			}
			pending++
			continue
		}

		// Non-trivial cell: flush buffered spaces, then render
		if pending > 0 {
			b.WriteString(strings.Repeat(" ", pending))
			pending = 0
		}
		if sgr != lastSGR {
			if !styled && lastSGR != "" {
				b.WriteString("\x1b[m")
			} else if styled {
				b.WriteString(sgr)
			}
			lastSGR = sgr
		}
		b.WriteString(ch)
	}

	// Reset at end if style was active (trailing spaces already dropped)
	if lastSGR != "" {
		b.WriteString("\x1b[m")
	}
	return b.String()
}

func (p *PaneModel) insertCursor(content string) string {
	pos := p.vt.CursorPosition()
	lines := strings.Split(content, "\n")

	if pos.Y < 0 || pos.Y >= len(lines) {
		return content
	}

	// Rebuild cursor line from cell data to avoid ANSI string splitting issues.
	w := p.vt.Width()
	var b strings.Builder

	for x := 0; x < w; x++ {
		cell := p.vt.CellAt(x, pos.Y)
		// Wide-char continuation cell — the lead cell already spans this
		// column (cursor landing on one is a degenerate case; skip it too).
		if cell != nil && cell.Width == 0 {
			continue
		}
		ch := " "
		if cell != nil && cell.Content != "" {
			ch = cell.Content
		}

		if x == pos.X {
			// Cursor: reset style, render in reverse video
			b.WriteString("\x1b[0m\x1b[7m")
			b.WriteString(ch)
			b.WriteString("\x1b[27m")
		} else {
			// Non-cursor: render with cell's original style
			if cell != nil {
				if sgr := cell.Style.String(); sgr != "" {
					b.WriteString(sgr)
				}
			}
			b.WriteString(ch)
		}
	}
	b.WriteString("\x1b[0m")

	lines[pos.Y] = b.String()
	return strings.Join(lines, "\n")
}

// setDestPaneCount records one destination's pane count and publishes the SUM
// across every destination as the workspace size the adaptive depth divides.
//
// A broadcast is one daemon's full state, so a client holding several hosts
// receives several of them and each reports only its own panes. The scrollback
// budget is a property of this PROCESS, which holds all of them at once, so the
// sum is the only figure that means anything here — publishing a single host's
// count would hand every pane a multiple of the depth the budget allows.
//
// Lives on Model rather than beside the package vars because it is per-client
// state: two Models in one process would each have their own destinations.
func (m *Model) setDestPaneCount(dest string, n int) {
	if m.paneCountByDest == nil {
		m.paneCountByDest = make(map[string]int, 2)
	}
	m.paneCountByDest[dest] = n
	total := 0
	for _, c := range m.paneCountByDest {
		total += c
	}
	SetPaneCount(total)
}
