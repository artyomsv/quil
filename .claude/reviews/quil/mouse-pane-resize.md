# Code Review State: quil / mouse-pane-resize

Last reviewed: 2026-07-15
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [qa/1] dragSplitBorder SplitVertical branch untested — TestModel_DragSplitBorder_Vertical added (round 1)
- [qa/2] CollectBorders placeholder-node skip branch untested — TestLayoutNode_CollectBorders_PlaceholderSkipped added (round 1)
- [qa/3] scrollbar-vs-split-border click priority untested — TestModel_SplitDrag_ScrollbarKeepsPriority added; precedence later inverted after manual testing (border owns its drawn line and is checked first; scrollbar keeps its drawn column bd-2 because the H-split zone is right-only asymmetric) (round 1)

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/L2] mcpHighlight overrides splitDragHighlight on a concurrently-highlighted pane — intentional precedence: surfacing MCP activity outranks the drag affordance, the other adjacent pane still shows the drag color (round 1)
- [code-quality/L3] setSplitDragHighlight exact `==` adjacency can miss a leaf in overfull degenerate layouts — panes already visibly overlap in that state; cosmetic, same arithmetic as the renderer in all normal sizes (round 1)
- [qa/4] minWidth/minHeight placeholder-leaf case shares the identical code branch as a real leaf — no distinct behavior to test (round 1)
- [qa/5] View() border-color precedence has no rendered-color assertion — consistent with how every other pane state is tested in this codebase; renderKey stale-frame guard is the load-bearing test (round 1)
