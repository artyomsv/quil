#!/usr/bin/env bash
set -euo pipefail

# Build the local container image Quil's sandbox panes run in.
#
# Quil publishes no image, so this builds one on YOUR machine from
# docker/sandbox/Dockerfile. Nothing is pulled from a registry except the base
# image, and nothing is pushed anywhere.
#
# Why a script and not a bare `docker build` line in the docs: the docs' recipe
# was copied by hand and shipped without the install step in it, producing an
# image with no `claude` on PATH that failed at spawn with an error the same
# page described three paragraphs later. A command that is RUN cannot drift
# from the recipe the way a command that is READ can.
#
# Usage:
#   scripts/sandbox-image.sh                       # build quil-sandbox:latest
#   scripts/sandbox-image.sh --tag my-image:v1     # different tag
#   scripts/sandbox-image.sh --claude-version 2.1.263
#   scripts/sandbox-image.sh --base node:22-bookworm
#   scripts/sandbox-image.sh --check               # verify an existing image
#
# After building, point the setup dialog at it once:
#   [sandbox]
#     default_image = "quil-sandbox:latest"
# in $QUIL_HOME/config.toml.

# `pwd -W` is what scripts/dev.sh uses and it is load-bearing on Windows: under
# Git Bash a plain `pwd` yields an MSYS path like /e/Projects/..., and Docker
# Desktop is a native Windows process that cannot resolve one — the build fails
# with "unable to prepare context: path not found". `pwd -W` prints the real
# E:/... form; it does not exist elsewhere, hence the fallback.
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"
CONTEXT_DIR="$PROJECT_DIR/docker/sandbox"

TAG="quil-sandbox:latest"
CLAUDE_VERSION="latest"
BASE_IMAGE="node:22-bookworm-slim"
CHECK_ONLY=0
# AGENTS are the extra agents installed beside Claude Code, space-separated.
# Empty by default: each is another vendor's package in the image, and the pane
# type you pick is the one that has to be present, not all of them.
AGENTS=""

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# Written out rather than sed'ing a line range out of the header comment: that
# range drifts silently the moment a comment line is added above it, which is
# how --help ended up printing the rationale paragraph instead of the usage.
usage() {
  cat <<'EOF'
Build the local container image Quil's sandbox panes run in.

  scripts/sandbox-image.sh                     build quil-sandbox:latest
  scripts/sandbox-image.sh --tag my:v1         build under a different tag
  scripts/sandbox-image.sh --claude-version X  pin the Claude Code version
  scripts/sandbox-image.sh --base IMAGE        use a different base image
  scripts/sandbox-image.sh --with codex,opencode   add other agents
  scripts/sandbox-image.sh --check --tag T     verify an image you already have

Quil publishes no image and pulls none. This builds one on YOUR machine from
docker/sandbox/Dockerfile, then verifies it provides a non-root user, a working
`claude`, and `git`.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --tag)            [ $# -ge 2 ] || die "--tag needs a value";            TAG="$2"; shift 2 ;;
    --claude-version) [ $# -ge 2 ] || die "--claude-version needs a value"; CLAUDE_VERSION="$2"; shift 2 ;;
    --base)           [ $# -ge 2 ] || die "--base needs a value";           BASE_IMAGE="$2"; shift 2 ;;
    --with)           [ $# -ge 2 ] || die "--with needs a value";           AGENTS="$(printf %s "$2" | tr ',' ' ')"; shift 2 ;;
    --check)          CHECK_ONLY=1; shift ;;
    -h|--help)        usage; exit 0 ;;
    *)                die "unknown argument: $1" ;;
  esac
done

command -v docker >/dev/null 2>&1 || die "docker is not on PATH"

# The same check the daemon's capability probe makes. Docker Desktop in
# Windows-containers mode answers `docker info` perfectly well and then fails
# every linux image at run, so a build that "succeeds" there produces something
# no sandbox pane can start.
os_type="$(docker info --format '{{.OSType}}' 2>/dev/null || true)"
[ -n "$os_type" ] || die "docker is installed but no engine is reachable — is Docker Desktop running?"
[ "$os_type" = "linux" ] || die "docker is running $os_type containers; sandbox panes need linux containers"

# verify_image asserts the two properties internal/sandbox documents as
# required, by ASKING THE IMAGE rather than trusting the build log. Both have
# already shipped broken once in the hand-written recipe this replaces.
verify_image() {
  image="$1"
  printf 'verifying %s\n' "$image"

  who="$(docker run --rm --entrypoint sh "$image" -c 'id -un' 2>/dev/null || true)"
  [ -n "$who" ] || die "could not run a shell in $image"
  [ "$who" != "root" ] || die "$image runs as root; Claude Code refuses --dangerously-skip-permissions as root"

  # Every agent the image was asked for, not just claude: a pane whose plugin
  # binary is missing dies at spawn with `executable file not found in $PATH`,
  # and the whole point of verifying here is that the build log cannot tell you
  # that. `codex` and `opencode` are checked with --version too, so a package
  # that installed but cannot run is caught as well as an absent one.
  for a in claude ${AGENTS}; do
    ver="$(docker run --rm --entrypoint sh "$image" -c "$a --version" 2>/dev/null || true)"
    [ -n "$ver" ] || die "no working '$a' on PATH in $image"
    printf '  %-9s: %s\n' "$a" "$ver"
  done

  git_ver="$(docker run --rm --entrypoint sh "$image" -c 'git --version' 2>/dev/null || true)"
  [ -n "$git_ver" ] || die "no 'git' in $image; a sandbox pane's checkout is a linked git worktree"

  printf '  %-9s: %s\n  %-9s: %s\n' "git" "$git_ver" "user" "$who"
}

if [ "$CHECK_ONLY" -eq 1 ]; then
  verify_image "$TAG"
  printf 'ok: %s satisfies what a sandbox pane needs\n' "$TAG"
  exit 0
fi

[ -f "$CONTEXT_DIR/Dockerfile" ] || die "missing $CONTEXT_DIR/Dockerfile"

# exact_version turns a version SPEC into the exact version npm would install.
#
# This is what makes a rebuild update anything at all. Docker keys a RUN layer
# on its text, so `npm install pkg@latest` is the same line every time and a
# rebuild reuses the cached layer — the agent stays on whatever version was
# current at the first build, and the build still reports success. An exact
# number changes the line when a release lands and keeps the cache when none
# has. Asked from inside the base image, which the build needs anyway, so no
# host npm is required.
#
# The fast path is a FULL match of an exact semantic version, never a glob: a
# pattern like [0-9]*.[0-9]*.[0-9]* also accepts the ranges `2.1.0 - 2.1.999`
# and `2.1.0 || 2.1.289`, which would then reach the build unresolved and
# bring back the stale cache this function exists to prevent.
EXACT_SEMVER='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
exact_version() {
  pkg="$1"; spec="$2"
  if [[ "$spec" =~ $EXACT_SEMVER ]]; then
    printf '%s' "$spec"
    return
  fi
  # A tag answers with one bare line; a range answers one `pkg@v 'v'` line
  # per match, highest last — so the last field of the last line, unquoted.
  v="$(docker run --rm "$BASE_IMAGE" npm view "$pkg@$spec" version 2>/dev/null | tail -n 1 | awk '{print $NF}' | tr -d "'\r" || true)"
  if [[ "$v" =~ $EXACT_SEMVER ]]; then
    printf '%s' "$v"
    return
  fi
  die "could not resolve $pkg@$spec to a version (no network, or no npm in $BASE_IMAGE); pass an exact version"
}

# installed_version prints what the image under TAG has now, or nothing.
installed_version() {
  docker image inspect "$TAG" >/dev/null 2>&1 || return 0
  docker run --rm --entrypoint sh "$TAG" -c "$1 --version" 2>/dev/null | head -n 1 | tr -d '\r' || true
}

CLAUDE_EXACT="$(exact_version @anthropic-ai/claude-code "$CLAUDE_VERSION")"
CODEX_EXACT="latest"
OPENCODE_EXACT="latest"
for a in $AGENTS; do
  case "$a" in
    codex)    CODEX_EXACT="$(exact_version @openai/codex latest)" ;;
    opencode) OPENCODE_EXACT="$(exact_version opencode-ai latest)" ;;
  esac
done

report_change() {
  name="$1"; before="$2"; after="$3"
  if [ -z "$before" ]; then
    printf '  %-9s: %s (new image)\n' "$name" "$after"
  elif [ "$before" = "$after" ]; then
    printf '  %-9s: %s (already current)\n' "$name" "$after"
  else
    printf '  %-9s: %s -> %s\n' "$name" "$before" "$after"
  fi
}

printf 'building %s (base=%s agents=%s)\n' "$TAG" "$BASE_IMAGE" "${AGENTS:-none}"
report_change claude "$(installed_version claude | awk '{print $1}')" "$CLAUDE_EXACT"
for a in $AGENTS; do
  case "$a" in
    codex)    report_change codex "$(installed_version codex | awk '{print $NF}')" "$CODEX_EXACT" ;;
    opencode) report_change opencode "$(installed_version opencode | awk '{print $NF}')" "$OPENCODE_EXACT" ;;
  esac
done

docker build \
  --build-arg "BASE_IMAGE=$BASE_IMAGE" \
  --build-arg "CLAUDE_CODE_VERSION=$CLAUDE_EXACT" \
  --build-arg "CODEX_VERSION=$CODEX_EXACT" \
  --build-arg "OPENCODE_VERSION=$OPENCODE_EXACT" \
  --build-arg "AGENTS=$AGENTS" \
  -t "$TAG" \
  "$CONTEXT_DIR"

verify_image "$TAG"

cat <<EOF

Built $TAG.

Point the setup dialog at it by adding this to \$QUIL_HOME/config.toml:

  [sandbox]
    default_image = "$TAG"

Or type the image name into the dialog's "Run in a Docker container" row.

Note: this image has no network egress restriction. Quil passes no --network
or --cap-add, so a pane can reach anything the host can. If you need a
default-deny firewall, start from Anthropic's example dev container instead.
EOF
