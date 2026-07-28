# Reports what this host is and whether quil is already installed.
#
# ALWAYS exits 0. The exit code of this command is how the caller tells an ssh
# failure from a probe finding, so "connected fine, found nothing" and "never
# connected" must not look alike. There is deliberately no `set -e`.
#
# Output contract — a sentinel line, then exactly five lines, in order:
#
#   __quil_probe__
#   1  $HOME
#   2  uname -s            (or -)
#   3  uname -m            (or -)
#   4  existing quil path  (or -)
#   5  rw | ro | -         (is that path's directory safely writable)
#
# The sentinel exists because positional parsing from line 0 breaks on anything
# the remote shell prints BEFORE us: ~/.zshenv is read for every zsh invocation
# including non-interactive ones, ~/.profile may echo, and an rc file touching
# stty emits "Inappropriate ioctl". Any of those shifts every field by one and
# yields a diagnosis pointing at something unrelated. Lines after the five are
# ignored, so trailing noise is harmless too.

printf '__quil_probe__\n'

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
  # "rw" means BOTH writable by us AND not writable by group or other.
  #
  # Plain [ -w ] is not enough. This directory is adopted as the install target
  # for an in-place upgrade and then recorded locally and executed as the ssh
  # remote command on every later attach, with no further prompt. A shared
  # /opt/bin, a group-writable /usr/local/bin (the Homebrew default on
  # multi-admin macOS), or "." on PATH would let another local user on that host
  # replace the binary afterwards. Reporting "ro" instead just routes us to
  # ~/.local/bin, which is the safe branch and already handled.
  #
  # Anything other than a plain "rw" is read as not-writable by the parser, so a
  # find(1) that is missing or errors degrades safely.
  if [ -w "$d" ] && [ -z "$(find "$d" -maxdepth 0 -perm -0022 2>/dev/null)" ]; then
    printf 'rw\n'
  else
    printf 'ro\n'
  fi
fi

exit 0
