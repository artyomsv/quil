#!/usr/bin/env sh
# Fetch the bundled ConPTY host (conpty.dll + OpenConsole.exe, x64) that the
# Windows build embeds via go:embed (internal/pty/winconpty/embed_windows.go).
#
# Source: Microsoft.Windows.Console.ConPTY NuGet redistributable (MIT) — the
# same OpenConsole that Windows Terminal ships. Needed because the Windows 10
# inbox conhost mis-renders claude-code's incremental input. The binaries are
# gitignored (*.dll / *.exe), so the build fetches them on demand.
#
# The downloaded binaries are pinned by SHA256 — a build-time supply-chain gate
# so a poisoned package/CDN/TLS path cannot inject native code into the release.
# Keep VER and the two hashes in sync with bundledVersion in embed_windows.go.
set -eu

VER="${1:-1.24.260512001}"
EXPECTED_DLL="c46dcd04f52b97f6a8cf53e8f547c85a821660bed18de2b3344afcd4a8389ad6"
EXPECTED_EXE="47828c3fe080212f69dfdb39ab3673170fcc7445924c76fe003cefd18247dd5d"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/internal/pty/winconpty/bins"

if [ -s "$DEST/conpty.dll" ] && [ -s "$DEST/OpenConsole.exe" ]; then
  echo "conpty: bins already present in $DEST; skipping fetch"
  exit 0
fi

mkdir -p "$DEST"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
URL="https://api.nuget.org/v3-flatcontainer/microsoft.windows.console.conpty/$VER/microsoft.windows.console.conpty.$VER.nupkg"
echo "conpty: fetching $VER"
curl -fsSL "$URL" -o "$TMP/c.nupkg"
unzip -o -q "$TMP/c.nupkg" -d "$TMP/x"

DLL_SRC="$TMP/x/runtimes/win-x64/native/conpty.dll"
EXE_SRC="$TMP/x/build/native/runtimes/x64/OpenConsole.exe"

# Verify SHA256 before trusting the binaries. sha256sum -c wants "<hash>  <file>".
echo "$EXPECTED_DLL  $DLL_SRC" | sha256sum -c - >/dev/null 2>&1 ||
  { echo "conpty: SHA256 mismatch for conpty.dll — refusing to bundle" >&2; exit 1; }
echo "$EXPECTED_EXE  $EXE_SRC" | sha256sum -c - >/dev/null 2>&1 ||
  { echo "conpty: SHA256 mismatch for OpenConsole.exe — refusing to bundle" >&2; exit 1; }

cp "$DLL_SRC" "$DEST/conpty.dll"
cp "$EXE_SRC" "$DEST/OpenConsole.exe"
echo "conpty: staged + verified conpty.dll + OpenConsole.exe ($VER) -> $DEST"
