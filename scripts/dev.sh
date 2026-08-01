#!/usr/bin/env bash
set -euo pipefail

# Docker-based development commands — no local Go required.

GO_IMAGE="golang:1.25-alpine"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"
DOCKER_RUN="docker run --rm -v ${PROJECT_DIR}:/src -v quil-gomod:/go/pkg/mod -w //src ${GO_IMAGE}"

# BUILT_BINARIES are every file `build` writes and `clean` removes, in this
# project directory. Production installs live elsewhere and are never touched.
BUILT_BINARIES="quil-dev.exe quild-dev.exe quil-debug.exe quild-debug.exe quil.exe quild.exe quil quild"

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

    QUIL_HOME="$PROJECT_DIR/.quil" "$PROJECT_DIR/quil-dev.exe" daemon stop

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
    $DOCKER_RUN sh -c "\
      apk add --no-cache curl unzip >/dev/null 2>&1 && \
      sh scripts/fetch-conpty.sh && \
      go install github.com/tc-hib/go-winres@v0.3.3 && \
      VER=\$(cat VERSION) && \
      go-winres make --in winres/winres.json --out cmd/quil/rsrc --product-version \$VER --file-version \$VER && \
      go-winres make --in winres/winres.json --out cmd/quild/rsrc --product-version \$VER --file-version \$VER && \
      F=\"-s -w -X main.version=\$VER\" && \
      F_DEV=\"\$F -X main.buildDevMode=true -X main.buildLogLevel=debug -X main.daemonBinary=quild-dev -X main.buildUpdatesOff=true\" && \
      F_DBG=\"\$F -X main.buildLogLevel=debug -X main.daemonBinary=quild-debug -X main.buildUpdatesOff=true\" && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$F_DEV\" -o quil-dev.exe    ./cmd/quil  && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$F_DEV\" -o quild-dev.exe   ./cmd/quild && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$F_DBG\" -o quil-debug.exe  ./cmd/quil  && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$F_DBG\" -o quild-debug.exe ./cmd/quild && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$F\"     -o quil.exe        ./cmd/quil  && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$F\"     -o quild.exe       ./cmd/quild"
    ;;

  test)
    $DOCKER_RUN go test ./...
    ;;

  test-race)
    $DOCKER_RUN sh -c \
      "apk add --no-cache gcc musl-dev && CGO_ENABLED=1 go test -race ./..."
    ;;

  vet)
    $DOCKER_RUN go vet ./...
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
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/quild-windows-amd64.exe ./cmd/quild"
    ;;

  image)
    docker build -t quil:latest "$PROJECT_DIR"
    ;;

  clean)
    # Same reason as build: rm cannot remove a held executable, and `set -e`
    # would abort the cleanup partway through.
    refuse_if_binaries_held
    rm -f "$PROJECT_DIR/quil" "$PROJECT_DIR/quild" \
          "$PROJECT_DIR/quil.exe" "$PROJECT_DIR/quild.exe" \
          "$PROJECT_DIR/quil-dev.exe" "$PROJECT_DIR/quild-dev.exe" \
          "$PROJECT_DIR/quil-debug.exe" "$PROJECT_DIR/quild-debug.exe"
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
    echo "  build          Build all variants: prod, dev, debug (6 binaries)"
    echo "  test           Run all tests"
    echo "  test-race      Run tests with race detector"
    echo "  vet            Run go vet"
    echo "  cross          Cross-compile for all platforms"
    echo "  image          Build Docker image (scratch-based)"
    echo "  clean          Remove built binaries"
    echo "  docs-size      Check .claude/ agent-context files against size limits"
    ;;
esac
