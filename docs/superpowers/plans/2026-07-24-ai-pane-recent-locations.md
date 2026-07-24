# AI Pane Recent Locations + Label Fit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the pane setup dialog's toggle labels fit on one line, and offer the last 5 used working directories as a persisted quick-pick when creating a pane.

**Architecture:** Part 1 shortens 3 TOML toggle labels (no Go change). Part 2 adds a TUI-owned `recent-cwds.json`, a pure `pushRecentCWD` helper, and reuses the existing git repo pick-list UI (generalized) to offer recent dirs when no git repos are discovered.

**Tech Stack:** Go 1.25, Bubble Tea v2 TUI, stdlib `encoding/json`, `BurntSushi/toml` defaults.

## Global Constraints

- Go: `gofmt`, tabs, LF, `MixedCaps` naming, wrap errors with `%w`.
- Never block pane creation on a persistence failure — log-and-continue.
- TUI-owned files use atomic `.tmp` + rename, `0600` (mirror `internal/tui/instances.go`).
- Dev/test only against `.quil/` dev home — never touch `~/.quil/`.
- Commit granularity: squash TDD sub-steps; land the feature in meaningful commits (repo convention: no WIP/spec/plan commits).

---

### Task 1: `recentcwd.go` — persistence + push helper

**Files:**
- Create: `internal/tui/recentcwd.go`
- Create: `internal/tui/recentcwd_test.go`
- Modify: `internal/config/config.go` (add `RecentCWDsPath`)

**Interfaces:**
- Produces: `LoadRecentCWDs(path string) []string`, `SaveRecentCWDs(path string, list []string) error`, `pushRecentCWD(list []string, dir string, max int) []string`, `config.RecentCWDsPath() string`, const `recentCWDMax = 5`.

- [ ] **Step 1: Write failing tests** in `internal/tui/recentcwd_test.go`

```go
package tui

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestPushRecentCWD(t *testing.T) {
	tests := []struct {
		name string
		list []string
		dir  string
		max  int
		want []string
	}{
		{"empty dir is no-op", []string{"a"}, "", 5, []string{"a"}},
		{"whitespace dir is no-op", []string{"a"}, "  ", 5, []string{"a"}},
		{"prepend new", []string{"a", "b"}, "c", 5, []string{"c", "a", "b"}},
		{"dedup moves to front", []string{"a", "b", "c"}, "c", 5, []string{"c", "a", "b"}},
		{"cap truncates oldest", []string{"a", "b", "c"}, "d", 3, []string{"d", "a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pushRecentCWD(tt.list, tt.dir, tt.max); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pushRecentCWD(%v, %q, %d) = %v, want %v", tt.list, tt.dir, tt.max, got, tt.want)
			}
		})
	}
}

func TestLoadSaveRecentCWDs_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent-cwds.json")
	want := []string{"/a", "/b"}
	if err := SaveRecentCWDs(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadRecentCWDs(path); !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip = %v, want %v", got, want)
	}
}

func TestLoadRecentCWDs_MissingFile(t *testing.T) {
	got := LoadRecentCWDs(filepath.Join(t.TempDir(), "nope.json"))
	if len(got) != 0 {
		t.Errorf("missing file = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`pushRecentCWD` undefined)

Run: `./scripts/dev.sh test` (or `go test ./internal/tui/ -run RecentCWD`)
Expected: compile error / FAIL.

- [ ] **Step 3: Implement** `internal/tui/recentcwd.go`

```go
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// recentCWDMax caps how many recent working directories are remembered.
const recentCWDMax = 5

// pushRecentCWD returns list with dir cleaned and moved to the front,
// de-duplicated (case-insensitively on Windows) and truncated to max.
// A blank dir is a no-op. Pure — never mutates the input slice.
func pushRecentCWD(list []string, dir string, max int) []string {
	if strings.TrimSpace(dir) == "" {
		return list
	}
	dir = filepath.Clean(dir)
	out := make([]string, 0, len(list)+1)
	out = append(out, dir)
	for _, d := range list {
		if pathEqual(d, dir) {
			continue
		}
		out = append(out, d)
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// pathEqual compares two paths, case-insensitively on Windows.
func pathEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// LoadRecentCWDs reads the recent-CWD list; empty on missing/corrupt file.
func LoadRecentCWDs(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	return list
}

// SaveRecentCWDs writes the list atomically (.tmp + rename).
func SaveRecentCWDs(path string, list []string) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Add** `RecentCWDsPath` to `internal/config/config.go` (next to `InstancesPath`)

```go
func RecentCWDsPath() string {
	return filepath.Join(QuilDir(), "recent-cwds.json")
}
```

- [ ] **Step 5: Run — expect PASS**

Run: `./scripts/dev.sh test`
Expected: PASS.

---

### Task 2: Shorten Claude Code toggle labels

**Files:**
- Modify: `internal/plugin/defaults/claude-code.toml`

**Interfaces:** none (data-only).

- [ ] **Step 1: Edit labels + bump schema_version** in `internal/plugin/defaults/claude-code.toml`

- `schema_version = 7` → `schema_version = 8`
- line 27 label → `Dangerously skip permissions (no confirmations)`
- line 34 label → `Enable auto mode (safer than skipping permissions)`
- line 45 label → `Chrome support (Claude in Chrome extension)`

- [ ] **Step 2: Add a label-fit assertion** to `internal/tui/setup_dialog_test.go`

```go
func TestClaudeCodeToggleLabelsFitFloor(t *testing.T) {
	reg := plugin.NewRegistry()
	if err := reg.EnsureDefaultPlugins(t.TempDir()); err != nil {
		t.Fatalf("ensure defaults: %v", err)
	}
	p := reg.Get("claude-code")
	if p == nil {
		t.Fatal("claude-code plugin not found")
	}
	// Floor 70 box, 6 row chrome + 4 padding => 60 usable label cells.
	const maxLabel = 60
	for _, tg := range p.Command.Toggles {
		if w := lipgloss.Width(tg.Label); w > maxLabel {
			t.Errorf("label %q width %d exceeds %d (would wrap)", tg.Label, w, maxLabel)
		}
	}
}
```

(Adjust `plugin.NewRegistry()` / `EnsureDefaultPlugins` to the actual constructor + signature used elsewhere in `setup_dialog_test.go`; confirm the import of `lipgloss` + `plugin` is present.)

- [ ] **Step 3: Run — expect PASS**

Run: `./scripts/dev.sh test`
Expected: PASS.

---

### Task 3: Model fields + load + commit-time push

**Files:**
- Modify: `internal/tui/model.go` (add fields + constructor load)
- Modify: `internal/tui/dialog.go` (`handleCreatePaneSplit` push+persist)

**Interfaces:**
- Consumes: Task 1 (`LoadRecentCWDs`, `SaveRecentCWDs`, `pushRecentCWD`, `recentCWDMax`, `config.RecentCWDsPath`).
- Produces: `Model.recentCWDs []string`, `Model.recentCandidates []string`.

- [ ] **Step 1: Add fields** in `internal/tui/model.go` near `lastSelectedCWD` (~line 263)

```go
	recentCWDs       []string               // last N committed CWDs (persisted, recent-cwds.json)
	recentCandidates []string               // recent CWDs offered by the setup dialog; nil = not in recent-pick mode
```

- [ ] **Step 2: Load in constructor** next to `instanceStore: LoadInstances(...)` (~line 391)

```go
		recentCWDs:       LoadRecentCWDs(config.RecentCWDsPath()),
```

- [ ] **Step 3: Push + persist** in `handleCreatePaneSplit` (`internal/tui/dialog.go`), inside the `if cwd != ""` block that sets `lastSelectedCWD`

```go
	if cwd != "" {
		m.lastSelectedCWD = cwd
		m.recentCWDs = pushRecentCWD(m.recentCWDs, cwd, recentCWDMax)
		if err := SaveRecentCWDs(config.RecentCWDsPath(), m.recentCWDs); err != nil {
			log.Printf("create pane: save recent cwds: %v", err)
		}
	}
```

- [ ] **Step 4: Build — expect PASS** (no new behavior test yet; wired in Task 4)

Run: `./scripts/dev.sh test`
Expected: PASS (compiles, existing tests green).

---

### Task 4: Recent-pick seeding + generalized pick UI

**Files:**
- Modify: `internal/tui/dialog.go` (`enterSetupOrSplit`, `renderCreatePaneSetupDialog`, `handleSetupCWDKey`, rename `handleSetupRepoKey`→`handleSetupPickKey`)
- Modify: `internal/tui/setup_dialog_test.go` (seeding + Browse… tests)

**Interfaces:**
- Consumes: Task 3 (`Model.recentCWDs`, `Model.recentCandidates`).

- [ ] **Step 1: Seed recent-pick** in `enterSetupOrSplit`, in the `if p.Command.PromptsCWD` block, AFTER the git-discover block and BEFORE `if len(m.repoCandidates) > 0 { ... } else { m.initSetupBrowser() }`. Replace that if/else with:

```go
		switch {
		case len(m.repoCandidates) > 0:
			// Pre-select the first git candidate so Enter-through submits it.
			m.cwdBrowseDir = m.repoCandidates[0]
			m.cwdBrowseCursor = 0
		case len(m.recentCWDs) > 0:
			m.recentCandidates = existingDirs(m.recentCWDs)
			if len(m.recentCandidates) > 0 {
				m.cwdBrowseDir = m.recentCandidates[0]
				m.cwdBrowseCursor = 0
			} else {
				m.initSetupBrowser()
			}
		default:
			m.initSetupBrowser()
		}
```

Add helper `existingDirs` in `dialog.go`:

```go
// existingDirs filters paths down to those that still resolve to a
// directory, preserving order. Keeps stale (deleted) entries out of the
// recent-locations pick list.
func existingDirs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}
```

Also add `m.recentCandidates = nil` to the state-reset block at the top of `enterSetupOrSplit` (next to `m.repoCandidates = nil`).

- [ ] **Step 2: Generalize render** in `renderCreatePaneSetupDialog`. Replace `if len(m.repoCandidates) > 0 {` … pick-list branch so it uses the active pick list:

```go
		pick := m.repoCandidates
		if len(pick) == 0 {
			pick = m.recentCandidates
		}
		if len(pick) > 0 {
			setupPickMaxWidth := m.setupDialogWidth() - 6
			rows := len(pick) + 1 // +1 for Browse…
			for i := 0; i < rows; i++ {
				var displayName string
				if i < len(pick) {
					displayName = leftTruncPath(pick[i], setupPickMaxWidth)
				} else {
					displayName = "Browse…"
				}
				if focused && i == m.cwdBrowseCursor {
					b.WriteString("  > " + dialogSelected.Render(displayName) + "\n")
				} else {
					b.WriteString("    " + dialogNormal.Render(displayName) + "\n")
				}
			}
			b.WriteString(dialogSubtle.Render("    ↑↓ navigate  Enter open here  Browse… for another folder") + "\n")
		} else {
```

(The `else {` continues into the existing directory-listing branch unchanged.)

- [ ] **Step 3: Generalize key routing.** In `handleSetupCWDKey`, change the guard:

```go
	if len(m.repoCandidates) > 0 || len(m.recentCandidates) > 0 {
		return m.handleSetupPickKey(p, key)
	}
```

Rename `handleSetupRepoKey` → `handleSetupPickKey` and make it operate on the active pick list:

```go
func (m Model) handleSetupPickKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	pick := m.repoCandidates
	if len(pick) == 0 {
		pick = m.recentCandidates
	}
	rows := len(pick) + 1 // +1 for Browse…

	syncSelection := func() {
		if m.cwdBrowseCursor < len(pick) {
			m.cwdBrowseDir = pick[m.cwdBrowseCursor]
		}
	}

	switch key {
	case "up", "k":
		if m.cwdBrowseCursor > 0 {
			m.cwdBrowseCursor--
			syncSelection()
		}
		return m, nil
	case "down", "j":
		if m.cwdBrowseCursor < rows-1 {
			m.cwdBrowseCursor++
			syncSelection()
		}
		return m, nil
	case "enter":
		if m.cwdBrowseCursor == len(pick) {
			// Browse… — drop pick mode, fall back to the directory browser.
			m.repoCandidates = nil
			m.recentCandidates = nil
			m.cwdBrowseDir = ""
			m.cwdBrowseCursor = 0
			m.initSetupBrowser()
			return m, nil
		}
		m.cwdBrowseDir = pick[m.cwdBrowseCursor]
		return m.submitSetupDialog(p)
	}
	return m, nil
}
```

- [ ] **Step 4: Write behavior tests** in `internal/tui/setup_dialog_test.go`

```go
func TestEnterSetup_RecentPickWhenNoRepos(t *testing.T) {
	dir := t.TempDir()
	m := newSetupTestModel(t) // helper: Model with claude-code selected + registry loaded
	m.repoCandidates = nil
	m.recentCWDs = []string{dir, filepath.Join(dir, "gone")}
	p := m.pluginRegistry.Get("claude-code")
	m.enterSetupOrSplit(p)
	// Only the existing dir survives the os.Stat filter.
	if !reflect.DeepEqual(m.recentCandidates, []string{filepath.Clean(dir)}) {
		t.Errorf("recentCandidates = %v, want [%q]", m.recentCandidates, dir)
	}
}

func TestSetupPickKey_BrowseEscapesToDirBrowser(t *testing.T) {
	dir := t.TempDir()
	m := newSetupTestModel(t)
	m.recentCandidates = []string{dir}
	m.cwdBrowseCursor = 1 // Browse… row (len(pick))
	p := m.pluginRegistry.Get("claude-code")
	m.handleSetupPickKey(p, "enter")
	if m.recentCandidates != nil {
		t.Errorf("recentCandidates not cleared after Browse…: %v", m.recentCandidates)
	}
}
```

(Reuse or add `newSetupTestModel` matching the construction already used by nearby tests in `setup_dialog_test.go`; if `enterSetupOrSplit`/`handleSetupPickKey` have value receivers, capture and assign the returned Model or switch the test to use the returned model as the existing tests do.)

- [ ] **Step 5: Run — expect PASS**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 6: Vet**

Run: `./scripts/dev.sh vet`
Expected: clean.

---

### Task 5: Docs + changelog + CLAUDE.md

**Files:**
- Modify: `docs/features.md`, `docs/configuration.md`, `docs/plugin-reference.md` (only if it quotes old labels), `CHANGELOG.md`, `.claude/CLAUDE.md`.

- [ ] **Step 1: CHANGELOG.md** — add under a new Unreleased/Added section:
  - `Recent working directories: the pane setup dialog now offers the last 5 used folders as a quick pick (persisted to recent-cwds.json).`
  - `Shorter Claude Code permission/Chrome toggle labels so they no longer wrap on narrow terminals.`

- [ ] **Step 2: docs/configuration.md** — add `recent-cwds.json` to the `~/.quil/` file map with a one-line description.

- [ ] **Step 3: docs/features.md** — note recent-locations quick pick under the pane-creation section.

- [ ] **Step 4: .claude/CLAUDE.md** — extend the pane-setup-dialog bullet: recent CWDs pick-list reuses the git repo pick-list UI; `recentCWDs`/`recentCandidates`; `recent-cwds.json` TUI-owned; `pushRecentCWD`.

- [ ] **Step 5: Commit** the whole feature (single meaningful commit).

```bash
git add internal/ docs/features.md docs/configuration.md CHANGELOG.md .claude/CLAUDE.md
git commit -m "feat(tui): recent locations quick-pick + fit setup toggle labels"
```

---

## Self-Review

- **Spec coverage:** Part 1 → Task 2. Part 2 storage → Task 1; model wiring → Task 3; quick-pick UI → Task 4; docs → Task 5. ✓
- **Placeholders:** none — all code shown. Test-helper adaptations are flagged explicitly because the exact constructor is codebase-specific and must be matched to `setup_dialog_test.go`.
- **Type consistency:** `pushRecentCWD`, `recentCWDMax`, `recentCandidates`, `handleSetupPickKey`, `existingDirs`, `RecentCWDsPath` used consistently across tasks. ✓
