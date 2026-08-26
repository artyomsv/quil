#!/usr/bin/env bash
set -euo pipefail

# Re-analyse a profile already fetched by pprof.sh, without re-fetching.
#
# Separate from pprof.sh because fetching and viewing have very different
# costs: a CPU profile takes 30 s of wall clock and perturbs the process being
# measured, while looking at it again from another angle is free. Iterating on
# `-top` vs `-tree` vs `-list` should never re-sample.
#
# Usage:
#   ./scripts/pprof-view.sh <profile> <view> [arg]
#
#   profile  the name passed to pprof.sh (heap, profile, goroutine, ...)
#   view     top | tree | traces | list | peek
#   arg      a regex, required for `list` and `peek`
#
# Examples:
#   ./scripts/pprof-view.sh profile tree
#   ./scripts/pprof-view.sh heap list 'applyWorkspaceState'
#   ./scripts/pprof-view.sh profile peek 'lipgloss'

GO_IMAGE="golang:1.25-alpine"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"

PROFILE="${1:-}"
VIEW="${2:-top}"
ARG="${3:-}"

if [ -z "$PROFILE" ]; then
  sed -n '5,22p' "$0" | sed 's|^# \{0,1\}||'
  exit 2
fi

# Same allowlist pprof.sh applies. Without it a "../" in the name reaches both
# the -f test and the container path — constrained to a directory that is
# already bind-mounted read-write, so nothing escalates, but two sibling scripts
# disagreeing about what a profile name may contain is how the next one gets it
# wrong.
case "$PROFILE" in
  heap|goroutine|profile|allocs|mutex|block|threadcreate) ;;
  *)
    echo "unknown profile '$PROFILE' (heap|goroutine|profile|allocs|mutex|block|threadcreate)" >&2
    exit 2
    ;;
esac

FILE="${PROJECT_DIR}/.pprof/${PROFILE}.pb.gz"
if [ ! -f "$FILE" ]; then
  echo "no saved profile at $FILE" >&2
  echo "fetch one first: ./scripts/pprof.sh <port> ${PROFILE}" >&2
  exit 1
fi

# An ARRAY, not a string. An unquoted "$FLAGS" word-splits and glob-expands into
# pprof's argv, so a regex containing spaces silently becomes extra FLAGS —
# `pprof-view.sh heap list 'x -proto -output=/src/f'` would write a file into the
# bind-mounted repo. Quoting "${FLAGS[@]}" keeps the regex one argument.
case "$VIEW" in
  top)    FLAGS=(-top -nodecount=35) ;;
  tree)   FLAGS=(-tree -nodecount=35) ;;
  traces) FLAGS=(-traces -nodecount=15) ;;
  list)
    if [ -z "$ARG" ]; then echo "list needs a regex" >&2; exit 2; fi
    FLAGS=(-list="$ARG")
    ;;
  peek)
    if [ -z "$ARG" ]; then echo "peek needs a regex" >&2; exit 2; fi
    FLAGS=(-peek="$ARG")
    ;;
  *)
    echo "unknown view '$VIEW' (top|tree|traces|list|peek)" >&2
    exit 2
    ;;
esac

# The double slash on //src is an MSYS escape, not a typo — see pprof.sh.
docker run --rm \
  -v "${PROJECT_DIR}:/src" \
  -v quil-gomod:/go/pkg/mod \
  -v quil-gocache:/root/.cache/go-build \
  -w //src "${GO_IMAGE}" \
  go tool pprof "${FLAGS[@]}" "//src/.pprof/${PROFILE}.pb.gz"
