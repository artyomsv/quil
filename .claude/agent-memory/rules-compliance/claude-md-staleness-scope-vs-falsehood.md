---
name: claude-md-staleness-scope-vs-falsehood
description: When flagging a CLAUDE.md invariant as stale, verify whether the new code makes the claim FALSE or just INCOMPLETE (new path not covered) — the two need different fixes.
metadata:
  type: feedback
---

When a diff adds a new code path near an existing CLAUDE.md invariant, don't
assume the invariant is now false just because the new path does something
different. Check the OLD path too before writing the finding.

Concrete case (feat/remote-phase-2-reconnect review): CLAUDE.md said "TUI
skips `ResetVT()` for terminal panes on ghost→live transition." The new
`resetForReattach` (reconnect path) resets terminal panes unconditionally,
which looked like a direct contradiction, so I reported the line as "now
factually wrong." Team lead checked `model.go:2780-2790` and confirmed the
original ghost→live skip is untouched — it still fires at that moment, for
that reason, on every non-`claude-code` pane. The two code paths run at
different moments (`resetForReattach` runs *before* the attach that triggers
replay; the ghost→live skip runs *at* first live output) and do not conflict.
The real gap was that CLAUDE.md's claim reads as unscoped/universal, and a
second path now legitimately overrides it in a situation the line doesn't
mention — an incompleteness, not a falsehood.

**Why this matters:** "correct a falsehood" and "add a missing scope" produce
different edits. Rewriting a still-true claim as if it were false makes the
doc wrong in a *new* way (overcorrection). The right fix here was to scope
the existing claim ("...at the ghost→live moment...") and add a sentence
about the second path, not to replace or qualify away the original claim as
though it no longer held.

**How to apply:** before flagging a CLAUDE.md line as stale/contradicted,
trace the OLD code path the line describes and confirm it still behaves as
documented. Only call it "now false" if the old path itself changed or its
condition can no longer be reached as stated. If the old path is intact and
a new path just adds a case the line doesn't cover, describe it as a scope
gap ("doesn't mention path X") rather than a falsehood, and recommend an
additive/qualifying edit rather than a rewrite.
