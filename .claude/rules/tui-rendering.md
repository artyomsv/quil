---
description: Pane rendering, tab bar, mouse handling, selection, scrollback, and the keybinding action registry. Load when touching pane/tab rendering, mouse routing, the selection layer, or internal/keymap.
paths:
  - "**/internal/tui/pane*.go"
  - "**/internal/tui/dim*.go"
  - "**/internal/tui/tab.go"
  - "**/internal/tui/layout.go"
  - "**/internal/tui/arrange*.go"
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
  - "**/internal/tui/perf*.go"
  - "**/internal/tui/frame_*_test.go"
  - "**/internal/tui/layoutsync*.go"
  - "**/internal/tui/layout_sync_test.go"
  - "**/internal/tui/typing_guard_test.go"
  - "**/internal/tui/multiclient_role_test.go"
  - "**/internal/clipboard/**"
---

# TUI Rendering

Extracted verbatim from `.claude/CLAUDE.md`. Loaded only when the files above are in play.

## Tab bar and keybindings

### Tab bar

tabs show 1-based index prefix (`1:Shell`, `2:Build`) matching Alt+1-9 shortcuts. Index hidden during rename editing. The active tab is also prefixed with `* ` (rendered through the `tabLabel(idx)` helper shared by `renderTabBar` and `hitTestTab` so click coords align with the rendered widths). Live rename emits `tea.ClearScreen` on every keypress so width changes don't leave stale glyphs from the previous-shorter render (Bubble Tea v2 cell-diff occasionally misses width shifts mid-bar — same "width changes — force full redraw" pattern used in dialogs). Mouse: click-and-drag a tab reorders it (slide semantics — intermediate tabs shift one slot at a time, dragged tab follows the cursor). The drag tracker (`Model.tabDragFromIdx`, init `-1`) is primed on click and consumed on motion at Y=0; each move fires `MsgReorderTab{TabID, NewIndex}` so the daemon's `SessionManager.tabOrder` stays authoritative and the next `workspace_state` broadcast is a no-op reconciliation. `SessionManager.ReorderTab` clamps NewIndex to bounds, so a stale TUI never has to race for an accurate tab count. **The move waits for the pointer to cross the hovered tab's MIDPOINT** (`dragSlot`, `internal/tui/reorder.go`), never first contact: tabs differ in width, so moving on contact swapped the dragged tab with a neighbour of a different size, put that neighbour under the pointer, and the next motion event swapped them back — a narrow tab over a wide one flip-flopped on every event, a wide tab over narrow ones flew several slots per cell. **`tabSpans()` is the SINGLE tab-bar geometry**: `renderTabBar` joins the spans it returns, `hitTestTab` delegates to `tabSpanAt`, and the drag reads the same starts and widths — so the painted bar and the click map cannot drift, because there is nothing left to drift from. Carrying the rendered text on the span costs nothing, since the width already comes from `lipgloss.Width(style.Render(label))` and the string was previously discarded. **A test that compares `hitTestTab` with `tabSpans` is therefore TAUTOLOGICAL** — it compares the geometry with itself; the load-bearing one drives the OVERFLOW branch and compares the spans against the PAINTED row (`TestTabSpansMatchThePaintedBarWhenTheBarOverflows`), and its fixture must paint at least TWO tabs or the separator arithmetic is never executed (at 40 columns the reserve leaves room for the active tab alone, and a separator mutation survives). The same `dragSlot` drives the sidebar drags measured in ROWS: a tab heading (`sidebarRowTab`; every row of a tab's group is marked `inTab`) reorders tabs via the same `MsgReorderTab`, and a project row reorders projects via `MsgReorderProject` carrying the project's index AMONG ITS OWN DAEMON'S projects (`sendReorderProject`), sent only when that index actually changed and only within the project's sidebar section (`moveProjectWithinSection`, see `projects.md`'s Project groups) — the sidebar interleaves several daemons' projects (`mergeProjects`) and a daemon's `projectOrder` holds only its own, so the cross-daemon interleaving is client-side state and is not persisted. **Both sides of that count use POINTER identity, never the project ID**: an ID is `"proj-"` plus eight hex digits of a UUID minted independently by each daemon (`daemon/project.go`), so two daemons in one sidebar can hand out the same one — `indexOfProject` returns the first match, which would move focus, and every action after it, to the other daemon's project. `moveProject` only permutes the existing slice, so the pointer is guaranteed to still be in it; a lookup MISS is refused rather than sent, because falling off the end leaves an index one past that daemon's last project, which the daemon clamps and acts on. **One motion event builds the sidebar ONCE** (`sidebarDragRows` hands back the row slice with the resolved row, and the span helpers are pure over it): the first version called `sidebarRowAt` and then a span helper, each of which built ~70 lipgloss-styled rows, twice per event, on the goroutine that also forwards keystrokes, for as long as the button was held. Keyboard equivalents: `tab.move_left/right` (Alt+Shift+PgUp/PgDn) and `project.move_up/down` (Alt+Shift+Up/Down); none has a legacy `[keybindings]` field (`promotedActions` in `keyspecs_test.go`)

**Manual scroll offset and two-sided markers.** The wheel over the tab bar (`Model.scrollTabBar`, `internal/tui/reorder.go`) moves the visible window without switching tabs or reaching a pane, via `Model.tabScrollFirst` (the first visible tab's index) and `Model.tabScrollAnchor` (the active tab's ID at the moment the scroll was set). **Manual mode is in effect only while the anchor still names the CURRENT active tab** (`Model.tabBarManualMode`) — any tab switch that does not also re-arm the anchor (every path except a click on a still-visible tab while scrolled) therefore returns the bar to auto-centering. **The compare alone is only a TEMPORARY disable, not a reset, and treating it as one was a bug**: nothing ever cleared the anchor itself, so switching away and back to the SAME tab by any non-click means (`switchTabBy` there and back, a project switch and back, an MCP jump) made the compare true again and resurrected the stale window — sometimes hiding the tab that had just become active. `Model.normalizeTabScrollAnchor` (`reorder.go`) is the actual reset: it clears the anchor once it no longer names the active tab, called from a `defer` on `Model.Update`'s NAMED return (`retModel`/`retCmd`) rather than sprinkled into every switch path — Update has dozens of return statements and no other point they all pass through, and a per-path reset is one a future switch path forgets. The click-while-scrolled path re-arms the anchor to the newly active tab BEFORE Update returns, so it still matches when the defer runs and survives unchanged; wheel-scroll anchors to the CURRENT (unchanged) active tab for the same reason. `tabSpans()` is now a thin wrapper over `tabBarLayout()` (spans plus `hiddenLeft`/`hiddenRight`), the single geometry manual and auto mode both go through — `renderTabBar` reads the counts off the SAME call it paints from, so the markers can never disagree with what is actually shown. The single right-side `«N more»` indicator is now TWO markers, `«N ` (left) and ` N»` (right), each shown only when tabs are hidden on that side; auto mode's expansion reserve is their combined WORST-CASE width (`N = n-1` on each, via `leftTabMarker`/`rightTabMarker` applied to `n-1`), replacing the old fixed `12` — but that worst case is a BUDGET number, never a position. Because the left marker occupies columns before the first tab, `span.start` includes its width, and `renderTabBar` paints the left marker FIRST — reversing that order desyncs the painted column from `hitTestTab`/`tabSpanAt`, which is why `TestTabSpansMatchThePaintedBar_WithLeftMarkerVisible` scrolls its fixture rather than only testing the unscrolled overflow case. **The width `span.start` adds is `leftTabMarker(firstIdx)`'s REAL width, not `tabBarWidths`' worst-case `leftMarkerW`** — a regression found by review: with 11+ tabs and 1-9 actually hidden on the left, `n-1` has two digits (`leftMarkerW` is sized for `"«11 "`) while the real marker (`"«3 "`) is one cell narrower, and reserving the wider one for a POSITION shifted every visible tab one cell left of where its own span said it started. `TestTabSpansMatchThePaintedBar_WorstCaseLeftMarkerWidthNeverShiftsPositions` pins it with a 12-tab fixture (the 5- and 8-tab siblings can't reach it, since single-digit `n-1` never diverges from a single-digit real count). The right marker has the same real-vs-worst-case split for consistency, but no position depends on its width — it is painted last. **Painted-column tests must compare CELL columns, not byte offsets, against the span's OWN rendered text.** `strings.Index` returns a byte offset, and the marker glyphs `«`/`»` are multi-byte in UTF-8, so a byte offset into an already-ANSI-stripped row still has to be converted to a cell count via `ansi.StringWidth` on the byte-offset prefix (`cellIndexOf` in `reorder_test.go`) before comparing it to `span.start`. The search target matters just as much: it must be `stripANSI(s.text)` — the span's own styled-and-padded text — never a hand-reconstructed `"N:name"` guess, which omits the tab style's leading pad cell (`.Padding(0,1)`) and is therefore off by exactly one cell from `span.start` regardless of the marker fix above; the OLD tests tolerated that one-cell gap by checking membership in `[s.start, s.start+s.width)` rather than exact equality, which is also why they could not have caught the marker-width regression either. `maxFirstIndex` (the smallest first-visible index whose tail already fits the bar) is the ONE clamp both manual layout and the wheel handler use — the wheel re-clamps the STORED value against it BEFORE adding the notch, so a tab closed or a resize that shrinks it can't leave the wheel stuck past the end.

**A click that re-arms the anchor also has to re-verify visibility.** The active tab's `"* "` prefix costs two cells the instant it becomes active, so clicking a tab sitting at the very end of the scrolled window can grow it right off the bar the same click just kept in view. `ensureTabVisibleInScrollWindow` (`reorder.go`) nudges `tabScrollFirst` forward one tab at a time — re-deriving `maxFirst` AFTER the click's `switchTab` already ran, so it reflects the clicked tab's NEW (wider) width — until the tab is visible, or falls back to auto mode if even `maxFirst` can't show it (auto mode's unconditional inclusion of the active tab is then the stronger guarantee). Starting an inline rename (`beginTabRename`) clears the anchor outright for the same reason a tab switch does: typing into a tab scrolled out of view is not observable, and `tabBarManualMode`'s anchor compare is what makes clearing it enough.

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

`Model.clearDragState()` zeros every mutually-exclusive drag flag (`tabDragFromIdx`, `projectDragging`/`projectDragKey`/`projectDragMoved`/`projectDragPressY`/`projectDrop`, `sidebarTabDragging`/`sidebarTabDragIdx`, `scrollDragPaneID`/`scrollDragRect`, `mouseDown`, `notesMouseDown`, `paneDrag`, `groupDragging`/`groupDragIdx`/`groupDragMoved`) in one place. The sidebar drags are a bool beside an index (or, for a project, a `(Dest, ID)` key) rather than a `-1` sentinel, so a `Model` built directly by a test — the zero value — reads as "no drag" without a constructor having to seed it. Every "start a new drag" path in `MouseClickMsg` and the "drag ended" branches in `MouseReleaseMsg` route through this helper instead of zeroing siblings inline, so a future drag mode can be added by extending the helper rather than auditing each click handler. `TestModel_ClearDragState` guards the invariant

### Split-border drag-resize

`CollectBorders`/`BorderHit` (`internal/tui/layout.go`) enumerate split lines (hit zone per line = the two drawn border glyphs + `splitBorderHitPadding` widening — symmetric for V-split rows, right-only for H-split columns so the zone never reaches the left neighbour's drawn scrollbar column at bd-2; reverse scan = deepest node wins at T-junctions). The border check runs BEFORE the scrollbar check in `MouseClickMsg` — the drawn split line always arms the drag (a scrollbar-first order silently ate the left glyph via scrollbar padding, with zero feedback on panes without scrollback), while thumb clicks on scrollbarX keep working because the border zone stops at bd-1; `hitTestSplitBorder`/`dragSplitBorder`/`finishSplitDrag` (`model.go`) arm/move/commit the drag — the ratio is clamped in cells against subtree minimums (`minWidth`/`minHeight`: leaves are 10×4, H-splits sum widths, V-splits sum heights) then derived, so boundaries are exact; PTY resize + layout persistence are deferred to mouse release (`resizeAllPanes` + `markLayoutChanged` for the one tab that moved — the on-release-only design avoids mid-drag PTY churn). Mid-drag only `Ratio` + pane RECTS move (`resizeNodeRects` in layout.go) — the VT emulator must NOT resize mid-drag: `ResizeVT`'s contract pairs every emulator resize with a PTY redraw, so unpaired intermediate-width rewraps permanently garble content at the narrowest width crossed (2026-07-15 corruption bug); the single VT+PTY resize pair fires together in `finishSplitDrag`. Panes whose rect touches the dragged line get a transient `splitDragHighlight` border (color 39, included in `renderKey`), set/cleared via `setSplitDragHighlight`. Disabled in focus mode, notes mode, and single-pane tabs; scrollbar hit test keeps priority. Drag state (`splitDragNode`/`splitDragRect`) rides `clearDragState()`; a node pruned mid-drag (workspace reconciliation) drops the drag via `treeContains`. Tests in `splitdrag_test.go`

### Pane drag (Alt+drag)

`Alt` + left press inside a pane arms a drag of the WHOLE pane (`internal/tui/panedrag.go`, `Model.paneDrag paneDragState`, zero value = none). The arm is the first thing in `MouseClickMsg`'s pane-area arm — ahead of the notes-editor click, `hitTestSplitBorder`, `hitTestScrollbar` and selection arming — so an Alt+press never starts any of those, and one that cannot arm is swallowed rather than falling through — a `tabLayoutBusy` tab also flashes `tabBusyFlash`, like the menu, palette and key paths. Not in notes mode. The chord is `paneDragModifier` and nothing else; Windows Terminal was verified to deliver Alt with the press while Quil tracks the mouse (a `logger.Debug("mouse click: …")` trace of every modified click is how). `Ctrl`+click stays swallowed at the top of the handler.

**IDs, not pointers** (`srcPaneID`, `srcTabID`, `targetPaneID`, `overTabID`): a broadcast can rebuild the tree under an armed drag. `paneDragIntact` — the source is still a leaf of the tab it started in, that tab is still active, no overlay, not notes mode — is re-checked on every motion, on release and by the preview. A source a broadcast moved or destroyed CANCELS the drag; without it, a tab-bar drop would move a pane out of a tab it is no longer in.

Motion only moves the target. Over another pane, `dropZoneAt` picks Left/Right/Top/Bottom from the outer quarter (`dropEdgeFraction`) measured from the cell's centre as a fraction of that dimension (the nearer edge in a corner, a tie to Left/Right) or Center. On row 0, `hitTestTab` picks a tab, kept only when it is in `movePaneCandidates(src)`. Mid-drag NOTHING resizes — the split-border rule. `View` composites the preview on `paneArea` after the sidebar rule: a heavy-line outline (`overlayOutline`, four `overlayAt` strips, so the content inside stays visible) around `dropPreviewRect` — the half of the target the pane will take, or the whole target for a swap — or the hovered tab re-rendered reversed in place.

Release re-tracks at the release cell (a release with no motion still resolves, and a target that went ineligible is dropped), clears the drag, then: a tab → `sendMovePane` (the daemon moves it; the spiral placement lands it); an edge → `moveLeafBeside`; the centre → `swapLeaves`, both through `applyTabArrangement` with the dragged pane active. Esc (`handleKey`, right after the context-menu arm) cancels, as does releasing on the source, its own tab, or anything else. Tests: `panedrag_test.go`.

### Panes moved between tabs

A pane the daemon moved (`move_pane`) reaches the TUI only as a broadcast listing it under its new tab; there is no optimistic move and no mover-side pre-split. `rebuildTabs` reconciles it in the same pass as every other arrival, in `internal/tui/model.go`.

**Detection is `existingPanes` reuse** (`migrated := ok` in the add loop). A hit there can only be a pane in ANOTHER tab's tree: panes already in this tree take the `treePaneIDs` branch, overlays never reach the loop, and the worktree-held pane is skipped before it. Every attached client of a daemon holds every tab of that daemon, so every client observes the same reuse — which is what lets a CLIENT-side rule stay in agreement across clients. A mover-only pre-split is still rejected, but not for the reason it once was: layout sync (below) now lets every OTHER client adopt the daemon's stored tree by revision, so two clients no longer re-send each other's trees on disagreement. What survives is the arrival race a revision cannot arbitrate — the requesting client's own `pendingSplit` reservation is placed LOCALLY, before `create_pane`/`MovePane` ever reaches the daemon, so it carries no revision of its own and a bystander's broadcast can still land inside that window. See "A migrated pane never fills a reservation" below for the guard that covers exactly that race.

**Placement: a spiral ("dwindle") into the last pane.** `placeArrivingPane` splits the LAST pane leaf in tree order (`spiralLeaf` — a descent preferring Right, skipping placeholder leaves) AGAINST its parent's direction (`spiralSplitDir`: parent left|right → top|bottom, parent top|bottom → left|right, a root leaf → left|right), and installs the pane in the right/bottom half at Ratio 0.5. Successive arrivals therefore spiral into the bottom-right corner — `A` → `A|new`, `A|B` → `A|(B/new)`, `A|(B/C)` → `A|(B/(C|new))`, `(A/B)` → `(A/(B|new))`. It replaced a largest-leaf rule that turned `p3|p4` into three thin columns. Both halves of the choice are properties of the tree alone, so equal trees choose the same leaf and direction whatever each client's window size. `arrivalSplitDir(pref, w, h)` then flips the direction only when the preferred one would leave a half under `minPaneW` (left|right) or `minPaneH` (top|bottom) AND the other one fits; neither fitting, or unknown geometry, keeps `pref`. The rect comes from the canonical `paneAreaWidth() × (height - chromeHeight)`, not the notes-squeezed width. The only client-dependent input is that fallback, which needs one client's leaf below the minimum and another's not. **"Equal trees" does not hold for a client with its own reservation** — its tab carries an extra placeholder split (`pendingSplit`) that no other client's copy has, since placeholders are pure client-local runtime state and are never broadcast, so THAT client's walk can choose a different leaf than everyone else (the spiral skips the placeholder, which may be the last leaf); see "A migrated pane never fills a reservation" below for why the divergence is contained rather than a thrash.

**`splitForNewPane` is deliberately unchanged** (first leaf, top|bottom). It also places a pane created by MCP `create_pane`, another client's `Alt+Shift+H/V` split as observers see it, and every pane of a layout-less tab on first attach (the `rebuildTabs` fallback and `restoreTabLayout`'s missing-pane loop). Changing it would re-lay-out all of those for nobody's request, and `canvas_test.go` relies on its shape. Only `migrated` panes take the new rule; `TestNewPaneFromElsewhere_StillUsesLegacyPlacement` pins the split.

**A migrated pane never fills a reservation** — any `pendingSplit` placeholder: an ordinary split, a replace, or a worktree create. Filling it would steal the tab's `ActivePane` and leave the pane this client actually asked for, which still arrives, with no leaf; for a worktree create it would also retire `worktreeCreates` and dispose `worktreeReplaced`. The two-client repro: client B splits tab T, and before B's own pane arrives a broadcast reports a pane another client moved into T. The daemon cannot catch this — the requesting client reserves its placeholder before `create_pane` reaches the daemon — so this guard is the ONLY protection. The moved pane is placed by the spiral rule instead, which skips placeholder leaves. Because an ordinary reservation has no prune exemption of its own (only a worktree create sets `CreatingBranch`), a pass in which a moved pane skipped one also spares the placeholder prune (`sparedReservation`) — otherwise the same broadcast would detach it while `pendingSplit` still pointed at it; the requested pane normally rides the next broadcast, and if it never comes the next pass prunes as before. A bare-root reservation (a replace on a single-pane tab) is kept as the left child of a new left|right split rather than overwritten. `TestMovedPane_NeverFillsAnOrdinaryPendingSplit` and the worktree variants pin it.

**`adoptMovedPane`** runs on the fallback branch that installed a migrated pane (the only branch one can take), EXCEPT when the target tab is THIS client's own active tab right now: it exits the target's focus mode (so the new split is what the tab shows) and makes the pane the target's `ActivePane`; `finalizeTabPanes` then rewrites the `Active` flags. The user stays in the source tab — the daemon never changes the source project's `ActiveTab` for a pane move, and a dissolved source resolves to the daemon's successor through `indexOfTab`.

**The `tab != m.activeTabModel()` guard exists because every attached client reconciles the same broadcast, and adopting is a claim about what THIS client is looking at, not about the pane that moved.** A second client sitting in the target tab — typing into some other pane there when a THIRD client's move lands — is not the mover and never asked to look at the arriving pane; adopting anyway would steal both `ActivePane` and focus mode out from under it mid-keystroke. The mover's own client never has the target tab active (Enter in the tab picker never switches tabs — `tabpicker.go`), so the guard only ever changes behaviour for a bystander. (The `pendingSplit` fill branch no longer adopts at all: a migrated pane never reaches it.)

**Source focus mode exits only when the focused pane is still in the broadcast.** `RemovePane` hands `ActivePane` to `leaves[0]` and leaves focus mode on, so a focused pane moving out of a 3-pane tab would leave it full-screening a pane nobody chose. The prune loop exits focus first when the pruned id is in `paneMap` (moved) and was the focused active pane; a DESTROYED pane is absent from `paneMap` and keeps the old behaviour (`TestDestroyedPane_KeepsFocusModeBehaviour`).

**Notes: "the bound pane is in the active tab".** `switchTab` and `switchProject` keep that invariant by exiting notes first; a broadcast can break it, either by moving the bound pane or by moving this client's active tab under an open editor (MCP `switch_tab`). `applyWorkspaceState`'s notes reconciliation exits notes when the bound pane's tab is not the active one, BEFORE the older arm that forces a tab's `ActivePane` back to the bound pane — that arm would otherwise re-focus a background tab while the editor sat beside the active tab's pane, writing another pane's notes.

**The target preceding the source in rebuild order is harmless.** The same `*PaneModel` sits in two trees until the source prunes it; nothing between the two rebuilds walks trees by pane id, and the dispose sweep runs after every project has rebuilt. Pinned by `TestMovedPane_TargetBeforeSourceInRebuildOrder`.

**Caveat: a client attaching inside the window** — after the move, before any client re-sent the target's layout — restores from a stored tree that lacks the pane and places it with `splitForNewPane`. The window is one round trip; accepted. Tests in `move_pane_apply_test.go`.

### Tab layout arrange

`internal/tui/arrange.go` is PURE: `evenOut`, `columnsLayout`, `rowsLayout`, `gridLayout`, `mainStackLayout`, `spiralLayout` (dispatched by `arrangeLayout(kind, root, active)`), `moveLeafBeside`, `swapLeaves`, `fitsMinSize`. Each returns a NEW tree over the same `*PaneModel` leaves and never mutates its input (`cloneLayout`; `RemoveLeaf` mutates in place, so the drag edits run on the clone) — the apply step may still refuse, and a refusal must leave the tab untouched. Presets read `Leaves()` order; main + stack takes the tab's active pane out of it; spiral reuses `spiralLeaf`/`spiralSplitDir` and then evens out.

**`fitsMinSize` is NOT built on `CollectRects`.** `CollectRects` clamps every child up to `minPaneW`/`minPaneH`, so each rect it returns reads as large enough while together they overflow the tab — five columns in 40 cells come back as five 10-wide rects. `fitsMinSize` repeats the same `int(float64(w)*Ratio)` arithmetic without the clamps; wherever no clamp fires the two agree cell for cell (`TestFitsMinSize_AgreesWithCollectRectsWhenItFits`). Even out goes through it too: equal AREAS are not a minimum height (two panes over three in 8 rows gives the top row 3).

`applyTabArrangement(tab, root, active)` (`arrange_apply.go`) is the ONE apply step for the six menu/palette/key actions and both in-tab drops. In order: notes mode → silent no-op; `tabLayoutBusy` → flash `Tab is busy — try again in a moment`; fewer than two panes → no-op; `fitsMinSize` against the CANONICAL geometry (`paneAreaWidth()` × `height-chromeHeight` — never the notes-squeezed width, and the menu may be arranging a BACKGROUND tab) → flash `Not enough room for that layout`; then the tree, `invalidateLeaves`, `ActivePane` plus every `Active` flag in the tab, `ExitFocus`, `SetCanvas`/`SetChrome`/`Resize`, and `tea.Batch(resizeAllPanes(), sendTabLayout(tab))`. `sendTabLayout` marshals on the Update goroutine and ships ONE `MsgUpdateLayout` for that tab through `sendDiffedLayouts`.

**`tabLayoutBusy` = `tabInFlight` + this client's own `pendingSplit` reservation.** `tabInFlight` deliberately did not grow the reservation: it also gates Move to tab…, which has its own rule for reservations (a moved pane never fills one). For an arrangement the reservation is the dangerous case — the new tree is a copy, so `pendingSplit` would be left pointing at a placeholder no tree holds. **Fixed by layout sync (below):** `sendTabLayout` now ships `MsgUpdateLayout{BaseRev: &tab.layoutRev}`, and every OTHER attached TUI adopts the daemon's higher-revision tree in `syncTabLayout`/`adoptTabLayout` instead of re-sending its own. Before that, other attached TUIs kept their own tree for the tab indefinitely — existing tabs never adopted the stored layout — so a second TUI's next broadcast disagreed with its OWN tree and RE-SENT it, overwriting the arrangement; the layout a restart restored was whichever client had sent last. Tests: `arrange_test.go`, `arrange_apply_test.go`, `layout_sync_test.go`.

## Multi-client

Several TUIs can attach to one daemon and share its workspace. `Model.clientID`
(minted once per process, sent on every attach) and, per destination,
`Model.sizeMaster[dest]`/`Model.clientCount[dest]` (kept from each broadcast's
`SizeMaster`/`Clients` fields, `internal/tui/model.go`) are what a client uses
to tell whether it is the size master. `isFollower(dest)` is
`sizeMaster[dest] != "" && sizeMaster[dest] != m.clientID` — the zero value (no
entry yet) answers false, so a client that has not heard from a destination
behaves as it always did. See `.claude/rules/daemon-lifecycle.md`'s
"Multi-client" section for the daemon-side registry and election this reads.

### The resize gates and batching

**A follower sends no `MsgResizePane`/`MsgResizePanes`.** The three resize
producers named by the `terminalPaintable` invariant in `.claude/CLAUDE.md` —
`resizeAllPanes`, `diffResizes`, `overlayResizeCmd` — each gate on
`isFollower(dest)` in addition to `terminalPaintable()`; the gate sits at the
same three fan-outs because those are the only three producers. A follower's
`sizedOnce` is deliberately NOT marked, so if this client later becomes master
the pane still gets its first-resize kick rather than reading as
already-sized for a size it never sent. A local rect change that never reaches
the daemon — entering focus mode, opening notes, toggling the notification
sidebar — resizes nothing on a follower for the same reason it resizes nothing
today: none of those three producers fires for it, follower or not.

**Becoming master clears `sizedOnce` for that destination rather than calling
`resizeAllPanes` directly** (`applyWorkspaceState`, guarded on
`state.SizeMaster == m.clientID && prevMaster != state.SizeMaster`). Every
pane's last-applied size was sent by whoever was master before (or by
nobody), so `sizedOnce` still reads "already sized" for sizes this client
never sent — `clearSizedOnceForDest` clears that, and `diffResizes` (which
Update runs immediately afterward, scoped to the same `dest`) picks up every
pane in ONE batch, the same re-arm `armReattachReset` performs after a
reattach. Calling `resizeAllPanes()` here as well would walk every OTHER
destination too, resizing panes this broadcast never mentioned.

**Batching, daemon and client.** A window resize or a split-drag release
sends `MsgResizePanes` (one frame for the whole destination) instead of one
`MsgResizePane` per pane; `sendDiffedResizes` and `resizeAllPanes` both build
one batch per dest. The daemon answers with at most one `pane_sizes` frame per
applied batch to each follower (`applyResizes`/`sendPaneSizes`,
`internal/daemon/daemon.go`), never one per pane — see the daemon-lifecycle
note for why the frame goes out before `pty.Resize` runs. Every pane in a
`pane_sizes` frame, and every `PaneInfo` in a workspace-state broadcast,
carries `size_seq`; `PaneModel.adoptDaemonSize` adopts only `seq >=
daemonSizeSeq`, so a workspace-state broadcast that raced a `pane_sizes` frame
from the same resize cannot undo it. A new TUI against an OLDER daemon (dev
builds only; release builds are version-gated) sends only `resize_panes`,
which that daemon drops as an unknown type, so its panes are never resized.

### Follower rendering

**Grid size.** `PaneModel.targetVTSize` (`internal/tui/pane.go`) is the single
decision point for a pane's EMULATOR size: a follower pane with a known
daemon size (`p.follower && p.daemonCols > 0 && p.daemonRows > 0`) takes that
size regardless of the box this client draws it in, at every site that sizes
a VT — `TabModel.Resize`/`resizeNode` for layout leaves, `sizePaneFull` for
focus mode and for overlay panes (lazygit), which sit outside `Leaves()`.
Everyone else, and a follower pane with no daemon size yet (never sized, or
pending — it falls back to `paneVTSize(rect)` for drawing and sends nothing),
uses `paneVTSize`. Resizing a follower's VT to its own box instead would
rewrap the master's output with no PTY redraw to pair it — the unpaired-resize
corruption `ResizeVT`'s contract forbids for split drags applies here too.
`p.follower` and `p.daemonCols`/`p.daemonRows` reach `PaneModel` through
`syncPaneMeta` (`internal/tui/workstate.go`), the path that already copies
every other daemon-derived field onto pane models; `adoptDaemonSize` is the
seq-gated write into `daemonCols`/`daemonRows` described above.

**Viewport (`pane_preview.go`).** A follower pane reuses the wide-canvas
preview renderer instead of a second one. `previewMode()` is true for a
follower whose grid exceeds its box in EITHER dimension (`innerW <
vt.Width() || innerH < vt.Height()`), on top of its existing wide-canvas
condition — a grid that FITS renders NATIVELY (top-left, padded), same as a
non-follower pane. The preview already bottom-anchors with scrollback above
(`renderPreview`, which shows `total - innerH - scrollBack`) and left-edge
crops, which is exactly the follower's cut: width keeps the LEFT columns,
height keeps the BOTTOM rows. Every rendered row stays exactly the pane's
width, as for any preview pane.

**Corner markers (`followerCutMark`, `internal/tui/pane.go`).**
`buildTopBorderCut` swaps one top-border CORNER for `"…"` — one cell for one
cell, so the border keeps its exact width and the label between the corners
is untouched — top-left for a HEIGHT cut (rows above the box are hidden, the
view is bottom-anchored) and top-right for a WIDTH cut (columns right of the
box are hidden, the view is cropped at the left edge). Both can show at once.

**Mouse.** There is no click forwarder in the TUI at all today, so follower
grid translation applies only to the wheel forwarder
(`wheelForwardSeq`/`sendInputToPane`): a notch translates box row `relY` to
grid row `relY + max(0, vtH - innerH)` while the view is not scrolled back,
with no horizontal offset (the crop is at the left already). A position past
`vtW`/`vtH` (the padding) sends nothing. Mouse selection uses the existing
`previewPosAt` mapping; keyboard selection works only when the grid fits the
box, as for wide-canvas panes today.

**Status bar.** While `clientCount[activeDest] >= 2`, the status bar shows
`[master]` or `[follower]` for the active destination, next to `[dev]`
(`internal/tui/model.go`, the status-bar assembly). With one client, nothing
is shown — a single TUI on a daemon looks exactly as it always has. `Take
control` (`client.take_control`, no default key, plus a palette command,
"Take control (size master)") sends `MsgTakeControl` and makes this client
master at once when the daemon accepts it.

### Layout sync between clients

`internal/tui/layoutsync.go`. The daemon numbers every stored write of a
tab's tree (`layout_rev`, spec §7.1) and refuses a write whose `base_rev` is
not the tab's current revision. A client sends a tab's tree only when ITS OWN
USER changed it, and adopts any broadcast carrying a higher revision than the
tree it holds — it never sends merely because the stored tree disagrees with
its own, which is what let two clients re-send each other's trees forever
before this landed (see the retired "Known limit" notes above).

**`markLayoutChanged(dest, tab)`** is the one place a user-caused mutation —
split, close, arrange, pane drag drop, split-border drag release, a client's
own `pendingSplit` reservation being filled — records the change: it
marshals the tree HERE, on the Update goroutine (the `tea.Cmd` it returns
holds only bytes, never the tab), sets `tab.layoutDirty`, and sends
`MsgUpdateLayout{TabID, Layout, BaseRev: &tab.layoutRev}` through
`sendDiffedLayouts`. **A write already in flight defers the next one**
(`tab.layoutResend`) instead of sending a second write on the same base —
that would be refused behind the first and lost — and the deferred change
rides the first write's own echo instead.

**Arrivals nobody on this client asked for — an MCP-created pane, another
client's split as a bystander sees it, a moved pane, a pane pruned because
another client closed it — are placed or pruned LOCALLY with the ordinary
arrival rules, so the screen is right at once, and recorded in
`tab.awaitingPanes`/`awaitingGone` without sending.** `syncTabLayout` checks,
on the NEXT broadcast for that tab, whether the stored tree still lacks any
awaited id or still holds a pruned one; only then does this client send its
tree with the current base rev, and the first such send from any client wins
— the rest are refused and adopt. Otherwise the requester's own write already
arrived at a higher revision and this client adopts it.

**`syncTabLayout`** runs per existing tab, per broadcast, BEFORE panes are
reconciled, and every comparison is STRUCTURAL (parsed `SerializedNode`),
never by bytes — the daemon's stored bytes and a client's own re-marshal of
the same tree encode differently (declaration order vs. `map[string]any`'s
alphabetical order), the same caveat the layout-persistence invariant in
`.claude/CLAUDE.md` states. A higher `layout_rev` (or `tab.adoptNext`, set
after a reattach — see below) triggers `adoptTabLayout`, UNLESS the stored
tree is this client's own write echoing back (`reflect.DeepEqual(stored,
tab.layoutSent)`), which is adopted as a no-op and clears `layoutDirty`,
resending only a deferred `layoutResend`. A LOWER revision, or a dirty tab,
keeps the local tree — the write in flight will come back with a higher one.
A refused write is simply superseded by the winner's broadcast and its local
change is lost; that needs two users editing the same tab at the same moment.

**`adoptTabLayout`** replaces the tab's tree with the stored one, reusing
`*PaneModel`s by id (no lost emulator or scrollback), dropping ids the
broadcast no longer lists, and leaving panes the stored tree lacks for the
ordinary arrival loop to place (which then follows the "arrivals nobody asked
for" rule above). It cancels an in-progress drag whose node is in this tab.
**This client's own `pendingSplit` reservation survives adoption**
(`reseatReservation`): it is put back BESIDE its original sibling pane, in
the original direction and half, when that sibling is still in the adopted
tree; only when the sibling is gone does it fall back to the spiral arrival
slot, and failing that it becomes the whole tab. A reservation whose
placeholder is no longer even IN the pre-adoption tree (abandoned) is
forgotten instead of re-seated. `tabLayoutBusy` still gates arrangements
throughout.

**Reattach resets the revs.** `armReattachReset` calls `resetLayoutSync(dest)`,
which zeros every tab's `layoutRev` on that destination and sets
`tab.adoptNext = true`: the daemon's stored tree is authoritative, and its
revision can be LOWER than this client's after a restart (the snapshot is
debounced 500 ms) — as low as the 0 of a tree nobody has written since
restore, which even a zeroed local revision would not otherwise adopt.
`adoptNext` is what makes `syncTabLayout` adopt the very next broadcast
whatever its revision. A tab whose stored layout is EMPTY (fresh, or restored
before any client described it) is sent by the client with the current base
rev — this replaces the retired `diffLayouts`' `len(stored)==0` branch.

**Older clients during dev** (release builds refuse a version mismatch, so
this matters only for dev builds): a client with no `BaseRev` support always
has its write accepted and never adopts; new clients adopt its tree, so
everyone converges on the old client's tree with no loop.

Tests: `layout_sync_test.go`. Mutation-checked: the send-on-change gate, the
rev compare-and-store (daemon side), the adopt condition, the arrival
"only the requester sends" rule, and the reattach rev reset.

### Typing guard across a remote tab switch

When a broadcast changes THIS client's active tab for the active project,
and this client did not itself request that switch, `Model.remoteSwitchAt`
is stamped and `Model.guardPaneID` records the pane that was active
immediately before the switch (`internal/tui/model.go`). "Requested" is
decided by a TOKEN, never a time window: `Model.requestedTab`, keyed by
`requestedTabKey(dest, projectID)`, records the tab this client asked for on
every local `switchTab`/`create_tab`;
a broadcast whose active tab equals it is this client's own switch landing
and clears the token, and any other change is remote.

**Keys reaching `enqueueInput` within `remoteSwitchGuardWindow` (250 ms) of
`remoteSwitchAt` are redirected to `guardPaneID`**, if that pane still exists
— otherwise they go to the new active pane. Only typed input is redirected;
mouse input always targets whatever is under the pointer now, since a click
is inherently aimed at what is on screen. A flash shows `Tab switched by
another client`.

**Unseen is not acknowledged until local input arrives.** A pane that became
focused only because of a remote switch is skipped by `ackFocusedPane` (no
`pane_seen` is sent) until this client receives a real key or mouse click —
otherwise every attached client would clear the mark on every remote switch
whether or not anyone actually looked at the pane.

Tests: `typing_guard_test.go`. Mutation-checked: the 250 ms window itself and
the `requestedTab` token gate.

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

**Buttonless motion is an audited skip site, and the busiest one.** `View` sets `tea.MouseModeAllMotion` whenever the project sidebar is painted (no dialog, `projectSidebarWidth() > 0`) or a context menu is open — cell motion otherwise — so the sidebar's hover highlight gets motion with no button held. That delivers every pointer move over the whole terminal. The `MouseMotionMsg` arm routes `Button == tea.MouseNone` to the hover ALONE, after the viewer, modal, overlay and context-menu branches and ahead of every drag branch (a drag is driven with the button held; buttonless motion must never start or advance one), and when the resolved `sidebarHover` key is unchanged it sets `skipRender = !prologueChangedView`; a changed key renders. The overlay branch does the same with the key cleared. Pinned by `TestSidebarHover_UnchangedHoverServesTheCachedFrame` (forced-rebuild style) — see `projects.md`'s Project groups for the highlight itself.

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



## Frame-cost instrumentation (`internal/tui/perf*.go`)

`eventLoopStats` (`perf.go`) writes one INFO line every 5 s. Its shape:

```
perf window=5s | view(n= skipped= hidden= avg= max=) | pane-out(bytes= max-vt=) |
  key-backlog-max=N | rt(cpu=/ gor= heap= gc= pause= assist=) | MsgType(n= avg= max=)
```

`rt(...)` (`perfruntime.go`) exists because the rest of the line can say a frame took
900 ms but not WHY a frame doing the same work cost fourteen times more. Three
hypotheses for the 2026-09 production stall — GC pressure, a large live VT heap, an
expensive renderer state — each had to be reproduced in a benchmark before they could be
ruled out, because the log could not separate them. **Read `cpu` against the View+Update
time on the same line**: comparable means the work genuinely cost more (look inside
quil); far below means the process was not running (look outside it).

Three rules the fields obey, all load-bearing:

- **`cpu` comes from the OS** — `getrusage(2)` / `GetProcessTimes`, in `perfcpu_unix.go`
  and `perfcpu_windows.go`. NOT from `/cpu/classes/total` minus `/cpu/classes/idle`.
  Those are a snapshot `work.cpuStats.accumulate` writes only from
  `gcMarkTermination`, so they advance when a GC cycle COMPLETES and never otherwise.
  Measured on Go 1.25: four goroutines burning CPU for 3 s with the collector off report
  `0.000s`. A TUI collecting every ~9 minutes would have printed `cpu=0s/5s` in ~99
  windows out of 100 — which this line's own reading rule calls "the process was off the
  CPU". `TestProcessCPU_Advances_WithoutAGarbageCollection` fails against that
  implementation and is the guard against reintroducing it.
- **Two sentinels, never a zero.** `?` = could not be measured (metric absent, or a
  counter that fell, meaning the samples did not come from one continuous run). `-` =
  nothing was published to report; assist is accounted at mark termination, so a window
  with no completed cycle has no assist figure. A `0s` in either position would be a
  finding the reading does not support.
- **Deltas, except `heap` and `gor`.** A cumulative counter on a process up for days
  cannot show that this window differed from the last. `heap` is the heap the PREVIOUS
  GC marked, so it holds between cycles — a step function by design, and `gc=` on the
  same line says when it last moved. `resetCounters` must never zero `lastRuntime`: it is
  the delta baseline, and zeroing it makes every later line read `?` while still looking
  plausible.

Sampled with `runtime/metrics`, not `runtime.ReadMemStats` — the latter stops the world,
and instrumentation that pauses the program is a poor way to investigate pauses.
`histogramTotal` sums with each bucket's UPPER bound: the bias is identical in both
samples of a delta, so it cancels, and the +Inf final bucket must fall back to its lower
bound or the duration conversion produces garbage that also poisons the next window.

**Two reproduction harnesses**, both `t.Skip`-gated so they never join `dev.sh test` or
CI (the large one holds ~1 GB and runs for minutes):

| Env var | Test | Answers |
|---|---|---|
| `QUIL_GC_REPRO` | `TestFrameLatencyVsLiveHeap` | Does live heap size move frame latency? Three arms: 2000-line scrollback, 100-line, and 2000 at GOGC=10. |
| `QUIL_STATE_SWEEP` | `TestFrameCostByModelState` | Does any model state (all panes blocked, notification sidebar open, scrolled back, selection held, wide-canvas off, 96 tabs) multiply frame cost? |

Run them with a direct `docker run … -e QUIL_GC_REPRO=1`; `dev.sh test` passes no env
vars and takes only one package argument. A frame classified as GC-affected is one where
a cycle was IN PROGRESS (`/gc/cycles/started` > `/gc/cycles/total`), not merely one where
a cycle finished — assists are charged across the whole mark phase, and the narrower test
files every assisted frame but the last under "clean", biasing the result toward
acquitting GC.
