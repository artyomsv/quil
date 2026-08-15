#!/bin/sh
set -eu

# Regression tests for scripts/promote-changelog.sh.
#
# This is the one step in the release pipeline that REWRITES CHANGELOG.md, so a
# splicing bug here silently eats unreleased prose or previously released
# history. Case 11 (released sections byte-identical) and the exact-output
# comparisons are the guards that matter most; the rest pin the grammar and the
# sentinel behaviour.
#
# Case 13 is the anti-divergence test: ci.yml's PR gate calls --filter-names
# instead of hand-writing its own regex, because a gate that disagrees with the
# action it guards is this repo's most expensive changelog bug (#130). If the
# two ever drift apart, this fails.
#
# No network, no git, no Go — the promoter is driven against a throwaway tree
# via CHANGELOG_FILE / CHANGELOG_FRAGMENT_DIR.

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
PROMOTE="$SCRIPT_DIR/promote-changelog.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

PASSED=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

ok() {
  PASSED=$((PASSED + 1))
  printf '  ok  %s\n' "$1"
}

BASELINE='# Changelog

All notable changes to Quil will be documented in this file.

## [Unreleased]

## [1.0.1] - 2026-01-02

### Fixed
- **Older fix.** Body text.

## [1.0.0] - 2026-01-01

### Added
- **First release.**
'

# Fresh throwaway tree per case.
setup() {
  CASE="$WORK/$1"
  rm -rf "$CASE"
  mkdir -p "$CASE/changelog.d"
  printf '%s' "$BASELINE" > "$CASE/CHANGELOG.md"
  printf '# Changelog fragments\n' > "$CASE/changelog.d/README.md"
  CHANGELOG_FILE="$CASE/CHANGELOG.md"
  CHANGELOG_FRAGMENT_DIR="$CASE/changelog.d"
  export CHANGELOG_FILE CHANGELOG_FRAGMENT_DIR
}

frag() {
  printf '%s\n' "$2" > "$CASE/changelog.d/$1"
}

# Assert the whole file, not a substring — a splice that drops history still
# contains every line the substring check would look for.
assert_changelog() {
  printf '%s' "$2" > "$CASE/expected"
  if ! diff -u "$CASE/expected" "$CHANGELOG_FILE" > "$CASE/diff" 2>&1; then
    cat "$CASE/diff" >&2
    fail "$1: CHANGELOG.md does not match expected output"
  fi
}

# --- 1. single fragment, exact output -------------------------------------
setup single
frag fixed-thing.md '- **New fix.** Body.'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
assert_changelog 'case 1' '# Changelog

All notable changes to Quil will be documented in this file.

## [Unreleased]

## [1.1.0] - 2026-02-03

### Fixed
- **New fix.** Body.

## [1.0.1] - 2026-01-02

### Fixed
- **Older fix.** Body text.

## [1.0.0] - 2026-01-01

### Added
- **First release.**
'
[ ! -e "$CASE/changelog.d/fixed-thing.md" ] || fail 'case 1: fragment was not deleted'
[ -e "$CASE/changelog.d/README.md" ] || fail 'case 1: README.md was deleted'
ok '1  single fragment: exact output, fragment consumed, README kept'

# --- 2 + 14. section order and whitespace ---------------------------------
# Fragments are created in reverse render order on purpose: if the promoter
# used filename or creation order, this case fails.
setup order
frag security-c.md '- **Sec.**'
frag added-a.md '- **Add.**'
frag fixed-b.md '- **Fix.**'
frag internal-d.md '- **Int.**'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
assert_changelog 'case 2' '# Changelog

All notable changes to Quil will be documented in this file.

## [Unreleased]

## [1.1.0] - 2026-02-03

### Added
- **Add.**

### Fixed
- **Fix.**

### Security
- **Sec.**

### Internal
- **Int.**

## [1.0.1] - 2026-01-02

### Fixed
- **Older fix.** Body text.

## [1.0.0] - 2026-01-01

### Added
- **First release.**
'
ok '2  render order is Keep a Changelog, not filename order; spacing exact'

# --- 3. several fragments of one type -------------------------------------
setup samety
frag fixed-zebra.md '- **Zebra.**'
frag fixed-alpha.md '- **Alpha.**'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
grep -q '^- \*\*Alpha\.\*\*$' "$CHANGELOG_FILE" || fail 'case 3: alpha entry missing'
grep -q '^- \*\*Zebra\.\*\*$' "$CHANGELOG_FILE" || fail 'case 3: zebra entry missing'
[ "$(grep -c '^### Fixed$' "$CHANGELOG_FILE")" -eq 2 ] \
  || fail 'case 3: expected one new "### Fixed" plus the pre-existing one'
ALPHA_AT=$(grep -n '^- \*\*Alpha\.\*\*$' "$CHANGELOG_FILE" | cut -d: -f1)
ZEBRA_AT=$(grep -n '^- \*\*Zebra\.\*\*$' "$CHANGELOG_FILE" | cut -d: -f1)
[ "$ALPHA_AT" -lt "$ZEBRA_AT" ] || fail 'case 3: fragments not in filename sort order'
ok '3  several fragments of one type share a heading, filename-sorted'

# --- 4. lone sentinel ------------------------------------------------------
setup sentinel
frag none-refactor.md '- ignored'
sh "$PROMOTE" --check > /dev/null || fail 'case 4: --check rejected a lone none-* fragment'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
grep -qx '_No user-facing changes\._' "$CHANGELOG_FILE" \
  || fail 'case 4: sentinel line not emitted'
[ ! -e "$CASE/changelog.d/none-refactor.md" ] || fail 'case 4: none-* not consumed'
ok '4  lone none-* passes --check and emits the sentinel'

# --- 5. sentinel alongside real fragments ---------------------------------
setup mixed
frag none-refactor.md '- ignored'
frag fixed-real.md '- **Real.**'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
if grep -qx '_No user-facing changes\._' "$CHANGELOG_FILE"; then
  fail 'case 5: sentinel emitted despite a real fragment being present'
fi
grep -q '^- \*\*Real\.\*\*$' "$CHANGELOG_FILE" || fail 'case 5: real entry missing'
[ ! -e "$CASE/changelog.d/none-refactor.md" ] || fail 'case 5: none-* not consumed'
ok '5  sentinel suppressed by a real fragment, still consumed'

# --- 6. leftover [Unreleased] prose ---------------------------------------
setup prose
printf '%s' '# Changelog

## [Unreleased]

### Changed
- **Hand written.**

## [1.0.0] - 2026-01-01

### Added
- **First release.**
' > "$CHANGELOG_FILE"
frag fixed-frag.md '- **From a fragment.**'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
assert_changelog 'case 6' '# Changelog

## [Unreleased]

## [1.1.0] - 2026-02-03

### Changed
- **Hand written.**

### Fixed
- **From a fragment.**

## [1.0.0] - 2026-01-01

### Added
- **First release.**
'
ok '6  hand-written [Unreleased] prose is promoted above the fragments'

# --- 7. nothing to promote -------------------------------------------------
setup empty
if sh "$PROMOTE" --check > /dev/null 2>&1; then
  fail 'case 7: --check passed with no fragments and an empty [Unreleased]'
fi
ok '7  --check refuses an empty release'

# --- 8. invalid filenames --------------------------------------------------
for BAD in banana-x.md Fixed-X.md fixed-.md notes.md; do
  setup "bad-$(printf '%s' "$BAD" | tr -d '.')"
  frag "$BAD" '- **Whatever.**'
  if sh "$PROMOTE" --check > /dev/null 2>&1; then
    fail "case 8: --check accepted invalid fragment name '$BAD'"
  fi
  if sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null 2>&1; then
    fail "case 8: promote ran with invalid fragment name '$BAD'"
  fi
  assert_changelog "case 8 ($BAD)" "$BASELINE"
done
setup bad-dotfile
: > "$CASE/changelog.d/.gitkeep"
if sh "$PROMOTE" --check > /dev/null 2>&1; then
  fail 'case 8: --check accepted a .gitkeep stray'
fi
ok '8  invalid names are refused and nothing is written'

# --- 9. README is inert ----------------------------------------------------
setup readme
frag fixed-x.md '- **X.**'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
[ -e "$CASE/changelog.d/README.md" ] || fail 'case 9: README.md deleted'
if grep -q 'Changelog fragments' "$CHANGELOG_FILE"; then
  fail 'case 9: README.md contents were promoted into CHANGELOG.md'
fi
ok '9  README.md is never consumed, deleted, or rejected'

# --- 10 + 11. anchor survives, history untouched --------------------------
setup history
frag added-y.md '- **Y.**'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
grep -qx '## \[Unreleased\]' "$CHANGELOG_FILE" || fail 'case 10: [Unreleased] anchor lost'
# Everything from the previous top version down must be byte-identical.
printf '%s' "$BASELINE" | sed -n '/^## \[1\.0\.1\]/,$p' > "$CASE/history-before"
sed -n '/^## \[1\.0\.1\]/,$p' "$CHANGELOG_FILE" > "$CASE/history-after"
diff -u "$CASE/history-before" "$CASE/history-after" \
  || fail 'case 11: previously released sections changed'
ok '10 [Unreleased] anchor survives; 11 released history byte-identical'

# --- 12. CRLF --------------------------------------------------------------
setup crlf
printf -- '- **CRLF entry.**\r\n' > "$CASE/changelog.d/fixed-crlf.md"
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
if grep -q "$(printf '\r')" "$CHANGELOG_FILE"; then
  fail 'case 12: carriage return leaked into CHANGELOG.md'
fi
grep -q '^- \*\*CRLF entry\.\*\*$' "$CHANGELOG_FILE" || fail 'case 12: entry missing'
ok '12 CRLF in a fragment does not reach CHANGELOG.md'

# --- 13. --filter-names agrees with --check -------------------------------
# The names ci.yml will accept must be exactly the names the release will.
VALID='added-a.md changed-b.md deprecated-c.md removed-d.md fixed-e.md
security-f.md internal-g.md none-h.md fixed-a.b_c-2.md'
INVALID='banana-x.md Fixed-X.md fixed-.md fixed--x.md notes.md README.md
fixed-x.txt .gitkeep'

for NAME in $VALID; do
  GOT=$(printf 'changelog.d/%s\n' "$NAME" | sh "$PROMOTE" --filter-names)
  [ "$GOT" = "changelog.d/$NAME" ] || fail "case 13: --filter-names rejected valid '$NAME'"
done
for NAME in $INVALID; do
  GOT=$(printf 'changelog.d/%s\n' "$NAME" | sh "$PROMOTE" --filter-names)
  [ -z "$GOT" ] || fail "case 13: --filter-names accepted invalid '$NAME'"
  # README.md is exempt from --check but is still not a fragment, so it must
  # not satisfy the PR gate. Every OTHER invalid name must also fail --check.
  if [ "$NAME" != 'README.md' ]; then
    setup "parity-$(printf '%s' "$NAME" | tr -d '.')"
    : > "$CASE/changelog.d/$NAME"
    if sh "$PROMOTE" --check > /dev/null 2>&1; then
      fail "case 13: --filter-names rejects '$NAME' but --check accepts it (divergence)"
    fi
  fi
done
# Paths outside changelog.d/, and nested ones, are not fragments.
for PATHNAME in CHANGELOG.md docs/changelog.d/fixed-x.md changelog.d/sub/fixed-x.md; do
  GOT=$(printf '%s\n' "$PATHNAME" | sh "$PROMOTE" --filter-names)
  [ -z "$GOT" ] || fail "case 13: --filter-names accepted out-of-tree path '$PATHNAME'"
done
# A realistic gate invocation: a mixed diff yields only the fragments.
GOT=$(printf '%s\n' 'internal/tui/model.go' 'changelog.d/fixed-e.md' 'CHANGELOG.md' \
  | sh "$PROMOTE" --filter-names)
[ "$GOT" = 'changelog.d/fixed-e.md' ] || fail 'case 13: mixed diff filtered wrongly'
ok '13 --filter-names and --check accept exactly the same names'

# --- 15. a symlink fragment is refused --------------------------------------
# The promoter reads a fragment and splices the bytes verbatim into
# CHANGELOG.md, which the release pushes to master and publishes as the
# GitHub Release body. A symlink is read THROUGH, so `fixed-x.md` pointing at
# `../.git/config` would publish the AUTHORIZATION header actions/checkout
# persists there — and that credential is RELEASE_PAT, a ruleset bypass actor.
# The name is perfectly legal, so only a file-type check catches it.
setup symlink
printf 'SECRET-CANARY-NOT-A-REAL-VALUE\n' > "$CASE/target.txt"
if ln -s "$CASE/target.txt" "$CASE/changelog.d/fixed-link.md" 2>/dev/null && [ -L "$CASE/changelog.d/fixed-link.md" ]; then
  if sh "$PROMOTE" --validate > /dev/null 2>&1; then
    fail 'case 15: --validate accepted a symlink fragment'
  fi
  if sh "$PROMOTE" --check > /dev/null 2>&1; then
    fail 'case 15: --check accepted a symlink fragment'
  fi
  if sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null 2>&1; then
    fail 'case 15: promote ran with a symlink fragment'
  fi
  assert_changelog 'case 15' "$BASELINE"
  if grep -q 'SECRET-CANARY' "$CHANGELOG_FILE"; then
    fail 'case 15: symlink target content reached CHANGELOG.md'
  fi
  [ -f "$CASE/target.txt" ] || fail 'case 15: symlink target was deleted'
  ok '15 symlink fragment refused; target neither read nor deleted'
else
  printf '  SKIP 15 (this filesystem does not support symlinks)\n'
fi

# --- 16. a directory named like a fragment is refused ------------------------
# It used to pass --check, then fail mid-promote AFTER CHANGELOG.md had
# already been replaced — leaving a half-written file behind.
setup dirfrag
mkdir -p "$CASE/changelog.d/fixed-adir.md"
if sh "$PROMOTE" --check > /dev/null 2>&1; then
  fail 'case 16: --check accepted a directory named like a fragment'
fi
if sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null 2>&1; then
  fail 'case 16: promote ran with a directory named like a fragment'
fi
assert_changelog 'case 16' "$BASELINE"
ok '16 directory named like a fragment refused; CHANGELOG.md untouched'

# --- 17. empty and blank-only fragments are refused --------------------------
# Otherwise they render a section heading with nothing under it, shipped to
# the changelog and the release page.
for BODY in '' '   ' '

'; do
  setup "empty$(printf '%s' "$BODY" | wc -c | tr -d ' ')"
  printf '%s' "$BODY" > "$CASE/changelog.d/fixed-blank.md"
  if sh "$PROMOTE" --check > /dev/null 2>&1; then
    fail 'case 17: --check accepted a blank fragment'
  fi
  if sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null 2>&1; then
    fail 'case 17: promote ran with a blank fragment'
  fi
  assert_changelog 'case 17' "$BASELINE"
done
ok '17 empty and blank-only fragments refused; nothing written'

# --- 18. an empty none-* fragment is still fine ------------------------------
# Its content is ignored by design, so the content rules must not apply to it.
setup emptynone
: > "$CASE/changelog.d/none-refactor.md"
sh "$PROMOTE" --check > /dev/null || fail 'case 18: --check rejected an empty none-*'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
grep -qx '_No user-facing changes\._' "$CHANGELOG_FILE" \
  || fail 'case 18: sentinel not emitted for an empty none-*'
ok '18 an empty none-* fragment is accepted and renders the sentinel'

# --- 19. a fragment carrying a version heading is refused --------------------
# goreleaser extracts the release body with a sed range ending at the next
# `^## [`, so such a line truncates the published notes there and leaves a
# bogus heading that every later extraction also mis-parses.
setup injection
printf -- '- **Real.**\n\n## [9.9.9] - 2020-01-01\n\n- **Injected.**\n' \
  > "$CASE/changelog.d/fixed-inject.md"
if sh "$PROMOTE" --check > /dev/null 2>&1; then
  fail 'case 19: --check accepted a fragment containing a "## [" heading'
fi
assert_changelog 'case 19' "$BASELINE"
ok '19 a fragment containing a "## [" heading is refused'

# --- 20. --validate vs --check -----------------------------------------------
# ci.yml runs --validate on every PR, including docs-only ones that are exempt
# from needing a fragment; those legitimately have nothing to promote.
setup validateonly
sh "$PROMOTE" --validate > /dev/null \
  || fail 'case 20: --validate failed on a clean, empty changelog.d'
if sh "$PROMOTE" --check > /dev/null 2>&1; then
  fail 'case 20: --check passed with nothing to promote'
fi
frag banana-x.md '- **Bad name.**'
if sh "$PROMOTE" --validate > /dev/null 2>&1; then
  fail 'case 20: --validate accepted an invalid name'
fi
ok '20 --validate checks hygiene without requiring something to promote'

# --- 21. argument handling ---------------------------------------------------
setup args
sh "$PROMOTE" --help > /dev/null 2>&1 && fail 'case 21: --help exited 0'
sh "$PROMOTE" > /dev/null 2>&1 && fail 'case 21: no-arg invocation exited 0'
frag fixed-x.md '- **X.**'
for BAD_V in 1.2 v1.2.3 1.2.3.4 ''; do
  if sh "$PROMOTE" "$BAD_V" 2026-02-03 > /dev/null 2>&1; then
    fail "case 21: accepted invalid version '$BAD_V'"
  fi
done
for BAD_D in 02-03-2026 2026-2-3 tomorrow; do
  if sh "$PROMOTE" 1.1.0 "$BAD_D" > /dev/null 2>&1; then
    fail "case 21: accepted invalid date '$BAD_D'"
  fi
done
assert_changelog 'case 21' "$BASELINE"
ok '21 bad arguments are refused and nothing is written'

# --- 22. malformed or missing CHANGELOG.md -----------------------------------
setup noanchor
printf '# Changelog\n\n## [1.0.0] - 2026-01-01\n\n### Added\n- **First.**\n' > "$CHANGELOG_FILE"
frag fixed-x.md '- **X.**'
if sh "$PROMOTE" --check > /dev/null 2>&1; then
  fail 'case 22: --check passed with no [Unreleased] anchor'
fi
setup nochangelog
rm -f "$CHANGELOG_FILE"
frag fixed-x.md '- **X.**'
if sh "$PROMOTE" --check > /dev/null 2>&1; then
  fail 'case 22: --check passed with no CHANGELOG.md'
fi
ok '22 a missing anchor or missing CHANGELOG.md is refused'

# --- 23. [Unreleased] as the last line ---------------------------------------
# The `rest=$((total + 1))` fallback: there is no following section to splice
# back in.
setup lastline
printf '# Changelog\n\n## [Unreleased]\n' > "$CHANGELOG_FILE"
frag added-first.md '- **First entry.**'
sh "$PROMOTE" 1.0.0 2026-02-03 > /dev/null
assert_changelog 'case 23' '# Changelog

## [Unreleased]

## [1.0.0] - 2026-02-03

### Added
- **First entry.**
'
ok '23 promotes correctly when [Unreleased] is the last line'

# --- 24. the remaining types render through promote() ------------------------
# Cases 2 and 13 between them cover added/fixed/security/internal and the NAME
# grammar; these three types had never been through the real render path.
setup remainingtypes
frag changed-a.md '- **Chg.**'
frag deprecated-b.md '- **Dep.**'
frag removed-c.md '- **Rem.**'
sh "$PROMOTE" 1.1.0 2026-02-03 > /dev/null
assert_changelog 'case 24' '# Changelog

All notable changes to Quil will be documented in this file.

## [Unreleased]

## [1.1.0] - 2026-02-03

### Changed
- **Chg.**

### Deprecated
- **Dep.**

### Removed
- **Rem.**

## [1.0.1] - 2026-01-02

### Fixed
- **Older fix.** Body text.

## [1.0.0] - 2026-01-01

### Added
- **First release.**
'
ok '24 changed/deprecated/removed render in the right order'

printf '\nPASS: %s promote-changelog checks\n' "$PASSED"
