#!/usr/bin/env bash
set -euo pipefail

# Docker-based development commands — no local Go required.

GO_IMAGE="golang:1.25-alpine"
PROJECT_DIR="E:/Projects/Stukans/Prototypes/calyx"
DOCKER_RUN="docker run --rm -v ${PROJECT_DIR}:/src -v aethel-gomod:/go/pkg/mod -w //src ${GO_IMAGE}"

case "${1:-help}" in
  build)
    $DOCKER_RUN sh -c \
      "VER=\$(cat VERSION) && LDFLAGS=\"-X main.version=\$VER\" && GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o aethel.exe ./cmd/aethel && GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o aetheld.exe ./cmd/aetheld"
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
      VER=\$(cat VERSION) && LDFLAGS=\"-X main.version=\$VER\" && \
      mkdir -p dist && \
      GOOS=linux   GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/aethel-linux-amd64        ./cmd/aethel && \
      GOOS=linux   GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/aetheld-linux-amd64       ./cmd/aetheld && \
      GOOS=linux   GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/aethel-linux-arm64        ./cmd/aethel && \
      GOOS=linux   GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/aetheld-linux-arm64       ./cmd/aetheld && \
      GOOS=darwin  GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/aethel-darwin-amd64       ./cmd/aethel && \
      GOOS=darwin  GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/aetheld-darwin-amd64      ./cmd/aetheld && \
      GOOS=darwin  GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/aethel-darwin-arm64       ./cmd/aethel && \
      GOOS=darwin  GOARCH=arm64 go build -ldflags \"\$LDFLAGS\" -o dist/aetheld-darwin-arm64      ./cmd/aetheld && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/aethel-windows-amd64.exe  ./cmd/aethel && \
      GOOS=windows GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o dist/aetheld-windows-amd64.exe ./cmd/aetheld"
    ;;

  image)
    docker build -t aethel:latest "$PROJECT_DIR"
    ;;

  clean)
    rm -f "$PROJECT_DIR/aethel" "$PROJECT_DIR/aetheld" \
          "$PROJECT_DIR/aethel.exe" "$PROJECT_DIR/aetheld.exe"
    rm -rf "$PROJECT_DIR/dist/"
    ;;

  help|*)
    echo "Usage: ./dev.sh <command>"
    echo ""
    echo "Commands:"
    echo "  build          Build TUI binaries (aethel + aetheld)"
    echo "  test           Run all tests"
    echo "  test-race      Run tests with race detector"
    echo "  vet            Run go vet"
    echo "  cross          Cross-compile for all platforms"
    echo "  image          Build Docker image (scratch-based)"
    echo "  clean          Remove built binaries"
    ;;
esac
