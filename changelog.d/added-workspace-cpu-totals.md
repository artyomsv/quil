---
headline: Tab and Total rows now show a real CPU figure
---
- **The workspace section totals CPU instead of showing an em dash.** `Total`
  and every tab row hardcoded "unknown", and a pane reported nothing while it
  was collapsed — so on a freshly opened dialog, which starts with everything
  collapsed, the `CPU` column was empty in every row that had one.

  The per-pane subtree totals are now computed once when each report arrives
  rather than on every render, which is what previously made summing them too
  expensive to do at all. A collapsed pane shows its subtotal for the same
  reason.

  A sum taken over processes that have not all been sampled yet renders as
  `~7%`. The number is real but an understatement, and a bare `7%` would claim
  a completeness it does not have.
