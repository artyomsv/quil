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

printf '\nPASS: %s promote-changelog checks\n' "$PASSED"
