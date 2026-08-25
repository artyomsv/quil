# The create-pane picker is titled "New Pane" even when it is creating a tab

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/tui/dialog.go` (create-pane render, step-0 arm: `dialogTitle.Render("New Pane")`) |
| Found during | Code review of PR #191 (new-tab picker Esc cancels) — pre-existing, not introduced there |
| Date | 2026-08-25 |

## Issue

The dialog serves two targets (`Model.createPaneTarget`): `paneTargetSplit`
(Ctrl+N, a pane in the active tab) and `paneTargetNewTab` (Ctrl+T, the first
pane of a new tab). Step 0 renders the heading `New Pane` unconditionally, so
pressing Ctrl+T opens a dialog whose title says it is about to create a pane in
the tab you are already looking at.

The rest of the flow already distinguishes the two targets carefully — the
placement step is skipped for a new tab, `setupDiscoveryBase` uses the project
root rather than the active pane's CWD, and the submit sends `create_tab`
rather than `create_pane`. The title is the one surface that does not.

## Why it matters

Small, but it is the first thing on screen and it names the wrong noun at the
exact moment the user is deciding whether they meant to open this dialog at
all. PR #191 made the same dialog's Esc key honest about cancelling; the
heading is the remaining place where the screen describes something other than
what will happen.

## Fix

Branch the step-0 title on `m.createPaneTarget` — `New Tab` for
`paneTargetNewTab`, `New Pane` otherwise. Worth a render assertion so the two
targets do not drift again; note the palette's "New tab" row dispatches through
`handleNewTab` and therefore lands on the same heading.
