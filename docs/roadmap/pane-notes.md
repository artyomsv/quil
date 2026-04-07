# Pane Notes

| Field | Value |
|-------|-------|
| Priority | 7 |
| Effort | Small |
| Impact | Medium |
| Status | Done |
| Depends on | — |

## Problem

While using Aethel, developers often want to jot down transient context about
what a specific pane is doing — steps that worked, links to debug, a partial
explanation from an AI, a TODO for tomorrow. Today there is no place for those
notes inside Aethel: users either scribble them in a separate editor (lost
context) or commit them to the pane's scrollback (lost when the pane is
destroyed or scrolls away).

The **context-switching tax** here is small but frequent: every time you want
to write something down, you either break flow by opening a separate editor
window or risk losing the thought entirely.

## Solution

A plain-text notes editor that opens **side by side** with the active pane
when toggled. Notes are bound to the pane (by ID), persisted to disk, and
survive pane destruction and daemon restart.

### Behaviour

- `Alt+E` toggles notes mode for the **active pane**
- When notes mode is active, the tab content area splits ~60/40 horizontally:
  the pane on the left, the notes editor on the right
- Other panes in the tab keep running in the background (notes mode does not
  affect them visually or functionally)
- The notes editor has keyboard focus while notes mode is active; keys route
  to the editor, not to the pane. The pane is effectively read-only while the
  editor is open. Exit notes mode (`Alt+E` or `Esc`) to interact with the pane
- Notes are stored one file per pane at `~/.aethel/notes/<pane-id>.md`
- Notes survive pane destruction — the file remains on disk for future
  browsing (a dedicated notes browser ships in Phase 2)
- Notes mode and focus mode (`Ctrl+E`) are mutually exclusive — entering
  notes mode while in focus mode exits focus first
- `Ctrl+W`, `Alt+W`, `Alt+H`, `Alt+V` (close/split) auto-exit notes mode
  (with a flush to disk) before the structural action fires

### Save behaviour (three safety nets)

1. **30-second debounce** — any keystroke that mutates content resets a
   5-second tick-driven check. When the pane is idle for ≥30s, the editor
   saves automatically.
2. **`Ctrl+S` explicit save** — traditional save shortcut. Cancels the
   debounce, writes immediately, clears the dirty flag.
3. **Auto-save on exit** — leaving notes mode (`Alt+E`, `Esc`, close/split
   actions, TUI quit) always flushes pending edits to disk.

## Architecture

```
internal/persist/notes.go        # atomic load/save for notes files
internal/tui/notes.go            # NotesEditor wrapper around TextEditor
internal/tui/editor.go           # TextEditor gains Highlight field
                                 # ("toml" | "plain")
internal/tui/model.go            # notesMode/notesEditor state, View split,
                                 # key routing, notesTick debounce check
internal/config/config.go        # NotesDir() path helper, NotesToggle keybind
```

### Data flow

```
User presses Alt+E
       │
       ▼
Model.enterNotesMode()
       │
       ├─ Load ~/.aethel/notes/pane-<id>.md via persist.LoadNotes()
       ├─ Wrap content in TextEditor{Highlight: "plain"}
       ├─ Create NotesEditor wrapper
       └─ Schedule notesTick() (5s interval)

User types → NotesEditor.HandleKey()
       │
       ├─ Delegates most keys to TextEditor
       ├─ Intercepts Ctrl+S → NotesEditor.Save() → persist.SaveNotes()
       ├─ Intercepts Esc → returns notesActionExit
       └─ Tracks dirty + lastEditAt

Tick fires (every 5s) → NotesEditor.MaybeAutoSave()
       │
       └─ If dirty && time.Since(lastEditAt) >= 30s → save

User presses Alt+E again (or Esc, or Ctrl+W, or quit)
       │
       ▼
Model.exitNotesMode()
       │
       ├─ NotesEditor.Close() → flushes dirty content
       └─ Reset notesMode, notesEditor
```

### Why a wrapper around TextEditor?

The existing `TextEditor` has two TOML-specific behaviours that make it
unsafe to use directly for notes:

1. **`TextEditor.Save()` validates TOML syntax** and requires the file path
   to live under `config.PluginsDir()`. For notes we need plain-text saves
   into `config.NotesDir()`.
2. **`TextEditor.HandleKey("esc")` closes the editor** (returns `closed=true`).
   For notes, `Esc` should clear a selection first and only exit on a second
   press.

The wrapper intercepts `Ctrl+S` and `Esc` before delegating to `TextEditor`,
leaving the TOML editor behaviour untouched. It also adds a `Highlight string`
field to `TextEditor` so the notes variant skips TOML syntax colouring.

## Files changed

| File | Action |
|---|---|
| `internal/persist/notes.go` | **New** — atomic load/save/delete |
| `internal/persist/notes_test.go` | **New** — roundtrip and path traversal tests |
| `internal/tui/notes.go` | **New** — NotesEditor wrapper |
| `internal/tui/notes_test.go` | **New** — editor wiring tests |
| `internal/tui/editor.go` | Modify — add `Highlight` field and `highlight()` helper |
| `internal/tui/model.go` | Modify — notesMode state, View split, key routing, tick |
| `internal/config/config.go` | Modify — `NotesDir()` helper, `NotesToggle` keybind |
| `cmd/aethel/main.go` | Modify — call `model.FlushNotes()` on exit as a safety net |
| `ROADMAP.md` | Modify — move M7 from Planned to Completed |
| `CHANGELOG.md` | Modify — Unreleased entry |
| `.claude/CLAUDE.md` | Modify — notes convention + M7 milestone |

## Phase 2 (not shipped)

- `~/.aethel/notes/.index.json` metadata sidecar (pane name, last edited)
- `F1 → Notes` dialog listing every notes file (active pane + orphans)
- Delete orphan notes from the browser
- Optional search across all notes

## Phase 3 (future)

- Markdown rendering preview pane
- MCP `read_pane_note` / `write_pane_note` tools — let AI assistants leave
  structured notes for the user about what they did in a pane
- Per-pane status hint in the pane border (`📝 12 lines`)

## Decisions

| Question | Answer | Rationale |
|---|---|---|
| Keybinding | `Alt+E` | `Alt+N` is taken by the notification center; `E` is mnemonic for "Edit notes" |
| Save behaviour | 30s debounce + `Ctrl+S` + auto-save on exit | Three independent safety nets so the user never loses work regardless of how they leave notes mode |
| Phase 1 scope | Core editing only | Ship and dogfood the editor UX before designing browsing |
| Pane interaction | Read-only pane | Keys always route to the editor; exit notes to interact with the pane. Simpler than focus cycling |
| File format | Markdown (`.md`) | Plain text today, room for rendered preview later without a migration |
| Pane ID as filename | Yes (stable UUID format `pane-XXXXXXXX`) | Survives daemon restarts because pane IDs are persisted in `workspace.json` |
