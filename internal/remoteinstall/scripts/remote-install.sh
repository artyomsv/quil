# Installs quil from a release archive delivered on stdin.
#
#   $1  target directory
#   $2  expected sha256 of the archive
#
# The archive occupies stdin, so this script cannot arrive there too — the
# caller passes it as an argument to `sh -c`. See InstallCommand.

set -eu

DIR=$1
WANT=$2

TMP=$(mktemp -d)
QT=
DT=
# Also removes the staging temp files: they live inside DIR so the rename below
# never crosses a filesystem, and an abort between staging and rename would
# otherwise leave dotfiles in the user's bin directory.
trap 'rm -rf "$TMP"; [ -z "$QT" ] || rm -f "$QT"; [ -z "$DT" ] || rm -f "$DT"; true' EXIT INT TERM HUP

cat > "$TMP/quil.tar.gz"

# Re-verify what actually arrived. The caller already checked this archive
# against the release checksums before sending a byte, so a mismatch here means
# the transfer was truncated or corrupted in flight rather than that the release
# is wrong. sha256sum is GNU/busybox; shasum is what macOS ships.
GOT=$({ sha256sum "$TMP/quil.tar.gz" 2>/dev/null || shasum -a 256 "$TMP/quil.tar.gz"; } | awk '{print $1}')
if [ "$GOT" != "$WANT" ]; then
  echo "quil-install: archive checksum mismatch (transfer corrupted)" >&2
  exit 2
fi

tar -xzf "$TMP/quil.tar.gz" -C "$TMP"
mkdir -p "$DIR"

# Stage inside DIR, then rename. Each binary lands on a NEW inode, which matters
# on macOS: overwriting in place with cp reuses the inode, and code-signing
# information is cached per vnode — a stale entry makes the kernel SIGKILL the
# new binary at exec time ("Code Signature Invalid"). Same reasoning, and the
# same shape, as scripts/install.sh.
#
# Renaming over a RUNNING binary is safe on Unix: the running process keeps its
# inode. That is what lets an upgrade replace quild while the old daemon is
# still shutting down.
QT=$(mktemp "$DIR/.quil.tmp.XXXXXX")
DT=$(mktemp "$DIR/.quild.tmp.XXXXXX")
cp "$TMP/quil" "$QT"
cp "$TMP/quild" "$DT"
chmod 755 "$QT" "$DT"
mv -f "$QT" "$DIR/quil"
mv -f "$DT" "$DIR/quild"
QT=
DT=

echo "quil-install: installed to $DIR"
