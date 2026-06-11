#!/bin/sh
set -eu

# Quil installer — detects OS/arch, downloads the latest release, verifies checksum, installs.
# Usage: curl -sSfL https://raw.githubusercontent.com/artyomsv/quil/master/scripts/install.sh | sh

REPO="artyomsv/quil"
INSTALL_DIR="${QUIL_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${QUIL_VERSION:-}"

main() {
  detect_platform
  fetch_latest_version
  download_and_verify
  install_binaries
  print_success
}

detect_platform() {
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)

  case "$OS" in
    linux)  OS="linux" ;;
    darwin) OS="darwin" ;;
    *)
      echo "Error: unsupported OS: $OS" >&2
      echo "Download manually from https://github.com/$REPO/releases/latest" >&2
      exit 1
      ;;
  esac

  case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
      echo "Error: unsupported architecture: $ARCH" >&2
      exit 1
      ;;
  esac

  echo "Detected platform: ${OS}/${ARCH}"
}

fetch_latest_version() {
  if [ -n "$VERSION" ]; then
    echo "Using pinned version: v${VERSION}"
    return
  fi

  echo "Fetching latest release..."
  RESPONSE=$(curl -sSf ${GITHUB_TOKEN:+-H "Authorization: token $GITHUB_TOKEN"} \
    "https://api.github.com/repos/$REPO/releases/latest") || {
    echo "Error: failed to fetch latest release (GitHub API may be rate-limiting)" >&2
    echo "Set GITHUB_TOKEN or use QUIL_VERSION=x.y.z to skip API call" >&2
    exit 1
  }
  VERSION=$(echo "$RESPONSE" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

  if [ -z "$VERSION" ]; then
    echo "Error: could not determine latest version" >&2
    echo "Check https://github.com/$REPO/releases" >&2
    exit 1
  fi

  echo "Latest version: v${VERSION}"
}

download_and_verify() {
  ARCHIVE="quil_${VERSION}_${OS}_${ARCH}.tar.gz"
  BASE_URL="https://github.com/$REPO/releases/download/v${VERSION}"
  TMP_DIR=$(mktemp -d)
  trap 'rm -rf "$TMP_DIR"' EXIT

  echo "Downloading ${ARCHIVE}..."
  curl -sSfL -o "$TMP_DIR/$ARCHIVE" "$BASE_URL/$ARCHIVE"
  curl -sSfL -o "$TMP_DIR/checksums.txt" "$BASE_URL/checksums.txt"

  echo "Verifying checksum..."
  EXPECTED=$(grep "$ARCHIVE" "$TMP_DIR/checksums.txt" | awk '{print $1}')
  if [ -z "$EXPECTED" ]; then
    echo "Error: checksum not found for $ARCHIVE in checksums.txt" >&2
    exit 1
  fi
  ACTUAL=$(cd "$TMP_DIR" && { sha256sum "$ARCHIVE" 2>/dev/null || shasum -a 256 "$ARCHIVE"; } | awk '{print $1}')
  if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "Error: checksum mismatch" >&2
    echo "  expected: $EXPECTED" >&2
    echo "  got:      $ACTUAL" >&2
    exit 1
  fi
  echo "Checksum verified."

  echo "Extracting..."
  tar -xzf "$TMP_DIR/$ARCHIVE" -C "$TMP_DIR"
}

install_binaries() {
  mkdir -p "$INSTALL_DIR"
  # Stage each binary as a temp file inside INSTALL_DIR, then mv it into
  # place: each binary is swapped in atomically via rename and lands on a
  # NEW inode (the two binaries are swapped one after the other — the pair
  # is not replaced transactionally). Overwriting an existing binary in
  # place with cp reuses the inode, and macOS caches code-signing info per
  # inode (vnode) — a stale cache entry makes the kernel SIGKILL the new
  # binary at exec time ("zsh: killed quil", "Code Signature Invalid" in
  # the crash report). The temp files live inside INSTALL_DIR so the
  # rename never crosses filesystems; the EXIT trap removes them if the
  # install aborts between staging and rename.
  QUIL_TMP=$(mktemp "$INSTALL_DIR/.quil.tmp.XXXXXX")
  QUILD_TMP=$(mktemp "$INSTALL_DIR/.quild.tmp.XXXXXX")
  trap 'rm -rf "$TMP_DIR"; rm -f "$QUIL_TMP" "$QUILD_TMP"' EXIT
  cp "$TMP_DIR/quil" "$QUIL_TMP"
  cp "$TMP_DIR/quild" "$QUILD_TMP"
  chmod 755 "$QUIL_TMP" "$QUILD_TMP"
  mv -f "$QUIL_TMP" "$INSTALL_DIR/quil"
  mv -f "$QUILD_TMP" "$INSTALL_DIR/quild"
}

print_success() {
  echo ""
  echo "Installed quil v${VERSION} to ${INSTALL_DIR}/"
  echo "  ${INSTALL_DIR}/quil"
  echo "  ${INSTALL_DIR}/quild"

  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      echo ""
      echo "Add ${INSTALL_DIR} to your PATH:"
      echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
      echo ""
      echo "Add this line to your ~/.bashrc or ~/.zshrc to make it permanent."
      ;;
  esac

  echo ""
  echo "Run 'quil' to get started."
}

# QUIL_INSTALLER_NO_MAIN=1 lets tests source this file for its functions
# without running the installer (see scripts/test-install.sh).
[ "${QUIL_INSTALLER_NO_MAIN:-0}" = "1" ] || main
