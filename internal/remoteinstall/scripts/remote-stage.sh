# Stages quil from a release archive delivered on stdin: receives it, checks
# it, and unpacks it into a fresh staging directory INSIDE the install
# directory. Nothing the host runs is touched — remote-commit.sh does that, and
# only after the old daemon has been stopped.
#
#   $1  target directory
#   $2  expected sha256 of the archive
#
# Output contract on success: the line __quil_staged__, then the staging path.
#
# Why staging and install are two steps: the daemon has to be stopped between
# them. Stopping first and then failing to write (a full disk, a broken link)
# killed every pane on the host for an upgrade that never happened. Staging
# first means every likely failure is over before anything is stopped.
#
# Why INSIDE the target directory, never $TMPDIR or /tmp: the final step is a
# rename, which is atomic only within one filesystem, and the target is the one
# place that must have room anyway. /tmp is often a small tmpfs, sometimes with
# a per-user quota — a host whose /tmp was full failed every upgrade with
# "cat: write error: Disk quota exceeded" while its home had 200 GB free. This
# is the same shape the Windows install already uses (remote-prepare.ps1).
#
# The archive occupies stdin, so this script cannot arrive there too — the
# caller passes it as an argument to `sh -c`. See StageCommand.

set -eu

DIR=$1
WANT=$2

mkdir -p "$DIR"

# Staging dirs a failed or interrupted install left behind. Each holds a full
# copy of the binaries, so leaving them would slowly fill the very directory
# this script needs room in.
#
# Only dirs untouched for over an hour are ABANDONED. A younger one may belong
# to another client's upgrade that has staged and is stopping the daemon right
# now; removing it makes that upgrade's commit fail after its stop — the exact
# failure the staging exists to prevent. One whole setup is bounded at 10
# minutes (remoteSetupTimeout), so an hour cannot catch a live one. A dir's
# mtime is its last entry change, which for a finished stage is the moment the
# archive was removed. -mmin and -maxdepth are not POSIX but GNU, busybox and
# BSD find all have them, and the probe already relies on -maxdepth.
find "$DIR" -maxdepth 1 -type d -name '.quil-staging-*' -mmin +60 \
  -exec rm -rf {} + 2>/dev/null || true

ST=$(mktemp -d "$DIR/.quil-staging-XXXXXXXXXX")
# Removed on every failure. Disarmed (ST=) only once the stage is complete,
# because a complete stage is exactly what remote-commit.sh is about to use.
trap '[ -z "$ST" ] || rm -rf "$ST"; true' EXIT INT TERM HUP

cat > "$ST/quil.tar.gz"

# Re-verify what actually arrived. The caller already checked this archive
# against the release checksums before sending a byte, so a mismatch here means
# the transfer was truncated or corrupted in flight rather than that the release
# is wrong. sha256sum is GNU/busybox; shasum is what macOS ships.
GOT=$({ sha256sum "$ST/quil.tar.gz" 2>/dev/null || shasum -a 256 "$ST/quil.tar.gz"; } | awk '{print $1}')
if [ "$GOT" != "$WANT" ]; then
  echo "quil-install: archive checksum mismatch (transfer corrupted)" >&2
  exit 2
fi

# Extracted from INSIDE the staging dir rather than with -C: GNU tar reads a
# "host:path" archive name as a remote one, so a path with a colon in it (a
# Windows drive under Git for Windows' sh, where this suite also runs) fails.
(cd "$ST" && tar -xzf quil.tar.gz)
rm -f "$ST/quil.tar.gz"

for b in quil quild; do
  if [ ! -f "$ST/$b" ]; then
    echo "quil-install: the archive holds no $b" >&2
    exit 3
  fi
done
chmod 755 "$ST/quil" "$ST/quild"

printf '__quil_staged__\n%s\n' "$ST"
ST=
