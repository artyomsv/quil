#!/bin/sh
# Refuse to build when an agent-context file has grown past the point where the
# harness will load all of it.
#
# Claude Code loads .claude/CLAUDE.md and the always-on .claude/rules/*.md into
# every session, top-down, and truncates past a per-file character limit. The
# failure is silent and it eats the TAIL — which is where the most recently
# added notes live. So the symptom is not "the file is too big", it is "the
# thing I documented last week is the one Claude does not know", and nothing
# reports it.
#
# CLAUDE.md hit 153,368 chars on 2026-08-01 (up from 57,185 on 2026-06-10) and
# was over the limit. The fix was to move package-specific detail into
# .claude/rules/*.md gated by `paths:` globs, which the harness loads only when
# a matching file is touched. This guard keeps it that way: without a hard
# ceiling the same drift returns, because every individual addition is small
# and obviously worth making.
#
# Bytes, not characters: `wc -c` counts bytes and these files are full of UTF-8
# em-dashes and box glyphs, so the byte count OVERSTATES the character count.
# That is the safe direction — this guard trips slightly early, never late.

set -eu

PROJECT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

# CLAUDE.md is always-on, so its ceiling is a project policy well under the
# harness limit — it leaves headroom for the rules table and for growth.
CLAUDE_MAX=100000
# A `paths:`-gated rule only loads when its files are in play, so it can be
# larger; this ceiling exists only to stay clear of the harness truncation.
RULE_MAX=140000

fail=0
warn=0

check() {
  path="$1"
  max="$2"
  label="$3"
  [ -f "$path" ] || return 0
  size=$(wc -c <"$path" | tr -d ' ')
  rel=${path#"$PROJECT_DIR"/}
  if [ "$size" -gt "$max" ]; then
    printf '  OVER LIMIT  %-40s %8s bytes  (max %s)\n' "$rel" "$size" "$max"
    fail=1
  elif [ "$size" -gt $((max * 80 / 100)) ]; then
    printf '  approaching %-40s %8s bytes  (max %s, %s)\n' "$rel" "$size" "$max" "$label"
    warn=1
  fi
}

check "$PROJECT_DIR/.claude/CLAUDE.md" "$CLAUDE_MAX" "always-on"

# Every rule file, whether or not it is gated. An always-on rule (no `paths:`)
# is as costly as CLAUDE.md, but the cheap check is the same either way.
if [ -d "$PROJECT_DIR/.claude/rules" ]; then
  for f in "$PROJECT_DIR"/.claude/rules/*.md; do
    [ -e "$f" ] || continue
    check "$f" "$RULE_MAX" "scoped rule"
  done
fi

if [ "$fail" -ne 0 ]; then
  cat <<'EOF'

  Agent-context file is over its size limit.

  Do not raise the limit. Move the package-specific parts into a scoped rule:

    1. Create or extend .claude/rules/<area>.md
    2. Give it frontmatter with BOTH `description:` and `paths:` globs —
       `paths:` is what makes it load conditionally. A rule with only a
       `description:` is loaded into EVERY session, same as CLAUDE.md.
    3. Leave a row for it in the "Scoped rules" table in .claude/CLAUDE.md.

  The test for what stays in CLAUDE.md: does this apply when I open a file in
  a different package? If no, it belongs in a scoped rule.

EOF
  exit 1
fi

if [ "$warn" -ne 0 ]; then
  printf '\n  Within limits, but plan the split before it trips the build.\n\n'
fi

exit 0
