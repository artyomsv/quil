# Reports what this host is and whether quil is already installed.
#
# ALWAYS exits 0. The exit code of this command is how the caller tells an ssh
# failure from a probe finding, so "connected fine, found nothing" and "never
# connected" must not look alike. There is deliberately no `set -e`.
#
# Output contract — exactly five lines, in order, positional:
#
#   1  $HOME
#   2  uname -s            (or -)
#   3  uname -m            (or -)
#   4  existing quil path  (or -)
#   5  rw | ro | -         (is that path's directory writable)
#
# Anything printed after line five is ignored by the parser, so a host with a
# chatty rc file still probes cleanly.

h=${HOME:-}
printf '%s\n' "$h"
uname -s 2>/dev/null || printf '%s\n' -
uname -m 2>/dev/null || printf '%s\n' -

# Absolute paths FIRST, and PATH only as a last resort. This runs in a
# non-interactive shell, which on Debian and Ubuntu returns from ~/.bashrc
# before reaching any PATH line, so ~/.local/bin is normally invisible here —
# that is the whole reason this feature exists. A PATH-only lookup would report
# "not installed" for a host that has quil sitting right there.
found=
for p in "$h/.local/bin/quil" /usr/local/bin/quil; do
  if [ -x "$p" ]; then
    found=$p
    break
  fi
done
if [ -z "$found" ]; then
  c=$(command -v quil 2>/dev/null)
  if [ -n "$c" ]; then
    found=$c
  fi
fi

if [ -z "$found" ]; then
  printf '%s\n%s\n' - -
else
  printf '%s\n' "$found"
  d=$(dirname "$found")
  # Only a plain "rw" is read as permission to write. Any other answer — including
  # one this script could not determine — makes the caller fall back to ~/.local/bin
  # rather than attempt a write it cannot perform without sudo.
  if [ -w "$d" ]; then
    printf 'rw\n'
  else
    printf 'ro\n'
  fi
fi

exit 0
