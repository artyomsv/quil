#!/usr/bin/env bash
set -euo pipefail

# Docker-based development commands — no local Go required.

GO_IMAGE="golang:1.25-alpine"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"

# TARGET_GOOS / TARGET_GOARCH are the HOST platform by default, not Windows.
#
# `build` used to hardcode GOOS=windows for all seven binaries. That is correct
# on the machine this project is developed on and wrong on every other one: a
# macOS or Linux checkout ended up with seven executables that cannot run on the
# machine that just built them, so `build` could never be followed by running
# the thing it built.
#
# It also tripped endpoint security. An unsigned PE written into a home
# directory by a Docker helper process is precisely the shape a static-AI
# scanner flags, and quil-activate.exe — 2 MB, windowsgui-linked, no console —
# is the most suspicious-looking of the set. A binary the host cannot execute
# has no upside to weigh against that.
#
# Overridable, so a Windows build is one variable away from any host:
#   QUIL_BUILD_GOOS=windows ./scripts/dev.sh build
# `cross` is unchanged and still emits every platform including Windows — that
# is what it is for.
host_goos() {
  case "$(uname -s)" in
    Darwin)                          echo darwin  ;;
    Linux)                           echo linux   ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT) echo windows ;;
    # Unknown kernels get linux rather than an error: the container this runs in
    # is Linux, so it is the one target guaranteed to compile.
    *)                               echo linux   ;;
  esac
}

host_goarch() {
  case "$(uname -m)" in
    arm64|aarch64) echo arm64 ;;
    x86_64|amd64)  echo amd64 ;;
    *)             echo amd64 ;;
  esac
}

TARGET_GOOS="${QUIL_BUILD_GOOS:-$(host_goos)}"
TARGET_GOARCH="${QUIL_BUILD_GOARCH:-$(host_goarch)}"

# EXE is the filename suffix, and only that. .gitignore already lists both
# spellings of all six names, and findDaemonBinary appends .exe itself on
# Windows, so -X main.daemonBinary= stays suffix-less on every target.
if [ "$TARGET_GOOS" = "windows" ]; then EXE=".exe"; else EXE=""; fi
# quil-gomod persists downloaded modules; quil-gocache persists COMPILED
# packages. The build cache is the load-bearing one: without it every
# `docker run --rm` starts with an empty /root/.cache/go-build and recompiles
# the whole dependency tree, so a one-line edit costs a full-tree build. With
# it, a repeat run only recompiles what changed. Race builds keep their own
# entries in the same cache, so test-race benefits too.
DOCKER_RUN="docker run --rm -v ${PROJECT_DIR}:/src -v quil-gomod:/go/pkg/mod -v quil-gocache:/root/.cache/go-build -w //src ${GO_IMAGE}"

# RACE_IMAGE is GO_IMAGE plus the C toolchain the race detector needs. Built
# once and reused, because `apk add gcc musl-dev` inside every test-race run
# is a network fetch and install paid before compilation even starts.
RACE_IMAGE="quil-race:$(printf '%s' "$GO_IMAGE" | tr ':/' '--')"
DOCKER_RUN_RACE="docker run --rm -v ${PROJECT_DIR}:/src -v quil-gomod:/go/pkg/mod -v quil-gocache:/root/.cache/go-build -w //src ${RACE_IMAGE}"

ensure_race_image() {
  if ! docker image inspect "$RACE_IMAGE" >/dev/null 2>&1; then
    echo "building $RACE_IMAGE (one-off; cached for later runs)" >&2
    printf 'FROM %s\nRUN apk add --no-cache gcc musl-dev\n' "$GO_IMAGE" \
      | docker build -q -t "$RACE_IMAGE" - >/dev/null
  fi
}

# pkg_target turns an optional package argument into a go pattern.
#   dev.sh test                 -> ./...
#   dev.sh test internal/tui    -> ./internal/tui/...
# Narrowing to the package you actually changed is the difference between
# recompiling 26 packages and recompiling one.
pkg_target() {
  if [ -z "${1:-}" ]; then
    echo "./..."
  else
    echo "./${1#./}/..." | sed 's|//*\.\.\.$|/...|'
  fi
}

# BUILT_BINARIES are every file `build` writes and `clean` removes, in this
# project directory. Production installs live elsewhere and are never touched.
#
# BOTH spellings are listed, not just this host's. A checkout built for Windows
# last week and for darwin today holds both sets, and the probe below skips
# names that do not exist — so the union costs nothing and catches a holder of
# the other set, which a host-scoped list would walk straight past. The
# suffix-less dev and debug names were missing here entirely, which is why a
# darwin quil-dev survived every `clean` ever run.
BUILT_BINARIES="quil quild quil-dev quild-dev quil-debug quild-debug"
BUILT_BINARIES="$BUILT_BINARIES quil.exe quild.exe quil-dev.exe quild-dev.exe"
BUILT_BINARIES="$BUILT_BINARIES quil-debug.exe quild-debug.exe quil-activate.exe"

# refuse_if_binaries_held stops a build that would silently half-finish.
#
# Neither platform lets you overwrite a running executable: Windows fails the
# open with a sharing violation, Linux returns ETXTBSY. The failure itself is
# not the problem — the ORDER is. The chain below builds six binaries in
# sequence with &&, so a holder on the Nth leaves the first N-1 freshly built
# and the rest stale. A new TUI beside a stale daemon fails the version gate at
# launch, which reads as a bug in whatever you were working on rather than as a
# build that ran halfway.
#
# Detection is a non-destructive probe rather than a process query: opening each
# target for APPEND asks the operating system the exact question the build is
# about to ask, and append never truncates. That catches every holder — the
# daemon, a TUI left running, a debug variant, an antivirus scanner — without
# enumerating processes or trusting a pid file.
#
# An earlier version read .quil/quild.pid and looked for a daemon. It missed a
# dev TUI holding quil-dev.exe and let exactly the half-build above through on
# its first real use, which is why this asks the filesystem instead of guessing
# who the holder might be.
#
# There is no override flag on purpose. "Build anyway" produces precisely the
# mismatched pair this exists to prevent.
refuse_if_binaries_held() {
  held=""
  for name in $BUILT_BINARIES; do
    target="$PROJECT_DIR/$name"
    [ -f "$target" ] || continue
    # Subshell so the descriptor closes with it; >> never truncates, so a
    # writable target is left byte-identical.
    if ! (exec 3>>"$target") 2>/dev/null; then
      held="$held $name"
    fi
  done
  [ -z "$held" ] || {
    printf '\n  These binaries are in use and cannot be rebuilt:\n' >&2
    for name in $held; do printf '    %s\n' "$name" >&2; done
    cat >&2 <<EOF

  Building anyway would rewrite the ones that are free and fail on these,
  leaving a mismatched set — typically a new TUI against a stale daemon,
  which then fails the version gate at launch.

  Close any Quil started from this directory. If a dev daemon is running:

    QUIL_HOME="$PROJECT_DIR/.quil" "$PROJECT_DIR/quil-dev$EXE" daemon stop

  Only files in $PROJECT_DIR were checked.
  A production install elsewhere is untouched.

EOF
    exit 1
  }
}

case "${1:-help}" in
  build)
    # Cheap, host-side, and it fails BEFORE the Docker run so a docs-size
    # problem costs a second rather than a full build.
    sh "$PROJECT_DIR/scripts/check-claude-md-size.sh"
    refuse_if_binaries_held
    echo "building for $TARGET_GOOS/$TARGET_GOARCH (override: QUIL_BUILD_GOOS / QUIL_BUILD_GOARCH)" >&2

    # WIN_PREP is the Windows-only prologue, and every piece of it is gated
    # behind //go:build windows in the tree:
    #   - fetch-conpty.sh downloads the OpenConsole pair that
    #     internal/pty/winconpty/embed_windows.go go:embeds;
    #   - go-winres writes the rsrc_windows_*.syso the linker picks up only
    #     when GOOS=windows.
    # A darwin or linux build can neither use nor link either one, and each
    # costs a network fetch, so the whole block is skipped rather than made
    # harmless. It ends in `&&` so it chains, and is empty on every other
    # target. VER is computed BEFORE it, because go-winres stamps it.
    WIN_PREP=""
    ACTIVATE_STEP=""
    if [ "$TARGET_GOOS" = "windows" ]; then
      # go-winres runs INSIDE the container, so it is installed for the
      # container's platform: under the exported GOOS=windows, `go install`
      # cross-compiles it into /go/bin/windows_amd64/, off PATH.
      WIN_PREP="apk add --no-cache curl unzip >/dev/null 2>&1 && sh scripts/fetch-conpty.sh && GOOS= GOARCH= go install github.com/tc-hib/go-winres@v0.3.3 && go-winres make --in winres/winres.json --out cmd/quil/rsrc --product-version \$VER --file-version \$VER && go-winres make --in winres/winres.json --out cmd/quild/rsrc --product-version \$VER --file-version \$VER &&"
      # quil-activate is a Windows URI handler. Its non-Windows file is a stub
      # that prints an error and exits 1 — building it elsewhere produces a
      # 2 MB executable whose only behaviour is to refuse.
      ACTIVATE_STEP="&& go build -ldflags \"\$F -H windowsgui\" -o quil-activate.exe ./cmd/quil-activate"
    fi

    # GOOS/GOARCH are exported once rather than prefixed onto each build: seven
    # copies of the same pair is seven chances for one to drift.
    $DOCKER_RUN sh -c "\
      export GOOS=$TARGET_GOOS GOARCH=$TARGET_GOARCH && \
      VER=\$(cat VERSION) && \
      $WIN_PREP \
      F=\"-s -w -X main.version=\$VER\" && \
      F_DEV=\"\$F -X main.buildDevMode=true -X main.buildLogLevel=debug -X main.daemonBinary=quild-dev -X main.buildUpdatesOff=true\" && \
      F_DBG=\"\$F -X main.buildLogLevel=debug -X main.daemonBinary=quild-debug -X main.buildUpdatesOff=true\" && \
      go build -ldflags \"\$F_DEV\" -o quil-dev$EXE    ./cmd/quil  && \
      go build -ldflags \"\$F_DEV\" -o quild-dev$EXE   ./cmd/quild && \
      go build -ldflags \"\$F_DBG\" -o quil-debug$EXE  ./cmd/quil  && \
      go build -ldflags \"\$F_DBG\" -o quild-debug$EXE ./cmd/quild && \
      go build -ldflags \"\$F\"     -o quil$EXE        ./cmd/quil  && \
      go build -ldflags \"\$F\"     -o quild$EXE       ./cmd/quild $ACTIVATE_STEP"
    ;;

  test)
    $DOCKER_RUN go test "$(pkg_target "${2:-}")"
    ;;

  # Integration-tagged tests are invisible to `test` and to CI's `go test ./...`
  # — the build tag excludes them from compilation, so they are not even
  # type-checked. Every one of them exists to cover what a unit test structurally
  # cannot (a handler being WIRED UP, a snapshot reaching disk and coming back),
  # which is exactly the class of failure that then ships unnoticed.
  test-integration)
    $DOCKER_RUN go test -tags=integration "$(pkg_target "${2:-}")"
    ;;

  test-race)
    ensure_race_image
    $DOCKER_RUN_RACE sh -c \
      "CGO_ENABLED=1 go test -race $(pkg_target "${2:-}")"
    ;;

  # Benchmarks are excluded from `test` (go test runs no benchmarks without
  # -bench), so they cost nothing on the normal loop and are only paid here.
  #
  # -count 6 gives benchstat enough samples to report a delta with a useful
  # confidence interval (its Mann-Whitney U test can reach p<0.05 at n=4, so
  # this is headroom, not a floor). -cpu 1 keeps runs comparable, since the
  # benchmarked code is single-threaded and GOMAXPROCS otherwise varies with
  # whatever else the machine is doing. -run '^$' skips tests so a slow suite
  # does not pad the timing run.
  #
  # Results land in bench/<label>.txt (gitignored). The workflow this exists for:
  #   ./scripts/dev.sh bench before     # on the unchanged code
  #   ...implement...
  #   ./scripts/dev.sh bench after      # prints the comparison automatically
  #
  # QUIL_BENCH_BASE names the baseline to compare against (default "before"), so
  # a baseline captured under another label is not silently skipped.
  bench)
    label="${2:-bench}"
    base="${QUIL_BENCH_BASE:-before}"
    # Both values are interpolated into a `sh -c` string that runs inside the
    # container with the project bind-mounted read-write, and the label is also
    # used as a path. Unvalidated, `bench 'x;id>/src/pwn'` executes in the
    # container and `bench ../../foo` writes outside bench/. Whoever runs this
    # already has the shell, so it is robustness rather than a privilege
    # boundary — but a label is a label, and rejecting is one line.
    for v in "$label" "$base"; do
      case "$v" in
        "" | *[!A-Za-z0-9._-]* | .* )
          echo "dev.sh bench: invalid label '$v' (allowed: A-Za-z0-9._- , not starting with '.')" >&2
          exit 1
          ;;
      esac
    done
    pkg="$(pkg_target "${3:-internal/ipc}")"
    mkdir -p "$PROJECT_DIR/bench"
    out="bench/${label}.txt"
    echo "benchmarking $pkg -> $out" >&2
    $DOCKER_RUN sh -c \
      "go test -run '^\$' -bench . -benchmem -count 6 -cpu 1 $pkg | tee $out"
    # benchstat is best-effort: comparison is a convenience, the .txt files are
    # the artifact, and a machine without network must still get its numbers.
    # The version is PINNED — @latest re-resolves against the proxy on every
    # run, which both needs network and can change the tool under a comparison.
    if [ "$label" != "$base" ] && [ -f "$PROJECT_DIR/bench/${base}.txt" ]; then
      echo "" >&2
      echo "=== benchstat ${base}.txt -> ${label}.txt ===" >&2
      # git is required to FETCH benchstat (the module proxy path goes through
      # a VCS checkout) and the golang:alpine image does not ship it. Installed
      # here rather than in a derived image because it is needed once per cold
      # module cache, and the cache is a persisted volume.
      $DOCKER_RUN sh -c \
        "command -v git >/dev/null 2>&1 || apk add --no-cache git >/dev/null 2>&1; \
         go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260813145340-fd4a688df892 bench/${base}.txt $out" \
        || echo "(benchstat unavailable — compare bench/${base}.txt and $out by hand)" >&2
    elif [ "$label" != "$base" ]; then
      echo "(no bench/${base}.txt — capture a baseline first, or set QUIL_BENCH_BASE)" >&2
    fi
    ;;

  vet)
    $DOCKER_RUN go vet "$(pkg_target "${2:-}")"
    ;;

  cross)
    $DOCKER_RUN sh -c "\
      apk add --no-cache curl unzip >/dev/null 2>&1 && \
      sh scripts/fetch-conpty.sh && \
      go install github.com/tc-hib/go-winres@v0.3.3 && \
      VER=\$(cat VERSION) && \
      go-winres make --in winres/winres.json --out cmd/quil/rsrc --product-version \$VER --file-version \$VER && \
      go-winres make --in winres/winres.json --out cmd/quild/rsrc --product-version \$VER --file-version \$VER && \
      LDFLAGS=\"-X main.version=\$VER\" && \
      mkdir -p dist && \
      GOOS=linux   GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/quil-linux-amd64        ./cmd/quil && \
      GOOS=linux   GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/quild-linux-amd64       ./cmd/quild && \
      GOOS=linux   GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/quil-linux-arm64        ./cmd/quil && \
      GOOS=linux   GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/quild-linux-arm64       ./cmd/quild && \
      GOOS=darwin  GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/quil-darwin-amd64       ./cmd/quil && \
      GOOS=darwin  GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/quild-darwin-amd64      ./cmd/quild && \
      GOOS=darwin  GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/quil-darwin-arm64       ./cmd/quil && \
      GOOS=darwin  GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/quild-darwin-arm64      ./cmd/quild && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/quil-windows-amd64.exe  ./cmd/quil && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/quild-windows-amd64.exe ./cmd/quild && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS -H windowsgui\" -o dist/quil-activate-windows-amd64.exe ./cmd/quil-activate"
    ;;

  image)
    docker build -t quil:latest "$PROJECT_DIR"
    ;;

  clean)
    # Same reason as build: rm cannot remove a held executable, and `set -e`
    # would abort the cleanup partway through.
    refuse_if_binaries_held
    # Driven off BUILT_BINARIES so the two lists cannot drift. The hand-written
    # list this replaces named only the .exe spellings plus bare quil/quild, so
    # a native quil-dev built on macOS was never removed by `clean` at all.
    for name in $BUILT_BINARIES; do rm -f "$PROJECT_DIR/$name"; done
    rm -f "$PROJECT_DIR"/cmd/quil/rsrc*.syso "$PROJECT_DIR"/cmd/quild/rsrc*.syso
    rm -rf "$PROJECT_DIR/dist/"
    ;;

  docs-size)
    sh "$PROJECT_DIR/scripts/check-claude-md-size.sh"
    echo "Agent-context files are within their size limits."
    ;;

  help|*)
    echo "Usage: ./dev.sh <command>"
    echo ""
    echo "Commands:"
    echo "  build          Build prod, dev and debug for THIS host ($TARGET_GOOS/$TARGET_GOARCH)"
    echo "                 Windows also gets quil-activate.exe. Override the target with"
    echo "                 QUIL_BUILD_GOOS=windows QUIL_BUILD_GOARCH=amd64 ./scripts/dev.sh build"
    echo "  test [pkg]     Run tests (all, or just ./<pkg>/...)"
    echo "  test-race [pkg]  Run tests with race detector"
    echo "  bench [label] [pkg]  Run benchmarks -> bench/<label>.txt (default pkg: internal/ipc)"
    echo "  vet [pkg]      Run go vet"
    echo "  cross          Cross-compile for all platforms"
    echo "  image          Build Docker image (scratch-based)"
    echo "  clean          Remove built binaries"
    echo "  docs-size      Check .claude/ agent-context files against size limits"
    ;;
esac
