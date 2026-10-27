# Moves binaries staged by remote-stage.sh into place.
#
#   $1  target directory
#   $2  staging directory (from remote-stage.sh's output)
#
# Each mv is a rename within one filesystem — the staging directory lives
# inside the target — so each binary is replaced atomically and lands on a NEW
# inode. That matters on macOS: overwriting in place reuses the inode, and
# code-signing information is cached per vnode, so a stale entry makes the
# kernel SIGKILL the new binary at exec time ("Code Signature Invalid").
#
# Renaming over a RUNNING binary is safe on Unix: the running process keeps its
# inode.

set -eu

DIR=$1
ST=$2

# The caller checks the shape too; this is the last line of defence against a
# path that would make the rm below reach outside the install directory.
case $ST in
  "$DIR"/.quil-staging-*/* | *..*)
    echo "quil-install: $ST is not a staging directory under $DIR" >&2
    exit 2
    ;;
  "$DIR"/.quil-staging-?*) ;;
  *)
    echo "quil-install: $ST is not a staging directory under $DIR" >&2
    exit 2
    ;;
esac

if [ ! -f "$ST/quil" ] || [ ! -f "$ST/quild" ]; then
  echo "quil-install: staged binaries are missing from $ST" >&2
  exit 3
fi

mv -f "$ST/quil" "$DIR/quil"
mv -f "$ST/quild" "$DIR/quild"
rm -rf "$ST"

echo "quil-install: installed to $DIR"
