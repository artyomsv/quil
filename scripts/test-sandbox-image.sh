#!/bin/sh
set -eu

# Regression test for the version resolution in sandbox-image.sh.
#
# Docker keys a RUN layer on its text, so the image only updates when the
# build receives an EXACT agent version that changes with each release. A spec
# that reaches `docker build` unresolved (`latest`, a range) is the same text on
# every rebuild, and the cached layer keeps the old agent while the build still
# reports success. This checks that every spec but an exact version is resolved
# through npm first, and that only an exact version reaches the build.
#
# No Docker and no network: a stub `docker` on PATH answers the script's calls
# with made-up versions (9.9.x) and records what `docker build` was given.

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir "$WORK/bin"
cat > "$WORK/bin/docker" <<'STUB'
#!/bin/sh
# Stub docker: logs every npm query and build, answers with fake versions.
log="$STUB_LOG"
case "$1" in
  info) echo linux; exit 0 ;;
  image) exit 1 ;;                      # no image under the tag yet
  build) echo "build $*" >> "$log"; exit 0 ;;
esac
# docker run ...: either `npm view PKG@SPEC version` or a verify `sh -c CMD`.
last=""; prev=""; spec=""
for a in "$@"; do
  if [ "$prev" = "view" ]; then spec="$a"; fi
  prev="$a"; last="$a"
done
if [ -n "$spec" ]; then
  echo "npmview $spec" >> "$log"
  case "$spec" in
    *@missing) ;;                                                 # unknown tag: no output
    *" - "*|*"||"*|*"^"*) printf "%s 9.9.1\n%s '9.9.2'\n" "x@9.9.1" "x@9.9.2" ;;
    *) echo "9.9.0" ;;                                            # a dist-tag
  esac
  exit 0
fi
case "$last" in
  "id -un") echo agent ;;
  *"--version") echo "9.9.0 (stub)" ;;
  "git --version") echo "git version 0" ;;
esac
STUB
chmod +x "$WORK/bin/docker"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

# run_case <name> <args...>: runs the script, leaves the log in $WORK/log.
run_case() {
  name="$1"; shift
  : > "$WORK/log"
  if ! PATH="$WORK/bin:$PATH" STUB_LOG="$WORK/log" bash "$SCRIPT_DIR/sandbox-image.sh" "$@" > "$WORK/out" 2>&1; then
    cat "$WORK/out" >&2
    fail "$name: the script failed"
  fi
}

build_arg() {
  grep '^build ' "$WORK/log" | tr ' ' '\n' | sed -n "s/^$1=//p"
}

queried() {
  grep -c "^npmview $1\$" "$WORK/log" || true
}

# A hyphen range and a union start with a digit, so a glob test for "exact"
# lets them through. Both must be resolved, and only the result built.
run_case hyphen --claude-version '2.1.0 - 2.1.999'
[ "$(queried '@anthropic-ai/claude-code@2.1.0 - 2.1.999')" = 1 ] || fail "hyphen range was not resolved"
[ "$(build_arg CLAUDE_CODE_VERSION)" = 9.9.2 ] || fail "hyphen range built as '$(build_arg CLAUDE_CODE_VERSION)', want the highest match 9.9.2"

run_case union --claude-version '2.1.0 || 2.1.289'
[ "$(queried '@anthropic-ai/claude-code@2.1.0 || 2.1.289')" = 1 ] || fail "union range was not resolved"
[ "$(build_arg CLAUDE_CODE_VERSION)" = 9.9.2 ] || fail "union range built as '$(build_arg CLAUDE_CODE_VERSION)'"

run_case caret --claude-version '^2.1.0'
[ "$(build_arg CLAUDE_CODE_VERSION)" = 9.9.2 ] || fail "caret range built as '$(build_arg CLAUDE_CODE_VERSION)'"

# The default is a tag, resolved the same way.
run_case latest
[ "$(queried '@anthropic-ai/claude-code@latest')" = 1 ] || fail "latest was not resolved"
[ "$(build_arg CLAUDE_CODE_VERSION)" = 9.9.0 ] || fail "latest built as '$(build_arg CLAUDE_CODE_VERSION)'"

# An exact version (with or without a prerelease) goes straight through.
run_case exact --claude-version 2.1.263
[ "$(grep -c '^npmview ' "$WORK/log" || true)" = 0 ] || fail "an exact version was looked up"
[ "$(build_arg CLAUDE_CODE_VERSION)" = 2.1.263 ] || fail "exact version built as '$(build_arg CLAUDE_CODE_VERSION)'"

run_case prerelease --claude-version 2.1.263-beta.1
[ "$(build_arg CLAUDE_CODE_VERSION)" = 2.1.263-beta.1 ] || fail "prerelease built as '$(build_arg CLAUDE_CODE_VERSION)'"

# The extra agents are resolved too.
run_case agents --with codex,opencode
[ "$(build_arg CODEX_VERSION)" = 9.9.0 ] || fail "codex built as '$(build_arg CODEX_VERSION)'"
[ "$(build_arg OPENCODE_VERSION)" = 9.9.0 ] || fail "opencode built as '$(build_arg OPENCODE_VERSION)'"

# A spec npm cannot resolve stops the build instead of passing it on.
: > "$WORK/log"
if PATH="$WORK/bin:$PATH" STUB_LOG="$WORK/log" bash "$SCRIPT_DIR/sandbox-image.sh" --claude-version missing > "$WORK/out" 2>&1; then
  fail "an unresolvable version did not stop the script"
fi
grep -q '^build ' "$WORK/log" && fail "an unresolvable version still reached docker build"

echo "ok: sandbox-image.sh resolves every non-exact version before building"
