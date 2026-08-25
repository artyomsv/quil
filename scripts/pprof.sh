#!/usr/bin/env bash
set -euo pipefail

# Fetch and analyse a pprof profile from a running quil or quild.
#
# Two steps rather than one, because the host has no Go toolchain: `go tool
# pprof http://...` would need Go on the host to fetch AND symbolize, and
# pointing the container at the host's loopback does not work — 127.0.0.1
# inside a container is the container. So the profile is fetched HERE with
# curl, written into the project directory, and analysed in the same Docker
# image dev.sh uses.
#
# Go's pprof format carries function names in the profile itself, so the
# analysis step needs no binary and no symbol server.
#
# Requires the target process to have been started with QUIL_PPROF set:
#
#   $env:QUIL_PPROF = "6060"; .\quil.exe          # TUI
#   $env:QUIL_PPROF = "6061"; .\quild.exe         # daemon (needs its OWN port)
#
# Usage:
#   ./scripts/pprof.sh <port> [profile] [seconds]
#
#   profile  heap | goroutine | profile | allocs | mutex | block  (default heap)
#   seconds  only meaningful for `profile` (CPU), default 30
#
# Examples:
#   ./scripts/pprof.sh 6060 profile 30   # 30 s CPU profile of the TUI
#   ./scripts/pprof.sh 6060 heap         # what is holding the RSS
#   ./scripts/pprof.sh 6060 goroutine    # leaked goroutines

GO_IMAGE="golang:1.25-alpine"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"

PORT="${1:-}"
PROFILE="${2:-heap}"
SECONDS_ARG="${3:-30}"

if [ -z "$PORT" ]; then
  sed -n '5,32p' "$0" | sed 's|^# \{0,1\}||'
  exit 2
fi

case "$PROFILE" in
  heap|goroutine|profile|allocs|mutex|block|threadcreate) ;;
  *)
    echo "unknown profile '$PROFILE' (heap|goroutine|profile|allocs|mutex|block|threadcreate)" >&2
    exit 2
    ;;
esac

URL="http://127.0.0.1:${PORT}/debug/pprof/${PROFILE}"
if [ "$PROFILE" = "profile" ]; then
  URL="${URL}?seconds=${SECONDS_ARG}"
fi

OUT_DIR="${PROJECT_DIR}/.pprof"
mkdir -p "$OUT_DIR"
# No timestamp in the name: `date` output differs across the shells this runs
# under, and a stable name means the analysis command below never has to be
# edited. Re-running overwrites, which is what iterating on one process wants.
OUT_FILE="${OUT_DIR}/${PROFILE}.pb.gz"

echo "fetching $URL"
if [ "$PROFILE" = "profile" ]; then
  echo "  (CPU profile — this blocks for ${SECONDS_ARG}s while it samples)"
fi

# --max-time generously exceeds the sample window so a CPU profile is never cut
# off by the fetch itself. -f makes an HTTP error a non-zero exit instead of a
# saved error page that pprof would later fail to parse.
if ! curl -fsS --max-time "$((SECONDS_ARG + 60))" -o "$OUT_FILE" "$URL"; then
  echo >&2
  echo "fetch failed. Most likely causes:" >&2
  echo "  * the process was not started with QUIL_PPROF set" >&2
  echo "  * it is listening on a different port (grep quil.log for 'pprof listening')" >&2
  echo "  * TUI and daemon were given the SAME port, so the second failed to bind" >&2
  exit 1
fi

echo "saved $OUT_FILE ($(wc -c <"$OUT_FILE" | tr -d ' ') bytes)"
echo

# The DOUBLE slash on //src is not a typo and is not optional. Under MSYS (Git
# Bash, which is how this runs on Windows) a leading single slash in an argument
# is rewritten to a Windows path before docker ever sees it: "/src/.pprof/x"
# arrives as "C:/Program Files/Git/src/.pprof/x", and pprof then treats it as a
# URL and tries to resolve a host called "C". A leading "//" is the MSYS escape
# that survives as a single slash. dev.sh uses the same form on -w for the same
# reason.
docker run --rm \
  -v "${PROJECT_DIR}:/src" \
  -v quil-gomod:/go/pkg/mod \
  -v quil-gocache:/root/.cache/go-build \
  -w //src "${GO_IMAGE}" \
  go tool pprof -top -nodecount=35 "//src/.pprof/${PROFILE}.pb.gz"

echo
echo "Other views over the same file:"
echo "  ./scripts/pprof-view.sh ${PROFILE} tree    # caller/callee tree"
echo "  ./scripts/pprof-view.sh ${PROFILE} list <regex>"
