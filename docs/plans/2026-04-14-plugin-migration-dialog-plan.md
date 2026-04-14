# Plugin Schema Migration Dialog — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a user updates Quil and embedded plugin defaults have a higher `schema_version` than the user's on-disk file, show a blocking side-by-side merge dialog that lets them reconcile their config before proceeding.

**Architecture:** `EnsureDefaultPlugins` returns stale plugin data instead of overwriting. The TUI receives this list, opens a full-screen split-view dialog with two `TextEditor` instances (editable left, read-only right). The dialog blocks until every stale plugin is resolved (saved or accepted as default). Dialog uses the existing TOML editor full-screen pattern.

**Tech Stack:** Go, Bubble Tea v2, Lipgloss v2, existing `TextEditor` with TOML highlighting

**Build/Test:** `./scripts/dev.sh test` (Docker-based, no local Go), `./scripts/dev.sh vet`, `./scripts/dev.sh build`

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/plugin/plugin.go` | Add `StalePlugin` struct |
| `internal/plugin/defaults.go` | Change `EnsureDefaultPlugins` return type to include stale list |
| `internal/plugin/defaults_test.go` | New: test stale detection + return |
| `internal/plugin/plugin_test.go` | Update existing tests for new return type |
| `internal/tui/model.go` | Add `dialogPluginMigration` iota, model fields, `NewModel` signature change |
| `internal/tui/migration.go` | New: render + key handler for migration dialog |
| `cmd/quil/main.go` | Call `EnsureDefaultPlugins`, pass stale list to model |
| `internal/daemon/daemon.go` | Update two call sites for new return type (ignore stale list) |
| `internal/tui/dialog.go` | Update one call site for new return type |

---

### Task 1: Add `StalePlugin` struct and change `EnsureDefaultPlugins` return type

**Files:**
- Modify: `internal/plugin/plugin.go`
- Modify: `internal/plugin/defaults.go`

- [ ] **Step 1: Add StalePlugin type to plugin.go**

Add after the `PanePlugin` struct (line ~20):

```go
// StalePlugin holds data for a plugin whose on-disk schema_version is lower
// than the embedded default. The TUI uses this to show a migration dialog.
type StalePlugin struct {
	Name        string // plugin name (e.g., "claude-code")
	FilePath    string // absolute path to the user's TOML file
	UserData    []byte // current content of the user's file
	DefaultData []byte // embedded default content (newer schema)
}
```

- [ ] **Step 2: Change EnsureDefaultPlugins to return stale list**

In `defaults.go`, change the signature and logic. Instead of overwriting stale files, collect them:

```go
func EnsureDefaultPlugins(dir string) ([]StalePlugin, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	entries, err := defaultPlugins.ReadDir("defaults")
	if err != nil {
		return nil, err
	}

	var stale []StalePlugin

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		embeddedData, err := defaultPlugins.ReadFile("defaults/" + e.Name())
		if err != nil {
			log.Printf("warning: read embedded plugin %s: %v", e.Name(), err)
			continue
		}

		dest := filepath.Join(dir, e.Name())
		if _, statErr := os.Stat(dest); statErr == nil {
			// File exists — check if it needs a schema upgrade.
			if needsSchemaUpgrade(dest, embeddedData) {
				userData, readErr := os.ReadFile(dest)
				if readErr != nil {
					log.Printf("warning: read user plugin %s: %v", dest, readErr)
					continue
				}
				// Parse plugin name from embedded data for display.
				name := parsePluginName(embeddedData)
				if name == "" {
					name = strings.TrimSuffix(e.Name(), ".toml")
				}
				stale = append(stale, StalePlugin{
					Name:        name,
					FilePath:    dest,
					UserData:    userData,
					DefaultData: embeddedData,
				})
			}
			continue
		} else if !os.IsNotExist(statErr) {
			log.Printf("warning: stat %s: %v", dest, statErr)
			continue
		}

		if err := os.WriteFile(dest, embeddedData, 0600); err != nil {
			log.Printf("warning: write default plugin %s: %v", dest, err)
			continue
		}
		log.Printf("created default plugin: %s", dest)
	}
	return stale, nil
}
```

- [ ] **Step 3: Add parsePluginName helper**

Add to `defaults.go` after `parseSchemaVersion`:

```go
// parsePluginName extracts [plugin].name from raw TOML bytes.
func parsePluginName(data []byte) string {
	var partial struct {
		Plugin struct {
			Name string `toml:"name"`
		} `toml:"plugin"`
	}
	if err := toml.Unmarshal(data, &partial); err != nil {
		return ""
	}
	return partial.Plugin.Name
}
```

- [ ] **Step 4: Run vet**

Run: `./scripts/dev.sh vet`
Expected: Compilation errors from callers of `EnsureDefaultPlugins` (return type changed). That's expected — we fix callers in Task 2.

---

### Task 2: Update all callers of `EnsureDefaultPlugins`

**Files:**
- Modify: `internal/daemon/daemon.go` (lines ~83, ~853)
- Modify: `internal/tui/dialog.go` (line ~1364)

- [ ] **Step 1: Update daemon.go call site at line ~83**

Change:
```go
if err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
```
To:
```go
if _, err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
```

- [ ] **Step 2: Update daemon.go second call site at line ~853**

Same change — discard the stale list with `_`:
```go
if _, err := plugin.EnsureDefaultPlugins(config.PluginsDir()); err != nil {
```

- [ ] **Step 3: Update dialog.go call site at line ~1364**

Change:
```go
plugin.EnsureDefaultPlugins(config.PluginsDir())
```
To:
```go
plugin.EnsureDefaultPlugins(config.PluginsDir()) // stale list ignored in plugin restore
```

Note: this call site is inside the "restore defaults" action in the Plugins dialog. It discards both return values (stale list + error), same as before. The `//` comment clarifies intent.

- [ ] **Step 4: Run vet to verify all callers compile**

Run: `./scripts/dev.sh vet`
Expected: Clean (no errors)

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/plugin.go internal/plugin/defaults.go internal/daemon/daemon.go internal/tui/dialog.go
git commit -m "refactor(plugin): EnsureDefaultPlugins returns stale plugin list

Instead of silently overwriting stale plugin TOMLs, collect them
as StalePlugin structs for the TUI to present in a migration dialog.
Existing callers (daemon, dialog restore) ignore the stale list."
```

---

### Task 3: Update existing tests for new return type

**Files:**
- Modify: `internal/plugin/plugin_test.go`

- [ ] **Step 1: Update all test call sites**

Every call to `EnsureDefaultPlugins(dir)` in `plugin_test.go` needs to handle the new return type. There are ~6 call sites. For each, change from:

```go
EnsureDefaultPlugins(dir)
// or
if err := EnsureDefaultPlugins(dir); err != nil {
```

To:

```go
EnsureDefaultPlugins(dir) // returns ([]StalePlugin, error) — tests ignore stale list
// or
if _, err := EnsureDefaultPlugins(dir); err != nil {
```

Specific locations (search for `EnsureDefaultPlugins` in the file):
- Line ~45: `EnsureDefaultPlugins(dir)` → add `_, _ =` prefix or leave as-is (Go allows discarding multi-return)
- Line ~64: `if err := EnsureDefaultPlugins(dir)` → `if _, err := EnsureDefaultPlugins(dir)`
- Line ~157: same pattern
- Line ~269: same pattern
- Line ~325: `EnsureDefaultPlugins(dir)` → no change needed (both returns discarded)
- Line ~332: same

- [ ] **Step 2: Run tests**

Run: `./scripts/dev.sh test`
Expected: All tests pass

- [ ] **Step 3: Commit**

```bash
git add internal/plugin/plugin_test.go
git commit -m "test(plugin): update tests for new EnsureDefaultPlugins return type"
```

---

### Task 4: Add stale plugin detection test

**Files:**
- Create: `internal/plugin/defaults_test.go`

- [ ] **Step 1: Write test for stale detection**

```go
package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDefaultPlugins_DetectsStalePlugins(t *testing.T) {
	dir := t.TempDir()

	// First run: creates fresh files (no stale)
	stale, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("first run: expected 0 stale, got %d", len(stale))
	}

	// Downgrade claude-code by removing schema_version from user file
	ccPath := filepath.Join(dir, "claude-code.toml")
	old := []byte("[plugin]\nname = \"claude-code\"\n\n[command]\ncmd = \"claude\"\n")
	if err := os.WriteFile(ccPath, old, 0600); err != nil {
		t.Fatal(err)
	}

	// Second run: should detect claude-code as stale
	stale, err = EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale plugin, got %d", len(stale))
	}
	if stale[0].Name != "claude-code" {
		t.Errorf("expected stale plugin 'claude-code', got %q", stale[0].Name)
	}
	if len(stale[0].UserData) == 0 {
		t.Error("expected non-empty UserData")
	}
	if len(stale[0].DefaultData) == 0 {
		t.Error("expected non-empty DefaultData")
	}

	// User file should NOT have been overwritten (migration dialog handles that)
	current, _ := os.ReadFile(ccPath)
	if string(current) != string(old) {
		t.Error("stale file was modified — should be left for migration dialog")
	}
}

func TestEnsureDefaultPlugins_CurrentVersionNotStale(t *testing.T) {
	dir := t.TempDir()

	// First run creates files with current schema
	_, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Second run should find nothing stale (files match embedded version)
	stale, err := EnsureDefaultPlugins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Errorf("expected 0 stale plugins, got %d", len(stale))
	}
}
```

- [ ] **Step 2: Run test**

Run: `./scripts/dev.sh test`
Expected: All tests pass including the new ones

- [ ] **Step 3: Commit**

```bash
git add internal/plugin/defaults_test.go
git commit -m "test(plugin): add stale plugin detection tests"
```

---

### Task 5: Add migration dialog to TUI model

**Files:**
- Modify: `internal/tui/model.go`

- [ ] **Step 1: Add dialogPluginMigration to iota**

After `dialogDisclaimer` (line ~142):

```go
	dialogDisclaimer
	dialogPluginMigration
)
```

- [ ] **Step 2: Add model fields**

Add to `Model` struct after the `notesAnchorCol` field (line ~214):

```go
	// Plugin migration dialog state
	migrationPlugins     []plugin.StalePlugin // stale plugins needing migration
	migrationIdx         int                  // active plugin tab index
	migrationLeft        *TextEditor          // user config (editable)
	migrationRight       *TextEditor          // new default (read-only)
	migrationRightFocus  bool                 // true when right pane has keyboard focus
	migrationError       string               // validation error message
```

- [ ] **Step 3: Update NewModel signature and initialization**

Change `NewModel` to accept stale plugins:

```go
func NewModel(client *ipc.Client, cfg config.Config, version string, registry *plugin.Registry, stalePlugins []plugin.StalePlugin) Model {
```

Add to the initialization (after notifications, before disclaimer check):

```go
	if len(stalePlugins) > 0 {
		m.migrationPlugins = stalePlugins
		// Migration dialog takes priority over disclaimer — show it after
		// workspace loads (set in the WorkspaceStateMsg handler if stale
		// plugins are pending).
	}
```

- [ ] **Step 4: Add migration dialog trigger in WorkspaceStateMsg handler**

In the `WorkspaceStateMsg` handler (after `applyWorkspaceState` and before the return), add a check that opens the migration dialog once workspace is loaded:

Find the `case WorkspaceStateMsg:` handler and add before the return statement:

```go
	// If stale plugins need migration, show the dialog now that workspace
	// is loaded and the user can see their panes behind the dialog.
	if len(m.migrationPlugins) > 0 && m.migrationLeft == nil {
		m.openMigrationDialog()
	}
```

- [ ] **Step 5: Run vet**

Run: `./scripts/dev.sh vet`
Expected: Errors from `NewModel` callers (signature changed) and missing `openMigrationDialog`. We fix callers in Task 6, dialog in Task 7.

---

### Task 6: Update NewModel callers

**Files:**
- Modify: `cmd/quil/main.go`
- Modify: `internal/tui/setup_dialog_test.go` (if it calls NewModel)

- [ ] **Step 1: Update cmd/quil/main.go**

Add stale plugin detection before creating the model. Change the plugin loading block:

```go
	// Initialize plugin registry for the TUI (detection runs in the TUI process
	// so the dialog knows which plugins are available)
	stalePlugins, _ := plugin.EnsureDefaultPlugins(config.PluginsDir())
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(config.PluginsDir()); err != nil {
		log.Printf("warning: load plugins: %v", err)
	}
	reg.DetectAvailability()
	if len(stalePlugins) > 0 {
		log.Printf("detected %d stale plugin(s) needing migration", len(stalePlugins))
	}
```

And update the `NewModel` call:

```go
	model := tui.NewModel(client, cfg, version, reg, stalePlugins)
```

- [ ] **Step 2: Add plugin import if needed**

Ensure `cmd/quil/main.go` imports `"github.com/artyomsv/quil/internal/plugin"`.

- [ ] **Step 3: Update any test files that call NewModel**

Search for `NewModel(` in test files. If `internal/tui/setup_dialog_test.go` calls it, add a `nil` argument:

```go
NewModel(client, cfg, version, reg, nil)
```

- [ ] **Step 4: Run vet**

Run: `./scripts/dev.sh vet`
Expected: Errors only from missing `openMigrationDialog` (implemented in Task 7)

---

### Task 7: Implement migration dialog (render + keys)

**Files:**
- Create: `internal/tui/migration.go`

- [ ] **Step 1: Create migration.go with openMigrationDialog**

```go
package tui

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// openMigrationDialog initializes the editors for the current migration plugin.
func (m *Model) openMigrationDialog() {
	sp := m.migrationPlugins[m.migrationIdx]
	halfW := m.width/2 - 1
	editorH := m.height - 4 // title + tab bar + status bar + padding

	m.migrationLeft = NewTextEditor(string(sp.UserData), sp.FilePath, halfW, editorH)
	m.migrationRight = NewTextEditor(string(sp.DefaultData), "", halfW, editorH)
	m.migrationRight.ReadOnly = true
	m.migrationRightFocus = false
	m.migrationError = ""
	m.dialog = dialogPluginMigration
}
```

- [ ] **Step 2: Add key handler**

```go
func (m Model) handleMigrationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch {
	case key == "ctrl+q":
		return m, tea.Quit

	case key == "esc":
		return m, nil // blocked — must resolve

	case key == "a":
		// Accept default: replace left editor content with the default
		if m.migrationRightFocus {
			break // 'a' is a normal character when right pane focused (read-only, ignored anyway)
		}
		sp := m.migrationPlugins[m.migrationIdx]
		halfW := m.width/2 - 1
		editorH := m.height - 4
		m.migrationLeft = NewTextEditor(string(sp.DefaultData), sp.FilePath, halfW, editorH)
		m.migrationError = ""
		return m, nil

	case key == "tab":
		m.migrationRightFocus = !m.migrationRightFocus
		return m, nil

	case key == "ctrl+left":
		if m.migrationIdx > 0 {
			m.migrationIdx--
			m.openMigrationDialog()
		}
		return m, nil

	case key == "ctrl+right":
		if m.migrationIdx < len(m.migrationPlugins)-1 {
			m.migrationIdx++
			m.openMigrationDialog()
		}
		return m, nil

	case key == "enter" && !m.migrationRightFocus:
		// Save: validate TOML, check schema_version, write to disk
		content := strings.Join(m.migrationLeft.Lines, "\n")

		// Validate TOML syntax
		var parsed map[string]any
		if err := toml.Unmarshal([]byte(content), &parsed); err != nil {
			m.migrationError = fmt.Sprintf("Invalid TOML: %v", err)
			return m, nil
		}

		// Check schema_version meets minimum
		sp := m.migrationPlugins[m.migrationIdx]
		requiredVer := plugin.ParseSchemaVersion(sp.DefaultData)
		savedVer := plugin.ParseSchemaVersion([]byte(content))
		if savedVer < requiredVer {
			m.migrationError = fmt.Sprintf("schema_version must be >= %d (got %d)", requiredVer, savedVer)
			return m, nil
		}

		// Write to disk
		if err := os.WriteFile(sp.FilePath, []byte(content), 0600); err != nil {
			m.migrationError = fmt.Sprintf("Save failed: %v", err)
			return m, nil
		}
		log.Printf("migration: saved %s (schema_version %d)", sp.Name, savedVer)

		// Advance to next stale plugin or close dialog
		m.migrationIdx++
		if m.migrationIdx >= len(m.migrationPlugins) {
			// All resolved — reload registry and close
			m.migrationLeft = nil
			m.migrationRight = nil
			m.migrationPlugins = nil
			m.migrationIdx = 0
			m.dialog = dialogNone
			if err := m.pluginRegistry.LoadFromDir(config.PluginsDir()); err != nil {
				log.Printf("migration: reload plugins: %v", err)
			}
			m.pluginRegistry.DetectAvailability()
			return m, tea.ClearScreen
		}
		m.openMigrationDialog()
		return m, nil
	}

	// Route remaining keys to the focused editor
	if m.migrationRightFocus {
		if m.migrationRight != nil {
			m.migrationRight.HandleKey(key)
		}
	} else {
		if m.migrationLeft != nil {
			_, _, cmd := m.migrationLeft.HandleKey(key)
			if cmd != nil {
				return m, cmd
			}
		}
	}
	return m, nil
}
```

- [ ] **Step 3: Add render function**

```go
func (m Model) renderMigrationFullScreen() string {
	if m.migrationLeft == nil || m.migrationRight == nil {
		return ""
	}

	halfW := m.width/2 - 1
	editorH := m.height - 4
	m.migrationLeft.ViewWidth = halfW
	m.migrationLeft.ViewHeight = editorH
	m.migrationRight.ViewWidth = halfW
	m.migrationRight.ViewHeight = editorH

	var b strings.Builder

	// Title bar
	title := "Plugin Migration"
	for len(title) < m.width {
		title += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[38;5;230m " + title + "\x1b[0m\n")

	// Tab bar (if multiple plugins)
	if len(m.migrationPlugins) > 1 {
		var tabs strings.Builder
		for i, sp := range m.migrationPlugins {
			label := sp.Name
			if i == m.migrationIdx {
				tabs.WriteString("\x1b[1m[" + label + "]\x1b[0m  ")
			} else {
				tabs.WriteString(" " + label + "   ")
			}
		}
		tabLine := tabs.String()
		for len(tabLine) < m.width {
			tabLine += " "
		}
		b.WriteString(tabLine + "\n")
	} else {
		b.WriteString("\n")
	}

	// Column headers
	leftHeader := " Your config (editable)"
	rightHeader := " New default (read-only)"
	for len(leftHeader) < halfW {
		leftHeader += " "
	}
	for len(rightHeader) < halfW {
		rightHeader += " "
	}
	leftColor := "\x1b[48;5;237m\x1b[38;5;117m"
	rightColor := "\x1b[48;5;237m\x1b[38;5;241m"
	if m.migrationRightFocus {
		leftColor = "\x1b[48;5;237m\x1b[38;5;241m"
		rightColor = "\x1b[48;5;237m\x1b[38;5;117m"
	}
	b.WriteString(leftColor + leftHeader + "\x1b[0m\x1b[48;5;238m \x1b[0m" + rightColor + rightHeader + "\x1b[0m\n")

	// Editor panes side by side
	leftLines := strings.Split(m.migrationLeft.Render(), "\n")
	rightLines := strings.Split(m.migrationRight.Render(), "\n")

	for i := 0; i < editorH; i++ {
		left := ""
		if i < len(leftLines) {
			left = leftLines[i]
		}
		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
		}
		b.WriteString(left + "\x1b[48;5;238m \x1b[0m" + right + "\n")
	}

	// Status bar
	var status string
	if m.migrationError != "" {
		status = fmt.Sprintf(" \x1b[31m%s\x1b[0m\x1b[48;5;236m\x1b[38;5;250m", m.migrationError)
	} else {
		status = " Enter save  A accept default  Tab switch focus"
		if len(m.migrationPlugins) > 1 {
			status += "  Ctrl+Left/Right switch plugin"
		}
		status += "  Ctrl+Q quit"
	}
	for len(status) < m.width {
		status += " "
	}
	b.WriteString("\x1b[48;5;236m\x1b[38;5;250m" + status + "\x1b[0m")

	return b.String()
}
```

- [ ] **Step 4: Run vet**

Run: `./scripts/dev.sh vet`
Expected: Errors from `ParseSchemaVersion` not being exported. Fix in Task 8.

---

### Task 8: Export ParseSchemaVersion and wire dialog into model

**Files:**
- Modify: `internal/plugin/defaults.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/dialog.go`

- [ ] **Step 1: Export parseSchemaVersion in defaults.go**

Rename `parseSchemaVersion` → `ParseSchemaVersion` (capitalize):

Change the function definition:
```go
func ParseSchemaVersion(data []byte) int {
```

And update the call in `needsSchemaUpgrade`:
```go
func needsSchemaUpgrade(userPath string, embeddedData []byte) bool {
	embeddedVer := ParseSchemaVersion(embeddedData)
	// ...
	userVer := ParseSchemaVersion(userData)
	return userVer < embeddedVer
}
```

- [ ] **Step 2: Wire render into View()**

In `model.go`, update the View function. Find the block at line ~968:

```go
} else if (m.dialog == dialogTOMLEditor || m.dialog == dialogLogViewer) && m.tomlEditor != nil {
```

Change to:

```go
} else if m.dialog == dialogPluginMigration && m.migrationLeft != nil {
	content = m.renderMigrationFullScreen()
} else if (m.dialog == dialogTOMLEditor || m.dialog == dialogLogViewer) && m.tomlEditor != nil {
```

- [ ] **Step 3: Wire key handler into handleDialogKey**

In `dialog.go`, in `handleDialogKey()`, add the case before `dialogDisclaimer`:

```go
case dialogPluginMigration:
	return m.handleMigrationKey(msg)
```

- [ ] **Step 4: Run vet and tests**

Run: `./scripts/dev.sh vet && ./scripts/dev.sh test`
Expected: Clean compilation, all tests pass

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/defaults.go internal/tui/migration.go internal/tui/model.go internal/tui/dialog.go
git commit -m "feat(tui): add plugin schema migration dialog

Full-screen split view: editable user config on the left,
read-only new default on the right. Blocks startup until all
stale plugins are resolved via merge or accept-default.

Reuses existing TextEditor with TOML highlighting."
```

---

### Task 9: Update cmd/quil/main.go and build

**Files:**
- Modify: `cmd/quil/main.go`

- [ ] **Step 1: Add plugin import and stale detection**

Add `"github.com/artyomsv/quil/internal/plugin"` to imports if not present.

Replace the plugin loading block (line ~260-266):

```go
	// Detect stale plugins before loading the registry. Stale plugins are
	// shown in a migration dialog — the TUI blocks until they're resolved.
	stalePlugins, _ := plugin.EnsureDefaultPlugins(config.PluginsDir())
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(config.PluginsDir()); err != nil {
		log.Printf("warning: load plugins: %v", err)
	}
	reg.DetectAvailability()
	if len(stalePlugins) > 0 {
		log.Printf("detected %d stale plugin(s) needing migration", len(stalePlugins))
	}
```

Update NewModel call:

```go
	model := tui.NewModel(client, cfg, version, reg, stalePlugins)
```

- [ ] **Step 2: Build all variants**

Run: `./scripts/dev.sh build`
Expected: All 6 binaries built

- [ ] **Step 3: Commit**

```bash
git add cmd/quil/main.go
git commit -m "feat: wire stale plugin detection into TUI startup"
```

---

### Task 10: End-to-end manual test

- [ ] **Step 1: Simulate stale plugin**

Downgrade the production claude-code TOML to trigger migration:

```bash
# Back up current file
cp ~/.quil/plugins/claude-code.toml ~/.quil/plugins/claude-code.toml.bak

# Write old-version file (no schema_version, no prompts_cwd)
cat > ~/.quil/plugins/claude-code.toml << 'EOF'
[plugin]
name = "claude-code"
display_name = "Claude Code"
category = "ai"
description = "AI coding assistant"

[command]
cmd = "claude"
detect = "claude --version"

[persistence]
strategy = "preassign_id"
ghost_buffer = false
start_args = ["--session-id", "{session_id}"]
resume_args = ["--resume", "{session_id}"]
EOF
```

- [ ] **Step 2: Launch and verify dialog appears**

Run: `./quil-debug.exe` (debug build uses production ~/.quil/)

Expected: After panes load, a full-screen migration dialog appears showing:
- Left: the old TOML (editable)
- Right: the new default with `schema_version = 2`, `prompts_cwd = true`, etc.

- [ ] **Step 3: Test key interactions**

- Press Tab → focus switches between panes
- Press A → left pane replaced with default content
- Press Esc → nothing happens (blocked)
- Press Enter → saves, dialog closes, TUI becomes interactive

- [ ] **Step 4: Verify saved file**

```bash
grep "schema_version" ~/.quil/plugins/claude-code.toml
grep "prompts_cwd" ~/.quil/plugins/claude-code.toml
```

Expected: Both fields present in the saved file.

- [ ] **Step 5: Restore backup if needed**

```bash
mv ~/.quil/plugins/claude-code.toml.bak ~/.quil/plugins/claude-code.toml
```

---

### Task 11: Update CLAUDE.md

**Files:**
- Modify: `.claude/CLAUDE.md`

- [ ] **Step 1: Add migration dialog to Dialog system section**

Find the dialog system description and add `dialogPluginMigration` to the list. Also add a brief description of the migration flow.

- [ ] **Step 2: Add to Key Conventions**

Add under the plugin system section:
- `schema_version` field in plugin TOMLs — bump when TOML structure changes
- `StalePlugin` type returned by `EnsureDefaultPlugins` for TUI migration
- `internal/tui/migration.go` — full-screen split-view merge dialog

- [ ] **Step 3: Commit**

```bash
git add .claude/CLAUDE.md
git commit -m "docs: document plugin schema migration dialog"
```
