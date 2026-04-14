#!/usr/bin/env bash
set -euo pipefail

# Docker-based development commands — no local Go required.

GO_IMAGE="golang:1.25-alpine"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"
DOCKER_RUN="docker run --rm -v ${PROJECT_DIR}:/src -v quil-gomod:/go/pkg/mod -w //src ${GO_IMAGE}"

case "${1:-help}" in
  build)
    $DOCKER_RUN sh -c "\
      go install github.com/tc-hib/go-winres@v0.3.3 && \
      VER=\$(cat VERSION) && \
      go-winres make --in winres/winres.json --out cmd/quil/rsrc --product-version \$VER --file-version \$VER && \
      go-winres make --in winres/winres.json --out cmd/quild/rsrc --product-version \$VER --file-version \$VER && \
      F=\"-s -w -X main.version=\$VER\" && \
      F_DEV=\"\$F -X main.buildDevMode=true -X main.buildLogLevel=debug -X main.daemonBinary=quild-dev\" && \
      F_DBG=\"\$F -X main.buildLogLevel=debug -X main.daemonBinary=quild-debug\" && \
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
    rm -f "$PROJECT_DIR/quil" "$PROJECT_DIR/quild" \
          "$PROJECT_DIR/quil.exe" "$PROJECT_DIR/quild.exe" \
          "$PROJECT_DIR/quil-dev.exe" "$PROJECT_DIR/quild-dev.exe" \
          "$PROJECT_DIR/quil-debug.exe" "$PROJECT_DIR/quild-debug.exe"
    rm -f "$PROJECT_DIR"/cmd/quil/rsrc*.syso "$PROJECT_DIR"/cmd/quild/rsrc*.syso
    rm -rf "$PROJECT_DIR/dist/"
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
    ;;
esac
