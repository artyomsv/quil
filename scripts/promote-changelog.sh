#!/bin/sh
# Promote per-PR changelog fragments into CHANGELOG.md at release time.
#
# Why fragments exist at all: every PR used to insert prose under the single
# `## [Unreleased]` heading, i.e. at the SAME anchor line. Git merges by line
# range, so the second of two parallel PRs conflicted with the first, every
# time, on a hunk the size of a multi-paragraph entry. Entries ended up written
# as separate follow-up commits (e2d74f3, 8c09458) purely to dodge the
# collision. One file per PR removes the conflict by construction — git has no
# conflict concept for two distinct ADDED paths.
#
# The grammar for a fragment filename lives HERE and only here. ci.yml's gate
# calls `--filter-names` rather than hand-writing an approximating regex,
# because a gate that can disagree with the action it guards is this repo's
# most expensive changelog bug: ci.yml asked "which files changed?" while
# release.yml asked "what type is the commit?", so `fix(site):` PRs passed
# review and then turned master red (#130, v1.49.0 -> v1.49.1). A looser regex
# in the gate would let `banana-x.md` merge green and die at the next release.
#
# Modes:
#   --filter-names   read candidate paths on stdin, print those that are
#                    fragments. Used by ci.yml's changelog gate.
#   --check          validate the working tree; write nothing. Used by
#                    release.yml BEFORE the version bump is committed, so a
#                    failure costs a one-line commit rather than a released
#                    version with no notes.
#   <version> <date> validate, then rewrite CHANGELOG.md and delete the
#                    consumed fragments.
#
# CHANGELOG_FILE / CHANGELOG_FRAGMENT_DIR override the paths, which is how
# scripts/test-promote-changelog.sh drives this against a throwaway tree.

set -eu

PROJECT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

FRAGMENT_DIR_REL="${CHANGELOG_FRAGMENT_DIR_REL:-changelog.d}"
FRAGMENT_DIR="${CHANGELOG_FRAGMENT_DIR:-$PROJECT_DIR/$FRAGMENT_DIR_REL}"
CHANGELOG="${CHANGELOG_FILE:-$PROJECT_DIR/CHANGELOG.md}"

# Keep a Changelog's section order, plus `internal` (already used in
# CHANGELOG.md) and `none` (the no-user-facing-changes sentinel, which renders
# no section at all). Render order is this order, NOT filename order.
FRAGMENT_TYPES='added changed deprecated removed fixed security internal'

# The slug must start alphanumeric so `fixed--x.md` and `fixed-.md` are refused
# rather than silently producing an odd-looking but valid-ish name.
NAME_RE='^(added|changed|deprecated|removed|fixed|security|internal|none)-[A-Za-z0-9][A-Za-z0-9._-]*\.md$'

SENTINEL='_No user-facing changes._'

die() {
  printf 'promote-changelog: %s\n' "$1" >&2
  exit 1
}

section_heading() {
  case "$1" in
    added)      printf 'Added' ;;
    changed)    printf 'Changed' ;;
    deprecated) printf 'Deprecated' ;;
    removed)    printf 'Removed' ;;
    fixed)      printf 'Fixed' ;;
    security)   printf 'Security' ;;
    internal)   printf 'Internal' ;;
    *)          die "unknown fragment type: $1" ;;
  esac
}

is_fragment_name() {
  printf '%s\n' "$1" | grep -Eq "$NAME_RE"
}

# Strip leading and trailing blank lines. Fragment authors should not have to
# think about whether their file ends with a newline or three.
trim_blank_lines() {
  awk '
    { line[NR] = $0 }
    END {
      s = 1; while (s <= NR && line[s] ~ /^[[:space:]]*$/) s++
      e = NR; while (e >= s && line[e] ~ /^[[:space:]]*$/) e--
      for (i = s; i <= e; i++) print line[i]
    }
  '
}

# --- mode: --filter-names -------------------------------------------------
# Reads paths (as `git diff --name-status --diff-filter=A | cut -f2` emits
# them) and prints the ones that are fragments. README.md is deliberately NOT
# a fragment: adding only the README must not satisfy the CI gate.
filter_names() {
  while IFS= read -r path; do
    path=$(printf '%s' "$path" | tr -d '\r')
    case "$path" in
      "$FRAGMENT_DIR_REL"/*/*) continue ;;
      "$FRAGMENT_DIR_REL"/*) ;;
      *) continue ;;
    esac
    if is_fragment_name "${path#"$FRAGMENT_DIR_REL"/}"; then
      printf '%s\n' "$path"
    fi
  done
}

# --- validation -----------------------------------------------------------
# Every entry in the fragment directory must be a valid fragment or the
# README. An unrecognised file is REFUSED, never skipped: a silently-dropped
# fragment is lost prose that surfaces weeks later as a wrong release page.
#
# README.md is the sole exemption and is load-bearing beyond documentation —
# git does not track empty directories, so without a permanent file
# changelog.d/ vanishes from the tree the moment a release consumes every
# fragment.
validate_fragment_dir() {
  [ -d "$FRAGMENT_DIR" ] || return 0
  invalid=''
  for f in "$FRAGMENT_DIR"/* "$FRAGMENT_DIR"/.[!.]*; do
    # -L as well as -e, so a DANGLING symlink is caught by the checks below
    # instead of falling through this unmatched-glob guard as if absent.
    if [ ! -e "$f" ] && [ ! -L "$f" ]; then
      continue
    fi
    name=${f##*/}
    # `[ x = y ] && continue` would be a set -e landmine: when the test fails
    # it is the last command of the AND-OR list, so the shell exits.
    if [ "$name" = 'README.md' ]; then
      continue
    fi
    if ! is_fragment_name "$name"; then
      invalid="$invalid
  $FRAGMENT_DIR_REL/$name — not a <type>-<slug>.md name"
      continue
    fi

    # A fragment must be a REGULAR file, and not a symlink.
    #
    # The promoter reads it and splices the bytes verbatim into CHANGELOG.md,
    # which release.yml then pushes to master and publishes as the GitHub
    # Release body. A symlink is read THROUGH, so a fragment named
    # `fixed-notes.md` pointing at `../.git/config` would publish the
    # AUTHORIZATION header that actions/checkout persists there — and that
    # credential is RELEASE_PAT, a ruleset bypass actor. Name validation
    # alone cannot see this: the name is perfectly legal.
    #
    # A directory is refused for the same reason it must not be skipped: it
    # passed the old check, then `tr` failed mid-promote AFTER CHANGELOG.md
    # had already been rewritten.
    #
    # Refused, never skipped — a skipped file is silently-dropped prose,
    # which is the failure this function exists to prevent.
    if [ -L "$f" ]; then
      invalid="$invalid
  $FRAGMENT_DIR_REL/$name — is a symlink; fragments must be regular files"
      continue
    fi
    if [ ! -f "$f" ]; then
      invalid="$invalid
  $FRAGMENT_DIR_REL/$name — is not a regular file"
      continue
    fi

    # `none-*` carries no prose by design, so the content rules below do not
    # apply to it.
    case "$name" in
      none-*) continue ;;
    esac

    # An empty or blank-only fragment renders a section heading with nothing
    # under it — `### Fixed` alone, shipped to the changelog and the release
    # page. Refused rather than skipped, because "forgot to write the prose"
    # and "deliberately nothing to say" must not resolve to the same output;
    # the second one is what `none-<slug>.md` is for.
    if [ -z "$(tr -d '\r' < "$f" | trim_blank_lines)" ]; then
      invalid="$invalid
  $FRAGMENT_DIR_REL/$name — is empty; write the entry, or use none-<slug>.md"
      continue
    fi

    # goreleaser extracts the release body with a sed range that ENDS at the
    # next `^## [` (release.yml). A fragment carrying such a line therefore
    # truncates the published notes at that point and leaves a bogus version
    # heading in CHANGELOG.md that every later extraction also mis-parses.
    # The promoter owns every heading, so a fragment never needs one.
    if grep -q '^## \[' "$f"; then
      invalid="$invalid
  $FRAGMENT_DIR_REL/$name — contains a '## [' heading; the promoter writes those"
    fi
  done
  [ -z "$invalid" ] || die "invalid changelog fragment(s):
$invalid

A fragment is a regular file named <type>-<slug>.md, where <type> is one of:
  added changed deprecated removed fixed security internal none
and <slug> starts with a letter or digit. It holds the bullet text only.
Only README.md is exempt."
}

# Paths of every fragment of one type, C-sorted so output is reproducible.
fragments_of_type() {
  [ -d "$FRAGMENT_DIR" ] || return 0
  # -f, not -e: validate_fragment_dir has already refused anything that is
  # not a regular file, so this only ever fires on the unmatched glob — but
  # it means no caller can be handed a directory or a dangling link even if
  # it forgot to validate first.
  for f in "$FRAGMENT_DIR"/"$1"-*.md; do
    [ -f "$f" ] || continue
    printf '%s\n' "$f"
  done | LC_ALL=C sort
}

any_fragments() {
  for t in $FRAGMENT_TYPES none; do
    if [ -n "$(fragments_of_type "$t")" ]; then
      return 0
    fi
  done
  return 1
}

# Whatever prose sits under `## [Unreleased]`. Normally empty — fragments are
# the supported path — but a hand-edit must never be silently discarded, so it
# is promoted too (placed above the rendered sections). Same sed range
# release.yml has always used.
unreleased_prose() {
  sed -n '/^## \[Unreleased\]/,/^## \[/{ /^## \[Unreleased\]/d; /^## \[/d; p; }' \
    "$CHANGELOG" | tr -d '\r' | trim_blank_lines
}

check() {
  [ -f "$CHANGELOG" ] || die "$CHANGELOG does not exist"
  grep -q '^## \[Unreleased\]' "$CHANGELOG" \
    || die "$CHANGELOG has no '## [Unreleased]' anchor — the promoter inserts below it"

  validate_fragment_dir

  # A lone `none-*` fragment counts: it is the explicit "this release has
  # nothing to tell users" statement, and refusing it here would red-master
  # exactly the release the sentinel exists to serve.
  if any_fragments; then
    return 0
  fi
  if [ -n "$(unreleased_prose)" ]; then
    return 0
  fi

  die "nothing to promote — no changelog fragments and an empty [Unreleased] section.

Add a fragment describing the change from a user's point of view:
  $FRAGMENT_DIR_REL/fixed-<slug>.md
If this release genuinely has nothing user-facing, say so explicitly:
  $FRAGMENT_DIR_REL/none-<slug>.md"
}

# --- mode: promote --------------------------------------------------------
promote() {
  version=$1
  date=$2

  printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
    || die "invalid version: '$version' (expected X.Y.Z)"
  printf '%s' "$date" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' \
    || die "invalid date: '$date' (expected YYYY-MM-DD)"

  check

  work=$(mktemp -d)
  # shellcheck disable=SC2064  # expand $work now, not at trap time
  trap "rm -rf '$work'" EXIT

  body="$work/body"
  : > "$body"

  # Hand-written prose first, so a duplicated `### Fixed` heading (prose plus
  # fragments both carrying one) is visible in review rather than lossy.
  prose=$(unreleased_prose)
  if [ -n "$prose" ]; then
    printf '%s\n' "$prose" >> "$body"
  fi

  for t in $FRAGMENT_TYPES; do
    files=$(fragments_of_type "$t")
    [ -n "$files" ] || continue
    [ ! -s "$body" ] || printf '\n' >> "$body"
    printf '### %s\n' "$(section_heading "$t")" >> "$body"
    # No blank line after the heading, exactly one before the next heading —
    # matches how every existing block in CHANGELOG.md is written.
    # A read failure inside this loop runs in a subshell, so `set -e` cannot
    # see it — and the failure would otherwise be discovered only after
    # CHANGELOG.md had already been replaced. Make it fatal explicitly.
    printf '%s\n' "$files" | while IFS= read -r f; do
      tr -d '\r' < "$f" | trim_blank_lines || exit 1
    done >> "$body" || die "could not read a $t fragment"
  done

  # Only `none-*` fragments were present: emit the sentinel release.yml's
  # goreleaser job already recognises (it falls through to generated notes).
  if [ ! -s "$body" ]; then
    printf '%s\n' "$SENTINEL" > "$body"
  fi

  anchor=$(grep -n '^## \[Unreleased\]' "$CHANGELOG" | head -1 | cut -d: -f1)
  total=$(wc -l < "$CHANGELOG" | tr -d ' ')
  rest=$(awk -v a="$anchor" 'NR > a && /^## \[/ { print NR; exit }' "$CHANGELOG")
  [ -n "$rest" ] || rest=$((total + 1))

  out="$work/changelog"
  {
    head -n "$anchor" "$CHANGELOG"
    printf '\n## [%s] - %s\n\n' "$version" "$date"
    cat "$body"
    # The blank line separates this section from the NEXT one, so it is only
    # correct when there is a next one. On the first release of a fresh
    # changelog `## [Unreleased]` is the last line, and emitting it
    # unconditionally leaves a trailing blank line at EOF forever.
    if [ "$rest" -le "$total" ]; then
      printf '\n'
      tail -n +"$rest" "$CHANGELOG"
    fi
  } > "$out"
  mv "$out" "$CHANGELOG"

  # Deleting the fragments is what keeps the NEXT branch conflict-free: a
  # rebased branch sees an empty changelog.d/ and its own new file collides
  # with nothing. release.yml stages this with `git add -A` — a plain `rm`
  # leaves the deletion unstaged and the fragment is re-promoted on every
  # subsequent release (the monorepo's process-changelog action carries a
  # comment recording exactly that bug).
  consumed=0
  for t in $FRAGMENT_TYPES none; do
    files=$(fragments_of_type "$t")
    [ -n "$files" ] || continue
    printf '%s\n' "$files" | while IFS= read -r f; do
      rm -f "$f"
    done
    consumed=$((consumed + 1))
  done

  printf 'promote-changelog: %s promoted into %s\n' \
    "$version" "${CHANGELOG#"$PROJECT_DIR"/}"
  [ "$consumed" -gt 0 ] || printf 'promote-changelog: no fragments (promoted [Unreleased] prose)\n'
}

USAGE='usage: %s [--filter-names | --validate | --check | <version> <date>]\n'

case "${1:---help}" in
  --filter-names)
    filter_names
    ;;
  --validate)
    # Directory hygiene only, with no "is there anything to release?"
    # requirement — so ci.yml can run it on EVERY pull request, including the
    # docs-only ones that are exempt from needing a fragment. Without this
    # split, an invalid file could land via a docs-only PR and first surface
    # as a failed release.
    validate_fragment_dir
    printf 'promote-changelog: %s/ is valid\n' "$FRAGMENT_DIR_REL"
    ;;
  --check)
    check
    printf 'promote-changelog: %s/ is valid and has something to promote\n' "$FRAGMENT_DIR_REL"
    ;;
  --help|-h)
    # shellcheck disable=SC2059  # USAGE is a literal format string
    printf "$USAGE" "$0" >&2
    exit 2
    ;;
  *)
    if [ $# -ne 2 ]; then
      # shellcheck disable=SC2059  # USAGE is a literal format string
      die "$(printf "$USAGE" "$0")"
    fi
    promote "$1" "$2"
    ;;
esac
