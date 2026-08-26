---
description: Pane rendering, tab bar, mouse handling, selection, scrollback, and the keybinding action registry. Load when touching pane/tab rendering, mouse routing, the selection layer, or internal/keymap.
paths:
  - "**/internal/tui/pane*.go"
  - "**/internal/tui/dim*.go"
  - "**/internal/tui/tab.go"
  - "**/internal/tui/layout.go"
  - "**/internal/tui/model.go"
  - "**/internal/tui/compose.go"
  - "**/internal/tui/selection.go"
  - "**/internal/tui/keymatch.go"
  - "**/internal/tui/keyspecs*.go"
  - "**/internal/tui/keydispatch*.go"
  - "**/internal/tui/sequence*.go"
  - "**/internal/keymap/**"
  - "**/internal/config/bindings*.go"
  - "**/internal/tui/oscfilter.go"
  - "**/internal/tui/splitdrag*.go"
  - "**/internal/clipboard/**"
---

# TUI Rendering

Extracted verbatim from `.claude/CLAUDE.md`. Loaded only when the files above are in play.

## Tab bar and keybindings

### Tab bar

tabs show 1-based index prefix (`1:Shell`, `2:Build`) matching Alt+1-9 shortcuts. Index hidden during rename editing. The active tab is also prefixed with `* ` (rendered through the `tabLabel(idx)` helper shared by `renderTabBar` and `hitTestTab` so click coords align with the rendered widths). Live rename emits `tea.ClearScreen` on every keypress so width changes don't leave stale glyphs from the previous-shorter render (Bubble Tea v2 cell-diff occasionally misses width shifts mid-bar — same "width changes — force full redraw" pattern used in dialogs). Mouse: click-and-drag a tab reorders it (slide semantics — intermediate tabs shift one slot at a time, dragged tab follows the cursor). The drag tracker (`Model.tabDragFromIdx`, init `-1`) is primed on click and consumed on motion at Y=0; each slot crossing fires `MsgReorderTab{TabID, NewIndex}` so the daemon's `SessionManager.tabOrder` stays authoritative and the next `workspace_state` broadcast is a no-op reconciliation. `SessionManager.ReorderTab` clamps NewIndex to bounds, so a stale TUI never has to race for an accurate tab count

### The action registry (`internal/keymap`)

keys resolve to ACTIONS, not to config strings. `internal/keymap` owns: `ParseChord`/`ParseSpec` (canonical chords, `,`-separated alternatives, space-separated multi-step sequences), the `registry` of `Action{ID, Label, Group, Tier, Order, Default, Hidden}` in `action.go`, and `Build(specs)` → `(*Keymap, []Conflict)`. It imports **stdlib plus `BurntSushi/toml`** (for `preset.go`'s embedded presets) and nothing else — no `config`, no `tui`, and no knowledge of where files live, which is what keeps it testable without a `Model` and without a `QUIL_HOME`. `config.KeySpecsFromConfig` maps the legacy `[keybindings]` field names onto action IDs; `internal/config/bindings.go` owns every `QuilDir()`-derived path.

**`Tier` is not cosmetic.** `handleKey` (`internal/tui/model.go`) does an early-tier lookup, then `tryPluginRawKey`, then `isSelectionExtendKey`, then a late-tier lookup, then the `ctrl+alt+v`/`f8` paste aliases, then the reserved `ctrl+n`/`f1` switch (`alt+1..9` left that switch when they became `tab.switch_1..9` actions). So an early action beats a plugin's `raw_keys` claim and a late one loses to it; moving an action between tiers silently changes that. `TestActions_TierSplitMatchesLegacySwitches` pins the split against the pre-registry switch order.

**Sequences are two flat maps, not a trie.** `Keymap.seqs` maps a full canonical sequence (`"ctrl+b c"`) to its action; `Keymap.partial` maps every PROPER prefix to one owning action. `MatchSeq(pending)` is two map hits — and `partial` is consulted on EVERY keypress, bound or not, so O(1) is what keeps the machine free in the input path. A trie earns nothing at ~54 actions.

**The probe is TIER-AGNOSTIC and that is the whole subtlety.** `pane.close` is late-tier, so `"ctrl+b x"` leaves its opening chord in neither tier's chord map: a tier-scoped probe answers `MatchNone`, the key falls to `tryPluginRawKey` or the PTY, and the sequence can never complete however many times `x` is pressed. The tier split governs `Exact` resolution of SINGLE CHORDS only. `TestMatchSeq_IsTierAgnostic` + `TestSequence_LateTierSequenceArmsAndCompletes` pin it; a mutation making the probe early-only fails ten tests.

**One probe site, in `handleKey` between the overlay guard and the early-tier lookup.** Dialog, rename, pane-rename, ctxmenu and overlay all `return` above it, so they are inert by ordering — no predicate. The reconnect/parked screen never reaches `handleKey` at all (`freezeInput` is called unconditionally in `Update` and returns frozen). Only `sidebarFocused` and an active selection sit DOWNSTREAM and are named explicitly; anything new that consumes keys after that line must be added there.

**A completed sequence sets a local `seqAction`, it does not run through a extracted dispatcher.** The two `switch` blocks stay byte-identical where they are — `TestHandleKey_EveryDispatchedActionHasACaseArm` scrapes `handleKey`'s SOURCE TEXT for `case "<id>"` arms and the early/late boundary, so moving them into methods breaks it, and the tier tests can no longer see which switch a case landed in. `seqAction` blanks the other tier's lookup (the final chord may ALSO be bound as a plain chord) and disables the between-tier guards, because a completed sequence must outrank a plugin's `raw_keys` claim on that chord — the one deliberate precedence change, scoped to multi-step bindings only.

**Prefix shadowing is resolved BEFORE insertion (`resolveShadowing`), never after.** Removing a loser from the tables afterwards leaves its prefixes in `km.partial`, so the dropped binding still swallows its own first chord. Resolution runs on PARSED sequences rather than spec strings: re-serialising and re-splitting on `,` is unreadable the moment a binding uses the comma key, and it hides a malformed spec from `Build`'s per-action fallback. Cross-layer the HIGHER layer wins whichever is shorter (a user override must be able to reclaim the prefix key as a chord); within one layer the SHORTER is refused (both tie on layer, so length is the only unambiguous tie-break). Equal-length collisions are `ConflictDuplicate`, resolved by `Order` — never `ConflictShadowed`.

**There is no `presets/default.toml`.** `DefaultLayer()` reads each action's registered `Default`; a file would be a second copy free to drift from the one dispatch uses. `presets/` holds only genuinely different keymaps (`tmux.toml`). A preset carries its own `Prefix`, and `SetBindings` uses it when `bindings.toml` sets none — without that, `preset = "tmux"` expands all 24 `${prefix}` bindings against `""` and drops every one, so the preset silently does nothing.

**The comma key is bindable only as `"comma"`** (a `keyAliases` entry). A literal `,` is the alternatives separator and splits the spec before any chord parsing; the canonical form stays `,` because that is what a real press reports. `TestPresetChords_MatchRealKeyPresses` (in `internal/tui`, because `keymap` cannot build a `tea.KeyPressMsg`) validates every preset chord against bubbletea — parse FIRST, then compare `Chord.String()`, or an aliased spelling can never match.

**`Build` never fails.** A malformed spec falls back to that action's shipped `Default` and reports a `ConflictMalformed`; an unknown ID is ignored with a `ConflictUnknownAction`; one bad config line must not cost the user their other 40 bindings. `Conflict.String()` is both the log line (`buildKeymap` warns each one) and the F1 → Shortcuts row, so it front-loads key → winner → loser and puts the consequence clause last, where truncation eats it first. `ConflictHardcoded` DERIVES its direction rather than storing one: `hardcodedKeys` records where `handleKey` checks each built-in key, and the 13 checked after BOTH tier lookups (`f1`, `ctrl+n`, `alt+1..9`, `f8`, `ctrl+alt+v`) are ones the bound ACTION wins — the shipped message claimed the opposite for all 21.

**Readers, by shape**: `Model.isAction(key, id)` for a modal surface that must recognise one action (dialog paste branches, `ctxmenu.go`, `overlay.go` — six call sites); `Keymap.Display(id)` for a help row (`" / "`-joined, canonical `ctrl+v` spelling); `Keymap.Keys(id)` when the individual chords are needed (the reconnect screen's freeze-escape check). F1 → Shortcuts is DERIVED from `ActionsByGroup()`, so a bound action cannot be missing from it — the hand-maintained list had lost seven of eight project bindings.

`internal/tui/keymatch.go`'s `kbMatches`/`kbBindings`/`kbDisplay` are the pre-registry string comparison and are DEPRECATED for dispatch: three call sites remain (`notesKeyExempt`, the notes-mode key split, the hardcoded reconnect resume key), all Stage 2's to remove. Do not add more. Multi-binding config strings still work (`rename_pane = "alt+f2,alt+shift+r"` — F2 is eaten by macOS unless "Use F1, F2, etc. keys as standard function keys" is enabled, and Option-as-Meta is terminal-specific, so the second binding is the reliable fallback); `ParseSpec` reads the same syntax `kbBindings` did

## Mouse

### Scrollbar click-and-drag

`PaneModel.ScrollToRelY(relY, innerH)` is the inverse of `renderScrollback`'s thumb-position formula — clicking at content row R puts the thumb's top at R (matches every GUI scrollbar). `Model.hitTestScrollbar(x, y)` returns the `PaneRect` whose scrollbar zone was hit and validates Y is inside the content area. The visible scrollbar is 1 cell at column `OX + W - 2`, but the hit zone widens to 3 cells (constant `scrollbarHitPadding = 1`) — the rightmost content column, the scrollbar column, and the right border — so off-by-one clicks register as scroll instead of text selection. The drag rect is captured once at click time into `Model.scrollDragPaneID` + `scrollDragRect` so a layout change (resize, split, notes mode toggle) mid-drag doesn't drift the mapping; `activePaneByID(id)` looks up the drag target through the active tab on each motion event so a destroyed pane silently drops the drag. Cleared on `MouseReleaseMsg`

### Mouse drag invariant

`Model.clearDragState()` zeros every mutually-exclusive drag flag (`tabDragFromIdx`, `scrollDragPaneID`/`scrollDragRect`, `mouseDown`, `notesMouseDown`) in one place. Every "start a new drag" path in `MouseClickMsg` and the "drag ended" branches in `MouseReleaseMsg` route through this helper instead of zeroing siblings inline, so a future drag mode can be added by extending the helper rather than auditing each click handler. `TestModel_ClearDragState` guards the invariant

### Split-border drag-resize

`CollectBorders`/`BorderHit` (`internal/tui/layout.go`) enumerate split lines (hit zone per line = the two drawn border glyphs + `splitBorderHitPadding` widening — symmetric for V-split rows, right-only for H-split columns so the zone never reaches the left neighbour's drawn scrollbar column at bd-2; reverse scan = deepest node wins at T-junctions). The border check runs BEFORE the scrollbar check in `MouseClickMsg` — the drawn split line always arms the drag (a scrollbar-first order silently ate the left glyph via scrollbar padding, with zero feedback on panes without scrollback), while thumb clicks on scrollbarX keep working because the border zone stops at bd-1; `hitTestSplitBorder`/`dragSplitBorder`/`finishSplitDrag` (`model.go`) arm/move/commit the drag — the ratio is clamped in cells against subtree minimums (`minWidth`/`minHeight`: leaves are 10×4, H-splits sum widths, V-splits sum heights) then derived, so boundaries are exact; PTY resize + layout persistence are deferred to mouse release (`resizeAllPanes` + `sendAllLayouts` — the on-release-only design avoids mid-drag PTY churn). Mid-drag only `Ratio` + pane RECTS move (`resizeNodeRects` in layout.go) — the VT emulator must NOT resize mid-drag: `ResizeVT`'s contract pairs every emulator resize with a PTY redraw, so unpaired intermediate-width rewraps permanently garble content at the narrowest width crossed (2026-07-15 corruption bug); the single VT+PTY resize pair fires together in `finishSplitDrag`. Panes whose rect touches the dragged line get a transient `splitDragHighlight` border (color 39, included in `renderKey`), set/cleared via `setSplitDragHighlight`. Disabled in focus mode, notes mode, and single-pane tabs; scrollbar hit test keeps priority. Drag state (`splitDragNode`/`splitDragRect`) rides `clearDragState()`; a node pruned mid-drag (workspace reconciliation) drops the drag via `treeContains`. Tests in `splitdrag_test.go`

### Mouse-wheel forwarding to tracking apps

apps that enable DEC mouse tracking (opencode, claude-code, vim, htop, lazygit, …) run on the alternate screen and scroll their own viewport — Quil's local scrollback is never populated, so the wheel must be forwarded to the child PTY instead. The daemon is authoritative because it is the only component that sees the one-time mouse-enable burst on every attach (the local VT emulator misses it when reattaching to an already-running `ghost_buffer=false` app like opencode): `internal/daemon/mousemode.go:scanMouseModes` walks the PTY stream for `CSI ? <params> (h|l)` and tracks a per-mode `mouseModeState` (`?9/?1000/?1002/?1003` tracking, `?1006` SGR) — one bool per mode so resetting a mode that was never set can't wrongly clear tracking. State lives on `Pane.MouseModes` (PluginMu-guarded), is broadcast (never persisted, re-derived on every spawn) via `mouse_tracking`/`mouse_sgr` in the workspace snapshot, and is throttled by `mouseModeBroadcastCooldown` (250 ms, compared against the last-broadcast state so a suppressed change is re-delivered on the next flush) to stop a hostile stream from forcing a full-snapshot broadcast storm. `handleRestartPaneReq` clears `MouseModes` on respawn (mirrors the TUI's `ResetVT`) so a stale flag can't type wheel escapes into a fresh shell prompt. TUI side (`internal/tui/pane.go`): `PaneModel.MouseTracking()` ORs the local VT-callback flags (`mouseX10/Normal/Button/Any`) with the daemon flag (`daemonMouseTracking`); `wheelForwardSeq(up, relX, relY)` encodes the notch as SGR (`\x1b[<64;…M` up / `65` down) when `?1006` is set, else legacy X10 (coords clamped to 222 — X10's single-byte limit). `internal/tui/model.go`'s `MouseWheelMsg` handler computes content-relative coords via `activePaneRect()` (resolves the active pane's rect in focus/notes/split layouts with the same width `View()` renders) and forwards via `sendInputToPane` before the local-scroll fallback — which enqueues onto the shared ordered input queue (`Model.enqueueInput`, see the pane-input-pipeline invariant in `.claude/CLAUDE.md`) rather than sending directly, because a wheel escape and a keystroke land on the same PTY stdin and either could otherwise overtake the other

## Rendering

### OSC window-title filtering (macOS claude-code render corruption)

the `charmbracelet/x/vt` emulator ends an OSC string at byte `0x9C` (the C1 String Terminator) even when `0x9C` is a UTF-8 continuation byte. claude-code sets its window title to `✳ Claude Code` (✳ = U+2733 = `E2 9C B3`); the `0x9C` terminates the OSC early and the tail (`… Claude Code`) spills into the VISIBLE grid — the doubled logo (`Claude CodClaude Code …`) and the input-line leak (`AAA`→`AAAude Code`) seen on Terminal.app. `internal/tui/oscfilter.go` (`oscTitleFilter`) strips OSC 0/1/2 (window/icon title) from a pane's PTY output before the emulator; called in `PaneModel.AppendOutput` after `rawBuf.Write` (the raw ring buffer keeps untouched bytes, only the emulator feed is filtered). It is a chunk-boundary-aware state machine (title split across coalesced output is still stripped), only strips numbers exactly 0/1/2 (OSC 7 cwd, 10/11 colors, 52 clipboard, 104, hyperlinks all pass through), and is one instance per `PaneModel` (`oscFilter`). Quil renders its own tab titles and never displays the child's window title, so dropping title OSCs is lossless. (A general fix belongs upstream: raw C1 bytes must not be treated as controls in a UTF-8 stream.) Word-jump keys: `keyToBytes` forwards any `Alt+<printable>` as `ESC+<char>` (Meta encoding) so macOS Terminal.app users with "Use Option as Meta key" get readline word navigation (`Option+B`/`Option+F` → `ESC-b`/`ESC-f`) with no config, and the documented `Alt+H`/`Alt+V`→PTY passthrough is restored (`Ctrl+Arrow` word-jump on Windows/Linux is unchanged). See `docs/keybindings.md`

### Pane cursor model

EVERY pane type gets a software reverse-video caret, drawn into the frame by `renderContent`/`insertCursor` (`internal/tui/pane.go`) when the pane is active and the app has not sent DECTCEM hide. `tea.View.Cursor` stays nil and the hardware cursor is never shown.

**The hardware cursor was tried and REVERTED, and this section used to document the version that lost.** `paneHardwareCursor()` and the `isTerminalPane` split it was gated on no longer exist — positioning the real cursor through `tea.View.Cursor` every frame desynced Bubble Tea's diff writer on Windows, and the first character typed on a fresh input line landed one cell off ("Test" → "T est"). The rationale lives at the bottom of `View()` in `model.go`. Anything that reintroduces per-frame cursor positioning inherits that bug.

Cell-loop renderers (`styledCellLine`, `styledCellLineWithSelection`, `insertCursor`) skip `Width==0` wide-char continuation cells — emitting a space there drifted scrollback/selection rendering +1 column per emoji/CJK glyph (`pane_widechar_test.go` guards this)

### Render coalescing

Bubble Tea calls `model.View()` once per MESSAGE (`p.render(model)` after every `Update`); its FPS option throttles the terminal flush, not the View construction. So every message — including timer ticks that change nothing — paid a full frame rebuild. `Model.skipRender` lets an audited branch return the cached frame instead (`Model.viewCache`).

**The design is fail-SAFE and must stay that way.** Rendering is the default, `Update` resets the flag on every message, and only branches audited as provably inert may lower it. A branch nobody has examined renders exactly as it did before coalescing existed. Getting this backwards trades a performance win for stale pixels.

**Two prologue mutations run before the type switch on EVERY message, and both must be folded in.** `ackFocusedPane` clears the focused pane's `unseen` (drawn by the tab bar and sidebar), and the context-menu prune closes a menu whose target pane vanished (View draws it AND derives `MouseMode` from it). Hence the single gate `prologueChangedView`, which every skip site ANDs against — the ctxmenu half is what round 1 of the performance review missed, and the symptom was a phantom menu on a cached frame with the terminal left in all-motion reporting.

**`PaneOutputMsg` is coalesced per BRANCH, not by pane visibility alone.** `View()` renders `m.activeTabModel()` and nothing else, so output from any other tab cannot change the frame — on a 41-tab workspace that was ~65% of all rebuilds. `handlePaneOutput` therefore returns `(tea.Cmd, changedView bool)`. `paneIsVisible` is the base; three branches raise it regardless — the restore settle, the first live frame, and a CWD change. **None of them is reachable from a BACKGROUND pane today**: restore state renders only in `PaneModel` (`buildTopBorder`), the tab bar draws eager/pinned/working/blocked/unseen, and the sidebar draws working/blocked/done/pinned — none of the three reads restore state or a CWD. A CWD *is* drawn outside the pane, by `renderStatusBar`, but only for `tab.ActivePaneModel()` of the active tab, which `paneIsVisible` already returns true for. So a pure-visibility gate would be correct as the code stands. The per-branch flags are deliberate conservatism: each fires once or twice per pane (free), and each is what a future indicator would read. An extra rebuild is invisible; a stale frame is a bug the user sees.

**INVARIANT: a branch added to `handlePaneOutput` that can move the screen MUST set `changedView`.** Nothing enforces it, and the failure mode is a stale frame rather than a crash.

`paneIsVisible` deliberately over-reports in four states that render less than it says — focus mode, an open dialog, the notes editor, and an overlay whose `overlayVisible` is false. All in the safe direction.

**The perf line's counters nest.** `skipped=` is every cache-served frame; `hidden=` is the subset attributable to off-screen output. `View`'s skip branch calls `recordSkippedView` then `recordHiddenSkip` (which bumps `viewHidden` ALONE) — attribution lives in `View` because `Update` only knows a skip was intended, and double-counting would break comparability with logs written before this existed.

**Honesty tests compare against a FORCED REBUILD of the model `Update` returned**, never against the frame from before the message: when the skip works those two are the same cached struct and the assertion is a tautology. That tautology is exactly how the ctxmenu case escaped the first audit. Tests in `view_coalesce_test.go`; a fresh pane must be PRIMED with one output chunk first, or the once-per-pane `liveOutputSeen` transition makes every case rebuild and the test proves nothing.

### Unfocused dim (`dim.go`)

`View()` runs the composed frame through `dimFrame` as its LAST step, immediately
before `tea.NewView(content)`, whenever `m.termFocused` is false and
`UIConfig.UnfocusedDimAmount()` is above 0. Every colour blends toward the terminal's
own background — reported by OSC 10/11 into `Model.termFg`/`termBg`, with a dark-theme
fallback.

**The config is TWO keys, and the split is not cosmetic.** `unfocused_dim_enabled`
(default true) is the off switch; `unfocused_dim` is the level. Folding "off" into
`0` — the original shape — means switching off has to WRITE 0 over the level, so
switching back on can only restore the default and a customised 0.35 does not
survive an off/on round trip. That is unnoticeable while the only way to change
either is to hand-edit `config.toml`, and unacceptable once a Settings row and a
palette command make the round trip one keystroke each. `UnfocusedDimLevel()` is the
clamped level IGNORING the switch (what the dialog and palette DISPLAY, so a
switched-off dim still shows what it will return to); `UnfocusedDimAmount()` is
`Level` gated on the switch (what the renderer blends with). Nothing but `Level`
may be shown to the user, and nothing but `Amount` may reach the blend.

**`unfocused_dim_enabled` MUST default true**, because it is absent from every
`config.toml` written before it existed and `Save` writes the whole struct. `Load`
starts from `Default()` and lets the decoder overwrite only the keys the file names,
which is the only reason the upgrade does not silently switch the dim off for every
install that has ever saved a config. A legacy `unfocused_dim = 0` still reads as off
through `Level`. Both pinned in `internal/config/unfocuseddim_test.go`.

**Both front doors act on the effective STATE, never on the flag.** `flag on, level 0`
is a real config on disk, so `toggleUnfocusedDim` (shared by the Settings row and
`palActDimToggle` — one implementation, because "switching on must also supply a
level" is a rule two copies drift apart on) tests `Amount() > 0` and, when switching
on, writes `DefaultUnfocusedDim` if the level is unusable. A naive `Enabled =
!Enabled` passes six of the eight toggle cases and is wrong exclusively for configs
that already exist. The Settings row's `get` reports `on`/`off` from `Amount()` for
the same reason the Desktop-notifications row reports registration state.

**The palette's level presets switch the dim ON as well as setting it**
(`setUnfocusedDimLevel`); the Settings level row deliberately does NOT. In the
palette a preset is the user's whole expressed intent and storing a level that never
renders is a command with no observable effect; in the dialog the toggle sits
directly above and owns the switch, so a level edit that flipped it would make the
dim impossible to keep off. `palActDimLevel` resolves its `arg` against
`dimLevelPresets` rather than parsing it — same rule as `palActSwitchProject`
resolving an ID. The "current" marker is gated on the dim actually being on, not on
the level matching, and is plain text rather than a glyph because that column
otherwise holds keybindings and is measured for width.

Neither row sets `relayout`: `dimFrame` rewrites SGR parameters only, so every cell
width is identical by construction and there is no geometry to recompute. Both apply
LIVE (next repaint) because `View()` reads the config every frame — the
`settingsFields` doc comment's "next launch" covers the rows read once at startup.

**The settable domain is bounded BELOW by the display resolution, not by
visibility.** `minDimLevel` = 10^-`dimLevelDecimals`, and `parseDimLevel` refuses
anything under it. Any level below 0.005 renders as `"0.00"` while the toggle row
still reads `on` — the amount is genuinely non-zero, so the state-based `get` cannot
catch it — and the row could never commit what it displayed, since typing back
`"0.00"` parses to 0 and is refused. It is reachable by TYPING, not just by
hand-editing config.toml: `ParseFloat` takes `"0.001"` and `"5e-324"`. Tying the
floor to the format's precision makes the accepted domain exactly the set of values
the row can display and the user can type back.

**The level row's unchanged-check compares FORMATTED values.** `get` renders the
clamped, rounded level while the stored field may hold neither, and
`handleSettingsKey` pre-fills the editor from `get` — so with a hand-edited
`unfocused_dim = 1.5` the row shows `0.90` and a RAW compare made Enter-Enter store
0.9 and set `configChanged`. `config.Save` writes the whole file, so merely
inspecting the row rewrote `config.toml` and any comments in it. Every other row in
that table is immune because its get/set round-trip is exact (`Itoa`, string); this
is the only one where it is not.

**`benchModelContent` builds its `Model` with `config.Default()`, and that is
load-bearing.** A `Model` literal's zero `Config` answers "off" to every
config-GATED renderer feature, silently — adding `UnfocusedDimEnabled` beside the
level `BenchmarkFrame_UnfocusedDim` already set made that benchmark measure an
UNDIMMED frame. Benchmarks assert nothing, so nothing failed; the dim simply read as
free. That benchmark now checks `UnfocusedDimAmount() > 0` and `b.Fatal`s, which is
what turns the next such regression from silence into a message. Expect the dim to
cost ~+93 allocs on a 41-tab frame; a delta near zero means the gate is off again.

**Why the composed frame and not the cells.** Chrome and pane content are already
flattened into one string at that point, so a single SGR rewrite dims both — the
~93 package-level `lipgloss.Color(...)` style vars need no palette indirection.
More importantly it sits **downstream of every render cache**: `viewCache` and the
per-pane render caches keep storing UNDIMMED content, and the pass rewrites
whatever they hand over. A cell-level design would have to invalidate every pane's
cache on each focus transition.

**The coalescing interaction is what makes that safe, and it is by omission.**
`tea.BlurMsg`, `tea.FocusMsg`, `tea.ForegroundColorMsg` and `tea.BackgroundColorMsg`
never set `skipRender`, so they fall through to the fail-safe default and the frame
rebuilds. Marking any of them inert — the obvious "this message changes no state"
optimisation — serves the cached frame at the wrong brightness, which is the same
class of bug as the ctxmenu case above and just as invisible in a test that
compares a cached frame against itself.

**INVARIANT: the pass may change colours and nothing else.** `renderTabBar`
measures `style.Render(name)` to hit-test clicks, and the cell-loop renderers
(`styledCellLine`, `insertCursor`) rebuild a row column by column, so a rewrite that
altered any line's rendered width would desync both from what is drawn. Only SGR
*parameters* are rewritten, which preserves width by construction;
`TestDimFrame_PreservesRenderedWidthOfEveryLine` is what keeps it true.

The software caret comes out right for a reason worth recording, since it looks like
it should not: `insertCursor` emits `\x1b[0m\x1b[7m` + glyph + `\x1b[27m`. The `0m`
re-arms `fgIsDefault`/`named`, so the stand-in lands between the `7m` and the glyph
and the caret block fades with everything else, and the `27m` leaves the stand-in in
effect for the cells after it — which is correct, because those cells were
default-foreground in the undimmed rendering too.

**38, 48 and 58 are the complete set of parameter-consuming SGR codes**, and
`dimSGRParams` must consume all three. A code left unconsumed does not merely go
undimmed — its sub-parameters fall back into the top-level loop and are read as
SGR codes, so `58;2;0;255;0` became faint + reset + reset and the reset clobbered
the active foreground. Where the stray index instead lands in 30-37/40-47/90-97/
100-107 the failure INVERTS — the run sets an explicit foreground, the stand-in is
suppressed, and following text stays at full brightness — so a regression row must
pick an index that collides (`58;5;31`); `58;5;9` passes while the bug is present.
Shipped broken and caught in review. Producers are Neovim and helix LSP diagnostics
and anything else setting a coloured underline — NOT every colour-using tool, since
`rg --color` and bat emit 38/48 only and checking those first reads as a false alarm.
Anything
unparseable is copied verbatim AND counted as an explicit foreground, so a miss
stays a miss rather than becoming corruption.

**The no-injection property is a checked invariant, not an assumption.** `sgrParams`
refuses any CSI body carrying a byte outside 0x20-0x3F. `ansi.DecodeSequence` cannot
currently return one, which is precisely why the check is there: without it, a future
parser that returned the raw span of a malformed CSI would silently turn
`dimSGRParams`' verbatim field copies into an injection primitive, and no test in the
package would notice. A refused sequence goes undimmed.

**Palette colours are resolved against the xterm defaults, and there is no fix
available.** `dimBasic`/`dimExtended` map indices through `ansi.BasicColor`/
`IndexedColor`, so a retheming terminal's `\x1b[31m` blends from `#800000` rather than
from what the user actually sees, and blur shifts hue slightly instead of only fading.
OSC 10/11 covers the default fg/bg; there is no palette-query Cmd in bubbletea v2, so
honouring the real 16/256 entries needs hand-rolled OSC 4. Documented in
`docs/configuration.md` as a known limitation rather than left looking accidental.

Rewrites are memoised per parameter run for the frame — a pane paints most cells in
the same few colours, worth ~860 allocations on a 41-tab frame. `BenchmarkFrame_UnfocusedDim`
measures it against the `WarmPane` baseline; compare allocation counts, not ns/op,
which is too noisy at 41 tabs to read.

### Force redraw

`redraw` keybinding (default `alt+shift+l`) emits `tea.ClearScreen` + `tea.RequestWindowSize` — recovery hatch for accumulated cell-diff drift AND a missed `WindowSizeMsg` (conhost drops resize events on maximize/restore; ClearScreen alone would repaint the same stale-size frame). Listed in the F1 shortcuts dialog; exempt in notes mode via `notesKeyExempt`

### Frame assembly (`joinfast.go`)

`View()` joins the tab bar, the pane area and the sidebar with
`joinVerticalWidth` / `joinHorizontalWidth` rather than lipgloss's joins.
Measured 2026-08-20 on a 41-tab / 200x50 frame with realistic pane content:
`ansi.stringWidth` is **54.7%** of a frame whose pane caches are all warm, and
97% of that arrives through lipgloss's own join internals — `getLines` 44.3%,
`JoinVertical` 35.7%, `JoinHorizontal` 17.1%, and only **2.9%** through our own
`lipgloss.Width` calls. Memoising those call sites was measured and **rejected**:
it could reach ~1.6% of a frame.

What the joins spend it on is the part worth knowing: every block View()
assembles is ALREADY rectangular, because each comes from a lipgloss style with
an explicit width (tab bar 1 line of 178, tab content 48 lines all 178, sidebar
49 lines all 22). The joins walk ~50 lines of grapheme clusters per frame to
discover they need to pad nothing.

benchstat n=6, p=0.002: **-34% to -35%** on a real repaint, **-39% to -56%** with
the pane caches warm, **-42.7% geomean**, and -31.8% bytes.

**Both helpers check their assumption and fall back to lipgloss when the check
fails — but the check is a SAMPLE, not a proof.** `blockIsWidth` measures the
FIRST and LAST line only, because an all-lines check costs exactly what the
optimisation saves. A block that is the declared width at both sampled lines but
ragged in its INTERIOR takes the fast path and DISAGREES with lipgloss: the short
line goes unpadded vertically, and horizontally the right column shifts left on
that row. So the guarantee is not "never wrong" — it is "wrong only for a block
nothing in the frame produces".

That premise is the load-bearing part, and
`TestFrameBlocks_AreRectangularAcrossGeometries` is what defends it, across the
same three tab counts and three geometries the equivalence test uses. If a future
renderer emits a ragged interior, THAT is the test that should fail.
`TestBlockIsWidth_OnlyChecksFirstAndLastLine` asserts the divergence explicitly
rather than describing it, and the equivalence tables include the shape that
masks an inverted check (both blocks matching on one sampled line only) — without
it, inverting either comparison survived the whole file while producing unpadded
output.

**lipgloss remains the width AUTHORITY.** These helpers never compute a width by
another route; they only skip measurement they can prove is unnecessary. If an
equivalence test fails, DELETE the helper rather than adjusting the test.

## Navigation and selection

### Spatial pane navigation

`internal/tui/tab.go` — `TabModel.NavigateDirection(dir Direction)` walks `CollectRects` (top-down geometry), filters by half-plane (`directionScore`), and picks the candidate with three tie-breakers: smallest gap, largest perpendicular overlap, smallest perpendicular center distance (tmux/vim parity). Default keys are `Alt+Left/Right/Up/Down` (`pane_left/right/up/down` in `[keybindings]`). Tab/Shift+Tab and `Alt+H/V` are deliberately NOT bound at the global level — they fall through to the PTY (shell completion, Claude Code mode toggle, claude-code's image paste). Splits live on `Alt+Shift+H/V`. Disabled in focus mode and on single-pane tabs (no-op). Vim users can rebind to `alt+h/l/k/j` in `config.toml`. Tests in `layout_test.go` cover all four directions, the no-overlap rejection branch, and the center-distance tie-breaker

### Text selection

`internal/tui/selection.go` — keyboard (Shift+Arrow, Ctrl+Shift+Arrow word jump, Ctrl+Alt+Shift+Arrow 3-word jump) and mouse (click+drag). Enter copies selection to clipboard via `internal/clipboard`. Shell cursor follows selection horizontally in real-time (same-line only; cross-line is visual-only to avoid triggering command history). Selection bounded by `lastContentLine()` — won't extend into empty terminal area

### Clipboard

`internal/clipboard/` — platform-native Read/Write. Windows: Win32 `GetClipboardData`/`SetClipboardData`. Unix: `pbpaste`/`pbcopy` (macOS), `xclip`/`xsel` (Linux). Paste (`Ctrl+V`) wraps content in bracketed paste sequences. Dialog paste sanitizes control characters.

**Image paste proxy**: `clipboard.ReadImage()` reads `CF_DIBV5`/`CF_DIB` on Windows (Unix is a stub), `dib.go` parses the DIB into an `image.Image` (24bpp BI_RGB, 32bpp BI_RGB and BI_BITFIELDS, top-down + bottom-up, all-zero-alpha promotion). `pasteClipboard` falls through to image when text is empty: saves PNG to `config.PasteDir()` (`~/.quil/paste/quil-paste-<timestamp>.png`) and types the path into the PTY. Works around the upstream Claude Code Windows clipboard bug (anthropics/claude-code#32791). Paste keys: `Ctrl+V` (kb.Paste — eaten by Windows Terminal), `Ctrl+Alt+V` and `F8` are hardcoded aliases; `F8` is the recommended Windows trigger because it has no AltGr ambiguity


