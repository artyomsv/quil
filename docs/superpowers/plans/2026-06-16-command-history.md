# Per-Pane Input History (Claude panes) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture each user prompt submitted to a Claude Code pane, list the prompts as 3-line previews in a modal, and open any one full-text in a read-only viewer the user can copy from.

**Architecture:** The Claude hook already receives the full prompt (`UserPromptSubmit`). It appends the full text to a per-pane JSONL store under `$QUIL_HOME/history/<paneID>.jsonl`, gated by a new `QUIL_RECORD_HISTORY=1` PTY env set only for opted-in plugins. The daemon reads/serves the store over two IPC request/response pairs (list previews; fetch one entry). A new TUI modal lists previews and opens the chosen entry in the existing read-only `TextEditor` overlay. OpenCode is an explicit follow-up (producer-only).

**Tech Stack:** Go 1.25, BurntSushi/toml, length-prefixed JSON IPC, Bubble Tea v2 / Lipgloss v2. Build/test via `./scripts/dev.sh` (Docker — host has no Go).

**Spec:** `docs/superpowers/specs/2026-06-15-command-history-design.md`

**Build/test commands (host has no Go/make — always Docker):**
- Test one package: `./scripts/dev.sh test 2>&1 | tail -40` (the script runs `go test ./...`; scope mentally by reading failures)
- Vet: `./scripts/dev.sh vet`
- Build: `./scripts/dev.sh build`

> NOTE: `./scripts/dev.sh test` runs the whole suite. Where a step says "run the package test", run `./scripts/dev.sh test` and confirm the named test passes in the output.

---

## File Structure

**Create:**
- `internal/panehistory/history.go` — store: `Entry`, `Path`/`Dir`, `Append`, `Read`, `Preview`, `Compact`, caps. Pure functions, stdlib only (importable by the hot-path hook).
- `internal/panehistory/history_test.go` — unit tests for the above.
- `internal/tui/history.go` — TUI dialog: state, open, request commands, response messages, render. Mirrors `internal/tui/memory.go`.

**Modify:**
- `internal/claudehook/runhook.go` — `HookEnv.RecordHistory`; append full prompt on `UserPromptSubmit`.
- `internal/claudehook/runhook_test.go` (or existing hook test file) — history append tests.
- `cmd/quild/hook.go` — read `QUIL_RECORD_HISTORY` into `HookEnv`.
- `internal/ipc/protocol.go` — 4 message types + 4 payload structs.
- `internal/ipc/protocol_test.go` — round-trip test for new payloads.
- `internal/daemon/daemon.go` — set `QUIL_RECORD_HISTORY=1` at spawn; two handlers; register in `handleMessage`; unlink history in `cleanupPaneArtifacts`.
- `internal/daemon/daemon_integration_test.go` — handler integration tests.
- `internal/plugin/plugin.go` — `CommandConfig.RecordHistory bool`.
- `internal/plugin/registry.go` — toml field + mapping.
- `internal/plugin/defaults/claude-code.toml` — `record_history = true`, bump `schema_version`.
- `internal/plugin/defaults_test.go` — assert flag wired.
- `internal/config/config.go` — `Keybindings.CommandHistory` field + default.
- `internal/tui/dialog.go` — `dialogCommandHistory` screen const; `openReadonlyText`; render + key handling.
- `internal/tui/model.go` — keybinding to open; dispatch new responses.
- Docs: `docs/keybindings.md`, `docs/plugin-reference.md`, `.claude/CLAUDE.md`.

---

## Task 1: panehistory store — Entry, paths, Append

**Files:**
- Create: `internal/panehistory/history.go`
- Test: `internal/panehistory/history_test.go`

- [ ] **Step 1: Write the failing test**

```go
package panehistory

import (
	"strings"
	"testing"
)

func TestAppend_WritesEntry_ReadBack(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, "pane-abc", Entry{TsMs: 100, SessionID: "s1", Text: "hello"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Read(dir, "pane-abc")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Text != "hello" || got[0].TsMs != 100 || got[0].V != 1 {
		t.Fatalf("unexpected entries: %+v", got)
	}
}

func TestAppend_SkipsEmptyAndWhitespace(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"", "   ", "\n\t "} {
		if err := Append(dir, "pane-x", Entry{TsMs: 1, Text: s}); err != nil {
			t.Fatalf("Append(%q): %v", s, err)
		}
	}
	got, _ := Read(dir, "pane-x")
	if len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got))
	}
}

func TestAppend_CapsOversizeText(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", MaxEntryBytes+5000)
	if err := Append(dir, "pane-y", Entry{TsMs: 1, Text: big}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _ := Read(dir, "pane-y")
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if len(got[0].Text) > MaxEntryBytes {
		t.Fatalf("text not capped: %d bytes", len(got[0].Text))
	}
	if !strings.HasSuffix(got[0].Text, "…[truncated]") {
		t.Fatalf("missing truncation marker")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — `internal/panehistory` does not compile (undefined `Append`, `Read`, `Entry`, `MaxEntryBytes`).

- [ ] **Step 3: Write minimal implementation**

Create `internal/panehistory/history.go`:

```go
// Package panehistory stores and serves per-pane user-input history. One
// JSONL file per pane lives under <quilDir>/history/<paneID>.jsonl. The Claude
// hook subprocess appends entries; the daemon reads, previews, and compacts.
package panehistory

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxEntryBytes caps a single entry's text — generous enough for a pasted
	// stack trace, bounded so one paste can't bloat the store.
	MaxEntryBytes = 64 * 1024
	// MaxEntries is the ring cap Compact enforces.
	MaxEntries = 200
	// schemaVersion is stamped into every entry's V field.
	schemaVersion = 1
	// truncMarker is appended when Append caps oversize text.
	truncMarker = "…[truncated]"
)

// Entry is one recorded user input, persisted as a single JSONL line.
type Entry struct {
	V         int    `json:"v"`
	TsMs      int64  `json:"ts_ms"`
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text"`
}

// Dir returns the history directory under quilDir.
func Dir(quilDir string) string { return filepath.Join(quilDir, "history") }

// Path returns the per-pane history file path.
func Path(quilDir, paneID string) string { return filepath.Join(Dir(quilDir), paneID+".jsonl") }

// Append writes one entry to the pane's history file. Empty/whitespace text is
// skipped (returns nil without writing). Oversize text is truncated on a rune
// boundary with a trailing marker. V is forced to the current schema version.
// O_APPEND keeps concurrent hook invocations from clobbering each other.
func Append(quilDir, paneID string, e Entry) error {
	if strings.TrimSpace(e.Text) == "" {
		return nil
	}
	e.V = schemaVersion
	e.Text = capText(e.Text, MaxEntryBytes)

	if err := os.MkdirAll(Dir(quilDir), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(Path(quilDir, paneID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// capText truncates s to at most maxBytes on a rune boundary, appending a
// marker when truncated. Always returns valid UTF-8.
func capText(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	budget := maxBytes - len(truncMarker)
	if budget < 0 {
		budget = 0
	}
	cut := 0
	for i := range s {
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut] + truncMarker
}

// Read returns all entries oldest-first. A missing file is not an error
// (returns nil, nil). Malformed lines — including a trailing partial line from
// an in-flight concurrent append — are skipped.
func Read(quilDir, paneID string) ([]Entry, error) {
	f, err := os.Open(Path(quilDir, paneID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), MaxEntryBytes+4096)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — `TestAppend_*` green. (`TestAppend_CapsOversizeText` exercises `Read` too.)

- [ ] **Step 5: Commit**

```bash
git add internal/panehistory/history.go internal/panehistory/history_test.go
git commit -m "feat(history): add panehistory store with Append/Read"
```

---

## Task 2: panehistory.Read — skip trailing partial line

**Files:**
- Modify: `internal/panehistory/history.go` (already handles it; this task adds the regression test)
- Test: `internal/panehistory/history_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRead_SkipsTrailingPartialLine(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, "pane-p", Entry{TsMs: 1, Text: "first"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Simulate an in-flight append: a partial JSON fragment with no newline.
	f, err := os.OpenFile(Path(dir, "pane-p"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"v":1,"ts_ms":2,"text":"partia`); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	got, err := Read(dir, "pane-p")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Text != "first" {
		t.Fatalf("partial line not skipped: %+v", got)
	}
}

func TestRead_MissingFile_NoError(t *testing.T) {
	got, err := Read(t.TempDir(), "pane-none")
	if err != nil || got != nil {
		t.Fatalf("want nil,nil; got %+v, %v", got, err)
	}
}
```

(Requires `import "os"` in the test file — add if not present.)

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS (implementation from Task 1 already skips malformed lines). If it FAILS, the partial-line handling regressed — fix `Read`.

- [ ] **Step 3: (no impl change needed if green)**

If green, skip. If red, ensure `json.Unmarshal` errors `continue` rather than abort.

- [ ] **Step 4: Re-run**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panehistory/history_test.go
git commit -m "test(history): cover trailing partial line and missing file"
```

---

## Task 3: panehistory.Preview

**Files:**
- Modify: `internal/panehistory/history.go`
- Test: `internal/panehistory/history_test.go`

- [ ] **Step 1: Write the failing test**

```go
import "reflect" // add to test imports

func TestPreview_MultilineTruncated(t *testing.T) {
	text := "line one\nline two\nline three\nline four"
	got := Preview(text, 3, 100)
	want := []string{"line one", "line two", "line three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPreview_LongLineWidthCapped(t *testing.T) {
	got := Preview(strings.Repeat("x", 50), 3, 10)
	if len(got) != 1 {
		t.Fatalf("want 1 line, got %d", len(got))
	}
	if !strings.HasSuffix(got[0], "…") || len([]rune(got[0])) > 10 {
		t.Fatalf("line not width-capped: %q (%d runes)", got[0], len([]rune(got[0])))
	}
}

func TestPreview_NormalizesTabsAndCR(t *testing.T) {
	got := Preview("a\tb\r\nc", 3, 100)
	if got[0] != "a    b" || got[1] != "c" {
		t.Fatalf("tabs/CR not normalized: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — undefined `Preview`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/panehistory/history.go`:

```go
// Preview returns up to maxLines logical lines of text, each truncated (rune-
// aware) to maxBytes with a trailing "…". Tabs become four spaces and CRs are
// stripped so the list renders cleanly.
func Preview(text string, maxLines, maxBytes int) []string {
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\t", "    ")
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, truncRunes(ln, maxBytes))
	}
	return out
}

// truncRunes truncates s to at most maxBytes bytes on a rune boundary,
// appending "…" when truncated.
func truncRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const ell = "…"
	budget := maxBytes - len(ell)
	if budget < 0 {
		budget = 0
	}
	cut := 0
	for i := range s {
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut] + ell
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — all `TestPreview_*` green.

- [ ] **Step 5: Commit**

```bash
git add internal/panehistory/history.go internal/panehistory/history_test.go
git commit -m "feat(history): add Preview line builder"
```

---

## Task 4: panehistory.Compact

**Files:**
- Modify: `internal/panehistory/history.go`
- Test: `internal/panehistory/history_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCompact_KeepsLastN(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		if err := Append(dir, "pane-c", Entry{TsMs: int64(i), Text: "msg"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := Compact(dir, "pane-c", 3); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	got, _ := Read(dir, "pane-c")
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	if got[0].TsMs != 7 || got[2].TsMs != 9 {
		t.Fatalf("kept wrong window: %+v", got)
	}
}

func TestCompact_NoOpWhenUnderLimit(t *testing.T) {
	dir := t.TempDir()
	Append(dir, "pane-d", Entry{TsMs: 1, Text: "a"})
	if err := Compact(dir, "pane-d", 5); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	got, _ := Read(dir, "pane-d")
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — undefined `Compact`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/panehistory/history.go`:

```go
// Compact rewrites the pane's history file keeping only the last keepLast
// entries. No-op when at or under the limit. Atomic via temp file + rename.
func Compact(quilDir, paneID string, keepLast int) error {
	entries, err := Read(quilDir, paneID)
	if err != nil {
		return err
	}
	if len(entries) <= keepLast {
		return nil
	}
	entries = entries[len(entries)-keepLast:]

	if err := os.MkdirAll(Dir(quilDir), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Dir(quilDir), paneID+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // best-effort cleanup if rename never happens

	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, Path(quilDir, paneID))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — `TestCompact_*` green.

- [ ] **Step 5: Commit**

```bash
git add internal/panehistory/history.go internal/panehistory/history_test.go
git commit -m "feat(history): add Compact ring trimming"
```

---

## Task 5: Plugin opt-in field `record_history`

**Files:**
- Modify: `internal/plugin/plugin.go:33-56` (CommandConfig)
- Modify: `internal/plugin/registry.go:292-315` (tomlPlugin.Command) and `:405-409` (mapping)
- Modify: `internal/plugin/defaults/claude-code.toml`
- Test: `internal/plugin/defaults_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/plugin/defaults_test.go`:

```go
func TestDefaultClaudeCode_RecordHistoryEnabled(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	reg := NewRegistry(dir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := reg.Get("claude-code")
	if p == nil {
		t.Fatal("claude-code plugin not found")
	}
	if !p.Command.RecordHistory {
		t.Fatal("expected claude-code Command.RecordHistory = true")
	}
}
```

> Verify the helper names against the existing `defaults_test.go` (e.g. `EnsureDefaultPlugins`, `NewRegistry`, `reg.Load()`, `reg.Get`). If the existing tests use a different construction pattern, copy that pattern — do not invent new constructors.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — `p.Command.RecordHistory` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/plugin/plugin.go`, add to `CommandConfig` (after `Discover string`):

```go
	// RecordHistory enables per-pane user-input history capture for this
	// plugin. When true, the daemon sets QUIL_RECORD_HISTORY=1 in the pane's
	// PTY env, and the plugin's hook producer appends submitted prompts to
	// <quilDir>/history/<paneID>.jsonl. Meaningful only for plugins with a
	// hook producer (claude-code; opencode is a follow-up).
	RecordHistory bool
```

In `internal/plugin/registry.go`, add to the `tomlPlugin.Command` struct (after `Discover string \`toml:"discover"\``):

```go
		RecordHistory bool `toml:"record_history"`
```

In the same file, in the mapping that builds `CommandConfig` (alongside `Discover: tp.Command.Discover`):

```go
			RecordHistory:    tp.Command.RecordHistory,
```

In `internal/plugin/defaults/claude-code.toml`:
- Bump `schema_version` by 1 under `[plugin]` (e.g. `schema_version = N+1`).
- Under `[command]`, add:

```toml
record_history = true
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — `TestDefaultClaudeCode_RecordHistoryEnabled` green. Existing `plugin_test.go` schema_version assertions may also need the new number — update any test asserting the old claude-code `schema_version` to the bumped value.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/plugin.go internal/plugin/registry.go internal/plugin/defaults/claude-code.toml internal/plugin/defaults_test.go internal/plugin/plugin_test.go
git commit -m "feat(plugin): add record_history opt-in, enable for claude-code"
```

---

## Task 6: claudehook producer — append full prompt

**Files:**
- Modify: `internal/claudehook/runhook.go:18-22` (HookEnv), `:103-107` (UserPromptSubmit)
- Modify: `cmd/quild/hook.go:22-28`
- Test: existing claudehook test file (find with `grep -rl "func Test" internal/claudehook/`); add cases there.

- [ ] **Step 1: Write the failing test**

Add to the claudehook test file (package `claudehook`):

```go
func TestRunHook_UserPromptSubmit_AppendsHistory(t *testing.T) {
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", QuilDir: dir, Mode: "default", RecordHistory: true}
	in := `{"hook_event_name":"UserPromptSubmit","session_id":"sess-1","prompt":"fix the parser bug"}`
	if err := RunHook(strings.NewReader(in), env, 12345); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got, err := panehistory.Read(dir, env.PaneID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Text != "fix the parser bug" || got[0].TsMs != 12345 {
		t.Fatalf("unexpected history: %+v", got)
	}
}

func TestRunHook_UserPromptSubmit_NoHistoryWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	env := HookEnv{PaneID: "pane-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", QuilDir: dir, Mode: "default", RecordHistory: false}
	in := `{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"hello"}`
	if err := RunHook(strings.NewReader(in), env, 1); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	got, _ := panehistory.Read(dir, env.PaneID)
	if len(got) != 0 {
		t.Fatalf("expected no history when disabled, got %d", len(got))
	}
}
```

Add imports to the test file: `"strings"` and `"github.com/artyomsv/quil/internal/panehistory"`. (Pane IDs above are 32 hex-ish chars to pass `validatePaneID`; if that validator rejects them, copy a valid id literal from the existing claudehook tests.)

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — `HookEnv.RecordHistory` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/claudehook/runhook.go`:
- Add the import `"github.com/artyomsv/quil/internal/panehistory"`.
- Add to `HookEnv` (after `Mode string`):

```go
	RecordHistory bool // QUIL_RECORD_HISTORY=1 — append full prompts to the history store
```

- Replace the `UserPromptSubmit` case body:

```go
	case "UserPromptSubmit":
		if env.RecordHistory {
			if err := panehistory.Append(env.QuilDir, env.PaneID, panehistory.Entry{
				TsMs:      nowMs,
				SessionID: in.SessionID,
				Text:      in.Prompt,
			}); err != nil {
				hookLog(env.QuilDir, env.PaneID, "append history failed: "+err.Error())
			}
		}
		preview := truncate(in.Prompt, 60)
		return spoolEvent(env, nowMs, "UserPromptSubmit", in.SessionID,
			truncate("Working on: "+preview, hookevents.MaxTitleBytes), hookevents.SeverityInfo,
			map[string]string{"prompt_preview": preview})
```

In `cmd/quild/hook.go`, add to the `HookEnv` literal:

```go
		RecordHistory: os.Getenv("QUIL_RECORD_HISTORY") == "1",
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — both new tests green; existing claudehook tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/claudehook/runhook.go cmd/quild/hook.go internal/claudehook/*_test.go
git commit -m "feat(history): append claude prompts to history store from hook"
```

---

## Task 7: Daemon sets `QUIL_RECORD_HISTORY` at spawn

**Files:**
- Modify: `internal/daemon/daemon.go:2200-2210` (spawn env assembly)

- [ ] **Step 1: Implement (no isolated unit test — covered by Task 16 manual check)**

In `internal/daemon/daemon.go`, immediately after the `switch p.Name { ... }` block at `:2210` (before `if len(envVars) > 0 {`):

```go
	// Generic opt-in: any plugin with a hook producer that records input
	// history gets the gate env. The hook subprocess reads QUIL_RECORD_HISTORY
	// and appends submitted prompts to the per-pane history store.
	if p.Command.RecordHistory {
		envVars = append(envVars, "QUIL_RECORD_HISTORY=1")
	}
```

- [ ] **Step 2: Verify it builds**

Run: `./scripts/dev.sh vet 2>&1 | tail -20`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/daemon.go
git commit -m "feat(history): set QUIL_RECORD_HISTORY env for opted-in plugins"
```

---

## Task 8: IPC message types + payloads

**Files:**
- Modify: `internal/ipc/protocol.go` (message-type consts near `:85`; payload structs near `:359`)
- Test: `internal/ipc/protocol_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/ipc/protocol_test.go`:

```go
func TestPaneHistoryPayloads_RoundTrip(t *testing.T) {
	req := PaneHistoryReqPayload{PaneID: "pane-1"}
	msg, err := NewMessage(MsgPaneHistoryReq, req)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var gotReq PaneHistoryReqPayload
	if err := msg.DecodePayload(&gotReq); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if gotReq.PaneID != "pane-1" {
		t.Fatalf("req round-trip mismatch: %+v", gotReq)
	}

	resp := PaneHistoryRespPayload{
		PaneID:  "pane-1",
		Entries: []HistoryEntryMeta{{TsMs: 9, Preview: []string{"a", "b"}}},
	}
	rmsg, _ := NewMessage(MsgPaneHistoryResp, resp)
	var gotResp PaneHistoryRespPayload
	if err := rmsg.DecodePayload(&gotResp); err != nil {
		t.Fatalf("DecodePayload resp: %v", err)
	}
	if len(gotResp.Entries) != 1 || gotResp.Entries[0].TsMs != 9 || gotResp.Entries[0].Preview[1] != "b" {
		t.Fatalf("resp round-trip mismatch: %+v", gotResp)
	}

	er := PaneHistoryEntryRespPayload{PaneID: "p", TsMs: 9, Text: "full", Found: true}
	emsg, _ := NewMessage(MsgPaneHistoryEntryResp, er)
	var gotER PaneHistoryEntryRespPayload
	if err := emsg.DecodePayload(&gotER); err != nil {
		t.Fatalf("DecodePayload entry resp: %v", err)
	}
	if !gotER.Found || gotER.Text != "full" {
		t.Fatalf("entry resp round-trip mismatch: %+v", gotER)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — undefined message consts / payload types.

- [ ] **Step 3: Write minimal implementation**

In `internal/ipc/protocol.go`, add to the message-type const block (near the memory-report consts):

```go
	MsgPaneHistoryReq       = "pane_history_req"
	MsgPaneHistoryResp      = "pane_history_resp"
	MsgPaneHistoryEntryReq  = "pane_history_entry_req"
	MsgPaneHistoryEntryResp = "pane_history_entry_resp"
```

Add payload structs (near the memory-report payloads):

```go
// PaneHistoryReqPayload requests the input-history preview list for one pane.
type PaneHistoryReqPayload struct {
	PaneID string `json:"pane_id"`
}

// HistoryEntryMeta is one list row: a stable id (TsMs) and up to 3 preview lines.
type HistoryEntryMeta struct {
	TsMs    int64    `json:"ts_ms"`
	Preview []string `json:"preview"`
}

// PaneHistoryRespPayload carries the preview list, newest first.
type PaneHistoryRespPayload struct {
	PaneID  string             `json:"pane_id"`
	Entries []HistoryEntryMeta `json:"entries"`
}

// PaneHistoryEntryReqPayload requests one entry's full text by its TsMs id.
type PaneHistoryEntryReqPayload struct {
	PaneID string `json:"pane_id"`
	TsMs   int64  `json:"ts_ms"`
}

// PaneHistoryEntryRespPayload carries one entry's full text (Found=false if the
// id no longer exists, e.g. compacted away between list and fetch).
type PaneHistoryEntryRespPayload struct {
	PaneID string `json:"pane_id"`
	TsMs   int64  `json:"ts_ms"`
	Text   string `json:"text"`
	Found  bool   `json:"found"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — `TestPaneHistoryPayloads_RoundTrip` green.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/protocol.go internal/ipc/protocol_test.go
git commit -m "feat(ipc): add pane history request/response messages"
```

---

## Task 9: Daemon handlers — list + fetch entry

**Files:**
- Modify: `internal/daemon/daemon.go` (`handleMessage` switch near `:819`; new handlers near `:3366`)
- Test: `internal/daemon/daemon_integration_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/daemon/daemon_integration_test.go`. Mirror the existing `TestHandleMemoryReportReq_*` setup for booting a daemon and capturing a response on a conn. The key assertions:

```go
func TestHandleHistoryReq_ReturnsPreviewsNewestFirst(t *testing.T) {
	d := newTestDaemon(t) // use whatever helper the existing integration tests use
	paneID := "pane-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := config.QuilDir() // tests set QUIL_HOME via t.Setenv in existing helpers
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 1, Text: "first prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 2, Text: "second\nmultiline"}); err != nil {
		t.Fatal(err)
	}

	resp := roundTripHistoryReq(t, d, ipc.PaneHistoryReqPayload{PaneID: paneID}) // helper below
	if len(resp.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(resp.Entries))
	}
	if resp.Entries[0].TsMs != 2 { // newest first
		t.Fatalf("want newest (ts=2) first, got %d", resp.Entries[0].TsMs)
	}
	if len(resp.Entries[0].Preview) != 2 {
		t.Fatalf("want 2 preview lines for multiline entry, got %d", len(resp.Entries[0].Preview))
	}
}
```

> Implement `newTestDaemon`, the conn round-trip, and `roundTripHistoryReq` by copying the exact mechanism `TestHandleMemoryReportReq_TabsPopulated` uses (it builds a daemon, marshals `MemoryReportReqPayload`, calls `d.handleMessage(conn, msg)` against an in-memory conn, and decodes the response). If that test uses a `fakeConn`/pipe, reuse it. Do not invent a new harness.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — daemon does not handle `MsgPaneHistoryReq` (no response captured), and helper refs undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/daemon/daemon.go` `handleMessage` switch, add (near the `MsgMemoryReportReq` case):

```go
	case ipc.MsgPaneHistoryReq:
		d.handlePaneHistoryReq(conn, msg)
	case ipc.MsgPaneHistoryEntryReq:
		d.handlePaneHistoryEntryReq(conn, msg)
```

Add the handlers (near `handleMemoryReportReq`):

```go
func (d *Daemon) handlePaneHistoryReq(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.PaneHistoryReqPayload
	if err := msg.DecodePayload(&p); err != nil {
		return
	}
	resp := ipc.PaneHistoryRespPayload{PaneID: p.PaneID}
	if !isValidHexID(p.PaneID, "pane-") {
		respondTo(conn, msg.ID, ipc.MsgPaneHistoryResp, resp)
		return
	}
	dir := config.QuilDir()
	// Opportunistic ring trim so the store stays bounded.
	if err := panehistory.Compact(dir, p.PaneID, panehistory.MaxEntries); err != nil {
		log.Printf("history compact pane %s: %v", p.PaneID, err)
	}
	entries, err := panehistory.Read(dir, p.PaneID)
	if err != nil {
		log.Printf("history read pane %s: %v", p.PaneID, err)
	}
	// Newest first.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		resp.Entries = append(resp.Entries, ipc.HistoryEntryMeta{
			TsMs:    e.TsMs,
			Preview: panehistory.Preview(e.Text, 3, 200),
		})
	}
	respondTo(conn, msg.ID, ipc.MsgPaneHistoryResp, resp)
}

func (d *Daemon) handlePaneHistoryEntryReq(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.PaneHistoryEntryReqPayload
	if err := msg.DecodePayload(&p); err != nil {
		return
	}
	resp := ipc.PaneHistoryEntryRespPayload{PaneID: p.PaneID, TsMs: p.TsMs}
	if !isValidHexID(p.PaneID, "pane-") {
		respondTo(conn, msg.ID, ipc.MsgPaneHistoryEntryResp, resp)
		return
	}
	entries, err := panehistory.Read(config.QuilDir(), p.PaneID)
	if err != nil {
		log.Printf("history read pane %s: %v", p.PaneID, err)
	}
	for _, e := range entries {
		if e.TsMs == p.TsMs {
			resp.Text = e.Text
			resp.Found = true
			break
		}
	}
	respondTo(conn, msg.ID, ipc.MsgPaneHistoryEntryResp, resp)
}
```

Add the import `"github.com/artyomsv/quil/internal/panehistory"` to `daemon.go` if not present.

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — `TestHandleHistoryReq_ReturnsPreviewsNewestFirst` green.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_integration_test.go
git commit -m "feat(history): serve pane history over IPC"
```

---

## Task 10: Delete history on pane destroy

**Files:**
- Modify: `internal/daemon/daemon.go:1184-1197` (`cleanupPaneArtifacts`)
- Test: `internal/daemon/daemon_integration_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCleanupPaneArtifacts_RemovesHistory(t *testing.T) {
	d := newTestDaemon(t)
	paneID := "pane-cccccccccccccccccccccccccccccccc"
	dir := config.QuilDir()
	if err := panehistory.Append(dir, paneID, panehistory.Entry{TsMs: 1, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	d.cleanupPaneArtifacts(paneID)
	if _, err := os.Stat(panehistory.Path(dir, paneID)); !os.IsNotExist(err) {
		t.Fatalf("history file not removed: stat err=%v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — file still exists.

- [ ] **Step 3: Write minimal implementation**

In `cleanupPaneArtifacts`, add before the closing brace:

```go
	if err := os.Remove(panehistory.Path(config.QuilDir(), paneID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("cleanup pane %s: remove history: %v", paneID, err)
	}
```

(`errors` and `os` are already imported in `daemon.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_integration_test.go
git commit -m "feat(history): unlink history file on pane destroy"
```

---

## Task 11: Keybinding default `command_history`

**Files:**
- Modify: `internal/config/config.go` (Keybindings struct near `:130`; defaults near `:220`)
- Test: `internal/config/config_test.go` (find existing default-keybinding test; add an assertion)

- [ ] **Step 1: Write the failing test**

Add to the config test that checks default keybindings (search `grep -n "ToggleEager\|Redraw" internal/config/config_test.go`; if none, add a focused test):

```go
func TestDefaultKeybindings_CommandHistory(t *testing.T) {
	cfg := Default()
	if cfg.Keybindings.CommandHistory != "alt+shift+i" {
		t.Fatalf("want alt+shift+i, got %q", cfg.Keybindings.CommandHistory)
	}
}
```

> Confirm the default-config constructor is named `Default()` by checking an existing config test; use whatever the file already uses.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: FAIL — `CommandHistory` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/config/config.go`, add to the `Keybindings` struct (after `ToggleEager`):

```go
	// CommandHistory opens the per-pane input-history modal (list of submitted
	// prompts; Enter opens one full-text read-only). Only meaningful for panes
	// whose plugin sets record_history (claude-code).
	CommandHistory string `toml:"command_history"`
```

In the defaults block (alongside `ToggleEager: "alt+shift+e"`):

```go
			CommandHistory: "alt+shift+i",
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(history): add command_history keybinding default"
```

---

## Task 12: TUI history dialog module

**Files:**
- Create: `internal/tui/history.go`
- Reference: `internal/tui/memory.go` (mirror its request/response/message pattern)

This task adds the state, the request commands, the response messages, and the render — but does NOT yet wire keys or the dialog screen (Task 13/14).

- [ ] **Step 1: Implement the module**

Create `internal/tui/history.go`:

```go
package tui

import (
	"fmt"
	"log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// historyState holds the input-history modal's data and cursor.
type historyState struct {
	paneID      string
	paneType    string
	supported   bool // active pane's plugin has record_history
	loading     bool
	entries     []ipc.HistoryEntryMeta // newest first
	cursor      int
}

// historyListMsg is produced when the daemon returns the preview list.
type historyListMsg struct {
	Resp ipc.PaneHistoryRespPayload
}

// historyEntryMsg is produced when the daemon returns one entry's full text.
type historyEntryMsg struct {
	Resp ipc.PaneHistoryEntryRespPayload
}

// openHistoryDialog transitions into the history modal for the active pane and
// records whether that pane's plugin supports history (for the empty state).
func (m Model) openHistoryDialog(paneID, paneType string, supported bool) Model {
	m.history = historyState{
		paneID:    paneID,
		paneType:  paneType,
		supported: supported,
		loading:   supported,
		cursor:    0,
	}
	m.dialog = dialogCommandHistory
	return m
}

// requestHistory asks the daemon for the active pane's preview list.
func (m Model) requestHistory(paneID string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgPaneHistoryReq, ipc.PaneHistoryReqPayload{PaneID: paneID})
		if err != nil {
			log.Printf("requestHistory: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("hist-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestHistory: send: %v", err)
		}
		return nil
	}
}

// requestHistoryEntry asks the daemon for one entry's full text by TsMs id.
func (m Model) requestHistoryEntry(paneID string, tsMs int64) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgPaneHistoryEntryReq, ipc.PaneHistoryEntryReqPayload{
			PaneID: paneID,
			TsMs:   tsMs,
		})
		if err != nil {
			log.Printf("requestHistoryEntry: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("histentry-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("requestHistoryEntry: send: %v", err)
		}
		return nil
	}
}

// applyHistoryList stores the list response and clamps the cursor.
func (m Model) applyHistoryList(resp ipc.PaneHistoryRespPayload) Model {
	if resp.PaneID != m.history.paneID {
		return m // stale response for a pane we've navigated away from
	}
	m.history.entries = resp.Entries
	m.history.loading = false
	if m.history.cursor >= len(m.history.entries) {
		m.history.cursor = len(m.history.entries) - 1
	}
	if m.history.cursor < 0 {
		m.history.cursor = 0
	}
	return m
}

// renderCommandHistory draws the modal. Reuses dialogBox styling helpers used
// by other dialogs (see renderMemory in memory.go for the established pattern).
func (m Model) renderCommandHistory() string {
	var b strings.Builder
	title := "Input history"
	if m.history.paneType != "" {
		title += " · " + m.history.paneType
	}
	b.WriteString(title + "\n\n")

	switch {
	case m.history.loading:
		b.WriteString("Loading…")
	case !m.history.supported:
		b.WriteString("No input history for this pane type.")
	case len(m.history.entries) == 0:
		b.WriteString("No input history yet.")
	default:
		// width budget for preview lines inside the box
		inner := m.width/2 - 8
		if inner < 20 {
			inner = 20
		}
		for i, e := range m.history.entries {
			marker := "  "
			if i == m.history.cursor {
				marker = "› "
			}
			for j, ln := range e.Preview {
				prefix := "    "
				if j == 0 {
					prefix = marker
				}
				if len([]rune(ln)) > inner {
					ln = string([]rune(ln)[:inner-1]) + "…"
				}
				b.WriteString(prefix + ln + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("↑↓ nav · Enter open · Esc close")
	}

	// Wrap in the shared modal box. Use the same boxing helper other dialogs
	// use; renderMemory shows the canonical call. Placeholder name dialogBox:
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}
```

> **Before finalizing the render:** open `internal/tui/memory.go`'s `renderMemory` (or the function dispatched from `dialog.go:689 case dialogMemory`) and copy its exact box/centering/style approach so the history modal matches existing dialogs visually. Replace the placeholder `lipgloss.NewStyle().Padding…` with the same boxing the memory dialog uses. Also add the `history historyState` field to the `Model` struct (in `model.go`, alongside `mem`).

- [ ] **Step 2: Add the Model field**

In `internal/tui/model.go`, add to the `Model` struct (near the `mem` field):

```go
	history historyState
```

- [ ] **Step 3: Verify it builds**

Run: `./scripts/dev.sh vet 2>&1 | tail -30`
Expected: builds once `dialogCommandHistory` exists — it does not yet, so expect ONE error: `undefined: dialogCommandHistory`. That is fixed in Task 13. To check this task in isolation, temporarily confirm there are no OTHER errors (only the `dialogCommandHistory` reference).

- [ ] **Step 4: Commit**

```bash
git add internal/tui/history.go internal/tui/model.go
git commit -m "feat(history): add TUI history dialog state and IPC commands"
```

---

## Task 13: TUI dialog screen + read-only viewer + key handling

**Files:**
- Modify: `internal/tui/dialog.go` (screen const; render dispatch near `:689`; key handling; new `openReadonlyText`)

- [ ] **Step 1: Add the dialog screen constant**

Find the `dialogScreen` iota block in `internal/tui/dialog.go` (contains `dialogMemory`, `dialogGitRepoPick`). Add a new member at the end:

```go
	dialogCommandHistory
```

- [ ] **Step 2: Add `openReadonlyText`**

In `internal/tui/dialog.go`, near `openLogViewer` (`:1839`):

```go
// openReadonlyText opens arbitrary in-memory content in the same full-screen
// read-only TextEditor used by the log viewer. Unlike openLogViewer it does not
// read a file — the caller supplies the content (e.g. a history entry's full
// text). label appears in the editor's path field.
func (m Model) openReadonlyText(label, content string) (tea.Model, tea.Cmd) {
	m.tomlEditor = m.newLogViewerEditor(content, label)
	m.dialog = dialogLogViewer
	return m, tea.ClearScreen
}
```

- [ ] **Step 3: Add render dispatch**

In the dialog render dispatch (the `switch` near `:689` that has `case dialogMemory:`), add:

```go
	case dialogCommandHistory:
		return m.renderCommandHistory()
```

> Match the surrounding cases' return convention (some return a string for centering, some render directly). Mirror `case dialogMemory:` exactly.

- [ ] **Step 4: Add key handling**

Find the dialog key handler (the function handling keys when `m.dialog != dialogNone`, e.g. `handleDialogKey` — search `case dialogMemory:` inside a key switch). Add a case:

```go
	case dialogCommandHistory:
		switch {
		case kbMatches(key, "up") || key == "k":
			if m.history.cursor > 0 {
				m.history.cursor--
			}
			return m, nil
		case kbMatches(key, "down") || key == "j":
			if m.history.cursor < len(m.history.entries)-1 {
				m.history.cursor++
			}
			return m, nil
		case key == "enter":
			if m.history.supported && m.history.cursor >= 0 && m.history.cursor < len(m.history.entries) {
				ts := m.history.entries[m.history.cursor].TsMs
				return m, m.requestHistoryEntry(m.history.paneID, ts)
			}
			return m, nil
		case key == "esc":
			m.dialog = dialogNone
			return m, tea.ClearScreen
		}
		return m, nil
```

> Use the project's actual key-string comparison idiom. The memory dialog's key case (same switch) shows whether keys arrive as raw strings (`"up"`, `"enter"`) or via `kbMatches`/`tea.KeyPressMsg`. Mirror it exactly — including how Esc/Enter are matched.

- [ ] **Step 5: Verify it builds**

Run: `./scripts/dev.sh vet 2>&1 | tail -30`
Expected: no errors (Task 12's `dialogCommandHistory` reference now resolves).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/dialog.go
git commit -m "feat(history): wire history dialog screen and read-only viewer"
```

---

## Task 14: TUI model wiring — keybinding + response dispatch

**Files:**
- Modify: `internal/tui/model.go` (global key switch; `listenForMessages` dispatch near `:3045`; Update handling near `:960`)

- [ ] **Step 1: Dispatch the new IPC responses**

In `listenForMessages` (the `switch msg.Type` near `:3045`), add:

```go
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
```

- [ ] **Step 2: Handle the messages in Update**

In `Update` (near the `case memoryReportMsg:` at `:960`), add:

```go
	case historyListMsg:
		m = m.applyHistoryList(msg.Resp)
		return m, nil

	case historyEntryMsg:
		if msg.Resp.Found {
			label := fmt.Sprintf("Input @ %s", time.UnixMilli(msg.Resp.TsMs).Format("2006-01-02 15:04:05"))
			return m.openReadonlyText(label, msg.Resp.Text)
		}
		// Entry vanished (compacted between list and fetch) — just refresh list.
		return m, m.requestHistory(m.history.paneID)
```

(Ensure `fmt` and `time` are imported in `model.go` — they already are.)

- [ ] **Step 3: Add the open keybinding**

In the global key handler (where `kbMatches(key, m.cfg.Keybindings.Redraw)` / `.ToggleEager` cases live), add a case. The active pane and its plugin determine support:

```go
		case kbMatches(key, m.cfg.Keybindings.CommandHistory):
			tab := m.activeTabModel()
			if tab == nil {
				return m, nil
			}
			pane := tab.ActivePaneModel()
			if pane == nil {
				return m, nil
			}
			supported := false
			if p := m.pluginRegistry.Get(pane.Type); p != nil {
				supported = p.Command.RecordHistory
			}
			m = m.openHistoryDialog(pane.ID, pane.Type, supported)
			if supported {
				return m, m.requestHistory(pane.ID)
			}
			return m, nil
```

> Place this alongside the other `kbMatches(key, m.cfg.Keybindings.X)` cases and match their exact structure (some flatten via `kbBindings`, some compare the raw key). Also add `m.cfg.Keybindings.CommandHistory` to `notesKeyExempt` if you want it usable in notes mode (optional; the spec lists it exempt — add it to the `notesKeyExempt` allow-list builder).

- [ ] **Step 4: Verify it builds and the suite passes**

Run: `./scripts/dev.sh test 2>&1 | tail -40`
Expected: PASS — whole suite green, no compile errors.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go
git commit -m "feat(history): open history modal via keybinding, dispatch responses"
```

---

## Task 15: Documentation

**Files:**
- Modify: `docs/keybindings.md`, `docs/plugin-reference.md`, `.claude/CLAUDE.md`

- [ ] **Step 1: Update docs**

- `docs/keybindings.md`: add `command_history` (default `alt+shift+i`) to the keymap table with a one-line description ("Open per-pane input history (AI panes)").
- `docs/plugin-reference.md`: document `[command] record_history` — boolean opt-in; when true the daemon sets `QUIL_RECORD_HISTORY=1` and the plugin's hook producer appends submitted prompts to `~/.quil/history/<paneID>.jsonl`; only meaningful for plugins with a hook producer (claude-code; opencode follow-up).
- `.claude/CLAUDE.md`: add a bullet under the architecture/conventions list summarizing the feature: new `internal/panehistory/` package; `history/<paneID>.jsonl` store; `record_history` plugin opt-in + `QUIL_RECORD_HISTORY` env; `MsgPaneHistory*` IPC; `dialogCommandHistory` + `alt+shift+i`; reuses the read-only log-viewer overlay; OpenCode is a follow-up.

- [ ] **Step 2: Verify nothing else references stale info**

Run: `git diff --stat`
Expected: only the three docs files changed.

- [ ] **Step 3: Commit**

```bash
git add docs/keybindings.md docs/plugin-reference.md .claude/CLAUDE.md
git commit -m "docs(history): document input history feature"
```

---

## Task 16: Manual verification (dev mode)

**Files:** none (verification only)

> Follow `.claude/rules/dev-environment.md`: dev mode ONLY. Never touch the production daemon or `~/.quil/`.

- [ ] **Step 1: Build dev binaries**

Run: `./scripts/dev.sh build 2>&1 | tail -20`
Expected: 6 binaries built, no errors.

- [ ] **Step 2: Launch dev instance**

Run (Windows): `./scripts/quil-dev.ps1`
Confirm `[dev]` shows in the status bar.

- [ ] **Step 3: Exercise the feature**

1. Open a `claude-code` pane (Ctrl+N → claude-code).
2. Submit three distinct prompts (e.g. "first prompt", a multiline paste with a stack trace, "third prompt").
3. Press `Alt+Shift+I` → the Input history modal lists them **newest first** with ≤3-line previews.
4. `↑/↓` to navigate; `Enter` on the multiline entry → full text opens in the read-only viewer; select + copy works (existing log-viewer copy keys); `Esc` returns.
5. Open a `lazygit` pane, press `Alt+Shift+I` → "No input history for this pane type."
6. Restart the dev daemon (stop dev daemon by PID from `./.quil/quild.pid`, relaunch) → reopen the claude pane's history → entries still present (persistence).
7. Destroy the claude pane (Ctrl+W) → confirm `./.quil/history/<paneID>.jsonl` is gone.

- [ ] **Step 4: Confirm success**

All seven sub-steps behave as described. If any fail, debug with `superpowers:systematic-debugging` before claiming completion.

- [ ] **Step 5: Final commit (if any doc/tweak fixes arose)**

```bash
git add -A
git commit -m "fix(history): address manual-verification findings"
```

---

## Self-Review Notes

- **Spec coverage:** capture (Task 6), store+caps (Tasks 1–4), opt-in (Task 5), env gate (Task 7), IPC (Task 8), serving+compaction (Task 9), destroy cleanup (Task 10), keybinding (Task 11), list UI (Tasks 12–14), read-only viewer reuse (Task 13), edge cases (partial line T2, empty/oversize T1, newest-first T9, unsupported-pane empty state T12/T14), docs (Task 15), restart-survival + manual (Task 16). OpenCode is explicitly out of scope (spec follow-up).
- **Stable entry id:** uses `TsMs` (set by the stateless hook from injected `nowMs`) rather than a sequence number the subprocess can't compute. `Found=false` covers the rare compacted-away-between-list-and-fetch race (Task 14 re-lists).
- **Type consistency:** `Entry{V,TsMs,SessionID,Text}`, `HistoryEntryMeta{TsMs,Preview}`, `PaneHistory*Payload`, `Append/Read/Preview/Compact/Path/Dir`, `MaxEntryBytes`/`MaxEntries`, `record_history`/`RecordHistory`/`QUIL_RECORD_HISTORY`, `dialogCommandHistory`, `Keybindings.CommandHistory` used consistently across tasks.
- **Verify-against-codebase flags:** several TUI/daemon/test steps say "mirror the existing X" because exact harness/idiom (test daemon helper, dialog key dispatch shape, modal boxing) must match what's already there — do not invent parallel mechanisms.
