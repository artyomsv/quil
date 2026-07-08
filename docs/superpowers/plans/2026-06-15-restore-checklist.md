# Per-Pane Restore Checklist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single-line restore indicator with a 4-row per-pane checklist (session loaded → history → resuming <tool>·<id> → waiting for output), each row reflecting a real restore signal and ticking to ✓ as it completes.

**Architecture:** The daemon adds two broadcast-only fields per pane (`session_id`, `history_lines`); the TUI mirrors them onto `PaneModel`, then `renderRestoreIndicator` builds the checklist from pure helpers (`restoreSteps`, `resumeLabel`) driven by `(Type, SessionID, HistoryLines, Pending, screenBlank, spinnerFrame)`. Falls back to today's compact single line on small panes.

**Tech Stack:** Go 1.25, Bubble Tea v2 / Lipgloss v2. Build/test via Docker: `./scripts/dev.sh test` / `vet` / `test-race` (no local Go).

**Spec:** `docs/superpowers/specs/2026-06-15-restore-checklist-design.md`

**PROJECT RULE — no intermediate commits:** Do NOT commit per task. Keep all changes in the working tree; the final task makes ONE commit after full verification. (Per `~/.claude/projects/.../memory/no-intermediate-commits.md`.)

---

## File Structure

- `internal/daemon/daemon.go` — `workspaceStateFromSnapshot`: emit `session_id` + `history_lines` (broadcast-only).
- `internal/daemon/restore_checklist_test.go` — **new**: broadcast carries the fields; disk path omits them.
- `internal/tui/model.go` — `PaneInfo` fields + `workspace_state` parse.
- `internal/tui/workstate.go` — `syncPaneMeta` copies the two fields.
- `internal/tui/pane.go` — `PaneModel` fields; `paneRenderKey` additions; `stepState`/`restoreStep` types; `restoreSteps`, `resumeLabel`, `renderRestoreIndicatorCompact`; rewritten `renderRestoreIndicator`; new `restoreDoneStyle`.
- `internal/tui/restore_indicator_test.go` — step-machine, resumeLabel, render, fallback, View tests.
- `internal/tui/pane_cache_test.go` — cache-key cases for the new fields.

---

## Task 1: Daemon broadcasts `session_id` + `history_lines`

**Files:**
- Modify: `internal/daemon/daemon.go` (inside `workspaceStateFromSnapshot`, the per-pane `PluginMu` block ~line 1723-1763)
- Test: `internal/daemon/restore_checklist_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/restore_checklist_test.go`:

```go
package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ringbuf"
)

// paneMapByID pulls the pane entries out of a workspace-state map keyed by id.
func paneMapByID(t *testing.T, state map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	panes, ok := state["panes"].([]map[string]any)
	if !ok {
		t.Fatalf("state[panes] wrong type: %T", state["panes"])
	}
	for _, p := range panes {
		out[p["id"].(string)] = p
	}
	return out
}

func TestWorkspaceState_BroadcastsSessionIDAndHistoryLines(t *testing.T) {
	d := New(config.Default())
	gh := []byte("line1\nline2\nline3\n") // 3 newlines
	pane := &Pane{
		ID: "pane-aa", TabID: "tab-aa", Type: "claude-code",
		OutputBuf:   ringbuf.NewRingBuffer(d.session.bufSize),
		GhostSnap:   gh,
		PluginState: map[string]string{"session_id": "8f2e1c00-dead-beef"},
	}
	d.session.RestoreTab(&Tab{ID: "tab-aa", Name: "A", Panes: []string{"pane-aa"}}, []*Pane{pane})

	// Broadcast path (includeOverlays=true).
	bc := paneMapByID(t, d.buildWorkspaceState())["pane-aa"]
	if bc["session_id"] != "8f2e1c00-dead-beef" {
		t.Errorf("broadcast session_id = %v, want full id", bc["session_id"])
	}
	if hl, _ := bc["history_lines"].(int); hl != 3 {
		t.Errorf("broadcast history_lines = %v, want 3", bc["history_lines"])
	}

	// Disk path (includeOverlays=false) must omit both.
	active, tabs, byTab := d.session.SnapshotState()
	disk := paneMapByID(t, d.workspaceStateFromSnapshot(active, tabs, byTab, false))["pane-aa"]
	if _, ok := disk["session_id"]; ok {
		t.Error("disk snapshot must not contain session_id")
	}
	if _, ok := disk["history_lines"]; ok {
		t.Error("disk snapshot must not contain history_lines")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `broadcast session_id = <nil>` / `history_lines = <nil>` (fields not emitted yet). (If `RestoreTab`/`New`/`bufSize` symbols differ, mirror the exact usage in `internal/daemon/lazy_restore_test.go`, which constructs panes the same way.)

- [ ] **Step 3: Implement the broadcast fields**

In `internal/daemon/daemon.go`, ensure `bytes` is imported (add `"bytes"` to the import block if absent).

In `workspaceStateFromSnapshot`, the per-pane `PluginMu` critical section currently reads `typ`, `cwd`, `isOverlay`, and copies `PluginState`. Add two reads inside that SAME lock (GhostSnap and PluginState are PluginMu-guarded):

Find:
```go
			pane.PluginMu.Lock()
			typ := pane.Type
			cwd := pane.CWD
			isOverlay := pane.Overlay
			if len(pane.PluginState) > 0 {
```
Replace with:
```go
			pane.PluginMu.Lock()
			typ := pane.Type
			cwd := pane.CWD
			isOverlay := pane.Overlay
			sessionID := pane.PluginState["session_id"]
			historyLines := bytes.Count(pane.GhostSnap, []byte{'\n'})
			if len(pane.PluginState) > 0 {
```

Then, in the broadcast-only area, right after the existing `pending` block:
```go
			if includeOverlays {
				pane.spawnMu.Lock()
				pending := pane.Pending
				pane.spawnMu.Unlock()
				if pending {
					paneData["pending"] = true
				}
			}
```
add:
```go
			// Broadcast-only restore-checklist hints (runtime, never persisted):
			// the tracked session id and the ghost-buffer line count the TUI
			// shows in the per-pane restore checklist.
			if includeOverlays {
				if sessionID != "" {
					paneData["session_id"] = sessionID
				}
				if historyLines > 0 {
					paneData["history_lines"] = historyLines
				}
			}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS (`TestWorkspaceState_BroadcastsSessionIDAndHistoryLines` + all existing). Do NOT commit.

---

## Task 2: TUI plumbing — PaneInfo / parse / PaneModel / syncPaneMeta / render key

**Files:**
- Modify: `internal/tui/model.go` (`PaneInfo` struct ~line 56; `workspace_state` parse ~line 3050)
- Modify: `internal/tui/workstate.go` (`syncPaneMeta`)
- Modify: `internal/tui/pane.go` (`PaneModel` struct; `paneRenderKey`; `renderKey`)
- Test: `internal/tui/restore_indicator_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/restore_indicator_test.go`:

```go
func TestSyncPaneMeta_PropagatesSessionAndHistory(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p", testRingBufSize)
	defer p.Dispose()
	syncPaneMeta(p, &PaneInfo{ID: "p", Type: "claude-code", SessionID: "abc123", HistoryLines: 42})
	if p.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want abc123", p.SessionID)
	}
	if p.HistoryLines != 42 {
		t.Errorf("HistoryLines = %d, want 42", p.HistoryLines)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `p.SessionID`/`p.HistoryLines` and `PaneInfo.SessionID`/`.HistoryLines` undefined.

- [ ] **Step 3: Add the fields and wiring**

(a) `internal/tui/model.go` — `PaneInfo` struct, add after `Pending bool`:
```go
	Pending      bool // deferred restore — not yet lazy-spawned
	SessionID    string
	HistoryLines int
```

(b) `internal/tui/model.go` — `workspace_state` parser, after the `pending` block:
```go
				if pending, ok := pm["pending"].(bool); ok {
					pi.Pending = pending
				}
				if sid, ok := pm["session_id"].(string); ok {
					pi.SessionID = sid
				}
				if hl, ok := pm["history_lines"].(float64); ok {
					pi.HistoryLines = int(hl)
				}
```

(c) `internal/tui/workstate.go` — `syncPaneMeta`, after `pane.Pending = info.Pending`:
```go
	pane.Pending = info.Pending
	pane.SessionID = info.SessionID
	pane.HistoryLines = info.HistoryLines
```

(d) `internal/tui/pane.go` — `PaneModel` struct, add near `Pending`:
```go
	Pending        bool                // deferred restore — not yet lazy-spawned (daemon-authoritative)
	SessionID      string              // tracked session id (daemon-authoritative; restore checklist)
	HistoryLines   int                 // ghost-buffer line count (daemon-authoritative; restore checklist)
```
(Run `gofmt` on the struct afterward — adding a longer field name may re-align the type column.)

(e) `internal/tui/pane.go` — `paneRenderKey` struct, add next to `pending`:
```go
	ghost, resuming, preparing     bool
	pending                        bool
	paneType, sessionID            string
	historyLines                   int
	mcpHighlight, muted, focusMode bool
```

(f) `internal/tui/pane.go` — `renderKey()`, add to the literal:
```go
		pending:        p.Pending,
		paneType:       p.Type,
		sessionID:      p.SessionID,
		historyLines:   p.HistoryLines,
		mcpHighlight:   p.mcpHighlight,
```
(Keep the struct literal gofmt-aligned.)

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh vet`
Expected: PASS, vet clean. Do NOT commit.

---

## Task 3: Pure step model — `restoreSteps` + `resumeLabel`

**Files:**
- Modify: `internal/tui/pane.go` (add types + helpers near `restoreContext`)
- Test: `internal/tui/restore_indicator_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/restore_indicator_test.go`:

```go
func TestResumeLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		typ, sid, want string
	}{
		{"claude-code", "8f2e1c00deadbeef", "resuming claude · 8f2e1c00"},
		{"claude-code", "", "resuming claude"},
		{"opencode", "abcdef0123", "resuming opencode · abcdef01"},
		{"terminal", "", "restarting shell"},
		{"", "", "restarting shell"},
		{"ssh", "ignored", "reconnecting ssh"},
		{"stripe", "", "restarting stripe"},
		{"weird", "", "starting weird"},
	}
	for _, tc := range cases {
		if got := resumeLabel(tc.typ, tc.sid); got != tc.want {
			t.Errorf("resumeLabel(%q,%q) = %q, want %q", tc.typ, tc.sid, got, tc.want)
		}
	}
}

func TestRestoreSteps_ClaudeDeferred(t *testing.T) {
	t.Parallel()
	p := &PaneModel{Type: "claude-code", SessionID: "8f2e1c00xx", HistoryLines: 0, Pending: true}
	steps := p.restoreSteps()
	want := []restoreStep{
		{"session loaded", stepDone},
		{"no saved history", stepNone},
		{"resuming claude · 8f2e1c00", stepActive},
		{"waiting for first output", stepPending},
	}
	if len(steps) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(steps), len(want), steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
}

func TestRestoreSteps_TerminalSpawnedWithHistory(t *testing.T) {
	t.Parallel()
	p := &PaneModel{Type: "terminal", HistoryLines: 412, Pending: false}
	steps := p.restoreSteps()
	want := []restoreStep{
		{"session loaded", stepDone},
		{"history restored (412 ln)", stepDone},
		{"restarting shell", stepDone},
		{"waiting for first output", stepActive},
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `resumeLabel`, `restoreStep`, `stepDone`, `restoreSteps` undefined.

- [ ] **Step 3: Implement the model**

In `internal/tui/pane.go`, add `"fmt"` to the import block. Then add, just after `restoreContext`:

```go
type stepState int

const (
	stepDone    stepState = iota // ✓ completed
	stepActive                   // ⠹ in progress (gets the spinner)
	stepPending                  // · not reached yet
	stepNone                     // ─ neutral (e.g. no saved history)
)

type restoreStep struct {
	text  string
	state stepState
}

// resumeLabel is row 3 of the checklist: a human description of the resume
// strategy for this pane type, with the tracked session-id prefix appended for
// the agent plugins when known.
func resumeLabel(paneType, sessionID string) string {
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
	if sessionID != "" && (paneType == "claude-code" || paneType == "opencode") {
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
	if p.HistoryLines > 0 {
		steps = append(steps, restoreStep{
			text:  fmt.Sprintf("history restored (%d ln)", p.HistoryLines),
			state: stepDone,
		})
	} else {
		steps = append(steps, restoreStep{text: "no saved history", state: stepNone})
	}

	spawned := !p.Pending
	resume := restoreStep{text: resumeLabel(p.Type, p.SessionID), state: stepActive}
	wait := restoreStep{text: "waiting for first output", state: stepPending}
	if spawned {
		resume.state = stepDone
		wait.state = stepActive
	}
	return append(steps, resume, wait)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS. Do NOT commit.

---

## Task 4: Render the checklist + fallback + View integration

**Files:**
- Modify: `internal/tui/pane.go` (`renderRestoreIndicator`; add `renderRestoreIndicatorCompact`; add `restoreDoneStyle`)
- Test: `internal/tui/restore_indicator_test.go`

- [ ] **Step 1: Write the failing tests**

In `internal/tui/restore_indicator_test.go`, REPLACE the existing `TestRenderRestoreIndicator_ResumingLabelAndContext` and `TestRenderRestoreIndicator_PreparingLabel` functions with:

```go
func TestRenderRestoreIndicator_Checklist(t *testing.T) {
	t.Parallel()
	p := &PaneModel{resuming: true, Type: "claude-code", SessionID: "8f2e1c00deadbeef", Pending: true}
	out := ansi.Strip(p.renderRestoreIndicator(48, 10))

	for _, want := range []string{"session loaded", "no saved history", "resuming claude · 8f2e1c00", "waiting for first output"} {
		if !strings.Contains(out, want) {
			t.Errorf("checklist missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("checklist missing done marker:\n%s", out)
	}
	if !strings.ContainsAny(out, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("checklist missing active spinner:\n%s", out)
	}
}

func TestRenderRestoreIndicator_FallbackWhenTooSmall(t *testing.T) {
	t.Parallel()
	// Too short for the 4-row checklist → compact single line.
	short := ansi.Strip((&PaneModel{resuming: true, Type: "claude-code"}).renderRestoreIndicator(48, 4))
	if !strings.Contains(short, "Rebuilding session") {
		t.Errorf("short pane should fall back to compact label:\n%s", short)
	}
	// Too narrow for the rows → compact single line.
	narrow := ansi.Strip((&PaneModel{preparing: true, Type: "terminal"}).renderRestoreIndicator(14, 10))
	if !strings.Contains(narrow, "Building new pane") {
		t.Errorf("narrow pane should fall back to compact label:\n%s", narrow)
	}
}
```

Also REPLACE `TestPaneView_ShowsIndicatorWhileResuming` and `TestPaneView_IndicatorPersistsThroughBootClear` bodies' assertion strings (they currently check `"Rebuilding session"`) to check for a checklist row instead — change each `strings.Contains(p.View(), "Rebuilding session")` to `strings.Contains(p.View(), "waiting for first output")`. And in `TestPaneView_NoIndicatorOnceContentArrives`, change the negative check from `"Rebuilding session"` to `"waiting for first output"`.

(`ansi` is already imported in this test file from earlier work; if not, add `"github.com/charmbracelet/x/ansi"`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `renderRestoreIndicator` still renders the old single line, so the checklist substrings are missing.

- [ ] **Step 3: Implement the renderer**

In `internal/tui/pane.go`, add `restoreDoneStyle` to the style var block:
```go
var (
	restoreAccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	restoreDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	restoreDoneStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
)
```

Replace the whole `renderRestoreIndicator` function with:
```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh vet`
Expected: PASS, vet clean. Do NOT commit.

---

## Task 5: Cache-key coverage + full verification + final commit

**Files:**
- Modify: `internal/tui/pane_cache_test.go`

- [ ] **Step 1: Add cache-key cases**

In `internal/tui/pane_cache_test.go`, in the `cases` slice of `TestPaneView_EveryKeyFieldInvalidates`, add after the `pending` case:
```go
		{"pending", func(p *PaneModel) { p.Pending = true }},
		{"sessionID", func(p *PaneModel) { p.resuming = true; p.SessionID = "deadbeef" }},
		{"historyLines", func(p *PaneModel) { p.resuming = true; p.HistoryLines = 7 }},
		{"paneType", func(p *PaneModel) { p.resuming = true; p.Type = "claude-code" }},
```

- [ ] **Step 2: Run the full suite**

Run: `./scripts/dev.sh test`
Expected: PASS — flipping `SessionID`/`HistoryLines`/`Type` invalidates the render cache (they are in `paneRenderKey`).

- [ ] **Step 3: Vet + race**

Run: `./scripts/dev.sh vet && ./scripts/dev.sh test-race`
Expected: vet clean; `ok` for `internal/tui` and `internal/daemon` with `-race`.

- [ ] **Step 4: gofmt check**

Run (Docker): `docker run --rm -v "$(pwd -W 2>/dev/null || pwd):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine sh -c 'for f in internal/tui/pane.go internal/tui/model.go internal/daemon/daemon.go; do tr -d "\r" < $f > /tmp/x.go && diff <(gofmt < /tmp/x.go) /tmp/x.go >/dev/null && echo "$f clean" || echo "$f NEEDS GOFMT"; done'`
Expected: all `clean`. If any `NEEDS GOFMT`, run `gofmt -w` on it (in Docker) and re-check.

- [ ] **Step 5: Single final commit**

Per the no-intermediate-commits rule, this is the ONLY commit for the feature:
```bash
git add internal/daemon/daemon.go internal/daemon/restore_checklist_test.go \
        internal/tui/model.go internal/tui/workstate.go internal/tui/pane.go \
        internal/tui/restore_indicator_test.go internal/tui/pane_cache_test.go
git commit -m "feat(tui): per-pane restore checklist in the restore indicator

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 6: Manual verification (dev mode)**

Build dev binaries (Docker, dev pair only — prod quil.exe is locked while production runs):
`docker run --rm -v "$(pwd -W 2>/dev/null || pwd):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine sh -c 'VER=$(cat VERSION) && F="-s -w -X main.version=$VER -X main.buildDevMode=true -X main.buildLogLevel=debug -X main.daemonBinary=quild-dev" && GOOS=windows GOARCH=amd64 go build -ldflags "$F" -o quil-dev.exe ./cmd/quil && GOOS=windows GOARCH=amd64 go build -ldflags "$F" -o quild-dev.exe ./cmd/quild'`

Then `./scripts/quil-dev.ps1`. **Success criteria:** restore a workspace — the active claude pane shows the 4-row checklist (`✓ session loaded`, `─ no saved history`, `✓ resuming claude · <id8>`, `⠹ waiting for first output`); a terminal pane shows `✓ history restored (N ln)`; switching to a deferred tab shows its pane's spinner on `resuming …` then `waiting for first output`; the indicator vanishes when the session paints.

---

## Self-Review

**Spec coverage:**
- 4-row checklist + markers/states → Task 3 (`restoreSteps`) + Task 4 (render) ✓
- Real `history_lines` / `no saved history` → Task 1 (daemon count) + Task 3 (row 2) ✓
- Resume label + session-id prefix → Task 3 (`resumeLabel`) ✓
- Spinner on the active row (row 3 while Pending, row 4 when spawned) → Task 3 state logic + Task 4 render ✓
- Broadcast-only `session_id` + `history_lines` (not persisted) → Task 1 (`includeOverlays` gate) + test ✓
- TUI plumbing (PaneInfo/parse/PaneModel/syncPaneMeta/key) → Task 2 ✓
- Fallback on small panes → Task 4 (`renderRestoreIndicatorCompact`) ✓
- Cache key for new visual inputs → Task 2 (key fields) + Task 5 (ratchet cases) ✓

**Placeholder scan:** none — every code step has complete code and exact commands.

**Type consistency:** `stepState`/`restoreStep`/`stepDone|stepActive|stepPending|stepNone`, `restoreSteps()`, `resumeLabel(paneType, sessionID)`, `renderRestoreIndicatorCompact`, `restoreDoneStyle`, and the key fields `paneType`/`sessionID`/`historyLines` are named identically across tasks and tests. `PaneInfo.SessionID/HistoryLines` ↔ `PaneModel.SessionID/HistoryLines` ↔ daemon keys `session_id`/`history_lines` are consistent.
