#!/bin/sh
set -eu

# Regression test for install_binaries() in install.sh.
#
# Guards the macOS stale code-sign cache fix: every install must land the
# binaries on FRESH inodes (staged temp file + mv, never an in-place cp —
# macOS caches code-signing info per inode and SIGKILLs a binary whose
# inode was overwritten) and must leave no staged temp files behind in the
# install directory.
#
# No network needed — fake binaries stand in for a release archive.

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

QUIL_INSTALL_DIR="$WORK/bin"
export QUIL_INSTALLER_NO_MAIN=1
# shellcheck source=install.sh
. "$SCRIPT_DIR/install.sh"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

inode_of() {
  ls -i "$1" | awk '{print $1}'
}

# Simulate one installer run: stage fake binaries the way
# download_and_verify would, then invoke install_binaries.
run_install() {
  TMP_DIR=$(mktemp -d "$WORK/stage.XXXXXX")
  printf 'fake-quil-%s' "$1" > "$TMP_DIR/quil"
  printf 'fake-quild-%s' "$1" > "$TMP_DIR/quild"
  install_binaries
  # install_binaries installs its own EXIT trap; restore the test's one.
  trap 'rm -rf "$WORK"' EXIT
  rm -rf "$TMP_DIR"
}

run_install one
INODE1_QUIL=$(inode_of "$QUIL_INSTALL_DIR/quil")
INODE1_QUILD=$(inode_of "$QUIL_INSTALL_DIR/quild")

run_install two
INODE2_QUIL=$(inode_of "$QUIL_INSTALL_DIR/quil")
INODE2_QUILD=$(inode_of "$QUIL_INSTALL_DIR/quild")

[ "$INODE1_QUIL" != "$INODE2_QUIL" ] || \
  fail "quil inode unchanged after re-install ($INODE1_QUIL) — in-place overwrite reintroduced"
[ "$INODE1_QUILD" != "$INODE2_QUILD" ] || \
  fail "quild inode unchanged after re-install ($INODE1_QUILD) — in-place overwrite reintroduced"

[ "$(cat "$QUIL_INSTALL_DIR/quil")" = "fake-quil-two" ] || \
  fail "quil content is not from the latest install"
[ "$(cat "$QUIL_INSTALL_DIR/quild")" = "fake-quild-two" ] || \
  fail "quild content is not from the latest install"

[ -x "$QUIL_INSTALL_DIR/quil" ] || fail "quil is not executable"
[ -x "$QUIL_INSTALL_DIR/quild" ] || fail "quild is not executable"

LEFTOVERS=$(find "$QUIL_INSTALL_DIR" -name '.*.tmp.*' | wc -l)
[ "$LEFTOVERS" -eq 0 ] || fail "$LEFTOVERS staged temp file(s) left in install dir"

echo "PASS: re-install lands on fresh inodes (quil $INODE1_QUIL -> $INODE2_QUIL," \
  "quild $INODE1_QUILD -> $INODE2_QUILD), no temp litter, executables in place"
